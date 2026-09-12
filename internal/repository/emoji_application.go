package repository

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/shiroha-a/mk/internal/model"
)

// Emoji application filter values accepted by EmojiApplicationRepository.List.
//
// Declared as plain string constants so callers (admin handlers, testutil
// mocks) can pass literals without depending on this package's type system.
const (
	// EmojiApplicationFilterAll returns every application.
	EmojiApplicationFilterAll = "all"
	// EmojiApplicationFilterPending returns applications awaiting review.
	EmojiApplicationFilterPending = "pending"
	// EmojiApplicationFilterProcessed returns applications that no longer need
	// action (approved / rejected / canceled).
	EmojiApplicationFilterProcessed = "processed"
)

// ErrEmojiApplicationDuplicatePending is returned when the applicant already
// has a pending application under the same name.
//
// **番兵で区別する。** 一意制約違反をそのまま 500 にすると、利用者には
// 「サーバーエラー」としか見えず、二重送信なのか障害なのか分からない。
var ErrEmojiApplicationDuplicatePending = errors.New("emoji application already pending for this name")

// EmojiApplicationRepository reads and writes `emoji_application` rows (#2934).
type EmojiApplicationRepository interface {
	// Create bypasses the rolling windows. **申請の作成は CreateWithQuota を
	// 使うこと (#2958)。** こちらは上限が 1 つも設定されていないときの委譲先
	// とテストの seed 用。
	Create(app *model.EmojiApplication) error
	FindByID(id string) (*model.EmojiApplication, error)
	// List returns applications newest first. filter is one of the
	// EmojiApplicationFilter* constants; anything else is treated as "all".
	List(filter string, limit int, untilID string) ([]model.EmojiApplication, error)
	ListByUser(userID string, limit int, untilID string) ([]model.EmojiApplication, error)
	CountPending() (int64, error)
	// UpdateIfPending writes the row only while it is still pending, and
	// reports whether it did.
	//
	// **条件付き UPDATE でないと last-write-wins になる (#2934 レビュー M1)。**
	// 読んでから書くだけだと、2 人のモデレーターが同じ申請を開いていた場合に
	// 後の書き込みが前を上書きする — 承認で emoji を作った直後に却下が被さり、
	// **絵文字は存在するのに申請は「却下」で emojiId も消える**。窓は「一覧を
	// 開いてから押すまで」の全期間なので、実際に起きる。
	UpdateIfPending(app *model.EmojiApplication) (bool, error)
	// CreateWithQuota inserts the row only while every limit still has room:
	// the rolling windows (#2958) and the awaiting-review cap (#2977).
	//
	// **COUNT してから INSERT では足りない。** 同じ利用者から同時に来た
	// リクエストが両方とも「空きあり」を読んで両方 INSERT できる。数えるのも
	// 作るのも 1 つのトランザクションに入れ、**利用者単位のアドバイザリロック**
	// で直列化する (行ロックでは「まだ無い行」を守れない)。
	//
	// limits の内訳は QuotaLimits を参照。収まらなければ
	// *QuotaExceededError / *PendingLimitExceededError を返し、行は作らない。
	CreateWithQuota(app *model.EmojiApplication, limits QuotaLimits) error
}

// QuotaLimits collects every per-user limit checked before an application is
// created. ゼロ値は「上限なし」。
type QuotaLimits struct {
	// Windows はローリング期間の上限 (#2958)。**全ステータスを数える**ので、
	// 却下・取り下げでも枠は戻らない。上限 0 の窓は無制限として飛ばす。
	Windows []QuotaWindow
	// MaxPending は同時に審査待ちにできる件数 (#2977)。0 は無制限。
	//
	// **Windows とは数え方が逆で、絞っているものも違う。** こちらは
	// `status = 'pending'` だけを数えるので**却下・取り下げで枠が戻る**。
	// Windows が絞るのは「出せる総量」、こちらが絞るのは「モデレーターが
	// 見る一覧の長さ」で、片方だけでは両方を制御できない。
	MaxPending int
}

// QuotaWindow is one rolling limit: "過去 Duration に Max 件まで" (#2958)。
type QuotaWindow struct {
	// Name は API が返す期間の識別子 ("day" / "week" / "month")。
	Name     string
	Duration time.Duration
	Max      int
}

// QuotaExceededError reports which rolling window was full.
type QuotaExceededError struct {
	Window QuotaWindow
	Used   int
	// RetryAt は**申請が通るようになる時刻**。窓が複数あるときは、いちばん
	// 遅く空くものに揃える (どれか 1 つでも満杯なら申請は通らない)。
	//
	// その窓の中では「`used - Max` 件飛ばした行が窓を出る時刻」。**最古では
	// 足りない** — `used > Max` のときは 1 件抜けても `used-1 >= Max` のまま。
	//
	// **審査待ちの上限 (#2977) も満杯ならゼロ値になる。** そのとき通るのは
	// モデレーターが処理した後で、時刻を予告できない。呼び出し元はゼロ値なら
	// `retryAt` も `Retry-After` も出さないこと。
	RetryAt time.Time
}

