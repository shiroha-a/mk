// Command apicompat compares mk-go's HTTP route surface against
// Misskey TS endpoint set and emits a markdown compatibility matrix.
//
// Usage:
//
//	apicompat \
//	  -ts-endpoints-dir third_party/misskey/packages/backend/src/server/api/endpoints \
//	  -mk-routes /tmp/mk-routes.json \
//	  -out docs/api-compat.md
//
// Misskey TS の endpoint は file path = API path 規約 (`admin/foo.ts` →
// `/api/admin/foo`) で、method は全て POST と仮定する (Misskey の API convention)。
// mk-go 側は cmd/misskey -dump-routes が出す JSON を入力に取り、`/api/*`
// prefix のものだけを対象に POST のみ抽出して比較する。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	tsDir := flag.String("ts-endpoints-dir", "third_party/misskey/packages/backend/src/server/api/endpoints", "path to Misskey TS endpoint .ts files")
	mkRoutesPath := flag.String("mk-routes", "", "path to JSON output of `misskey -dump-routes` (default: stdin)")
	outPath := flag.String("out", "", "output markdown path (default: stdout)")
	flag.Parse()

	tsEndpoints, err := collectTSEndpoints(*tsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apicompat: collect TS endpoints: %v\n", err)
		os.Exit(1)
	}

	mkRoutes, err := readMkRoutes(*mkRoutesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apicompat: read mk-go routes: %v\n", err)
		os.Exit(1)
	}

	report := compare(tsEndpoints, mkRoutes)
	md := renderMarkdown(report, mkRoutes.MisskeyVersion, mkRoutes.MkGoVersion)

	out := os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "apicompat: open out: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}
	if _, err := io.WriteString(out, md); err != nil {
		fmt.Fprintf(os.Stderr, "apicompat: write out: %v\n", err)
		os.Exit(1)
	}
}

func readMkRoutes(path string) (*DumpedRoutes, error) {
	var r io.Reader = os.Stdin
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	var dr DumpedRoutes
	if err := json.NewDecoder(r).Decode(&dr); err != nil {
		return nil, fmt.Errorf("decode mk-routes JSON: %w", err)
	}
	return &dr, nil
}

// DumpedRoute mirrors internal/server.DumpedRoute. Duplicated here so the
// tool stays a single-binary CLI without importing the server package
// (which would pull in DB / queue / echo dependencies).
type DumpedRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// DumpedRoutes mirrors internal/server.DumpedRoutes.
type DumpedRoutes struct {
	MisskeyVersion string        `json:"misskeyVersion"`
	MkGoVersion    string        `json:"mkGoVersion"`
	Routes         []DumpedRoute `json:"routes"`
}

// stripQueryDocs strips trailing path params (`/:id`) and Echo wildcards (`*`)
// to keep matching stable across mk-go / TS path styles. mk-go では Echo の
// `:param` 形式、TS では openapi で `{param}` 形式になることがあるが、
// Phase 1 では path string そのまま compare するため、param を持つ endpoint は
// param 名差分で別物扱いになる。これは Phase 2 で正規化したい。
func stripQueryDocs(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return p[:i]
	}
	return p
}
