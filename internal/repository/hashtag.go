package repository

import (
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// HashtagRepository は `hashtag` テーブルへのアクセスを提供する (#680)。
//
// Misskey TS の HashtagService.updateHashtag 相当を mk-go に移植するための
// repository 層。RecordMention / RecordAttach は冪等で、同じ user を二度
// 渡してもカウンタは増えない (mentionedUserIds 配列で dedup する)。
type HashtagRepository interface {
	// FindByName は name で 1 行引く。見つからなければ gorm.ErrRecordNotFound。
	FindByName(name string) (*model.Hashtag, error)

	// RecordMention はユーザー `userID` がハッシュタグ `name` を **言及した**
	// 事実を記録する。冪等: 同じ user を再度渡しても count は増えない。
	// `isLocal` は user.Host == nil で判定して呼び出し側が渡す (このメソッド
	// 自体は user 行を fetch しない)。
	//
	// row が存在しなければ INSERT して count=1 で初期化する (id は idGen で
	// 採番)。既存 row があれば mentionedUserIds に append して count を 1
	// 増やす (NOT ANY ガードで重複防止)。
	RecordMention(id, name, userID string, isLocal bool) error

	// RecordAttach は user の profile に hashtag を attached した記録を残す。
	// semantics は RecordMention と同じだが対象列が attached* 系。
	// (現状 mk-go では user profile への hashtag 添付 UI が無いため呼出側
	// 未配線だが、API 互換性のため提供する。)
	RecordAttach(id, name, userID string, isLocal bool) error
}

type hashtagRepository struct {
	db *gorm.DB
}

// NewHashtagRepository creates a new HashtagRepository.
func NewHashtagRepository(db *gorm.DB) HashtagRepository {
	return &hashtagRepository{db: db}
}

func (r *hashtagRepository) FindByName(name string) (*model.Hashtag, error) {
	var h model.Hashtag
	if err := r.db.Where("name = ?", name).First(&h).Error; err != nil {
		return nil, err
	}
	return &h, nil
}

// RecordMention は 2 段の SQL で冪等な「行確保 → dedup-add」を実装する。
//
// 段階 1: INSERT ON CONFLICT DO NOTHING で「同名 hashtag 行が必ず 1 つ存在する」
// 状態を作る。並行 2 リクエストがあっても name UNIQUE 前提で 1 行に収束する。
// なお、新規 INSERT 時は count=0 / userIds=空 で挿入する (count 更新は段階 2 が
// 担当する。INSERT 時に count=1 で挿入してしまうと、既に存在した行に対する
// 並行 INSERT が DO NOTHING で skip された後で UPDATE が走ると、後続の dedup
// guard が効いて二重 count を防げてしまうので、count は常に「step 2 で計算」
// する単一系統に集約する)。
//
// 段階 2: UPDATE ... WHERE NOT (? = ANY(...)) で「まだ list に居なければ
// append + count++」を atomic に行う。WHERE ガードのおかげで多重 RecordMention
// が走っても結果は idempotent (count は user 種類数だけ増える)。
//
// `local` / `remote` 列は isLocal フラグで分岐。total 列は常に更新する。
func (r *hashtagRepository) RecordMention(id, name, userID string, isLocal bool) error {
	return r.recordImpl(id, name, userID, isLocal,
		"mentionedUserIds", "mentionedUsersCount",
		"mentionedLocalUserIds", "mentionedLocalUsersCount",
		"mentionedRemoteUserIds", "mentionedRemoteUsersCount",
	)
}

func (r *hashtagRepository) RecordAttach(id, name, userID string, isLocal bool) error {
	return r.recordImpl(id, name, userID, isLocal,
		"attachedUserIds", "attachedUsersCount",
		"attachedLocalUserIds", "attachedLocalUsersCount",
		"attachedRemoteUserIds", "attachedRemoteUsersCount",
	)
}

// recordImpl は RecordMention / RecordAttach 共通実装。column 名だけ差し替え。
func (r *hashtagRepository) recordImpl(
	id, name, userID string, isLocal bool,
	totalIDsCol, totalCountCol,
	localIDsCol, localCountCol,
	remoteIDsCol, remoteCountCol string,
) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// 段階 1: 行確保。
		if err := tx.Exec(
			`INSERT INTO "hashtag" (id, name) VALUES (?, ?) ON CONFLICT (name) DO NOTHING`,
			id, name,
		).Error; err != nil {
			return err
		}
		// 段階 2: 該当 user がまだ list に居なければ append + count++。
		// total / (local|remote) を 1 度の UPDATE で更新する。
		// 文字列連結で column 名を組むのは固定 const のみなので injection 安全。
		var subIDsCol, subCountCol string
		if isLocal {
			subIDsCol, subCountCol = localIDsCol, localCountCol
		} else {
			subIDsCol, subCountCol = remoteIDsCol, remoteCountCol
		}
		query := `
UPDATE "hashtag" SET
  "` + totalIDsCol + `"   = array_append("` + totalIDsCol + `", ?),
  "` + totalCountCol + `" = "` + totalCountCol + `" + 1,
  "` + subIDsCol + `"     = array_append("` + subIDsCol + `", ?),
  "` + subCountCol + `"   = "` + subCountCol + `" + 1
WHERE name = ? AND NOT (? = ANY("` + totalIDsCol + `"))`
		return tx.Exec(query, userID, userID, name, userID).Error
	})
}
