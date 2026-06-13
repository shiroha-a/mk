package notes

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// SetFavoriteRepo attaches a NoteFavoriteRepository for favorites endpoints.
func (h *Handler) SetFavoriteRepo(r repository.NoteFavoriteRepository) {
	h.favoriteRepo = r
}

// FavoritesCreate handles POST /api/notes/favorites/create.
func (h *Handler) FavoritesCreate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		NoteID string `json:"noteId"`
	}
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "noteId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// 旧実装は noteRepo.FindByID で存在確認のみ行い visibility check が抜けて
	// いた (#1443)。note ID を既に知っている viewer が followers / specified
	// visibility の note を favorite 化でき、その後 /api/i/favorites 経由で
	// content / author を pull 可能 (favorite 一覧側は upstream 互換のため
	// 素の PackNotes で返す設計 — handler.go:633-636)。author が visibility
	// を絞った後も古い favorite 行が残るため #799「ID 既知公開」doctrine は
	// 適用不可。queryService.RequireVisible で見えない note は存在隠蔽する。
	// queryService 未配線時は fail-closed (ShowPartialBulk と同じ pattern)。
	if h.queryService == nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "6dd26674-e060-4816-909a-45ba3f4da458"))
	}
	if _, err := h.queryService.RequireVisible(user, req.NoteID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "6dd26674-e060-4816-909a-45ba3f4da458"))
	}
	if h.favoriteRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	exists, _ := h.favoriteRepo.Exists(user.ID, req.NoteID)
	if exists {
		return c.JSON(http.StatusConflict, apierr.Error("ALREADY_FAVORITED", "Already favorited.", "a402c12b-34dd-41d2-97d8-4d2ffd96a1a6"))
	}
	now := time.Now()
	fav := &model.NoteFavorite{
		ID:        h.idGen.Generate(now),
		UserID:    user.ID,
		NoteID:    req.NoteID,
		CreatedAt: now,
	}
	if err := h.favoriteRepo.Create(fav); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// FavoritesDelete handles POST /api/notes/favorites/delete.
func (h *Handler) FavoritesDelete(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		NoteID string `json:"noteId"`
	}
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "noteId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if h.favoriteRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// upstream favorites/delete.ts は getNote で存在確認 (無ければ NO_SUCH_NOTE)
	// → favorite 行が無ければ NOT_FAVORITED を返す (#1538)。旧実装は Delete を
	// 直接呼び未 favorite でも 204 を返していた。delete は自分の favorite 行のみ
	// 操作するので visibility gate は不要 (可視性を絞られた後でも un-favorite
	// できるべき)、存在確認のみ行う。
	if _, err := h.noteRepo.FindByID(req.NoteID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "80848a2c-398f-4343-baa9-df1d57696c56"))
	}
	if exists, _ := h.favoriteRepo.Exists(user.ID, req.NoteID); !exists {
		return c.JSON(http.StatusBadRequest, apierr.Error("NOT_FAVORITED", "You have not favorited that note.", "b625fc69-635e-45e9-86f4-dbefbef35af5"))
	}
	if err := h.favoriteRepo.Delete(user.ID, req.NoteID); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// Featured handles POST /api/notes/featured.
