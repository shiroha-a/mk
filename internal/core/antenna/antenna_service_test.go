package antenna

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
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

func newSvc(t *testing.T) (*Service, *testutil.MockAntennaRepository) {
	t.Helper()
	testRedis.FlushAll(context.Background())
	repo := testutil.NewMockAntennaRepository()
	return NewService(repo, testutil.NewMockUserRepository(), testRedis.Client, idGen), repo
}

func closedClient(t *testing.T) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	_ = c.Close()
	return c
}

// --- Create ----------------------------------------------------------------

func TestCreate_HappyPath(t *testing.T) {
	svc, repo := newSvc(t)
	a, err := svc.Create(CreateInput{
		OwnerID:  "u1",
		Name:     "alpha",
		Src:      model.AntennaSourceAll,
		Keywords: [][]string{{"misskey"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "alpha", a.Name)
	assert.Equal(t, model.AntennaSourceAll, a.Src)
	assert.Len(t, repo.Antennas, 1)
}

func TestCreate_NameRequired(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.Create(CreateInput{OwnerID: "u1", Src: model.AntennaSourceAll})
	assert.ErrorIs(t, err, ErrAntennaNameRequired)
}

func TestCreate_OwnerRequired(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.Create(CreateInput{Name: "alpha", Src: model.AntennaSourceAll})
	assert.Error(t, err)
}

func TestCreate_InvalidSource(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.Create(CreateInput{OwnerID: "u1", Name: "alpha", Src: "bogus"})
	assert.ErrorIs(t, err, ErrInvalidSource)
}

func TestCreate_ListSourceAccepted(t *testing.T) {
	// #170 以降 list ソースも正規値として扱う (validSource に追加済み)。
	svc, _ := newSvc(t)
	listID := "ul1"
	_, err := svc.Create(CreateInput{OwnerID: "u1", Name: "alpha", Src: model.AntennaSourceList, UserListID: &listID})
	require.NoError(t, err)
}

func TestAllKeywordsEmpty(t *testing.T) {
	assert.True(t, AllKeywordsEmpty(nil))
	assert.True(t, AllKeywordsEmpty([][]string{}))
	assert.True(t, AllKeywordsEmpty([][]string{{""}, {"", ""}}))
	assert.False(t, AllKeywordsEmpty([][]string{{""}, {"foo"}}))
	assert.False(t, AllKeywordsEmpty([][]string{{"bar"}}))
}

// src=list で他人所有 / 不在の list を参照すると ErrNoSuchUserList。
func TestCreate_ListSource_NoSuchUserList(t *testing.T) {
	svc, _ := newSvc(t)
	lists := testutil.NewMockUserListRepository()
	svc.SetUserListRepo(lists)
	require.NoError(t, lists.Create(&model.UserList{ID: "ul1", UserID: "other", Name: "theirs"}))

	// 他人所有の list。
	notMine := "ul1"
	_, err := svc.Create(CreateInput{OwnerID: "u1", Name: "alpha", Src: model.AntennaSourceList, UserListID: &notMine})
	assert.ErrorIs(t, err, ErrNoSuchUserList)

	// 存在しない list。
	ghost := "ghost"
	_, err = svc.Create(CreateInput{OwnerID: "u1", Name: "alpha", Src: model.AntennaSourceList, UserListID: &ghost})
	assert.ErrorIs(t, err, ErrNoSuchUserList)
}

func TestCreate_RepoError(t *testing.T) {
	svc, repo := newSvc(t)
	repo.CreateErr = errors.New("boom")
	_, err := svc.Create(CreateInput{OwnerID: "u1", Name: "alpha", Src: model.AntennaSourceAll})
	assert.Error(t, err)
}

// --- Show ------------------------------------------------------------------

func TestShow_HappyPath(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1", Name: "alpha"}
	got, err := svc.Show("u1", "a1")
	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Name)
}

func TestShow_NotFound(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.Show("u1", "missing")
	assert.ErrorIs(t, err, ErrAntennaNotFound)
}

func TestShow_AccessDenied(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	_, err := svc.Show("u2", "a1")
	assert.ErrorIs(t, err, ErrAccessDenied)
}

func TestShow_DoesNotBumpLastUsedAt(t *testing.T) {
	svc, repo := newSvc(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1", LastUsedAt: old, IsActive: false}
	// 本家 show.ts は lastUsedAt を bump しない (#1604)。
	_, err := svc.Show("u1", "a1")
	require.NoError(t, err)
	assert.Equal(t, old, repo.Antennas["a1"].LastUsedAt, "Show は lastUsedAt を変えない")
	assert.False(t, repo.Antennas["a1"].IsActive, "Show は再活性化しない")
}

// --- Update ----------------------------------------------------------------

func TestUpdate_HappyPath(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{
		ID: "a1", UserID: "u1", Name: "alpha", Src: model.AntennaSourceAll,
		Keywords: datatypes.JSON([]byte("[]")), ExcludeKeywords: datatypes.JSON([]byte("[]")),
	}
	newName := "alpha-v2"
	src := model.AntennaSourceUsers
	users := []string{"alice"}
	keywords := [][]string{{"foo"}}
	exclude := [][]string{{"bar"}}
	caseS := true
	exB := true
	wR := true
	wF := true
	lo := true
	active := false
	got, err := svc.Update("u1", "a1", UpdateInput{
		Name:            &newName,
		Src:             &src,
		Users:           &users,
		Keywords:        &keywords,
		ExcludeKeywords: &exclude,
		CaseSensitive:   &caseS,
		ExcludeBots:     &exB,
		WithReplies:     &wR,
		WithFile:        &wF,
		LocalOnly:       &lo,
		IsActive:        &active,
	})
	require.NoError(t, err)
	assert.Equal(t, "alpha-v2", got.Name)
}

func TestUpdate_BumpsLastUsedAt(t *testing.T) {
	svc, repo := newSvc(t)
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	svc.SetClock(func() time.Time { return fixed })
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.Antennas["a1"] = &model.Antenna{
		ID: "a1", UserID: "u1", Name: "alpha", Src: model.AntennaSourceAll, LastUsedAt: old,
		Keywords: datatypes.JSON([]byte("[]")), ExcludeKeywords: datatypes.JSON([]byte("[]")),
	}
	name := "alpha-v2"
	repo.Antennas["a1"].IsActive = false
	// 本家 update.ts と同じく更新で lastUsedAt が now に bump され、isActive を明示
	// 指定しない編集では isActive=true へ復帰する (#1604)。
	_, err := svc.Update("u1", "a1", UpdateInput{Name: &name})
	require.NoError(t, err)
	assert.Equal(t, fixed, repo.Antennas["a1"].LastUsedAt)
	assert.True(t, repo.Antennas["a1"].IsActive, "isActive 未指定の編集は再活性化")
}

func TestUpdate_RespectsExplicitIsActiveFalse(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{
		ID: "a1", UserID: "u1", Name: "alpha", Src: model.AntennaSourceAll, IsActive: true,
		Keywords: datatypes.JSON([]byte("[]")), ExcludeKeywords: datatypes.JSON([]byte("[]")),
	}
	off := false
	// 明示的な isActive=false は尊重する (mk-go の isActive param 拡張)。
	_, err := svc.Update("u1", "a1", UpdateInput{IsActive: &off})
	require.NoError(t, err)
	assert.False(t, repo.Antennas["a1"].IsActive, "明示 isActive=false は尊重")
}

func TestUpdate_NotFound(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.Update("u1", "missing", UpdateInput{})
	assert.ErrorIs(t, err, ErrAntennaNotFound)
}

func TestUpdate_AccessDenied(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "other"}
	_, err := svc.Update("u1", "a1", UpdateInput{})
	assert.ErrorIs(t, err, ErrAccessDenied)
}

func TestUpdate_NameEmpty(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	empty := ""
	_, err := svc.Update("u1", "a1", UpdateInput{Name: &empty})
	assert.ErrorIs(t, err, ErrAntennaNameRequired)
}

func TestUpdate_InvalidSource(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	bogus := model.AntennaSource("bogus")
	_, err := svc.Update("u1", "a1", UpdateInput{Src: &bogus})
	assert.ErrorIs(t, err, ErrInvalidSource)
}

// Update で empty Users を渡しても、UpdateFields に pq.StringArray
// として渡されることを担保する (#896 と同 pattern)。plain []string で
// 渡すと production の GORM 経由で NULL 化して NOT NULL 制約違反になる。
func TestUpdate_EmptyUsersWrappedAsStringArray(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	emptyUsers := []string{}
	_, err := svc.Update("u1", "a1", UpdateInput{Users: &emptyUsers})
	require.NoError(t, err)
	require.Contains(t, repo.LastUpdates, "users")
	v, ok := repo.LastUpdates["users"].(pq.StringArray)
	require.True(t, ok, "users should be pq.StringArray, got %T", repo.LastUpdates["users"])
	assert.Empty(t, v)
}

func TestUpdate_RepoError(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	repo.UpdateErr = errors.New("boom")
	name := "x"
	_, err := svc.Update("u1", "a1", UpdateInput{Name: &name})
	assert.Error(t, err)
}

// --- Delete ----------------------------------------------------------------

func TestDelete_HappyPath(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	require.NoError(t, svc.Delete("u1", "a1"))
	assert.Empty(t, repo.Antennas)
}

func TestDelete_NotFound(t *testing.T) {
	svc, _ := newSvc(t)
	err := svc.Delete("u1", "missing")
	assert.ErrorIs(t, err, ErrAntennaNotFound)
}

func TestDelete_AccessDenied(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "other"}
	err := svc.Delete("u1", "a1")
	assert.ErrorIs(t, err, ErrAccessDenied)
}

// failingDeleteRepo causes Delete to fail.
type failingDeleteRepo struct {
	*testutil.MockAntennaRepository
}

func (r *failingDeleteRepo) Delete(_ *model.Antenna) error { return errors.New("boom") }

func TestDelete_RepoError(t *testing.T) {
	mock := testutil.NewMockAntennaRepository()
	mock.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	repo := &failingDeleteRepo{MockAntennaRepository: mock}
	svc := NewService(repo, testutil.NewMockUserRepository(), testRedis.Client, idGen)
	err := svc.Delete("u1", "a1")
	assert.Error(t, err)
}

// --- ListByUser ------------------------------------------------------------

func TestListByUser(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	repo.Antennas["a2"] = &model.Antenna{ID: "a2", UserID: "u1"}
	rows, err := svc.ListByUser("u1")
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

// --- Notes -----------------------------------------------------------------

func TestNotes_HappyPath(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	require.NoError(t, svc.pushNote(context.Background(), "a1", "n1", time.Now()))
	require.NoError(t, svc.pushNote(context.Background(), "a1", "n2", time.Now().Add(time.Millisecond)))
	rows, err := svc.Notes(context.Background(), "u1", "a1", 10, "", "")
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestNotes_BumpsLastUsedAtAndReactivates(t *testing.T) {
	svc, repo := newSvc(t)
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	svc.SetClock(func() time.Time { return fixed })
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	// 非アクティブ + 古い lastUsedAt の antenna を notes 取得すると、本家
	// antennas/notes.ts と同じく isActive=true + lastUsedAt=now に bump される (#1604)。
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1", IsActive: false, LastUsedAt: old}
	_, err := svc.Notes(context.Background(), "u1", "a1", 10, "", "")
	require.NoError(t, err)
	assert.True(t, repo.Antennas["a1"].IsActive, "notes 取得で再活性化")
	assert.Equal(t, fixed, repo.Antennas["a1"].LastUsedAt, "notes 取得で lastUsedAt が now に bump")
}

func TestNotes_LimitClamping(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	rows, err := svc.Notes(context.Background(), "u1", "a1", -1, "", "")
	require.NoError(t, err)
	assert.Empty(t, rows)
	rows, err = svc.Notes(context.Background(), "u1", "a1", 9999, "", "")
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestNotes_NotFound(t *testing.T) {
	svc, _ := newSvc(t)
	_, err := svc.Notes(context.Background(), "u1", "missing", 10, "", "")
	assert.ErrorIs(t, err, ErrAntennaNotFound)
}

func TestNotes_RedisError(t *testing.T) {
	repo := testutil.NewMockAntennaRepository()
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	svc := NewService(repo, testutil.NewMockUserRepository(), closedClient(t), idGen)
	_, err := svc.Notes(context.Background(), "u1", "a1", 10, "", "")
	assert.Error(t, err)
}

// #693: untilID で渡した noteID より strictly old なエントリだけ返ること。
// FE の無限スクロールが「次のページ」を取りに来た時にこれが効かないと、
// 同じ最新 N 件を毎回返してしまう。
func TestNotes_PagingUntilID(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	// 3 件: 古→新の順 (idGen.Generate は時間 monotonic)
	t1 := time.Now()
	id1 := idGen.Generate(t1)
	id2 := idGen.Generate(t1.Add(time.Millisecond))
	id3 := idGen.Generate(t1.Add(2 * time.Millisecond))
	require.NoError(t, svc.pushNote(context.Background(), "a1", id1, t1))
	require.NoError(t, svc.pushNote(context.Background(), "a1", id2, t1.Add(time.Millisecond)))
	require.NoError(t, svc.pushNote(context.Background(), "a1", id3, t1.Add(2*time.Millisecond)))

	// untilID = id3 → strictly older だけ → id2, id1 (newest first)
	rows, err := svc.Notes(context.Background(), "u1", "a1", 10, "", id3)
	require.NoError(t, err)
	assert.Equal(t, []string{id2, id1}, rows)

	// untilID = id1 (= 一番古い) → さらに古いものは無いので空
	rows, err = svc.Notes(context.Background(), "u1", "a1", 10, "", id1)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// #693: sinceID で渡した noteID より strictly new なエントリだけ返ること。
func TestNotes_PagingSinceID(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	t1 := time.Now()
	id1 := idGen.Generate(t1)
	id2 := idGen.Generate(t1.Add(time.Millisecond))
	id3 := idGen.Generate(t1.Add(2 * time.Millisecond))
	require.NoError(t, svc.pushNote(context.Background(), "a1", id1, t1))
	require.NoError(t, svc.pushNote(context.Background(), "a1", id2, t1.Add(time.Millisecond)))
	require.NoError(t, svc.pushNote(context.Background(), "a1", id3, t1.Add(2*time.Millisecond)))

	// #1778: sinceID 単独 (fetch-newer) は昇順 (oldest-first) で返す
	// (upstream notes.ts の sinceId-only ascending sort)。strictly newer の
	// 最古から id2, id3 の順。
	rows, err := svc.Notes(context.Background(), "u1", "a1", 10, id1, "")
	require.NoError(t, err)
	assert.Equal(t, []string{id2, id3}, rows)

	// limit を絞ると sinceID 直後の最古 limit 件 (id2) を返す (newest ではなく)。
	rows, err = svc.Notes(context.Background(), "u1", "a1", 1, id1, "")
	require.NoError(t, err)
	assert.Equal(t, []string{id2}, rows)
}

// #693 review #1: 同一 ms に同じアンテナへ複数回 pushNote しても XADD が
// 失敗せず、両方が stream に格納されて一覧取得できる (旧 `<ms>-0` 固定実装は
// monotonic 違反で 2 件目を drop していた)。
func TestPushNote_SameMsDoesNotCollide(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	now := time.Now()
	require.NoError(t, svc.pushNote(context.Background(), "a1", "n1", now))
	require.NoError(t, svc.pushNote(context.Background(), "a1", "n2", now), "同 ms でも 2 件目が成功すること")
	rows, err := svc.Notes(context.Background(), "u1", "a1", 10, "", "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"n1", "n2"}, rows)
}

// #693: 不正な ID (ParseTime 失敗) は無視されて全範囲が返る (安全側 fallback)。
func TestNotes_PagingInvalidID(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	require.NoError(t, svc.pushNote(context.Background(), "a1", "n1", time.Now()))
	rows, err := svc.Notes(context.Background(), "u1", "a1", 10, "", "not-an-id")
	require.NoError(t, err)
	assert.Len(t, rows, 1, "ParseTime 失敗時は全 range にフォールバックして既存挙動を維持")
}

func TestNotes_SkipsBadValue(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	// noteId フィールドが無いエントリを混ぜる
	require.NoError(t, testRedis.Client.XAdd(context.Background(), &redis.XAddArgs{
		Stream: streamKey("a1"),
		ID:     "1-0",
		Values: map[string]any{"other": "x"},
	}).Err())
	rows, err := svc.Notes(context.Background(), "u1", "a1", 10, "", "")
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// --- OnNoteCreated + matchNote --------------------------------------------

func makeAntenna(t *testing.T, id, userID string, kw [][]string, mods ...func(*model.Antenna)) *model.Antenna {
	t.Helper()
	keywordsJSON := []byte("[]")
	if len(kw) > 0 {
		raw, err := json.Marshal(kw)
		require.NoError(t, err)
		keywordsJSON = raw
	}
	a := &model.Antenna{
		ID:              id,
		UserID:          userID,
		Name:            id,
		Src:             model.AntennaSourceAll,
		Keywords:        datatypes.JSON(keywordsJSON),
		ExcludeKeywords: datatypes.JSON([]byte("[]")),
		IsActive:        true,
	}
	for _, m := range mods {
		m(a)
	}
	return a
}

func TestOnNoteCreated_HappyPath(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", [][]string{{"misskey"}})

	text := "hello misskey world"
	n := &model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityPublic}
	author := &model.User{ID: "author", Username: "alice"}
	svc.OnNoteCreated(n, author)

	rows, err := svc.Notes(context.Background(), "u1", "a1", 10, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"n1"}, rows)
}

func TestOnNoteCreated_InsertsUnreadRow(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "owner", [][]string{{"hit"}})
	unread := testutil.NewMockAntennaNoteUnreadRepository()
	svc.SetUnreadRepo(unread)

	text := "hit this"
	svc.OnNoteCreated(
		&model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "author", Username: "a"},
	)
	// 1 row should be inserted for antenna owner (not the author)
	require.Len(t, unread.Rows, 1)
	assert.Equal(t, "owner", unread.Rows[0].UserID)
	assert.Equal(t, "a1", unread.Rows[0].AntennaID)
	assert.Equal(t, "n1", unread.Rows[0].NoteID)
}

func TestOnNoteCreated_SelfAuthoredSkipsUnread(t *testing.T) {
	svc, repo := newSvc(t)
	// antenna owner == note author → unread を出さない
	repo.Antennas["a1"] = makeAntenna(t, "a1", "author", [][]string{{"hit"}})
	unread := testutil.NewMockAntennaNoteUnreadRepository()
	svc.SetUnreadRepo(unread)

	text := "hit this"
	svc.OnNoteCreated(
		&model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "author", Username: "a"},
	)
	assert.Empty(t, unread.Rows)
}

func TestOnNoteCreated_NilArgsAreNoOp(t *testing.T) {
	svc, _ := newSvc(t)
	svc.OnNoteCreated(nil, &model.User{})
	svc.OnNoteCreated(&model.Note{Visibility: model.NoteVisibilityPublic}, nil)
}

func TestOnNoteCreated_RepoErrorIsNoOp(t *testing.T) {
	repo := testutil.NewMockAntennaRepository()
	svc := NewService(&listFailRepo{repo}, testutil.NewMockUserRepository(), testRedis.Client, idGen)
	text := "hi"
	svc.OnNoteCreated(&model.Note{ID: "n1", Text: &text, Visibility: model.NoteVisibilityPublic}, &model.User{ID: "u1"})
}

// listFailRepo causes ListAllActive to fail.
type listFailRepo struct {
	*testutil.MockAntennaRepository
}

func (r *listFailRepo) ListAllActive() ([]*model.Antenna, error) {
	return nil, errors.New("boom")
}

func TestOnNoteCreated_NoMatchSkipped(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", [][]string{{"missing"}})

	text := "hello world"
	svc.OnNoteCreated(
		&model.Note{ID: "n1", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "author", Username: "alice"},
	)

	rows, err := svc.Notes(context.Background(), "u1", "a1", 10, "", "")
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// --- matchNote each filter ------------------------------------------------

func TestMatchNote_LocalOnlyRejectsRemote(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil, func(a *model.Antenna) {
		a.LocalOnly = true
	})
	host := "remote.example"
	require.False(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Visibility: model.NoteVisibilityPublic}, &model.User{Host: &host}))
}

