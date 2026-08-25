package federation_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/model"
)

// 列に入らない id の Announce は renote を作らないこと (#2723)。
//
// `note.uri` は varchar(512)。Announce の id は renote の身元で、**同じ activity の
// 重複検出 (`FindByURI`) の鍵**でもあるので切らずに拒否する。切ると別の行と衝突する。
//
// (Undo(Announce) はこの URI を引かない — `ListRenotesOf` で announcer の renote を
// 探す。)
func TestProcess_Announce_RejectsOversizedURI(t *testing.T) {
	env := newFullProcessor(t, aliceActor)
	env.noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	env.userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	before := len(env.noteRepo.Notes)

	body := []byte(`{
		"type": "Announce",
		"id": "https://remote.example/announces/` + strings.Repeat("a", 512) + `",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/n1"
	}`)
	err := env.processor.Process(body)
	require.Error(t, err, "列に入らない id の Announce を受理している")
	// error は上位に surface する (inbox job は retry を使い切って dead になる)。
	// ここで確かめるのは renote を作らないことまで。
	assert.ErrorIs(t, err, federation.ErrInvalidNote)
	assert.Len(t, env.noteRepo.Notes, before, "renote が作られている")
}

// ちょうど列に収まる id は受理すること (境界を 1 つずらす実装を弾く)。
func TestProcess_Announce_AcceptsURIAtColumnLimit(t *testing.T) {
	env := newFullProcessor(t, aliceActor)
	env.noteRepo.Notes["n1"] = &model.Note{
		ID: "n1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	env.userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bob"}
	before := len(env.noteRepo.Notes)

	prefix := "https://remote.example/announces/"
	// 512 は migration の列長そのもの。定数を参照すると両側が一緒に動く。
	id := prefix + strings.Repeat("a", 512-len(prefix))
	require.Len(t, []rune(id), 512)
	body := []byte(`{
		"type": "Announce",
		"id": "` + id + `",
		"actor": "https://remote.example/users/alice",
		"object": "https://example.com/notes/n1"
	}`)
	require.NoError(t, env.processor.Process(body))
	assert.Len(t, env.noteRepo.Notes, before+1, "renote が作られていない")
}
