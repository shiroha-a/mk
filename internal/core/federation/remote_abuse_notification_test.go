package federation_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

type stubRemoteModeratorLister struct{ mods []*model.User }

func (s stubRemoteModeratorLister) GetModerators() ([]*model.User, error) { return s.mods, nil }

type stubRemoteInAppNotifier struct{ created []notification.CreateInput }

func (s *stubRemoteInAppNotifier) Create(_ context.Context, in notification.CreateInput) (*notification.Notification, error) {
	s.created = append(s.created, in)
	return &notification.Notification{ID: "n1", Type: in.Type}, nil
}

// #2868: リモートからの通報 (AP Flag) もモデレーターの通知欄に出す。
//
// **local の report-abuse だけ通知するのは非対称。** どちらも同じ
// abuse_user_report 行として管理画面には出るので、通知が来ないことだけが
// 症状になる。
func TestProcess_FlagNotifiesModerators(t *testing.T) {
	p, repo, _ := newProcessorWithBlocking(t)
	abuseRepo := testutil.NewMockAbuseReportRepository()
	idGenFlag, _ := id.NewGenerator("aidx")
	p.SetAbuseReportRepo(abuseRepo, idGenFlag)
	notifier := &stubRemoteInAppNotifier{}
	p.SetAbuseReportNotification(stubRemoteModeratorLister{mods: []*model.User{{ID: "mod1"}, {ID: "mod2"}}}, notifier)

	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	body := []byte(`{
		"type": "Flag",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob",
		"content": "abuse"
	}`)
	require.NoError(t, p.Process(body))
	require.Len(t, abuseRepo.Reports, 1)
	require.Len(t, notifier.created, 2, "moderator ごとに 1 件作る")

	for _, in := range notifier.created {
		assert.Equal(t, notification.TypeAbuseReport, in.Type)
		assert.Equal(t, "bob", in.Extra["targetUserId"])
		assert.NotEmpty(t, in.Extra["reportId"])
		assert.NotEmpty(t, in.Extra["comment"])
	}
	assert.ElementsMatch(t, []string{"mod1", "mod2"},
		[]string{notifier.created[0].NotifieeID, notifier.created[1].NotifieeID})
}

// 未配線なら通知を作らない (旧挙動)。
func TestProcess_FlagWithoutNotifierIsNoop(t *testing.T) {
	p, repo, _ := newProcessorWithBlocking(t)
	abuseRepo := testutil.NewMockAbuseReportRepository()
	idGenFlag, _ := id.NewGenerator("aidx")
	p.SetAbuseReportRepo(abuseRepo, idGenFlag)

	repo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	body := []byte(`{
		"type": "Flag",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/users/bob",
		"content": "abuse"
	}`)
	require.NoError(t, p.Process(body))
	assert.Len(t, abuseRepo.Reports, 1, "通知の配線が無くても通報自体は保存する")
}