func TestMatchNote_ExcludeBotsRejectsBot(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil, func(a *model.Antenna) {
		a.ExcludeBots = true
	})
	require.False(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Visibility: model.NoteVisibilityPublic}, &model.User{IsBot: true}))
}

func TestMatchNote_WithFileRequiresAttachment(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil, func(a *model.Antenna) {
		a.WithFile = true
	})
	require.False(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Visibility: model.NoteVisibilityPublic}, &model.User{}))
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{FileIDs: []string{"f1"}, Visibility: model.NoteVisibilityPublic}, &model.User{}))
}

func TestMatchNote_WithRepliesFalseRejectsReplies(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil)
	parent := "p1"
	require.False(t, svc.matchNote(repo.Antennas["a1"], &model.Note{ReplyID: &parent, Visibility: model.NoteVisibilityPublic}, &model.User{}))
}

func TestMatchNote_WithRepliesTrueAllowsReplies(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil, func(a *model.Antenna) {
		a.WithReplies = true
	})
	parent := "p1"
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{ReplyID: &parent, Visibility: model.NoteVisibilityPublic}, &model.User{}))
}

func TestMatchNote_UsersSourceWhitelist(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil, func(a *model.Antenna) {
		a.Src = model.AntennaSourceUsers
		a.Users = []string{"alice"}
	})
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Visibility: model.NoteVisibilityPublic}, &model.User{Username: "alice"}))
	require.False(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Visibility: model.NoteVisibilityPublic}, &model.User{Username: "bob"}))
}

