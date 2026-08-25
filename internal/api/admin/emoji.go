package admin

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// EmojiAddAliasesBulk handles POST /api/admin/emoji/add-aliases-bulk.
//
// 旧実装は id ごとに FindByID→UpdateFields を直列に呼んでいたが、件数が多いと
// DB 往復が線形に増える。FindManyByIDs で一括取得 → 個別に alias を merge →
// UpdateFields で書き戻す (alias はレコード毎に異なるため UpdateFieldsMany
// では一括化できない)。
func (h *Handler) EmojiAddAliasesBulk(c echo.Context) error {
	var req struct {
		IDs     []string `json:"ids"`
		Aliases []string `json:"aliases"`
	}
	if err := c.Bind(&req); err != nil || len(req.IDs) == 0 {
		return c.NoContent(http.StatusNoContent)
	}
	if h.emojiRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	rows, err := h.emojiRepo.FindManyByIDs(req.IDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	for _, e := range rows {
		merged := dedupe(append(append([]string{}, e.Aliases...), req.Aliases...))
		// model.Emoji.Aliases は model.StringArray なので plain []string で
		// 渡すと GORM が record literal `('a','b')` を生成して
		// SQLSTATE 42804 (column type mismatch) になる drift がある
		// (#896 と同 pattern)。model.StringArray で wrap して
		// `'{a,b}'` array literal として書き込ませる。
		if err := h.emojiRepo.UpdateFields(e.ID, map[string]any{"aliases": model.StringArray(merged)}); err != nil {
			// per-emoji 失敗は bulk 操作全体を中断せず log で観測する
			// (#882 で発覚した silent failure 防止)。複数件のうち一部が
			// 失敗しても他の emoji は更新したい意図。
			slog.WarnContext(c.Request().Context(), "admin/emoji/add-aliases-bulk: per-emoji update failed",
				"emojiId", e.ID, "err", err)
		}
	}
	h.publishEmojiUpdatedByIDs(req.IDs) // #2046
	return c.NoContent(http.StatusNoContent)
}

// EmojiCopy handles POST /api/admin/emoji/copy.
//
// Misskey TS 本家 (admin/emoji/copy.ts) は元の name のまま local emoji を
// 追加し、同名の local emoji が既に存在する場合のみ DUPLICATE_NAME を
// 返す。以前の mk-go 実装は `_copy` suffix を勝手に付与していたが、
// これは TS 仕様違反 (#650 問題 1) なのでサフィックスを除去した。
//
// 画像については upstream の driveService.uploadFromUrl 相当に対応するため、
// emojiImageFetcher が wire 済みなら src.OriginalURL を local drive に
// 保存し、新 emoji の OriginalURL/PublicURL を drive 経由の URL に差し替える
// (#670)。fetcher 未配線時は src の URL をそのまま継承する legacy 挙動を
// 維持する (テスト容易性 + 未配線環境での graceful degradation 用)。
//
// ref: third_party/misskey/packages/backend/src/server/api/endpoints/admin/emoji/copy.ts
func (h *Handler) EmojiCopy(c echo.Context) error {
	var req struct {
		EmojiID string `json:"emojiId"`
	}
	if err := c.Bind(&req); err != nil || req.EmojiID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("emojiId is required."))
	}
	if h.emojiRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	src, err := h.emojiRepo.FindByID(req.EmojiID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_EMOJI", "No such emoji.", "e2785b66-dca3-4087-9cac-b93c541cc425"))
	}
	if existing, err := h.emojiRepo.FindByNameAndHost(src.Name, nil); err == nil && existing != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("DUPLICATE_NAME", "Duplicate name.", "f7a3462c-4e6e-4069-8421-b9bd4f4c3975"))
	}
	copied := *src
	copied.ID = h.idGen.Generate(time.Now())
	copied.Host = nil

	// fetcher が wire され src.OriginalURL があれば drive に保存して URL
	// を切り替える。drive file は upstream Misskey TS の uploadFromUrl
	// ({user: null}) と同じく **system 所有 (user nil)** で作成する。
	// custom emoji はインスタンス管理アセットであり、操作者個人の drive に
	// 紐付けるとロール変更や削除で巻き込まれて表示が壊れる。失敗時は
	// INTERNAL_ERROR を返して emoji 作成自体を中止する (URL 引き継ぎだけで
	// 作成すると #670 の症状に逆戻りするため)。
	if h.emojiImageFetcher != nil && src.OriginalURL != "" {
		df, err := h.emojiImageFetcher.FetchAndStore(c.Request().Context(), src.OriginalURL, nil, src.Name)
		if err != nil {
			slog.WarnContext(c.Request().Context(), "emoji copy: drive fetch failed",
				"srcId", src.ID, "url", src.OriginalURL, "err", err)
			return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Failed to fetch emoji image.", "0a4e0b9e-2d7c-4d6f-8f6b-1f9c2e9b4d83"))
		}
		// 不変条件 (#722): emoji.originalUrl は必ず drive_file.url と一致
		// させる。`DriveFileRepository.DeleteOrphans` の cleanup guard が
		// `NOT EXISTS (emoji.originalUrl = drive_file.url ...)` で system 所有
		// emoji 画像を保護しているので、ここで df.URL 以外を入れると guard を
		// すり抜けて cleanup で消える。webpublic 系列を直接 originalUrl に
		// セットする変更を入れる際は cleanup 側も同時更新すること。
		copied.OriginalURL = df.URL
		if df.WebpublicURL != nil && *df.WebpublicURL != "" {
			copied.PublicURL = *df.WebpublicURL
		} else {
			copied.PublicURL = df.URL
		}
		// webpublic / fallback いずれの経路も defensive copy で *string を
		// 作って df のポインタを共有しない (df 側を後から触る経路は無いが、
		// 型 (*string) を扱う読み手のメンタルモデルを揃えるため)。
		if df.WebpublicType != nil && *df.WebpublicType != "" {
			t := *df.WebpublicType
			copied.Type = &t
		} else if df.Type != "" {
			t := df.Type
			copied.Type = &t
		}
	}

	if err := h.emojiRepo.Create(&copied); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	h.logModeration(c, moderationlog.LogAddCustomEmoji, map[string]any{
		"emojiId": copied.ID,
		"emoji":   &copied,
	})
	h.publishEmojiAdded(&copied) // #2046
	return c.JSON(http.StatusOK, map[string]any{"id": copied.ID})
}

