package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanRemoteFilesDisabledRe は「キャッシュをクリア」ボタンの disabled 指定。
//
// **`disabled: true` に戻されていないことを見る。** #3102 で直したのはまさに
// その状態で、引き継いだ運用者は消せるはずのものを UI から消せなかった。
var cleanRemoteFilesDisabledRe = regexp.MustCompile(`disabled:\s*([^,\n]+)`)

// TestCleanRemoteFilesButtonIsConditional asserts the admin "clear cached
// files" button stays driven by the actual target count (#3102).
//
// **壊れても誰も気付かない。** `disabled: true` に戻しても vue-tsc / eslint /
// vitest は全部緑になる (判定関数のテストは utility しか見ない)。症状は
// 「ボタンが押せない」だけで、エラーもログも出ない。#2892 / #2934 と同じ型。
//
// **submodule を checkout する job でしか動かせない** ので、無いときは skip する。
// ただし skip は成功として扱われるため、submodule がある前提の job では
// `MK_FRONTEND_GATES_REQUIRE_SUBMODULE` を渡して skip を禁じる
// (`make frontend-check` がそれを渡す)。
func TestCleanRemoteFilesButtonIsConditional(t *testing.T) {
	rel := "third_party/misskey/packages/frontend/src/pages/admin/files.vue"
	body, err := os.ReadFile(filepath.Join(repoRootDir(t), rel))
	if err != nil {
		if os.Getenv("MK_FRONTEND_GATES_REQUIRE_SUBMODULE") != "" {
			require.NoError(t, err, "submodule を要求する job なのに %s を読めない", rel)
		}
		t.Skipf("%s が無い (submodule を checkout する job でのみ検査する)", rel)
	}
	src := string(body)

	// **判定の入口が配線されていること。** どれか 1 つでも欠けると、ボタンの
	// 状態が実際の対象件数から切り離される。
	//
	// **識別子の存在ではなく「呼んでいる形」を要求する。** 存在だけを見ると
	// import 行やコメントに同じ語が残っているだけで通り、**判定関数を定数に
	// 差し替える変異が素通りする** (初版で実測した)。
	for _, want := range []string{
		// 判定材料を実際に引いている (コメント中の言及では通らない)
		"misskeyApi('admin/drive/usage'",
		// 判定は共有の純粋関数 (test/unit/remote-cache-cleanup.test.ts が固定する)
		"cleanRemoteFilesState(remoteUsage",
		// 件数をダイアログに出している
		"cachedRemoteFileCount(remoteUsage",
		// 不可逆であることを出している
		"_mkgoCleanRemoteFiles.irreversible",
	} {
		assert.Containsf(t, src, want,
			"%s に %q が無い。ボタンの状態が実際の対象件数から切り離される (#3102)", rel, want)
	}

	// **`disabled` が定数でないこと。** ここが `true` に戻ると #3102 以前に戻る
	// (引き継いだ DB の実体つきリモートファイルを UI から消せない)。`false` も
	// 同じく駄目 — 対象 0 件のインスタンスで誤って押させる。
	found := cleanRemoteFilesDisabledRe.FindAllStringSubmatch(src, -1)
	require.NotEmptyf(t, found,
		"%s から disabled の指定を 1 つも拾えていない。書式が変わったなら、この gate も直すこと", rel)
	sawConditional := false
	for _, m := range found {
		v := strings.TrimSpace(m[1])
		assert.NotEqualf(t, "true", v,
			"%s の disabled が定数 true。#3102 以前に戻っている", rel)
		if strings.Contains(v, "cleanState") {
			sawConditional = true
		}
	}
	assert.Truef(t, sawConditional,
		"%s の disabled が cleanState を見ていない。対象件数と切り離されている (#3102)", rel)
}
