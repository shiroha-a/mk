package webpush

import (
	"encoding/json"

	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// NoteRepoPacker adapts (NoteRepository + id.Generator) into the
// notification.NotePacker interface. The adapter re-uses entity.PackNote so
// that the Web Push payload matches the shape returned by /api/i/notifications.
type NoteRepoPacker struct {
	repo          repository.NoteRepository
	idGen         id.Generator
	followingRepo repository.FollowingRepository
}

// NewNoteRepoPacker constructs a NoteRepoPacker. followingRepo is used to gate
// the embedded note by the push recipient's visibility (#1572); a nil repo
// fails closed (followers notes hidden from non-author recipients).
func NewNoteRepoPacker(repo repository.NoteRepository, idGen id.Generator, followingRepo repository.FollowingRepository) *NoteRepoPacker {
	return &NoteRepoPacker{repo: repo, idGen: idGen, followingRepo: followingRepo}
}

// PackNoteByID implements notification.NotePacker. viewerID is the push
// recipient (notifiee); the note is gated by their visibility before packing,
// mirroring REST i/notifications (FilterVisible, #1444) and the stream
// notification path (noteVisibleToNotifiee, #1471): when the recipient cannot
// see the note, return (nil, false) so the caller omits the note detail (the
// notification keeps its noteId). Web Push previously packed the note with no
// gate at all (#1572 IDOR). followingRepo unwired / blank viewerID fails closed
// for followers / specified notes; public / home always pass.
func (p *NoteRepoPacker) PackNoteByID(noteID, viewerID string) (map[string]any, bool) {
	if p == nil || p.repo == nil {
		return nil, false
	}
	n, err := p.repo.FindByIDWithRelations(noteID)
	if err != nil {
		return nil, false
	}
	var viewer *model.User
	if viewerID != "" {
		viewer = &model.User{ID: viewerID}
	}
	if !corenote.CanSeeNote(viewer, n, p.followingRepo) {
		return nil, false
	}
	return toMap(entity.PackNote(n, p.idGen))
}

// UserRepoPacker adapts UserRepository into notification.UserPacker using
// entity.PackUserLite.
type UserRepoPacker struct {
	repo repository.UserRepository
}

// NewUserRepoPacker constructs a UserRepoPacker.
func NewUserRepoPacker(repo repository.UserRepository) *UserRepoPacker {
	return &UserRepoPacker{repo: repo}
}

// PackUserByID implements notification.UserPacker.
func (p *UserRepoPacker) PackUserByID(userID string) (map[string]any, bool) {
	if p == nil || p.repo == nil {
		return nil, false
	}
	u, err := p.repo.FindByID(userID)
	if err != nil {
		return nil, false
	}
	return toMap(entity.PackUserLite(u))
}

// toMap round-trips any JSON-serializable value into a map. Used because the
// notification hook expects a map[string]any and the entity helpers return
// concrete structs.
func toMap(v any) (map[string]any, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}