func TestMatchNote_UsersBlacklist(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil, func(a *model.Antenna) {
		a.Src = model.AntennaSourceUsersBlacklist
		a.Users = []string{"alice"}
	})
	require.False(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Visibility: model.NoteVisibilityPublic}, &model.User{Username: "alice"}))
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Visibility: model.NoteVisibilityPublic}, &model.User{Username: "bob"}))
}

func TestMatchNote_KeywordsCaseSensitiveMiss(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", [][]string{{"Misskey"}}, func(a *model.Antenna) {
		a.CaseSensitive = true
	})
	text := "this is misskey"
	require.False(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Text: &text, Visibility: model.NoteVisibilityPublic}, &model.User{}))
}

func TestMatchNote_KeywordsCaseSensitiveHit(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", [][]string{{"Misskey"}}, func(a *model.Antenna) {
		a.CaseSensitive = true
	})
	text := "this is Misskey"
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Text: &text, Visibility: model.NoteVisibilityPublic}, &model.User{}))
}

func TestMatchNote_KeywordsAndOr(t *testing.T) {
	svc, repo := newSvc(t)
	// (foo AND bar) OR baz
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", [][]string{{"foo", "bar"}, {"baz"}})
	hit1 := "foo bar"
	hit2 := "baz only"
	miss := "foo only"
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Text: &hit1, Visibility: model.NoteVisibilityPublic}, &model.User{}))
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Text: &hit2, Visibility: model.NoteVisibilityPublic}, &model.User{}))
	require.False(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Text: &miss, Visibility: model.NoteVisibilityPublic}, &model.User{}))
}

