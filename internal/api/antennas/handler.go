// Package antennas provides /api/antennas/* endpoints.
package antennas

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	coreantenna "github.com/shiroha-a/mk/internal/core/antenna"
	"github.com/shiroha-a/mk/internal/core/notesfilter"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler handles antenna-related API endpoints.
type Handler struct {
	svc          *coreantenna.Service
	noteRepo     repository.NoteRepository
	idGen        id.Generator
	instanceRepo repository.InstanceRepository
	emojiRepo    repository.EmojiRepository
	bufReader    entity.BufferedReactionsReader
	fieldRes     *entity.NoteFieldResolver
	// userRepo は antennas/notes の hardMutedWords filter (#787) のために
	// viewer profile を引く。未配線時は filter skip。
	userRepo repository.UserRepository
}

// SetUserRepo wires a UserRepository so antennas/notes filters out notes that
// match the viewer's hardMutedWords (#787).
func (h *Handler) SetUserRepo(r repository.UserRepository) {
	h.userRepo = r
}

// SetNoteFieldResolver attaches the shared resolver that fills Files /
// MyReaction / Channel on packed notes including their Renote / Reply embed
// (#426)。nil-safe (Apply no-op)。
func (h *Handler) SetNoteFieldResolver(r *entity.NoteFieldResolver) {
	h.fieldRes = r
}

// NewHandler constructs an antennas Handler. noteRepo は antennas/notes で
// note id → entity 変換に使う。
func NewHandler(svc *coreantenna.Service, noteRepo repository.NoteRepository, idGen id.Generator) *Handler {
	return &Handler{svc: svc, noteRepo: noteRepo, idGen: idGen}
}

// SetInstanceRepo attaches an InstanceRepository so antennas/notes populates
// UserLite.Instance for remote users (#277).
func (h *Handler) SetInstanceRepo(r repository.InstanceRepository) {
	h.instanceRepo = r
}

func (h *Handler) instanceLookup() entity.InstanceLookup {
	if h.instanceRepo == nil {
		return nil
	}
	return h.instanceRepo
}

// SetEmojiRepo attaches an EmojiRepository so custom emoji shortcodes in
// note text and user displayNames get resolved to URLs.
func (h *Handler) SetEmojiRepo(r repository.EmojiRepository) {
	h.emojiRepo = r
}

// SetReactionReader wires a BufferedReactionsReader so PackNote / PackNotes
// can merge in-flight buffered reaction deltas (#647)。
func (h *Handler) SetReactionReader(r entity.BufferedReactionsReader) {
	h.bufReader = r
}

func (h *Handler) reactionReader() entity.BufferedReactionsReader {
	return h.bufReader
}

func (h *Handler) emojiLookup() entity.EmojiLookup {
	if h.emojiRepo == nil {
		return nil
	}
	return h.emojiRepo
}

// CreateRequest is the request body for antennas/create.
type CreateRequest struct {
	Name            string              `json:"name"`
	Src             model.AntennaSource `json:"src"`
	UserListID      *string             `json:"userListId"`
	Users           []string            `json:"users"`
	Keywords        [][]string          `json:"keywords"`
	ExcludeKeywords [][]string          `json:"excludeKeywords"`
	CaseSensitive   bool                `json:"caseSensitive"`
	ExcludeBots     bool                `json:"excludeBots"`
	WithReplies     bool                `json:"withReplies"`
	WithFile        bool                `json:"withFile"`
	LocalOnly       bool                `json:"localOnly"`
}

