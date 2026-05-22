package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCollectTSEndpoints(t *testing.T) {
	tmp := t.TempDir()
	// Build a small fake Misskey endpoints tree:
	//   <tmp>/meta.ts                   (endpoint)
	//   <tmp>/admin/foo.ts              (endpoint)
	//   <tmp>/admin/bar/baz.ts          (endpoint)
	//   <tmp>/endpoint.ts               (endpoint — meta tag in TS)
	//   <tmp>/endpoints.ts              (endpoint — meta tag in TS)
	//   <tmp>/legacy.d.ts               (excluded: type declaration)
	//   <tmp>/admin/README.md           (excluded: non-ts)
	//   <tmp>/admin/foo.test.ts         (excluded: Vitest unit test)
	//   <tmp>/notes/create.test.ts      (excluded: Vitest unit test)
	mustWrite(t, filepath.Join(tmp, "meta.ts"), "")
	mustWrite(t, filepath.Join(tmp, "admin", "foo.ts"), "")
	mustWrite(t, filepath.Join(tmp, "admin", "bar", "baz.ts"), "")
	// `endpoint.ts` / `endpoints.ts` are real Misskey API endpoints (meta tag),
	// not plumbing — must NOT be skipped.
	mustWrite(t, filepath.Join(tmp, "endpoint.ts"), "")
	mustWrite(t, filepath.Join(tmp, "endpoints.ts"), "")
	mustWrite(t, filepath.Join(tmp, "legacy.d.ts"), "")
	mustWrite(t, filepath.Join(tmp, "admin", "README.md"), "")
	// Vitest unit test files are NOT endpoints — must be skipped.
	mustWrite(t, filepath.Join(tmp, "admin", "foo.test.ts"), "")
	mustWrite(t, filepath.Join(tmp, "notes", "create.test.ts"), "")

	got, err := collectTSEndpoints(tmp)
	if err != nil {
		t.Fatalf("collectTSEndpoints: %v", err)
	}
	want := []string{
		"/api/admin/bar/baz",
		"/api/admin/foo",
		"/api/endpoint",
		"/api/endpoints",
		"/api/meta",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collectTSEndpoints mismatch\n got %v\nwant %v", got, want)
	}
}

func TestCollectTSEndpointsMissingDir(t *testing.T) {
	if _, err := collectTSEndpoints(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for missing dir, got nil")
	}
}

func TestCompare(t *testing.T) {
	ts := []string{
		"/api/meta",
		"/api/notes/create",
		"/api/users/show",
	}
	mk := &DumpedRoutes{
		MisskeyVersion: "2026.5.4",
		MkGoVersion:    "0.9.1",
		Routes: []DumpedRoute{
			{Method: "POST", Path: "/api/meta"},
			{Method: "GET", Path: "/api/meta"}, // GET variant on POST = both
			{Method: "POST", Path: "/api/notes/create"},
			// users/show is missing from mk-go
			{Method: "POST", Path: "/api/test/reset-db"}, // mk-go only (other)
			{Method: "GET", Path: "/api/url"},            // mk-go only (other, no POST)
			{Method: "POST", Path: "/api/chat/messages"}, // mk-go only (chat_ext)
			{Method: "POST", Path: "/api/signup"},        // both (TS router-direct)
			{Method: "POST", Path: "/api/signin-flow"},   // both (TS router-direct)
			{Method: "GET", Path: "/healthz"},            // excluded (not /api)
			{Method: "GET", Path: "/.well-known/nodeinfo"},
			// Echo の catch-all noise — フィルタされて matrix に現れないこと。
			{Method: "POST", Path: "/api/*"},
			{Method: "GET", Path: "/api/*"},
			{Method: "echo_route_not_found", Path: "/api/*"},
		},
	}

	got := compare(ts, mk)

	wantBoth := []string{
		"/api/meta",
		"/api/notes/create",
		"/api/signin-flow",
		"/api/signup",
	}
	if !reflect.DeepEqual(got.Both, wantBoth) {
		t.Errorf("Both mismatch\n got %v\nwant %v", got.Both, wantBoth)
	}
	wantTSOnly := []string{
		// router-direct ts paths が mk-go に未登録だった場合に TSOnly に
		// 出ることを担保する。
		"/api/signin-with-passkey",
		"/api/signup-pending",
		"/api/users/show",
	}
	if !reflect.DeepEqual(got.TSOnly, wantTSOnly) {
		t.Errorf("TSOnly mismatch\n got %v\nwant %v", got.TSOnly, wantTSOnly)
	}
	wantMkOnly := []MkOnlyRoute{
		{Method: "POST", Path: "/api/chat/messages", Category: CatChatExt},
		{Method: "GET", Path: "/api/meta", Category: CatGETVariant},
		{Method: "POST", Path: "/api/test/reset-db", Category: CatOther},
		{Method: "GET", Path: "/api/url", Category: CatOther},
	}
	if !reflect.DeepEqual(got.MkOnly, wantMkOnly) {
		t.Errorf("MkOnly mismatch\n got %v\nwant %v", got.MkOnly, wantMkOnly)
	}
}

func TestRenderMarkdownHeader(t *testing.T) {
	r := Report{
		Both:   []string{"/api/meta"},
		TSOnly: []string{"/api/foo"},
		MkOnly: []MkOnlyRoute{
			{Method: "POST", Path: "/api/test/reset-db", Category: CatOther},
			{Method: "GET", Path: "/api/charts/users", Category: CatGETVariant},
			{Method: "POST", Path: "/api/chat/messages", Category: CatChatExt},
		},
	}
	md := renderMarkdown(r, "2026.5.4", "0.9.1")

	for _, want := range []string{
		"# API Compatibility Matrix",
		"Misskey (TS) version: `2026.5.4`",
		"mk-go version: `0.9.1`",
		"TS endpoints (POST `/api/*`): **2**",
		"mk-go implemented (TS の subset): **1**",
		"mk-go coverage of TS: **50.0%**",
		"/api/foo",
		"/api/meta",
		// category sections
		"### GET variant 追加 (1)",
		"### cherrypick 系 chat 拡張 (1)",
		"### その他 mk-go 独自 / alias (1)",
		"/api/test/reset-db",
		"/api/charts/users",
		"/api/chat/messages",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("renderMarkdown output missing %q\nfull:\n%s", want, md)
		}
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	md := renderMarkdown(Report{}, "x", "y")
	if !strings.Contains(md, "mk-go coverage of TS: **n/a**") {
		t.Errorf("expected n/a coverage marker, got:\n%s", md)
	}
}

func TestDedupSorted(t *testing.T) {
	tests := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{"a"}, []string{"a"}},
		{[]string{"a", "a"}, []string{"a"}},
		{[]string{"a", "a", "b"}, []string{"a", "b"}},
		{[]string{"a", "b", "b", "c", "c", "c"}, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		got := dedupSorted(tt.in)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("dedupSorted(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestStripQueryDocs(t *testing.T) {
	if got := stripQueryDocs("/api/foo?a=1"); got != "/api/foo" {
		t.Errorf("stripQueryDocs strips query: got %q", got)
	}
	if got := stripQueryDocs("/api/foo"); got != "/api/foo" {
		t.Errorf("stripQueryDocs passthrough: got %q", got)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}
