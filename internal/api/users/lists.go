package users

import (
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"gorm.io/gorm"
)

// UserListFavoriteRepository is the interface for user list favorite operations.
type UserListFavoriteRepository interface {
	Create(fav *model.UserListFavorite) error
	Delete(userID, listID string) error
	ListByUser(userID string) ([]*model.UserListFavorite, error)
	Exists(userID, listID string) (bool, error)
}

// SetUserListFavoriteRepo attaches a UserListFavoriteRepository.
func (h *Handler) SetUserListFavoriteRepo(r UserListFavoriteRepository) {
	h.userListFavoriteRepo = r
}

// SetUserListRepo attaches a UserListRepository for list update endpoints.
func (h *Handler) SetUserListRepo(r repository.UserListRepository) {
	h.userListRepo = r
}

// ListsCreateFromPublic handles POST /api/users/lists/create-from-public.
//
// 公開済みの UserList (listId) から名前を引き継いだ新しい list (name) を作って、
// 元 list のメンバーをそのままコピーする。メンバー追加で一部失敗しても残りは
// 続行する (1 件のブロックや重複で全体を失敗させないほうが UX 上望ましい)。
func (h *Handler) ListsCreateFromPublic(c echo.Context) error {
	viewer := middleware.GetUser(c)
	var req struct {
		Name   string `json:"name"`
		ListID string `json:"listId"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" || req.ListID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.userListRepo == nil {
		return apierr.JSONInternalError(c)
	}
	src, err := h.userListRepo.FindByID(req.ListID)
	if err != nil || !src.IsPublic {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", "9292f798-6175-4f7d-93f4-b6742279667d"))
	}
	now := time.Now()
	newList := &model.UserList{
		ID:       h.idGen.Generate(now),
		UserID:   viewer.ID,
		Name:     req.Name,
		IsPublic: false,
	}
	if err := h.userListRepo.Create(newList); err != nil {
		return apierr.JSONInternalError(c)
	}
	members, err := h.userListRepo.ListMembers(req.ListID)
	if err != nil {
		// list 自体は既に作成済みなので、メンバーコピー失敗時も新 list を返す。
		return c.JSON(http.StatusOK, entity.PackUserList(newList, nil, h.idGen))
	}
	memberIDs := make([]string, 0, len(members))
	for _, m := range members {
		mb := &model.UserListMembership{
			ID:         h.idGen.Generate(time.Now()),
			UserListID: newList.ID,
			UserID:     m.UserID,
		}
		// メンバー追加に成功したものだけ userIds に反映する (block/dup の
		// 詳細エラーは #1550 follow-up、ここでは shape を upstream に揃える)。
		if err := h.userListRepo.AddMember(mb); err == nil {
			memberIDs = append(memberIDs, m.UserID)
		}
	}
	// upstream create-from-public.ts は res:ref'UserList' を userListEntityService.pack
	// で返す。model.UserList 生 JSON だと createdAt/userIds 欠落 + userId 露出する (#1550)。
	return c.JSON(http.StatusOK, entity.PackUserList(newList, memberIDs, h.idGen))
}

// ListsFavorite handles POST /api/users/lists/favorite.
func (h *Handler) ListsFavorite(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		ListID string `json:"listId"`
	}
	if err := c.Bind(&req); err != nil || req.ListID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.userListFavoriteRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// Misskey TS の `users/lists/favorite` は `exists({ id, isPublic: true })`
	// を満たさない list を一律 `NO_SUCH_USER_LIST` で弾く (favorite 固有の
	// エラー: push/pull/delete/show の `NO_SUCH_LIST` とは別 UUID)。所有者
	// 本人であっても private list は favorite 不可。これにより listId を
	// 知っている任意の認証ユーザーが fav row を作って `i/favorites` 経由で
	// 他人の private list の存在を fingerprint することを防ぐ (#1423)。
	// userListRepo が未配線の test 経路ではこの gate を skip する
	// (production は router が必ず wire)。
	if h.userListRepo != nil {
		list, err := h.userListRepo.FindByID(req.ListID)
		if err != nil || !list.IsPublic {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER_LIST", "No such user list.", "7dbaf3cf-7b42-4b8f-b431-b3919e580dbe"))
		}
	}
	// Misskey TS `users/lists/favorite` は既 fav を ALREADY_FAVORITED
	// (HTTP 400) で弾く (旧 mk-go は 204 を返していて shape が乖離)。UUID は
	// favorite.ts の alreadyFavorited と一致 (unfavorite の同 code とは別 UUID)。
	already, _ := h.userListFavoriteRepo.Exists(user.ID, req.ListID)
	if already {
		return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_FAVORITED", "The list has already been favorited.", "6425bba0-985b-461e-af1b-518070e72081"))
	}
	fav := &model.UserListFavorite{
		ID:         h.idGen.Generate(time.Now()),
		UserID:     user.ID,
		UserListID: req.ListID,
	}
	if err := h.userListFavoriteRepo.Create(fav); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListsUnfavorite handles POST /api/users/lists/unfavorite.
func (h *Handler) ListsUnfavorite(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		ListID string `json:"listId"`
	}
	if err := c.Bind(&req); err != nil || req.ListID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.userListFavoriteRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	// Misskey TS `users/lists/unfavorite` は favorite と同じく `exists({ id,
	// isPublic: true })` を満たさない list を NO_SUCH_USER_LIST で弾く (UUID は
	// unfavorite.ts 固有で favorite の NO_SUCH_USER_LIST とは別)。userListRepo
	// 未配線の test 経路ではこの gate を skip する (production は router が必ず wire)。
	if h.userListRepo != nil {
		list, err := h.userListRepo.FindByID(req.ListID)
		if err != nil || !list.IsPublic {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER_LIST", "No such user list.", "baedb33e-76b8-4b0c-86a8-9375c0a7b94b"))
		}
	}
	// fav row が無い場合は upstream の notFavorited (code は ALREADY_FAVORITED
	// だが UUID は favorite の alreadyFavorited とは別) を返す。delete は冪等
	// だが TS と shape を揃えるため exists を先に確認する。
	exists, _ := h.userListFavoriteRepo.Exists(user.ID, req.ListID)
	if !exists {
		return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_FAVORITED", "You have not favorited the list.", "835c4b27-463d-4cfa-969b-a9058678d465"))
	}
	if err := h.userListFavoriteRepo.Delete(user.ID, req.ListID); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListsUpdate handles POST /api/users/lists/update.
func (h *Handler) ListsUpdate(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		ListID   string  `json:"listId"`
		Name     *string `json:"name"`
		IsPublic *bool   `json:"isPublic"`
	}
	if err := c.Bind(&req); err != nil || req.ListID == "" {
		return apierr.JSONInvalidParam(c)
	}
	// upstream update.ts は name を minLength:1/maxLength:100 で validate する。
	// name は optional だが、渡されたら検証する (空文字や 100 超は INVALID_PARAM、#1550)。
	if req.Name != nil {
		if rc := utf8.RuneCountInString(*req.Name); rc < 1 || rc > 100 {
			return apierr.JSONInvalidParam(c)
		}
	}
	if h.userListRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	list, err := h.userListRepo.FindByID(req.ListID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", "796666fe-3dff-4d39-becb-8a5932c1d5b7"))
	}
	// 所有権チェック
	if list.UserID != user.ID {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", "796666fe-3dff-4d39-becb-8a5932c1d5b7"))
	}
	fields := map[string]any{}
	if req.Name != nil {
		fields["name"] = *req.Name
		list.Name = *req.Name
	}
	if req.IsPublic != nil {
		fields["isPublic"] = *req.IsPublic
		list.IsPublic = *req.IsPublic
	}
	if len(fields) > 0 {
		if err := h.userListRepo.UpdateList(req.ListID, fields); err != nil {
			return apierr.JSONInternalError(c)
		}
	}
	// upstream update.ts は res:ref'UserList' を userListEntityService.pack で返す (#1550)。
	return c.JSON(http.StatusOK, entity.PackUserList(list, h.listMemberIDs(req.ListID), h.idGen))
}

// listMemberIDs returns the member user IDs of a list for UserList packing.
// 取得失敗時は nil (PackUserList が空配列に正規化する)。
func (h *Handler) listMemberIDs(listID string) []string {
	members, err := h.userListRepo.ListMembers(listID)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.UserID)
	}
	return ids
}

// ListsUpdateMembership handles POST /api/users/lists/update-membership.
func (h *Handler) ListsUpdateMembership(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		ListID      string `json:"listId"`
		UserID      string `json:"userId"`
		WithReplies bool   `json:"withReplies"`
	}
	if err := c.Bind(&req); err != nil || req.ListID == "" || req.UserID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.userListRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	list, err := h.userListRepo.FindByID(req.ListID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", "7f44670e-ab16-43b8-b4c1-ccd2ee89cc02"))
	}
	// 所有権チェック
	if list.UserID != user.ID {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", "7f44670e-ab16-43b8-b4c1-ccd2ee89cc02"))
	}
	if err := h.userListRepo.UpdateMembership(req.ListID, req.UserID, req.WithReplies); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "588e7f72-c744-4a61-b180-d354e912bda2"))
		}
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListsGetMemberships handles POST /api/users/lists/get-memberships.
//
// upstream get-memberships.ts: listId(required) の membership 配列
// [{id, createdAt, userId, user(UserLite), withReplies}] を返す
// (requireCredential:false の public endpoint)。可視性は forPublic=false かつ
// 認証済なら own list のみ、それ以外は public list のみ (#1550)。
func (h *Handler) ListsGetMemberships(c echo.Context) error {
	viewer := middleware.GetUser(c)
	var req struct {
		ListID    string `json:"listId"`
		ForPublic bool   `json:"forPublic"`
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
	}
	if err := c.Bind(&req); err != nil || req.ListID == "" {
		return apierr.JSONInvalidParam(c)
	}
	if h.userListRepo == nil {
		return apierr.JSONInternalError(c)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}

	const noSuchList = "7bc05c21-1d7a-41ae-88f1-66820f4dc686"
	list, err := h.userListRepo.FindByID(req.ListID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", noSuchList))
	}
	// upstream: !forPublic && me!=null → own list (任意 visibility)、それ以外は public list。
	if !req.ForPublic && viewer != nil {
		if list.UserID != viewer.ID {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", noSuchList))
		}
	} else if !list.IsPublic {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_LIST", "No such list.", noSuchList))
	}

	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	members, err := h.userListRepo.ListMembershipsPage(req.ListID, sinceID, untilID, limit)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		// User relation が取れない orphan membership は skip (UserLite を pack
		// できないため。通常は FK で存在保証される)。
		if m.User == nil {
			continue
		}
		createdAt := ""
		if t, perr := h.idGen.ParseTime(m.ID); perr == nil {
			createdAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		out = append(out, map[string]any{
			"id":          m.ID,
			"createdAt":   createdAt,
			"userId":      m.UserID,
			"user":        entity.PackUserLite(m.User),
			"withReplies": m.WithReplies,
		})
	}
	return c.JSON(http.StatusOK, out)
}
