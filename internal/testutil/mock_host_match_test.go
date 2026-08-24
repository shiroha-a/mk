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
		// **ここが無いと「mock だけ緩い」変異を捕まえられない。** 両辺を
		// 正規化して比べる実装 (= 保存側も正規化する前提) にすると、下の 2 つが
		// true になってしまい本番と食い違う。
		{"unicode で保存された行は punycode では引けない", "パイ.example", "xn--eckve.example", false},
		{"非正規化で保存された行は正規化形では引けない", "Mixed.Example", "mixed.example", false},
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

// **host 表記の違う 2 行があるとき、本番と同じ行を選ぶこと** (#2704 review)。
//
// 1 行しか入れないケースでは「どちらを返すか」を観測できないので、完全一致優先を
// 外す変異が素通りしてしまう。畳み込みも本番と揃っていないと、mention 解決を
// mock で書いたテストが map の反復順に依存して非決定になる。
func TestMockUserRepository_PrefersExactRowLikeProduction(t *testing.T) {
	newRepo := func() *MockUserRepository {
		repo := NewMockUserRepository()
		a, b := "mixed.example", "Mixed.Example"
		repo.Users["ua"] = &model.User{ID: "ua", Username: "Alice", UsernameLower: "alice", Host: &a}
		repo.Users["ub"] = &model.User{ID: "ub", Username: "Alice", UsernameLower: "alice", Host: &b}
		return repo
	}

	t.Run("完全一致の行を返す", func(t *testing.T) {
		// map の反復順に依存しないよう繰り返す。
		for i := 0; i < 50; i++ {
			q := "Mixed.Example"
			got, err := newRepo().FindByUsernameLower("alice", &q)
			require.NoError(t, err)
			require.Equal(t, "ub", got.ID)
		}
	})

	t.Run("一括引きは username ごとに 1 行へ畳む", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			q := "Mixed.Example"
			many, err := newRepo().FindManyByUsernamesAndHost([]string{"Alice"}, &q)
			require.NoError(t, err)
			require.Len(t, many, 1)
			require.Equal(t, "ub", many[0].ID)
		}
	})
}
