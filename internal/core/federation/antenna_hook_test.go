package federation_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
)

// #2743: リモートの投稿が antenna に一切入らなかった。antenna hook は
// note_create_service (= ローカル作成) にしか配線されておらず、inbound
// Create / Announce は NoteCreateService を通らないため。
//
// **リレー経由だけは意図的に外している。** ただし判定は「どの関数を通るか」
// ではなく signer / announcer で決まる。publishRelayDeliveredNote を通らない
// 形 (Mastodon 系リレーが元の Create / Announce を転送し署名だけがリレーに
// なる) があるため、handleCreate / handleAnnounce 側にも gate が要る。
// 最初の実装はここを取り違えて 2 形態が素通りしていた。

// recordingAntennaHook は federation.AntennaHook 専用の stub。
//
// **fanout の stub を流用しないこと。** 両 interface はメソッドセットが同一
// だったため、以前は取り違えてもテストが全部緑になった。marker メソッドを
// 足して型で分けてある (#2743)。
type recordingAntennaHook struct {
	calls chan recordingFanoutCall
}

func newRecordingAntennaHook() *recordingAntennaHook {
	return &recordingAntennaHook{calls: make(chan recordingFanoutCall, 16)}
}

func (h *recordingAntennaHook) OnNoteCreated(note *model.Note, author *model.User) {
	authorID := ""
	if author != nil {
		authorID = author.ID
	}
	noteID := ""
	if note != nil {
		noteID = note.ID
	}
	h.calls <- recordingFanoutCall{noteID: noteID, author: authorID}
}

func (h *recordingAntennaHook) IsAntennaHook() {}

func TestProcess_CreateNote_FiresAntennaHook(t *testing.T) {
	env := newFullProcessor(t, aliceActor)
	hook := newRecordingAntennaHook()
	env.processor.SetAntennaHook(hook)

	noteURI := "https://remote.example/notes/antenna-create"
	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"object": {
			"type": "Note",
			"id": "` + noteURI + `",
			"attributedTo": "https://remote.example/users/alice",
			"content": "hi"
		}
	}`)
	require.NoError(t, env.processor.Process(body))

	ingested := findIngestedRemoteNote(t, env.noteRepo, noteURI)
	select {
	case call := <-hook.calls:
		assert.Equal(t, ingested.ID, call.noteID, "取り込んだ note が渡る")
	case <-time.After(2 * time.Second):
		t.Fatal("inbound Create で antennaHook が呼ばれなかった (#2743)")
	}
}

func TestProcess_Announce_FiresAntennaHook(t *testing.T) {
	env := newFullProcessor(t, aliceActor)
	hook := newRecordingAntennaHook()
	env.processor.SetAntennaHook(hook)

	env.noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	env.userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}

	body := []byte(`{
		"type": "Announce",
		"id": "https://remote.example/announces/antenna-1",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/n1"
	}`)
	require.NoError(t, env.processor.Process(body))

	select {
	case call := <-hook.calls:
		assert.NotEqual(t, "n1", call.noteID, "渡るのは target ではなく renote 行")
		assert.NotEmpty(t, call.noteID)
	case <-time.After(2 * time.Second):
		t.Fatal("Announce で antennaHook が呼ばれなかった (#2743)")
	}
}

// TestProcess_RelayDeliveredNote_DoesNotFireAntennaHook は #2743 で
// **意図的に外した**経路を固定する。
//
// 同じ Announce activity に対して fanoutHook は発火し antennaHook は発火
// しない、という差そのものを assert する。片方だけ見ると「hook が呼ばれない」
// が配線漏れなのか意図なのか区別できないため。
//
// 外す理由: publishRelayDeliveredNote が扱うのは ephemeral note (DB に行が
// 無い)。antenna service は materialize しない方針なので DB は膨らまないが、
// pushNote は走るので DB から引けない ID が ZSET (上限 200) を埋める。
func TestProcess_RelayDeliveredNote_DoesNotFireAntennaHook(t *testing.T) {
	env := newFullProcessor(t, aliceActor)
	env.processor.SetRelayActorChecker(&stubRelayActorChecker{
		relayActorURI: "https://remote.example/users/alice",
	})
	fanout := newRecordingFanoutHook()
	antenna := newRecordingAntennaHook()
	env.processor.SetFanoutHook(fanout)
	env.processor.SetAntennaHook(antenna)

	env.noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	env.userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}

	body := []byte(`{
		"type": "Announce",
		"id": "https://remote.example/announces/relay-antenna",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/n1"
	}`)
	require.NoError(t, env.processor.Process(body))

	// fanout は発火する (= リレー経由の配送自体は成立している)。
	select {
	case call := <-fanout.calls:
		assert.Equal(t, "n1", call.noteID)
	case <-time.After(2 * time.Second):
		t.Fatal("fanoutHook が呼ばれなかった (テストの前提が崩れている)")
	}

	// 他の 2 本と同じ sentinel + drain で見る。fanout 受信だけを錨にすると
	// hook が 400ms 遅れる変異で素通りする (実測)。
	sentinelURI := "https://remote.example/notes/sentinel-relay"
	sentinel := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"object": {
			"type": "Note",
			"id": "` + sentinelURI + `",
			"attributedTo": "https://remote.example/users/alice",
			"content": "sentinel"
		}
	}`)
	require.NoError(t, env.processor.Process(sentinel))
	want := findIngestedRemoteNote(t, env.noteRepo, sentinelURI)

	assertOnlySentinelFired(t, antenna, want.ID, "リレー経由の note")
}

