package reaction

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #2106 N15: resolveReaction の unicode 分岐は非絵文字を ❤ (FallbackReaction) に矯正する
// (upstream ReactionService.normalize 相当)。custom/legacy でない任意文字列は保存しない。
func TestResolveReaction_UnicodeValidation(t *testing.T) {
	s := &Service{} // unicode/非custom 入力は emojiRepo を参照しない
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"grinning", "\U0001F600", "\U0001F600"},
		{"heart_with_vs", "❤️", "❤"}, // VS strip 後の canonical (= U+2764)
		{"thumbsup_skin", "\U0001F44D\U0001F3FD", "\U0001F44D\U0001F3FD"},
		{"flag_jp", "\U0001F1EF\U0001F1F5", "\U0001F1EF\U0001F1F5"},
		{"plain_text", "abc", FallbackReaction},
		{"hiragana", "あ", FallbackReaction},
		{"script_tag", "<script>", FallbackReaction},
		{"emoji_plus_text", "\U0001F600x", FallbackReaction},
	}
	for _, c := range cases {
		got, _ := s.resolveReaction(c.in, nil)
		assert.Equal(t, c.want, got, "input=%q (%s)", c.in, c.name)
	}
}
