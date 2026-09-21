package ld

import (
	"errors"
	"fmt"
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

// 深く入れ子にしても停止すること (病的に深い入力への保険)。
func TestCheckComplexity_DeepNestingTerminates(t *testing.T) {
	t.Parallel()

	var node any = map[string]any{"http://e/v": "leaf"}
	for i := 0; i < 128; i++ {
		node = map[string]any{"http://e/p": node}
	}
	// 走査が打ち切られるだけで panic も無限ループも起きない。
	_ = CheckComplexity(node)
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
