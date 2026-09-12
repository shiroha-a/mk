// Package emojiapplication implements the lifecycle of custom emoji
// registration requests (#2934).
//
// 承認までは `emoji` 行を作らない。詳細は migration/000086 と
// internal/model/emoji_application.go を参照。
package emojiapplication

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by Service. 呼び出し側 (API 層) が HTTP の形へ落とす。
var (
	// ErrInvalidName is returned when the requested name is not usable.
	ErrInvalidName = errors.New("invalid emoji name")
	// ErrLicenseRequired is returned when the license field is empty.
	ErrLicenseRequired = errors.New("license is required")
	// ErrFileRequired is returned when an "own" application has no file.
	ErrFileRequired = errors.New("file is required")
	// ErrInvalidKind is returned when the requested kind is not recognised.
	ErrInvalidKind = errors.New("invalid application kind")
	// ErrRemoteRequired is returned when a "remote" application has no host/name.
	ErrRemoteRequired = errors.New("remote host and name are required")
	// ErrRemoteFetchFailed is returned when the emoji image could not be stored.
	ErrRemoteFetchFailed = errors.New("failed to fetch remote emoji image")
	// ErrNoSuchRemoteEmoji is returned when the referenced remote emoji is not
	// known to this instance.
	//
	// **リモート絵文字の行はキャッシュに近い。** 使われなくなると消えるので、
	// 申請の時点で引けることを確かめる (審査時にもう一度取り直す)。
	ErrNoSuchRemoteEmoji = errors.New("no such remote emoji")
	// ErrDuplicateName is returned when a local emoji already uses the name.
	ErrDuplicateName = errors.New("duplicate emoji name")
	// ErrAlreadyPending is returned when the applicant already has a pending
	// application under the same name.
	ErrAlreadyPending = errors.New("application already pending")
	// ErrNotFound is returned when the application does not exist.
	ErrNotFound = errors.New("no such application")
	// ErrNotPending is returned when the application was already processed.
	ErrNotPending = errors.New("application is not pending")
	// ErrForbidden is returned when the caller does not own the application.
	ErrForbidden = errors.New("not the applicant")
	// ErrUnsupportedFileType is returned when the drive file is not an image
	// type the emoji pipeline accepts.
	ErrUnsupportedFileType = errors.New("unsupported file type")
	// ErrTooLong is returned when a field exceeds the column width.
	//
	// 列の長さを超えたまま DB へ渡すと SQLSTATE 22001 が生で返り、利用者の
	// 入力で 5xx が立つ。
	ErrTooLong = errors.New("field is too long")
	// ErrForeignFile is returned when the drive file belongs to someone else.
	//
	// **他人のファイルを指定させない (#2934 レビュー H2)。** 許すと (a) 応答に
	// 含まれる URL から他人のファイルを読める (`/files/:accessKey` は認証なしの
	// GET で、URL そのものが capability)、(b) 承認すると他人の画像がサーバーの
	// 絵文字として登録される。`core/user` の applyMediaUpdate が avatar / banner
	// に対して同じ検証をしている。
	ErrForeignFile = errors.New("drive file belongs to another user")
	// ErrFileGone is returned when the drive file backing the request was
	// deleted between applying and approving.
	//
	// 申請者が drive から消せるので普通に起きる。500 にしない。
	ErrFileGone = errors.New("drive file is gone")
)

// namePattern mirrors the constraint upstream's admin/emoji/add enforces.
//
// **申請側で先に弾く。** 承認まで通してから登録で落ちると、モデレーターが
// 押した後でエラーになり、申請者にも審査者にも何も残らない。
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// AllowedImageTypes mirrors upstream FILE_TYPE_IMAGE (const.ts).
//
// **prefix 判定 ("image/") にしない。** それでは `image/svg+xml` を通してしまい、
// 絵文字は本文中にそのまま埋め込まれるので XSS になる。admin 側の
// `isAllowedEmojiImageType` と**同じ集合を共有する** — 申請側が prefix、承認側が
// allowlist という食い違いがあると、申請は通るのに承認で必ず落ちる形になり、
// 「押した後にエラーになるのを無くす」という申請時検証の目的が svg でだけ
// 達成できない (レビュー R3)。
// IsAllowedImageType reports whether the MIME type may back a custom emoji.
//
// **map を公開しない (レビュー Low 8)。** 公開 mutable な map だと、どこからでも
// 書き換えられる。関数越しにすれば集合の共有はそのままで、書き換えの経路が消える。
func IsAllowedImageType(mime string) bool { return allowedImageTypes[mime] }

