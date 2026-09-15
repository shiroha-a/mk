package activitypub

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// **組み立てと解析は対でしか意味を持たない (#2994)。** 受信側はこの正規表現で
// room id を取り出すので、`ChatRoomURI` のパス形状を変えると連合が黙って壊れる。
func TestChatRoomURI_RoundTrip(t *testing.T) {
	b := NewURLBuilder("https://local.example")
	uri := b.ChatRoomURI("ar09czcv0ie9004f")
	assert.Equal(t, "https://local.example/chat/rooms/ar09czcv0ie9004f", uri)
	assert.Equal(t, "ar09czcv0ie9004f", ChatRoomIDFromURI(uri), "自分で作った URI を解析できない")
	assert.True(t, IsChatRoomURI(uri))
}

// **サブパス構成の base URL でも往復すること。** `config.url` が
// `https://h/misskey` のような構成では room URI もその下に出る。接頭辞を
// `^https?://[^/]+/chat/rooms/` に縛ると相手の正当な URI を落とす。
func TestChatRoomURI_SubpathBaseURL(t *testing.T) {
	b := NewURLBuilder("https://local.example/misskey")
	uri := b.ChatRoomURI("room1")
	assert.Equal(t, "https://local.example/misskey/chat/rooms/room1", uri)
	assert.Equal(t, "room1", ChatRoomIDFromURI(uri))
}

func TestChatRoomIDFromURI(t *testing.T) {
	for _, tc := range []struct {
		name string
		uri  string
		want string
	}{
		{"通常", "https://remote.example/chat/rooms/room1", "room1"},
		{"port 付き", "https://remote.example:8080/chat/rooms/room1", "room1"},
		{"空", "", ""},
		{"別の path", "https://remote.example/notes/abc", ""},
		{"末尾スラッシュ", "https://remote.example/chat/rooms/room1/", ""},
		{"id が無い", "https://remote.example/chat/rooms/", ""},
		// 英数字以外は id にならない (NUL や区切りを混ぜられない)。
		{"記号入り", "https://remote.example/chat/rooms/ro-om1", ""},
		{"クエリ付き", "https://remote.example/chat/rooms/room1?x=1", ""},
		// path の途中に現れるだけのものは room ではない。
		{"接尾ではない", "https://remote.example/chat/rooms/room1/messages", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ChatRoomIDFromURI(tc.uri))
			assert.Equal(t, tc.want != "", IsChatRoomURI(tc.uri))
		})
	}
}
