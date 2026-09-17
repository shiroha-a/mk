package entitycompat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// **`peerJob.Envelope` の json tag を固定する (#3037)。**
//
// `admin/queue/jobs` はプラグイン peer の送信本文を伏せるが、判定は
// `jobSecretKeys` の文字列 `"envelope"` との一致でしかない。tag を変えると
// **全テスト緑のまま伏せ字が外れる**。`peerJob` は `internal/server` の
// unexported な型なので `internal/api/admin` からは reflect で引けず、
// あちらの突き合わせに載せられない。ここで形として固定する。
func TestPeerJobEnvelopeTagIsStable(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join("..", "server", "plugin_peer.go"), nil, 0)
	require.NoError(t, err)

	var tag string
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "peerJob" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, fld := range st.Fields.List {
			for _, name := range fld.Names {
				if name.Name != "Envelope" {
					continue
				}
				found = true
				if fld.Tag != nil {
					tag = fld.Tag.Value
				}
			}
		}
		return true
	})

	require.True(t, found, "peerJob.Envelope が見つからない (rename した? admin の jobSecretKeys も直すこと)")
	// **オプションはカンマで切ってから比べる (#3037 レビュー 2 周目)。**
	// `json:"envelope,omitempty"` は wire 名が変わらないのに、素の
	// `Contains` だと落ちる。`internal/api/admin` の `queue_redact_test.go`
	// は元からカンマで切っており、両者の規則が食い違っていた。
	assert.Equal(t, "envelope", jsonTagName(tag),
		"peerJob.Envelope の json tag が変わっている。`internal/api/admin` の jobSecretKeys も直すこと "+
			"(放置するとプラグイン peer の送信本文が admin/queue/jobs から読める)")
}

// jsonTagName extracts the wire name from a struct tag.
func jsonTagName(tag string) string {
	m := regexp.MustCompile(`json:"([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return strings.Split(m[1], ",")[0]
}