var allowedImageTypes = map[string]bool{
	"image/png":    true,
	"image/gif":    true,
	"image/jpeg":   true,
	"image/webp":   true,
	"image/avif":   true,
	"image/apng":   true,
	"image/bmp":    true,
	"image/tiff":   true,
	"image/x-icon": true,
}

// maxAliases caps how many aliases a single application may carry.
//
// 検索用の別名で、際限なく受けると emoji 行の配列がそのまま膨らむ。
const maxAliases = 16

// IDGenerator issues new row IDs.
type IDGenerator interface {
	Generate(t time.Time) string
}

// EmojiCreator turns an approved application into a real emoji row.
//
// **admin/emoji/add と同じ経路を通すための穴。** ここで独自に emoji を作ると、
// MIME の allowlist や webpublic variant の優先といった検証が申請経路だけ
// 抜ける — 承認が「検証を迂回して絵文字を登録する方法」になってしまう。
// 実装は admin handler 側に置き、既存の追加処理と 1 本にまとめる。
type EmojiCreator interface {
	CreateFromApplication(ctx context.Context, app *model.EmojiApplication) (emojiID string, err error)
	// DeleteCreatedEmoji removes an emoji created for an application that then
	// lost the race to another moderator.
	//
	// **作った emoji を片付ける口 (レビュー R5)。** 承認が条件付き UPDATE に
	// 負けると、申請は「却下」なのに絵文字だけ登録済みで使える状態が残る。
	// M1 前の「絵文字があるのに申請は却下」と症状が同じで、確率が下がっただけ。
	DeleteCreatedEmoji(ctx context.Context, emojiID string) error
}

// ResultNotifier tells the applicant that their request was processed.
//
// 通知は副作用なので失敗しても審査自体は成立させる (呼び出し側で握る)。
type ResultNotifier interface {
	NotifyEmojiApplicationProcessed(ctx context.Context, app *model.EmojiApplication) error
}

// DriveFileLookup resolves the drive file backing an "own" application.
type DriveFileLookup interface {
	FindByID(id string) (*model.DriveFile, error)
}

// EmojiLookup is the only thing this package needs from the emoji repository.
//
// **広い interface を取らない。** repository.EmojiRepository は CRUD を一式
// 持つが、ここで要るのは重複判定だけ。狭く取ると、テストの偽物が本物の面を
// 全部埋める必要が無くなり、依存の実態も読んで分かる。
type EmojiLookup interface {
	FindByNameAndHost(name string, host *string) (*model.Emoji, error)
}

// Service owns the state transitions of emoji applications.
type Service struct {
	apps     repository.EmojiApplicationRepository
	emojis   EmojiLookup
	files    DriveFileLookup
	idGen    IDGenerator
	creator  EmojiCreator
	notifier ResultNotifier
	nowFunc  func() time.Time
	// policies は申請の期間上限を引く先 (#2958)。**未配線なら上限を掛けない** —
	// 掛けられないのに掛けたつもりになると、ロールを設定した運営者が
	// 「効いている」と誤解する。既定値も 0 (無制限) なので挙動は変わらない。
	policies PolicyProvider
}

// PolicyProvider resolves a user's effective role policies (#2958).
type PolicyProvider interface {
	GetUserPolicies(userID string) map[string]any
}

// SetPolicyProvider wires the role policy source used for the rolling quota.
func (s *Service) SetPolicyProvider(p PolicyProvider) { s.policies = p }

// HasPolicyProvider reports whether the quota can be enforced.
func (s *Service) HasPolicyProvider() bool { return s.policies != nil }

// QuotaExceededError is returned when a rolling window is full (#2958).
// 呼び出し元はこれを HTTP 429 に翻訳し、期間・使用数・上限・再試行時刻を返す。
type QuotaExceededError struct {
	Period  string
	Used    int
	Limit   int
	RetryAt time.Time
}

func (e *QuotaExceededError) Error() string { return "emoji application quota exceeded" }

