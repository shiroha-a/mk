package federation_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
)

// actorWithSuspended returns sampleActor with the Mastodon `suspended`
// extension set to the given literal (raw JSON なので "true" / `"yes"` など
// 壊れた形も渡せる)。
func actorWithSuspended(t *testing.T, literal string) string {
	t.Helper()
	out := strings.Replace(sampleActor,
		`"type": "Person",`,
		`"type": "Person",`+"\n\t\"suspended\": "+literal+",", 1)
	// **置換が空振りしたら落とす。** `strings.Replace` は一致しなければ元の
	// 文字列をそのまま返すので、`sampleActor` の整形が変わると
	// 「suspended を含まない actor」で否定側テスト (false で解除しない /
	// 壊れた値で凍結しない) が**空虚に PASS** する。
	if !strings.Contains(out, `"suspended"`) {
		t.Fatalf("actorWithSuspended: 置換が空振りした (sampleActor の整形が変わった?)")
	}
	return out
}

// **発信元が凍結を明示していたら、取り込んだ時点で凍結する** (#2951)。
//
// Mastodon 系は凍結しても `Delete` を送らず actor に `suspended: true` を
// 立てるだけなので、読まないと「相手は切ったのにこちらでは生きている」状態に
// なる。Misskey 系は `Delete` を送るので同じ結果になる。
func TestResolveActor_SuspendedTrue_SuspendsNewUser(t *testing.T) {
	r, repo := newResolver(t, actorWithSuspended(t, "true"), nil)

	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	assert.Truef(t, user.IsSuspended,
		"発信元が suspended を立てているのに凍結されていない")

	stored := repo.Users[user.ID]
	require.NotNil(t, stored)
	assert.True(t, stored.IsSuspended, "保存された行にも反映されていること")
}

// 立っていないときは触らない (既定の挙動を変えない)。
func TestResolveActor_NoSuspended_DoesNotSuspend(t *testing.T) {
	r, _ := newResolver(t, sampleActor, nil)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	assert.False(t, user.IsSuspended)
}

// **`false` では解除しない。これが一方向である理由そのもの。**
//
// 解除まで追従すると、**侵害された発信元がこちらのモデレーターの判断を
// 取り消せる**。`true` は「相手が自分の利用者を切った」という自己申告なので
// 安全に受け取れるが、`false` は「こちらの判断を否定する」意味を持ちうる。
func TestResolveActor_SuspendedFalse_DoesNotUnsuspend(t *testing.T) {
	r, repo := newResolver(t, actorWithSuspended(t, "false"), nil)

	// ローカルのモデレーターが先に凍結している状態を作る。
	host := "remote.example"
	uri := "https://remote.example/users/alice"
	repo.Users["existing"] = &model.User{
		ID: "existing", Username: "alice", UsernameLower: "alice",
		Host: &host, URI: &uri, IsSuspended: true,
	}

	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	assert.Truef(t, user.IsSuspended,
		"発信元の suspended:false でローカルの凍結が解除された。"+
			"侵害された発信元がモデレーターの判断を取り消せてしまう")
	assert.True(t, repo.Users["existing"].IsSuspended, "保存された行も凍結のままであること")
}

// 既存ユーザーが後から凍結されたら追従する (更新経路)。
func TestResolveActor_SuspendedTrue_SuspendsExistingUser(t *testing.T) {
	r, repo := newResolver(t, actorWithSuspended(t, "true"), nil)

	host := "remote.example"
	uri := "https://remote.example/users/alice"
	repo.Users["existing"] = &model.User{
		ID: "existing", Username: "alice", UsernameLower: "alice",
		Host: &host, URI: &uri, IsSuspended: false,
	}

	user, err := r.ResolveActor(uri)
	require.NoError(t, err)
	assert.Truef(t, user.IsSuspended, "後から凍結された actor に追従していない")
	assert.True(t, repo.Users["existing"].IsSuspended)
}

// **壊れた値で誤って凍結しない。** `APLenientBool` は読めない形を false に
// 倒す (`APTruthyBool` だと不明な形が true になり、意味が反転する)。
func TestResolveActor_SuspendedMalformed_DoesNotSuspend(t *testing.T) {
	// `[]` / `{"a":1}` / `"maybe"` は **`APLenientBool` と `APTruthyBool` で
	// 結果が変わる**入力 (実測)。後者だと true になり、壊れた値で誤凍結する。
	for _, literal := range []string{`"no"`, `{"a":1}`, `[]`, `null`, `0`, `"maybe"`} {
		t.Run(literal, func(t *testing.T) {
			r, _ := newResolver(t, actorWithSuspended(t, literal), nil)
			user, err := r.ResolveActor("https://remote.example/users/alice")
			require.NoErrorf(t, err, "actor ごと読めなくなっている (document を壊してはいけない)")
			assert.Falsef(t, user.IsSuspended,
				"%s を凍結として読んでいる。壊れた値で誤凍結する", literal)
		})
	}
}

// 文字列の "true" は PostgreSQL の boolean 入力構文として真。
func TestResolveActor_SuspendedStringTrue_Suspends(t *testing.T) {
	r, _ := newResolver(t, actorWithSuspended(t, `"true"`), nil)
	user, err := r.ResolveActor("https://remote.example/users/alice")
	require.NoError(t, err)
	assert.True(t, user.IsSuspended)
}
