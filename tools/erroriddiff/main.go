// Command erroriddiff regenerates the golden per-endpoint error-id snapshot
// used by the entitycompat error-id drift gate (TestErrorIDDrift).
//
// Misskey identifies API errors not only by their `code` but by a per-endpoint
// UUID `id` (meta.errors[*].id). Clients branch on that id, so an id that does
// not match the upstream endpoint breaks drop-in compatibility even when the
// code is right. This tool extracts every endpoint's (code -> id) map from the
// backend endpoint definitions and writes it to
// internal/entitycompat/testdata/golden_error_ids.json. Regenerate it whenever
// the third_party/misskey submodule is bumped to a new upstream version.
//
// Usage:
//
//	go run ./tools/erroriddiff
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// codeIDRe matches a Misskey meta.errors entry. All 448 upstream entries place
// `id` directly after `code`, so the adjacent pattern captures every pair.
var codeIDRe = regexp.MustCompile(`code:\s*'([^']+)'\s*,\s*id:\s*'([^']+)'`)

func main() {
	epDir := flag.String("endpoints", "third_party/misskey/packages/backend/src/server/api/endpoints", "path to Misskey backend endpoints dir")
	out := flag.String("out", "internal/entitycompat/testdata/golden_error_ids.json", "golden snapshot output path")
	flag.Parse()

	golden := map[string]map[string]string{}
	err := filepath.WalkDir(*epDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".ts") {
			return nil
		}
		rel, err := filepath.Rel(*epDir, path)
		if err != nil {
			return err
		}
		ep := strings.TrimSuffix(filepath.ToSlash(rel), ".ts")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range codeIDRe.FindAllStringSubmatch(string(data), -1) {
			if golden[ep] == nil {
				golden[ep] = map[string]string{}
			}
			golden[ep][m[1]] = m[2]
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "erroriddiff: walk: %v\n", err)
		os.Exit(1)
	}
	if len(golden) == 0 {
		fmt.Fprintln(os.Stderr, "erroriddiff: ERROR no endpoints parsed (wrong path or upstream format change?)")
		os.Exit(1)
	}

	buf, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "erroriddiff: marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(buf, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "erroriddiff: write: %v\n", err)
		os.Exit(1)
	}

	ids := 0
	for _, m := range golden {
		ids += len(m)
	}
	fmt.Printf("erroriddiff: wrote %d endpoints / %d error ids -> %s\n", len(golden), ids, *out)
}
