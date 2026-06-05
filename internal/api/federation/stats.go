package federation

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/pagination"
	"github.com/shiroha-a/mk/internal/model"
)

// Stats handles POST /api/federation/stats.
//
// 連合統計: followers / following がもっとも多いリモートインスタンス上位
// N 件と、そのほかのインスタンスをまとめた残り総計を返す。
func (h *Handler) Stats(c echo.Context) error {
	// admin/overview は GET で叩いてくるので `query` タグも必要
	// (#421 Devin review: charts.Request と同じ pattern)。
	var req struct {
		Limit int `json:"limit" query:"limit"`
	}
	_ = c.Bind(&req)
	req.Limit = pagination.ClampLimit(req.Limit, 10, 100)

	empty := map[string]any{
		"topSubInstances":     []any{},
		"otherFollowersCount": 0,
		"topPubInstances":     []any{},
		"otherFollowingCount": 0,
	}
	if h.svc == nil {
		return c.JSON(http.StatusOK, empty)
	}

	// "+" 接頭辞が DESC (= 上位順) なので、followers / following の多い順を
	// 取るには +followers / +following を渡す (repository の sort の向きを本家
	// TS の federation/instances に合わせたため)。
	subs, err := h.svc.List(model.InstanceListFilter{SortBy: "+followers", Limit: req.Limit})
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	pubs, err := h.svc.List(model.InstanceListFilter{SortBy: "+following", Limit: req.Limit})
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	totalFollowers, totalFollowing, err := totalCounts(h.svc)
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	// isBlocked / isSilenced / isMediaSilenced 用に meta の host 一覧を 1 度
	// だけ取得し、両リストの pack で使い回す。
	hosts, err := h.svc.FederationHostLists()
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	// federation/stats は公開エンドポイントなので moderationNote は出さない。
	topSubFollowers := 0
	topSub := make([]map[string]any, 0, len(subs))
	for _, inst := range subs {
		topSubFollowers += inst.FollowersCount
		topSub = append(topSub, instanceToMap(inst, hosts, false))
	}
	topPubFollowing := 0
	topPub := make([]map[string]any, 0, len(pubs))
	for _, inst := range pubs {
		topPubFollowing += inst.FollowingCount
		topPub = append(topPub, instanceToMap(inst, hosts, false))
	}

	return c.JSON(http.StatusOK, map[string]any{
		"topSubInstances":     topSub,
		"otherFollowersCount": totalFollowers - topSubFollowers,
		"topPubInstances":     topPub,
		"otherFollowingCount": totalFollowing - topPubFollowing,
	})
}

// totalCounts sums followersCount / followingCount across every instance row.
// Scans the whole instance table in pages; OK for current scale (a few
// thousand rows) and matches upstream Misskey's aggregation approach.
func totalCounts(svc interface {
	List(filter model.InstanceListFilter) ([]*model.Instance, error)
}) (int, int, error) {
	totalFollowers := 0
	totalFollowing := 0
	offset := 0
	for {
		page, err := svc.List(model.InstanceListFilter{Limit: 100, Offset: offset})
		if err != nil {
			return 0, 0, err
		}
		if len(page) == 0 {
			break
		}
		for _, inst := range page {
			totalFollowers += inst.FollowersCount
			totalFollowing += inst.FollowingCount
		}
		if len(page) < 100 {
			break
		}
		offset += 100
	}
	return totalFollowers, totalFollowing, nil
}
