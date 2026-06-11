package bubblegame

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Handler handles bubble-game/* endpoints.
type Handler struct {
	repo  repository.BubbleGameRepository
	idGen id.Generator
}

// NewHandler creates a new bubble-game handler.
func NewHandler(repo repository.BubbleGameRepository, idGen id.Generator) *Handler {
	return &Handler{repo: repo, idGen: idGen}
}

// Register handles POST /api/bubble-game/register.
func (h *Handler) Register(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Score       int     `json:"score"`
		Seed        string  `json:"seed"`
		Logs        [][]any `json:"logs"`
		GameMode    string  `json:"gameMode"`
		GameVersion int     `json:"gameVersion"`
	}
	if err := c.Bind(&req); err != nil || req.Seed == "" || req.GameMode == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "score, seed, logs, gameMode, gameVersion are required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	// シード検証: seedはUnixタイムスタンプ文字列
	seedMs, err := strconv.ParseInt(req.Seed, 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_SEED", "Provided seed is invalid.", "eb627bc7-574b-4a52-a860-3c3eae772b88"))
	}
	seedDate := time.UnixMilli(seedMs)
	now := time.Now()

	// 未来のシードは不正
	if seedDate.After(now) {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_SEED", "Provided seed is invalid.", "eb627bc7-574b-4a52-a860-3c3eae772b88"))
	}
	// 5時間以上前のシードは不正
	if seedDate.Before(now.Add(-5 * time.Hour)) {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_SEED", "Provided seed is invalid.", "eb627bc7-574b-4a52-a860-3c3eae772b88"))
	}

	logsJSON, _ := json.Marshal(req.Logs)
	record := &model.BubbleGameRecord{
		ID:          h.idGen.Generate(now),
		UserID:      user.ID,
		SeededAt:    seedDate,
		Seed:        req.Seed,
		GameVersion: req.GameVersion,
		GameMode:    req.GameMode,
		Score:       req.Score,
		Logs:        logsJSON,
		IsVerified:  false,
	}
	if err := h.repo.Create(record); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// Ranking handles POST /api/bubble-game/ranking.
func (h *Handler) Ranking(c echo.Context) error {
	var req struct {
		GameMode string `json:"gameMode"`
	}
	if err := c.Bind(&req); err != nil || req.GameMode == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "gameMode is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	records, err := h.repo.Ranking(req.GameMode, 10)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}

	result := make([]map[string]any, len(records))
	for i, r := range records {
		entry := map[string]any{
			"id":    r.ID,
			"score": r.Score,
		}
		// upstream ranking.ts は user を ref:'UserLite' で返す。ad-hoc な
		// 4 フィールドマップだと avatarUrl 等の必須フィールドが欠けて
		// frontend のランキング表示が壊れるため packer を経由する (#1553)。
		if r.User != nil {
			entry["user"] = entity.PackUserLite(r.User)
		}
		result[i] = entry
	}
	return c.JSON(http.StatusOK, result)
}
