package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	queuemetrics "github.com/shiroha-a/mk/internal/queue/metrics"
)

// TestMetricsRoute_WiringEmits200WithExpectedSeries is a smoke test for the
// wiring between queuemetrics.Metrics, prometheus.Registry, and the Echo
// /metrics endpoint. It does NOT bootstrap a full Server (which would
// require Redis / Postgres); instead it replays the exact construction
// sequence used by router.go's setupRoutes so a regression in the wiring
// (forgotten Register / Echo route / mime type) gets caught here even
// though internal/server has the 0% coverage exception.
func TestMetricsRoute_WiringEmits200WithExpectedSeries(t *testing.T) {
	metrics := queuemetrics.New()
	registry := prometheus.NewRegistry()
	require.NoError(t, metrics.Register(registry))

	e := echo.New()
	e.GET("/metrics", echo.WrapHandler(promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	bodyStr := string(body)

	// 5 metric (ADR §6.1 で finalize) の HELP / TYPE が応答に含まれること
	// = collectors が全て Register され、scrape pipeline が動くことの確認。
	for _, want := range []string{
		"# HELP mk_job_dispatch_wait_seconds",
		"# TYPE mk_job_dispatch_wait_seconds histogram",
		"# HELP mk_job_processing_seconds",
		"# TYPE mk_job_processing_seconds histogram",
		"# HELP mk_job_scale_events_total",
		"# TYPE mk_job_scale_events_total counter",
		"# HELP mk_job_scrape_errors_total",
		"# TYPE mk_job_scrape_errors_total counter",
	} {
		assert.Contains(t, bodyStr, want)
	}

	// pull-mode metric は BindDriver 前なので body に出ない。
	// (driverCollector 自体が未 register なので Describe / Collect は呼ばれない)
	assert.NotContains(t, bodyStr, "mk_job_workers_active")
	assert.NotContains(t, bodyStr, "mk_job_queue_pending ")
}
