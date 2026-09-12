package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 自動追い読み (`v-appear`) を持つコンポーネントは、レート制限の理由を出すこと (#2955)。
//
// **振る舞いの検証は vitest 側が持つ** (`test/unit/paginator-rate-limit.test.ts`)。
// 当初はこのゲートで `paginator.ts` の字面を見ていたが、敵対的レビューで
// **11 変異中 7 件を素通り**することを実測された — 「`canFetch*` を落とさず印
// だけ立てる」「向きを取り違える」という、修正の本体そのものを壊す変異が通って
// いた。**文字列照合では振る舞いを検証できない。**
//
// ここが見るのは vitest では届かない範囲、すなわち**新しい自動追い読みの場所が
// 増えたときに理由の表示を忘れる**ことだけ。実際 `i/notifications`
// (30s/30 と、このプロジェクトで最も厳しい閲覧系の制限) は `MkPagination` を
// 経由せず独自のボタンを持っており、**そこだけ理由が出ていなかった**。
//
// **既知の取りこぼし**: `MkDrive` は Paginator を使わず独自の `fetchMoreFiles`
// で回すので対象外 (`drive/files` にレート制限も無い)。allowlist に理由付きで
// 持つ。
var noPaginatorAutoLoad = map[string]string{
	// Paginator ではなく独自の fetchMoreFiles / canFetchFiles で回す。
	// drive/files にレート制限は無い。
	"MkDrive.vue": "Paginator を使わない",
}

func TestAutoLoadingComponentsShowRateLimit(t *testing.T) {
	fe := filepath.Join(repoRootDir(t), "third_party", "misskey", "packages", "frontend", "src")
	components := filepath.Join(fe, "components")
	if _, err := os.Stat(components); err != nil {
		if os.Getenv("MK_FRONTEND_GATES_REQUIRE_SUBMODULE") != "" {
			require.NoError(t, err, "submodule を要求する job なのに %s を読めない", components)
		}
		t.Skipf("submodule が無い (checkout する job でのみ検査する)")
	}

	entries, err := os.ReadDir(components)
	require.NoError(t, err)

	var autoLoading, missing []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".vue" {
			continue
		}
		src := stripComments(readFileString(t, filepath.Join(components, e.Name())))
		if !strings.Contains(src, "v-appear") {
			continue
		}
		autoLoading = append(autoLoading, e.Name())
		if _, skip := noPaginatorAutoLoad[e.Name()]; skip {
			continue
		}
		// **template での使用を見る。** 識別子だけを探すと **import 行が
		// 残っているだけで緑になる** (実測で素通りした)。
		if !strings.Contains(src, "<MkRateLimitedNotice") {
			missing = append(missing, e.Name())
		}
	}

	// 拾えなかったら落とす。書式が変わって空振りすると、検査していないのに
	// 緑になる (#2874 / #2828 と同じ判断)。
	require.NotEmptyf(t, autoLoading, "`v-appear` を持つコンポーネントを 1 つも拾えない (書式が変わった?)")
	require.Emptyf(t, missing,
		"自動追い読みを持つのにレート制限の理由を出していない: %v\n"+
			"429 で停止したとき、ボタンが黙って消えるだけになり「これで全部」に見える (#2955)", missing)

	// **表示そのものが中身を持っていること。** 上は「置いてあるか」しか見て
	// いないので、`MkRateLimitedNotice` の中を空にすると全部素通りする
	// (実測で `v-if="false"` と再試行の削除が通った)。
	notice := stripComments(readFileString(t,
		filepath.Join(components, "MkRateLimitedNotice.vue")))
	for _, want := range []struct{ frag, why string }{
		{`v-if="paginator.rateLimited.value"`, "レート制限のときだけ出す条件が無い"},
		{"i18n.ts.rateLimitExceeded", "理由の文言が出ない"},
		{`@click="retry"`, "再試行の導線が無い"},
		{"props.paginator.retryAfterRateLimit()", "再試行が Paginator を呼んでいない"},
		{":disabled=\"cooling\"", "再試行に冷却が無い。押すほど窓が延びる"},
	} {
		require.Containsf(t, notice, want.frag, "MkRateLimitedNotice: %s", want.why)
	}
}
