package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler handles MiAuth endpoints.
type Handler struct {
	repo  repository.AuthSessionRepository
	cfg   *config.Config
	idGen id.Generator
	// userRepo は miauth/:session/check で承認ユーザーを UserDetailedNotMe で
	// pack するために使う (#1224)。未配線時は user フィールドを省く。
	userRepo repository.UserRepository
}

// NewHandler creates a new auth handler.
func NewHandler(repo repository.AuthSessionRepository, cfg *config.Config, idGen id.Generator) *Handler {
	return &Handler{repo: repo, cfg: cfg, idGen: idGen}
}

// SetUserRepo wires the user repository used by MiAuthCheck to pack the
// approving user.
func (h *Handler) SetUserRepo(r repository.UserRepository) { h.userRepo = r }

// SessionGenerate handles POST /api/auth/session/generate.
func (h *Handler) SessionGenerate(c echo.Context) error {
	var req struct {
		AppSecret string `json:"appSecret"`
	}
	if err := c.Bind(&req); err != nil || req.AppSecret == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "appSecret is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	app, err := h.repo.FindAppBySecret(req.AppSecret)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_APP", "No such app.", "92f93e63-428e-4f2f-a5a4-39e1407fe998"))
	}

	token := uuid.New().String()
	now := time.Now()
	session := &model.AuthSession{
		ID:        h.idGen.Generate(now),
		CreatedAt: now,
		Token:     token,
		AppID:     app.ID,
	}
	if err := h.repo.CreateSession(session); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	return c.JSON(http.StatusOK, map[string]any{
		"token": token,
		"url":   fmt.Sprintf("%s/auth/%s", h.cfg.URL, token),
	})
}

// SessionShow handles POST /api/auth/session/show.
func (h *Handler) SessionShow(c echo.Context) error {
	var req struct {
		Token string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.Token == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "token is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	session, err := h.repo.FindSessionByToken(req.Token)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_SESSION", "No such session.", "bd72c97d-eba7-4adb-a467-f171b8847250"))
	}

	return c.JSON(http.StatusOK, packSession(session))
}

// Accept handles POST /api/auth/accept.
func (h *Handler) Accept(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Token string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.Token == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "token is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	session, err := h.repo.FindSessionByToken(req.Token)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_SESSION", "No such session.", "9c72d8de-391a-43c1-9d06-08d29efde8df"))
	}

	// アクセストークンが既に存在するか確認
	_, tokenErr := h.repo.FindAccessTokenByAppAndUser(session.AppID, user.ID)
	if tokenErr != nil {
		// アクセストークンを新規生成
		tokenStr := misc.SecureRandomHex(32)
		hash := sha256Hex(tokenStr + session.App.Secret)
		now := time.Now()
		accessToken := &model.AccessToken{
			ID:         h.idGen.Generate(now),
			Token:      tokenStr,
			Hash:       hash,
			UserID:     user.ID,
			AppID:      &session.AppID,
			Permission: session.App.Permission,
			LastUsedAt: &now,
		}
		if err := h.repo.CreateAccessToken(accessToken); err != nil {
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
		}
	}

	// セッションにユーザーIDを設定
	if err := h.repo.UpdateSessionUserID(session.ID, user.ID); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	return c.NoContent(http.StatusNoContent)
}

// SessionUserkey handles POST /api/auth/session/userkey.
func (h *Handler) SessionUserkey(c echo.Context) error {
	var req struct {
		AppSecret string `json:"appSecret"`
		Token     string `json:"token"`
	}
	if err := c.Bind(&req); err != nil || req.AppSecret == "" || req.Token == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "appSecret and token are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	app, err := h.repo.FindAppBySecret(req.AppSecret)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_APP", "No such app.", "fcab192a-2c5a-43b7-8ad8-9b7054d8d40d"))
	}

	session, err := h.repo.FindSessionByTokenAndAppID(req.Token, app.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_SESSION", "No such session.", "5b5a1503-8bc8-4bd0-8054-dc189e8cdcb3"))
	}

	if session.UserID == nil {
		return c.JSON(http.StatusForbidden, apierr.Error("PENDING_SESSION", "This session is not yet approved.", "8c8a4145-02cc-4cca-8e66-29ba60445a8e"))
	}

	accessToken, err := h.repo.FindAccessTokenByAppAndUser(app.ID, *session.UserID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// セッション削除（使い捨て）
	_ = h.repo.DeleteSession(session.ID)

	resp := map[string]any{
		"accessToken": accessToken.Token,
		"user":        h.packUserDetailed(session.User, *session.UserID),
	}
	return c.JSON(http.StatusOK, resp)
}

