package transfer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseFollowingCSVLine covers the `acct[,key=value...]` parser used by
// importFollowing (#1058). upstream Misskey TS の loose `value === 'true'`
// semantics と互換にするため、"true" 以外は (空文字 / "false" / 不明値) すべて
// false として扱う。
func TestParseFollowingCSVLine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		line    string
		acct    string
		options FollowOptions
	}{
		{
			name:    "acct only",
			line:    "@bob@host.example",
			acct:    "@bob@host.example",
			options: FollowOptions{WithReplies: false},
		},
		{
			name:    "withReplies=true",
			line:    "@bob@host.example,withReplies=true",
			acct:    "@bob@host.example",
			options: FollowOptions{WithReplies: true},
		},
		{
			name:    "withReplies=false",
			line:    "@bob@host.example,withReplies=false",
			acct:    "@bob@host.example",
			options: FollowOptions{WithReplies: false},
		},
		{
			name: "withReplies=invalid falls back to false",
			// loose parsing: "true" 以外はすべて false (upstream の
			// `value === 'true'` と同 semantics)。
			line:    "@bob@host.example,withReplies=yes",
			acct:    "@bob@host.example",
			options: FollowOptions{WithReplies: false},
		},
		{
			name:    "unknown key is silently ignored",
			line:    "@bob@host.example,silent=true",
			acct:    "@bob@host.example",
			options: FollowOptions{WithReplies: false},
		},
		{
			name: "malformed key=value fragment (no =) silently skipped",
			// `withReplies=true` が後続にあるので acct + WithReplies=true で
			// 解釈される。途中の bare token は skip。
			line:    "@bob@host.example,bareToken,withReplies=true",
			acct:    "@bob@host.example",
			options: FollowOptions{WithReplies: true},
		},
		{
			name:    "leading/trailing spaces stripped",
			line:    "  @bob@host.example  ,  withReplies=true  ",
			acct:    "@bob@host.example",
			options: FollowOptions{WithReplies: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			acct, opts := parseFollowingCSVLine(tc.line)
			assert.Equal(t, tc.acct, acct)
			assert.Equal(t, tc.options, opts)
		})
	}
}
