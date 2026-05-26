// Package federation provides /api/federation/* endpoints.
package federation

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	coreinstance "github.com/shiroha-a/mk/internal/core/instance"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// ActorResolver is the narrow subset of core/federation.Resolver that
// federation/update-remote-user needs. Kept as an interface so unit tests can
// swap in a fake without pulling in HTTP transports.
type ActorResolver interface {
	ForceResolveActor(uri string) (*model.User, error)
}

// Handler handles federation-related API endpoints.
type Handler struct {
	svc           *coreinstance.Service
	followingRepo repository.FollowingRepository
	userRepo      repository.UserRepository
	resolver      ActorResolver
}

// NewHandler creates a new federation Handler.
func NewHandler(svc *coreinstance.Service) *Handler {
	return &Handler{svc: svc}
}

// SetFollowingRepo attaches a FollowingRepository for followers/following lookup.
func (h *Handler) SetFollowingRepo(r repository.FollowingRepository) {
	h.followingRepo = r
}

// SetUserRepo attaches a UserRepository for the users-per-host listing.
func (h *Handler) SetUserRepo(r repository.UserRepository) {
	h.userRepo = r
}

// SetResolver attaches an ActorResolver for update-remote-user.
func (h *Handler) SetResolver(r ActorResolver) {
	h.resolver = r
}

// InstancesRequest is the request body for federation/instances.
type InstancesRequest struct {
	Host          string `json:"host"`
	Suspended     *bool  `json:"suspended"`
	NotResponding *bool  `json:"notResponding"`
	Blocked       *bool  `json:"blocked"`
	Silenced      *bool  `json:"silenced"`
	Federating    *bool  `json:"federating"`
	Subscribing   *bool  `json:"subscribing"`
	Publishing    *bool  `json:"publishing"`
	Sort          string `json:"sort"`
	Limit         int    `json:"limit"`
	Offset        int    `json:"offset"`
	SinceID       string `json:"sinceId"`
	UntilID       string `json:"untilId"`
	SinceDate     *int64 `json:"sinceDate"`
	UntilDate     *int64 `json:"untilDate"`
}

// Instances handles POST /api/federation/instances.
//
// frontend Paginator (cursor mode) は untilId / sinceId を forward する (#493)。
func (h *Handler) Instances(c echo.Context) error {
	var req InstancesRequest
	if err := c.Bind(&req); err != nil {
		return apierr.JSONInvalidParam(c)
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	// blocked / silenced は instance 列ではなく meta.blockedHosts /
	// silencedHosts との突合で判定する。meta は 1 回だけ取得し、フィルタ突合と
	// レスポンスの isBlocked / isSilenced / isMediaSilenced 算出の双方で使う。
	blockedHosts, silencedHosts, mediaSilencedHosts := h.svc.FederationHostLists()
	filter := model.InstanceListFilter{
		Host:          req.Host,
		Suspended:     req.Suspended,
		NotResponding: req.NotResponding,
		Blocked:       req.Blocked,
		Silenced:      req.Silenced,
		BlockedHosts:  blockedHosts,
		SilencedHosts: silencedHosts,
		Federating:    req.Federating,
		Subscribing:   req.Subscribing,
		Publishing:    req.Publishing,
		SortBy:        req.Sort,
		Limit:         req.Limit,
		Offset:        req.Offset,
		SinceID:       sinceID,
		UntilID:       untilID,
	}
	rows, err := h.svc.List(filter)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, inst := range rows {
		out = append(out, instanceToMap(inst, blockedHosts, silencedHosts, mediaSilencedHosts))
	}
	return c.JSON(http.StatusOK, out)
}

// ShowInstanceRequest is the request body for federation/show-instance.
type ShowInstanceRequest struct {
	Host string `json:"host"`
}

// ShowInstance handles POST /api/federation/show-instance.
func (h *Handler) ShowInstance(c echo.Context) error {
	var req ShowInstanceRequest
	if err := c.Bind(&req); err != nil || req.Host == "" {
		return apierr.JSONInvalidParam(c)
	}
	inst, err := h.svc.FindByHost(req.Host)
	if err != nil {
		// upstream Misskey TS は該当 instance 行がないとき 204 No Content を
		// 返す (= null 相当)。frontend admin の lookup UI も 204 を「未知 host」
		// として扱うため drop-in 互換でもこの shape に合わせる (#915)。
		// DB 障害等の真の error は 500 として観測性を保つ。
		if errors.Is(err, coreinstance.ErrInstanceNotFound) {
			return c.NoContent(http.StatusNoContent)
		}
		slog.Error("federation/show-instance: FindByHost failed", "host", req.Host, "err", err)
		return apierr.JSONInternalError(c)
	}
	blockedHosts, silencedHosts, mediaSilencedHosts := h.svc.FederationHostLists()
	return c.JSON(http.StatusOK, instanceToMap(inst, blockedHosts, silencedHosts, mediaSilencedHosts))
}

// instanceToMap shapes an Instance row into the JSON response object expected
// by misskey-js. 不明値や nil ポインタはそのまま JSON null になるよう map に
// 載せる。
//
// federating / subscribing / publishingは本家Misskeyと同様に
// followingCount / followersCountから動的に計算する (DBには列を持たない)。
//
// isBlocked / isSilenced / isMediaSilenced は instance 列ではなく
// meta.blockedHosts / silencedHosts / mediaSilencedHosts との suffix-match で
// 判定する (本家 InstanceEntityService と同じ)。突合対象の host 一覧は呼び出し
// 元が meta から 1 度だけ取得して渡す。
func instanceToMap(inst *model.Instance, blockedHosts, silencedHosts, mediaSilencedHosts []string) map[string]any {
	return map[string]any{
		"id":                      inst.ID,
		"firstRetrievedAt":        inst.FirstRetrievedAt,
		"host":                    inst.Host,
		"usersCount":              inst.UsersCount,
		"notesCount":              inst.NotesCount,
		"followingCount":          inst.FollowingCount,
		"followersCount":          inst.FollowersCount,
		"federating":              inst.FollowingCount > 0 || inst.FollowersCount > 0,
		"subscribing":             inst.FollowersCount > 0,
		"publishing":              inst.FollowingCount > 0,
		"latestRequestReceivedAt": inst.LatestRequestReceivedAt,
		"isNotResponding":         inst.IsNotResponding,
		"notRespondingSince":      inst.NotRespondingSince,
		"isSuspended":             inst.SuspensionState != model.SuspensionStateNone,
		"suspensionState":         inst.SuspensionState,
		"isBlocked":               coreinstance.HostMatchesAny(blockedHosts, inst.Host),
		"isSilenced":              coreinstance.HostMatchesAny(silencedHosts, inst.Host),
		"isMediaSilenced":         coreinstance.HostMatchesAny(mediaSilencedHosts, inst.Host),
		"softwareName":            inst.SoftwareName,
		"softwareVersion":         inst.SoftwareVersion,
		"openRegistrations":       inst.OpenRegistrations,
		"name":                    inst.Name,
		"description":             inst.Description,
		"maintainerName":          inst.MaintainerName,
		"maintainerEmail":         inst.MaintainerEmail,
		"iconUrl":                 inst.IconURL,
		"faviconUrl":              inst.FaviconURL,
		"themeColor":              inst.ThemeColor,
		"infoUpdatedAt":           inst.InfoUpdatedAt,
		"moderationNote":          inst.ModerationNote,
	}
}
