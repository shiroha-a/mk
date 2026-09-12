// Package emojiapplications exposes the applicant-facing half of custom emoji
// registration requests (#2934).
//
// モデレーター側は internal/api/admin にある。
package emojiapplications

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/emojiapplication"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler serves the applicant-facing endpoints.
type Handler struct {
	svc  *emojiapplication.Service
	apps repository.EmojiApplicationRepository
	// files resolves the drive file so the list can show a preview without a
	// second round trip.
	//
	// **広い interface を取らない。** repository.DriveFileRepository は CRUD を
	// 一式持つが、ここで要るのは 1 件の解決だけ。狭く取ると依存の実態が読んで
	// 分かり、テストの偽物が本物の面を全部埋める必要も無くなる。
	files DriveFileLookup
}

// DriveFileLookup resolves a drive file for the preview image.
type DriveFileLookup interface {
	FindByID(id string) (*model.DriveFile, error)
}

// NewHandler creates the handler. A nil svc disables the endpoints, which is
// how a deployment that has not wired the feature behaves.
func NewHandler(
	svc *emojiapplication.Service,
	apps repository.EmojiApplicationRepository,
	files DriveFileLookup,
) *Handler {
	return &Handler{svc: svc, apps: apps, files: files}
}

// Create handles POST /api/emoji-application/create.
func (h *Handler) Create(c echo.Context) error {
	if h.svc == nil {
		return apierr.JSONInternalError(c)
	}
	me := middleware.GetUser(c)
	if me == nil {
		return c.JSON(http.StatusUnauthorized, apierr.Error(
			"CREDENTIAL_REQUIRED", "Credential required.",
			"1384574d-a912-4b81-8601-c7b1c4085df1"))
	}

	var req struct {
		Name        string   `json:"name"`
		Category    string   `json:"category"`
		Aliases     []string `json:"aliases"`
		License     string   `json:"license"`
		IsSensitive bool     `json:"isSensitive"`
		FileID      string   `json:"fileId"`
		// kind = remote (#2935)。どの絵文字を取り込むかを host + name で持つ。
		// **emoji の行 ID では持たない** — リモート絵文字の行はキャッシュに
		// 近く、審査を待つ間に消えると申請ごと無意味になる。
		Kind       string `json:"kind"`
		RemoteHost string `json:"remoteHost"`
		RemoteName string `json:"remoteName"`
		Comment    string `json:"comment"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"INVALID_PARAM", "Invalid param.",
			"3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	app, err := h.svc.Create(emojiapplication.CreateInput{
		UserID:      me.ID,
		Name:        req.Name,
		Category:    req.Category,
		Aliases:     req.Aliases,
		License:     req.License,
		IsSensitive: req.IsSensitive,
		FileID:      req.FileID,
		Kind:        req.Kind,
		RemoteHost:  req.RemoteHost,
		RemoteName:  req.RemoteName,
		Comment:     req.Comment,
	})
	if err != nil {
		return h.createError(c, err)
	}
	return c.JSON(http.StatusOK, h.pack(app))
}

// createError maps service errors onto the wire.
//
// **種別を潰さない。** すべて 400 INVALID_PARAM に丸めると、利用者には
// 「何か間違っている」としか伝わらず、名前を直せばよいのか別の問題なのかが
// 分からない。DB 障害は 500 のまま残す (#2792 と同じ理由)。
func (h *Handler) createError(c echo.Context, err error) error {
	// 期間上限は sentinel ではなく値を持つので errors.As で受ける (#2958)。
	var quota *emojiapplication.QuotaExceededError
	switch {
	case errors.As(err, &quota):
		return h.quotaExceeded(c, quota)
	case errors.Is(err, emojiapplication.ErrInvalidName):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"INVALID_EMOJI_NAME", "Emoji name must match ^[a-zA-Z0-9_]+$.",
			"6c1bd2b1-1c3e-4f6f-9b0c-8f5e0f2a3d11"))
	case errors.Is(err, emojiapplication.ErrLicenseRequired):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"LICENSE_REQUIRED", "License is required for a request.",
			"7f3b0a8e-5a2d-4a1b-9d4e-2c7a6b5f0e22"))
	case errors.Is(err, emojiapplication.ErrFileRequired):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"NO_SUCH_FILE", "fileId is required.",
			"fc46b5a4-6b92-4c33-ac66-b806659bb5cf"))
	case errors.Is(err, emojiapplication.ErrTooLong):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"TOO_LONG", "A field is too long.",
			"8c5e2f60-1a4d-4b93-8e77-3d0f6a2b9c66"))
	case errors.Is(err, emojiapplication.ErrUnsupportedFileType):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"UNSUPPORTED_FILE_TYPE", "Unsupported file type.",
			"f7599d96-8750-af68-1633-9575d625c1a7"))
	case errors.Is(err, emojiapplication.ErrFileGone):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"NO_SUCH_FILE", "No such file.",
			"fc46b5a4-6b92-4c33-ac66-b806659bb5cf"))
	case errors.Is(err, emojiapplication.ErrInvalidKind):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"INVALID_PARAM", "Unknown kind.",
			"3d81ceae-475f-4600-b2a8-2bc116157532"))
	case errors.Is(err, emojiapplication.ErrRemoteRequired):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"INVALID_PARAM", "remoteHost and remoteName are required.",
			"3d81ceae-475f-4600-b2a8-2bc116157532"))
	case errors.Is(err, emojiapplication.ErrNoSuchRemoteEmoji):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"NO_SUCH_EMOJI", "No such emoji.",
			"e2785b66-dca3-4087-9cac-b93c541cc425"))
	case errors.Is(err, emojiapplication.ErrDuplicateName):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"DUPLICATE_NAME", "Duplicate name.",
			"f7a3462c-4e6e-4069-8421-b9bd4f4c3975"))
	case errors.Is(err, emojiapplication.ErrAlreadyPending):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"ALREADY_REQUESTED", "You already have a pending request for this name.",
			"9a0d4c51-3b6e-4a77-88b0-5f2c1d8e4a33"))
	default:
		return apierr.JSONInternalError(c)
	}
}

// ListMine handles POST /api/emoji-application/list-mine.
func (h *Handler) ListMine(c echo.Context) error {
	if h.apps == nil {
		return apierr.JSONInternalError(c)
	}
	me := middleware.GetUser(c)
	if me == nil {
		return c.JSON(http.StatusUnauthorized, apierr.Error(
			"CREDENTIAL_REQUIRED", "Credential required.",
			"1384574d-a912-4b81-8601-c7b1c4085df1"))
	}
	var req struct {
		Limit   int    `json:"limit"`
		UntilID string `json:"untilId"`
	}
	_ = c.Bind(&req)
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 30
	}

	rows, err := h.apps.ListByUser(me.ID, req.Limit, req.UntilID)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		out = append(out, h.pack(&rows[i]))
	}
	return c.JSON(http.StatusOK, out)
}

// Cancel handles POST /api/emoji-application/cancel.
func (h *Handler) Cancel(c echo.Context) error {
	if h.svc == nil {
		return apierr.JSONInternalError(c)
	}
	me := middleware.GetUser(c)
	if me == nil {
		return c.JSON(http.StatusUnauthorized, apierr.Error(
			"CREDENTIAL_REQUIRED", "Credential required.",
			"1384574d-a912-4b81-8601-c7b1c4085df1"))
	}
	var req struct {
		ApplicationID string `json:"applicationId"`
	}
	if err := c.Bind(&req); err != nil || req.ApplicationID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"INVALID_PARAM", "applicationId is required.",
			"3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	switch err := h.svc.Cancel(req.ApplicationID, me.ID); {
	case err == nil:
		return c.NoContent(http.StatusNoContent)
	// **他人の申請は「無い」と答える。** ErrForbidden をそのまま 403 で返すと、
	// 存在する申請 ID を総当たりで数えられる。
	case errors.Is(err, emojiapplication.ErrNotFound), errors.Is(err, emojiapplication.ErrForbidden):
		return c.JSON(http.StatusNotFound, apierr.Error(
			"NO_SUCH_APPLICATION", "No such application.",
			"2b7e0c94-6d1a-4f3e-b5c8-0a9d3e7f1b44"))
	case errors.Is(err, emojiapplication.ErrNotPending):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"ALREADY_PROCESSED", "This request was already processed.",
			"4e6f1a37-9c2b-4d80-a1f5-7b3c8e0d2a55"))
	default:
		return apierr.JSONInternalError(c)
	}
}

// quotaExceeded renders the rolling-window rejection (#2958).
//
// **429 にする。** 上限に達しただけで申請の内容は正しいので、400 に倒すと
// 利用者は入力を直そうとして無駄に試行する。`Retry-After` も付けて、いつ
// 空くかを機械可読にしておく (既存の 1 時間 rate limit と同じ形)。
func (h *Handler) quotaExceeded(c echo.Context, q *emojiapplication.QuotaExceededError) error {
	// **切り上げる。** 切り捨てると「Retry-After 秒後」に叩いてもまだ窓の
	// 中にいて、もう一度 429 を返すことになる。
	retryAfter := int64(math.Ceil(time.Until(q.RetryAt).Seconds()))
	if retryAfter < 0 {
		retryAfter = 0
	}
	c.Response().Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
	body := apierr.Error(
		"EMOJI_APPLICATION_QUOTA_EXCEEDED",
		"You have reached the maximum number of emoji applications for this period.",
		"0b6f2a1e-9c34-4f8d-8a51-7b2e4d6c9f30")
	body["error"].(map[string]any)["info"] = map[string]any{
		"period":  q.Period,
		"used":    q.Used,
		"limit":   q.Limit,
		"retryAt": entity.ISOMillis(q.RetryAt),
	}
	return c.JSON(http.StatusTooManyRequests, body)
}

// pack renders an application for the applicant.
//
// **却下理由は出すが、審査したモデレーターは出さない。** 誰が押したかは監査用で、
// 申請者に見せると個人への抗議に繋がりやすい (upstream の通報処理も同じ扱い)。
func (h *Handler) pack(app *model.EmojiApplication) map[string]any {
	out := map[string]any{
		"id":          app.ID,
		"kind":        app.Kind,
		"status":      app.Status,
		"name":        app.Name,
		"category":    app.Category,
		"aliases":     []string(app.Aliases),
		"license":     app.License,
		"isSensitive": app.IsSensitive,
		"comment":     app.Comment,
		"createdAt":   entity.ISOMillis(app.CreatedAt),
	}
	out["processedAt"] = entity.ISOMillisPtr(app.ProcessedAt)
	if app.RejectReason != nil {
		out["rejectReason"] = *app.RejectReason
	}
	if app.EmojiID != nil {
		out["emojiId"] = *app.EmojiID
	}
	// **両方を見る (レビュー Low 3)。** 片側だけの行があると nil deref で
	// 500 になる (migration に「両方揃っている」CHECK は無い)。
	if app.RemoteHost != nil && app.RemoteName != nil {
		out["remoteHost"] = *app.RemoteHost
		out["remoteName"] = *app.RemoteName
	}
	out["url"] = h.previewURL(app)
	return out
}

// previewURL resolves the image to show in the list.
//
// drive の行が消えていれば空文字を返す。**それで一覧ごと落とさない** — 画像が
// 消えても申請の経緯 (却下理由など) は読めるべき。
func (h *Handler) previewURL(app *model.EmojiApplication) string {
	if app.FileID == nil || h.files == nil {
		return ""
	}
	f, err := h.files.FindByID(*app.FileID)
	if err != nil || f == nil {
		return ""
	}
	return f.URL
}
