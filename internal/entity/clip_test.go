package entity

import (
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestPackClip_Basic(t *testing.T) {
	idGen := newTestIDGen(t)
	clipID := idGen.Generate(time.Now())
	desc := "my clip"
	owner := &model.User{ID: "u1", Username: "alice"}
	lastClipped := time.Date(2026, 4, 10, 1, 2, 3, 0, time.UTC)
	cl := &model.Clip{ID: clipID, UserID: "u1", Name: "favs", Description: &desc, IsPublic: true, NotesCount: 5, LastClippedAt: &lastClipped}

	out := PackClip(cl, idGen, owner)
	assert.Equal(t, clipID, out["id"])
	assert.Equal(t, "u1", out["userId"])
	assert.Equal(t, "favs", out["name"])
	assert.Equal(t, &desc, out["description"])
	assert.Equal(t, true, out["isPublic"])
	assert.Equal(t, 5, out["notesCount"])
	// lastClippedAt は ISO ms 形式 (createdAt と統一)。
	assert.Equal(t, "2026-04-10T01:02:03.000Z", out["lastClippedAt"])
	// misskey_dart の Clip.fromJson が非null必須とする field (#1237)。
	createdAt, ok := out["createdAt"].(string)
	assert.True(t, ok, "createdAt must be a non-null string")
	assert.NotEmpty(t, createdAt)
	assert.Equal(t, 0, out["favoritedCount"])
	user, ok := out["user"].(UserLite)
	assert.True(t, ok, "user must be present")
	assert.Equal(t, "u1", user.ID)
}

func TestPackClip_NilOwnerOmitsUser(t *testing.T) {
	idGen := newTestIDGen(t)
	cl := &model.Clip{ID: idGen.Generate(time.Now()), UserID: "u1", Name: "x"}
	out := PackClip(cl, idGen, nil)
	_, hasUser := out["user"]
	assert.False(t, hasUser)
}

func TestPackClip_NilInput(t *testing.T) {
	assert.Nil(t, PackClip(nil, nil, nil))
}
