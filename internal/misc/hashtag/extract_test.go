package hashtag

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "empty input",
			in:   []string{""},
			want: nil,
		},
		{
			name: "no hashtags",
			in:   []string{"hello world"},
			want: nil,
		},
		{
			name: "single tag",
			in:   []string{"hello #golang"},
			want: []string{"golang"},
		},
		{
			name: "tag at start",
			in:   []string{"#golang is fast"},
			want: []string{"golang"},
		},
		{
			name: "multiple tags",
			in:   []string{"learn #go and #rust today"},
			want: []string{"go", "rust"},
		},
		{
			name: "duplicate tags collapse",
			in:   []string{"#go again #go and #GO"},
			want: []string{"go"},
		},
		{
			name: "japanese tag",
			in:   []string{"おはよう #朝 みなさん"},
			want: []string{"朝"},
		},
		{
			name: "underscore and hyphen",
			in:   []string{"#hello_world #foo-bar"},
			want: []string{"hello_world", "foo-bar"},
		},
		{
			name: "no space before #",
			in:   []string{"foo#bar"},
			want: nil, // word boundary 必須
		},
		{
			name: "consecutive hashtags pick first only",
			in:   []string{"#one#two"},
			want: []string{"one"}, // upstream Misskey と同じく #two は拾わない
		},
		{
			name: "after quote",
			in:   []string{`"#quoted"`},
			want: []string{"quoted"},
		},
		{
			name: "hashtag-only line",
			in:   []string{"#alpha\n#beta"},
			want: []string{"alpha", "beta"},
		},
		{
			name: "case preserved on first occurrence",
			in:   []string{"#Golang and #golang"},
			want: []string{"Golang"},
		},
		{
			name: "text + cw",
			in:   []string{"hello #foo", "warning #bar"},
			want: []string{"foo", "bar"},
		},
		{
			name: "long tag truncated",
			in:   []string{"#" + strings.Repeat("a", MaxTagLength+50)},
			want: []string{strings.Repeat("a", MaxTagLength)},
		},
		{
			name: "fenced code block excluded",
			in:   []string{"normal #yes\n```\n#secret\n```\nback #also"},
			want: []string{"yes", "also"},
		},
		{
			name: "inline code excluded",
			in:   []string{"check `#var` here #real"},
			want: []string{"real"},
		},
		{
			name: "url fragment not picked up",
			in:   []string{"see https://example.com/path#anchor for #real"},
			want: []string{"real"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Extract(tc.in...)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExtractUserTags(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "normalizes to lowercase",
			in:   []string{"I love #Golang and #GoLang"},
			want: []string{"golang"}, // case-insensitive dedup after normalize
		},
		{
			name: "NFKC folds full-width",
			in:   []string{"#Ｔｏｋｙｏ"},
			want: []string{"tokyo"},
		},
		{
			name: "AP person.tag style #-prefixed names",
			in:   []string{"#art", "#Music"},
			want: []string{"art", "music"},
		},
		{
			name: "no hashtags returns nil",
			in:   []string{"plain text without tags"},
			want: nil,
		},
		{
			name: "empty input returns nil",
			in:   []string{""},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ExtractUserTags(tc.in...))
		})
	}
}

// caps at MaxUserTags (32) entries.
func TestExtractUserTags_Cap(t *testing.T) {
	parts := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		parts = append(parts, "#tag"+string(rune('a'+i%26))+string(rune('0'+i/26)))
	}
	got := ExtractUserTags(strings.Join(parts, " "))
	assert.Len(t, got, MaxUserTags)
}
