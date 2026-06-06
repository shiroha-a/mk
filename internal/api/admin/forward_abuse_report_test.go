package admin_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForwardAbuseUserReport(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// reportId 欠落は 204 (forwarder 未配線, abuseRepo 未配線 → no-op)
	assert.Equal(t, http.StatusNoContent, doPost(h.ForwardAbuseUserReport, `{}`, adminUser).Code)
}

type stubAbuseForwarder struct {
	calledWith string
	err        error
}

func (s *stubAbuseForwarder) ForwardReport(reportID string) error {
	s.calledWith = reportID
	return s.err
}

func TestForwardAbuseUserReport_UsesForwarderWhenWired(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	stub := &stubAbuseForwarder{}
	h.SetAbuseForwarder(stub)
	rec := doPost(h.ForwardAbuseUserReport, `{"reportId":"r1"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "r1", stub.calledWith)
}

func TestForwardAbuseUserReport_ForwarderError(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetAbuseForwarder(&stubAbuseForwarder{err: assertError{}})
	rec := doPost(h.ForwardAbuseUserReport, `{"reportId":"r1"}`, adminUser)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- moderation log assertion (#664) ---

func TestForwardAbuseUserReport_WritesModerationLog(t *testing.T) {
	// forwarder 未配線 fallback path で abuseRepo の DB フラグが立った時に
	// forwardAbuseReport log が書かれることを確認。target は remote (host あり)
	// でないと local-target guard で弾かれる。
	host := "remote.example"
	h, _ := setupAbuseReportHandler(t,
		&model.AbuseUserReport{ID: "r1", TargetUserID: "u1", ReporterID: "u2", TargetUserHost: &host},
	)
	repo := attachModLog(t, h)

	rec := doPost(h.ForwardAbuseUserReport, `{"reportId":"r1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "forwardAbuseReport", repo.Snapshot()[0].Type)
}

// ローカル対象 (targetUserHost == null) は forward 不可で 400。
func TestForwardAbuseUserReport_RejectsLocalTarget(t *testing.T) {
	h, _ := setupAbuseReportHandler(t,
		&model.AbuseUserReport{ID: "r1", TargetUserID: "u1", ReporterID: "u2"},
	)
	rec := doPost(h.ForwardAbuseUserReport, `{"reportId":"r1"}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// abuseRepo wired で report が存在しなければ NO_SUCH_ABUSE_REPORT (404)。
func TestForwardAbuseUserReport_NotFound(t *testing.T) {
	h, _ := setupAbuseReportHandler(t) // abuseRepo wired, report 無し
	rec := doPost(h.ForwardAbuseUserReport, `{"reportId":"ghost"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_ABUSE_REPORT")
	assert.Contains(t, rec.Body.String(), "8763e21b-d9bc-40be-acf6-54c1a6986493")
}

// 既に forwarded 済みの report は再 forward 不可で 400。
func TestForwardAbuseUserReport_RejectsAlreadyForwarded(t *testing.T) {
	host := "remote.example"
	h, _ := setupAbuseReportHandler(t,
		&model.AbuseUserReport{ID: "r1", TargetUserID: "u1", ReporterID: "u2", TargetUserHost: &host, Forwarded: true},
	)
	rec := doPost(h.ForwardAbuseUserReport, `{"reportId":"r1"}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
