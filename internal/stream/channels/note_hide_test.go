package channels

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
)

const hideNowMs int64 = 1_700_000_000_000

func sptr(s string) *string { return &s }

func embed(vis, author string, visibleUserIDs []string) entity.NoteEntity {
	if visibleUserIDs == nil {
		visibleUserIDs = []string{}
	}
	return entity.NoteEntity{
		ID: "embed", UserID: author, CreatedAt: "2026-01-02T03:04:05.000Z",
		User: entity.UserLite{ID: author}, Visibility: vis,
		Text: sptr("embed secret"), FileIDs: []string{}, Files: []any{},
		VisibleUserIDs: visibleUserIDs, Mentions: []string{},
	}
}

func parentJSON(t *testing.T, e entity.NoteEntity) []byte {
	t.Helper()
	parent := entity.NoteEntity{
		ID: "parent", UserID: "pa", CreatedAt: "2026-01-02T03:04:05.000Z",
		User: entity.UserLite{ID: "pa"}, Text: sptr("parent body"), Renote: &e,
		FileIDs: []string{}, Files: []any{}, VisibleUserIDs: []string{}, Mentions: []string{},
	}
	b, err := json.Marshal(parent)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func renoteOf(t *testing.T, out []byte) *entity.NoteEntity {
	t.Helper()
	var n entity.NoteEntity
	if err := json.Unmarshal(out, &n); err != nil {
		t.Fatal(err)
	}
	return n.Renote
}

func TestHideEmbedsForViewer_FollowersEmbed(t *testing.T) {
	viewer := &model.User{ID: "viewer"}

	t.Run("non-follower blanked", func(t *testing.T) {
		payload := parentJSON(t, embed("followers", "author", nil))
		out := hideEmbedsForViewer(payload, viewer, map[string]bool{}, hideNowMs)
		r := renoteOf(t, out)
		if r == nil || !r.IsHidden || r.Text != nil {
			t.Fatalf("followers embed must be blanked for non-follower: %+v", r)
		}
		// parent untouched.
		var p entity.NoteEntity
		_ = json.Unmarshal(out, &p)
		if p.Text == nil || *p.Text != "parent body" {
			t.Error("parent note must not be touched")
		}
	})

	t.Run("follower intact", func(t *testing.T) {
		payload := parentJSON(t, embed("followers", "author", nil))
		out := hideEmbedsForViewer(payload, viewer, map[string]bool{"author": true}, hideNowMs)
		if !bytes.Equal(out, payload) {
			t.Error("follower should see embed -> verbatim payload")
		}
	})

	t.Run("author intact", func(t *testing.T) {
		payload := parentJSON(t, embed("followers", "viewer", nil))
		out := hideEmbedsForViewer(payload, viewer, map[string]bool{}, hideNowMs)
		if !bytes.Equal(out, payload) {
			t.Error("own embed must stay visible -> verbatim")
		}
	})

	t.Run("anonymous blanked", func(t *testing.T) {
		payload := parentJSON(t, embed("followers", "author", nil))
		out := hideEmbedsForViewer(payload, nil, nil, hideNowMs)
		if r := renoteOf(t, out); r == nil || !r.IsHidden {
			t.Error("anonymous must not see followers embed")
		}
	})
}

func TestHideEmbedsForViewer_PublicVerbatim(t *testing.T) {
	payload := parentJSON(t, embed("public", "author", nil))
	out := hideEmbedsForViewer(payload, nil, nil, hideNowMs)
	if !bytes.Equal(out, payload) {
		t.Error("public embed must be forwarded verbatim even to anonymous")
	}
}

func TestHideEmbedsForViewer_Specified(t *testing.T) {
	viewer := &model.User{ID: "viewer"}
	t.Run("listed visible", func(t *testing.T) {
		payload := parentJSON(t, embed("specified", "author", []string{"viewer"}))
		out := hideEmbedsForViewer(payload, viewer, nil, hideNowMs)
		if !bytes.Equal(out, payload) {
			t.Error("specified embed listing viewer -> verbatim")
		}
	})
	t.Run("unlisted blanked", func(t *testing.T) {
		payload := parentJSON(t, embed("specified", "author", []string{"someone"}))
		out := hideEmbedsForViewer(payload, viewer, nil, hideNowMs)
		if r := renoteOf(t, out); r == nil || !r.IsHidden {
			t.Error("specified embed not listing viewer must be blanked")
		}
	})
}

func TestHideEmbedsForViewer_NoEmbedVerbatim(t *testing.T) {
	plain := entity.NoteEntity{ID: "p", UserID: "pa", CreatedAt: "2026-01-02T03:04:05.000Z", User: entity.UserLite{ID: "pa"}, Text: sptr("hi"), FileIDs: []string{}, Files: []any{}, VisibleUserIDs: []string{}, Mentions: []string{}}
	b, _ := json.Marshal(plain)
	out := hideEmbedsForViewer(b, nil, nil, hideNowMs)
	if !bytes.Equal(out, b) {
		t.Error("note without embed must be returned byte-for-byte")
	}
}

func TestHideEmbedsForViewer_MalformedReturnsOriginal(t *testing.T) {
	bad := []byte(`{not json`)
	out := hideEmbedsForViewer(bad, nil, nil, hideNowMs)
	if !bytes.Equal(out, bad) {
		t.Error("malformed payload must be returned unchanged")
	}
}

func TestHideEmbedsForViewer_InputBytesUnchanged(t *testing.T) {
	viewer := &model.User{ID: "viewer"}
	payload := parentJSON(t, embed("followers", "author", nil))
	cp := append([]byte(nil), payload...)
	_ = hideEmbedsForViewer(payload, viewer, map[string]bool{}, hideNowMs) // hide path
	if !bytes.Equal(payload, cp) {
		t.Error("hideEmbedsForViewer must never mutate the shared input buffer")
	}
}

// TestHideEmbedsForViewer_UnparseableCreatedAtFailsClosed mirrors the REST-side
// guard (#1567 review): an embed whose createdAt cannot be parsed must FAIL
// CLOSED on the absolute makeNotesHiddenBefore gate, even for a follower.
func TestHideEmbedsForViewer_UnparseableCreatedAtFailsClosed(t *testing.T) {
	viewer := &model.User{ID: "viewer"}
	hb := 1_600_000_000 // absolute epoch seconds, before hideNowMs
	e := entity.NoteEntity{
		ID: "embed", UserID: "author", CreatedAt: "not-a-timestamp",
		User: entity.UserLite{ID: "author", MakeNotesHiddenBefore: &hb}, Visibility: "public",
		Text: sptr("old secret"), FileIDs: []string{}, Files: []any{}, VisibleUserIDs: []string{}, Mentions: []string{},
	}
	payload := parentJSON(t, e)
	// snapshot says viewer follows author, isolating the time gate (public anyway).
	out := hideEmbedsForViewer(payload, viewer, map[string]bool{"author": true}, hideNowMs)
	if r := renoteOf(t, out); r == nil || !r.IsHidden {
		t.Error("unparseable createdAt must fail CLOSED on absolute makeNotesHiddenBefore")
	}
}