//
// 注目ノート — renoteCount + repliesCount が高いノートを返す簡易版。
// channelId 指定時は該当チャンネル内のノートだけを返す (channel.vue の
// ハイライトタブから叩かれる経路)。過去はこの絞り込みが無く、無関係な
// グローバルノートが混ざっていた (#489)。untilId は cursor pagination 用。
func (h *Handler) Featured(c echo.Context) error {
	var req struct {
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
		ChannelID string `json:"channelId"`
		UntilID   string `json:"untilId"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	// untilDate を aidx prefix に正規化 (#1166)。
	_, untilID := id.NormalizeCursor("", req.UntilID, nil, req.UntilDate)
	notes, err := h.noteRepo.ListFeatured(req.ChannelID, untilID, req.Limit, req.Offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, h.packMany(c.Request().Context(), notes, middleware.GetUser(c)))
}

// Unrenote handles POST /api/notes/unrenote.
func (h *Handler) Unrenote(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		NoteID string `json:"noteId"`
	}
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "noteId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// renoteId が指定ノートの自分のノートを探して削除
	renote, err := h.noteRepo.FindRenoteByUser(user.ID, req.NoteID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "efd4a259-2442-496b-8dd7-b255aa1a160f"))
	}
	if err := h.deleteService.Delete(user, renote.ID); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.NoContent(http.StatusNoContent)
}

// Mentions handles POST /api/notes/mentions.
func (h *Handler) Mentions(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		Limit      int    `json:"limit"`
		SinceID    string `json:"sinceId"`
		UntilID    string `json:"untilId"`
		SinceDate  *int64 `json:"sinceDate"`
		UntilDate  *int64 `json:"untilDate"`
		Visibility string `json:"visibility"`
		Following  bool   `json:"following"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "Invalid parameters.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	// visibility kind の絞り込みは ListMentions の SQL push-down に委譲する
	// (#1451)。upstream TS notes/mentions と同じく、visibility 指定時のみ
	// note.visibility = <値> で exact-match し、未指定は全種別を返す。旧実装は
	// post-fetch で specified / 非specified に振り分けていたため、ページ内が片方の
	// 種別で埋まると limit 未満になる under-fill が起きていた。
	// following=true のとき followee + 自分の note のみに絞る (upstream
	// mentions.ts following param、#1554)。
	notes, err := h.noteRepo.ListMentions(user.ID, req.Visibility, req.Following, req.Limit, sinceID, untilID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	// upstream mentions は generateBaseNoteFilteringQuery + generateMutedNoteThreadQuery
	// で被block / mute / instance-mute / thread-mute を除外する (#1554)。
	notes, err = h.applyMuteBlock(user, notes)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	notes, err = h.applyThreadMute(user, notes)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, h.packMany(c.Request().Context(), notes, user))
}

// UserListTimeline handles POST /api/notes/user-list-timeline.
func (h *Handler) UserListTimeline(c echo.Context) error {
	me := middleware.GetUser(c)
	var req struct {
		ListID                string `json:"listId"`
		Limit                 int    `json:"limit"`
		SinceID               string `json:"sinceId"`
		UntilID               string `json:"untilId"`
		SinceDate             *int64 `json:"sinceDate"`
		UntilDate             *int64 `json:"untilDate"`
		WithFiles             bool   `json:"withFiles"`
		WithRenotes           *bool  `json:"withRenotes"`
		IncludeMyRenotes      *bool  `json:"includeMyRenotes"`
		IncludeRenotedMyNotes *bool  `json:"includeRenotedMyNotes"`
		IncludeLocalRenotes   *bool  `json:"includeLocalRenotes"`
	}
	if err := c.Bind(&req); err != nil || req.ListID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "listId is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	// リスト所有権チェック (TS互換: 自分のリストのみ閲覧可)
	if h.userListRepo != nil {
		list, err := h.userListRepo.FindByID(req.ListID)
		if err != nil {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", "8fb1fbd5-e476-4c37-9fb0-43d55b63a2ff"))
		}
		if list.UserID != me.ID {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", "8fb1fbd5-e476-4c37-9fb0-43d55b63a2ff"))
		}
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	// list owner gate だけでは note の visibility を守れない。list メンバーは
	// 自由に編集できるため、未フォローのアカウントを list に詰めれば followers
	// visibility note を読めてしまう (#1442)。#1452 で visibility を
	// ListByUserList の SQL push-down に移し、LIMIT 前に絞ることで under-fill と
	// followers 判定 N+1 を解消した。viewer の見える note だけが返るため handler
	// 側の post-fetch FilterVisible / fail-closed ガードは不要。
	// RequireAuth() 配下なので me は非nil (所有権チェックでも me.ID を直接参照)。
	//
	// renote/file 系 param は他 timeline と同じ filter で SQL push-down する
	// (#1498)。WithReplies は per-member (membership.withReplies, #1496) で別途
	// 処理されるため filter には積まない。
	// upstream user-list-timeline.ts:188-201 は generateBaseNoteFilteringQuery +
	// generateMutedUserRenotesQueryForNotes + mutedChannelIds を適用するため、
	// 他 timeline と同じ base-filter (user-mute / renote-mute / 被block /
	// instance-mute / channel-mute) を積む (#1681)。
	filter := model.TimelineDBFilter{
		ViewerID:                me.ID,
		WithFiles:               req.WithFiles,
		WithRenotes:             req.WithRenotes,
		IncludeMyRenotes:        req.IncludeMyRenotes,
		IncludeRenotedMyNotes:   req.IncludeRenotedMyNotes,
		IncludeLocalRenotes:     req.IncludeLocalRenotes,
		UseMutingSubquery:       true,
		UseRenoteMutingSubquery: true,
		MutedChannelIDs:         h.loadMutedChannelIDs(me),
		BlockerIDs:              h.loadBlockerIDs(me),
		MutedInstances:          h.loadMutedInstances(me),
	}
	notes, err := h.noteRepo.ListByUserList(req.ListID, req.Limit, sinceID, untilID, filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, h.packMany(c.Request().Context(), notes, me))
}

// SearchByTag handles POST /api/notes/search-by-tag.
// TS互換: tag (string) または query (string[][] — AND/OR組み合わせ) を受け付ける。
// query の完全なAND/OR交差はサポートせず、最初に見つかったタグで検索する。
func (h *Handler) SearchByTag(c echo.Context) error {
	var req struct {
		Tag       string     `json:"tag"`
		Query     [][]string `json:"query"`
		Limit     int        `json:"limit"`
		SinceID   string     `json:"sinceId"`
		UntilID   string     `json:"untilId"`
		SinceDate *int64     `json:"sinceDate"`
		UntilDate *int64     `json:"untilDate"`
		Reply     *bool      `json:"reply"`
		Renote    *bool      `json:"renote"`
		Poll      *bool      `json:"poll"`
		WithFiles bool       `json:"withFiles"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "tag is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	// query 配列から tag を抽出 (tag が空の場合のフォールバック)
	if req.Tag == "" && len(req.Query) > 0 {
		for _, inner := range req.Query {
			if len(inner) > 0 && inner[0] != "" {
				req.Tag = inner[0]
				break
			}
		}
	}
	if req.Tag == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "tag is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)
	// sinceDate / untilDate を aidx prefix に正規化 (#1166)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	// tagsカラムにtagを含むノートを検索。visibility は repository 側で
	// push-down する (#1439)。discovery 系の tag 検索は notes/show の
	// 「ID 既知公開」doctrine 対象外なので、匿名/非follower には followers/
	// specified note を返さない。
	viewer := middleware.GetUser(c)
	viewerID := ""
	if viewer != nil {
		viewerID = viewer.ID
	}
	// reply/renote/poll/withFiles で絞る (upstream search-by-tag.ts、#1554)。
	filter := model.NoteSearchTagFilter{Reply: req.Reply, Renote: req.Renote, Poll: req.Poll, WithFiles: req.WithFiles}
	notes, err := h.noteRepo.SearchByTag(req.Tag, viewerID, req.Limit, sinceID, untilID, filter)
	if err != nil {
		// tag 検索失敗は従来どおり空配列で返す (TS 互換) が、visibility
		// push-down 追加で SQL エラーも黙殺されうるため診断用に 1 行残す。
		// ユーザー挙動 (200 + 空配列) は不変で operator-actionable でもないため
		// Warn ではなく Debug に留める (#1446 review)。
		slog.Debug("notes/search-by-tag: SearchByTag failed", "tag", req.Tag, "err", err)
		return c.JSON(http.StatusOK, []entity.NoteEntity{})
	}
	// upstream search-by-tag は generateBaseNoteFilteringQuery で被block / mute /
	// instance-mute を除外する (#1554)。block/mute は post-fetch で適用する。
	notes, err = h.applyMuteBlock(viewer, notes)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}
	return c.JSON(http.StatusOK, h.packMany(c.Request().Context(), notes, viewer))
}

// SetClipRepos wires the clip repos used by notes/clips (#1554)。未配線時は
// 旧 stub 同様に空配列を返す。
func (h *Handler) SetClipRepos(clip repository.ClipRepository, clipNote repository.ClipNoteRepository, clipFavorite repository.ClipFavoriteRepository) {
	h.clipRepo = clip
	h.clipNoteRepo = clipNote
	h.clipFavoriteRepo = clipFavorite
}

// Clips handles POST /api/notes/clips.
//
// upstream notes/clips.ts: getNote で note 存在確認 (無ければ NO_SUCH_NOTE) →
// clipNotesRepository.findBy({noteId}) で note を含む clip を引き →
// clipsRepository.findBy({id IN, isPublic:true}) → clipEntityService.packMany。
// owner / 他人を問わず「その note を含む public clip」を返す (#1554)。
func (h *Handler) Clips(c echo.Context) error {
	var req struct {
		NoteID string `json:"noteId"`
	}
	if err := c.Bind(&req); err != nil || req.NoteID == "" {
		return apierr.JSONInvalidParam(c)
	}
	// clip repo 未配線時は旧 stub 互換で空配列。
	if h.clipRepo == nil || h.clipNoteRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// note 存在確認 (upstream getterService.getNote)。可視性は問わず存在のみ。
	if _, err := h.noteRepo.FindByID(req.NoteID); err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "47db1a1c-b0af-458d-8fb4-986e4efafe1e"))
	}
	clipIDs, err := h.clipNoteRepo.ListClipIDsByNote(req.NoteID)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	clips, err := h.clipRepo.ListPublicByIDs(clipIDs)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	viewer := middleware.GetUser(c)
	out := make([]map[string]any, 0, len(clips))
	for _, cl := range clips {
		// owner は clip ごとに異なりうる (note は複数 user の clip に入りうる)。
		var owner *model.User
		if h.userRepo != nil {
			owner, _ = h.userRepo.FindByID(cl.UserID)
		}
		// favoritedCount は常時、isFavorited は認証 viewer のみ、notesCount は
		// owner 閲覧時のみ (#1562、upstream ClipEntityService.pack)。
		extras := entity.ClipExtras{ShowNotesCount: viewer != nil && viewer.ID == cl.UserID}
		if h.clipFavoriteRepo != nil {
			if n, err := h.clipFavoriteRepo.CountByClip(cl.ID); err == nil {
				extras.FavoritedCount = n
			}
			if viewer != nil {
				if ok, err := h.clipFavoriteRepo.Exists(viewer.ID, cl.ID); err == nil {
					extras.IsFavorited = &ok
				}
			}
		}
		out = append(out, entity.PackClip(cl, h.idGen, owner, extras))
	}
	return c.JSON(http.StatusOK, out)
}

// Translate handles POST /api/notes/translate.
func (h *Handler) Translate(c echo.Context) error {
	var req struct {
		NoteID     string `json:"noteId"`
		TargetLang string `json:"targetLang"`
	}
	if err := c.Bind(&req); err != nil || req.NoteID == "" || req.TargetLang == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "noteId and targetLang are required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}

	if h.translator == nil {
		return c.JSON(http.StatusServiceUnavailable, apierr.Error("UNAVAILABLE", "Translator is not configured.", "50a70314-2d8a-431b-b433-efa5cc56444c"))
	}

	n, err := h.noteRepo.FindByID(req.NoteID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_NOTE", "No such note.", "bea9b03f-36e0-49c5-a4db-627a029f8971"))
	}

	// 旧実装は存在確認のみで visibility check が無く、note ID 既知の viewer が
	// followers / specified note を翻訳 = 本文の content leak + DeepL quota 消費
	// ができた (#1445)。upstream TS `notes/translate` は非可視 note を専用の
	// CANNOT_TRANSLATE_INVISIBLE_NOTE で弾く (NO_SUCH_NOTE で隠さない) ため
	// それに合わせる。queryService 未配線時は fail-closed で同エラー。
	// この gate は translator.Translate より前なので DeepL を消費しない。
	viewer := middleware.GetUser(c)
	if h.queryService == nil || !h.queryService.CanSee(viewer, n) {
		return c.JSON(http.StatusBadRequest, apierr.Error("CANNOT_TRANSLATE_INVISIBLE_NOTE", "Cannot translate invisible note.", "ea29f2ca-c368-43b3-aaf1-5ac3e74bbe5d"))
	}

	if n.Text == nil || *n.Text == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("CANNOT_TRANSLATE", "Nothing to translate.", "bef6e895-c05f-4572-96ab-58f5ae1e2e28"))
	}

	result, err := h.translator.Translate(c.Request().Context(), *n.Text, req.TargetLang)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Translation failed.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	return c.JSON(http.StatusOK, map[string]any{
		"sourceLang": result.SourceLang,
		"text":       result.Text,
	})
}

// ShowPartialBulk handles POST /api/notes/show-partial-bulk.
func (h *Handler) ShowPartialBulk(c echo.Context) error {
	var req struct {
		NoteIDs []string `json:"noteIds"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "noteIds is required.", "3d81ceae-475f-4600-b2a8-2bc116157532"))
	}
	if len(req.NoteIDs) == 0 {
		return c.JSON(http.StatusOK, []any{})
	}
	notes, err := h.noteRepo.FindManyByIDsWithUser(req.NoteIDs)
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	viewer := middleware.GetUser(c)
	// ShowPartialBulk は anonymous でも叩ける endpoint (router で RequireAuth()
	// 無し) のため、followers / specified visibility のノートが任意の閲覧者に
	// 漏洩しないよう FilterVisible に通す (#509、Devin #529 FLAG-1)。queryService
	// 未配線時は fail-closed で空配列。なお本家 show-partial-bulk は可視性
	// filter を持たないが、進行中の可視性 IDOR sweep と整合させるため mk-go では
	// filter を維持する (#1538、shape のみ本家化、意図的な divergence)。
	if h.queryService == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	notes = h.queryService.FilterVisible(viewer, notes)
	// 本家 show-partial-bulk は fetchDiffs で {id, reactions, reactionEmojis} の
	// 軽量 diff のみ返す (reaction polling 用途、本文/可視性詳細は返さない)。
	// packMany が reactions (buffered merge 込み) と reactionEmojis を解決済みなので
	// それを投影して shape を揃える (#1538)。
	packed := h.packMany(c.Request().Context(), notes, viewer)
	diffs := make([]map[string]any, 0, len(packed))
	for i := range packed {
		diffs = append(diffs, map[string]any{
			"id":             packed[i].ID,
			"reactions":      packed[i].Reactions,
			"reactionEmojis": packed[i].ReactionEmojis,
		})
	}
	return c.JSON(http.StatusOK, diffs)
}