// Create handles POST /api/antennas/create.
func (h *Handler) Create(c echo.Context) error {
	user := middleware.GetUser(c)
	var req CreateRequest
	if err := c.Bind(&req); err != nil || req.Name == "" {
		return apierr.JSONInvalidParam(c)
	}
	a, err := h.svc.Create(coreantenna.CreateInput{
		OwnerID:         user.ID,
		Name:            req.Name,
		Src:             req.Src,
		UserListID:      req.UserListID,
		Users:           req.Users,
		Keywords:        req.Keywords,
		ExcludeKeywords: req.ExcludeKeywords,
		CaseSensitive:   req.CaseSensitive,
		ExcludeBots:     req.ExcludeBots,
		WithReplies:     req.WithReplies,
		WithFile:        req.WithFile,
		LocalOnly:       req.LocalOnly,
	})
	if err != nil {
		// Name は事前 invalidParam で弾いているため、ここに来るのは
		// ErrInvalidSource か repo エラー、または antennaLimit 超過のみ。
		if errors.Is(err, coreantenna.ErrInvalidSource) {
			return apierr.JSONInvalidParam(c)
		}
		if errors.Is(err, coreantenna.ErrTooManyAntennas) {
			return apierr.JSONTooManyAntennas(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.antennaToMap(a))
}

// ShowRequest is the request body for antennas/show.
type ShowRequest struct {
	AntennaID string `json:"antennaId"`
}

// Show handles POST /api/antennas/show.
func (h *Handler) Show(c echo.Context) error {
	user := middleware.GetUser(c)
	var req ShowRequest
	if err := c.Bind(&req); err != nil || req.AntennaID == "" {
		return apierr.JSONInvalidParam(c)
	}
	a, err := h.svc.Show(user.ID, req.AntennaID)
	if err != nil {
		if errors.Is(err, coreantenna.ErrAccessDenied) {
			return apierr.JSONAccessDenied(c)
		}
		// Show は ErrAntennaNotFound 以外を返さない (未マップ含む)
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ANTENNA", "No such antenna.", "c06569fb-b025-4f23-b22d-1fcd20d2816b"))
	}
	return c.JSON(http.StatusOK, h.antennaToMap(a))
}

// UpdateRequest is the request body for antennas/update.
type UpdateRequest struct {
	AntennaID       string               `json:"antennaId"`
	Name            *string              `json:"name"`
	Src             *model.AntennaSource `json:"src"`
	UserListID      *string              `json:"userListId"`
	Users           *[]string            `json:"users"`
	Keywords        *[][]string          `json:"keywords"`
	ExcludeKeywords *[][]string          `json:"excludeKeywords"`
	CaseSensitive   *bool                `json:"caseSensitive"`
	ExcludeBots     *bool                `json:"excludeBots"`
	WithReplies     *bool                `json:"withReplies"`
	WithFile        *bool                `json:"withFile"`
	LocalOnly       *bool                `json:"localOnly"`
	IsActive        *bool                `json:"isActive"`
}

// Update handles POST /api/antennas/update.
func (h *Handler) Update(c echo.Context) error {
	user := middleware.GetUser(c)
	var req UpdateRequest
	if err := c.Bind(&req); err != nil || req.AntennaID == "" {
		return apierr.JSONInvalidParam(c)
	}
	a, err := h.svc.Update(user.ID, req.AntennaID, coreantenna.UpdateInput{
		Name:            req.Name,
		Src:             req.Src,
		UserListID:      req.UserListID,
		Users:           req.Users,
		Keywords:        req.Keywords,
		ExcludeKeywords: req.ExcludeKeywords,
		CaseSensitive:   req.CaseSensitive,
		ExcludeBots:     req.ExcludeBots,
		WithReplies:     req.WithReplies,
		WithFile:        req.WithFile,
		LocalOnly:       req.LocalOnly,
		IsActive:        req.IsActive,
	})
	if err != nil {
		switch {
		case errors.Is(err, coreantenna.ErrAntennaNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ANTENNA", "No such antenna.", "10c673ac-8852-48eb-aa1f-f5b67f069290"))
		case errors.Is(err, coreantenna.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		case errors.Is(err, coreantenna.ErrAntennaNameRequired),
			errors.Is(err, coreantenna.ErrInvalidSource):
			return apierr.JSONInvalidParam(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.JSON(http.StatusOK, h.antennaToMap(a))
}

// DeleteRequest is the request body for antennas/delete.
type DeleteRequest struct {
	AntennaID string `json:"antennaId"`
}

// Delete handles POST /api/antennas/delete.
func (h *Handler) Delete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req DeleteRequest
	if err := c.Bind(&req); err != nil || req.AntennaID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if err := h.svc.Delete(user.ID, req.AntennaID); err != nil {
		switch {
		case errors.Is(err, coreantenna.ErrAntennaNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ANTENNA", "No such antenna.", "b34dcf9d-348f-44bb-99d0-6c9314cfe2df"))
		case errors.Is(err, coreantenna.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// List handles POST /api/antennas/list.
func (h *Handler) List(c echo.Context) error {
	user := middleware.GetUser(c)
	rows, err := h.svc.ListByUser(user.ID)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		out = append(out, h.antennaToMap(a))
	}
	return c.JSON(http.StatusOK, out)
}

// NotesRequest is the request body for antennas/notes.
//
// SinceID / UntilID は upstream Misskey TS と同じ paging key (#693)。
// 設定しないと FE が無限スクロールするたびに同じ最新 N 件を取り続けて
// 「同じノートが何度も表示される」現象になる。
type NotesRequest struct {
	AntennaID string `json:"antennaId"`
	Limit     int    `json:"limit"`
	SinceID   string `json:"sinceId"`
	UntilID   string `json:"untilId"`
	SinceDate *int64 `json:"sinceDate"`
	UntilDate *int64 `json:"untilDate"`
}

// Notes handles POST /api/antennas/notes.
func (h *Handler) Notes(c echo.Context) error {
	user := middleware.GetUser(c)
	var req NotesRequest
	if err := c.Bind(&req); err != nil || req.AntennaID == "" {
		return apierr.JSONInvalidParam(c)
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	ids, err := h.svc.Notes(c.Request().Context(), user.ID, req.AntennaID, req.Limit, sinceID, untilID)
	if err != nil {
		switch {
		case errors.Is(err, coreantenna.ErrAntennaNotFound):
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_ANTENNA", "No such antenna.", "850926e0-fd3b-49b6-b69a-b28a5dbd82fe"))
		case errors.Is(err, coreantenna.ErrAccessDenied):
			return apierr.JSONAccessDenied(c)
		}
		return apierr.JSONInternalError(c)
	}
	notes, err := h.noteRepo.FindManyByIDsWithUser(ids)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	notes = notesfilter.ApplyHardMute(h.userRepo, user, notes)
	entities := entity.PackNotes(c.Request().Context(), notes, h.idGen, h.instanceLookup(), h.emojiLookup(), h.reactionReader())
	h.fieldRes.Apply(entities, user)
	out := make([]any, 0, len(entities))
	for _, pn := range entities {
		out = append(out, pn)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) antennaToMap(a *model.Antenna) map[string]any {
	// upstream Misskey TS の packAntenna は userId field を embed しない
	// (= antenna は user-scoped で frontend が呼出 user の id を別経路で持つ
	// 設計、#904)。drop-in 互換維持のため mk-go も同 shape に揃える。
	//
	// createdAt は antenna ID (aidx) から復元する。以前は lastUsedAt を流用して
	// いたが createdAt は作成時刻であるべき + misskey_dart の Antenna.fromJson が
	// 非null String として cast するため ISO ms 形式で出す (#1244)。
	// hasUnreadNote は misskey_dart が非null bool として cast する (#1244)。
	// mk-go は antenna 未読 note を追跡しないため false 固定。
	createdAt := ""
	if h.idGen != nil {
		if t, err := h.idGen.ParseTime(a.ID); err == nil {
			createdAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	// users は pq.StringArray (nil なら null になる) を golden の非null array に
	// 合わせて [] へ coalesce する (#1270 L3 検出)。
	users := []string(a.Users)
	if users == nil {
		users = []string{}
	}
	return map[string]any{
		"id":         a.ID,
		"createdAt":  createdAt,
		"name":       a.Name,
		"src":        a.Src,
		"userListId": a.UserListID,
		"users":      users,
		// keywords / excludeKeywords は jsonb (datatypes.JSON)。未設定 antenna は
		// nil で JSON null になるが golden Antenna は string[][] 必須なので [] へ
		// coalesce する (#1318。users #1270 と同種の null-array drift)。
		"keywords":        jsonArrayOrEmpty(a.Keywords),
		"excludeKeywords": jsonArrayOrEmpty(a.ExcludeKeywords),
		"caseSensitive":   a.CaseSensitive,
		"excludeBots":     a.ExcludeBots,
		"withReplies":     a.WithReplies,
		"withFile":        a.WithFile,
		"localOnly":       a.LocalOnly,
		"isActive":        a.IsActive,
		"hasUnreadNote":   false,
		// excludeNotesInSensitiveChannel は model にあるので反映。notify は
		// golden で必須 (boolean) だが mk-go は antenna notify を未実装なので
		// false 固定で shape だけ満たす (#1270 L3 検出)。
		"excludeNotesInSensitiveChannel": a.ExcludeNotesInSensitiveChannel,
		"notify":                         false,
	}
}

// jsonArrayOrEmpty coalesces a nil/empty jsonb column to a non-null empty
// array so the JSON encoder emits `[]` instead of `null`。非空なら格納済みの
// 生 JSON をそのまま返す (#1318)。
func jsonArrayOrEmpty(b []byte) any {
	if len(b) == 0 {
		return []any{}
	}
	return json.RawMessage(b)
}
