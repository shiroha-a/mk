package hashtags_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/api/hashtags"
	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var testDB *gorm.DB

func init() {
	testDB = testutil.MustOpenTestDB()
	testutil.ApplyMigrations(testDB)
}

func newHandler() *hashtags.Handler {
	return hashtags.NewHandler(testDB)
}

func brokenHandler() *hashtags.Handler {
	db := testutil.MustOpenTestDB()
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	return hashtags.NewHandler(db)
}

func doPost(h func(echo.Context) error, body string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = h(c)
	return rec
}

func cleanup() {
	testDB.Exec(`DELETE FROM "hashtag"`)
}

// --- List ---

func TestList_Success(t *testing.T) {
	cleanup()
	assert.Equal(t, http.StatusOK, doPost(newHandler().List, `{"sort":"+mentionedUsers"}`).Code)
}

func TestList_WithData(t *testing.T) {
	cleanup()
	testDB.Create(&model.Hashtag{ID: "ht1", Name: "golang", MentionedUsersCount: 5})
	defer cleanup()
	rec := doPost(newHandler().List, `{"limit":10,"sort":"+mentionedUsers"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "golang")
}

func TestList_WithOffset(t *testing.T) {
	assert.Equal(t, http.StatusOK, doPost(newHandler().List, `{"limit":5,"offset":1,"sort":"+mentionedUsers"}`).Code)
}

func TestList_LimitCap(t *testing.T) {
	assert.Equal(t, http.StatusOK, doPost(newHandler().List, `{"limit":999,"sort":"+mentionedUsers"}`).Code)
}

func TestList_InvalidJSON(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().List, `invalid`).Code)
}

// TestList_SortRequired: upstream Misskey TS は paramDef で sort を required
// にしている。mk-go も同 shape に揃え、sort 抜きは 400 で弾く (#925)。
func TestList_SortRequired(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().List, `{}`).Code)
}

// TestList_SortEnum: upstream paramDef は sort を 12 値の enum 限定にしている。
// mk-go も同 enum に絞り、未定義 sort 値で 400 を返す (#925 review nit)。
func TestList_SortEnum(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().List, `{"sort":"bogus"}`).Code)
}

func TestList_DBError(t *testing.T) {
	assert.Equal(t, http.StatusInternalServerError, doPost(brokenHandler().List, `{"sort":"+mentionedUsers"}`).Code)
}

// --- Search ---

func TestSearch_Success(t *testing.T) {
	cleanup()
	testDB.Create(&model.Hashtag{ID: "ht2", Name: "gotest"})
	defer cleanup()
	rec := doPost(newHandler().Search, `{"query":"gotest"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "gotest")
}

func TestSearch_InvalidParam(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().Search, `{}`).Code)
}

func TestSearch_DBError(t *testing.T) {
	assert.Equal(t, http.StatusInternalServerError, doPost(brokenHandler().Search, `{"query":"x"}`).Code)
}

// --- Show ---

