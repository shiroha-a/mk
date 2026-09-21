package ld

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// identicalBlankNodes builds n structurally identical blank nodes (no @id,
// same keys, same values) under one property.
func identicalBlankNodes(n int) map[string]any {
	nodes := make([]any, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, map[string]any{"http://e/v": "same"})
	}
	return map[string]any{
		"@context":   "https://www.w3.org/ns/activitystreams",
		"type":       "Create",
		"id":         "https://attacker.example/activities/1",
		"http://e/l": nodes,
	}
}

func TestCheckComplexity_RejectsIdenticalBlankNodes(t *testing.T) {
	t.Parallel()

	// **境界はリテラルで書く。** 定数を参照すると、定数を緩める変異と一緒に
	// 期待値まで動いてしまい、変異検証が素通りする (実際に一度そうなった)。
	require.NoError(t, CheckComplexity(identicalBlankNodes(8)),
		"at the limit (8) the document must still be accepted")

	err := CheckComplexity(identicalBlankNodes(9))
	require.Error(t, err, "one past the limit must be rejected")
	require.True(t, errors.Is(err, ErrTooComplex))
	require.Contains(t, err.Error(), "identical blank nodes")
}

func TestCheckComplexity_RejectsTooManyBlankNodes(t *testing.T) {
	t.Parallel()

	// 値をすべて変えて同型判定は避けつつ、総数だけを上限より 1 つ多くする。
	nodes := make([]any, 0, 513)
	for i := 0; i < 513; i++ {
		nodes = append(nodes, map[string]any{"http://e/v": fmt.Sprintf("v%d", i)})
	}
	err := CheckComplexity(map[string]any{"http://e/l": nodes})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrTooComplex))
	require.Contains(t, err.Error(), "blank nodes (limit")
}

func TestCheckComplexity_AcceptsRealisticActivity(t *testing.T) {
	t.Parallel()

	// upstream の受け入れ上限どおりの添付 16 件 + 絵文字タグ 128 件。
	// いずれも値が異なるので非一意にならない。
	att := make([]any, 0, 16)
	for i := 0; i < 16; i++ {
		att = append(att, map[string]any{
			"type":      "Document",
			"mediaType": "image/png",
			"url":       fmt.Sprintf("https://remote.example/files/%d.png", i),
			"name":      fmt.Sprintf("file-%d", i),
		})
	}
	tags := make([]any, 0, 128)
	for i := 0; i < 128; i++ {
		tags = append(tags, map[string]any{
			"type": "Emoji",
			"id":   fmt.Sprintf("https://remote.example/emojis/%d", i),
			"name": fmt.Sprintf(":e%d:", i),
			"icon": map[string]any{
				"type": "Image",
				"url":  fmt.Sprintf("https://remote.example/e/%d.png", i),
			},
		})
	}
	doc := map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Create",
		"id":       "https://remote.example/activities/1",
		"actor":    "https://remote.example/users/a",
		"object": map[string]any{
			"type":       "Note",
			"id":         "https://remote.example/notes/1",
			"content":    "hello",
			"attachment": att,
			"tag":        tags,
		},
	}
	require.NoError(t, CheckComplexity(doc))
}

// 同じ形でも `@id` があれば blank node にならないので数えない。
func TestCheckComplexity_IdentifiedNodesAreNotBlank(t *testing.T) {
	t.Parallel()

	nodes := make([]any, 0, 1024)
	for i := 0; i < 1024; i++ {
		nodes = append(nodes, map[string]any{
			"@id":        "https://remote.example/o/x",
			"http://e/v": "same",
		})
	}
	require.NoError(t, CheckComplexity(map[string]any{"http://e/l": nodes}),
		"nodes carrying an explicit identifier must not be counted as blank nodes")

	// `id` (compact 形) も同じ扱い。
	compact := make([]any, 0, 16)
	for i := 0; i < 16; i++ {
		compact = append(compact, map[string]any{
			"id":         "https://remote.example/o/y",
			"http://e/v": "same",
		})
	}
	require.NoError(t, CheckComplexity(map[string]any{"http://e/l": compact}))
}

// **深すぎる入れ子は拒否する (fail-closed)。**
//
// 打ち切って「見なかったことにする」と、payload を 33 段包むだけで他の上限が
// まとめて無効になる。実際そうなっていた — 同型 blank node 1000 個を 40 段
// 包むと素通りした。
func TestCheckComplexity_RejectsDeepNesting(t *testing.T) {
	t.Parallel()

	var node any = map[string]any{"http://e/v": "leaf"}
	for i := 0; i < 128; i++ {
		node = map[string]any{"http://e/p": node}
	}
	require.ErrorIs(t, CheckComplexity(node), ErrTooComplex)

	// **包むだけで他の上限を外せないこと。** 中身だけなら総数上限で落ちる形を
	// 40 段包んでも、やはり落ちること。
	inner := make([]any, 0, 1000)
	for i := 0; i < 1000; i++ {
		inner = append(inner, map[string]any{"http://e/v": "same"})
	}
	var wrapped any = inner
	for i := 0; i < 40; i++ {
		wrapped = map[string]any{"http://e/p": wrapped}
	}
	require.ErrorIs(t, CheckComplexity(wrapped), ErrTooComplex,
		"深さで打ち切ると総数の上限が消える")
}