func TestMatchNote_ExcludeKeywords(t *testing.T) {
	svc, repo := newSvc(t)
	exclude := []byte(`[["spam"]]`)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil, func(a *model.Antenna) {
		a.ExcludeKeywords = datatypes.JSON(exclude)
	})
	dirty := "this is spam"
	clean := "this is fine"
	require.False(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Text: &dirty, Visibility: model.NoteVisibilityPublic}, &model.User{}))
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Text: &clean, Visibility: model.NoteVisibilityPublic}, &model.User{}))
}

func TestMatchNote_BadKeywordsJSONTreatedAsEmpty(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil, func(a *model.Antenna) {
		a.Keywords = datatypes.JSON([]byte("{not json"))
	})
	// 不正 JSON は emptyMatches=true として扱うので keyword フィルタは pass
	// (matchKeywords が true を返す)
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Visibility: model.NoteVisibilityPublic}, &model.User{}))
}

func TestMatchNote_NilKeywordsTreatedAsEmpty(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil, func(a *model.Antenna) {
		a.Keywords = nil // empty raw → emptyMatches=true
	})
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Visibility: model.NoteVisibilityPublic}, &model.User{}))
}

func TestMatchNote_EmptyInnerGroupSkipped(t *testing.T) {
	svc, repo := newSvc(t)
	// 外側 OR の中に空 group + 実 group。空 group は skip され実 group が
	// マッチしなければ false
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", nil, func(a *model.Antenna) {
		a.Keywords = datatypes.JSON([]byte(`[[],["foo"]]`))
	})
	miss := "no match here"
	require.False(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Text: &miss, Visibility: model.NoteVisibilityPublic}, &model.User{}))
	hit := "foo here"
	require.True(t, svc.matchNote(repo.Antennas["a1"], &model.Note{Text: &hit, Visibility: model.NoteVisibilityPublic}, &model.User{}))
}

func TestNoteText_CWAndText(t *testing.T) {
	cw := "warning"
	text := "body"
	n := &model.Note{CW: &cw, Text: &text, Visibility: model.NoteVisibilityPublic}
	got := noteText(n)
	assert.Contains(t, got, "warning")
	assert.Contains(t, got, "body")
}

// --- SetClock --------------------------------------------------------------

func TestSetClock(t *testing.T) {
	svc, _ := newSvc(t)
	fixed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return fixed })
	svc.SetClock(nil)
	a, err := svc.Create(CreateInput{OwnerID: "u1", Name: "alpha", Src: model.AntennaSourceAll})
	require.NoError(t, err)
	assert.Equal(t, fixed, a.LastUsedAt)
}

// Home source: owner follows author → match, otherwise miss.
func TestMatchNote_HomeSource_Follows(t *testing.T) {
	svc, _ := newSvc(t)
	follows := testutil.NewMockFollowingRepository()
	svc.SetFollowingRepo(follows)
	// u1 → alice をフォロー
	require.NoError(t, follows.Create(&model.Following{ID: "f1", FollowerID: "u1", FolloweeID: "alice"}))
	a, err := svc.Create(CreateInput{OwnerID: "u1", Name: "homeA", Src: model.AntennaSourceHome})
	require.NoError(t, err)
	text := "hi"
	n := &model.Note{ID: "n1", UserID: "alice", Text: &text, Visibility: model.NoteVisibilityPublic}
	assert.True(t, svc.matchNote(a, n, &model.User{ID: "alice", Username: "alice"}))
}

