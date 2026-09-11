package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/emojiapplication"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// emojiApplicationReviewer is the subset of the emoji application service the
// admin endpoints need.
type emojiApplicationReviewer interface {
	Approve(ctx context.Context, id, moderatorID string) (*model.EmojiApplication, error)
	Reject(ctx context.Context, id, moderatorID, reason string) (*model.EmojiApplication, error)
}

// SetEmojiApplicationReviewer wires the review half of #2934.
func (h *Handler) SetEmojiApplicationReviewer(r emojiApplicationReviewer) {
	h.emojiApplicationReviewer = r
}

// SetEmojiApplicationRepo wires the read half of #2934.
func (h *Handler) SetEmojiApplicationRepo(r repository.EmojiApplicationRepository) {
	h.emojiApplicationRepo = r
}

// EmojiApplicationList handles POST /api/admin/emoji-application/list.
func (h *Handler) EmojiApplicationList(c echo.Context) error {
	if h.emojiApplicationRepo == nil {
		return apierr.JSONInternalError(c)
	}
	var req struct {
		Filter  string `json:"filter"`
		Limit   int    `json:"limit"`
		UntilID string `json:"untilId"`
	}
	_ = c.Bind(&req)
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 30
	}
	if req.Filter == "" {
		req.Filter = repository.EmojiApplicationFilterPending
	}

	rows, err := h.emojiApplicationRepo.List(req.Filter, req.Limit, req.UntilID)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		out = append(out, h.packEmojiApplicationForModerator(&rows[i]))
	}
	return c.JSON(http.StatusOK, out)
}

// EmojiApplicationApprove handles POST /api/admin/emoji-application/approve.
func (h *Handler) EmojiApplicationApprove(c echo.Context) error {
	if h.emojiApplicationReviewer == nil {
		return apierr.JSONInternalError(c)
	}
	me := middleware.GetUser(c)
	if me == nil {
		return apierr.JSONInternalError(c)
	}
	var req struct {
		ApplicationID string `json:"applicationId"`
	}
	if err := c.Bind(&req); err != nil || req.ApplicationID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"INVALID_PARAM", "applicationId is required.",
			"3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	app, err := h.emojiApplicationReviewer.Approve(c.Request().Context(), req.ApplicationID, me.ID)
	if err != nil {
		return emojiApplicationReviewError(c, err)
	}
	// **`emoji` キーを必ず入れる。** 管理画面の modlog は
	// `log.info.emoji.name` を無条件に読むので (upstream の
	// `modlog.ModLog.vue`)、無いと TypeError で **その 1 件が空行になって
	// 消える** — Vue 3.5 の renderComponentRoot が render 例外を握って
	// Comment ノードを返すため、コンソールにしか出ない。
	h.logModeration(c, moderationlog.LogAddCustomEmoji, map[string]any{
		"emojiId":                 derefString(app.EmojiID),
		"emoji":                   emojiApplicationLogEmoji(app),
		"emojiApplicationId":      app.ID,
		"emojiApplicationUserId":  app.UserID,
		"emojiApplicationDecided": "approved",
	})
	return c.JSON(http.StatusOK, h.packEmojiApplicationForModerator(app))
}

// EmojiApplicationReject handles POST /api/admin/emoji-application/reject.
func (h *Handler) EmojiApplicationReject(c echo.Context) error {
	if h.emojiApplicationReviewer == nil {
		return apierr.JSONInternalError(c)
	}
	me := middleware.GetUser(c)
	if me == nil {
		return apierr.JSONInternalError(c)
	}
	var req struct {
		ApplicationID string `json:"applicationId"`
		Reason        string `json:"reason"`
	}
	if err := c.Bind(&req); err != nil || req.ApplicationID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"INVALID_PARAM", "applicationId is required.",
			"3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	app, err := h.emojiApplicationReviewer.Reject(c.Request().Context(), req.ApplicationID, me.ID, req.Reason)
	if err != nil {
		return emojiApplicationReviewError(c, err)
	}
	// **却下も監査に残す。** 何を通したかだけでなく、何を落としたかが残らないと
	// 「モデレーターが恣意的に落としている」という疑いに答える材料が無くなる。
	//
	// **type は addCustomEmoji のまま。** modlog UI はこの type を「追加」として
	// 緑 + ti-plus で描くので、却下ログも「追加」に見える (レビュー Low 3)。
	// 専用の type を足すには upstream の enum と UI 両方に手を入れることになり、
	// 追従のたびに衝突する。`emojiApplicationDecided` を info に入れてあるので
	// 区別は付く。専用 type は #2935 と合わせて検討する。
	h.logModeration(c, moderationlog.LogAddCustomEmoji, map[string]any{
		"emoji":                   emojiApplicationLogEmoji(app),
		"emojiApplicationId":      app.ID,
		"emojiApplicationUserId":  app.UserID,
		"emojiApplicationDecided": "rejected",
	})
	return c.JSON(http.StatusOK, h.packEmojiApplicationForModerator(app))
}

