package i

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/core/notification"
	coreuser "github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/misc/id"
	miscsmtp "github.com/shiroha-a/mk/internal/misc/smtp"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

var stubError = errors.New("stub error")

// failingPiningRepo lets us trigger non-domain errors from PinNote.
type failingPiningRepo struct {
	*testutil.MockUserNotePiningRepository
}

func (f *failingPiningRepo) CountByUser(_ string) (int, error) { return 0, stubError }

// failingPiningDeleteRepo lets us trigger non-domain errors from UnpinNote
// while still allowing FindByPair to succeed (so the service reaches Delete).
type failingPiningDeleteRepo struct {
	*testutil.MockUserNotePiningRepository
}

func (f *failingPiningDeleteRepo) Delete(_ *model.UserNotePining) error {
	return stubError
}

func newHandlerWithFailingUnpinDelete(t *testing.T) (*Handler, *testutil.MockUserNotePiningRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	mock := testutil.NewMockUserNotePiningRepository()
	piningRepo := &failingPiningDeleteRepo{MockUserNotePiningRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	return NewHandler(svc, idGen), mock
}

func newHandlerWithFailingPiningCount(t *testing.T) (*Handler, *testutil.MockUserRepository, *testutil.MockNoteRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := &failingPiningRepo{MockUserNotePiningRepository: testutil.NewMockUserNotePiningRepository()}
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	return NewHandler(svc, idGen), userRepo, noteRepo
}

// failingUserRepoForUpdate forces user.UpdateUser to fail.
type failingUserRepoForUpdate struct {
	*testutil.MockUserRepository
}

func (f *failingUserRepoForUpdate) UpdateUser(_ string, _ map[string]any) error { return stubError }

func newHandlerWithFailingUpdate(t *testing.T) (*Handler, *testutil.MockUserRepository) {
	t.Helper()
	mockUR := testutil.NewMockUserRepository()
	mockUR.Users["user1"] = &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	userRepo := &failingUserRepoForUpdate{MockUserRepository: mockUR}
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	return NewHandler(svc, idGen), mockUR
}

// post is a small helper to invoke a handler with an authenticated request.
func post(h echo.HandlerFunc, body string, me *model.User) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if me != nil {
		c.Set(string(middleware.UserContextKey), me)
	}
	_ = h(c)
	return rec
}

func newTestHandler(t *testing.T) (*Handler, *testutil.MockUserRepository, *testutil.MockNoteRepository, *testutil.MockUserNotePiningRepository) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	h := NewHandler(svc, idGen)
	return h, userRepo, noteRepo, piningRepo
}

func TestMe_Success(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)

	name := "Test User"
	user := &model.User{
		ID:                "user1",
		Username:          "testuser",
		Name:              &name,
		FollowersCount:    10,
		FollowingCount:    20,
		NotesCount:        100,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
		ChatScope:         "mutual",
	}

	email := "test@example.com"
	userRepo.Profiles["user1"] = &model.UserProfile{
		UserID:              "user1",
		Email:               &email,
		EmailVerified:       true,
		TwoFactorEnabled:    false,
		AutoAcceptFollowed:  true,
		NoCrawle:            false,
		PreventAiLearning:   true,
		Fields:              datatypes.JSON([]byte("[]")),
		FollowersVisibility: model.FollowingVisibilityPublic,
		FollowingVisibility: model.FollowingVisibilityPublic,
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), user)

	err := h.Me(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, "user1", resp["id"])
	assert.Equal(t, "testuser", resp["username"])
	assert.Equal(t, "Test User", resp["name"])
	assert.Equal(t, float64(10), resp["followersCount"])
	assert.Equal(t, float64(20), resp["followingCount"])
	assert.Equal(t, float64(100), resp["notesCount"])

	// Private fields
	assert.Equal(t, "test@example.com", resp["email"])
	assert.Equal(t, true, resp["emailVerified"])
	assert.Equal(t, true, resp["autoAcceptFollowed"])
	assert.Equal(t, false, resp["twoFactorEnabled"])
	assert.Equal(t, true, resp["preventAiLearning"])

	// Hardcoded fields
	assert.Equal(t, false, resp["hasUnreadNotification"])
	assert.Equal(t, false, resp["hasPendingReceivedFollowRequest"])

	// Phase 4.5c 互換性フィールド
	assert.Equal(t, false, resp["isAdmin"])
	assert.Equal(t, false, resp["isModerator"])
	assert.Equal(t, false, resp["isDeleted"])
	assert.NotNil(t, resp["pinnedNoteIds"])
	assert.NotNil(t, resp["pinnedNotes"])
	assert.Nil(t, resp["pinnedPageId"])
	assert.NotNil(t, resp["policies"])
	assert.NotNil(t, resp["roles"])
	assert.NotNil(t, resp["achievements"])
	assert.NotNil(t, resp["unreadAnnouncements"])
	assert.Equal(t, false, resp["publicReactions"]) // profile has default false
	// C3 追加フィールド
	assert.NotNil(t, resp["avatarUrl"]) // identicon URL 自動生成
	assert.Equal(t, false, resp["hasUnreadChatMessages"])
	assert.Equal(t, "public", resp["followersVisibility"])
	assert.Equal(t, "public", resp["followingVisibility"])
	assert.Equal(t, "mutual", resp["chatScope"])
	assert.Equal(t, true, resp["canChat"])
	assert.NotNil(t, resp["verifiedLinks"])
	assert.NotNil(t, resp["securityKeysList"])
	assert.NotNil(t, resp["mutingNotificationTypes"])
	assert.Equal(t, false, resp["securityKeys"])
	assert.Nil(t, resp["movedTo"])
	assert.Nil(t, resp["alsoKnownAs"])
}