// quotaWindows builds the rolling windows from the user's role policies.
//
// **ローリング期間にする。** 固定暦だとタイムゾーン依存になり、切り替わりの
// 直前と直後に連続で申請できてしまう。
func (s *Service) quotaWindows(userID string) []repository.QuotaWindow {
	if s.policies == nil {
		return nil
	}
	// **errorless 版を使う。** ロール解決に失敗すると base が返る。base には
	// `meta.policies` (管理画面のベースロール) まで載っているので、そちらに
	// 上限を入れていればそれが効き、入れていなければ既定の 0 = 無制限に
	// 倒れる。他の policy consumer と揃えた fail-soft で、DB 全断ならこの
	// 直後の INSERT も落ちる。
	p := s.policies.GetUserPolicies(userID)
	if p == nil {
		return nil
	}
	// 窓は狭い順に並べてある。**ただし repository 側は満杯のものを全部評価して
	// いちばん遅く空くものを返す**ので、順序は結果を変えない (読みやすさのため)。
	return []repository.QuotaWindow{
		{Name: "day", Duration: 24 * time.Hour, Max: policyMax(p, role.PolicyEmojiApplicationMaxPerDay)},
		{Name: "week", Duration: 7 * 24 * time.Hour, Max: policyMax(p, role.PolicyEmojiApplicationMaxPerWeek)},
		{Name: "month", Duration: 30 * 24 * time.Hour, Max: policyMax(p, role.PolicyEmojiApplicationMaxPerMonth)},
	}
}

// policyMax reads a rolling-window limit. 0 以下・未設定・読めない値はすべて
// 「無制限」(0) に倒す。
//
// **小数は切り捨てる。** `PolicyNumber` は 2.5 のような値をそのまま返すので、
// 切り上げると運営者が意図した上限より 1 件多く通る。
func policyMax(p map[string]any, key string) int {
	v, ok := role.PolicyNumber(p[key])
	if !ok || v <= 0 {
		return 0
	}
	if v > float64(math.MaxInt32) {
		return math.MaxInt32
	}
	return int(math.Floor(v))
}

// NewService constructs the service. creator / notifier may be nil in tests and
// in configurations that do not wire them.
func NewService(
	apps repository.EmojiApplicationRepository,
	emojis EmojiLookup,
	files DriveFileLookup,
	idGen IDGenerator,
	creator EmojiCreator,
	notifier ResultNotifier,
) *Service {
	return &Service{
		apps:     apps,
		emojis:   emojis,
		files:    files,
		idGen:    idGen,
		creator:  creator,
		notifier: notifier,
		nowFunc:  time.Now,
	}
}

// CreateInput is the applicant-supplied part of a new application.
type CreateInput struct {
	UserID string
	// Kind is "own" (default) or "remote".
	Kind        string
	Name        string
	Category    string
	Aliases     []string
	License     string
	IsSensitive bool
	FileID      string
	// RemoteHost / RemoteName identify the emoji to import when Kind == remote.
	RemoteHost string
	RemoteName string
	Comment    string
}

