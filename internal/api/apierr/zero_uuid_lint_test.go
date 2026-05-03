package apierr_test

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestZeroUUIDLint walks internal/api/ and fails if any non-test source
// file still contains the zero-UUID placeholder string. Phase A of #673
// established that all generic codes (INVALID_PARAM / INTERNAL_ERROR /
// NOT_FOUND) flow through apierr helpers with stable UUIDs; this lint
// keeps regressions from sneaking back in.
//
// Endpoint-specific codes still pending #673 Phase B (NO_SUCH_KEY /
// REGISTRATION_FAILED / NO_SUCH_DRAFT / NO_SECURITY_KEYS / INVALID_TOKEN)
// are listed in pendingZeroUUIDOccurrences below as exceptions that this
// test tolerates until each gets its proper upstream UUID. **DO NOT add
// new entries** — instead pick the right Misskey TS UUID and use the
// apierr helper.
func TestZeroUUIDLint(t *testing.T) {
	const placeholder = "00000000-0000-0000-0000-000000000000"

	// from internal/api/apierr/ → ../.. = internal/api/
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	pendingExceptions := map[string]int{
		// TwoFADone INVALID_TOKEN / TwoFARegisterKey INVALID_TOKEN /
		// TwoFAKeyDone INVALID_TOKEN / TwoFAKeyDone REGISTRATION_FAILED /
		// TwoFARemoveKey NO_SUCH_KEY / TwoFAUpdateKey NO_SUCH_KEY /
		// TwoFAPasswordLess NO_SECURITY_KEYS。upstream の register-key /
		// key-done は 2FA 失敗時に `new Error('authentication failed')` を
		// 投げる (ApiError でないので UUID 無し) ので、こちら側でも
		// upstream UUID と紐づける素材が無い (#698)。
		"api/i/handler_2fa.go":        7,
		"api/notes/handler_drafts.go": 1, // NO_SUCH_DRAFT
	}

	violations := map[string]int{}
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		count := strings.Count(string(body), placeholder)
		if count == 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		violations[rel] = count
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify each violation is whitelisted; reject unexpected new entries
	// AND old whitelisted entries that have shrunk below their declared
	// count (means callers were partially fixed but the lint exception
	// can be tightened).
	var unexpected []string
	for path, n := range violations {
		want, ok := pendingExceptions[path]
		if !ok {
			unexpected = append(unexpected, path+" ("+strconv.Itoa(n)+" occurrences)")
			continue
		}
		if n != want {
			unexpected = append(unexpected, path+" ("+strconv.Itoa(n)+" occurrences, exception expected "+strconv.Itoa(want)+")")
		}
	}
	// pendingExceptions is iterated to detect stale exception entries.
	// Sort the keys so the failure messages are deterministic across runs
	// (Go map iteration order is randomized, which makes CI logs noisy).
	stalePending := make([]string, 0, len(pendingExceptions))
	for path := range pendingExceptions {
		if _, ok := violations[path]; !ok {
			stalePending = append(stalePending, path)
		}
	}
	sort.Strings(stalePending)
	for _, path := range stalePending {
		t.Errorf("pending exception %q has no zero-UUID matches anymore — remove the entry from pendingExceptions", path)
	}

	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		t.Errorf("zero-UUID placeholder regressions found in %d file(s):\n  - %s\n\nUse apierr.{InvalidParam,InternalError,NotFound,NoSuchUser,NoSuchNote,...} helpers (or add a new typed helper with the upstream Misskey UUID) instead of \"00000000-0000-0000-0000-000000000000\".",
			len(unexpected), strings.Join(unexpected, "\n  - "))
	}
}