// TestProcess_RelayForwardedCreate_DoesNotFireAntennaHook は #2743 のレビューで
// 見つかった穴を固定する。
//
// Mastodon 系リレーは元の Create をそのまま転送し、**署名だけがリレーのもの**
// になる。handleCreate は既に `viaRelay := p.isRelayDelivery(signer)` を持って
// いて ingestCreateNote に渡しているが、antenna hook がそれを見落としていた。
// 見落とすと ephemeral 有効時に DB に行が無い note が antenna の ZSET に積まれ、
// 上限 200 を幽霊 ID が埋める。
// assertOnlySentinelFired は「リレー由来では発火しない」ことを、壁時計待ちだけ
// に頼らず確かめる。
//
// 単に「一定時間発火しなかった」で判定すると、gate を外しても hook goroutine が
// 遅れれば通ってしまう (実測で素通りした)。ここでは 2 段構えにする。
//
//  1. 直接配送の sentinel を後から流し、**最初に届いた 1 件が sentinel か**を
//     見る
//  2. そのうえで bounded drain し、**2 件目が来ない**ことを見る
//
// 実測では gate を壊した変異を捕まえているのは **2 段目だけ**。リレー経路は
// actor 解決で余計に時間を使うので、先着するのはむしろ sentinel 側だった。
// 1 段目は「別の note が渡る」変異に対する保険として残している。
func assertOnlySentinelFired(t *testing.T, hook *recordingAntennaHook, sentinelID, what string) {
	t.Helper()
	select {
	case call := <-hook.calls:
		assert.Equal(t, sentinelID, call.noteID, "%s が antenna に載った (sentinel より先に届いた)", what)
	case <-time.After(2 * time.Second):
		t.Fatal("直接配送の Create でも antennaHook が呼ばれなかった")
	}
	select {
	case call := <-hook.calls:
		t.Fatalf("%s が antenna に載った (遅れて届いた, noteID=%s)", what, call.noteID)
	case <-time.After(1500 * time.Millisecond):
	}
}

