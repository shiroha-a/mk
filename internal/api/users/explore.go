package users

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/meself"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// SetMetaRepo attaches a MetaRepository for POST /api/pinned-users.
//
// 未配線なら pinned-users は空配列を返す (upstream も `pinnedUsers` が空なら
// 同じ結果になるので、shape は変わらない)。
func (h *Handler) SetMetaRepo(r repository.MetaRepository) { h.metaRepo = r }

// SetLocalHost records the instance hostname used to resolve `@user@host`
// entries in `meta.pinnedUsers`.
//
// 空でも動く — その場合 `@user@own.example` 形式の指定が remote 扱いになり
// 引けなくなるだけで、`@user` 形式は従来どおり解決する。
func (h *Handler) SetLocalHost(host string) { h.localHost = host }

// List serves the public user directory.
//
// POST /api/users
//
// **読めなくても 200 で空配列を返す。** upstream の explore ページは配列前提で
// `.map()` するので、ここで 500 にするとページ全体が開かなくなる。
func (h *Handler) List(c echo.Context) error {
	var req struct {
		Limit    int    `json:"limit"`
		Offset   int    `json:"offset"`
		Sort     string `json:"sort"`
		State    string `json:"state"`
		Origin   string `json:"origin"`
		Hostname string `json:"hostname"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// upstream users.ts:35 の state enum は ['all','alive'] (default 'all')。
	// 範囲外 (moderator/admin 等の role state) は ajv が 400 で reject するので、
	// public /users でも同じく弾く (ListUsers の role filter に到達させない、#1996)。
	if !ValidListState(req.State) {
		return apierr.JSONInvalidParam(c)
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Origin == "" {
		req.Origin = "local"
	}
	viewer := middleware.GetUser(c)
	viewerID := ""
	if viewer != nil {
		viewerID = viewer.ID
	}
	if h.userRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	// upstream users.ts: base filter isExplorable=TRUE AND isSuspended=FALSE、
	// hostname 絞り込み、認証時は mute/block 除外 (#1957-b)。
	list, err := h.userRepo.ListUsers(model.UserListFilter{
		State: req.State, Origin: req.Origin, Sort: req.Sort,
		Limit: req.Limit, Offset: req.Offset,
		Hostname:             req.Hostname,
		ExplorableOnly:       true,
		ExcludeRelatedTo:     viewerID,
		UpdatedAtSortNonNull: true, // #1975: public /users は updatedAt sort で NULL updatedAt を除外
	})
	if err != nil {
		return c.JSON(http.StatusOK, []any{})
	}
	ctx := c.Request().Context()
	result := make([]any, 0, len(list))
	for _, u := range list {
		profile, _ := h.userRepo.FindProfileByUserID(u.ID)
		// idGen を渡して createdAt を有効にする。未配線だと createdAt="" で
		// misskey_dart の DateTimeConverter が FormatException で落ちる (#1251)。
		d := entity.PackUserDetailed(u, profile, h.idGen)
		// 認証 caller には viewer->user の relation block を付与 (#1957-a)。
		h.viewerRelationRepos().Apply(&d, viewerID, u, profile)
		// upstream の pack は isDetailed && isMe で MeDetailed を返す。
		result = append(result, meself.Pack(ctx, d, u, profile, viewer))
	}
	return c.JSON(http.StatusOK, result)
}

// PinnedUsers serves the instance's featured accounts.
//
// POST /api/pinned-users
//
// 引けない acct は**黙って飛ばす**。`meta.pinnedUsers` は管理者が手で書く
// 文字列で、typo や退会済みの指定が混ざりうる。1 件のせいで全体が空になる
// ほうが困る。
func (h *Handler) PinnedUsers(c echo.Context) error {
	if h.metaRepo == nil || h.userRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	m, err := h.metaRepo.Fetch()
	if err != nil || m == nil || len(m.PinnedUsers) == 0 {
		return c.JSON(http.StatusOK, []any{})
	}
	viewerID := ""
	if v := middleware.GetUser(c); v != nil {
		viewerID = v.ID
	}
	result := make([]entity.UserDetailed, 0, len(m.PinnedUsers))
	for _, acct := range m.PinnedUsers {
		username, host := ParseAcct(acct, h.localHost)
		if username == "" {
			continue
		}
		u, err := h.userRepo.FindByUsernameLower(strings.ToLower(username), host)
		if err != nil {
			continue
		}
		profile, _ := h.userRepo.FindProfileByUserID(u.ID)
		d := entity.PackUserDetailed(u, profile, h.idGen)
		// 認証 caller には viewer->user の relation block を付与 (#1957-a)。
		h.viewerRelationRepos().Apply(&d, viewerID, u, profile)
		result = append(result, d)
	}
	return c.JSON(http.StatusOK, result)
}

// ParseAcct splits an `@user@host` string into its username and host parts.
//
// host は**自インスタンスなら nil** を返す (local user は host 列が NULL)。
// 先頭の `@` は付いていてもいなくてもよい。
func ParseAcct(acct, localHost string) (username string, host *string) {
	acct = strings.TrimSpace(acct)
	acct = strings.TrimPrefix(acct, "@")
	if acct == "" {
		return "", nil
	}
	at := strings.IndexByte(acct, '@')
	if at < 0 {
		return acct, nil
	}
	name := acct[:at]
	h := strings.ToLower(acct[at+1:])
	if name == "" {
		return "", nil
	}
	if h == "" || h == localHost {
		return name, nil
	}
	return name, &h
}
