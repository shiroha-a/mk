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