func TestMatchNote_HomeSource_NotFollowing(t *testing.T) {
	svc, _ := newSvc(t)
	svc.SetFollowingRepo(testutil.NewMockFollowingRepository())
	a, err := svc.Create(CreateInput{OwnerID: "u1", Name: "homeB", Src: model.AntennaSourceHome})
	require.NoError(t, err)
	text := "hi"
	n := &model.Note{ID: "n1", UserID: "bob", Text: &text, Visibility: model.NoteVisibilityPublic}
	assert.False(t, svc.matchNote(a, n, &model.User{ID: "bob", Username: "bob"}))
}

func TestMatchNote_HomeSource_NilRepoMisses(t *testing.T) {
	// followingRepo 未注入なら home source は必ず miss (設定ミスを気付かせる)
	svc, _ := newSvc(t)
	a, err := svc.Create(CreateInput{OwnerID: "u1", Name: "homeC", Src: model.AntennaSourceHome})
	require.NoError(t, err)
	text := "hi"
	n := &model.Note{ID: "n1", UserID: "alice", Text: &text, Visibility: model.NoteVisibilityPublic}
	assert.False(t, svc.matchNote(a, n, &model.User{ID: "alice", Username: "alice"}))
}

// List source: author が UserList に含まれていれば match。
func TestMatchNote_ListSource_MemberMatches(t *testing.T) {
	svc, _ := newSvc(t)
	lists := testutil.NewMockUserListRepository()
	svc.SetUserListRepo(lists)
	listID := "ul1"
	require.NoError(t, lists.Create(&model.UserList{ID: listID, UserID: "u1", Name: "friends"}))
	require.NoError(t, lists.AddMember(&model.UserListMembership{UserListID: listID, UserID: "alice"}))
	a, err := svc.Create(CreateInput{OwnerID: "u1", Name: "listA", Src: model.AntennaSourceList, UserListID: &listID})
	require.NoError(t, err)
	text := "hi"
	n := &model.Note{ID: "n1", UserID: "alice", Text: &text, Visibility: model.NoteVisibilityPublic}
	assert.True(t, svc.matchNote(a, n, &model.User{ID: "alice", Username: "alice"}))
}

func TestMatchNote_ListSource_NonMember(t *testing.T) {
	svc, _ := newSvc(t)
	lists := testutil.NewMockUserListRepository()
	svc.SetUserListRepo(lists)
	listID := "ul1"
	require.NoError(t, lists.Create(&model.UserList{ID: listID, UserID: "u1", Name: "friends"}))
	a, err := svc.Create(CreateInput{OwnerID: "u1", Name: "listB", Src: model.AntennaSourceList, UserListID: &listID})
	require.NoError(t, err)
	text := "hi"
	n := &model.Note{ID: "n1", UserID: "bob", Text: &text, Visibility: model.NoteVisibilityPublic}
	assert.False(t, svc.matchNote(a, n, &model.User{ID: "bob", Username: "bob"}))
}

// --- List source: end-to-end through OnNoteCreated → Notes -----------------

// リストメンバーのノートだけがアンテナに届き、非メンバーのノートは除外される
// ことを OnNoteCreated → Notes の完全フローで検証する。
func TestOnNoteCreated_ListSource_OnlyMemberNotesAppear(t *testing.T) {
	svc, repo := newSvc(t)
	lists := testutil.NewMockUserListRepository()
	svc.SetUserListRepo(lists)

	// pushNoteはnow.UnixMilliからRedisストリームIDを生成するため、同一msで
	// 2件以上ENTRYするとXADDが重複IDで失敗する。テスト決定性のため単調増加の
	// 疑似クロックを注入する。
	start := time.Unix(1_700_000_000, 0)
	var tick int64
	svc.SetClock(func() time.Time {
		tick++
		return start.Add(time.Duration(tick) * time.Millisecond)
	})

	// ユーザーリストを作成し、alice と carol をメンバーに追加
	listID := "ul1"
	require.NoError(t, lists.Create(&model.UserList{ID: listID, UserID: "owner", Name: "favorites"}))
	require.NoError(t, lists.AddMember(&model.UserListMembership{UserListID: listID, UserID: "alice"}))
	require.NoError(t, lists.AddMember(&model.UserListMembership{UserListID: listID, UserID: "carol"}))

	// list ソースのアンテナを作成
	a := makeAntenna(t, "ant1", "owner", nil, func(a *model.Antenna) {
		a.Src = model.AntennaSourceList
		a.UserListID = &listID
	})
	repo.Antennas["ant1"] = a

	// リストメンバー alice のノート → アンテナに届くはず
	text1 := "hello from alice"
	n1 := &model.Note{ID: "n1", UserID: "alice", Text: &text1, Visibility: model.NoteVisibilityPublic}
	svc.OnNoteCreated(n1, &model.User{ID: "alice", Username: "alice"})

	// 非メンバー bob のノート → アンテナに届かないはず
	text2 := "hello from bob"
	n2 := &model.Note{ID: "n2", UserID: "bob", Text: &text2, Visibility: model.NoteVisibilityPublic}
	svc.OnNoteCreated(n2, &model.User{ID: "bob", Username: "bob"})

	// リストメンバー carol のノート → アンテナに届くはず
	text3 := "hello from carol"
	n3 := &model.Note{ID: "n3", UserID: "carol", Text: &text3, Visibility: model.NoteVisibilityPublic}
	svc.OnNoteCreated(n3, &model.User{ID: "carol", Username: "carol"})

	// 非メンバー dave のノート → アンテナに届かないはず
	text4 := "hello from dave"
	n4 := &model.Note{ID: "n4", UserID: "dave", Text: &text4, Visibility: model.NoteVisibilityPublic}
	svc.OnNoteCreated(n4, &model.User{ID: "dave", Username: "dave"})

	rows, err := svc.Notes(context.Background(), "owner", "ant1", 100, "", "")
	require.NoError(t, err)
	// alice と carol のノートだけが含まれ、bob と dave のノートは含まれない
	assert.Len(t, rows, 2)
	assert.Contains(t, rows, "n1")
	assert.Contains(t, rows, "n3")
	assert.NotContains(t, rows, "n2")
	assert.NotContains(t, rows, "n4")
}

