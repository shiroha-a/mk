package timeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSuspendedLookup は SuspendedAuthorLookup の最小実装。
type fakeSuspendedLookup struct {
	suspended map[string]bool
	calls     int
	err       error
}

func (f *fakeSuspendedLookup) FindByID(id string) (*model.User, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	s, ok := f.suspended[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return &model.User{ID: id, IsSuspended: s}, nil
}

// fannedOut reports whether the note reached the author's user timeline.
func fannedOut(t *testing.T, fanout *FanoutTimelineService, authorID, noteID string) bool {
	t.Helper()
	out, err := fanout.Get(context.Background(), UserTimelineName(authorID), "", "", 10)
	require.NoError(t, err)
	for _, id := range out {
		if id == noteID {
			return true
		}
	}
	return false
}

// #2624: 凍結ユーザー本人の note は fanout しない。
func TestFanoutHook_SuspendedAuthorIsNotFannedOut(t *testing.T) {
	h, fanout, _ := newTestHook(t)

	noteID := idGen.Generate(time.Now())
	n := &model.Note{ID: noteID, UserID: "author", Visibility: model.NoteVisibilityPublic}
	h.OnNoteCreated(n, &model.User{ID: "author", IsSuspended: true})

	assert.False(t, fannedOut(t, fanout, "author", noteID), "凍結ユーザーの note は fanout されない")
}

// #2624: リノート先の author が凍結なら fanout しない (preload 済みの経路)。
func TestFanoutHook_SuspendedRenoteTargetPreloaded(t *testing.T) {
	h, fanout, _ := newTestHook(t)

	targetID := "target"
	noteID := idGen.Generate(time.Now())
	n := &model.Note{
		ID: noteID, UserID: "author", Visibility: model.NoteVisibilityPublic,
		RenoteID:     &targetID,
		RenoteUserID: &targetID,
		Renote:       &model.Note{ID: targetID, UserID: targetID, User: &model.User{ID: targetID, IsSuspended: true}},
	}
	h.OnNoteCreated(n, &model.User{ID: "author"})

	assert.False(t, fannedOut(t, fanout, "author", noteID), "凍結ユーザーへのリノートは fanout されない")
}

// #2624: preload が無くても renoteUserId から引いて判定する。
// inbound Announce (handleAnnounce) はリノートを作った直後で Renote を
// preload していないため、この経路が本番で効く側になる。
func TestFanoutHook_SuspendedRenoteTargetViaLookup(t *testing.T) {
	h, fanout, _ := newTestHook(t)
	lookup := &fakeSuspendedLookup{suspended: map[string]bool{"target": true}}
	h.SetSuspendedAuthorLookup(lookup)

	targetID := "target"
	targetNoteID := "target_note"
	noteID := idGen.Generate(time.Now())
	n := &model.Note{
		ID: noteID, UserID: "author", Visibility: model.NoteVisibilityPublic,
		RenoteID:     &targetNoteID,
		RenoteUserID: &targetID,
	}
	h.OnNoteCreated(n, &model.User{ID: "author"})

	assert.False(t, fannedOut(t, fanout, "author", noteID), "lookup 経由でも凍結を検出する")
	assert.Equal(t, 1, lookup.calls, "renote 先だけを 1 回引く")
}

// #2624: 返信先の author が凍結でも弾く (取得経路と同じ 3 author)。
func TestFanoutHook_SuspendedReplyTargetViaLookup(t *testing.T) {
	h, fanout, _ := newTestHook(t)
	h.SetSuspendedAuthorLookup(&fakeSuspendedLookup{suspended: map[string]bool{"target": true}})

	targetID := "target"
	targetNoteID := "target_note"
	noteID := idGen.Generate(time.Now())
	n := &model.Note{
		ID: noteID, UserID: "author", Visibility: model.NoteVisibilityPublic,
		ReplyID:     &targetNoteID,
		ReplyUserID: &targetID,
	}
	h.OnNoteCreated(n, &model.User{ID: "author"})

	assert.False(t, fannedOut(t, fanout, "author", noteID), "凍結ユーザーへの返信も弾く")
}

// #2624: 凍結していない相手へのリノートは従来どおり fanout する。
func TestFanoutHook_ActiveRenoteTargetIsFannedOut(t *testing.T) {
	h, fanout, _ := newTestHook(t)
	h.SetSuspendedAuthorLookup(&fakeSuspendedLookup{suspended: map[string]bool{"target": false}})

	targetID := "target"
	targetNoteID := "target_note"
	noteID := idGen.Generate(time.Now())
	n := &model.Note{
		ID: noteID, UserID: "author", Visibility: model.NoteVisibilityPublic,
		RenoteID:     &targetNoteID,
		RenoteUserID: &targetID,
	}
	h.OnNoteCreated(n, &model.User{ID: "author"})

	assert.True(t, fannedOut(t, fanout, "author", noteID), "凍結していなければ従来どおり流す")
}

// #2624: lookup 未配線でも従来どおり動く (後方互換)。
func TestFanoutHook_WithoutLookupKeepsLegacyBehaviour(t *testing.T) {
	h, fanout, _ := newTestHook(t)

	targetID := "target"
	targetNoteID := "target_note"
	noteID := idGen.Generate(time.Now())
	n := &model.Note{
		ID: noteID, UserID: "author", Visibility: model.NoteVisibilityPublic,
		RenoteID:     &targetNoteID,
		RenoteUserID: &targetID,
	}
	h.OnNoteCreated(n, &model.User{ID: "author"})

	assert.True(t, fannedOut(t, fanout, "author", noteID), "lookup 未配線なら素通し")
}

// #2624: user 行が引けない (削除済み) 場合は素通しする。
// SQL 側の `NOT EXISTS` も user 行が無ければ通すので、両経路の扱いを揃える。
func TestFanoutHook_UnknownRenoteAuthorIsFannedOut(t *testing.T) {
	h, fanout, _ := newTestHook(t)
	h.SetSuspendedAuthorLookup(&fakeSuspendedLookup{suspended: map[string]bool{}})

	targetID := "deleted"
	targetNoteID := "target_note"
	noteID := idGen.Generate(time.Now())
	n := &model.Note{
		ID: noteID, UserID: "author", Visibility: model.NoteVisibilityPublic,
		RenoteID:     &targetNoteID,
		RenoteUserID: &targetID,
	}
	h.OnNoteCreated(n, &model.User{ID: "author"})

	assert.True(t, fannedOut(t, fanout, "author", noteID), "削除済みユーザーは SQL 側と同じく通す")
}

// #2624: reply も renote も無い note は lookup を引かない (追加クエリ 0 回)。
func TestFanoutHook_PlainNoteDoesNotQueryLookup(t *testing.T) {
	h, _, _ := newTestHook(t)
	lookup := &fakeSuspendedLookup{suspended: map[string]bool{}}
	h.SetSuspendedAuthorLookup(lookup)

	noteID := idGen.Generate(time.Now())
	n := &model.Note{ID: noteID, UserID: "author", Visibility: model.NoteVisibilityPublic}
	h.OnNoteCreated(n, &model.User{ID: "author"})

	assert.Equal(t, 0, lookup.calls, "reply/renote が無ければ DB を引かない")
}
