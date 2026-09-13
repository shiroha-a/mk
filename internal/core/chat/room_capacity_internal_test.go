package chat

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apiMaxRoomMembersRe reads the api layer's own copy of the room capacity.
// 値は `const maxRoomMembers = 50` の形で書かれている。
var apiMaxRoomMembersRe = regexp.MustCompile(`(?m)^const maxRoomMembers = (\d+)$`)

// TestMaxRoomMembersMatchesAPILayer pins the room capacity used by the AP
// invitation path to the one the local API enforces.
//
// 定員はローカル API (`chat/rooms/invitations/create` / `rooms/join`) と AP 経路の
// 2 箇所で判定する。api 層の定数は `internal/api/chat` パッケージのもので core から
// import できない (依存が逆向きになる) ため値を複製しているが、**片側だけ動かすと
// 「ローカルでは満室、連合経由なら入れる」形の乖離になる**ので、ここでソースを
// 読んで突き合わせる。読めなかったら落とす (書式が変われば検査が黙って止まるため)。
func TestMaxRoomMembersMatchesAPILayer(t *testing.T) {
	const apiHandler = "../../api/chat/handler.go"
	src, err := os.ReadFile(apiHandler)
	require.NoError(t, err, "api 層の handler を読めること")

	m := apiMaxRoomMembersRe.FindSubmatch(src)
	require.Len(t, m, 2, "%s の `const maxRoomMembers = N` を読めること", apiHandler)
	apiValue, err := strconv.Atoi(string(m[1]))
	require.NoError(t, err)

	assert.Equal(t, apiValue, maxRoomMembers,
		"core/chat と internal/api/chat の room 定員が食い違っている")
	// upstream ChatService.MAX_ROOM_MEMBERS。両側を同時に動かす変更も止める。
	assert.Equal(t, 50, maxRoomMembers, "upstream MAX_ROOM_MEMBERS と一致すること")
}
