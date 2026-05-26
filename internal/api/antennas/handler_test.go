package antennas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	coreantenna "github.com/shiroha-a/mk/internal/core/antenna"
	"github.com/shiroha-a/mk/internal/entitycompat/shapetest"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testRedis *testutil.TestRedis
	idGen     id.Generator
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	tr, err := testutil.SetupRedis(ctx)
	if err != nil {
		log.Fatalf("redis setup failed: %v", err)
	}
	testRedis = tr
	idGen, _ = id.NewGenerator("aidx")
	code := m.Run()
	testRedis.Teardown(ctx)
	os.Exit(code)
}

func newHandler(t *testing.T) (*Handler, *testutil.MockAntennaRepository, *testutil.MockNoteRepository) {
	t.Helper()
	testRedis.FlushAll(context.Background())
	repo := testutil.NewMockAntennaRepository()
	noteRepo := testutil.NewMockNoteRepository()
	svc := coreantenna.NewService(repo, testutil.NewMockUserRepository(), testRedis.Client, idGen)
	return NewHandler(svc, noteRepo, idGen), repo, noteRepo
}

func newReq(t *testing.T, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func setUser(c echo.Context, userID string) {
	c.Set(string(middleware.UserContextKey), &model.User{ID: userID})
}

// --- Create ----------------------------------------------------------------

func TestCreate_Success(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"name":"alpha","src":"all","keywords":[["foo"]]}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestCreate_NoUserIdInResponse は #904 で fix した shape drift の
// regression guard。upstream Misskey TS の packAntenna は userId を
// embed しないので、mk-go も同 shape (= userId field なし) で揃える。
func TestCreate_NoUserIdInResponse(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"name":"alpha","src":"all","keywords":[["foo"]]}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, ok := body["userId"]
	assert.False(t, ok, "antennaToMap response must not include userId (#904)")
	// 必須 field の existence は念のため touch しておく (drift で吸い込ま
	// れないように reverse の guard)。
	assert.Equal(t, "alpha", body["name"])
	// misskey_dart の Antenna.fromJson は createdAt (非null String) /
	// hasUnreadNote (非null bool) を要求する (#1244)。
	createdAt, ok := body["createdAt"].(string)
	assert.True(t, ok, "createdAt must be a non-null string")
	assert.NotEmpty(t, createdAt)
	hasUnread, ok := body["hasUnreadNote"].(bool)
	assert.True(t, ok, "hasUnreadNote must be a non-null bool")
	assert.False(t, hasUnread)
	// L3 (#1270): antennas/create の実レスポンスを golden Antenna に突合する
	// (上の #1244 手動チェックを gate 化して全 field をカバー)。
	shapetest.Assert(t, "Antenna", body)
}

