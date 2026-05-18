package server

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/shiroha-a/mk/internal/queue/driver"
	queuemetrics "github.com/shiroha-a/mk/internal/queue/metrics"
)

// wireMetricsEndpoint constructs the queue metrics + Prometheus registry and
// mounts the /metrics endpoint on e. Extracted so router.setupRoutes and the
// smoke test (`metrics_route_test.go`) share a single configuration path —
// if you change the endpoint path, the handler options, or the registry
// shape here, the test covers the regression.
//
// Returns an error if metrics registration fails (= duplicate registration
// bug). Caller is responsible for the EnableMetrics gate so this helper can
// be reused from contexts that always want the endpoint (e.g. tests).
func wireMetricsEndpoint(e *echo.Echo, d driver.Driver) error {
	handler, err := buildMetricsHandler(d)
	if err != nil {
		return err
	}
	e.GET("/metrics", echo.WrapHandler(handler))
	return nil
}

// buildMetricsHandler bundles queuemetrics.New + BindDriver + Register + the
// promhttp wrapper into a single http.Handler. Exposed separately from
// wireMetricsEndpoint so tests can drive the handler directly (httptest)
// without an Echo instance.
func buildMetricsHandler(d driver.Driver) (http.Handler, error) {
	metrics := queuemetrics.New()
	metrics.BindDriver(d)
	registry := prometheus.NewRegistry()
	if err := metrics.Register(registry); err != nil {
		return nil, err
	}
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}), nil
}