// EmojiDeleteBulk handles POST /api/admin/emoji/delete-bulk.
//
// Misskey TS の CustomEmojiService.deleteBulk() は削除対象を for ループで
// 一件ずつ処理し、絵文字ごとに deleteCustomEmoji moderation log を書く。
// 互換性のため mk-go も削除前の snapshot を取得して per-emoji log を出力
// する。bulk 削除自体は 1 SQL (DeleteMany) で完結。
func (h *Handler) EmojiDeleteBulk(c echo.Context) error {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := c.Bind(&req); err != nil || len(req.IDs) == 0 {
		return c.NoContent(http.StatusNoContent)
	}
	if h.emojiRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// per-emoji log のため削除前に snapshot を取得。失敗しても削除自体は
	// 続行する (snapshot 失敗で監査が落ちるより操作完遂が優先)。エラー時は
	// log が出ない理由を debug-level で残して運用時の追跡を可能にする。
	snapshots, err := h.emojiRepo.FindManyByIDs(req.IDs)
	if err != nil {
		slog.DebugContext(c.Request().Context(), "moderation log: snapshot lookup failed before delete-bulk",
			"ids", req.IDs, "err", err)
	}
	if err := h.emojiRepo.DeleteMany(req.IDs); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	// per-emoji log を 1 batch にまとめる (#671)。Misskey TS 互換は per-emoji
	// row 単位なので結果として永続化される行数は同じだが、N goroutine + N
	// INSERT を 1 goroutine + 1 multi-row INSERT に圧縮する。
	if len(snapshots) > 0 {
		entries := make([]moderationlog.Entry, 0, len(snapshots))
		for _, e := range snapshots {
			entries = append(entries, moderationlog.Entry{
				Type: moderationlog.LogDeleteCustomEmoji,
				Info: map[string]any{
					"emojiId": e.ID,
					"emoji":   e,
				},
			})
		}
		h.logModerationMany(c, entries)
	}
	// 削除前 snapshot を broadcast (#2046)。snapshot 取得失敗時は空で no-op。
	h.publishEmojiDeleted(snapshots...)
	return c.NoContent(http.StatusNoContent)
}