// emojiApplicationReviewError maps service errors onto the wire.
func emojiApplicationReviewError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, emojiapplication.ErrNotFound):
		return c.JSON(http.StatusNotFound, apierr.Error(
			"NO_SUCH_APPLICATION", "No such application.",
			"2b7e0c94-6d1a-4f3e-b5c8-0a9d3e7f1b44"))
	case errors.Is(err, emojiapplication.ErrNotPending):
		// **同時に 2 人が押した場合もここに来る。** 「もう処理済み」と返して
		// 一覧を引き直させるほうが、500 を出して何が起きたか分からないより良い。
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"ALREADY_PROCESSED", "This request was already processed.",
			"4e6f1a37-9c2b-4d80-a1f5-7b3c8e0d2a55"))
	case errors.Is(err, emojiapplication.ErrDuplicateName):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"DUPLICATE_NAME", "Duplicate name.",
			"f7a3462c-4e6e-4069-8421-b9bd4f4c3975"))
	case errors.Is(err, emojiapplication.ErrUnsupportedFileType):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"UNSUPPORTED_FILE_TYPE", "Unsupported file type.",
			"f7599d96-8750-af68-1633-9575d625c1a7"))
	case errors.Is(err, emojiapplication.ErrFileGone):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"NO_SUCH_FILE", "No such file.",
			"fc46b5a4-6b92-4c33-ac66-b806659bb5cf"))
	default:
		return apierr.JSONInternalError(c)
	}
}

// packEmojiApplicationForModerator renders a row for the review screen.
//
// 申請者向け (internal/api/emojiapplications) より項目が多い。審査に要る情報を
// 出し切るのが目的で、**同名の既存絵文字があるかどうかもここで解決する** —
// 承認を押してから DUPLICATE_NAME で落ちると、押した側にも申請者にも何も残らない。
func (h *Handler) packEmojiApplicationForModerator(app *model.EmojiApplication) map[string]any {
	out := map[string]any{
		"id":          app.ID,
		"userId":      app.UserID,
		"kind":        app.Kind,
		"status":      app.Status,
		"name":        app.Name,
		"category":    app.Category,
		"aliases":     []string(app.Aliases),
		"license":     app.License,
		"isSensitive": app.IsSensitive,
		"comment":     app.Comment,
		"createdAt":   entity.ISOMillis(app.CreatedAt),
		"processedAt": entity.ISOMillisPtr(app.ProcessedAt),
	}
	if app.RejectReason != nil {
		out["rejectReason"] = *app.RejectReason
	}
	if app.EmojiID != nil {
		out["emojiId"] = *app.EmojiID
	}
	// **drive は 1 回だけ引く (レビュー M6)。** url と fileType で別々に呼ぶと
	// 1 行あたり 2 回になり、limit=100 の一覧で 200 クエリが直列に走る。
	//
	// **「消えた」と「確認できなかった」を区別する (レビュー M1)。** DB 障害を
	// 「画像がありません」と表示すると、モデレーターが実際には存在する申請を
	// 却下しうる。nameConflict と同じく null で区別できるようにする。
	f, lookupOK := h.emojiApplicationFile(app)
	switch {
	case f != nil:
		out["url"] = f.URL
		out["fileType"] = f.Type
	case lookupOK:
		// 引けたが行が無い = 申請者が drive から消した。
		out["url"] = ""
		out["fileType"] = ""
	default:
		// 引けなかった。null で「分からない」を表す。
		out["url"] = nil
		out["fileType"] = nil
	}

	// 審査中のものだけ重複を見る。処理済みの行で引いても意味が無く、
	// 一覧のたびに件数分のクエリを足すだけになる。
	if app.IsPending() && h.emojiRepo != nil {
		existing, err := h.emojiRepo.FindByNameAndHost(app.Name, nil)
		// **DB 障害を「重複なし」に丸めない** (#2792)。判定できなかったことを
		// そのまま出し、モデレーターに「確認できていない」と分かるようにする。
		switch {
		case err != nil && !repository.IsNotFound(err):
			out["nameConflict"] = nil
		case err == nil && existing != nil:
			out["nameConflict"] = map[string]any{
				"emojiId":   existing.ID,
				"createdAt": entity.ISOMillisPtr(existing.UpdatedAt),
			}
		default:
			out["nameConflict"] = false
		}
	}
	return out
}

