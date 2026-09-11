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
	"github.com/shiroha-a/mk/internal/misc/colfit"
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
	case errors.Is(err, emojiapplication.ErrRemoteFetchFailed):
		return c.JSON(http.StatusInternalServerError, apierr.Error(
			"INTERNAL_ERROR", "Failed to fetch emoji image.",
			"0a4e0b9e-2d7c-4d6f-8f6b-1f9c2e9b4d83"))
	case errors.Is(err, emojiapplication.ErrNoSuchRemoteEmoji):
		return c.JSON(http.StatusBadRequest, apierr.Error(
			"NO_SUCH_EMOJI", "The remote emoji is no longer known to this instance.",
			"e2785b66-dca3-4087-9cac-b93c541cc425"))
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
	// **リモートも画像を出す (レビュー H1)。** 出さないと、モデレーターは
	// `:name:@host` の文字列だけを見て承認を押すことになる — #2934 が「大きな
	// 升目だけで判断すると本文で潰れて読めないものを通してしまう」として申請側と
	// 審査側の両方にプレビューを置いた設計の、リモート側だけが抜ける。
	// 名前だけ穏当にして中身が問題のある絵文字を通す審査回避にも直結する。
	if app.RemoteHost != nil && app.RemoteName != nil {
		out["remoteHost"] = *app.RemoteHost
		out["remoteName"] = *app.RemoteName
		// **処理済みの行でも引く (レビュー Low 3)。** 下の nameConflict は
		// `IsPending()` で絞っているが、こちらは絞らない — 画像は履歴を見る
		// ときにも要るもので、絞ると「承認済みのリモート絵文字だけ画像が
		// 出ない」という own 側との非対称が生まれる。件数は frontend が
		// limit 50 なので最大 100 クエリで、本番実測は 25-35ms (どちらも
		// index scan)。
		src, url, ft, lookupOK := h.remoteEmojiPreview(app)
		out["url"] = url
		out["fileType"] = ft
		// **承認できるかどうかもここで分かるようにする。** 審査を待つ間に
		// リモート絵文字の行が消えると承認は NO_SUCH_EMOJI で落ちる。
		//
		// **DB 障害を「消えた」に丸めない (レビュー R2-H2 / #2792)。** 丸めると
		// 障害中にモデレーターが「もう無い」と言われて**承認を操作ごと止められる**。
		// nil = 確認できなかった / false = ある / true = 消えた の 3 値にする。
		switch {
		case !lookupOK:
			out["remoteGone"] = nil
		default:
			out["remoteGone"] = src == nil
		}
		// **早期 return しない (レビュー R2-H1)。** 下の nameConflict に到達
		// しなくなり、リモートでだけ重複の警告が出なくなる — 承認を押してから
		// DUPLICATE_NAME で落ちるという、#2934 が塞いだ状態に戻る。
	} else {
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

// remoteEmojiPreview resolves the source emoji so the review screen can show it.
//
// `checkRemoteEmoji` と同じ lookup。**DB 障害と「消えた」を区別する** — 4 つ目の
// 戻り値が false なら「引けなかった」で、呼び出し側は null を出して
// 「分からない」と伝える。true かつ src が nil なら「もう無い」。
func (h *Handler) remoteEmojiPreview(app *model.EmojiApplication) (*model.Emoji, any, any, bool) {
	if h.emojiRepo == nil {
		return nil, nil, nil, false
	}
	src, err := h.emojiRepo.FindByNameAndHost(*app.RemoteName, app.RemoteHost)
	if err != nil {
		if repository.IsNotFound(err) {
			return nil, "", "", true
		}
		slog.Warn("emoji application: remote emoji lookup failed",
			"applicationId", app.ID, "host", *app.RemoteHost, "name", *app.RemoteName, "err", err)
		return nil, nil, nil, false
	}
	if src == nil {
		return nil, "", "", true
	}
	// publicUrl が空なら originalUrl に落とす (entity の packSimple と同じ)。
	url := src.PublicURL
	if url == "" {
		url = src.OriginalURL
	}
	var ft any = ""
	if src.Type != nil {
		ft = *src.Type
	}
	return src, url, ft, true
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
func (h *Handler) CreateFromApplication(ctx context.Context, app *model.EmojiApplication) (string, error) {
	if h.emojiRepo == nil || h.idGen == nil {
		return "", errors.New("admin: emoji creation is not wired")
	}
	if app.Kind == model.EmojiApplicationKindRemote {
		return h.createFromRemoteApplication(ctx, app)
	}
	if h.driveFileRepo == nil {
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

// createFromRemoteApplication imports a remote emoji for an approved request.
//
// **`admin/emoji/copy` と同じ経路を通す (#2935)。** 独自に emoji を組むと、
// リモート由来の文字列を列に収める規則 (`colfit`、#2726) と drive への取り込み
// (#670 / #722) が申請経路だけ抜ける。前者が抜けると相手サーバーが決めた長い
// 文字列で SQLSTATE 22001、後者が抜けると相手が画像を消した瞬間に表示が壊れる。
//
// **承認の時点で引き直す。** 申請は host + name で持っており emoji の行 ID では
// ない — リモート絵文字の行はキャッシュに近く、審査を待つ間に消えうる。
func (h *Handler) createFromRemoteApplication(ctx context.Context, app *model.EmojiApplication) (string, error) {
	if app.RemoteHost == nil || app.RemoteName == nil {
		return "", emojiapplication.ErrNoSuchRemoteEmoji
	}
	src, err := h.emojiRepo.FindByNameAndHost(*app.RemoteName, app.RemoteHost)
	if err != nil {
		if repository.IsNotFound(err) {
			// 審査を待つ間に消えた。500 ではなく「もう無い」と伝える。
			return "", emojiapplication.ErrNoSuchRemoteEmoji
		}
		return "", err
	}
	if src == nil {
		return "", emojiapplication.ErrNoSuchRemoteEmoji
	}

	// 承認の直前にもう一度だけ重複を見る (own と同じ)。
	existing, dupErr := h.emojiRepo.FindByNameAndHost(app.Name, nil)
	if dupErr != nil && !repository.IsNotFound(dupErr) {
		return "", dupErr
	}
	if dupErr == nil && existing != nil {
		return "", emojiapplication.ErrDuplicateName
	}

	now := time.Now()
	copied := *src
	copied.ID = h.idGen.Generate(now)
	// **`updatedAt` は今にする。** `EmojiCopy` は src の値を引き継ぐが、
	// それだと「相手サーバーで最後に更新された時刻」がローカル絵文字の
	// 更新時刻として並ぶ。`EmojiAdd` は now を入れており、そちらが正しい。
	// (`EmojiCopy` 側も揃えるべきだが、この PR の範囲外。)
	copied.UpdatedAt = &now
	copied.Host = nil
	copied.Name = app.Name
	// **`URI` は引き継ぐ。** `EmojiCopy` と同じ挙動で、renderer が
	// `emoji.URI != nil` のとき AP の `tag.ID` にそれを使う — remote emoji の
	// 参照同一性を壊さないための保守的方針 (#1948-11)。ここだけ変えると
	// 同じ操作で結果が変わるので踏襲する (本番に該当が 11 件ある)。

	// **申請で指定された値を列に収めてから入れる (#2726 と同じ規則)。**
	// 出どころは相手サーバーなので、そのまま渡すと列を超えて 500 になる。
	// 本文は切り、alias は長すぎる要素だけ落とす (切ると別の名前になる)。
	if app.Category != nil {
		v := colfit.Text(*app.Category, emojiCategoryMaxRunes)
		copied.Category = &v
	}
	if len(app.Aliases) > 0 {
		out := make([]string, 0, len(app.Aliases))
		for _, a := range app.Aliases {
			a = colfit.StripNUL(a)
			if a == "" || !colfit.Fits(a, emojiAliasMaxRunes) {
				continue
			}
			out = append(out, a)
		}
		copied.Aliases = model.StringArray(out)
	}
	// **申請者が書いたときだけ上書きする (レビュー M1)。** リモート絵文字の
	// 43% は AP の `_misskey_license` 由来の**本物の license** を持っている
	// (#731)。申請者は `fetch-remote-meta` を叩けない (moderator 専用) ので、
	// 参照できない値を手で書かせて潰すことになる。`EmojiCopy` も
	// `req.License != nil` のときだけ上書きしている。
	if app.License != "" {
		license := colfit.Text(app.License, emojiLicenseMaxRunes)
		copied.License = &license
	}
	// isSensitive も同じ理由で、指定があったときだけ上書きする。
	if app.IsSensitive {
		copied.IsSensitive = true
	}

	// **drive へ取り込む (#670 / #722)。** URL を引き継ぐだけだと、相手が
	// 画像を消した瞬間に表示が壊れる。system 所有で作るのは、操作者個人の
	// drive に紐付けるとロール変更や削除で巻き込まれるため。
	if h.emojiImageFetcher != nil && src.OriginalURL != "" {
		df, ferr := h.emojiImageFetcher.FetchAndStore(ctx, src.OriginalURL, nil, src.Name)
		if ferr != nil {
			slog.WarnContext(ctx, "emoji application: drive fetch failed",
				"applicationId", app.ID, "url", src.OriginalURL, "err", ferr)
			// **取得できなかったことを伝える (レビュー Low 2)。** 生の err を
			// 返すと汎用 500 になり、審査画面では「何か問題が」としか出ない。
			return "", emojiapplication.ErrRemoteFetchFailed
		}
		// **取り込んだものの MIME を見る (レビュー M7)。** 相手が icon.url に
		// 非画像を置くと、承認でそれが絵文字として登録される。own 経路は
		// 申請時に見ているので、remote だけ無検査なのは非対称。
		// (`EmojiCopy` も同じ穴だが、承認は「検証を迂回する方法」にしない。)
		if !isAllowedEmojiImageType(df.Type) {
			return "", emojiapplication.ErrUnsupportedFileType
		}
		copied.OriginalURL = df.URL
		copied.PublicURL = preferWebpublicURL(df)
		// **nil ガードは要らない。** 手前の isAllowedEmojiImageType が
		// `df.Type` の空を弾くので、preferWebpublicType が nil を返す経路に
		// 入らない (レビュー Low 1 の指摘はこの順序を見落としていた。
		// ガードを入れても到達しないデッドコードになる)。
		copied.Type = preferWebpublicType(df)
	}

	if err := h.emojiRepo.Create(&copied); err != nil {
		return "", err
	}
	h.publishEmojiAdded(&copied)
	return copied.ID, nil
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