// Create records a new pending application.
func (s *Service) Create(in CreateInput) (*model.EmojiApplication, error) {
	name := strings.TrimSpace(in.Name)
	// name は ASCII のみ (namePattern) なので文字数 = バイト数だが、数え方を
	// 揃えておく。
	if name == "" || utf8.RuneCountInString(name) > 128 || !namePattern.MatchString(name) {
		return nil, ErrInvalidName
	}
	license := strings.TrimSpace(in.License)
	// **列の長さを超える入力を DB に渡さない (レビュー M8)。** 渡すと
	// SQLSTATE 22001 が生のまま返り、利用者の入力で 5xx が立つ。
	//
	// **文字数で数える (レビュー R4)。** varchar(N) は文字数だが len() は
	// バイト数なので、バイトで見ると日本語は列の約 1/3 しか使えず、正当な
	// 入力が 400 になる。
	if utf8.RuneCountInString(license) > 1024 {
		return nil, ErrTooLong
	}
	if utf8.RuneCountInString(strings.TrimSpace(in.Category)) > 128 ||
		utf8.RuneCountInString(strings.TrimSpace(in.Comment)) > 2048 {
		return nil, ErrTooLong
	}
	// **素材の検証だけが kind で分かれる。** 名前・ライセンス・長さ・重複は
	// 共通で、ここから下だけが「自作画像」と「リモート絵文字」で違う。
	// どちらも**申請の時点で**検証する — 承認まで通してから落ちると、
	// モデレーターが押した後にエラーになり、申請者にも審査者にも何も残らない。
	kind := in.Kind
	if kind == "" {
		kind = model.EmojiApplicationKindOwn
	}

	var fileID, remoteHost, remoteName string
	switch kind {
	case model.EmojiApplicationKindOwn:
		fileID = strings.TrimSpace(in.FileID)
		if fileID == "" {
			return nil, ErrFileRequired
		}
		// 所有権は security の問題で、MIME は体験の問題。
		if err := s.checkFile(fileID, in.UserID); err != nil {
			return nil, err
		}
	case model.EmojiApplicationKindRemote:
		remoteHost = strings.TrimSpace(in.RemoteHost)
		remoteName = strings.TrimSpace(in.RemoteName)
		if remoteHost == "" || remoteName == "" {
			return nil, ErrRemoteRequired
		}
		if utf8.RuneCountInString(remoteHost) > 128 || utf8.RuneCountInString(remoteName) > 128 {
			return nil, ErrTooLong
		}
		if err := s.checkRemoteEmoji(remoteName, remoteHost); err != nil {
			return nil, err
		}
	default:
		return nil, ErrInvalidKind
	}

	// **ライセンスの必須は自作のときだけ (レビュー M1)。** リモートは取り込み元
	// (host + name) 自体が出典で、相手の絵文字が `_misskey_license` を持っていれば
	// 承認時にそれが入る。申請者は `fetch-remote-meta` を叩けないので、参照できない
	// 値の記入を必須にすると「それらしい嘘」を書かせることになる。
	//
	// **kind を確定させてから見る (レビュー Low 4)。** 正規化前の `in.Kind` で
	// 判定していると、未知の kind + ライセンス空が ErrInvalidKind ではなく
	// ErrLicenseRequired になり、診断が「ライセンスを書け」と事実と違うことを指す。
	if license == "" && kind != model.EmojiApplicationKindRemote {
		return nil, ErrLicenseRequired
	}

	// **既に同名の絵文字があるなら受け付けない。** 受けてしまうと、審査で
	// 承認を押した瞬間に DUPLICATE_NAME で落ちる。押す前に分かるほうがよい。
	// DB 障害を「重複なし」に丸めない (#2792)。
	existing, err := s.emojis.FindByNameAndHost(name, nil)
	if err != nil && !repository.IsNotFound(err) {
		return nil, err
	}
	if err == nil && existing != nil {
		return nil, ErrDuplicateName
	}

	now := s.nowFunc()
	app := &model.EmojiApplication{
		ID:          s.idGen.Generate(now),
		UserID:      in.UserID,
		Kind:        kind,
		Status:      model.EmojiApplicationPending,
		Name:        name,
		Aliases:     normalizeAliases(in.Aliases),
		License:     license,
		IsSensitive: in.IsSensitive,
		Comment:     optionalString(in.Comment),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if cat := strings.TrimSpace(in.Category); cat != "" {
		app.Category = &cat
	}
	if fileID != "" {
		app.FileID = &fileID
	}
	if remoteHost != "" {
		app.RemoteHost = &remoteHost
		app.RemoteName = &remoteName
	}

	// **数えるのと作るのを 1 つのトランザクションでやる (#2958)。** COUNT →
	// INSERT に分けると、同じ利用者から同時に来たリクエストが両方とも
	// 「空きあり」を読んで両方通る。
	if err := s.apps.CreateWithQuota(app, s.quotaWindows(in.UserID)); err != nil {
		if errors.Is(err, repository.ErrEmojiApplicationDuplicatePending) {
			return nil, ErrAlreadyPending
		}
		var qe *repository.QuotaExceededError
		if errors.As(err, &qe) {
			return nil, &QuotaExceededError{
				Period: qe.Window.Name, Used: qe.Used, Limit: qe.Window.Max, RetryAt: qe.RetryAt,
			}
		}
		return nil, err
	}
	return app, nil
}

// checkFile resolves the drive file and asserts the applicant owns a usable image.
//
// **userId が NULL のものも拒否する。** 未紐付けのファイルは誰のものとも
// 言えないので、申請の素材にはしない (applyMediaUpdate と同じ判断)。
func (s *Service) checkFile(fileID, userID string) error {
	if s.files == nil {
		// 未配線の構成では検証できない。**通さない** — 検証していないものを
		// 通すと、配線を落とした瞬間に穴が開く (fail-closed)。
		return ErrFileGone
	}
	f, err := s.files.FindByID(fileID)
	if err != nil {
		if repository.IsNotFound(err) {
			return ErrFileGone
		}
		// DB 障害を not-found に丸めない (#2792)。
		return err
	}
	if f.UserID == nil || *f.UserID != userID {
		// **「他人のもの」とは答えない。** 区別できると、ファイル ID の
		// 存在確認に使える。存在しないのと同じ応答にする。
		return ErrFileGone
	}
	if !IsAllowedImageType(f.Type) {
		return ErrUnsupportedFileType
	}
	return nil
}

// checkRemoteEmoji asserts the referenced remote emoji is known to this instance.
//
// **リモート絵文字の行はキャッシュに近い。** 使われなくなると消えるので、
// 申請の時点で引けることを確かめる。承認時にはもう一度取り直すので、審査を
// 待つ間に消えても申請自体は無意味にならない (だから `emojiId` ではなく
// host + name で持つ)。
func (s *Service) checkRemoteEmoji(name, host string) error {
	e, err := s.emojis.FindByNameAndHost(name, &host)
	if err != nil {
		if repository.IsNotFound(err) {
			return ErrNoSuchRemoteEmoji
		}
		// DB 障害を not-found に丸めない (#2792)。
		return err
	}
	if e == nil {
		return ErrNoSuchRemoteEmoji
	}
	return nil
}

// Approve registers the emoji and closes the application.
//
// **emoji を作ってから申請を閉じる。** 逆順にすると、作成に失敗したときに
// 「承認済みなのに絵文字が無い」申請が残り、押し直す手段も無くなる。
func (s *Service) Approve(ctx context.Context, id, moderatorID string) (*model.EmojiApplication, error) {
	app, err := s.pending(id)
	if err != nil {
		return nil, err
	}
	if s.creator == nil {
		return nil, errors.New("emojiapplication: creator is not wired")
	}

	emojiID, err := s.creator.CreateFromApplication(ctx, app)
	if err != nil {
		return nil, err
	}

	now := s.nowFunc()
	app.Status = model.EmojiApplicationApproved
	app.EmojiID = &emojiID
	app.ProcessedByID = &moderatorID
	app.ProcessedAt = &now
	app.UpdatedAt = now
	// **条件付きで書く。** 読んでから書くまでの間に他のモデレーターが処理して
	// いたら、上書きせずに ErrNotPending を返す (レビュー M1)。
	ok, err := s.apps.UpdateIfPending(app)
	if err != nil {
		return nil, err
	}
	if !ok {
		// **負けたら作った emoji を片付ける (レビュー R5)。** 残すと、申請は
		// 「却下」で通知も却下なのに絵文字だけ使える状態になる。削除に失敗しても
		// 審査の結果は変わらないので、ErrNotPending をそのまま返す。
		if derr := s.creator.DeleteCreatedEmoji(ctx, emojiID); derr != nil {
			slog.Warn("emojiapplication: 競合で負けた承認の emoji を消せなかった",
				"applicationId", app.ID, "emojiId", emojiID, "err", derr)
		}
		return nil, ErrNotPending
	}
	s.notify(ctx, app)
	return app, nil
}

// Reject closes the application without registering anything.
func (s *Service) Reject(ctx context.Context, id, moderatorID, reason string) (*model.EmojiApplication, error) {
	app, err := s.pending(id)
	if err != nil {
		return nil, err
	}
	now := s.nowFunc()
	app.Status = model.EmojiApplicationRejected
	app.ProcessedByID = &moderatorID
	app.ProcessedAt = &now
	app.RejectReason = optionalString(reason)
	app.UpdatedAt = now
	ok, err := s.apps.UpdateIfPending(app)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotPending
	}
	s.notify(ctx, app)
	return app, nil
}

