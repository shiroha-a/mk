package keyword

import "testing"

func TestIsKeyWordIncluded(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		keywords []string
		want     bool
	}{
		{"empty keywords", "hello world", nil, false},
		{"empty text", "", []string{"hello"}, false},
		{"simple substring hit", "hello world", []string{"hello"}, true},
		{"simple substring miss", "hello world", []string{"goodbye"}, false},
		{"AND match within one filter (hit)", "hello world", []string{"hello world"}, true},
		{"AND match within one filter (partial = miss)", "hello earth", []string{"hello world"}, false},
		{"AND words can be in any order", "world hello", []string{"hello world"}, true},
		{"multiple filters, OR semantics", "spam content", []string{"good", "spam"}, true},
		{"regex hit (case-insensitive)", "Hello WORLD", []string{`/hello.*world/i`}, true},
		{"regex miss without flag", "Hello world", []string{`/hello/`}, false},
		{"regex hit without flags", "hello world", []string{`/hello/`}, true},
		{"regex multi-line flag", "line1\nline2", []string{`/^line2/m`}, true},
		{"regex dotall flag", "a\nb", []string{`/a.b/s`}, true},
		{"regex JS-only flag g is ignored (no crash)", "abcabc", []string{`/abc/g`}, true},
		{"malformed regex returns false, not panic", "any text", []string{`/[unclosed/`}, false},
		{"mix: regex miss + AND hit", "lorem ipsum", []string{`/foo/`, "lorem ipsum"}, true},
		{"empty filter is skipped", "any text", []string{"", "lorem"}, false},
		{"empty filter does NOT vacuously match", "any text", []string{""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsKeyWordIncluded(c.text, c.keywords)
			if got != c.want {
				t.Errorf("IsKeyWordIncluded(%q, %v) = %v, want %v",
					c.text, c.keywords, got, c.want)
			}
		})
	}
}