func TestMe_LoggedInDays(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)

	user := &model.User{
		ID:                "user1",
		Username:          "testuser",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
		ChatScope:         "mutual",
	}

	userRepo.Profiles["user1"] = &model.UserProfile{
		UserID:              "user1",
		Fields:              datatypes.JSON([]byte("[]")),
		LoggedInDates:       pq.StringArray{"2026-04-01", "2026-04-02", "2026-04-03"},
		FollowersVisibility: model.FollowingVisibilityPublic,
		FollowingVisibility: model.FollowingVisibilityPublic,
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), user)

	err := h.Me(c)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, float64(3), resp["loggedInDays"])
}

func TestMe_BackupCodesStock(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		codes    pq.StringArray
		expected string
	}{
		{"disabled 2FA", false, nil, "none"},
		{"enabled but no codes", true, pq.StringArray{}, "none"},
		{"enabled with partial codes", true, pq.StringArray{"code1", "code2"}, "partial"},
		{"enabled with full codes", true, pq.StringArray{"c1", "c2", "c3", "c4", "c5"}, "full"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, userRepo, _, _ := newTestHandler(t)

			user := &model.User{
				ID:                "user1",
				Username:          "testuser",
				AvatarDecorations: datatypes.JSON([]byte("[]")),
				ChatScope:         "mutual",
			}

			userRepo.Profiles["user1"] = &model.UserProfile{
				UserID:                "user1",
				Fields:                datatypes.JSON([]byte("[]")),
				TwoFactorEnabled:      tt.enabled,
				TwoFactorBackupSecret: tt.codes,
				FollowersVisibility:   model.FollowingVisibilityPublic,
				FollowingVisibility:   model.FollowingVisibilityPublic,
			}

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.Set(string(middleware.UserContextKey), user)

			err := h.Me(c)
			require.NoError(t, err)

			var resp map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tt.expected, resp["twoFactorBackupCodesStock"])
		})
	}
}

func TestJsonbArray(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected any
	}{
		{"nil input", nil, []any{}},
		{"empty input", []byte{}, []any{}},
		{"valid array", []byte(`[{"name":"test"}]`), []any{map[string]any{"name": "test"}}},
		{"invalid JSON", []byte("not json"), []any{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := jsonbArray(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMe_AvatarAndBannerIDs(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)

	avatarID := "avatar123"
	bannerID := "banner456"
	user := &model.User{
		ID:                "user1",
		Username:          "testuser",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
		ChatScope:         "mutual",
		AvatarID:          &avatarID,
		BannerID:          &bannerID,
	}

	userRepo.Profiles["user1"] = &model.UserProfile{
		UserID:              "user1",
		Fields:              datatypes.JSON([]byte("[]")),
		FollowersVisibility: model.FollowingVisibilityPublic,
		FollowingVisibility: model.FollowingVisibilityPublic,
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), user)

	err := h.Me(c)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "avatar123", resp["avatarId"])
	assert.Equal(t, "banner456", resp["bannerId"])
	assert.NotNil(t, resp["securityKeysList"])
}

// stubRoleProvider implements i.RoleProvider for testing.
type stubRoleProvider struct {
	admin     bool
	moderator bool
	silenced  bool
	roles     []*model.Role
	policies  map[string]any
}

func (s *stubRoleProvider) IsAdministrator(_ string) bool { return s.admin }
func (s *stubRoleProvider) IsModerator(_ string) bool     { return s.moderator }
func (s *stubRoleProvider) IsSilenced(_ string) bool      { return s.silenced }
func (s *stubRoleProvider) GetUserRoles(_ string) ([]*model.Role, error) {
	return s.roles, nil
}
func (s *stubRoleProvider) GetUserPolicies(_ string) map[string]any {
	if s.policies != nil {
		return s.policies
	}
	return map[string]any{}
}

func TestMe_WithRoleProvider(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetRoleProvider(&stubRoleProvider{
		admin:     true,
		moderator: true,
		roles: []*model.Role{
			{ID: "r1", Name: "Admin", IsAdministrator: true, DisplayOrder: 10},
		},
		policies: map[string]any{"gtlAvailable": true, "driveCapacityMb": 500},
	})

	user := &model.User{
		ID:                "user1",
		Username:          "admin",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), user)

	err := h.Me(c)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["isAdmin"])
	assert.Equal(t, true, resp["isModerator"])

	roles := resp["roles"].([]any)
	assert.Len(t, roles, 1)
	role := roles[0].(map[string]any)
	assert.Equal(t, "Admin", role["name"])

	policies := resp["policies"].(map[string]any)
	assert.Equal(t, float64(500), policies["driveCapacityMb"])
}

func TestMe_CreatedAtFromValidID(t *testing.T) {
	h, _, _, _ := newTestHandler(t)

	// AIDXで生成した有効なIDを使う
	idGen, _ := id.NewGenerator("aidx")
	validID := idGen.Generate(java_time())

	user := &model.User{
		ID:                validID,
		Username:          "validid",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), user)

	err := h.Me(c)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// createdAt は有効なIDから復元される
	createdAt, ok := resp["createdAt"].(string)
	require.True(t, ok, "createdAt should be a string")
	assert.Contains(t, createdAt, "T") // ISO8601 format
}

func java_time() time.Time {
	return time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
}

func TestMe_NoProfile(t *testing.T) {
	h, _, _, _ := newTestHandler(t)

	user := &model.User{
		ID:                "user1",
		Username:          "noprofile",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), user)

	err := h.Me(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, "user1", resp["id"])
	assert.Equal(t, "noprofile", resp["username"])
	// profileがない場合、private fieldsはレスポンスに含まれない
	assert.Nil(t, resp["email"])
	// ただしhardcoded fieldsは含まれる
	assert.Equal(t, false, resp["hasUnreadNotification"])
}