// --- 相互参照する blank node (指数爆発の本体) ---
//
// URDNA2015 の `hashNDegreeQuads` が全順列を列挙するのは**相互に参照し合う**
// 非一意な blank node に対してで、孤立した同型ノードをいくら並べても爆発
// しない。JSON はツリーなので、blank node 同士の相互参照は**明示ラベル
// (`_:b0`) でしか作れない**。

// cliqueDoc builds n mutually-referencing blank nodes.
func cliqueDoc(n int, bare bool) map[string]any {
	nodes := make([]any, 0, n)
	for i := 0; i < n; i++ {
		refs := make([]any, 0, n)
		for j := 0; j < n; j++ {
			label := "_:b" + strconv.Itoa(j)
			if bare {
				// `@type: "@id"` を宣言した述語の値として直接書く形。
				refs = append(refs, label)
			} else {
				refs = append(refs, map[string]any{"@id": label})
			}
		}
		nodes = append(nodes, map[string]any{"@id": "_:b" + strconv.Itoa(i), "p": refs})
	}
	return map[string]any{
		"@context": map[string]any{"p": map[string]any{"@id": "http://example.com/p", "@type": "@id"}},
		"@graph":   nodes,
	}
}

// **`@id` の値が `_:` で始まるものは IRI ではない。**
//
// identity とみなすと clique の各ノードがまるごとカウントから外れ、**実際の
// 攻撃形が素通りする**。実測では 956 バイトの document (n=7) の正規化に
// 708ms、n=9 で 1m52s かかった。
func TestCheckComplexity_RejectsBlankNodeClique(t *testing.T) {
	t.Parallel()

	for _, bare := range []bool{false, true} {
		name := "id-object"
		if bare {
			name = "bare-label"
		}
		t.Run(name, func(t *testing.T) {
			// 5 は上限 (4) のすぐ上。上限そのものを参照しない。
			require.ErrorIs(t, CheckComplexity(cliqueDoc(5, bare)), ErrTooComplex)
			require.ErrorIs(t, CheckComplexity(cliqueDoc(7, bare)), ErrTooComplex)
		})
	}
}

// **ラベルは形のダイジェストから外す。**
//
// URDNA2015 は入力ラベルを捨てて正規化するので `_:b0` と `_:b1` は同型。
// ダイジェストに残すと各ノードが別グループに割れて同型カウントが立たない。
func TestCheckComplexity_BlankLabelsShareShape(t *testing.T) {
	t.Parallel()

	nodes := make([]any, 0, 3)
	for i := 0; i < 3; i++ {
		// ラベルは distinct だが形は同じ。ラベル数の上限には掛からない数に
		// 絞って、**グループ化が揃っているか**だけを見る。
		nodes = append(nodes, map[string]any{"@id": "_:n" + strconv.Itoa(i), "http://e/v": "same"})
	}
	c := &complexityCounter{groups: make(map[string]int), labels: make(map[string]struct{})}
	_, err := c.walk(nodes, 0)
	require.NoError(t, err)
	require.Len(t, c.groups, 1,
		"ラベルが違うだけの同型ノードは 1 つの形として数えること (割れると同型カウントが立たない)")
	for _, n := range c.groups {
		require.Equal(t, 3, n)
	}
}

// 本文にたまたま `_:` の形が現れても通ること (偽陽性の下限)。
func TestCheckComplexity_AllowsFewIncidentalLabels(t *testing.T) {
	t.Parallel()

	require.NoError(t, CheckComplexity(map[string]any{
		"id":      "https://remote.example/notes/1",
		"type":    "Note",
		"content": "<p>_:a と _:b の話</p>",
		"summary": "_:c",
	}))
}

// Normalize が正規化の前に判定を通すこと (これが本体の choke point)。
func TestNormalizeRejectsComplexDocumentBeforeCanonicalizing(t *testing.T) {
	t.Parallel()

	// **同型ノードではなく「総数超過」を使う。** 同型を渡すと、判定を外す変異の
	// ときに正規化が指数的に走ってメモリを食い尽くす。値が全て異なるノードは
	// 一意なので順列の列挙が起きず、変異時も有限時間で終わる (= assert で落ちる)。
	nodes := make([]any, 0, 513)
	for i := 0; i < 513; i++ {
		nodes = append(nodes, map[string]any{"http://e/v": fmt.Sprintf("v%d", i)})
	}
	doc := map[string]any{
		"@context":   "https://www.w3.org/ns/activitystreams",
		"type":       "Create",
		"id":         "https://attacker.example/activities/1",
		"http://e/l": nodes,
	}

	p := NewProcessor()
	_, err := p.Normalize(doc)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrTooComplex),
		"Normalize must reject before handing the document to the canonicalizer")
}

// 正当な document は Normalize を通って n-quads になること。
func TestNormalizeAcceptsOrdinaryDocument(t *testing.T) {
	t.Parallel()

	p := NewProcessor()
	out, err := p.Normalize(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Create",
		"id":       "https://remote.example/activities/1",
		"actor":    "https://remote.example/users/a",
	})
	require.NoError(t, err)
	require.True(t, strings.Contains(out, "https://remote.example/activities/1"))
}
