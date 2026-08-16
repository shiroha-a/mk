package federation

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// AP の `type` は文字列とその配列のどちらでも来る。配列で来たときに読めないと、
// Note が Note と判定されず取り込みが丸ごと空振りする。
func TestSingleAPType(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "string", raw: `"Note"`, want: "Note"},
		{name: "array takes the first entry", raw: `["Note", "Article"]`, want: "Note"},
		{name: "empty array", raw: `[]`, want: ""},
		{name: "absent", raw: ``, want: ""},
		{name: "unexpected shape", raw: `{"foo": 1}`, want: ""},
		{name: "array of non-strings", raw: `[1, 2]`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, singleAPType(json.RawMessage(tt.raw)))
		})
	}
}

// コレクションの要素は URI 文字列か、埋め込まれたオブジェクトのどちらか。
func TestFeaturedItemRef(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantURI  string
		wantType string
	}{
		{
			name:    "bare URI has no type",
			raw:     `"https://remote.example/notes/1"`,
			wantURI: "https://remote.example/notes/1",
		},
		{
			name:     "inlined object",
			raw:      `{"id": "https://remote.example/notes/1", "type": "Note"}`,
			wantURI:  "https://remote.example/notes/1",
			wantType: "Note",
		},
		{
			name:    "object without type",
			raw:     `{"id": "https://remote.example/notes/1"}`,
			wantURI: "https://remote.example/notes/1",
		},
		{name: "number", raw: `42`},
		{name: "malformed", raw: `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uri, apType := featuredItemRef(json.RawMessage(tt.raw))
			assert.Equal(t, tt.wantURI, uri)
			assert.Equal(t, tt.wantType, apType)
		})
	}
}