// Cancel lets the applicant withdraw their own pending application.
func (s *Service) Cancel(id, userID string) error {
	// **所有者の確認を pending の判定より先に行う (レビュー M2)。** 逆順だと、
	// 他人の「処理済み」申請に対して ErrNotPending が返り、存在しない ID
	// (ErrNotFound) と区別できてしまう — ID 列挙のオラクルになる。呼び出し側が
	// ErrForbidden を 404 に潰しても、この順序でなければ塞がらない。
	app, err := s.apps.FindByID(id)
	if err != nil {
		if repository.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	if app.UserID != userID {
		return ErrForbidden
	}
	if !app.IsPending() {
		return ErrNotPending
	}
	now := s.nowFunc()
	app.Status = model.EmojiApplicationCanceled
	app.UpdatedAt = now
	ok, err := s.apps.UpdateIfPending(app)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotPending
	}
	return nil
}

// pending loads an application and asserts it still awaits review.
func (s *Service) pending(id string) (*model.EmojiApplication, error) {
	app, err := s.apps.FindByID(id)
	if err != nil {
		if repository.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !app.IsPending() {
		return nil, ErrNotPending
	}
	return app, nil
}

// notify is best-effort: 通知に失敗しても審査の結果は確定している。
func (s *Service) notify(ctx context.Context, app *model.EmojiApplication) {
	if s.notifier == nil {
		return
	}
	_ = s.notifier.NotifyEmojiApplicationProcessed(ctx, app)
}

// normalizeAliases trims, drops empties and duplicates, and caps the count.
func normalizeAliases(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" || utf8.RuneCountInString(a) > 128 {
			continue
		}
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
		if len(out) >= maxAliases {
			break
		}
	}
	return out
}

func optionalString(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