func (e *QuotaExceededError) Error() string {
	return "emoji application quota exceeded for window " + e.Window.Name
}

// PendingLimitExceededError reports that too many applications from the same
// user are still awaiting review (#2977).
//
// **RetryAt を持たない。** 空くのはモデレーターが処理したときで、時刻を
// 予告できない。`QuotaExceededError` と同じ形にして `Retry-After` を付けると
// 「待てば通る」と誤解させる。利用者が取れる行動は取り下げか、結果を待つか。
type PendingLimitExceededError struct {
	Used  int
	Limit int
}

func (e *PendingLimitExceededError) Error() string {
	return "too many emoji applications awaiting review"
}

type emojiApplicationRepository struct {
	db *gorm.DB
}

// NewEmojiApplicationRepository constructs the GORM-backed repository.
func NewEmojiApplicationRepository(db *gorm.DB) EmojiApplicationRepository {
	return &emojiApplicationRepository{db: db}
}

// Create inserts an application without checking any rolling window.
//
// **新しい呼び出し元は `CreateWithQuota` を使うこと (#2958)。** こちらを直に
// 呼ぶとロール別の期間上限を素通りする。残してあるのは、上限が 1 つも
// 設定されていないときに `CreateWithQuota` が委譲する先だから。
func (r *emojiApplicationRepository) Create(app *model.EmojiApplication) error {
	err := r.db.Create(app).Error
	if err == nil {
		return nil
	}
	// 部分一意索引 (userId, name) WHERE status = 'pending' の違反だけを
	// 区別する。ほかの制約違反は素通しして呼び出し側で 500 にする。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "IDX_emoji_application_pending_name" {
		return ErrEmojiApplicationDuplicatePending
	}
	return err
}

// quotaLockNamespace keeps the advisory lock key from colliding with other
// features that lock on the same user id.
const quotaLockNamespace = 2958

