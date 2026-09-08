package server

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/effectivepolicy"
)

// mk-go 固有の role policy キーは fork frontend の 2 箇所で列挙されている。
// どちらかが落ちると、その policy は管理画面から設定できない (#2898)。
//
// **型検査では捕まらない。** どちらも `Misskey.rolePolicies` に戻しても
// vue-tsc は通り、症状は「編集しても保存されない」「開き直すと無効表示」で、
// エラーもログも出ない。#2898 で実際に 2 段階で踏んだ (roles.editor を直した
// あと roles.policy-editor に同じループが残っていた)。
//
// **突き合わせ先は effectivepolicy の defaults。** upstream 由来のキーは
// misskey-js の rolePolicies が持つので、こちらが列挙するのは「defaults に
// あって upstream に無いもの」= mk-go 固有キーだけになる。
// notInFrontendUI は「管理画面の policy 編集 UI に出していない mk-go 固有キー」を
// 理由付きで持つ。**新規流入を止めるための allowlist** で、ここに足すのは
// 「UI に出さないと決めた」ときだけ。
//
// key ごとに理由を持つ (件数だけだと、1 つ足して 1 つ消す形で素通りする)。
var notInFrontendUI = map[string]string{
	"canUseChunkedUpload":                "分割アップロード (#2313) の policy。導入時から UI に無く、instance 設定 (chunkedUploadEnabled 等) だけで運用している。UI を足すかは別途判断する",
	"chunkedUploadMaxConcurrentSessions": "同上",
	"chunkedUploadMaxPendingMb":          "同上",
}

func TestMkGoRolePolicyKeysAreListedInFrontend(t *testing.T) {
	root := filepath.Join(repoRootDir(t), "third_party", "misskey")
	consts := filepath.Join(root, "packages", "misskey-js", "src", "consts.ts")
	if _, err := os.Stat(consts); err != nil {
		if os.Getenv("MK_FRONTEND_GATES_REQUIRE_SUBMODULE") != "" {
			require.NoError(t, err, "submodule を要求する job なのに %s を読めない", consts)
		}
		t.Skipf("submodule が無い (checkout する job でのみ検査する)")
	}
	upstream := parseUpstreamRolePolicies(t, consts)
	require.NotEmpty(t, upstream, "misskey-js の rolePolicies を読めなかった")

	var mkGoKeys []string
	for key := range effectivepolicy.Defaults() {
		if !upstream[key] || notInFrontendUI[key] != "" {
			if notInFrontendUI[key] != "" {
				continue
			}
			mkGoKeys = append(mkGoKeys, key)
		}
	}
	sort.Strings(mkGoKeys)
	require.NotEmpty(t, mkGoKeys, "mk-go 固有の policy キーが 1 つも無い; この gate は何も検査していない")

	// **allowlist の陳腐化を検出する。** 消えた policy を許可し続けると、
	// 一覧に「もう存在しないキー」が残ったまま気付けない。
	defaults := effectivepolicy.Defaults()
	for key := range notInFrontendUI {
		_, ok := defaults[key]
		require.True(t, ok, "allowlist の %q は policy から消えている。allowlist から外すこと", key)
		require.False(t, upstream[key], "allowlist の %q は upstream の rolePolicies にある。allowlist から外すこと", key)
	}

	for _, file := range []string{
		filepath.Join("packages", "frontend", "src", "pages", "admin", "roles.editor.vue"),
		filepath.Join("packages", "frontend", "src", "pages", "admin", "roles.policy-editor.vue"),
	} {
		listed := parseMkGoPolicyKeyList(t, filepath.Join(root, file))
		require.NotEmpty(t, listed, "%s から mk-go 固有キーの一覧を読めなかった", file)
		for _, key := range mkGoKeys {
			require.Contains(t, listed, key,
				"%s の一覧に %q が無い。管理画面からこの policy を設定できなくなる", file, key)
		}
	}
}

// parseUpstreamRolePolicies reads misskey-js の `export const rolePolicies`.
func parseUpstreamRolePolicies(t *testing.T, path string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	block := regexp.MustCompile(`(?s)export const rolePolicies = \[(.*?)\]`).FindSubmatch(src)
	require.NotNil(t, block, "rolePolicies の宣言が見つからない (書式が変わった?)")
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([A-Za-z0-9_]+)'`).FindAllSubmatch(block[1], -1) {
		out[string(m[1])] = true
	}
	return out
}

// parseMkGoPolicyKeyList reads a `[...Misskey.rolePolicies, 'a', 'b']` literal.
func parseMkGoPolicyKeyList(t *testing.T, path string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	block := regexp.MustCompile(`\[\s*\.\.\.Misskey\.rolePolicies\s*,([^\]]*)\]`).FindSubmatch(src)
	require.NotNil(t, block, "`[...Misskey.rolePolicies, ...]` の一覧が見つからない (書式が変わった?)")
	var out []string
	for _, m := range regexp.MustCompile(`'([A-Za-z0-9_]+)'`).FindAllSubmatch(block[1], -1) {
		out = append(out, string(m[1]))
	}
	return out
}
