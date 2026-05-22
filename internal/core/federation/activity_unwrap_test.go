package federation

import (
	"encoding/json"
	"testing"
)

func TestTryUnwrapSingletonArray(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		wantOK bool
		wantTy string // unwrap 結果の type field (object のみ)。"" は object じゃない or unwrap 無し
	}{
		{
			name:   "object (typical AP activity)",
			body:   `{"type":"Like","actor":"a","object":"o"}`,
			wantOK: false,
		},
		{
			name:   "singleton array (Foundkey-style)",
			body:   `[{"type":"Delete","actor":"a","object":{"id":"x"}}]`,
			wantOK: true,
			wantTy: "Delete",
		},
		{
			name:   "singleton array with leading whitespace",
			body:   "  \n\t[{\"type\":\"Update\",\"actor\":\"a\"}]",
			wantOK: true,
			wantTy: "Update",
		},
		{
			name:   "empty array",
			body:   `[]`,
			wantOK: false,
		},
		{
			name:   "two-element array (batch, unsupported)",
			body:   `[{"type":"A"},{"type":"B"}]`,
			wantOK: false,
		},
		{
			name:   "null",
			body:   `null`,
			wantOK: false,
		},
		{
			name:   "malformed JSON",
			body:   `[{"type":}]`,
			wantOK: false,
		},
		{
			name:   "empty bytes",
			body:   ``,
			wantOK: false,
		},
		{
			name:   "string (not array, not object)",
			body:   `"hi"`,
			wantOK: false,
		},
		// nested singleton array (= 入れ子) は剥がさない。inbox direct POST
		// で来る現実的なケースは 1 段だけと割り切る。
		{
			name:   "nested singleton array (only one level unwrapped)",
			body:   `[[{"type":"X"}]]`,
			wantOK: true,
			// unwrap 結果は `[{"type":"X"}]` (= array of 1) で、object としては parse できない。
			wantTy: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unwrapped, ok := tryUnwrapSingletonArray([]byte(tt.body))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (body=%q, unwrapped=%q)", ok, tt.wantOK, tt.body, unwrapped)
			}
			if !ok {
				if unwrapped != nil {
					t.Errorf("expected nil unwrapped on !ok, got %q", unwrapped)
				}
				return
			}
			if tt.wantTy != "" {
				var obj struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal(unwrapped, &obj); err != nil {
					t.Fatalf("unwrapped body should be valid JSON object: %v (%q)", err, unwrapped)
				}
				if obj.Type != tt.wantTy {
					t.Errorf("unwrapped type = %q, want %q", obj.Type, tt.wantTy)
				}
			}
		})
	}
}
