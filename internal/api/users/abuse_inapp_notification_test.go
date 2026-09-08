package users_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/model"
)

// stubInAppNotifier records the notifications report-abuse creates.
type stubInAppNotifier struct {
	created []notification.CreateInput
	err     error
}

func (s *stubInAppNotifier) Create(_ context.Context, in notification.CreateInput) (*notification.Notification, error) {
	s.created = append(s.created, in)
	if s.err != nil {
		return nil, s.err
	}
	return &notification.Notification{ID: "n1", Type: in.Type}, nil
}

// #2868: 通報はモデレーターの通知欄にも残る。
//
// **admin stream (#1549) では足りない。** あちらはその瞬間に管理画面を開いて
// いる人にしか届かない。
func TestReportAbuse_CreatesInAppNotification(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	notifier := &stubInAppNotifier{}
	h.SetAbuseReportFanout(stubModeratorLister{mods: []*model.User{{ID: "mod1"}, {ID: "mod2"}}}, &stubAbuseNotifier{})
	h.SetAbuseReportInAppNotifier(notifier)

	rec := postExtra(h.ReportAbuse, `{"userId":"u2","comment":"spam"}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, notifier.created, 2, "moderator ごとに 1 件作る")

	for _, in := range notifier.created {
		assert.Equal(t, notification.TypeAbuseReport, in.Type)
		assert.Equal(t, "u1", in.NotifierID, "notifier は通報者")
		assert.Equal(t, "u2", in.Extra["targetUserId"])
		assert.Equal(t, "spam", in.Extra["comment"])
		assert.NotEmpty(t, in.Extra["reportId"], "管理画面の該当通報へ飛ぶために要る")
	}
	assert.ElementsMatch(t, []string{"mod1", "mod2"},
		[]string{notifier.created[0].NotifieeID, notifier.created[1].NotifieeID})
}

// admin stream 未配線でも in-app 通知だけは作る。
//
// 早期 return の条件を `lister == nil || notifier == nil` のままにすると、
// 片方だけ配線した構成で無言で no-op になる。
func TestReportAbuse_InAppNotificationWithoutAdminStream(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	notifier := &stubInAppNotifier{}
	h.SetAbuseReportFanout(stubModeratorLister{mods: []*model.User{{ID: "mod1"}}}, nil)
	h.SetAbuseReportInAppNotifier(notifier)

	rec := postExtra(h.ReportAbuse, `{"userId":"u2","comment":"spam"}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, notifier.created, 1)
}

// 通知の作成に失敗しても通報自体は成功する (report は永続化済み)。
func TestReportAbuse_InAppNotificationFailureIsBestEffort(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	notifier := &stubInAppNotifier{err: assertAnError{}}
	h.SetAbuseReportFanout(stubModeratorLister{mods: []*model.User{{ID: "mod1"}}}, &stubAbuseNotifier{})
	h.SetAbuseReportInAppNotifier(notifier)

	rec := postExtra(h.ReportAbuse, `{"userId":"u2","comment":"spam"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// 未配線なら通知を作らない (旧挙動)。
func TestReportAbuse_NoInAppNotifierIsNoop(t *testing.T) {
	h, _, _ := newExtraHandler(t)
	adminNotifier := &stubAbuseNotifier{}
	h.SetAbuseReportFanout(stubModeratorLister{mods: []*model.User{{ID: "mod1"}}}, adminNotifier)

	rec := postExtra(h.ReportAbuse, `{"userId":"u2","comment":"spam"}`, &model.User{ID: "u1"})
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, adminNotifier.calls, 1, "admin stream 側は従来どおり動く")
}

type assertAnError struct{}

func (assertAnError) Error() string { return "boom" }