func TestCreate_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreate_NameRequired(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreate_InvalidSource(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"name":"alpha","src":"bogus"}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// failingAntennaRepo returns errors on Create.
type failingAntennaRepo struct {
	*testutil.MockAntennaRepository
}

func (r *failingAntennaRepo) Create(_ *model.Antenna) error { return errors.New("boom") }

func TestCreate_RepoError(t *testing.T) {
	mock := testutil.NewMockAntennaRepository()
	repo := &failingAntennaRepo{MockAntennaRepository: mock}
	svc := coreantenna.NewService(repo, testutil.NewMockUserRepository(), testRedis.Client, idGen)
	h := NewHandler(svc, testutil.NewMockNoteRepository(), idGen)
	c, rec := newReq(t, `{"name":"alpha","src":"all"}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Show ------------------------------------------------------------------

func TestShow_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice", Name: "alpha"}
	c, rec := newReq(t, `{"antennaId":"a1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestShow_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "alice")
	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestShow_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"antennaId":"missing"}`)
	setUser(c, "alice")
	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestShow_AccessDenied(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "bob"}
	c, rec := newReq(t, `{"antennaId":"a1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// --- Update ----------------------------------------------------------------

func TestUpdate_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice", Name: "alpha"}
	c, rec := newReq(t, `{"antennaId":"a1","name":"alpha-v2"}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdate_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdate_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"antennaId":"missing"}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUpdate_AccessDenied(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "bob"}
	c, rec := newReq(t, `{"antennaId":"a1","name":"x"}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestUpdate_NameEmpty(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice"}
	c, rec := newReq(t, `{"antennaId":"a1","name":""}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdate_InvalidSource(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice"}
	c, rec := newReq(t, `{"antennaId":"a1","src":"bogus"}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// failingUpdateRepo causes UpdateFields to fail.
type failingUpdateRepo struct {
	*testutil.MockAntennaRepository
}

func (r *failingUpdateRepo) UpdateFields(_ string, _ map[string]any) error {
	return errors.New("boom")
}

func TestUpdate_RepoError(t *testing.T) {
	mock := testutil.NewMockAntennaRepository()
	mock.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice"}
	repo := &failingUpdateRepo{MockAntennaRepository: mock}
	svc := coreantenna.NewService(repo, testutil.NewMockUserRepository(), testRedis.Client, idGen)
	h := NewHandler(svc, testutil.NewMockNoteRepository(), idGen)
	c, rec := newReq(t, `{"antennaId":"a1","name":"x"}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Delete ----------------------------------------------------------------

func TestDelete_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice"}
	c, rec := newReq(t, `{"antennaId":"a1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestDelete_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDelete_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"antennaId":"missing"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestDelete_AccessDenied(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "bob"}
	c, rec := newReq(t, `{"antennaId":"a1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// failingDeleteRepo causes Delete to fail.
type failingDeleteRepo struct {
	*testutil.MockAntennaRepository
}

func (r *failingDeleteRepo) Delete(_ *model.Antenna) error { return errors.New("boom") }

func TestDelete_RepoError(t *testing.T) {
	mock := testutil.NewMockAntennaRepository()
	mock.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice"}
	repo := &failingDeleteRepo{MockAntennaRepository: mock}
	svc := coreantenna.NewService(repo, testutil.NewMockUserRepository(), testRedis.Client, idGen)
	h := NewHandler(svc, testutil.NewMockNoteRepository(), idGen)
	c, rec := newReq(t, `{"antennaId":"a1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Delete(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- List ------------------------------------------------------------------

func TestList_Success(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice", Name: "alpha"}
	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "alpha")
}

// listFailRepo causes ListByUser to fail.
type listFailRepo struct {
	*testutil.MockAntennaRepository
}

func (r *listFailRepo) ListByUser(_ string) ([]*model.Antenna, error) {
	return nil, errors.New("boom")
}

func TestList_RepoError(t *testing.T) {
	repo := &listFailRepo{MockAntennaRepository: testutil.NewMockAntennaRepository()}
	svc := coreantenna.NewService(repo, testutil.NewMockUserRepository(), testRedis.Client, idGen)
	h := NewHandler(svc, testutil.NewMockNoteRepository(), idGen)
	c, rec := newReq(t, `{}`)
	setUser(c, "alice")
	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Notes -----------------------------------------------------------------

func TestNotes_Success(t *testing.T) {
	h, repo, noteRepo := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "alice"}

	// Push a note id directly into the antenna's stream so Notes returns it.
	ctx := context.Background()
	require.NoError(t, testRedis.Client.XAdd(ctx, &redis.XAddArgs{
		Stream: "antennaTimeline:a1",
		ID:     "1-0",
		Values: map[string]any{"noteId": "n1"},
	}).Err())

	c, rec := newReq(t, `{"antennaId":"a1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Notes(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "n1")
}

func TestNotes_BadJSON(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{not`)
	setUser(c, "alice")
	require.NoError(t, h.Notes(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNotes_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"antennaId":"missing"}`)
	setUser(c, "alice")
	require.NoError(t, h.Notes(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNotes_AccessDenied(t *testing.T) {
	h, repo, _ := newHandler(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "bob"}
	c, rec := newReq(t, `{"antennaId":"a1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Notes(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNotes_RedisError(t *testing.T) {
	repo := testutil.NewMockAntennaRepository()
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice"}
	closed := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	_ = closed.Close()
	svc := coreantenna.NewService(repo, testutil.NewMockUserRepository(), closed, idGen)
	h := NewHandler(svc, testutil.NewMockNoteRepository(), idGen)
	c, rec := newReq(t, `{"antennaId":"a1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Notes(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// failingNoteRepo causes FindManyByIDsWithUser to fail.
type failingNoteRepo struct {
	*testutil.MockNoteRepository
}

func (r *failingNoteRepo) FindManyByIDsWithUser(_ []string) ([]*model.Note, error) {
	return nil, errors.New("boom")
}

// #693: handler が untilId / sinceId を service 層に渡し、結果として
// stream の paging が効くこと。strictly older / newer な区切りで挙動が変わる
// ことを e2e (redis 経由) で確認する。
func TestNotes_PagingUntilId(t *testing.T) {
	h, repo, noteRepo := newHandler(t)
	// stream ID 衝突を避けるため antenna ID を test 専用に分ける。
	repo.Antennas["a693u"] = &model.Antenna{ID: "a693u", UserID: "alice"}

	t1 := time.Now()
	idA := idGen.Generate(t1)
	idB := idGen.Generate(t1.Add(time.Millisecond))
	idC := idGen.Generate(t1.Add(2 * time.Millisecond))
	entries := []struct {
		noteID string
		ms     int64
	}{
		{idA, t1.UnixMilli()},
		{idB, t1.Add(time.Millisecond).UnixMilli()},
		{idC, t1.Add(2 * time.Millisecond).UnixMilli()},
	}
	for _, e := range entries {
		require.NoError(t, testRedis.Client.XAdd(context.Background(), &redis.XAddArgs{
			Stream: "antennaTimeline:a693u",
			ID:     fmt.Sprintf("%d-0", e.ms),
			Values: map[string]any{"noteId": e.noteID},
		}).Err())
		noteRepo.Notes[e.noteID] = &model.Note{ID: e.noteID, UserID: "alice"}
	}

	// untilId = idC → strictly older
	body := fmt.Sprintf(`{"antennaId":"a693u","untilId":%q}`, idC)
	c, rec := newReq(t, body)
	setUser(c, "alice")
	require.NoError(t, h.Notes(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), idA)
	assert.Contains(t, rec.Body.String(), idB)
	assert.NotContains(t, rec.Body.String(), idC, "untilId 自身は含まれてはいけない")
}

func TestNotes_NoteRepoError(t *testing.T) {
	repo := testutil.NewMockAntennaRepository()
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "alice"}
	svc := coreantenna.NewService(repo, testutil.NewMockUserRepository(), testRedis.Client, idGen)
	noteRepo := &failingNoteRepo{MockNoteRepository: testutil.NewMockNoteRepository()}
	h := NewHandler(svc, noteRepo, idGen)
	c, rec := newReq(t, `{"antennaId":"a1"}`)
	setUser(c, "alice")
	require.NoError(t, h.Notes(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
