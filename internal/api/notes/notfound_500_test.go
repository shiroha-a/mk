package notes

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// dbFailingNoteRepo makes every note lookup look like a database failure.
type dbFailingNoteRepo struct {
	*testutil.MockNoteRepository
	err error
}

func (r *dbFailingNoteRepo) FindByID(string) (*model.Note, error) { return nil, r.err }

// unrenote は renote 行を引くので、そちらも落とす。
func (r *dbFailingNoteRepo) FindRenoteByUser(string, string) (*model.Note, error) {
	return nil, r.err
}

// newDBFailingHandler builds a Handler whose note lookups all fail with a
// database error.
func newDBFailingHandler(t *testing.T, err error) *Handler {
	t.Helper()
	failing := &dbFailingNoteRepo{MockNoteRepository: testutil.NewMockNoteRepository(), err: err}
	pollRepo := testutil.NewMockPollRepository()
	idGen, _ := id.NewGenerator("aidx")
	return NewHandler(failing,
		corenote.NewCreateService(failing, pollRepo, idGen, nil),
		corenote.NewDeleteService(failing),
		corenote.NewQueryService(failing, nil),
		nil, nil, nil, nil, idGen)
}

// **DB 障害を「そんなノートは無い」にしない** (#2792)。
//
// note を引く endpoint をまとめて固定する。ここが無いと guard を巻き戻しても
// CI が緑のまま通る。
func TestNotes_DBFailureIsNot4xx(t *testing.T) {
	dbErr := errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
	me := &model.User{ID: "u1"}

	for _, tt := range []struct {
		name string
		run  func(*Handler) int
	}{
		{"notes/favorites/delete", func(h *Handler) int {
			return postExtra(h.FavoritesDelete, `{"noteId":"n1"}`, me).Code
		}},
		{"notes/unrenote", func(h *Handler) int {
			return postExtra(h.Unrenote, `{"noteId":"n1"}`, me).Code
		}},
		{"notes/thread-muting/create", func(h *Handler) int {
			return postExtra(h.ThreadMutingCreate, `{"noteId":"n1"}`, me).Code
		}},
		{"notes/thread-muting/delete", func(h *Handler) int {
			return postExtra(h.ThreadMutingDelete, `{"noteId":"n1"}`, me).Code
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, http.StatusInternalServerError, tt.run(newDBFailingHandler(t, dbErr)),
				"DB 障害が 4xx に化けている (#2792)")
		})
	}
}

// **DB 障害を「そんなノートは無い」にしない** (#2792)。
func TestTranslate_DBFailureIsNot4xx(t *testing.T) {
	// **Handler 構築時に repo を差し替える。** notes には後付けの setter が無い。
	failing := &dbFailingNoteRepo{
		MockNoteRepository: testutil.NewMockNoteRepository(),
		err:                errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"),
	}
	pollRepo := testutil.NewMockPollRepository()
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(failing,
		corenote.NewCreateService(failing, pollRepo, idGen, nil),
		corenote.NewDeleteService(failing),
		corenote.NewQueryService(failing, nil),
		nil, nil, nil, nil, idGen)

	rec := postExtra(h.Translate, `{"noteId":"n1","targetLang":"en"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"DB 障害が 4xx に化けている (#2792)")
}