func TestShow_Found(t *testing.T) {
	cleanup()
	testDB.Create(&model.Hashtag{ID: "ht3", Name: "rustlang"})
	defer cleanup()
	rec := doPost(newHandler().Show, `{"tag":"rustlang"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "rustlang")
	// L3 (#1270): hashtags/show の実レスポンスを golden Hashtag に突合する。
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	shapetest.Assert(t, "Hashtag", resp)
}

// TestShow_BadRequestForUnknownTag: upstream Misskey TS は ApiError 既定動作
// で 400 + NO_SUCH_HASHTAG body を返す (#925)。404 ではなく 400 に揃える。
// HTTP semantics 的には 404 が自然だが drop-in 互換を優先する。
func TestShow_BadRequestForUnknownTag(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().Show, `{"tag":"ghost"}`).Code)
}

func TestShow_InvalidParam(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().Show, `{}`).Code)
}

// --- Trend ---

// cleanupNotes は Trend 関連 test の前後で note と user (FK) を掃除する。
func cleanupNotes() {
	testDB.Exec(`DELETE FROM "note"`)
	testDB.Exec(`DELETE FROM "user" WHERE "id" LIKE 'tu_%'`)
}

// ensureUser は note の userId FK を満たすための最小限の user row を
// 用意する。同 id が既存の場合は no-op。
func ensureUser(t *testing.T, userID string) {
	t.Helper()
	testDB.Exec(`
		INSERT INTO "user" ("id", "username", "usernameLower")
		VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING
	`, userID, userID, userID)
}

// insertNote: Trend test 用の最小限 note row を作る。aidx ID を
// createdAt として埋め込み、note.tags に集計対象タグを入れる。
// model.Note 経由だと validation が重いので raw SQL で軽く済ます。
func insertNote(t *testing.T, gen id.Generator, userID string, tags []string, createdAt time.Time) {
	t.Helper()
	insertNoteWithVisibility(t, gen, userID, tags, createdAt, "public")
}

// insertNoteWithVisibility: visibility (public/home/followers/specified)
// を制御できる挿入ヘルパー。trend 集計の visibility フィルタ検証用。
func insertNoteWithVisibility(t *testing.T, gen id.Generator, userID string, tags []string, createdAt time.Time, visibility string) {
	t.Helper()
	ensureUser(t, userID)
	noteID := gen.Generate(createdAt)
	require.NoError(t, testDB.Exec(
		`INSERT INTO "note" ("id", "userId", "tags", "visibility", "text")
		 VALUES (?, ?, ?, ?::note_visibility_enum, '')`,
		noteID, userID, pq.StringArray(tags), visibility,
	).Error)
}

func TestTrend_EmptyReturnsEmptyArray(t *testing.T) {
	cleanupNotes()
	rec := doPost(newHandler().Trend, `{}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp, "no notes → empty trend list")
}

func TestTrend_AggregatesRecentNotes(t *testing.T) {
	cleanupNotes()
	defer cleanupNotes()

	gen, err := id.NewGenerator("aidx")
	require.NoError(t, err)

	now := time.Now()
	// alpha: 2 distinct users (tu_1@bucket0, tu_2@bucket1)
	insertNote(t, gen, "tu_1", []string{"alpha"}, now.Add(-5*time.Minute))
	insertNote(t, gen, "tu_2", []string{"alpha"}, now.Add(-15*time.Minute))
	// beta: 1 user (tu_1@bucket0)
	insertNote(t, gen, "tu_1", []string{"beta"}, now.Add(-5*time.Minute))
	// gamma: 1 note だが 200 分超 → bucket 範囲外、cutoff で除外
	insertNote(t, gen, "tu_3", []string{"gamma"}, now.Add(-300*time.Minute))

	rec := doPost(newHandler().Trend, `{}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// alpha (total 2 users) → beta (total 1) の順にランクされる。gamma は除外。
	require.Len(t, resp, 2, "alpha + beta only")
	assert.Equal(t, "alpha", resp[0]["tag"])
	assert.Equal(t, "beta", resp[1]["tag"])

	// chart 配列は 20 buckets 固定
	chart := resp[0]["chart"].([]any)
	require.Len(t, chart, 20, "chart bucket count = 20")

	// alpha: bucket 0 (直近 10 分) と bucket 1 (10-20 分) に各 1 user
	assert.EqualValues(t, 1, chart[0], "alpha bucket 0 has tu_1")
	assert.EqualValues(t, 1, chart[1], "alpha bucket 1 has tu_2")
	for i := 2; i < 20; i++ {
		assert.EqualValues(t, 0, chart[i], "alpha bucket %d empty", i)
	}

	// usersCount は max(chart) で alpha は 1 (各 bucket の users が 1)
	assert.EqualValues(t, 1, resp[0]["usersCount"])
}

func TestTrend_DBError(t *testing.T) {
	assert.Equal(t, http.StatusInternalServerError, doPost(brokenHandler().Trend, `{}`).Code)
}

// TestTrend_VisibilityFilterExcludesPrivate guards #678 review:
// followers / specified の note は trend 集計対象外。
func TestTrend_VisibilityFilterExcludesPrivate(t *testing.T) {
	cleanupNotes()
	defer cleanupNotes()

	gen, err := id.NewGenerator("aidx")
	require.NoError(t, err)

	now := time.Now()
	// public + home は集計対象、followers + specified は除外。
	insertNoteWithVisibility(t, gen, "tu_p", []string{"public_tag"}, now.Add(-5*time.Minute), "public")
	insertNoteWithVisibility(t, gen, "tu_h", []string{"home_tag"}, now.Add(-5*time.Minute), "home")
	insertNoteWithVisibility(t, gen, "tu_f", []string{"followers_tag"}, now.Add(-5*time.Minute), "followers")
	insertNoteWithVisibility(t, gen, "tu_s", []string{"specified_tag"}, now.Add(-5*time.Minute), "specified")

	rec := doPost(newHandler().Trend, `{}`)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	// public_tag / home_tag は出る。
	assert.Contains(t, body, `"public_tag"`)
	assert.Contains(t, body, `"home_tag"`)
	// followers_tag / specified_tag は trend に出てはいけない (プライバシー保護)。
	assert.NotContains(t, body, `"followers_tag"`,
		"followers visibility は集計対象外であるべき")
	assert.NotContains(t, body, `"specified_tag"`,
		"specified visibility は集計対象外であるべき")
}

// --- Users ---

// seedTagUser inserts a local/remote user with the given normalized tags for
// the hashtags/users query tests. user.tags は正規化済み (lowercase) で渡す
// 前提 (= Part 2 の populate と同じ。query 側もここで正規化一致を検証する)。
func seedTagUser(t *testing.T, id, username string, tags []string, host *string, suspended bool, followers int, updatedAt time.Time) {
	t.Helper()
	u := &model.User{
		ID:                id,
		Username:          username,
		UsernameLower:     strings.ToLower(username),
		Tags:              pq.StringArray(tags),
		Host:              host,
		IsSuspended:       suspended,
		FollowersCount:    followers,
		UpdatedAt:         &updatedAt,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	require.NoError(t, testDB.Create(u).Error)
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "user" WHERE id = ?`, id) })
}

func decodeUserIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	ids := make([]string, 0, len(list))
	for _, u := range list {
		id, _ := u["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

// tag containment は正規化一致で効く: "tokyo" を持つ user は "Tokyo" query で
// ヒットし、別タグの user はヒットしない。あわせて UserDetailed shape も確認。
func TestUsers_ContainmentAndShape(t *testing.T) {
	now := time.Now()
	seedTagUser(t, "u_htu_a", "htua", []string{"tokyo"}, nil, false, 1, now)
	seedTagUser(t, "u_htu_b", "htub", []string{"osaka"}, nil, false, 1, now)

	rec := doPost(newHandler().Users, `{"tag":"Tokyo","sort":"+follower"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "u_htu_a", list[0]["id"])
	// UserDetailed shape (UserLite + detailed fields) で返ること。
	assert.Equal(t, "htua", list[0]["username"])
	assert.Contains(t, list[0], "followersCount")
	assert.Contains(t, list[0], "notesCount")
}

// suspended user は除外される (isSuspended = FALSE)。
func TestUsers_SuspendedExcluded(t *testing.T) {
	now := time.Now()
	seedTagUser(t, "u_htu_sus", "htususp", []string{"golangx"}, nil, true, 1, now)
	ids := decodeUserIDs(t, doPost(newHandler().Users, `{"tag":"golangx","sort":"+follower"}`))
	assert.NotContains(t, ids, "u_htu_sus")
}

// +follower DESC / -follower ASC の sort 順を確認。
func TestUsers_SortFollower(t *testing.T) {
	now := time.Now()
	seedTagUser(t, "u_htu_lo", "htulo", []string{"sortt"}, nil, false, 1, now)
	seedTagUser(t, "u_htu_hi", "htuhi", []string{"sortt"}, nil, false, 100, now)

	desc := decodeUserIDs(t, doPost(newHandler().Users, `{"tag":"sortt","sort":"+follower"}`))
	require.Equal(t, []string{"u_htu_hi", "u_htu_lo"}, desc)
	asc := decodeUserIDs(t, doPost(newHandler().Users, `{"tag":"sortt","sort":"-follower"}`))
	require.Equal(t, []string{"u_htu_lo", "u_htu_hi"}, asc)
}

// state=alive は updatedAt が 5日以内の user のみ返す。
func TestUsers_StateAlive(t *testing.T) {
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour)
	seedTagUser(t, "u_htu_new", "htunew", []string{"alivet"}, nil, false, 1, now)
	seedTagUser(t, "u_htu_old", "htuold", []string{"alivet"}, nil, false, 1, old)

	all := decodeUserIDs(t, doPost(newHandler().Users, `{"tag":"alivet","sort":"+follower"}`))
	assert.ElementsMatch(t, []string{"u_htu_new", "u_htu_old"}, all)
	alive := decodeUserIDs(t, doPost(newHandler().Users, `{"tag":"alivet","sort":"+follower","state":"alive"}`))
	assert.Equal(t, []string{"u_htu_new"}, alive)
}

// origin=local は host IS NULL のみ、remote は host IS NOT NULL のみ、
// combined は両方返す。
func TestUsers_Origin(t *testing.T) {
	now := time.Now()
	host := "remote.example"
	seedTagUser(t, "u_htu_loc", "htuloc", []string{"origint"}, nil, false, 1, now)
	seedTagUser(t, "u_htu_rem", "hturem", []string{"origint"}, &host, false, 1, now)

	local := decodeUserIDs(t, doPost(newHandler().Users, `{"tag":"origint","sort":"+follower","origin":"local"}`))
	assert.Equal(t, []string{"u_htu_loc"}, local)
	remote := decodeUserIDs(t, doPost(newHandler().Users, `{"tag":"origint","sort":"+follower","origin":"remote"}`))
	assert.Equal(t, []string{"u_htu_rem"}, remote)
	combined := decodeUserIDs(t, doPost(newHandler().Users, `{"tag":"origint","sort":"+follower","origin":"combined"}`))
	assert.ElementsMatch(t, []string{"u_htu_loc", "u_htu_rem"}, combined)
}

// limit + offset が効く (offset で 2 ページ目を取得)。
func TestUsers_LimitOffset(t *testing.T) {
	now := time.Now()
	seedTagUser(t, "u_htu_p1", "htup1", []string{"paget"}, nil, false, 3, now)
	seedTagUser(t, "u_htu_p2", "htup2", []string{"paget"}, nil, false, 2, now)
	seedTagUser(t, "u_htu_p3", "htup3", []string{"paget"}, nil, false, 1, now)

	page1 := decodeUserIDs(t, doPost(newHandler().Users, `{"tag":"paget","sort":"+follower","limit":2}`))
	require.Equal(t, []string{"u_htu_p1", "u_htu_p2"}, page1)
	page2 := decodeUserIDs(t, doPost(newHandler().Users, `{"tag":"paget","sort":"+follower","limit":2,"offset":2}`))
	require.Equal(t, []string{"u_htu_p3"}, page2)
}

func TestUsers_InvalidParam(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().Users, `{}`).Code)
}

// TestUsers_SortRequired: upstream Misskey TS は paramDef で sort も
// required にしている。tag だけだと 400 (#925)。
func TestUsers_SortRequired(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().Users, `{"tag":"test"}`).Code)
}

// TestUsers_SortEnum: upstream paramDef は sort を 6 値の enum 限定。
// 未定義 sort で 400 を返すこと (#925 review nit)。
func TestUsers_SortEnum(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().Users, `{"tag":"test","sort":"bogus"}`).Code)
}

// state / origin も upstream paramDef enum 限定。未定義値で 400。
func TestUsers_StateEnum(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().Users, `{"tag":"test","sort":"+follower","state":"bogus"}`).Code)
}
func TestUsers_OriginEnum(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, doPost(newHandler().Users, `{"tag":"test","sort":"+follower","origin":"bogus"}`).Code)
}
func TestUsers_DBError(t *testing.T) {
	assert.Equal(t, http.StatusInternalServerError, doPost(brokenHandler().Users, `{"tag":"test","sort":"+follower"}`).Code)
}
