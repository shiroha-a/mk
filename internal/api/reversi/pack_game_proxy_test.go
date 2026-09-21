package reversi

import (
	"context"
	"net/url"
	"strings"
	"testing"

	corereversi "github.com/shiroha-a/mk/internal/core/reversi"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// capturingGamePublisher captures the payloads the core reversi Service pushes
// to the WebSocket channel.
type capturingGamePublisher struct {
	events []capturedGameEvent
}

type capturedGameEvent struct {
	gameID    string
	eventType string
	body      map[string]any
}

func (p *capturingGamePublisher) PublishGameEvent(gameID, eventType string, body any) {
	m, _ := body.(map[string]any)
	p.events = append(p.events, capturedGameEvent{gameID: gameID, eventType: eventType, body: m})
}

// REST (entity.PackUserLite 経由) と stream (core の userLiteMap +
// SetAvatarProxy) が同じ avatar URL を返すことを固定する。
//
// stream 側は #417 の core/entity 分離で自前 packer になり、media proxy の適用が
// 抜けていた (#1529)。REST だけ / stream だけを生 URL に戻す変異をここで落とす。
func TestPackGame_RESTAndStreamAgreeOnProxiedAvatar(t *testing.T) {
	entity.SetMediaURLContext(entity.NewMediaURLContext(
		"https://mk.example", "https://mk.example/proxy", []byte("s"), false, true))
	t.Cleanup(func() { entity.SetMediaURLContext(nil) })

	host := "remote.example"
	remoteAvatar := "https://remote.example/a.png"
	u1 := &model.User{ID: "alice", Username: "alice", Host: &host, AvatarURL: &remoteAvatar}
	u2 := &model.User{ID: "bob", Username: "bob"}
	g := &model.ReversiGame{
		ID: "g1", User1ID: "alice", User2ID: "bob",
		Map: model.StringArray{"--------", "--------", "--------", "---wb---", "---bw---", "--------", "--------", "--------"},
		BW:  "random", TimeLimitForEachTurn: 90,
		Logs:  datatypes.JSON("[]"),
		User1: u1, User2: u2,
	}

	h, repo := newTestHandler()
	rest := packGame(g, h.idGen)

	// stream 側: core Service を駆動して started payload を捕まえる。
	// mock の MarkStarted は repo 行の IsStarted を guard にする (実 DB の
	// atomic claim 相当) ので、Service に渡すオブジェクトとは別に未開始の行を
	// repo へ置く。
	stored := *g
	repo.games[g.ID] = &stored
	pub := &capturingGamePublisher{}
	svc := corereversi.NewService(repo, pub, nil)
	svc.SetAvatarProxy(entity.ProxyAvatarURLString)
	require.NoError(t, svc.StartGame(context.Background(), g))
	require.Len(t, pub.events, 1)
	require.Equal(t, "started", pub.events[0].eventType)
	streamGame, ok := pub.events[0].body["game"].(map[string]any)
	require.True(t, ok)

	// remote user: 両経路とも proxy 済みで、URL が一致する。
	restU1 := rest["user1"].(entity.UserLite)
	streamU1, ok := streamGame["user1"].(map[string]any)
	require.True(t, ok)
	streamAvatar, ok := streamU1["avatarUrl"].(*string)
	require.True(t, ok)
	require.NotNil(t, streamAvatar)
	assert.Equal(t, restU1.AvatarURL, *streamAvatar, "REST と stream で avatar URL が食い違う")
	assert.True(t, strings.HasPrefix(restU1.AvatarURL, "https://mk.example/proxy/avatar.webp?"),
		"rest avatar must be proxied, got %q", restU1.AvatarURL)
	parsed, err := url.Parse(restU1.AvatarURL)
	require.NoError(t, err)
	assert.Equal(t, "mk.example", parsed.Host, "proxy 自身のホスト以外を向いている")
	assert.NotEqual(t, remoteAvatar, *streamAvatar,
		"stream が生 URL を返している (SetAvatarProxy 未配線)")

	// local user (avatar なし): identicon は両経路とも相対 URL のまま。
	restU2 := rest["user2"].(entity.UserLite)
	streamU2, ok := streamGame["user2"].(map[string]any)
	require.True(t, ok)
	streamIdenticon, ok := streamU2["avatarUrl"].(*string)
	require.True(t, ok)
	require.NotNil(t, streamIdenticon)
	assert.Equal(t, "/identicon/bob", restU2.AvatarURL)
	assert.Equal(t, restU2.AvatarURL, *streamIdenticon)
}