func TestProcess_RelayForwardedCreate_DoesNotFireAntennaHook(t *testing.T) {
	env := newFullProcessor(t, aliceActor)
	env.processor.SetRelayActorChecker(&stubRelayActorChecker{
		relayActorURI: "https://remote.example/users/alice",
	})
	hook := newRecordingAntennaHook()
	env.processor.SetAntennaHook(hook)
	relayURI := "https://remote.example/users/alice"
	signer := &model.User{ID: "relay1", Username: "alice", URI: &relayURI}

	body := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"object": {
			"type": "Note",
			"id": "https://remote.example/notes/relay-create",
			"attributedTo": "https://remote.example/users/alice",
			"content": "hi"
		}
	}`)
	require.NoError(t, env.processor.ProcessWithSigner(body, signer))

	// **sentinel 方式で壁時計依存を消す。** 「一定時間発火しなかった」で判定
	// すると、gate を外しても hook が遅れれば通ってしまう (実際に変異検証で
	// 素通りした)。リレーの後に**直接配送**を 1 本流し、その発火を待ってから
	// 「最初に届いたのが sentinel か」を見る。リレー側が発火していれば先に
	// 処理した分がキューに入るので捕まる。
	sentinelURI := "https://remote.example/notes/sentinel-create"
	sentinel := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"object": {
			"type": "Note",
			"id": "` + sentinelURI + `",
			"attributedTo": "https://remote.example/users/alice",
			"content": "sentinel"
		}
	}`)
	require.NoError(t, env.processor.Process(sentinel))
	want := findIngestedRemoteNote(t, env.noteRepo, sentinelURI)

	assertOnlySentinelFired(t, hook, want.ID, "リレーが転送した Create")
}

// TestProcess_RelayForwardedAnnounce_DoesNotFireAntennaHook は同じく
// レビューで見つかった 2 つ目の穴。
//
// handleAnnounce の既存 `viaRelay` は **announcer が relay actor 本人か**しか
// 見ないので (Misskey 系リレー)、Mastodon 系リレーが他人の Announce を転送する
// 形を捕まえられない。signer 側でも判定する。
//
// 既存の renote 抑止の判定は変えていない — この形は従来どおり renote を作る。
func TestProcess_RelayForwardedAnnounce_DoesNotFireAntennaHook(t *testing.T) {
	env := newFullProcessor(t, aliceActor)
	// **relay actor は announcer とは別人**にする。announcer (alice) が relay
	// actor 本人だと既存の viaRelay で捕まってしまい、signer 側の判定を
	// 検証できない (= テストが空振りする)。
	env.processor.SetRelayActorChecker(&stubRelayActorChecker{
		relayActorURI: "https://relay.example/actor",
	})
	hook := newRecordingAntennaHook()
	env.processor.SetAntennaHook(hook)
	fanout := newRecordingFanoutHook()
	env.processor.SetFanoutHook(fanout)

	env.noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	env.userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}

	relayURI := "https://relay.example/actor"
	signer := &model.User{ID: "relay1", Username: "relay", URI: &relayURI}

	body := []byte(`{
		"type": "Announce",
		"id": "https://remote.example/announces/relay-fwd",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/n1"
	}`)
	require.NoError(t, env.processor.ProcessWithSigner(body, signer))

	// 前提の確認: この形は従来どおり renote を作る (既存の viaRelay は false)。
	var renotes int
	for _, n := range env.noteRepo.Notes {
		if n.RenoteID != nil && *n.RenoteID == "n1" {
			renotes++
		}
	}
	require.Equal(t, 1, renotes, "renote 抑止の判定は変えていない (テストの前提)")

	// sentinel 方式 (理由は Create 側のテストを参照)。
	sentinelURI := "https://remote.example/notes/sentinel-announce"
	sentinel := []byte(`{
		"type": "Create",
		"actor": "https://remote.example/users/alice",
		"to": ["https://www.w3.org/ns/activitystreams#Public"],
		"object": {
			"type": "Note",
			"id": "` + sentinelURI + `",
			"attributedTo": "https://remote.example/users/alice",
			"content": "sentinel"
		}
	}`)
	require.NoError(t, env.processor.Process(sentinel))
	want := findIngestedRemoteNote(t, env.noteRepo, sentinelURI)

	assertOnlySentinelFired(t, hook, want.ID, "リレーが転送した Announce")
}
