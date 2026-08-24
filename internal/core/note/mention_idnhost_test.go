package note_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// **Unicode で書かれた IDN ホストのメンションが解決されること** (#2704)。
//
// `user.host` は punycode で保存されるが、メンションの host は**書き手が打った
// まま**なので Unicode で来る。正規化しないと mention が解決されず、
// `note.mentions` に載らないので**通知も飛ばない**。ユーザーページが開けない
// のと違って、こちらは黙って落ちるので気付きにくい。
func TestResolveMentionUserIDs_IDNHost(t *testing.T) {
	const puny = "xn--eckve.example"
	repo := testutil.NewMockUserRepository()
	host := puny
	uri := "https://" + puny + "/users/idn"
	repo.Users["idn1"] = &model.User{
		ID: "idn1", Username: "IdnUser", UsernameLower: "idnuser", Host: &host, URI: &uri,
	}
	svc := corenote.NewCreateService(nil, nil, nil, nil)
	require.NotNil(t, svc)
	svc.SetUserRepo(repo)

	for _, tc := range []struct {
		name string
		host string
	}{
		{"punycode", puny},
		{"unicode", "パイ.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := svc.ResolveMentionUserIDsForTest([]corenote.Mention{
				{Username: "idnuser", Host: tc.host},
			})
			assert.Equal(t, []string{"idn1"}, got, "host=%q のメンションが解決されること", tc.host)
		})
	}

	t.Run("別の host は解決しない", func(t *testing.T) {
		got := svc.ResolveMentionUserIDsForTest([]corenote.Mention{
			{Username: "idnuser", Host: "other.example"},
		})
		assert.Empty(t, got)
	})
}