// dedupe returns the slice with duplicates removed while preserving order.
func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// EmojiImportZip handles POST /api/admin/emoji/import-zip.
//
// fileId で Drive にアップロード済みの ZIP を指定し、非同期ジョブで展開して
// ローカルカスタム絵文字として登録する。本家 Misskey 互換
// (QueueService.createImportCustomEmojisJob 相当)。
func (h *Handler) EmojiImportZip(c echo.Context) error {
	var req struct {
		FileID string `json:"fileId"`
	}
	if err := c.Bind(&req); err != nil || req.FileID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "fileId is required.", "5f4c9d8a-7c39-4bfa-9dcb-09f17e0f7a25"))
	}
	// upstream import-zip.ts:30 は drive file の存在確認をせず無条件で
	// createImportCustomEmojisJob を enqueue する (存在しなければ job が後で失敗)。
	// mk-go の同期 NO_SUCH_FILE check は upstream に無い divergence なので外す
	// (= 不正 fileId でも 204 を返し job 側に委ねる、#1999)。
	if h.emojiEnqueuer == nil {
		return c.NoContent(http.StatusNoContent)
	}
	user := middleware.GetUser(c)
	if user == nil {
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.emojiEnqueuer.EnqueueImportCustomEmojis(queue.ImportCustomEmojisPayload{
		UserID: user.ID,
		FileID: req.FileID,
	}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Failed to enqueue emoji import.", "89a6d9fd-0fe6-4c3c-9daa-7c6b1f29f1a4"))
	}
	return c.NoContent(http.StatusNoContent)
}

