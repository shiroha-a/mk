// Command permspec regenerates the golden per-endpoint permission-level
// snapshot used by the entitycompat permission drift gate (TestPermissionDrift).
//
// Misskey declares each endpoint's access requirement in its meta:
// requireAdmin / requireModerator / requireCredential. This tool reduces those
// to a single level (public < auth < moderator < admin) per endpoint and writes
// it to internal/entitycompat/testdata/golden_permissions.json. The gate then
// checks that mk-go's router middleware is never LOOSER than this level
// (a privilege-escalation / missing-auth guard). Regenerate whenever the
// third_party/misskey submodule is bumped.
//
// Usage:
//
//	go run ./tools/permspec
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

var (
	requireAdminRe     = regexp.MustCompile(`requireAdmin:\s*true`)
	requireModeratorRe = regexp.MustCompile(`requireModerator:\s*true`)
	requireCredRe      = regexp.MustCompile(`requireCredential:\s*true`)
)

// level reduces an endpoint's meta to its access level. The hierarchy is
// admin > moderator > auth > public; admin/moderator imply credential.
func level(s string) string {
	switch {
	case requireAdminRe.MatchString(s):
		return "admin"
	case requireModeratorRe.MatchString(s):
		return "moderator"
	case requireCredRe.MatchString(s):
		return "auth"
	default:
		return "public"
	}
}

func main() {
	epDir := flag.String("endpoints", "third_party/misskey/packages/backend/src/server/api/endpoints", "path to Misskey backend endpoints dir")
	out := flag.String("out", "internal/entitycompat/testdata/golden_permissions.json", "golden snapshot output path")
	flag.Parse()

	perms := map[string]string{}
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
		perms[ep] = level(string(data))
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "permspec: walk: %v\n", err)
		os.Exit(1)
	}
	if len(perms) == 0 {
		fmt.Fprintln(os.Stderr, "permspec: ERROR no endpoints parsed (wrong path or upstream format change?)")
		os.Exit(1)
	}

	buf, err := json.MarshalIndent(perms, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "permspec: marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(buf, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "permspec: write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("permspec: wrote %d endpoints -> %s\n", len(perms), *out)
}
