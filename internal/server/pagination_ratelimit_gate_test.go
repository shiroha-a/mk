package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 429 を受けたら追い読みを止める配線が生きていることを見る (#2955)。
//
// **判断は純関数 (`resolveRateLimitStop`) に閉じてあり、単体テストで固定して
// いる。** ただし**それがどこにも呼ばれていなければ意味が無い** — vue-tsc は
// 未使用の export を落とさないので、配線だけ消しても型検査は通る。症状は
// 「レート制限に当たると読み込みが終わらない」で、エラーもログも出ない
// (#2915 の `admin:user` スロットと同じ「宣言したのに配線していない」型)。
//
// **サーバー側の制限は叩くのをやめるまで解けない。** store は拒否した
// リクエストも記録するので、429 のまま再試行を続けると窓が前へ押し戻され
// 続ける。だから「止める」ことが実際に効いている必要がある。
func TestPaginationStopsRetryingOnRateLimit(t *testing.T) {
	fe := filepath.Join(repoRootDir(t), "third_party", "misskey", "packages", "frontend", "src")
	paginator := filepath.Join(fe, "utility", "paginator.ts")
	if _, err := os.Stat(paginator); err != nil {
		if os.Getenv("MK_FRONTEND_GATES_REQUIRE_SUBMODULE") != "" {
			require.NoError(t, err, "submodule を要求する job なのに %s を読めない", paginator)
		}
		t.Skipf("submodule が無い (checkout する job でのみ検査する)")
	}

	src := stripComments(readFileString(t, paginator))
	// **呼び出しの形で見る。** 識別子だけを探すと **import 行が残っている
	// だけで緑になる** — 本体をコメントアウトして定数に差し替える変異が
	// 素通りすることを実測した。
	require.Containsf(t, src, "resolveRateLimitStop(err, direction)",
		"paginator が判断を呼んでいない。429 を握り潰して自動追い読みが自走する")

	// **両方向で呼ぶこと。** 片方だけだと、そちらの向きだけ止まらない。
	for _, dir := range []string{"'older'", "'newer'"} {
		require.Containsf(t, src, "this.noteRateLimit(err, "+dir+")",
			"%s 方向の取得が noteRateLimit を通っていない", dir)
	}

	// **`canFetch*` を実際に落とすこと。** 印を立てるだけだと `v-appear` が
	// 再発火し続ける。
	//
	// **判断の結果を使っている形で見る。** `this.canFetchOlder.value = false`
	// という字面は**このファイルに他に 2 箇所ある** (終端に達したときの正常な
	// 処理) ので、素の部分一致だと本体を消しても素通りする (実測)。
	for _, want := range []string{
		"decision.stop === 'older'",
		"decision.stop === 'newer'",
	} {
		require.Containsf(t, src, want,
			"判断の結果 (%s) を使っていない。印を立てるだけでは自動発火が止まらない", want)
	}

	// **UI 側で理由と手動の再試行を出すこと。** 落とすだけだと一覧が黙って
	// 途中で終わったように見え、利用者は「これで全部」と誤解する。
	pagination := stripComments(readFileString(t, filepath.Join(fe, "components", "MkPagination.vue")))
	require.Containsf(t, pagination, "rateLimited",
		"MkPagination がレート制限の状態を見ていない")
	require.Containsf(t, pagination, "rateLimitExceeded",
		"レート制限の理由が利用者に出ない")
}