// emojiApplicationFile resolves the drive file for an application.
//
// 返り値の 2 つ目は「引けたかどうか」。**DB 障害を「画像が無い」に丸めない
// (#2792 の型)。** 丸めると障害中に審査画面が「画像がありません」と表示し、
// モデレーターが実際には存在する申請を却下しうる。
//
//	(f, true)   — 見つかった
//	(nil, true) — 引けたが行が無い (申請者が drive から消した)
//	(nil, false) — 引けなかった (障害)。呼び出し側は null で「分からない」を出す
func (h *Handler) emojiApplicationFile(app *model.EmojiApplication) (*model.DriveFile, bool) {
	if app.FileID == nil || h.driveFileRepo == nil {
		return nil, true
	}
	f, err := h.driveFileRepo.FindByID(*app.FileID)
	if err != nil {
		if repository.IsNotFound(err) {
			return nil, true
		}
		slog.Warn("emoji application: drive file lookup failed",
			"applicationId", app.ID, "fileId", *app.FileID, "err", err)
		return nil, false
	}
	return f, true
}

// CreateFromApplication implements emojiapplication.EmojiCreator.
//
// **EmojiAdd と同じ検証・同じ導出を通す。** 独自に emoji 行を組むと、MIME の
// allowlist や webpublic variant の優先が申請経路だけ抜け、承認が「検証を
// 迂回して絵文字を登録する方法」になる。共有しているのは
// isAllowedEmojiImageType / preferWebpublicURL / preferWebpublicType の 3 つで、
// これは EmojiAdd がそのまま使っているものと同一。
func (h *Handler) CreateFromApplication(_ context.Context, app *model.EmojiApplication) (string, error) {
	if h.emojiRepo == nil || h.driveFileRepo == nil || h.idGen == nil {
		return "", errors.New("admin: emoji creation is not wired")
	}
	if app.FileID == nil {
		return "", emojiapplication.ErrFileGone
	}

	f, err := h.driveFileRepo.FindByID(*app.FileID)
	if err != nil {
		if repository.IsNotFound(err) {
			// **申請から承認までの間にファイルが消えることがある。** 利用者が
			// drive から消せるので、500 ではなく「もう無い」と伝える。
			return "", emojiapplication.ErrFileGone
		}
		return "", err
	}
	// **承認側でも所有者を見る (レビュー Low 3)。** 申請時にも見ているが、
	// 検証が入る前に作られた行が残っている可能性があり、多層にしておけば
	// 「申請時の検証を落としたら承認が素通りする」形にもならない。
	if f.UserID == nil || *f.UserID != app.UserID {
		return "", emojiapplication.ErrFileGone
	}
	if !isAllowedEmojiImageType(f.Type) {
		return "", emojiapplication.ErrUnsupportedFileType
	}

	// 承認の直前にもう一度だけ重複を見る。申請の受付時にも見ているが、審査を
	// 待つ間に同じ名前が登録されうる。
	existing, dupErr := h.emojiRepo.FindByNameAndHost(app.Name, nil)
	if dupErr != nil && !repository.IsNotFound(dupErr) {
		return "", dupErr
	}
	if dupErr == nil && existing != nil {
		return "", emojiapplication.ErrDuplicateName
	}

	now := time.Now()
	e := &model.Emoji{
		ID:          h.idGen.Generate(now),
		UpdatedAt:   &now,
		Name:        app.Name,
		OriginalURL: f.URL,
		PublicURL:   preferWebpublicURL(f),
		Type:        preferWebpublicType(f),
		Category:    app.Category,
		Aliases:     model.StringArray(app.Aliases),
		License:     &app.License,
		IsSensitive: app.IsSensitive,
	}
	if err := h.emojiRepo.Create(e); err != nil {
		return "", err
	}
	// **EmojiAdd と同じく broadcast する (レビュー M3)。** これが無いと、承認した
	// 絵文字が全クライアントのリロードまで絵文字ピッカーに出てこない
	// (`boot/main-boot.ts` が emojiAdded を受けて addCustomEmoji する)。
	h.publishEmojiAdded(e)
	return e.ID, nil
}

// emojiApplicationLogEmoji renders the emoji-ish payload the moderation log UI
// expects.
//
// 却下では emoji 行が存在しないので、申請の内容から同じ形を組む。**空にしない** —
// modlog UI が `log.info.emoji.name` を無条件に読むので、無いとその行が消える。
func emojiApplicationLogEmoji(app *model.EmojiApplication) map[string]any {
	return map[string]any{
		"id":       derefString(app.EmojiID),
		"name":     app.Name,
		"category": app.Category,
		"aliases":  []string(app.Aliases),
		"license":  app.License,
	}
}

// DeleteCreatedEmoji implements emojiapplication.EmojiCreator.
//
// 承認が競合に負けたときだけ呼ばれる。**broadcast も出す** — 作成時に
// emojiAdded を流しているので、消したことも伝えないとピッカーに残る。
func (h *Handler) DeleteCreatedEmoji(_ context.Context, emojiID string) error {
	if h.emojiRepo == nil || emojiID == "" {
		return nil
	}
	e, err := h.emojiRepo.FindByID(emojiID)
	if err != nil {
		if repository.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := h.emojiRepo.Delete(emojiID); err != nil {
		return err
	}
	h.publishEmojiDeleted(e)
	return nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