// キーワードフィルタとリストソースを組み合わせた場合、リストメンバーかつ
// キーワードマッチするノートだけがアンテナに届く。
func TestOnNoteCreated_ListSource_WithKeywords(t *testing.T) {
	svc, repo := newSvc(t)
	lists := testutil.NewMockUserListRepository()
	svc.SetUserListRepo(lists)

	listID := "ul2"
	require.NoError(t, lists.Create(&model.UserList{ID: listID, UserID: "owner", Name: "devs"}))
	require.NoError(t, lists.AddMember(&model.UserListMembership{UserListID: listID, UserID: "alice"}))

	// list ソース + キーワード "golang" のアンテナ
	a := makeAntenna(t, "ant2", "owner", [][]string{{"golang"}}, func(a *model.Antenna) {
		a.Src = model.AntennaSourceList
		a.UserListID = &listID
	})
	repo.Antennas["ant2"] = a

	// alice のノートでキーワードマッチ → 届く
	text1 := "I love golang"
	svc.OnNoteCreated(
		&model.Note{ID: "n1", UserID: "alice", Text: &text1, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice"},
	)

	// alice のノートでキーワード不一致 → 届かない
	text2 := "I love python"
	svc.OnNoteCreated(
		&model.Note{ID: "n2", UserID: "alice", Text: &text2, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice"},
	)

	// 非メンバー bob のノートでキーワードマッチ → 届かない (リスト外)
	text3 := "I love golang too"
	svc.OnNoteCreated(
		&model.Note{ID: "n3", UserID: "bob", Text: &text3, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "bob", Username: "bob"},
	)

	rows, err := svc.Notes(context.Background(), "owner", "ant2", 100, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"n1"}, rows)
}

// --- List source: edge cases -----------------------------------------------

// userListRepo が未注入のとき list ソースは常に不一致になる。
func TestMatchNote_ListSource_NilRepoMisses(t *testing.T) {
	svc, _ := newSvc(t)
	// SetUserListRepo を呼ばない
	listID := "ul1"
	a, err := svc.Create(CreateInput{OwnerID: "u1", Name: "noRepo", Src: model.AntennaSourceList, UserListID: &listID})
	require.NoError(t, err)
	text := "hi"
	assert.False(t, svc.matchNote(a, &model.Note{Text: &text, Visibility: model.NoteVisibilityPublic}, &model.User{ID: "alice", Username: "alice"}))
}

// UserListID が nil のとき list ソースは不一致になる。
func TestMatchNote_ListSource_NilUserListID(t *testing.T) {
	svc, repo := newSvc(t)
	lists := testutil.NewMockUserListRepository()
	svc.SetUserListRepo(lists)
	a := makeAntenna(t, "ant-nil", "u1", nil, func(a *model.Antenna) {
		a.Src = model.AntennaSourceList
		a.UserListID = nil
	})
	repo.Antennas["ant-nil"] = a
	text := "hi"
	assert.False(t, svc.matchNote(a, &model.Note{Text: &text, Visibility: model.NoteVisibilityPublic}, &model.User{ID: "alice", Username: "alice"}))
}

// UserListID が空文字列のとき list ソースは不一致になる。
func TestMatchNote_ListSource_EmptyUserListID(t *testing.T) {
	svc, repo := newSvc(t)
	lists := testutil.NewMockUserListRepository()
	svc.SetUserListRepo(lists)
	empty := ""
	a := makeAntenna(t, "ant-empty", "u1", nil, func(a *model.Antenna) {
		a.Src = model.AntennaSourceList
		a.UserListID = &empty
	})
	repo.Antennas["ant-empty"] = a
	text := "hi"
	assert.False(t, svc.matchNote(a, &model.Note{Text: &text, Visibility: model.NoteVisibilityPublic}, &model.User{ID: "alice", Username: "alice"}))
}

// ListMembers がエラーを返す場合 list ソースは不一致になる。
func TestMatchNote_ListSource_ListMembersError(t *testing.T) {
	svc, repo := newSvc(t)
	svc.SetUserListRepo(&failingUserListRepo{})
	listID := "ul-err"
	a := makeAntenna(t, "ant-err", "u1", nil, func(a *model.Antenna) {
		a.Src = model.AntennaSourceList
		a.UserListID = &listID
	})
	repo.Antennas["ant-err"] = a
	text := "hi"
	assert.False(t, svc.matchNote(a, &model.Note{Text: &text, Visibility: model.NoteVisibilityPublic}, &model.User{ID: "alice", Username: "alice"}))
}

// failingUserListRepo causes ListMembers and FindByID to fail.
type failingUserListRepo struct{}

func (r *failingUserListRepo) Create(_ *model.UserList) error { return nil }
func (r *failingUserListRepo) FindByID(_ string) (*model.UserList, error) {
	return nil, errors.New("boom")
}
func (r *failingUserListRepo) ListByUser(_ string) ([]*model.UserList, error) { return nil, nil }
func (r *failingUserListRepo) CountByUser(_ string) (int64, error)            { return 0, nil }
func (r *failingUserListRepo) Delete(_ string) error                          { return nil }
func (r *failingUserListRepo) AddMember(_ *model.UserListMembership) error    { return nil }
func (r *failingUserListRepo) RemoveMember(_, _ string) error                 { return nil }
func (r *failingUserListRepo) CountMembers(_ string) (int64, error)           { return 0, nil }
func (r *failingUserListRepo) ListMembers(_ string) ([]*model.UserListMembership, error) {
	return nil, errors.New("list members error")
}
func (r *failingUserListRepo) ListMembershipsPage(_, _, _ string, _ int) ([]*model.UserListMembership, error) {
	return nil, errors.New("list memberships page error")
}
func (r *failingUserListRepo) ListMembersByListIDs(_ []string) (map[string][]string, error) {
	return nil, errors.New("list members by ids error")
}
func (r *failingUserListRepo) UpdateList(_ string, _ map[string]any) error { return nil }
func (r *failingUserListRepo) UpdateMembership(_, _ string, _ *bool) error { return nil }
func (r *failingUserListRepo) ListIDsByMember(_ string) ([]string, error)  { return nil, nil }
func (r *failingUserListRepo) ListIDsAndOwnersByMember(_ string) (map[string]string, error) {
	return nil, nil
}
func (r *failingUserListRepo) ListsContainingMember(_, _ string) ([]*model.UserList, error) {
	return nil, nil
}

// リストメンバーを追加・削除した後のアンテナ動作を検証する。
// メンバーを削除するとそのユーザーのノートはアンテナに届かなくなる。
func TestOnNoteCreated_ListSource_MemberRemoved(t *testing.T) {
	svc, repo := newSvc(t)
	lists := testutil.NewMockUserListRepository()
	svc.SetUserListRepo(lists)

	listID := "ul-rm"
	require.NoError(t, lists.Create(&model.UserList{ID: listID, UserID: "owner", Name: "team"}))
	require.NoError(t, lists.AddMember(&model.UserListMembership{UserListID: listID, UserID: "alice"}))
	require.NoError(t, lists.AddMember(&model.UserListMembership{UserListID: listID, UserID: "bob"}))

	a := makeAntenna(t, "ant-rm", "owner", nil, func(a *model.Antenna) {
		a.Src = model.AntennaSourceList
		a.UserListID = &listID
	})
	repo.Antennas["ant-rm"] = a

	// bob をリストから削除
	require.NoError(t, lists.RemoveMember(listID, "bob"))

	text := "post after removal"
	svc.OnNoteCreated(
		&model.Note{ID: "n-alice", UserID: "alice", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "alice", Username: "alice"},
	)
	svc.OnNoteCreated(
		&model.Note{ID: "n-bob", UserID: "bob", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "bob", Username: "bob"},
	)

	rows, err := svc.Notes(context.Background(), "owner", "ant-rm", 100, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"n-alice"}, rows)
}

