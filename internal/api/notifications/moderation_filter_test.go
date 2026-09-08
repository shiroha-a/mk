package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

type stubModeratorChecker struct {
	moderators map[string]bool
	admins     map[string]bool
}

func (s stubModeratorChecker) IsModerator(userID string) bool     { return s.moderators[userID] }
func (s stubModeratorChecker) IsAdministrator(userID string) bool { return s.admins[userID] }

func seedAbuseReport(t *testing.T, svc *notification.Service, notifieeID string) {
	t.Helper()
	_, err := svc.Create(context.Background(), notification.CreateInput{
		NotifieeID: notifieeID, NotifierID: "reporter", Type: notification.TypeAbuseReport,
		Extra: map[string]any{"reportId": "r1", "targetUserId": "u2", "comment": "spam"},
	})
	require.NoError(t, err)
}

func listNotifications(t *testing.T, h *Handler, userID string) []map[string]any {
	t.Helper()
	c, rec := newJSONRequest(t, "/api/i/notifications", `{}`)
	setAuth(c, &model.User{ID: userID})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var out []map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(rec.Body.String())), &out))
	return out
}

// #2868: モデレーターには通報の通知が見える。
func TestShow_AbuseReportVisibleToModerator(t *testing.T) {
	h, svc := newTestHandler(t)
	h.SetModeratorChecker(stubModeratorChecker{moderators: map[string]bool{"mod1": true}})
	seedAbuseReport(t, svc, "mod1")

	out := listNotifications(t, h, "mod1")
	require.Len(t, out, 1)
	assert.Equal(t, "abuseReport", out[0]["type"])
	assert.Equal(t, "r1", out[0]["reportId"])
}

// administrator も通す (upstream の iAmModerator と同じ扱い)。
func TestShow_AbuseReportVisibleToAdministrator(t *testing.T) {
	h, svc := newTestHandler(t)
	h.SetModeratorChecker(stubModeratorChecker{admins: map[string]bool{"root": true}})
	seedAbuseReport(t, svc, "root")

	out := listNotifications(t, h, "root")
	require.Len(t, out, 1)
}

// **権限を失うと見えなくなる (#2868)。** 通報本文と対象ユーザー ID が Extra に
// 入っているので、admin/abuse-user-reports が 403 になった後も通知欄から
// 読み続けられてはいけない。
func TestShow_AbuseReportHiddenAfterPrivilegeLoss(t *testing.T) {
	h, svc := newTestHandler(t)
	h.SetModeratorChecker(stubModeratorChecker{})
	seedAbuseReport(t, svc, "exmod")

	out := listNotifications(t, h, "exmod")
	assert.Empty(t, out, "権限を失った利用者に通報の通知を返してはいけない")
}

// checker 未配線なら返さない (fail-closed)。
func TestShow_AbuseReportHiddenWithoutChecker(t *testing.T) {
	h, svc := newTestHandler(t)
	seedAbuseReport(t, svc, "mod1")

	out := listNotifications(t, h, "mod1")
	assert.Empty(t, out, "checker 未配線では通報の通知を返してはいけない (fail-closed)")
}

// 他の型は権限に関係なく返る (filter が広すぎないこと)。
func TestShow_OtherTypesUnaffectedByModerationFilter(t *testing.T) {
	h, svc := newTestHandler(t)
	h.SetModeratorChecker(stubModeratorChecker{})
	_, err := svc.Create(context.Background(), notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	out := listNotifications(t, h, "alice")
	require.Len(t, out, 1)
	assert.Equal(t, "follow", out[0]["type"])
}

// **通報者をミュートしていても通報は届く (#2868)。** notifier は通報者なので、
// 通常の valid-notifier filter に掛けるとモデレーターがミュートした相手からの
// 通報が通知欄に一切現れなくなる。
func TestShow_AbuseReportSurvivesNotifierMute(t *testing.T) {
	h, svc := newTestHandler(t)
	h.SetModeratorChecker(stubModeratorChecker{moderators: map[string]bool{"mod1": true}})
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	mutingRepo := testutil.NewMockMutingRepository()
	userRepo.Users["reporter"] = &model.User{ID: "reporter", Username: "reporter"}
	require.NoError(t, mutingRepo.Create(&model.Muting{ID: "mu1", MuterID: "mod1", MuteeID: "reporter"}))
	h.SetRepos(userRepo, noteRepo)
	h.SetMutingRepo(mutingRepo)
	seedAbuseReport(t, svc, "mod1")

	out := listNotifications(t, h, "mod1")
	require.Len(t, out, 1, "ミュートした相手からの通報も通知欄に出す")
}