func (r *emojiApplicationRepository) CreateWithQuota(app *model.EmojiApplication, limits QuotaLimits) error {
	active := make([]QuotaWindow, 0, len(limits.Windows))
	for _, w := range limits.Windows {
		if w.Max > 0 && w.Duration > 0 {
			active = append(active, w)
		}
	}
	if len(active) == 0 && limits.MaxPending <= 0 {
		return r.Create(app)
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		// **利用者単位で直列化する。** 行ロックだと「これから作る行」を
		// 守れないので、アドバイザリロックで数える側ごと囲う。
		// トランザクション版なので commit / rollback で自動的に解放される。
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(?, hashtext(?))`,
			quotaLockNamespace, app.UserID).Error; err != nil {
			return err
		}
		now := app.CreatedAt
		if now.IsZero() {
			now = time.Now()
		}
		// **審査待ちの件数は先に数えるが、返すかどうかは窓の結果で決める。**
		// 両方満杯のときに審査待ちを返すと「取り下げれば出せる」と案内することに
		// なるが、**期間上限は全ステータスを数えるので取り下げた行は枠を占有した
		// まま戻らない** — 案内に従うと申請を 1 件失ったうえに枠も消費する。
		var pending int64
		if limits.MaxPending > 0 {
			if err := tx.Model(&model.EmojiApplication{}).
				Where(`"userId" = ? AND "status" = ?`, app.UserID, model.EmojiApplicationPending).
				Count(&pending).Error; err != nil {
				return err
			}
		}
		pendingFull := limits.MaxPending > 0 && pending >= int64(limits.MaxPending)

		// **満杯の窓を全部評価する。** 最初に見つけたもので返すと、2 つ以上が
		// 同時に満杯のときに早すぎる時刻を案内することになる (実測で 78 時間
		// ずれた)。申請が通るのは全部の窓に空きができてからなので、いちばん
		// 遅く空くものを返す。行は減る一方なので、そこまで待てば他の窓にも
		// 空きがある。
		var worst *QuotaExceededError
		for _, w := range active {
			since := now.Add(-w.Duration)
			var used int64
			// **全ステータスを数える。** 却下・取り下げで枠が戻ると、申請と
			// 取り下げを繰り返して審査通知と履歴を大量に作れる。
			if err := tx.Model(&model.EmojiApplication{}).
				Where(`"userId" = ? AND "createdAt" >= ?`, app.UserID, since).
				Count(&used).Error; err != nil {
				return err
			}
			if used < int64(w.Max) {
				continue
			}
			// **「最古の 1 件」では足りない。** 空くのは used が Max を下回った
			// ときなので、`used - Max + 1` 件が窓を出るまで待つ必要がある。
			// 最古だけを見ると、上限を後から下げたときや上限の緩いロールを
			// 外したときに**広告した時刻に叩いてもまた弾かれる**。
			// offset は 0 起算なので `used - Max` 件飛ばした行が最後の 1 件。
			var freed []time.Time
			if err := tx.Model(&model.EmojiApplication{}).
				Select(`"createdAt"`).
				Where(`"userId" = ? AND "createdAt" >= ?`, app.UserID, since).
				Order(`"createdAt" ASC`).
				Offset(int(used - int64(w.Max))).
				Limit(1).
				Scan(&freed).Error; err != nil {
				return err
			}
			if len(freed) == 0 {
				// **fail-closed に倒す。** ここへ来るのは残り行数が `used - Max`
				// 以下のときで、「窓が空いた」とは限らない (used=10 / Max=3 なら
				// 7 行残っていても空になる)。COUNT を取り直さない以上、満杯の
				// まま通す側へは倒さない。`now` を置くと最大限保守的な時刻に
				// なる。**現状は到達しない** — `emoji_application` を消す
				// production 経路が無く、FK も張っていないので利用者削除の
				// CASCADE でも消えない。
				freed = []time.Time{now}
			}
			// **1ms 足す。** 窓の判定は `createdAt >= since` なので、境界ちょうど
			// の行はまだ窓の中にいる。しかも `entity.ISOMillis` は切り捨てなので、
			// 境界をそのまま広告すると**その値で再スケジュールするクライアントが
			// 必ず 1 回空振りする**。
			at := freed[0].Add(w.Duration + time.Millisecond)
			if worst == nil || at.After(worst.RetryAt) {
				worst = &QuotaExceededError{Window: w, Used: int(used), RetryAt: at}
			}
		}
		if worst != nil {
			if pendingFull {
				// **時刻を出さない。** `RetryAt` の契約は「申請が通るようになる
				// 時刻」だが、審査待ちも満杯ならその時刻でも通らない。審査待ちが
				// 空くのはモデレーターが処理したときで予告できないので、嘘の時刻を
				// 広告するより「いつ空くか分からない」と伝えるほうが正確。
				worst.RetryAt = time.Time{}
			}
			return worst
		}
		if pendingFull {
			return &PendingLimitExceededError{Used: int(pending), Limit: limits.MaxPending}
		}
		if err := tx.Create(app).Error; err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
				pgErr.ConstraintName == "IDX_emoji_application_pending_name" {
				return ErrEmojiApplicationDuplicatePending
			}
			return err
		}
		return nil
	})
}

func (r *emojiApplicationRepository) FindByID(id string) (*model.EmojiApplication, error) {
	var app model.EmojiApplication
	if err := r.db.Where(`"id" = ?`, id).First(&app).Error; err != nil {
		return nil, err
	}
	return &app, nil
}

func (r *emojiApplicationRepository) List(filter string, limit int, untilID string) ([]model.EmojiApplication, error) {
	q := r.db.Model(&model.EmojiApplication{})
	switch filter {
	case EmojiApplicationFilterPending:
		q = q.Where(`"status" = ?`, model.EmojiApplicationPending)
	case EmojiApplicationFilterProcessed:
		q = q.Where(`"status" <> ?`, model.EmojiApplicationPending)
	}
	return r.scanPage(q, limit, untilID)
}

func (r *emojiApplicationRepository) ListByUser(userID string, limit int, untilID string) ([]model.EmojiApplication, error) {
	q := r.db.Model(&model.EmojiApplication{}).Where(`"userId" = ?`, userID)
	return r.scanPage(q, limit, untilID)
}

// scanPage applies the shared ordering and keyset pagination.
//
// **id で切るのは createdAt で切らないため。** aidx は時刻順に単調なので同じ
// 並びになり、同一時刻の行を取りこぼさない。
func (r *emojiApplicationRepository) scanPage(q *gorm.DB, limit int, untilID string) ([]model.EmojiApplication, error) {
	if untilID != "" {
		q = q.Where(`"id" < ?`, untilID)
	}
	if limit <= 0 {
		limit = 30
	}
	var out []model.EmojiApplication
	if err := q.Order(`"id" DESC`).Limit(limit).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *emojiApplicationRepository) CountPending() (int64, error) {
	var n int64
	err := r.db.Model(&model.EmojiApplication{}).
		Where(`"status" = ?`, model.EmojiApplicationPending).
		Count(&n).Error
	return n, err
}

func (r *emojiApplicationRepository) UpdateIfPending(app *model.EmojiApplication) (bool, error) {
	// Save ではなく Updates + Where。Save は主キーだけで WHERE を組むので
	// 条件を足せない。
	res := r.db.Model(&model.EmojiApplication{}).
		Where(`"id" = ? AND "status" = ?`, app.ID, model.EmojiApplicationPending).
		Updates(map[string]any{
			"status":        app.Status,
			"emojiId":       app.EmojiID,
			"processedById": app.ProcessedByID,
			"processedAt":   app.ProcessedAt,
			"rejectReason":  app.RejectReason,
			"updatedAt":     app.UpdatedAt,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