// packUserDetailed packs the approving user with the UserDetailedNotMe schema,
// matching upstream auth/session/userkey (res.user is ref 'UserDetailedNotMe',
// userEntityService.pack(session.userId, null, {schema:'UserDetailedNotMe'})).
// userRepo は router で必ず wire されるが、未配線 (unit test 等) のときは
// プロフィール無しで pack して最低限 UserDetailed shape を維持する。
func (h *Handler) packUserDetailed(u *model.User, userID string) any {
	if u == nil {
		return map[string]any{}
	}
	var profile *model.UserProfile
	if h.userRepo != nil {
		profile, _ = h.userRepo.FindProfileByUserID(userID)
	}
	return entity.PackUserDetailed(u, profile, h.idGen)
}

// GenToken handles POST /api/miauth/gen-token.
// MiAuth用のアクセストークンを直接生成する（App不要）。
func (h *Handler) GenToken(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Session     *string  `json:"session"`
		Name        *string  `json:"name"`
		Description *string  `json:"description"`
		IconURL     *string  `json:"iconUrl"`
		Permission  []string `json:"permission"`
	}
	if err := c.Bind(&req); err != nil || req.Permission == nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "permission is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	tokenStr := misc.SecureRandomHex(32)
	now := time.Now()
	accessToken := &model.AccessToken{
		ID:          h.idGen.Generate(now),
		LastUsedAt:  &now,
		Token:       tokenStr,
		Hash:        sha256Hex(tokenStr),
		UserID:      user.ID,
		Session:     req.Session,
		Name:        req.Name,
		Description: req.Description,
		IconURL:     req.IconURL,
		Permission:  req.Permission,
	}
	if err := h.repo.CreateAccessToken(accessToken); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	return c.JSON(http.StatusOK, map[string]any{"token": tokenStr})
}

// MiAuthCheck handles POST /api/miauth/:session/check.
//
// 3rd-party クライアントは MiAuth 承認 (= GenToken で session 紐付き access
// token 作成) 後、本 endpoint を polling して token を取得する。upstream
// ApiServerService の /miauth/:session/check 相当: session に対応する未取得の
// token があれば fetched=true にして {ok, token, user} を返し、無ければ
// {ok:false} を返す。認証不要 (token をまだ持たない client が叩くため)。
func (h *Handler) MiAuthCheck(c echo.Context) error {
	session := c.Param("session")
	if session == "" {
		return c.JSON(http.StatusOK, map[string]any{"ok": false})
	}
	tok, err := h.repo.FindAccessTokenBySession(session)
	if err != nil || tok == nil || tok.Session == nil || tok.Fetched {
		// session 不在 / 既に取得済 (one-time) は ok:false。
		return c.JSON(http.StatusOK, map[string]any{"ok": false})
	}
	// one-time 取得: fetched=false→true の遷移をアトミックに行い、勝った
	// リクエストだけが token を払い出す。並行 polling で同じ token が二重に
	// 渡ることを防ぐ (上の Fetched チェックは fast path で、ここが真の関門)。
	won, err := h.repo.MarkAccessTokenFetched(tok.ID)
	if err != nil || !won {
		return c.JSON(http.StatusOK, map[string]any{"ok": false})
	}

	resp := map[string]any{"ok": true, "token": tok.Token}
	if h.userRepo != nil {
		if u, uerr := h.userRepo.FindByID(tok.UserID); uerr == nil && u != nil {
			profile, _ := h.userRepo.FindProfileByUserID(u.ID)
			resp["user"] = entity.PackUserDetailed(u, profile, h.idGen)
		}
	}
	return c.JSON(http.StatusOK, resp)
}

func packSession(s *model.AuthSession) map[string]any {
	result := map[string]any{
		"id":    s.ID,
		"token": s.Token,
	}
	if s.App != nil {
		result["app"] = map[string]any{
			"id":          s.App.ID,
			"name":        s.App.Name,
			"callbackUrl": s.App.CallbackURL,
			"permission":  s.App.Permission,
		}
	}
	return result
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
