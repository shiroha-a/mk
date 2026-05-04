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
// file still contains the zero-UUID placeholder string. Phase A (#673) で
// 汎用 code (INVALID_PARAM / INTERNAL_ERROR / NOT_FOUND) を helper 経由化
// し、Phase B (#688) で endpoint 固有の zero-UUID もすべて upstream UUID に
// 置き換えた。本 lint は placeholder 再導入の regression を CI で塞ぐ。
//
// **DO NOT add entries to pendingExceptions** — 新しい endpoint 固有 code
// が必要なら apierr helper を新設 (例: NoSuchNoteDraft) して upstream Misskey
// TS の UUID を採用すること。
func TestZeroUUIDLint(t *testing.T) {
	const placeholder = "00000000-0000-0000-0000-000000000000"

	// from internal/api/apierr/ → ../.. = internal/api/
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	// Phase B 完了後は空。zero-UUID が無いことを保証する pure regression
	// guard として機能する。万が一新規導入が必要になったら、ここを増やす
	// のではなく helper / UUID を新設する。
	pendingExceptions := map[string]int{}

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