// --- Visibility gate (#1464) ----------------------------------------------
//
// matchNote が note visibility に対して CanSeeNote 相当のチェックを行うことを
// 保証する。`src=all` / `src=users` のような broad source antenna が
// followers / specified note を pickup してしまう IDOR (#1464) の regression
// gate。

// followers note: antenna owner が follow していなければ matchNote が false。
func TestMatchNote_FollowersVisibility_NonFollowerOwnerRejected(t *testing.T) {
	svc, repo := newSvc(t)
	// followingRepo を空 (= owner は author を follow していない) で配線
	svc.SetFollowingRepo(testutil.NewMockFollowingRepository())
	repo.Antennas["a1"] = makeAntenna(t, "a1", "owner", [][]string{{"hi"}})

	text := "hi"
	n := &model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityFollowers}
	author := &model.User{ID: "author", Username: "alice"}

	require.False(t, svc.matchNote(repo.Antennas["a1"], n, author))
}

// followers note: antenna owner が follower なら matchNote が true。
func TestMatchNote_FollowersVisibility_FollowerOwnerAccepted(t *testing.T) {
	svc, repo := newSvc(t)
	follows := testutil.NewMockFollowingRepository()
	// owner → author を follow
	require.NoError(t, follows.Create(&model.Following{ID: "f1", FollowerID: "owner", FolloweeID: "author"}))
	svc.SetFollowingRepo(follows)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "owner", [][]string{{"hi"}})

	text := "hi"
	n := &model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityFollowers}
	author := &model.User{ID: "author", Username: "alice"}

	require.True(t, svc.matchNote(repo.Antennas["a1"], n, author))
}

// followers note: antenna owner == author なら follow チェック無しで通る。
func TestMatchNote_FollowersVisibility_SelfAuthored(t *testing.T) {
	svc, repo := newSvc(t)
	// followingRepo は無くて良い (本人 short-circuit)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "author", [][]string{{"hi"}})

	text := "hi"
	n := &model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityFollowers}
	author := &model.User{ID: "author", Username: "alice"}

	require.True(t, svc.matchNote(repo.Antennas["a1"], n, author))
}

// specified note: antenna owner が visibleUserIds に含まれていなければ false。
func TestMatchNote_SpecifiedVisibility_NonRecipientRejected(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "owner", [][]string{{"hi"}})

	text := "hi"
	n := &model.Note{
		ID: "n1", UserID: "author", Text: &text,
		Visibility:     model.NoteVisibilitySpecified,
		VisibleUserIDs: []string{"someoneElse"},
	}
	author := &model.User{ID: "author", Username: "alice"}

	require.False(t, svc.matchNote(repo.Antennas["a1"], n, author))
}

// specified note: antenna owner が visibleUserIds に含まれていれば true。
func TestMatchNote_SpecifiedVisibility_RecipientAccepted(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "owner", [][]string{{"hi"}})

	text := "hi"
	n := &model.Note{
		ID: "n1", UserID: "author", Text: &text,
		Visibility:     model.NoteVisibilitySpecified,
		VisibleUserIDs: []string{"owner"},
	}
	author := &model.User{ID: "author", Username: "alice"}

	require.True(t, svc.matchNote(repo.Antennas["a1"], n, author))
}

// followingRepo 未配線 + followers note → CanSeeNote の fail-closed semantics
// で本人以外は reject される (= IDOR fail-closed)。
func TestMatchNote_FollowersVisibility_NilFollowingRepoFailClosed(t *testing.T) {
	svc, repo := newSvc(t)
	// followingRepo を意図的に注入しない (本番でも `home` source 設定ミス検出時と同じ挙動)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "owner", [][]string{{"hi"}})

	text := "hi"
	n := &model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityFollowers}
	author := &model.User{ID: "author", Username: "alice"}

	require.False(t, svc.matchNote(repo.Antennas["a1"], n, author))
}

// OnNoteCreated 経由でも push されないことを確認 (REST/WS 両 surface への gate)。
func TestOnNoteCreated_FollowersNote_NotPushedToNonFollowerAntenna(t *testing.T) {
	svc, repo := newSvc(t)
	svc.SetFollowingRepo(testutil.NewMockFollowingRepository()) // 空 → owner は author を follow していない
	repo.Antennas["a1"] = makeAntenna(t, "a1", "owner", [][]string{{"hi"}})

	text := "hi there"
	svc.OnNoteCreated(
		&model.Note{ID: "n-followers", UserID: "author", Text: &text, Visibility: model.NoteVisibilityFollowers},
		&model.User{ID: "author", Username: "alice"},
	)

	rows, err := svc.Notes(context.Background(), "owner", "a1", 10, "", "")
	require.NoError(t, err)
	assert.Empty(t, rows, "non-follower antenna owner should not receive followers note")
}

// public note は visibility gate を素通りすることを confirm (regression guard)。
func TestOnNoteCreated_PublicNote_PushedRegardlessOfFollow(t *testing.T) {
	svc, repo := newSvc(t)
	svc.SetFollowingRepo(testutil.NewMockFollowingRepository()) // 空でも public は通る
	repo.Antennas["a1"] = makeAntenna(t, "a1", "owner", [][]string{{"hi"}})

	text := "hi there"
	svc.OnNoteCreated(
		&model.Note{ID: "n-public", UserID: "author", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "author", Username: "alice"},
	)

	rows, err := svc.Notes(context.Background(), "owner", "a1", 10, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"n-public"}, rows)
}

// countingFollowingRepo は Exists の呼び出し回数を記録する MockFollowingRepository
// の薄い wrap。#1467 review nit (home source の Exists 二重呼び出し回避) の回帰テスト
// 専用。Following struct を直接操作するため map にも直接 push する。
type countingFollowingRepo struct {
	*testutil.MockFollowingRepository
	existsCalls int
}

func newCountingFollowingRepo() *countingFollowingRepo {
	return &countingFollowingRepo{MockFollowingRepository: testutil.NewMockFollowingRepository()}
}

func (c *countingFollowingRepo) Exists(followerID, followeeID string) (bool, error) {
	c.existsCalls++
	return c.MockFollowingRepository.Exists(followerID, followeeID)
}

// #1467 review nit: home source antenna が followers visibility note を pickup する
// ケースで、CanSeeNote 内 (`Exists(owner, author)`) と matchSource 内
// (`Exists(a.UserID, author.ID)`) の同一 pair に対する Exists を 1 回に畳めている
// ことを assert する。
func TestMatchNote_HomeSource_FollowersNote_NoDuplicateExists(t *testing.T) {
	svc, _ := newSvc(t)
	counting := newCountingFollowingRepo()
	// owner → author を follow
	require.NoError(t, counting.Create(&model.Following{ID: "f1", FollowerID: "owner", FolloweeID: "author"}))
	svc.SetFollowingRepo(counting)
	a, err := svc.Create(CreateInput{OwnerID: "owner", Name: "home-followers", Src: model.AntennaSourceHome})
	require.NoError(t, err)

	text := "hi"
	n := &model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityFollowers}
	author := &model.User{ID: "author", Username: "author"}

	require.True(t, svc.matchNote(a, n, author))
	assert.Equal(t, 1, counting.existsCalls,
		"Exists should be called exactly once for home source + followers visibility (no duplicate after #1467 review)")
}

