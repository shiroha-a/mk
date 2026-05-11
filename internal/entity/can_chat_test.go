package entity

import (
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"gorm.io/datatypes"
)

type stubCanChatLookup struct {
	results map[string]bool
}

func (s *stubCanChatLookup) LookupCanChat(userID string) (bool, bool) {
	v, ok := s.results[userID]
	return v, ok
}

// TestPackUserLite_CanChatDefault: lookup が unwired のとき canChat=true
// (= upstream DefaultPolicies の `chatAvailability: "available"`) で fallback
// すること。既存 unit test が SetCanChatLookup を呼ばずに通るための path。
func TestPackUserLite_CanChatDefault(t *testing.T) {
	t.Cleanup(func() { SetCanChatLookup(nil) })
	SetCanChatLookup(nil)

	u := &model.User{
		ID:                "u1",
		Username:          "default",
		ChatScope:         "none", // 旧実装ではこの値で canChat=false だった
		AvatarDecorations: datatypes.JSON([]byte("[]")),
	}
	lite := PackUserLite(u)
	assert.True(t, lite.CanChat, "lookup unwired → default true (upstream available)")
}

// TestPackUserLite_CanChatFromLookup: lookup が wired のとき lookup の結果が
// canChat に反映されること。role policy で chat 制限された user は false。
func TestPackUserLite_CanChatFromLookup(t *testing.T) {
	t.Cleanup(func() { SetCanChatLookup(nil) })
	SetCanChatLookup(&stubCanChatLookup{results: map[string]bool{
		"u1": true,
		"u2": false,
	}})

	u1 := &model.User{ID: "u1", Username: "ok", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	u2 := &model.User{ID: "u2", Username: "blocked", AvatarDecorations: datatypes.JSON([]byte("[]"))}

	assert.True(t, PackUserLite(u1).CanChat)
	assert.False(t, PackUserLite(u2).CanChat)
}

// TestPackUserLite_CanChatLookupNotFound: lookup is wired but the user is not
// in the result map (= legacy data without role assignment). Should fall back
// to default true.
func TestPackUserLite_CanChatLookupNotFound(t *testing.T) {
	t.Cleanup(func() { SetCanChatLookup(nil) })
	SetCanChatLookup(&stubCanChatLookup{results: map[string]bool{}})

	u := &model.User{ID: "unknown", Username: "unknown", AvatarDecorations: datatypes.JSON([]byte("[]"))}
	assert.True(t, PackUserLite(u).CanChat, "lookup ok=false → default true")
}

// TestPackUserLite_CanChatIgnoresChatScope: chatScope の値に関係なく lookup
// が支配的になること (= upstream の semantics、chatScope は受信側設定で
// canChat とは別軸)。
func TestPackUserLite_CanChatIgnoresChatScope(t *testing.T) {
	t.Cleanup(func() { SetCanChatLookup(nil) })
	SetCanChatLookup(&stubCanChatLookup{results: map[string]bool{"u1": false}})

	for _, scope := range []string{"all", "followers", "mutuals", "none"} {
		u := &model.User{ID: "u1", Username: "x", ChatScope: scope, AvatarDecorations: datatypes.JSON([]byte("[]"))}
		assert.False(t, PackUserLite(u).CanChat, "scope=%s should not affect canChat", scope)
	}
}
