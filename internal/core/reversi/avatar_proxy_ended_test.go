package reversi

import (
	"context"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// **`ended` payload の 3 経路すべてが avatarProxy (SetAvatarProxy) を通ること
// (#3130 review)。**
//
// `TestPackGame_RESTAndStreamAgreeOnProxiedAvatar` (internal/api/reversi) は
// `StartGame` の `started` payload しか駆動していないので、`PutStone` の
// 自然終了 / `Surrender` / `CheckTimeout` の 3 経路 — どれも `packGame(game,
// s.avatarProxy)` を呼ぶ箇所が独立している — のいずれか 1 つを
// `packGame(game, nil)` へ戻しても検出できなかった。ここでは core パッケージ
// 単独 (entity へ依存しない stub proxy) で 3 経路それぞれの `ended` event を
// 直接検証する。これは同時に、レビューで指摘された「core パッケージ自身の
// カバレッジが 0%」(avatarProxy 分岐が internal/api/reversi 経由でしか
// 実行されていない) も解消する。

// stubAvatarProxy makes proxied URLs distinguishable from raw ones without
// pulling in the entity package (#417 の layer 規約: core は entity に
// 依存しない)。
func stubAvatarProxy(raw string) string { return "proxied:" + raw }

// remoteReversiUsers returns a User1/User2 pair where BOTH users have
// distinct remote avatars, so packGame's userLiteMap has something to proxy
// for every UserLite slot it fills (user1 / user2 / winner).
//
// **user2 も remote avatar を持つ必要がある (#3130 review 3周目)。** 以前は
// user2 が avatar 未設定 (identicon 枝) だったため、
// `out["user2"] = userLiteMap(game.User2, avatarProxy)` の avatarProxy を
// nil に戻しても userLiteMap 内の `avatarProxy != nil` 分岐自体が実行されず、
// どのテストも検出できなかった。
func remoteReversiUsers() (*model.User, *model.User) {
	host := "remote.example"
	avatar1 := "https://remote.example/a.png"
	avatar2 := "https://remote.example/b.png"
	u1 := &model.User{ID: "alice", Username: "alice", Host: &host, AvatarURL: &avatar1}
	u2 := &model.User{ID: "bob", Username: "bob", Host: &host, AvatarURL: &avatar2}
	return u1, u2
}

// endedGamePayload extracts the "game" field from the latest "ended" event
// captured by pub, failing the test if none was published.
func endedGamePayload(t *testing.T, pub *capturePublisher) map[string]any {
	t.Helper()
	for i := len(pub.events) - 1; i >= 0; i-- {
		if pub.events[i].kind != "ended" {
			continue
		}
		body, ok := pub.events[i].body.(map[string]any)
		require.True(t, ok, "ended event body must be a map")
		game, ok := body["game"].(map[string]any)
		require.True(t, ok, "ended event body must include game")
		return game
	}
	t.Fatal("no ended event was published")
	return nil
}

// assertUserLiteAvatarProxied checks that a single UserLite map's avatarUrl
// went through SetAvatarProxy.
func assertUserLiteAvatarProxied(t *testing.T, userLite map[string]any, rawURL string) {
	t.Helper()
	got, _ := userLite["avatarUrl"].(*string)
	require.NotNil(t, got, "avatarUrl must be present")
	assert.Equal(t, "proxied:"+rawURL, *got,
		"avatar url did not go through SetAvatarProxy")
}

// assertAvatarProxied checks that user1's AND user2's avatars in an
// ended-event game payload went through SetAvatarProxy, and — when the
// payload includes a winner (Surrender / CheckTimeout always set one) —
// that the winner slot did too.
//
// **3 スロットとも独立した packGame の呼び出し行 (#3130 review 3周目)。**
// `out["user1"] = userLiteMap(game.User1, avatarProxy)` /
// `out["user2"] = ...` / `out["winner"] = ...` はそれぞれ別行なので、
// user1 だけを見るテストでは user2 枝や winner 枝の avatarProxy を
// nil に戻す変異を検出できない。winner がどちらのユーザー由来かは
// 呼び出し側で決め打たず、winner map の id から判定する。
func assertAvatarProxied(t *testing.T, game map[string]any) {
	t.Helper()
	u1, ok := game["user1"].(map[string]any)
	require.True(t, ok, "ended payload must include user1")
	assertUserLiteAvatarProxied(t, u1, "https://remote.example/a.png")

	u2, ok := game["user2"].(map[string]any)
	require.True(t, ok, "ended payload must include user2")
	assertUserLiteAvatarProxied(t, u2, "https://remote.example/b.png")

	winner, ok := game["winner"].(map[string]any)
	if !ok {
		return
	}
	wantRaw := "https://remote.example/a.png"
	if id, _ := winner["id"].(string); id == "bob" {
		wantRaw = "https://remote.example/b.png"
	}
	assertUserLiteAvatarProxied(t, winner, wantRaw)
}

// **HasAvatarProxy 自体を core パッケージ内で直接演習する。** これまで
// `internal/server` の起動時ゲート (`criticalWiring`) からしか呼ばれておらず、
// `internal/server` は CI のカバレッジ対象外 (CLAUDE.md Section 4) なので
// `HasAvatarProxy` はこのパッケージ自身の実行では一度も通っていなかった
// (#3130 review)。
func TestService_HasAvatarProxy(t *testing.T) {
	svc := NewService(newFakeRepo(), &capturePublisher{}, nil)
	assert.False(t, svc.HasAvatarProxy(), "未配線なら false")
	svc.SetAvatarProxy(stubAvatarProxy)
	assert.True(t, svc.HasAvatarProxy(), "SetAvatarProxy 後は true")
}

// PutStone による自然終了 (engine.Turn == nil) の ended payload。
func TestService_PutStone_EndsGame_ProxiesAvatar(t *testing.T) {
	repo := newFakeRepo()
	pub := &capturePublisher{}
	svc := NewService(repo, pub, nil)
	svc.SetAvatarProxy(stubAvatarProxy)

	u1, u2 := remoteReversiUsers()
	// TestService_PutStone_EndsGame_SmallBoard と同じ 4x4 board: すぐに
	// 手詰まりになり finalize 経路 (ended) を踏む。
	game := &model.ReversiGame{
		ID:                   "tiny-proxy",
		User1ID:              "alice",
		User2ID:              "bob",
		Map:                  model.StringArray{"----", "-wb-", "-bw-", "----"},
		BW:                   "1",
		TimeLimitForEachTurn: 60,
		Logs:                 datatypes.JSON("[]"),
		User1:                u1,
		User2:                u2,
	}
	require.NoError(t, repo.Create(game))
	require.NoError(t, svc.UpdateReady(context.Background(), game.ID, "alice", true))
	require.NoError(t, svc.UpdateReady(context.Background(), game.ID, "bob", true))

	for range 16 {
		g, _ := repo.FindByID(game.ID)
		if g.IsEnded {
			break
		}
		engine, err := EngineFromGame(g)
		require.NoError(t, err)
		if engine.Turn == nil {
			break
		}
		places := engine.GetPuttablePlaces(*engine.Turn)
		if len(places) == 0 {
			break
		}
		uid := playerIDForColor(g, *engine.Turn)
		require.NotEmpty(t, uid)
		require.NoError(t, svc.PutStone(context.Background(), game.ID, uid, places[0], ""))
	}

	final, _ := repo.FindByID(game.ID)
	require.True(t, final.IsEnded, "test setup: small board must end via normal play")
	assertAvatarProxied(t, endedGamePayload(t, pub))
}

// Surrender の ended payload。
func TestService_Surrender_ProxiesAvatar(t *testing.T) {
	repo := newFakeRepo()
	pub := &capturePublisher{}
	svc := NewService(repo, pub, nil)
	svc.SetAvatarProxy(stubAvatarProxy)

	u1, u2 := remoteReversiUsers()
	game := &model.ReversiGame{
		ID:                   "surrender-proxy",
		User1ID:              "alice",
		User2ID:              "bob",
		Map:                  model.StringArray{"--------", "--------", "--------", "---wb---", "---bw---", "--------", "--------", "--------"},
		BW:                   "1",
		TimeLimitForEachTurn: 90,
		Logs:                 datatypes.JSON("[]"),
		User1:                u1,
		User2:                u2,
	}
	require.NoError(t, repo.Create(game))
	require.NoError(t, svc.UpdateReady(context.Background(), game.ID, "alice", true))
	require.NoError(t, svc.UpdateReady(context.Background(), game.ID, "bob", true))

	require.NoError(t, svc.Surrender(context.Background(), game.ID, "bob"))
	assertAvatarProxied(t, endedGamePayload(t, pub))
}

// CheckTimeout の ended payload。turnTimerExists は redis 未配線だと常に
// true (未期限) を返すので、期限切れを再現するには実 Redis (TestMain の
// reversiTestRedis) が要る (TestService_CheckTimeout_Expired と同じ形)。
func TestService_CheckTimeout_ProxiesAvatar(t *testing.T) {
	reversiTestRedis.FlushAll(context.Background())
	repo := newFakeRepo()
	pub := &capturePublisher{}
	svc := NewService(repo, pub, reversiTestRedis.Client)
	svc.SetAvatarProxy(stubAvatarProxy)

	u1, u2 := remoteReversiUsers()
	game := &model.ReversiGame{
		ID:                   "timeout-proxy",
		User1ID:              "alice",
		User2ID:              "bob",
		Map:                  model.StringArray{"--------", "--------", "--------", "---wb---", "---bw---", "--------", "--------", "--------"},
		BW:                   "1",
		TimeLimitForEachTurn: 1,
		Logs:                 datatypes.JSON("[]"),
		User1:                u1,
		User2:                u2,
	}
	require.NoError(t, repo.Create(game))
	require.NoError(t, svc.UpdateReady(context.Background(), game.ID, "alice", true))
	require.NoError(t, svc.UpdateReady(context.Background(), game.ID, "bob", true))

	require.NoError(t, reversiTestRedis.Client.Del(context.Background(), turnTimerKey(game.ID, 0)).Err())
	require.NoError(t, svc.CheckTimeout(context.Background(), game.ID))

	final, _ := repo.FindByID(game.ID)
	require.True(t, final.IsEnded, "test setup: timeout must end the game")
	assertAvatarProxied(t, endedGamePayload(t, pub))
}
