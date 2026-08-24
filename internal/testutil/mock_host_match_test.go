package testutil

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// **mock は本番の repository (hostMatch) と同じ意味論であること** (#2704)。
//
// ずれると、mock を使うテストだけが通って本番との差が隠れる。ここを直接
// 固定しているのは、service 経由だと先に正規化されて mock まで非正規化の host が
// 届かないため (= service 経由のテストでは差が観測できない)。
func TestMockUserRepository_HostMatchesProduction(t *testing.T) {
	newRepo := func(stored string) *MockUserRepository {
		repo := NewMockUserRepository()
		h := stored
		repo.Users["u1"] = &model.User{ID: "u1", Username: "Alice", UsernameLower: "alice", Host: &h}
		return repo
	}

	cases := []struct {
		name   string
		stored string
		query  string
		want   bool
	}{
		{"完全一致", "remote.example", "remote.example", true},
		{"unicode で引く", "xn--eckve.example", "パイ.example", true},
		{"大文字 punycode で引く", "xn--eckve.example", "XN--ECKVE.EXAMPLE", true},
		{"大文字 ASCII で引く", "remote.example", "Remote.Example", true},
		{"非正規化で保存された行は保存された形で引ける", "Mixed.Example", "Mixed.Example", true},
		{"別ホストは引かない", "remote.example", "other.example", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(tc.stored)
			q := tc.query

			got, err := repo.FindByUsernameLower("alice", &q)
			if tc.want {
				require.NoError(t, err, "stored=%q query=%q", tc.stored, tc.query)
				assert.Equal(t, "u1", got.ID)
			} else {
				assert.Error(t, err, "stored=%q query=%q", tc.stored, tc.query)
			}

			many, err := repo.FindManyByUsernamesAndHost([]string{"Alice"}, &q)
			require.NoError(t, err)
			assert.Equal(t, tc.want, len(many) == 1, "一括引きも同じ結果になること")
		})
	}

	t.Run("host nil はローカル行だけ", func(t *testing.T) {
		repo := newRepo("remote.example")
		repo.Users["local"] = &model.User{ID: "local", Username: "Alice", UsernameLower: "alice"}
		got, err := repo.FindByUsernameLower("alice", nil)
		require.NoError(t, err)
		assert.Equal(t, "local", got.ID)
	})
}
