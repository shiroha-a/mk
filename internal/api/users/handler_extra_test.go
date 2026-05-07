package users_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/users"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	coreuser "github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newExtraHandler(t *testing.T) (*users.Handler, *testutil.MockUserRepository, *testutil.MockNoteRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	followRequestRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	userSvc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	followingSvc := corefollowing.NewService(userRepo, followingRepo, followRequestRepo, idGen)
	h := users.NewHandler(userSvc, followingSvc, noteRepo, idGen)
	h.SetAbuseRepo(testutil.NewMockAbuseReportRepository())
	return h, userRepo, noteRepo
}

func postExtra(h func(echo.Context) error, body string, user *model.User) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if user != nil {
		c.Set(string(middleware.UserContextKey), user)
	}
	_ = h(c)
	return rec
}

// --- Relation ---

func TestRelation_Success(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Relation, `{"userId":"u2"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "u2", resp["id"])
	assert.Equal(t, false, resp["isFollowing"])
}

func TestRelation_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Relation, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- ReportAbuse ---

func TestReportAbuse_Success(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.ReportAbuse, `{"userId":"u2","comment":"spam"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestReportAbuse_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.ReportAbuse, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestReportAbuse_NilRepo(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	idGen, _ := id.NewGenerator("aidx")
	userSvc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	h := users.NewHandler(userSvc, nil, noteRepo, idGen)
	// abuseRepo not set
	rec := postExtra(h.ReportAbuse, `{"userId":"u2","comment":"spam"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// --- Reactions ---

func TestReactions_Success(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Reactions, `{"userId":"u1"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestReactions_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.Reactions, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// 以下 #821 PR-D で stub から本実装に書き換えた users/reactions の各 path
// を cover する。userRepo / noteReactionRepo を wire するので
// newExtraHandler とは別の構築 helper を使う。

func newReactionsHandler(t *testing.T) (*users.Handler, *testutil.MockUserRepository, *testutil.MockNoteReactionRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	idGen, _ := id.NewGenerator("aidx")
	userSvc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	h := users.NewHandler(userSvc, nil, noteRepo, idGen)
	h.SetUserRepo(userRepo)
	rxRepo := testutil.NewMockNoteReactionRepository()
	h.SetNoteReactionRepo(rxRepo)
	return h, userRepo, rxRepo
}

// target user が存在しない → 404 NO_SUCH_USER。
func TestReactions_NoSuchUser(t *testing.T) {
	h, _, _ := newReactionsHandler(t)
	rec := postExtra(h.Reactions, `{"userId":"missing"}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// target が remote user (host が non-nil) → 400 IS_REMOTE_USER。
// upstream TS は `host !== null` で判定するので、host=="" の row も remote
// 扱いになる (= mk-go も nil 判定のみ)。
func TestReactions_RemoteUser(t *testing.T) {
	h, userRepo, _ := newReactionsHandler(t)
	host := "remote.example"
	userRepo.Users["u_remote"] = &model.User{ID: "u_remote", Host: &host}
	rec := postExtra(h.Reactions, `{"userId":"u_remote"}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// publicReactions=false で viewer != target → 403 REACTIONS_NOT_PUBLIC。
func TestReactions_NotPublic(t *testing.T) {
	h, userRepo, _ := newReactionsHandler(t)
	userRepo.Users["u_target"] = &model.User{ID: "u_target"}
	userRepo.Profiles["u_target"] = &model.UserProfile{UserID: "u_target", PublicReactions: false}
	rec := postExtra(h.Reactions, `{"userId":"u_target"}`, &model.User{ID: "u_viewer"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// self view (viewer == target) なら publicReactions=false でも取得可能。
// あわせて Preload された User / Note が出力 entry に詰まること、
// idGen.ParseTime で createdAt が ISO8601 形式に整形されることも check。
func TestReactions_SelfViewBypassesPublicReactions(t *testing.T) {
	h, userRepo, rxRepo := newReactionsHandler(t)
	userRepo.Users["u_self"] = &model.User{ID: "u_self"}
	userRepo.Profiles["u_self"] = &model.UserProfile{UserID: "u_self", PublicReactions: false}
	// createdAt の format check のため real aidx を使う (= ParseTime が成功)。
	idGen, _ := id.NewGenerator("aidx")
	rxID := idGen.Generate(time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
	noteText := "hi"
	rxRepo.Reactions[rxID] = &model.NoteReaction{
		ID:       rxID,
		UserID:   "u_self",
		NoteID:   "n1",
		Reaction: "👍",
		User:     &model.User{ID: "u_self", Username: "self"},
		Note:     &model.Note{ID: "n1", UserID: "u_other", Text: &noteText},
	}
	rec := postExtra(h.Reactions, `{"userId":"u_self","limit":150}`, &model.User{ID: "u_self"})
	require.Equal(t, http.StatusOK, rec.Code)

	var list []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)
	entry := list[0]
	assert.Equal(t, rxID, entry["id"])
	assert.Equal(t, "👍", entry["type"])
	require.NotNil(t, entry["user"])
	require.NotNil(t, entry["note"])
	note, _ := entry["note"].(map[string]any)
	assert.Equal(t, "n1", note["id"])
	assert.Equal(t, "u_other", note["userId"])
	assert.Equal(t, "hi", note["text"])
	// createdAt は ISO8601 ms 精度で出る (= 2026-05-07T12:00:00.000Z)。
	createdAt, ok := entry["createdAt"].(string)
	require.True(t, ok, "createdAt should be a string")
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", createdAt)
	require.NoError(t, err, "createdAt=%q should be ISO8601 ms", createdAt)
	assert.Equal(t, 2026, parsed.Year())
}

// publicReactions=true (default) で viewer != target → list を返す。
func TestReactions_PublicReactions(t *testing.T) {
	h, userRepo, rxRepo := newReactionsHandler(t)
	userRepo.Users["u_pub"] = &model.User{ID: "u_pub"}
	userRepo.Profiles["u_pub"] = &model.UserProfile{UserID: "u_pub", PublicReactions: true}
	rxRepo.Reactions["rx2"] = &model.NoteReaction{
		ID:       "rx2",
		UserID:   "u_pub",
		NoteID:   "n2",
		Reaction: "❤",
	}
	rec := postExtra(h.Reactions, `{"userId":"u_pub"}`, &model.User{ID: "u_viewer"})
	require.Equal(t, http.StatusOK, rec.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)
}

// noteReactionRepo が wire されていない (= test stub 構成) なら空配列を
// 返す互換 path を維持する。
func TestReactions_NoteReactionRepoNotWired(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	idGen, _ := id.NewGenerator("aidx")
	userSvc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	h := users.NewHandler(userSvc, nil, noteRepo, idGen)
	h.SetUserRepo(userRepo)
	userRepo.Users["u_x"] = &model.User{ID: "u_x"}
	userRepo.Profiles["u_x"] = &model.UserProfile{UserID: "u_x", PublicReactions: true}
	// noteReactionRepo を SetNoteReactionRepo していない状態。
	rec := postExtra(h.Reactions, `{"userId":"u_x"}`, &model.User{ID: "u_x"})
	require.Equal(t, http.StatusOK, rec.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Len(t, list, 0)
}

// noteReactionRepo.ListByUserID がエラー → 500 INTERNAL_ERROR。
type failingListByUserIDRepo struct {
	*testutil.MockNoteReactionRepository
}

func (f *failingListByUserIDRepo) ListByUserID(_ string, _, _ string, _ int) ([]*model.NoteReaction, error) {
	return nil, assert.AnError
}

func TestReactions_ListError(t *testing.T) {
	h, userRepo, _ := newReactionsHandler(t)
	userRepo.Users["u_e"] = &model.User{ID: "u_e"}
	userRepo.Profiles["u_e"] = &model.UserProfile{UserID: "u_e", PublicReactions: true}
	h.SetNoteReactionRepo(&failingListByUserIDRepo{testutil.NewMockNoteReactionRepository()})
	rec := postExtra(h.Reactions, `{"userId":"u_e"}`, &model.User{ID: "u_e"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- FeaturedNotes ---

func TestFeaturedNotes_Success(t *testing.T) {
	h, _, noteRepo := newExtraHandler(t)
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1", Visibility: "public", User: &model.User{ID: "u1"}}
	rec := postExtra(h.FeaturedNotes, `{"userId":"u1"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestFeaturedNotes_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.FeaturedNotes, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- SearchByUsernameAndHost ---

func TestSearchByUsernameAndHost_Success(t *testing.T) {
	h, userRepo, _ := newExtraHandler(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}
	rec := postExtra(h.SearchByUsernameAndHost, `{"username":"alice"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSearchByUsernameAndHost_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.SearchByUsernameAndHost, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// #766: host が指定されたら当該 host の user のみ返す。host=nil なら local
// user (host IS NULL) のみ。"全 host のマッチを返す" 旧挙動の regression
// guard。
func TestSearchByUsernameAndHost_FiltersByHost(t *testing.T) {
	h, userRepo, _ := newExtraHandler(t)
	remote := "remote.example"
	other := "other.example"
	userRepo.Users["u_local"] = &model.User{ID: "u_local", Username: "alice", UsernameLower: "alice"}
	userRepo.Users["u_remote"] = &model.User{ID: "u_remote", Username: "alice", UsernameLower: "alice", Host: &remote}
	userRepo.Users["u_other"] = &model.User{ID: "u_other", Username: "alice", UsernameLower: "alice", Host: &other}

	// host 未指定 / 空文字 → local user のみ
	for _, body := range []string{`{"username":"alice"}`, `{"username":"alice","host":""}`} {
		rec := postExtra(h.SearchByUsernameAndHost, body, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var got []entity.UserLite
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got, 1, "body=%s should return only local user", body)
		assert.Equal(t, "u_local", got[0].ID)
	}

	// host 指定 → 当該 host の user のみ (local や別 host の user は除外)
	rec := postExtra(h.SearchByUsernameAndHost, `{"username":"alice","host":"remote.example"}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var got []entity.UserLite
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "u_remote", got[0].ID)

	// case-insensitive な host 比較
	rec = postExtra(h.SearchByUsernameAndHost, `{"username":"alice","host":"REMOTE.EXAMPLE"}`, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "u_remote", got[0].ID)
}

// --- UpdateMemo ---

func TestUpdateMemo_Success(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.UpdateMemo, `{"userId":"u2"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestUpdateMemo_InvalidParam(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	rec := postExtra(h.UpdateMemo, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Failing repo ---

type failingAbuseCreateRepo struct {
	*testutil.MockAbuseReportRepository
}

func (f *failingAbuseCreateRepo) Create(_ *model.AbuseUserReport) error {
	return assert.AnError
}

func TestReportAbuse_CreateError(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	h.SetAbuseRepo(&failingAbuseCreateRepo{testutil.NewMockAbuseReportRepository()})
	rec := postExtra(h.ReportAbuse, `{"userId":"u2","comment":"spam"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingListByUserRepo struct {
	*testutil.MockNoteRepository
}

func (f *failingListByUserRepo) ListByUserID(_ string, _, _ string, _ int) ([]*model.Note, error) {
	return nil, assert.AnError
}

func TestFeaturedNotes_Error(t *testing.T) {
	userRepo := testutil.NewMockUserRepository()
	noteRepo := &failingListByUserRepo{testutil.NewMockNoteRepository()}
	piningRepo := testutil.NewMockUserNotePiningRepository()
	idGen, _ := id.NewGenerator("aidx")
	userSvc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	h := users.NewHandler(userSvc, nil, noteRepo, idGen)
	rec := postExtra(h.FeaturedNotes, `{"userId":"u1"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type failingSearchRepo struct {
	*testutil.MockUserRepository
}

func (f *failingSearchRepo) SearchByUsernameAndHost(_ string, _ *string, _ int) ([]*model.User, error) {
	return nil, assert.AnError
}

func TestSearchByUsernameAndHost_Error(t *testing.T) {
	userRepo := &failingSearchRepo{testutil.NewMockUserRepository()}
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	idGen, _ := id.NewGenerator("aidx")
	userSvc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	h := users.NewHandler(userSvc, nil, noteRepo, idGen)
	rec := postExtra(h.SearchByUsernameAndHost, `{"username":"x"}`, nil)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
