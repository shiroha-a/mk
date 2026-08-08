package server

import (
	"strings"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
)

func strptr(s string) *string { return &s }

// 埋め込みは認証を伴わない経路なので、ここが緩むとそのまま IDOR になる。
// upstream ClientServerService の `/embed/notes/:note` と同じ判定を固定する。
func TestEmbedNoteIsPublic(t *testing.T) {
	tests := []struct {
		name string
		note *model.Note
		want bool
	}{
		{
			name: "public local note is embeddable",
			note: &model.Note{Visibility: "public"},
			want: true,
		},
		{
			name: "home visibility is embeddable",
			note: &model.Note{Visibility: "home"},
			want: true,
		},
		{
			// 宛先を指定した投稿。埋め込むと宛先以外の誰でも読める。
			name: "specified visibility is never embeddable",
			note: &model.Note{Visibility: "specified"},
			want: false,
		},
		{
			// フォロワー限定。同上。
			name: "followers visibility is never embeddable",
			note: &model.Note{Visibility: "followers"},
			want: false,
		},
		{
			// リモートノートは自分が権威でないので配らない (upstream と同じ)。
			name: "remote note is not embeddable",
			note: &model.Note{Visibility: "public", UserHost: strptr("remote.example")},
			want: false,
		},
		{
			// host が空文字列はローカル扱い。DB 上 NULL でなく "" が入る経路への保険。
			name: "empty host counts as local",
			note: &model.Note{Visibility: "public", UserHost: strptr("")},
			want: true,
		},
		{
			name: "remote followers note is not embeddable",
			note: &model.Note{Visibility: "followers", UserHost: strptr("remote.example")},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := embedNoteIsPublic(tt.note); got != tt.want {
				t.Errorf("embedNoteIsPublic(%+v) = %v, want %v", tt.note, got, tt.want)
			}
		})
	}
}

// JSON を <script> に埋める以上、閉じタグの早期終了は必ず潰す。ノート本文も
// ユーザー名も攻撃者が自由に書けるので、ここが抜けると XSS になる。
func TestEscapeJSONForScript(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "closing script tag is neutralised",
			input: `{"text":"</script><img src=x onerror=alert(1)>"}`,
			want:  `{"text":"\u003c/script\u003e\u003cimg src=x onerror=alert(1)\u003e"}`,
		},
		{
			name:  "ampersand is escaped",
			input: `{"text":"a&b"}`,
			want:  `{"text":"a\u0026b"}`,
		},
		{
			name:  "plain json is unchanged",
			input: `{"a":1}`,
			want:  `{"a":1}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeJSONForScript(tt.input); got != tt.want {
				t.Errorf("escapeJSONForScript(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// 攻撃者が制御できる文字列がどう来ても、script 要素を閉じる並びは残らない。
func TestEscapeJSONForScriptNeverLeavesClosingTag(t *testing.T) {
	payloads := []string{
		`{"text":"</script>"}`,
		`{"text":"</SCRIPT>"}`,
		`{"text":"</script >"}`,
		`{"name":"<!--</script>"}`,
	}
	for _, p := range payloads {
		got := escapeJSONForScript(p)
		if strings.Contains(got, "<") || strings.Contains(got, ">") {
			t.Errorf("escapeJSONForScript(%q) left raw angle brackets: %q", p, got)
		}
	}
}