func TestMe_ClientDataAndRoomExposed(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)

	userRepo.Profiles["user1"] = &model.UserProfile{
		UserID:     "user1",
		ClientData: datatypes.JSON([]byte(`{"theme":"dark","sidebar":{"collapsed":true}}`)),
		Room:       datatypes.JSON([]byte(`{"furnitures":[{"id":"chair1"}]}`)),
		Fields:     datatypes.JSON([]byte("[]")),
	}

	user := &model.User{
		ID:                "user1",
		Username:          "cdroom",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), user)

	require.NoError(t, h.Me(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	cd, ok := resp["clientData"].(map[string]any)
	require.True(t, ok, "clientData should be an object, got %T", resp["clientData"])
	assert.Equal(t, "dark", cd["theme"])
	sidebar, ok := cd["sidebar"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, sidebar["collapsed"])

	room, ok := resp["room"].(map[string]any)
	require.True(t, ok, "room should be an object")
	furnitures, ok := room["furnitures"].([]any)
	require.True(t, ok)
	require.Len(t, furnitures, 1)
}

func TestMe_ClientDataAndRoomEmptyNormalized(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)

	// profile が存在するが jsonb が空/不正なケース。
	// frontend に安定した空 object を返すことを保証する。
	userRepo.Profiles["user1"] = &model.UserProfile{
		UserID:     "user1",
		ClientData: nil,
		Room:       datatypes.JSON([]byte("null")),
		Fields:     datatypes.JSON([]byte("[]")),
	}

	user := &model.User{
		ID:                "user1",
		Username:          "empty",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), user)

	require.NoError(t, h.Me(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	cd, ok := resp["clientData"].(map[string]any)
	require.True(t, ok, "clientData should normalize to {} when nil")
	assert.Empty(t, cd)

	room, ok := resp["room"].(map[string]any)
	require.True(t, ok, "room should normalize to {} when payload is JSON null")
	assert.Empty(t, room)
}

// --- Update ---

func TestUpdate_Success(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user

	rec := post(h.Update, `{"name": "New Name", "description": "hi", "location": "Tokyo", "birthday": "1990-01-01", "lang": "ja", "isLocked": true, "isBot": true, "isCat": true, "isExplorable": false, "hideOnlineStatus": true, "alwaysMarkNsfw": true, "autoSensitive": true, "noCrawle": true, "preventAiLearning": true}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdate_FollowedMessageAndPublicReactions(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	repo.Profiles["user1"] = &model.UserProfile{UserID: "user1", Fields: datatypes.JSON([]byte("[]"))}

	rec := post(h.Update, `{"followedMessage":"thanks!","publicReactions":false}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
	// profile had fields applied
	p := repo.Profiles["user1"]
	require.NotNil(t, p.FollowedMessage)
	assert.Equal(t, "thanks!", *p.FollowedMessage)
	assert.False(t, p.PublicReactions)
}

func TestUpdate_InvalidJSON(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1"}
	rec := post(h.Update, `{invalid`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// #692: chatScope を i/update で受け付け、user テーブルに反映する。
func TestUpdate_ChatScope_Persisted(t *testing.T) {
	cases := []string{"everyone", "followers", "following", "mutual", "none"}
	for _, scope := range cases {
		t.Run(scope, func(t *testing.T) {
			h, repo, _, _ := newTestHandler(t)
			user := &model.User{ID: "user1", Username: "user1", ChatScope: "mutual", AvatarDecorations: datatypes.JSON([]byte("[]"))}
			repo.Users["user1"] = user
			rec := post(h.Update, `{"chatScope":"`+scope+`"}`, user)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, scope, repo.Users["user1"].ChatScope)
		})
	}
}

func TestUpdate_ChatScope_InvalidValue(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1", Username: "user1", ChatScope: "mutual", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user
	rec := post(h.Update, `{"chatScope":"NEVER_VALID"}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "mutual", repo.Users["user1"].ChatScope, "不正値で update されないこと")
}

func TestUpdate_ChatScope_OmittedIsNoop(t *testing.T) {
	// JSON に chatScope が含まれない update は ChatScope を変更しない。
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1", Username: "user1", ChatScope: "followers", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user
	rec := post(h.Update, `{"name":"new"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "followers", repo.Users["user1"].ChatScope)
}

func TestUpdate_UserNotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	user := &model.User{ID: "ghost"}
	rec := post(h.Update, `{}`, user)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUpdate_InternalError(t *testing.T) {
	h, _ := newHandlerWithFailingUpdate(t)
	user := &model.User{ID: "user1"}
	rec := post(h.Update, `{"isLocked": true}`, user)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestUpdate_RoomAccepted(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	repo.Profiles["user1"] = &model.UserProfile{UserID: "user1", Fields: datatypes.JSON([]byte("[]"))}

	rec := post(h.Update, `{"room":{"furnitures":[{"id":"bed1"}],"seed":42}}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)

	// profile 側に反映されていること。
	got := repo.Profiles["user1"]
	require.NotNil(t, got)
	assert.JSONEq(t, `{"furnitures":[{"id":"bed1"}],"seed":42}`, string(got.Room))
}

func TestUpdate_RoomMalformedOuterJSONRejected(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user

	// 親 JSON が壊れていれば c.Bind が失敗するため 400。
	rec := post(h.Update, `{"room":{"broken":`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdate_ProhibitedWordRejected(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user

	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", ProhibitedWordsForNameOfUser: []string{"Spam", "foo"}}
	h.SetMetaRepo(metaRepo)

	// "ContainsSpamString" は "Spam" を含むのでブロック (case-insensitive)。
	rec := post(h.Update, `{"name":"ContainsSPAMString"}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdate_ProhibitedWordAllowsClear(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user

	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", ProhibitedWordsForNameOfUser: []string{"spam"}}
	h.SetMetaRepo(metaRepo)

	// 空文字 (クリア) は検査対象外 — ユーザーが表示名を外せなくなるのを避ける。
	rec := post(h.Update, `{"name":""}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdate_ProhibitedWordNoMetaSkipped(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user

	// metaRepo 未設定なら検査をスキップ。禁止ワードらしき値でも通る。
	rec := post(h.Update, `{"name":"spammer"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdate_ProhibitedWordEmptyListSkipped(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user

	metaRepo := testutil.NewMockMetaRepository()
	// 空要素だけが入っているケース: 空文字比較で全 name がヒットしないことを検査する。
	metaRepo.Meta = &model.Meta{ID: "x", ProhibitedWordsForNameOfUser: []string{"", "   "}}
	h.SetMetaRepo(metaRepo)

	rec := post(h.Update, `{"name":"anything"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdate_RoomOmittedLeavesUnchanged(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	repo.Profiles["user1"] = &model.UserProfile{
		UserID: "user1",
		Room:   datatypes.JSON([]byte(`{"keep":true}`)),
		Fields: datatypes.JSON([]byte("[]")),
	}

	rec := post(h.Update, `{"name":"renamed"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)

	got := repo.Profiles["user1"]
	assert.JSONEq(t, `{"keep":true}`, string(got.Room))
}

// avatarDecorations の検証 (#521)。全 case で stubRoleProvider と
// MockAvatarDecorationRepository を組み合わせて catalog / role / policy を
// 注入する。
func TestUpdate_AvatarDecorations_Success(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	repo.Profiles["user1"] = &model.UserProfile{UserID: "user1", Fields: datatypes.JSON([]byte("[]"))}

	deco := testutil.NewMockAvatarDecorationRepository()
	deco.Decorations["dec1"] = &model.AvatarDecoration{ID: "dec1", Name: "Hat", URL: "https://e/x.png"}
	h.SetAvatarDecorationRepo(deco)
	h.SetRoleProvider(&stubRoleProvider{policies: map[string]any{"avatarDecorationLimit": 1}})

	rec := post(h.Update, `{"avatarDecorations":[{"id":"dec1","angle":0.5,"flipH":true,"offsetX":0.1,"offsetY":-0.2}]}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)

	// 永続化された JSON が正規化済みであること。
	got := string(repo.Users["user1"].AvatarDecorations)
	assert.JSONEq(t, `[{"id":"dec1","angle":0.5,"flipH":true,"offsetX":0.1,"offsetY":-0.2}]`, got)
}

func TestUpdate_AvatarDecorations_EmptyArrayClears(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte(`[{"id":"dec1","angle":0,"flipH":false,"offsetX":0,"offsetY":0}]`)),
	}
	repo.Users["user1"] = user
	repo.Profiles["user1"] = &model.UserProfile{UserID: "user1", Fields: datatypes.JSON([]byte("[]"))}

	h.SetRoleProvider(&stubRoleProvider{policies: map[string]any{"avatarDecorationLimit": 1}})

	rec := post(h.Update, `{"avatarDecorations":[]}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)

	assert.JSONEq(t, `[]`, string(repo.Users["user1"].AvatarDecorations))
}

func TestUpdate_AvatarDecorations_DefaultsAppliedWhenFieldsOmitted(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	repo.Profiles["user1"] = &model.UserProfile{UserID: "user1", Fields: datatypes.JSON([]byte("[]"))}

	deco := testutil.NewMockAvatarDecorationRepository()
	deco.Decorations["dec1"] = &model.AvatarDecoration{ID: "dec1"}
	h.SetAvatarDecorationRepo(deco)
	h.SetRoleProvider(&stubRoleProvider{policies: map[string]any{"avatarDecorationLimit": 1}})

	// id だけ指定 — 残り全フィールドは 0 / false で埋まる。
	rec := post(h.Update, `{"avatarDecorations":[{"id":"dec1"}]}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `[{"id":"dec1","angle":0,"flipH":false,"offsetX":0,"offsetY":0}]`, string(repo.Users["user1"].AvatarDecorations))
}

func TestUpdate_AvatarDecorations_ExceedsLimit(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	deco := testutil.NewMockAvatarDecorationRepository()
	deco.Decorations["dec1"] = &model.AvatarDecoration{ID: "dec1"}
	deco.Decorations["dec2"] = &model.AvatarDecoration{ID: "dec2"}
	h.SetAvatarDecorationRepo(deco)
	h.SetRoleProvider(&stubRoleProvider{policies: map[string]any{"avatarDecorationLimit": 1}})

	rec := post(h.Update, `{"avatarDecorations":[{"id":"dec1"},{"id":"dec2"}]}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "TOO_MANY_AVATAR_DECORATIONS")
}

func TestUpdate_AvatarDecorations_UnknownIDRejected(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	deco := testutil.NewMockAvatarDecorationRepository()
	h.SetAvatarDecorationRepo(deco)
	h.SetRoleProvider(&stubRoleProvider{policies: map[string]any{"avatarDecorationLimit": 1}})

	rec := post(h.Update, `{"avatarDecorations":[{"id":"missing"}]}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_AVATAR_DECORATION")
}

func TestUpdate_AvatarDecorations_RestrictedByRole(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	deco := testutil.NewMockAvatarDecorationRepository()
	deco.Decorations["dec1"] = &model.AvatarDecoration{ID: "dec1", RoleIDs: pq.StringArray{"role-vip"}}
	h.SetAvatarDecorationRepo(deco)
	// ユーザーは role-other のみ所持 → role-vip 必須の dec1 は弾かれる。
	h.SetRoleProvider(&stubRoleProvider{
		roles:    []*model.Role{{ID: "role-other"}},
		policies: map[string]any{"avatarDecorationLimit": 1},
	})

	rec := post(h.Update, `{"avatarDecorations":[{"id":"dec1"}]}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "RESTRICTED_BY_ROLE")
}

func TestUpdate_AvatarDecorations_RoleAllowed(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	repo.Profiles["user1"] = &model.UserProfile{UserID: "user1", Fields: datatypes.JSON([]byte("[]"))}
	deco := testutil.NewMockAvatarDecorationRepository()
	deco.Decorations["dec1"] = &model.AvatarDecoration{ID: "dec1", RoleIDs: pq.StringArray{"role-vip"}}
	h.SetAvatarDecorationRepo(deco)
	h.SetRoleProvider(&stubRoleProvider{
		roles:    []*model.Role{{ID: "role-vip"}, {ID: "role-other"}},
		policies: map[string]any{"avatarDecorationLimit": 1},
	})

	rec := post(h.Update, `{"avatarDecorations":[{"id":"dec1"}]}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdate_AvatarDecorations_EmptyIDRejected(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1", Username: "user1", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user
	h.SetRoleProvider(&stubRoleProvider{policies: map[string]any{"avatarDecorationLimit": 5}})

	rec := post(h.Update, `{"avatarDecorations":[{"id":""}]}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdate_AvatarDecorations_OmittedLeavesUnchanged(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	prev := datatypes.JSON([]byte(`[{"id":"dec1","angle":0,"flipH":false,"offsetX":0,"offsetY":0}]`))
	user := &model.User{ID: "user1", Username: "user1", AvatarDecorations: prev}
	repo.Users["user1"] = user
	repo.Profiles["user1"] = &model.UserProfile{UserID: "user1", Fields: datatypes.JSON([]byte("[]"))}

	rec := post(h.Update, `{"name":"x"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, string(prev), string(repo.Users["user1"].AvatarDecorations))
}

func TestUpdate_AvatarDecorations_NoRepoSkipsCatalogCheck(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1", Username: "user1", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user
	repo.Profiles["user1"] = &model.UserProfile{UserID: "user1", Fields: datatypes.JSON([]byte("[]"))}
	h.SetRoleProvider(&stubRoleProvider{policies: map[string]any{"avatarDecorationLimit": 1}})

	// AvatarDecorationRepo 未配線 → catalog 検証 skip。任意 id を受け付ける fallback。
	rec := post(h.Update, `{"avatarDecorations":[{"id":"anything"}]}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// role.GetUserPolicies は jsonb override を json.Unmarshal で any に
// 展開するため、数値は float64 で入ってくる。int 限定 assertion だと
// override が silent fallback して既定 limit=1 のままになる回帰を防ぐ。
func TestUpdate_AvatarDecorations_RoleOverrideFloatLimitAccepted(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1", Username: "user1", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user
	repo.Profiles["user1"] = &model.UserProfile{UserID: "user1", Fields: datatypes.JSON([]byte("[]"))}
	deco := testutil.NewMockAvatarDecorationRepository()
	deco.Decorations["a"] = &model.AvatarDecoration{ID: "a"}
	deco.Decorations["b"] = &model.AvatarDecoration{ID: "b"}
	deco.Decorations["c"] = &model.AvatarDecoration{ID: "c"}
	h.SetAvatarDecorationRepo(deco)
	// jsonb override 経由を模倣して float64 を渡す。
	h.SetRoleProvider(&stubRoleProvider{policies: map[string]any{"avatarDecorationLimit": float64(3)}})

	rec := post(h.Update, `{"avatarDecorations":[{"id":"a"},{"id":"b"},{"id":"c"}]}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdate_AvatarDecorations_NoRoleProviderUsesDefaultLimit(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1", Username: "user1", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user

	// roleProvider 未配線 → default limit=1。2 件渡すと 400。
	rec := post(h.Update, `{"avatarDecorations":[{"id":"a"},{"id":"b"}]}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Pin ---

func TestPin_Success(t *testing.T) {
	h, repo, noteRepo, _ := newTestHandler(t)
	user := &model.User{
		ID:                "user1",
		Username:          "user1",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	repo.Users["user1"] = user
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "user1"}

	rec := post(h.Pin, `{"noteId": "n1"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestPin_NoteNotFound(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user

	rec := post(h.Pin, `{"noteId": "ghost"}`, user)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPin_AlreadyPinned(t *testing.T) {
	h, repo, noteRepo, _ := newTestHandler(t)
	user := &model.User{ID: "user1", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "user1"}
	post(h.Pin, `{"noteId": "n1"}`, user)

	rec := post(h.Pin, `{"noteId": "n1"}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPin_LimitExceeded(t *testing.T) {
	h, repo, noteRepo, _ := newTestHandler(t)
	user := &model.User{ID: "user1", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user
	for i := 1; i <= coreuser.MaxPinnedNotes; i++ {
		nid := "n" + string(rune('0'+i))
		noteRepo.Notes[nid] = &model.Note{ID: nid, UserID: "user1"}
		post(h.Pin, `{"noteId": "`+nid+`"}`, user)
	}
	noteRepo.Notes["nx"] = &model.Note{ID: "nx", UserID: "user1"}

	rec := post(h.Pin, `{"noteId": "nx"}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPin_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1"}
	rec := post(h.Pin, `{}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPin_InternalError(t *testing.T) {
	h, repo, noteRepo := newHandlerWithFailingPiningCount(t)
	user := &model.User{ID: "user1", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "user1"}

	rec := post(h.Pin, `{"noteId": "n1"}`, user)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Unpin ---

func TestUnpin_Success(t *testing.T) {
	h, repo, noteRepo, _ := newTestHandler(t)
	user := &model.User{ID: "user1", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "user1"}
	post(h.Pin, `{"noteId": "n1"}`, user)

	rec := post(h.Unpin, `{"noteId": "n1"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUnpin_NotFound(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	repo.Users["user1"] = user

	rec := post(h.Unpin, `{"noteId": "n1"}`, user)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnpin_InvalidParam(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	user := &model.User{ID: "user1"}
	rec := post(h.Unpin, `{}`, user)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnpin_InternalError(t *testing.T) {
	h, mock := newHandlerWithFailingUnpinDelete(t)
	mock.Pinings["p1"] = &model.UserNotePining{ID: "p1", UserID: "user1", NoteID: "n1"}
	user := &model.User{ID: "user1"}

	rec := post(h.Unpin, `{"noteId": "n1"}`, user)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Registry endpoints ---

func newHandlerWithRegistry(t *testing.T) (*Handler, *testutil.MockRegistryRepository) {
	t.Helper()
	h, _, _, _ := newTestHandler(t)
	regRepo := testutil.NewMockRegistryRepository()
	h.SetRegistryRepo(regRepo)
	return h, regRepo
}

func TestRegistrySet_Success(t *testing.T) {
	h, regRepo := newHandlerWithRegistry(t)
	user := &model.User{ID: "u1"}
	rec := post(h.RegistrySet, `{"key":"theme","value":"dark"}`, user)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Len(t, regRepo.Items, 1)
}

func TestRegistrySet_InvalidParam(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	rec := post(h.RegistrySet, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRegistrySet_PublishesRegistryUpdated_WhenDomainNil(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	pub := &stubIMainStreamPublisher{}
	h.SetMainStreamPublisher(pub)

	rec := post(h.RegistrySet, `{"key":"theme","value":"dark","scope":["client"]}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Len(t, pub.calls, 1)
	assert.Equal(t, "u1", pub.calls[0].userID)
	assert.Equal(t, "registryUpdated", pub.calls[0].eventType)
	body, ok := pub.calls[0].body.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "theme", body["key"])
	// scope は []string で渡される
	scope, ok := body["scope"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"client"}, scope)
	// value は json.RawMessage (生バイトを frontend にそのまま渡すため)
	val, ok := body["value"].(json.RawMessage)
	require.True(t, ok)
	assert.Equal(t, `"dark"`, string(val))
}

func TestRegistrySet_SkipsPublishWhenDomainSpecified(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	pub := &stubIMainStreamPublisher{}
	h.SetMainStreamPublisher(pub)

	// domain指定時はTS本家同様emitしない(サードパーティアプリ領域)
	rec := post(h.RegistrySet, `{"key":"k","value":"v","domain":"app.example"}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, pub.calls)
}

func TestRegistrySet_NoPublisher_NoEmit(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	// publisher 未設定でも通常動作すること
	rec := post(h.RegistrySet, `{"key":"k","value":"v"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRegistryGet_Success(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	user := &model.User{ID: "u1"}
	post(h.RegistrySet, `{"key":"lang","value":"ja"}`, user)
	rec := post(h.RegistryGet, `{"key":"lang"}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRegistryGet_NotFound(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	rec := post(h.RegistryGet, `{"key":"ghost"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestRegistryGet_InvalidParam(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	rec := post(h.RegistryGet, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRegistryGetAll_Success(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	user := &model.User{ID: "u1"}
	post(h.RegistrySet, `{"key":"k1","value":1}`, user)
	post(h.RegistrySet, `{"key":"k2","value":2}`, user)
	rec := post(h.RegistryGetAll, `{}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRegistryGetAll_InvalidJSON(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	rec := post(h.RegistryGetAll, `invalid`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRegistryKeysWithType_Success(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	user := &model.User{ID: "u1"}
	post(h.RegistrySet, `{"key":"theme","value":"dark"}`, user)
	rec := post(h.RegistryKeysWithType, `{}`, user)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRegistryKeysWithType_InvalidJSON(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	rec := post(h.RegistryKeysWithType, `invalid`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRegistryRemove_Success(t *testing.T) {
	h, regRepo := newHandlerWithRegistry(t)
	user := &model.User{ID: "u1"}
	post(h.RegistrySet, `{"key":"temp","value":true}`, user)
	assert.Len(t, regRepo.Items, 1)
	rec := post(h.RegistryRemove, `{"key":"temp"}`, user)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, regRepo.Items)
}

type failingSetRegistryRepo struct {
	*testutil.MockRegistryRepository
}

func (f *failingSetRegistryRepo) Set(_ *model.RegistryItem) error { return stubError }

type failingGetAllRegistryRepo struct {
	*testutil.MockRegistryRepository
}

func (f *failingGetAllRegistryRepo) GetAll(_ string, _ []string, _ *string) ([]*model.RegistryItem, error) {
	return nil, stubError
}

func (f *failingGetAllRegistryRepo) KeysWithType(_ string, _ []string, _ *string) (map[string]string, error) {
	return nil, stubError
}

type failingRemoveRegistryRepo struct {
	*testutil.MockRegistryRepository
}

func (f *failingRemoveRegistryRepo) Remove(_ string, _ string, _ []string, _ *string) error {
	return stubError
}

func TestRegistrySet_Error(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetRegistryRepo(&failingSetRegistryRepo{testutil.NewMockRegistryRepository()})
	rec := post(h.RegistrySet, `{"key":"k","value":1}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRegistryGetAll_Error(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetRegistryRepo(&failingGetAllRegistryRepo{testutil.NewMockRegistryRepository()})
	rec := post(h.RegistryGetAll, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRegistryKeysWithType_Error(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetRegistryRepo(&failingGetAllRegistryRepo{testutil.NewMockRegistryRepository()})
	rec := post(h.RegistryKeysWithType, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRegistryRemove_Error(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetRegistryRepo(&failingRemoveRegistryRepo{testutil.NewMockRegistryRepository()})
	rec := post(h.RegistryRemove, `{"key":"k"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRegistryRemove_InvalidParam(t *testing.T) {
	h, _ := newHandlerWithRegistry(t)
	rec := post(h.RegistryRemove, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Handler setters ---

func TestSetServerURL(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetServerURL("https://example.com")
}

func TestSetEmailSender(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmailSender(func(_ string, _ miscsmtp.Message) {})
}

func TestSetEmailValidationClient(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmailValidationClient(&http.Client{})
}

// --- Phase 7-2 (#244): 未読系フィールド実装 ---

// stubUnreadNotification is a local UnreadNotificationSource for tests.
type stubUnreadNotification struct {
	count        int64
	hasTypes     bool
	hasSpecified bool
	gotTypesArgs []notification.Type
}

func (s *stubUnreadNotification) UnreadSummary(_ context.Context, _ string, types []notification.Type) (notification.UnreadSummary, error) {
	s.gotTypesArgs = types
	return notification.UnreadSummary{
		TotalCount:       s.count,
		HasMentions:      s.hasTypes,
		HasSpecifiedNote: s.hasSpecified,
	}, nil
}

type stubAnnouncementRepo struct {
	rows []*model.Announcement
}

func (s *stubAnnouncementRepo) UnreadForUser(_ string) ([]*model.Announcement, error) {
	return s.rows, nil
}

type stubChatRepo struct{ n int64 }

func (s *stubChatRepo) CountUnread(_ string) (int64, error) { return s.n, nil }

// runMe invokes h.Me with a minimal user + profile seeded and returns the
// decoded response.
func runMe(t *testing.T, h *Handler, userRepo *testutil.MockUserRepository, userID string) map[string]any {
	t.Helper()
	u := &model.User{
		ID: userID, Username: "u", AvatarDecorations: datatypes.JSON([]byte("[]")),
		ChatScope: "mutual",
	}
	userRepo.Profiles[userID] = &model.UserProfile{
		UserID: userID, Fields: datatypes.JSON([]byte("[]")),
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), u)
	require.NoError(t, h.Me(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func TestMe_UnreadNotification_Populated(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	stub := &stubUnreadNotification{count: 3, hasTypes: true}
	h.SetNotificationService(stub)

	resp := runMe(t, h, userRepo, "u1")
	assert.Equal(t, float64(3), resp["unreadNotificationsCount"])
	assert.Equal(t, true, resp["hasUnreadNotification"])
	assert.Equal(t, true, resp["hasUnreadMentions"])
	assert.Equal(t, []notification.Type{notification.TypeMention, notification.TypeReply}, stub.gotTypesArgs)
}

func TestMe_UnreadNotification_ZeroCount(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	h.SetNotificationService(&stubUnreadNotification{count: 0, hasTypes: false})

	resp := runMe(t, h, userRepo, "u1")
	assert.Equal(t, float64(0), resp["unreadNotificationsCount"])
	assert.Equal(t, false, resp["hasUnreadNotification"])
	assert.Equal(t, false, resp["hasUnreadMentions"])
}

func TestMe_PendingFollowRequest(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	frRepo := testutil.NewMockFollowRequestRepository()
	require.NoError(t, frRepo.Create(&model.FollowRequest{ID: "fr1", FollowerID: "other", FolloweeID: "u1"}))
	h.SetFollowRequestRepo(frRepo)

	resp := runMe(t, h, userRepo, "u1")
	assert.Equal(t, true, resp["hasPendingReceivedFollowRequest"])
}

func TestMe_UnreadAnnouncement(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	h.SetAnnouncementRepo(&stubAnnouncementRepo{
		rows: []*model.Announcement{{ID: "a1", Title: "hello", Text: "world"}},
	})

	resp := runMe(t, h, userRepo, "u1")
	assert.Equal(t, true, resp["hasUnreadAnnouncement"])
	arr := resp["unreadAnnouncements"].([]any)
	assert.Len(t, arr, 1)
	first := arr[0].(map[string]any)
	assert.Equal(t, "a1", first["id"])
	assert.Equal(t, "hello", first["title"])
}

func TestMe_UnreadAnnouncement_Empty(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	h.SetAnnouncementRepo(&stubAnnouncementRepo{rows: nil})

	resp := runMe(t, h, userRepo, "u1")
	assert.Equal(t, false, resp["hasUnreadAnnouncement"])
	arr := resp["unreadAnnouncements"].([]any)
	assert.Empty(t, arr)
}

func TestMe_UnreadChatMessages(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	h.SetChatRepo(&stubChatRepo{n: 5})

	resp := runMe(t, h, userRepo, "u1")
	assert.Equal(t, true, resp["hasUnreadChatMessages"])
}

func TestMe_UnreadFieldsDefaults(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	resp := runMe(t, h, userRepo, "u1")
	assert.Equal(t, float64(0), resp["unreadNotificationsCount"])
	assert.Equal(t, false, resp["hasUnreadNotification"])
	assert.Equal(t, false, resp["hasUnreadMentions"])
	assert.Equal(t, false, resp["hasPendingReceivedFollowRequest"])
	assert.Equal(t, false, resp["hasUnreadAnnouncement"])
	assert.Equal(t, false, resp["hasUnreadChatMessages"])
	assert.Equal(t, false, resp["hasUnreadAntenna"])
	assert.Equal(t, false, resp["hasUnreadChannel"])
	assert.Equal(t, false, resp["hasUnreadSpecifiedNotes"])
}

// stubUnreadSource is a minimal AntennaUnread / ChannelUnreadSource stub.
type stubUnreadSource struct{ has bool }

func (s *stubUnreadSource) HasAnyByUser(_ string) (bool, error) { return s.has, nil }

func TestMe_UnreadAntennaAndChannel_Populated(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	h.SetAntennaUnreadRepo(&stubUnreadSource{has: true})
	h.SetChannelUnreadRepo(&stubUnreadSource{has: true})
	resp := runMe(t, h, userRepo, "u1")
	assert.Equal(t, true, resp["hasUnreadAntenna"])
	assert.Equal(t, true, resp["hasUnreadChannel"])
}

func TestMe_HasUnreadSpecifiedNotes_Populated(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	h.SetNotificationService(&stubUnreadNotification{hasSpecified: true})
	resp := runMe(t, h, userRepo, "u1")
	assert.Equal(t, true, resp["hasUnreadSpecifiedNotes"])
}

// --- Phase 7-3 (#245): pinnedNotes / pinnedPage ---

func TestMe_PinnedNoteIDs_Populated(t *testing.T) {
	h, userRepo, noteRepo, _ := newTestHandler(t)

	piningRepo := testutil.NewMockUserNotePiningRepository()
	require.NoError(t, piningRepo.Create(&model.UserNotePining{ID: "p1", UserID: "u1", NoteID: "note_a"}))
	require.NoError(t, piningRepo.Create(&model.UserNotePining{ID: "p2", UserID: "u1", NoteID: "note_b"}))
	h.SetPiningRepo(piningRepo)

	// noteRepo に 2 件の note を挿入 (FindManyByIDsWithUser の戻り対象)
	txtA := "a"
	noteRepo.Notes["note_a"] = &model.Note{ID: "note_a", UserID: "u1", Text: &txtA, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}"))}
	txtB := "b"
	noteRepo.Notes["note_b"] = &model.Note{ID: "note_b", UserID: "u1", Text: &txtB, Visibility: model.NoteVisibilityPublic, Reactions: datatypes.JSON([]byte("{}"))}
	h.SetNoteRepo(noteRepo)

	resp := runMe(t, h, userRepo, "u1")

	ids, ok := resp["pinnedNoteIds"].([]any)
	require.True(t, ok)
	assert.Len(t, ids, 2)

	notes, ok := resp["pinnedNotes"].([]any)
	require.True(t, ok)
	assert.Len(t, notes, 2)
}

func TestMe_PinnedNoteIDs_EmptyDefault(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	// 依存 未wire → default empty
	resp := runMe(t, h, userRepo, "u1")
	ids, ok := resp["pinnedNoteIds"].([]any)
	require.True(t, ok)
	assert.Empty(t, ids)
	notes, _ := resp["pinnedNotes"].([]any)
	assert.Empty(t, notes)
}

// stubPageRepo to avoid DB.
type stubPageRepo struct {
	page *model.Page
	err  error
}

func (s *stubPageRepo) Create(*model.Page) error                              { return nil }
func (s *stubPageRepo) FindByID(id string) (*model.Page, error)               { return s.page, s.err }
func (s *stubPageRepo) FindByUserAndName(string, string) (*model.Page, error) { return nil, nil }
func (s *stubPageRepo) UpdateFields(string, map[string]any) error             { return nil }
func (s *stubPageRepo) Delete(*model.Page) error                              { return nil }
func (s *stubPageRepo) ListByUser(string, string, string, int, int) ([]*model.Page, error) {
	return nil, nil
}
func (s *stubPageRepo) ListPublicByUser(string, string, string, int, int) ([]*model.Page, error) {
	return nil, nil
}
func (s *stubPageRepo) ListFeatured(string, string, int, int) ([]*model.Page, error) { return nil, nil }
func (s *stubPageRepo) IncrementCount(string, string, int) error                     { return nil }

func TestMe_PinnedPage_Populated(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)

	pageID := "page_123"
	userRepo.Profiles["u1"] = &model.UserProfile{
		UserID:       "u1",
		Fields:       datatypes.JSON([]byte("[]")),
		PinnedPageID: &pageID,
	}

	h.SetPageRepo(&stubPageRepo{page: &model.Page{ID: pageID, Title: "pinned", UserID: "u1"}})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/i", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(middleware.UserContextKey), &model.User{
		ID: "u1", Username: "u", AvatarDecorations: datatypes.JSON([]byte("[]")),
		ChatScope: "mutual",
	})
	require.NoError(t, h.Me(c))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, pageID, resp["pinnedPageId"])
	pg, ok := resp["pinnedPage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "pinned", pg["title"])
}

func TestMe_PinnedPage_NoPinnedPageID(t *testing.T) {
	h, userRepo, _, _ := newTestHandler(t)
	resp := runMe(t, h, userRepo, "u1")
	assert.Nil(t, resp["pinnedPageId"])
	assert.Nil(t, resp["pinnedPage"])
}

// #739: 多くの setter (SetNoteFieldResolver / SetInstanceRepo / SetEmojiRepo /
// SetReactionReader) は wire のみで lookup の non-nil 分岐は別パスで踏む。
// ここでは構築 + setter 呼び出しのみで panic しないことを担保する。
func TestSetters_NotPanic(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetNoteFieldResolver(nil)
	h.SetInstanceRepo(testutil.NewMockInstanceRepository())
	h.SetEmojiRepo(testutil.NewMockEmojiRepository())
	h.SetReactionReader(stubBufferedReactions{})
}

// stubBufferedReactions implements entity.BufferedReactionsReader as a no-op
// for setter wiring tests (#739)。
type stubBufferedReactions struct{}

func (stubBufferedReactions) GetBufferedMany(_ context.Context, _ []string) (map[string]map[string]int64, error) {
	return map[string]map[string]int64{}, nil
}

// #739: verifyURL は serverURL fallback と path 結合の単純関数だが UpdateEmail
// 経路で email 送信が起きないと cover されない。直接呼んで両分岐 (serverURL
// 未設定 / 設定済み) を担保する。
func TestVerifyURL(t *testing.T) {
	h := &Handler{}
	// serverURL 未設定 → https://localhost フォールバック
	assert.Equal(t, "https://localhost/verify-email/abc", h.verifyURL("abc"))

	h2 := &Handler{serverURL: "https://example.com"}
	assert.Equal(t, "https://example.com/verify-email/abc", h2.verifyURL("abc"))
}
