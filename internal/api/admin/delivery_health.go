package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/deliveryhealth"
)

// DeliveryHealthProvider exposes the aggregated per-host delivery view.
// 循環依存を避けるため interface で受け取る。実装は core/deliveryhealth.Service。
type DeliveryHealthProvider interface {
	Query(ctx context.Context, window time.Duration) ([]deliveryhealth.HostHealth, error)
	EvictedHosts() int64
}

// SetDeliveryHealthProvider wires the delivery health source (#2461)。
func (h *Handler) SetDeliveryHealthProvider(p DeliveryHealthProvider) {
	h.deliveryHealth = p
}

// defaultDeliveryHealthWindow is used when the request omits `window`.
const defaultDeliveryHealthWindow = time.Hour

// FederationDeliveryHealth handles POST /api/admin/federation/delivery-health.
//
// **mk-go 独自 endpoint** (#2461)。upstream は配送結果を
// `instance.isNotResponding` の真偽値にしか残さないため、対応物が無い。
//
// 返すのは観測値だけで、配送を止める判断は含まない。
func (h *Handler) FederationDeliveryHealth(c echo.Context) error {
	if h.deliveryHealth == nil {
		// telemetry 未配線の構成では「データが無い」を返す。エラーにすると
		// 管理画面が壊れて見えるが、実際には機能が無効なだけ。
		return c.JSON(http.StatusOK, deliveryHealthResponse{
			WindowSeconds: int(defaultDeliveryHealthWindow.Seconds()),
			Hosts:         []deliveryhealth.HostHealth{},
		})
	}
	var req struct {
		// WindowSeconds は遡る秒数。上限は deliveryhealth.MaxWindow。
		WindowSeconds int `json:"windowSeconds"`
	}
	// body 無しでも既定値で応答する (管理画面が引数なしで叩く)。
	_ = c.Bind(&req)

	window := defaultDeliveryHealthWindow
	if req.WindowSeconds > 0 {
		window = time.Duration(req.WindowSeconds) * time.Second
	}
	if window > deliveryhealth.MaxWindow {
		window = deliveryhealth.MaxWindow
	}

	hosts, err := h.deliveryHealth.Query(c.Request().Context(), window)
	if err != nil {
		return apierr.JSONInternalError(c)
	}
	if hosts == nil {
		hosts = []deliveryhealth.HostHealth{}
	}
	return c.JSON(http.StatusOK, deliveryHealthResponse{
		WindowSeconds: int(window.Seconds()),
		Hosts:         hosts,
		EvictedHosts:  h.deliveryHealth.EvictedHosts(),
	})
}

// deliveryHealthResponse is the endpoint's body.
type deliveryHealthResponse struct {
	WindowSeconds int                         `json:"windowSeconds"`
	Hosts         []deliveryhealth.HostHealth `json:"hosts"`
	// EvictedHosts はメモリ上限で捨てたホスト数の累計。0 でなければ
	// 上限が足りていないので、運用者が判断できるよう出す。
	EvictedHosts int64 `json:"evictedHosts"`
}