// home source + public note では visibility gate (CanSeeNote) が Exists を呼ばないため、
// matchSource 側の Exists が 1 回だけ走る。NoDuplicateExists test の対照 (公開範囲が
// followers でないと最適化の前提も失われていないことを示す)。
func TestMatchNote_HomeSource_PublicNote_SingleExists(t *testing.T) {
	svc, _ := newSvc(t)
	counting := newCountingFollowingRepo()
	require.NoError(t, counting.Create(&model.Following{ID: "f1", FollowerID: "owner", FolloweeID: "author"}))
	svc.SetFollowingRepo(counting)
	a, err := svc.Create(CreateInput{OwnerID: "owner", Name: "home-public", Src: model.AntennaSourceHome})
	require.NoError(t, err)

	text := "hi"
	n := &model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityPublic}
	author := &model.User{ID: "author", Username: "author"}

	require.True(t, svc.matchNote(a, n, author))
	assert.Equal(t, 1, counting.existsCalls,
		"public visibility skips CanSeeNote Exists; matchSource home performs the single call")
}

// --- StreamingPublisher (#1573) -------------------------------------------

// stubStreamingPublisher records PublishNote calls so OnNoteCreated's pub/sub
// publish can be asserted without a real stream layer.
type stubStreamingPublisher struct {
	calls []publishCall
}

type publishCall struct {
	topic  string
	noteID string
	author string
}

func (p *stubStreamingPublisher) PublishNote(topic string, n *model.Note, author *model.User) {
	pc := publishCall{topic: topic}
	if n != nil {
		pc.noteID = n.ID
	}
	if author != nil {
		pc.author = author.ID
	}
	p.calls = append(p.calls, pc)
}

// TestOnNoteCreated_PublishesMatchedNote covers #1573 課題1: a matched note is
// published to the antennaTimeline:<id> pub/sub topic (= streamKey) alongside
// the Redis stream XAdd, so the WS antenna channel receives live notes.
func TestOnNoteCreated_PublishesMatchedNote(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", [][]string{{"misskey"}})
	pub := &stubStreamingPublisher{}
	svc.SetStreamingPublisher(pub)

	text := "hello misskey world"
	n := &model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityPublic}
	author := &model.User{ID: "author", Username: "alice"}
	svc.OnNoteCreated(n, author)

	require.Len(t, pub.calls, 1, "matched note must be published once")
	assert.Equal(t, "antennaTimeline:a1", pub.calls[0].topic,
		"publish topic must equal streamKey so AntennaChannel.Subscribe matches")
	assert.Equal(t, "n1", pub.calls[0].noteID)
	assert.Equal(t, "author", pub.calls[0].author)
}

// TestOnNoteCreated_NoMatchDoesNotPublish guards that a non-matching note is
// neither pushed to the stream nor published to pub/sub.
func TestOnNoteCreated_NoMatchDoesNotPublish(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", [][]string{{"missing"}})
	pub := &stubStreamingPublisher{}
	svc.SetStreamingPublisher(pub)

	text := "hello world"
	svc.OnNoteCreated(
		&model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "author", Username: "alice"},
	)
	assert.Empty(t, pub.calls, "non-matching note must not be published")
}

// TestOnNoteCreated_PublishesToEachMatchedAntenna verifies one publish per
// matched antenna, each to its own streamKey topic.
func TestOnNoteCreated_PublishesToEachMatchedAntenna(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", [][]string{{"misskey"}})
	repo.Antennas["a2"] = makeAntenna(t, "a2", "u2", [][]string{{"misskey"}})
	repo.Antennas["a3"] = makeAntenna(t, "a3", "u3", [][]string{{"other"}}) // no match
	pub := &stubStreamingPublisher{}
	svc.SetStreamingPublisher(pub)

	text := "hello misskey"
	svc.OnNoteCreated(
		&model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "author", Username: "alice"},
	)

	topics := make([]string, 0, len(pub.calls))
	for _, c := range pub.calls {
		topics = append(topics, c.topic)
	}
	assert.ElementsMatch(t, []string{"antennaTimeline:a1", "antennaTimeline:a2"}, topics,
		"each matched antenna gets a publish to its own streamKey topic; non-matching is skipped")
}

// TestOnNoteCreated_NilPublisherIsNoOp guards that an unwired publisher leaves
// the stream XAdd path intact (REST `antennas/notes` still works).
func TestOnNoteCreated_NilPublisherIsNoOp(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = makeAntenna(t, "a1", "u1", [][]string{{"misskey"}})
	// publisher 未配線でも panic せず stream XAdd は行われる。

	text := "hello misskey world"
	svc.OnNoteCreated(
		&model.Note{ID: "n1", UserID: "author", Text: &text, Visibility: model.NoteVisibilityPublic},
		&model.User{ID: "author", Username: "alice"},
	)
	rows, err := svc.Notes(context.Background(), "u1", "a1", 10, "", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"n1"}, rows)
}

// #2069 (upstream #17463): RemoveNote は antenna TL stream から noteId 一致 entry を
// 削除する。owner 検証 (not-found / not-owner)、存在しないノートは no-op 成功。
func TestRemoveNote(t *testing.T) {
	svc, repo := newSvc(t)
	repo.Antennas["a1"] = &model.Antenna{ID: "a1", UserID: "u1"}
	ctx := context.Background()
	for _, n := range []string{"n1", "n2", "n3"} {
		require.NoError(t, testRedis.Client.XAdd(ctx, &redis.XAddArgs{
			Stream: streamKey("a1"), Values: map[string]any{"noteId": n},
		}).Err())
	}

	// n2 を削除 → n1, n3 が残る。
	require.NoError(t, svc.RemoveNote("u1", "a1", "n2"))
	rows, err := svc.Notes(ctx, "u1", "a1", 10, "", "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"n1", "n3"}, rows, "n2 のみ削除される (#2069)")

	// 存在しないノートは no-op 成功。
	require.NoError(t, svc.RemoveNote("u1", "a1", "ghost"))

	// not-owner は ErrAccessDenied、未存在 antenna は ErrAntennaNotFound。
	assert.ErrorIs(t, svc.RemoveNote("other", "a1", "n1"), ErrAccessDenied)
	assert.ErrorIs(t, svc.RemoveNote("u1", "nope", "n1"), ErrAntennaNotFound)
}