// EmojiListRemote handles POST /api/admin/emoji/list-remote.
func (h *Handler) EmojiListRemote(c echo.Context) error {
	if h.emojiRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	var req struct {
		Query     string `json:"query"`
		Host      string `json:"host"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
		Limit     *int   `json:"limit"`
		Offset    int    `json:"offset"`
	}
	_ = c.Bind(&req)
	limit, limitOK := pagination.ResolveLimit(req.Limit, 10, 100)
	if !limitOK {
		return apierr.JSONInvalidParam(c)
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	// upstream list-remote.ts:69 は host を toPuny (lowercase + IDN punycode) して
	// から equality 突合する。保存側も #2706 で同じ正規化を掛けるようになったので、
	// IDN / 大文字混在の host param を正規化しないと match しない (#1948-13)。
	// **backfill 前の行はここでは救えない** — 完全一致なので、非正規化で保存された
	// emoji は `cmd/backfill-remote-host` を流すまで引けない。
	host := req.Host
	if host != "" {
		host = toPunyHost(host)
	}
	emojis, err := h.emojiRepo.ListRemoteWithFilter(req.Query, host, sinceID, untilID, limit, req.Offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	if emojis == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// upstream Misskey の admin/emoji/list-remote は EmojiDetailed schema を
	// 返す (= `url` field、`publicUrl` ではない)。frontend の旧
	// custom-emojis-manager.vue は emoji.url を読み込んで <img> を組み立てる
	// ため、raw model.Emoji (publicUrl / originalUrl 持ち、url 無し) を返すと
	// 画像が壊れて表示そのものが空に見える (#466)。entity.PackEmojiDetailedList
	// で publicUrl → url 変換した shape にする。
	return c.JSON(http.StatusOK, entity.PackEmojiDetailedList(emojis))
}

// EmojiRemoveAliasesBulk handles POST /api/admin/emoji/remove-aliases-bulk.
func (h *Handler) EmojiRemoveAliasesBulk(c echo.Context) error {
	var req struct {
		IDs     []string `json:"ids"`
		Aliases []string `json:"aliases"`
	}
	if err := c.Bind(&req); err != nil || len(req.IDs) == 0 {
		return c.NoContent(http.StatusNoContent)
	}
	if h.emojiRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	rows, err := h.emojiRepo.FindManyByIDs(req.IDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	removeSet := make(map[string]bool, len(req.Aliases))
	for _, a := range req.Aliases {
		removeSet[a] = true
	}
	for _, e := range rows {
		filtered := make([]string, 0, len(e.Aliases))
		for _, a := range e.Aliases {
			if !removeSet[a] {
				filtered = append(filtered, a)
			}
		}
		// model.StringArray wrap (#896 と同 pattern) — add-aliases-bulk と
		// 同じ理由で plain []string では SQLSTATE 42804 になる。
		if err := h.emojiRepo.UpdateFields(e.ID, map[string]any{"aliases": model.StringArray(filtered)}); err != nil {
			// per-emoji 失敗は他 emoji への影響を避けるため warn log で
			// 観測しつつ continue する (add-aliases-bulk と同 policy)。
			slog.WarnContext(c.Request().Context(), "admin/emoji/remove-aliases-bulk: per-emoji update failed",
				"emojiId", e.ID, "err", err)
		}
	}
	h.publishEmojiUpdatedByIDs(req.IDs) // #2046
	return c.NoContent(http.StatusNoContent)
}

// EmojiSetAliasesBulk handles POST /api/admin/emoji/set-aliases-bulk.
//
// 全 id で同じ aliases 値を設定するため UpdateFieldsMany (WHERE IN UPDATE) で
// 1 文に集約できる。
func (h *Handler) EmojiSetAliasesBulk(c echo.Context) error {
	var req struct {
		IDs     []string `json:"ids"`
		Aliases []string `json:"aliases"`
	}
	if err := c.Bind(&req); err != nil || len(req.IDs) == 0 {
		return c.NoContent(http.StatusNoContent)
	}
	if h.emojiRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// model.StringArray wrap (#896 と同 pattern) — UpdateFieldsMany 経由でも
	// 同 drift が発生するので caller 側で wrap する。
	if err := h.emojiRepo.UpdateFieldsMany(req.IDs, map[string]any{"aliases": model.StringArray(req.Aliases)}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	h.publishEmojiUpdatedByIDs(req.IDs) // #2046
	return c.NoContent(http.StatusNoContent)
}

// EmojiSetCategoryBulk handles POST /api/admin/emoji/set-category-bulk.
func (h *Handler) EmojiSetCategoryBulk(c echo.Context) error {
	var req struct {
		IDs      []string `json:"ids"`
		Category *string  `json:"category"`
	}
	if err := c.Bind(&req); err != nil || len(req.IDs) == 0 {
		return c.NoContent(http.StatusNoContent)
	}
	if h.emojiRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// upstream set-category-bulk は category nullable ('Use null to reset') で
	// ps.category ?? null を書く。*string にして JSON null を SQL NULL に落とす
	// (旧 non-pointer string だと null が "" になっていた、#1948-13)。
	if err := h.emojiRepo.UpdateFieldsMany(req.IDs, map[string]any{"category": nullableString(req.Category)}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	h.publishEmojiUpdatedByIDs(req.IDs) // #2046
	return c.NoContent(http.StatusNoContent)
}

// nullableString returns the dereferenced string for a non-nil pointer, else nil
// so a GORM map Updates writes SQL NULL (matching upstream's `?? null` reset
// semantics for nullable emoji fields, #1948-13).
func nullableString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// EmojiSetLicenseBulk handles POST /api/admin/emoji/set-license-bulk.
func (h *Handler) EmojiSetLicenseBulk(c echo.Context) error {
	var req struct {
		IDs     []string `json:"ids"`
		License *string  `json:"license"`
	}
	if err := c.Bind(&req); err != nil || len(req.IDs) == 0 {
		return c.NoContent(http.StatusNoContent)
	}
	if h.emojiRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// upstream set-license-bulk も license nullable で ps.license ?? null を書く (#1948-13)。
	if err := h.emojiRepo.UpdateFieldsMany(req.IDs, map[string]any{"license": nullableString(req.License)}); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	h.publishEmojiUpdatedByIDs(req.IDs) // #2046
	return c.NoContent(http.StatusNoContent)
}
