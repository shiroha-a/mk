package mfm

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseWithin fails the test when Parse does not return within d.
//
// 修正前のパーサは閉じない <b> を 1 段増やすごとに所要時間が倍になった
// (24 段で 30 秒)。落ちるときは永遠に返らないので、時間で打ち切る。
func parseWithin(t *testing.T, in string, d time.Duration) []*Node {
	t.Helper()
	done := make(chan []*Node, 1)
	go func() { done <- Parse(in) }()
	select {
	case n := <-done:
		return n
	case <-time.After(d):
		t.Fatalf("Parse did not finish within %v for %d bytes", d, len(in))
		return nil
	}
}

func TestParse_PathologicalInputsAreNotExponential(t *testing.T) {
	units := []string{
		"<b>", "<small>", "<center>", "<i>", "<s>",
		"**", "~~", "$[x ", "$[x.a=b ", "[", "[<b>", "?[",
		"<b>**~~$[x [<small><i>",
	}
	for _, u := range units {
		t.Run(u, func(t *testing.T) {
			// 200 段は修正前なら宇宙の寿命でも終わらない。-race 下でも十分な余裕を取る
			parseWithin(t, strings.Repeat(u, 200), 10*time.Second)
			// ローカルの本文上限 (3000 文字) 相当
			parseWithin(t, strings.Repeat(u, 3000/len(u)), 10*time.Second)
		})
	}
}

func TestParse_WorkAndMemoryStayBounded(t *testing.T) {
	for _, u := range []string{"<b>", "$[x ", "[<b>", "<b>**~~$[x [<small><i>"} {
		// 32KB でメモ表の上限 (16MB) に届く。-race 下では 1 桁以上遅くなるので、
		// 時間ではなく仕事量とメモリ量で見る
		in := strings.Repeat(u, (32<<10)/len(u))
		s := newState(in, false)
		mergeText(s.parseNodes(false))
		assert.LessOrEqual(t, s.budget.memUsed, memoByteLimit, "memo storage must stay under the cap for %q", u)
		// 上限に達した後は、その時点で走っている子のループ (深さごとに高々 1 つ) が
		// 残りを 1 文字ずつテキストとして読むだけになる
		assert.LessOrEqual(t, s.budget.used, s.budget.limit+2*(s.nestLimit+2)*len(in), "work must stay linear for %q", u)
	}
}

func TestParse_DeepButBalancedNestingStillParses(t *testing.T) {
	// 上限 (20) の内側の正しい入れ子は従来どおり構文として読む
	in := strings.Repeat("<b>", 10) + "x" + strings.Repeat("</b>", 10)
	nodes := parseWithin(t, in, 5*time.Second)
	require.Len(t, nodes, 1)
	depth := 0
	for n := nodes[0]; n != nil; {
		require.Equal(t, NodeBold, n.Type)
		depth++
		if len(n.Children) != 1 || n.Children[0].Type != NodeBold {
			break
		}
		n = n.Children[0]
	}
	assert.Equal(t, 10, depth)
}

func TestParse_UnclosedPrefixKeepsLaterSyntax(t *testing.T) {
	// 閉じない開きタグが並んでも、その後ろの構文は失われない
	in := strings.Repeat("<b>", 50) + "**bold**"
	nodes := parseWithin(t, in, 5*time.Second)
	require.NotEmpty(t, nodes)
	last := nodes[len(nodes)-1]
	assert.Equal(t, NodeBold, last.Type)
	assert.Equal(t, strings.Repeat("<b>", 50), nodes[0].textValue())
}

func TestParse_TextNodesAreNotShared(t *testing.T) {
	// consumeChar は ASCII の 1 文字ノードを共有する。出力に出るテキストノードは
	// mergeText が作り直すので、呼び出し側が書き換えても他の Parse に波及しない
	a := Parse("a")
	require.Len(t, a, 1)
	a[0].Props["text"] = "changed"
	b := Parse("a")
	require.Len(t, b, 1)
	assert.Equal(t, "a", b[0].textValue())
	assert.NotSame(t, asciiText['a'], b[0])
}

func TestMergeText_DoesNotMutateInput(t *testing.T) {
	x, y := Text("x"), Text("y")
	out := mergeText([]*Node{x, y})
	require.Len(t, out, 1)
	assert.Equal(t, "xy", out[0].textValue())
	assert.Equal(t, "x", x.textValue())
	assert.Equal(t, "y", y.textValue())
}
