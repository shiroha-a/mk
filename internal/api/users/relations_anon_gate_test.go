package users

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	coreuser "github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// countingRemoteResolver records how many times the remote resolution path was
// entered, so a test can assert that an anonymous request produced **no**
// outbound WebFinger / actor fetch at all.
type countingRemoteResolver struct {
	repo  *testutil.MockUserRepository
	calls int
}

func (r *countingRemoteResolver) ResolveByUsernameHost(username, host string) (*model.User, error) {
	r.calls++
	// 実物の resolver は解決したリモート user 行を作る。ShowByID がそれを
	// 引けるよう、stub でも repo へ入れる。
	h := host
	u := &model.User{
		ID:                "remoteResolved",
		Username:          username,
		UsernameLower:     strings.ToLower(username),
		Host:              &h,
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	r.repo.Users[u.ID] = u
	return u, nil
}

func newRelationsGateHandler(t *testing.T) (*Handler, *countingRemoteResolver) {
	t.Helper()
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	piningRepo := testutil.NewMockUserNotePiningRepository()
	fRepo := testutil.NewMockFollowingRepository()
	frRepo := testutil.NewMockFollowRequestRepository()
	idGen, _ := id.NewGenerator("aidx")
	resolver := &countingRemoteResolver{repo: userRepo}
	svc := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	svc.SetRemoteUserResolver(resolver)
	fSvc := corefollowing.NewService(userRepo, fRepo, frRepo, idGen)
	h := NewHandler(svc, fSvc, noteRepo, idGen)
	h.SetFollowingRepo(fRepo)
	h.SetFollowRequestRepo(frRepo)
	return h, resolver
}

func relationsStatus(t *testing.T, fn func(echo.Context) error, body string, viewer *model.User) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/followers", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if viewer != nil {
		c.Set(string(middleware.UserContextKey), viewer)
	}
	require.NoError(t, fn(c))
	return rec
}

// users/followers と users/following は auth middleware を持たない
// (router.go の `api.POST("/users/followers", ...)`)。`ShowByUsername` は
// ローカル DB が miss すると WebFinger + actor fetch に落ちるので、gate が
// 無いと**未認証の POST 1 回**で任意のリモート host への outbound を強制できる。
// users/show の #2106 S3 gate と同じ条件でここも塞ぐ。
func TestRelations_AnonymousRemoteLookupIsGated(t *testing.T) {
	for _, tc := range []struct {
		name string
		pick func(*Handler) func(echo.Context) error
		nsu  string
	}{
		{"followers", func(h *Handler) func(echo.Context) error { return h.Followers }, "27fa5435-88ab-43de-9360-387de88727cd"},
		{"following", func(h *Handler) func(echo.Context) error { return h.Following }, "63e4aba4-4156-4e53-be25-c9559e42d71b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, resolver := newRelationsGateHandler(t)
			h.SetUGCVisibility("local")

			rec := relationsStatus(t, tc.pick(h), `{"username":"bob","host":"remote.example"}`, nil)
			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.Equal(t, 0, resolver.calls,
				"anonymous request must not trigger any remote resolution (WebFinger + actor fetch)")

			var resp map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			errObj := resp["error"].(map[string]any)
			assert.Equal(t, "NO_SUCH_USER", errObj["code"])
			// endpoint ごとに別 id が振られている (upstream と同じ)。
			assert.Equal(t, tc.nsu, errObj["id"])
		})
	}
}

// 認証済み viewer は従来どおり解決できる (gate が広すぎないこと)。
func TestRelations_AuthenticatedRemoteLookupStillResolves(t *testing.T) {
	for _, name := range []string{"followers", "following"} {
		t.Run(name, func(t *testing.T) {
			h, resolver := newRelationsGateHandler(t)
			h.SetUGCVisibility("local")
			fn := h.Followers
			if name == "following" {
				fn = h.Following
			}
			rec := relationsStatus(t, fn, `{"username":"bob","host":"remote.example"}`, &model.User{ID: "viewer1"})
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, 1, resolver.calls)
		})
	}
}

// ugcVisibility が 'local' 以外なら gate しない (users/show と同じ条件)。
func TestRelations_GateOnlyAppliesUnderUgcVisibilityLocal(t *testing.T) {
	for _, vis := range []string{"all", ""} {
		t.Run("vis="+vis, func(t *testing.T) {
			h, resolver := newRelationsGateHandler(t)
			h.SetUGCVisibility(vis)
			rec := relationsStatus(t, h.Followers, `{"username":"bob","host":"remote.example"}`, nil)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, 1, resolver.calls)
		})
	}
}

// host 指定が無い (= ローカル利用者の) 匿名リクエストは gate されない。
// remote 解決を誘発しないので塞ぐ対象ではない。
func TestRelations_LocalLookupNotGatedForAnonymous(t *testing.T) {
	h, resolver := newRelationsGateHandler(t)
	h.SetUGCVisibility("local")
	resolver.repo.Users["local1"] = &model.User{
		ID: "local1", Username: "alice", UsernameLower: "alice",
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	rec := relationsStatus(t, h.Followers, `{"username":"alice"}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, resolver.calls)
}
