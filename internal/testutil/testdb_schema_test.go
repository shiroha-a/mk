package testutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// callerPackage は呼び出し元のパッケージから schema 名を決める (#2450)。
// ここが壊れると全パッケージが同じ schema に集まり、干渉が黙って戻る。
func TestCallerPackage_DerivesFromCallingPackage(t *testing.T) {
	// skip=1 = この test 自身のファイル -> internal/testutil。
	assert.Equal(t, "internal_testutil", callerPackage(1))
}

// runtime.Caller が取れないほど深い skip では空を返す。呼び出し元を特定できない
// まま適当な schema を選ぶより、共有側に倒して接続だけは通す。
func TestCallerPackage_UnknownCallerReturnsEmpty(t *testing.T) {
	assert.Empty(t, callerPackage(1000))
}

// identifier 上限を超えたら末尾を残す。silent に切り詰められて別パッケージと
// 同じ名前に化けるのを防ぐ。
func TestCallerPackage_LengthIsBounded(t *testing.T) {
	assert.LessOrEqual(t, len(callerPackage(1)), maxPackageSuffixLen)
}

// search_path は schema を渡したときだけ付く。空なら共有 (public) のまま。
func TestTestDSN_PinsSearchPath(t *testing.T) {
	assert.NotContains(t, testDSN(""), "search_path")
	assert.Contains(t, testDSN("internal_api_gallery"), "search_path=internal_api_gallery")
}

// 生成した schema 名がそのまま SQL identifier に入るので、英数字と `_` 以外が
// 残っていないことを担保する (injection の余地を作らない)。
func TestCallerPackage_SanitizesToIdentifierChars(t *testing.T) {
	name := callerPackage(1)
	require.NotEmpty(t, name)
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_'
		assert.Truef(t, ok, "unexpected character %q in schema name %q", r, name)
	}
}

// 「他が先に作った」系のエラーは成功扱いにする。並行して同じ schema を作りに来た
// ときに、片方だけ落とさないため。
func TestIsBenignCreateSchemaError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"duplicate schema", `ERROR: schema "x" already exists (SQLSTATE 42P06)`, true},
		{"sqlstate only", "failed: SQLSTATE 42P06", true},
		{"pg_namespace unique violation", "failed: SQLSTATE 23505", true},
		{"permission denied", "ERROR: permission denied for database (SQLSTATE 42501)", false},
		{"connection refused", "dial tcp: connection refused", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isBenignCreateSchemaError(errString(tc.msg)))
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// projectRoot はこの file の位置から repo root を導く。ずれると callerPackage が
// 相対化に失敗してフルパス由来の名前になる (壊れはしないが名前が変わる)。
func TestProjectRoot_ContainsMigrationDir(t *testing.T) {
	root := projectRoot()
	require.NotEmpty(t, root)
	assert.True(t, strings.HasSuffix(root, "mk") || strings.Contains(root, "/"),
		"unexpected project root: %s", root)
}
