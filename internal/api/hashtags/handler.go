package hashtags

import (
	"net/http"
	"sort"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/gorm"
)

// Handler handles hashtag-related API endpoints.
type Handler struct {
	db *gorm.DB
}

// NewHandler creates a new hashtags Handler.
func NewHandler(db *gorm.DB) *Handler {
	return &Handler{db: db}
}

// validListSorts は upstream Misskey TS hashtags/list paramDef enum と一致 (#925)。
var validListSorts = map[string]struct{}{
	"+mentionedUsers": {}, "-mentionedUsers": {},
	"+mentionedLocalUsers": {}, "-mentionedLocalUsers": {},
	"+mentionedRemoteUsers": {}, "-mentionedRemoteUsers": {},
	"+attachedUsers": {}, "-attachedUsers": {},
	"+attachedLocalUsers": {}, "-attachedLocalUsers": {},
	"+attachedRemoteUsers": {}, "-attachedRemoteUsers": {},
}

// List handles POST /api/hashtags/list.
func (h *Handler) List(c echo.Context) error {
	var req struct {
		Limit  int    `json:"limit"`
		Sort   string `json:"sort"`
		Offset int    `json:"offset"`
	}
	if err := c.Bind(&req); err != nil || req.Sort == "" {
		// upstream Misskey TS は paramDef で sort を required にしている (#925)。
		// mk-go も同 shape に揃え、permissive な挙動を弾く。
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if _, ok := validListSorts[req.Sort]; !ok {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "sort must be one of upstream-defined enum.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	q := h.db.Model(&model.Hashtag{}).Order("\"mentionedUsersCount\" DESC").Limit(req.Limit)
	if req.Offset > 0 {
		q = q.Offset(req.Offset)
	}
	var tags []*model.Hashtag
	if err := q.Find(&tags).Error; err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	result := make([]map[string]any, 0, len(tags))
	for _, t := range tags {
		result = append(result, packTag(t))
	}
	return c.JSON(http.StatusOK, result)
}

// Search handles POST /api/hashtags/search.
func (h *Handler) Search(c echo.Context) error {
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := c.Bind(&req); err != nil || req.Query == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "query is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	var tags []*model.Hashtag
	if err := h.db.Where("name ILIKE ?", "%"+req.Query+"%").Limit(req.Limit).Find(&tags).Error; err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	names := make([]string, 0, len(tags))
	for _, t := range tags {
		names = append(names, t.Name)
	}
	return c.JSON(http.StatusOK, names)
}

// Show handles POST /api/hashtags/show.
func (h *Handler) Show(c echo.Context) error {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := c.Bind(&req); err != nil || req.Tag == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "tag is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	var tag model.Hashtag
	if err := h.db.Where("name = ?", req.Tag).First(&tag).Error; err != nil {
		// HTTP semantics 的には not-found = 404 が望ましいが、upstream
		// Misskey TS は ApiError の既定動作で 400 を返している (#925)。
		// drop-in 互換を優先して 400 + NO_SUCH_HASHTAG body に揃える。
		// error id も upstream と同じ ...c167b2e6981e に統一。
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_HASHTAG", "No such hashtag.", "110ee688-193e-4a3a-9ecf-c167b2e6981e"))
	}
	return c.JSON(http.StatusOK, packTag(&tag))
}

// Trend handles POST /api/hashtags/trend.
//
// Misskey TS 互換: 直近 200 分 (10 分 × 20 bucket) の note 数を hashtag
// 別に集計し、frontend の MkMiniChart が描画できる時系列を返す。
//
// TS 上流 (HashtagService.getCharts) は Redis HyperLogLog で users 単位
// のユニーク数を count するが、mk-go では HLL を維持せず note テーブル
// から動的に集計する。
//
// mk-go の note テーブルは createdAt 列を持たず、aidx ID 先頭 8 文字に
// ms timestamp が埋め込まれている (`internal/misc/id`)。200 分前の
// cutoff prefix を生成して `id > cutoffID` で範囲フィルタをかける。
// tags 配列 (varchar(128)[]) には migration 000043 で GIN index を張った
// ので `<>` フィルタも index で効く。
//
// visibility フィルタ: TS の HashtagService.updateHashtag は public /
// home の note のみ集計対象にする。followers / specified を含めると
// 公開トレンドに非公開タグが現れてプライバシー上の問題になるため、
// SQL の WHERE で同じ条件を強制する。
//
// 取得した row は Go 側で aidx parse → bucket idx 計算 → tag/bucket 別
// の unique users を集計する。直近 200 分の対象 note 数は中規模インス
// タンスなら数千件オーダーで、Go 側の集計コストは無視できる。
//
// chart の bucket は新しいもの順 (index 0 = 直近 10 分、index 19 = 200 分前)。
// usersCount は TS と同じく `max(chart)` で算出する。
func (h *Handler) Trend(c echo.Context) error {
	const (
		bucketSeconds = 600 // 10 分
		bucketCount   = 20  // 過去 200 分
		topN          = 10
	)

	gen, err := id.NewGenerator("aidx")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	cutoff := time.Now().Add(-time.Duration(bucketSeconds*bucketCount) * time.Second)
	cutoffID := id.AidxCutoffPrefix(cutoff)

	// pq.StringArray を含む struct を Scan(&rows) に渡すと GORM の
	// schema reflection が `unsupported data type: &[]` を warn-log する
	// (pq.StringArray の Scanner で runtime には正しく動くが、初期 type
	// infer で躓く)。Rows().Scan() で個別に scan するとこの noise を回避
	// できるので、本 handler では明示的なループ scan を採用する。
	type row struct {
		ID     string
		UserID string
		Tags   []string
	}
	var rows []row
	sqlRows, err := h.db.Raw(`
		SELECT "id", "userId", "tags"
		FROM "note"
		WHERE "id" > ?
		  AND "tags" <> ARRAY[]::varchar[]
		  AND "visibility" IN ('public', 'home')
	`, cutoffID).Rows()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	defer sqlRows.Close()
	for sqlRows.Next() {
		var r row
		var tags pq.StringArray
		if err := sqlRows.Scan(&r.ID, &r.UserID, &tags); err != nil {
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
		r.Tags = []string(tags)
		rows = append(rows, r)
	}
	if err := sqlRows.Err(); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// bucketUsers[tag][bucketIdx] = set of unique userId
	bucketUsers := make(map[string][bucketCount]map[string]struct{})
	totalUsers := make(map[string]map[string]struct{})

	now := time.Now()
	for _, r := range rows {
		ts, perr := gen.ParseTime(r.ID)
		if perr != nil {
			continue
		}
		secsAgo := now.Sub(ts).Seconds()
		if secsAgo < 0 || secsAgo >= float64(bucketSeconds*bucketCount) {
			continue
		}
		bucketIdx := int(secsAgo) / bucketSeconds
		for _, tag := range r.Tags {
			if tag == "" {
				continue
			}
			buckets, exists := bucketUsers[tag]
			if !exists {
				for i := range buckets {
					buckets[i] = map[string]struct{}{}
				}
				bucketUsers[tag] = buckets
			}
			bucketUsers[tag][bucketIdx][r.UserID] = struct{}{}

			if _, exists := totalUsers[tag]; !exists {
				totalUsers[tag] = map[string]struct{}{}
			}
			totalUsers[tag][r.UserID] = struct{}{}
		}
	}

	// ranking: total users 降順、同点は tag name 昇順 (deterministic)
	type ranked struct {
		tag   string
		total int
	}
	rankings := make([]ranked, 0, len(totalUsers))
	for tag, users := range totalUsers {
		rankings = append(rankings, ranked{tag: tag, total: len(users)})
	}
	sort.Slice(rankings, func(i, j int) bool {
		if rankings[i].total != rankings[j].total {
			return rankings[i].total > rankings[j].total
		}
		return rankings[i].tag < rankings[j].tag
	})
	if len(rankings) > topN {
		rankings = rankings[:topN]
	}

	result := make([]map[string]any, 0, len(rankings))
	for _, rk := range rankings {
		chart := make([]int, bucketCount)
		buckets := bucketUsers[rk.tag]
		maxUsers := 0
		for i := 0; i < bucketCount; i++ {
			n := len(buckets[i])
			chart[i] = n
			if n > maxUsers {
				maxUsers = n
			}
		}
		result = append(result, map[string]any{
			"tag":        rk.tag,
			"chart":      chart,
			"usersCount": maxUsers,
		})
	}
	return c.JSON(http.StatusOK, result)
}

// validUsersSorts は upstream Misskey TS hashtags/users paramDef enum と一致 (#925)。
var validUsersSorts = map[string]struct{}{
	"+follower": {}, "-follower": {},
	"+createdAt": {}, "-createdAt": {},
	"+updatedAt": {}, "-updatedAt": {},
}

// Users handles POST /api/hashtags/users.
func (h *Handler) Users(c echo.Context) error {
	var req struct {
		Tag   string `json:"tag"`
		Sort  string `json:"sort"`
		Limit int    `json:"limit"`
	}
	if err := c.Bind(&req); err != nil || req.Tag == "" || req.Sort == "" {
		// upstream Misskey TS は paramDef で tag + sort を required にしている (#925)。
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "tag and sort are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if _, ok := validUsersSorts[req.Sort]; !ok {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "sort must be one of upstream-defined enum.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// ハッシュタグを使ったユーザー一覧 (簡易版: 空配列)
	return c.JSON(http.StatusOK, []any{})
}

func packTag(t *model.Hashtag) map[string]any {
	return map[string]any{
		"tag":                       t.Name,
		"mentionedUsersCount":       t.MentionedUsersCount,
		"mentionedLocalUsersCount":  t.MentionedLocalUsersCount,
		"mentionedRemoteUsersCount": t.MentionedRemoteUsersCount,
		"attachedUsersCount":        t.AttachedUsersCount,
		"attachedLocalUsersCount":   t.AttachedLocalUsersCount,
		"attachedRemoteUsersCount":  t.AttachedRemoteUsersCount,
	}
}
