package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/shiroha-a/mk/internal/core/notification"
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
	tr.Teardown(ctx)
	os.Exit(code)
}

func newTestHandler(t *testing.T) (*Handler, *notification.Service) {
	t.Helper()
	testRedis.FlushAll(context.Background())
	svc := notification.NewService(testRedis.Client, idGen, "")
	// テストは AfterFunc 待ちを避けるため同期 publish にする。
	svc.SetUnreadPublishDelay(0)
	return NewHandler(svc, idGen), svc
}

func newJSONRequest(t *testing.T, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func setAuth(c echo.Context, user *model.User) {
	c.Set(string(middleware.UserContextKey), user)
}

func TestShow_Empty(t *testing.T) {
	h, _ := newTestHandler(t)
	c, rec := newJSONRequest(t, "/api/i/notifications", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp)
}

func TestShow_WithEntries(t *testing.T) {
	h, svc := newTestHandler(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)
	_, err = svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeReaction,
		NoteID: "n1", Reaction: "👍",
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications", `{"limit":50}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 2)
	assert.Equal(t, "reaction", resp[0]["type"])
	assert.Equal(t, "n1", resp[0]["noteId"])
	assert.Equal(t, "👍", resp[0]["reaction"])
	assert.Equal(t, "bob", resp[0]["userId"])
}

// sinceDate=0 (= 1970 epoch) を渡したら全 notification が post-fetch filter
// を通過する。adapter pattern (= sinceDate → aidx prefix) が effectively に
// 動くことの positive case (#1174)。
func TestShow_SinceDate_EpochIncludesAll(t *testing.T) {
	h, svc := newTestHandler(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications", `{"limit":50,"sinceDate":0}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1, "sinceDate=0 (= 1970) で全件含まれること")
}

// untilDate を未来に設定したら全 notification が post-fetch filter を通過する
// (= untilID は exclusive upper bound、未来 ms に対する aidx prefix は全 ID
// より大きいので全件含まれる)。
func TestShow_UntilDate_FutureIncludesAll(t *testing.T) {
	h, svc := newTestHandler(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	futureMs := time.Now().Add(time.Hour).UnixMilli()
	body := fmt.Sprintf(`{"limit":50,"untilDate":%d}`, futureMs)
	c, rec := newJSONRequest(t, "/api/i/notifications", body)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1, "untilDate=future で全件含まれること")
}

// sinceDate を未来に設定したら post-fetch filter で全件除外される。
// adapter pattern の negative case (#1174)。
func TestShow_SinceDate_FutureFiltersOut(t *testing.T) {
	h, svc := newTestHandler(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	futureMs := time.Now().Add(time.Hour).UnixMilli()
	body := fmt.Sprintf(`{"limit":50,"sinceDate":%d}`, futureMs)
	c, rec := newJSONRequest(t, "/api/i/notifications", body)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp, "sinceDate=future で全件 filter out されること")
}

func TestShow_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler(t)
	c, rec := newJSONRequest(t, "/api/i/notifications", `{invalid`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestShow_RedisError(t *testing.T) {
	closed := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	_ = closed.Close()
	svc := notification.NewService(closed, idGen, "")
	h := NewHandler(svc, idGen)

	c, rec := newJSONRequest(t, "/api/i/notifications", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// stubMainPublisher captures PublishMainEvent calls so the test can verify
// the implicit mark-as-read side effect of /api/i/notifications (#420).
type stubMainPublisher struct {
	mu     sync.Mutex
	events []stubMainEvent
}

type stubMainEvent struct {
	userID    string
	eventType string
}

func (s *stubMainPublisher) PublishMainEvent(userID, eventType string, _ any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, stubMainEvent{userID: userID, eventType: eventType})
}

func (s *stubMainPublisher) types(userID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.events))
	for _, e := range s.events {
		if e.userID == userID {
			out = append(out, e.eventType)
		}
	}
	return out
}

// 通知一覧 fetch の副作用として暗黙の mark-all-as-read が走り、main stream
// に `readAllNotifications` が publish されることを確認する (#420)。
func TestShow_DefaultMarksAllAsRead(t *testing.T) {
	h, svc := newTestHandler(t)
	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)

	_, err := svc.Create(context.Background(), notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Contains(t, pub.types("alice"), "readAllNotifications",
		"Show should publish readAllNotifications by default")
}

// markAsRead: false を明示した場合は副作用を発火させない。本家
// i/notifications と同じ semantics。
func TestShow_MarkAsReadFalseSkipsRead(t *testing.T) {
	h, svc := newTestHandler(t)
	pub := &stubMainPublisher{}
	svc.SetMainStreamPublisher(pub)

	_, err := svc.Create(context.Background(), notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications", `{"markAsRead":false}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.NotContains(t, pub.types("alice"), "readAllNotifications",
		"Show with markAsRead:false must not publish readAllNotifications")
}

func TestMarkAllAsRead_OK(t *testing.T) {
	h, svc := newTestHandler(t)
	_, err := svc.Create(context.Background(), notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/notifications/mark-all-as-read", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.MarkAllAsRead(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMarkAllAsRead_RedisError(t *testing.T) {
	closed := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	_ = closed.Close()
	svc := notification.NewService(closed, idGen, "")
	h := NewHandler(svc, idGen)

	c, rec := newJSONRequest(t, "/api/notifications/mark-all-as-read", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.MarkAllAsRead(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestCreate_Success(t *testing.T) {
	h, _ := newTestHandler(t)
	c, rec := newJSONRequest(t, "/api/notifications/create", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestFlush_Success(t *testing.T) {
	h, svc := newTestHandler(t)
	ctx := context.Background()
	// 1件作成してから flush するとストリームが消えることを確認。
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/notifications/flush", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Flush(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// handler が Flush() を呼んでいれば stream 長 0。
	// 以前は MarkAllAsRead() を呼んでいたため元通知が残ってしまっていた。
	out, err := svc.List(ctx, "alice", 10)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestFlush_NilService(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	h := NewHandler(nil, idGen)
	c, rec := newJSONRequest(t, "/api/notifications/flush", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Flush(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestFlush_RedisError(t *testing.T) {
	closed := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	_ = closed.Close()
	svc := notification.NewService(closed, idGen, "")
	h := NewHandler(svc, idGen)

	c, rec := newJSONRequest(t, "/api/notifications/flush", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Flush(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestTestNotification_Success(t *testing.T) {
	h, _ := newTestHandler(t)
	c, rec := newJSONRequest(t, "/api/notifications/test-notification", `{}`)
	require.NoError(t, h.TestNotification(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestShow_WithUserAndNoteResolution(t *testing.T) {
	h, svc := newTestHandler(t)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	userRepo.Users["bob"] = &model.User{ID: "bob", Username: "bobuser"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "bob", Visibility: "public", User: &model.User{ID: "bob", Username: "bobuser"}}
	h.SetRepos(userRepo, noteRepo)

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeMention, NoteID: "n1",
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "mention", resp[0]["type"])
	assert.NotNil(t, resp[0]["user"])
	assert.NotNil(t, resp[0]["note"])
}

// InstanceRepo / EmojiRepo 配線時にリモート notifier の user.instance が
// streaming / REST payload に入ることを確認する (#415 InstanceTicker 欠落の
// follow-up)。
func TestShow_WithInstanceAndEmojiResolution(t *testing.T) {
	h, svc := newTestHandler(t)
	userRepo := testutil.NewMockUserRepository()
	noteRepo := testutil.NewMockNoteRepository()
	instanceRepo := testutil.NewMockInstanceRepository()
	emojiRepo := testutil.NewMockEmojiRepository()

	remoteHost := "remote.example"
	themeColor := "#ff7799"
	instanceRepo.Instances[remoteHost] = &model.Instance{Host: remoteHost, ThemeColor: &themeColor}
	userRepo.Users["remoteU"] = &model.User{ID: "remoteU", Username: "remotee", Host: &remoteHost}

	h.SetRepos(userRepo, noteRepo)
	h.SetInstanceRepo(instanceRepo)
	h.SetEmojiRepo(emojiRepo)

	ctx := context.Background()
	_, err := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "remoteU", Type: notification.TypeFollow,
	})
	require.NoError(t, err)

	c, rec := newJSONRequest(t, "/api/i/notifications", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	user, ok := resp[0]["user"].(map[string]any)
	require.True(t, ok)
	inst, ok := user["instance"].(map[string]any)
	require.True(t, ok, "user.instance must be set on remote notifier")
	assert.Equal(t, themeColor, inst["themeColor"])
}

func TestShow_IncludeTypesFilter(t *testing.T) {
	h, svc := newTestHandler(t)
	ctx := context.Background()
	_, _ = svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	_, _ = svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeMention, NoteID: "n1",
	})

	c, rec := newJSONRequest(t, "/api/i/notifications", `{"includeTypes":["mention"]}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
	assert.Equal(t, "mention", resp[0]["type"])
}

func TestShow_ExcludeTypesFilter(t *testing.T) {
	h, svc := newTestHandler(t)
	ctx := context.Background()
	_, _ = svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	_, _ = svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeMention, NoteID: "n1",
	})

	c, rec := newJSONRequest(t, "/api/i/notifications", `{"excludeTypes":["follow"]}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp, 1)
	assert.Equal(t, "mention", resp[0]["type"])
}

func TestShow_CursorPagination(t *testing.T) {
	h, svc := newTestHandler(t)
	ctx := context.Background()
	_, _ = svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeFollow,
	})
	n2, _ := svc.Create(ctx, notification.CreateInput{
		NotifieeID: "alice", NotifierID: "bob", Type: notification.TypeMention, NoteID: "n1",
	})

	// sinceId: n2より新しいものだけ (= 該当なし)
	body := `{"sinceId":"` + n2.ID + `"}`
	c, rec := newJSONRequest(t, "/api/i/notifications", body)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp)
}

// countingNotifRepos wraps the mock user / note repos to count single-row
// FindByID-style calls. After #515 the bulk path must use FindManyByIDs /
// FindManyByIDsWithUser so single-row callers should be 0.
type countingNotifUserRepo struct {
	*testutil.MockUserRepository
	findByIDCalls         int
	findManyByIDsCalls    int
	findManyByIDsCallSize int
}

func (c *countingNotifUserRepo) FindByID(id string) (*model.User, error) {
	c.findByIDCalls++
	return c.MockUserRepository.FindByID(id)
}

func (c *countingNotifUserRepo) FindManyByIDs(ids []string) ([]*model.User, error) {
	c.findManyByIDsCalls++
	c.findManyByIDsCallSize += len(ids)
	return c.MockUserRepository.FindManyByIDs(ids)
}

type countingNotifNoteRepo struct {
	*testutil.MockNoteRepository
	findByIDWithRelationsCalls int
	findManyByIDsWithUserCalls int
}

func (c *countingNotifNoteRepo) FindByIDWithRelations(id string) (*model.Note, error) {
	c.findByIDWithRelationsCalls++
	return c.MockNoteRepository.FindByIDWithRelations(id)
}

func (c *countingNotifNoteRepo) FindManyByIDsWithUser(ids []string) ([]*model.Note, error) {
	c.findManyByIDsWithUserCalls++
	return c.MockNoteRepository.FindManyByIDsWithUser(ids)
}

// 通知 5 件 (異なる notifier / note) を返すケースで、user と note が batch
// 1 回ずつだけ DB に問い合わされ、per-row FindByID 経路が使われていないこと
// を担保する (#515 N+1 解消)。
func TestShow_BatchFetchesNotifierAndNote(t *testing.T) {
	h, svc := newTestHandler(t)
	userRepo := &countingNotifUserRepo{MockUserRepository: testutil.NewMockUserRepository()}
	noteRepo := &countingNotifNoteRepo{MockNoteRepository: testutil.NewMockNoteRepository()}
	for i := 0; i < 5; i++ {
		uid := fmt.Sprintf("notifier-%d", i)
		nid := fmt.Sprintf("note-%d", i)
		userRepo.Users[uid] = &model.User{ID: uid, Username: uid}
		noteRepo.Notes[nid] = &model.Note{ID: nid, UserID: uid, Visibility: "public",
			User: &model.User{ID: uid, Username: uid}}
	}
	h.SetRepos(userRepo, noteRepo)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := svc.Create(ctx, notification.CreateInput{
			NotifieeID: "alice",
			NotifierID: fmt.Sprintf("notifier-%d", i),
			Type:       notification.TypeMention,
			NoteID:     fmt.Sprintf("note-%d", i),
		})
		require.NoError(t, err)
	}

	c, rec := newJSONRequest(t, "/api/i/notifications", `{}`)
	setAuth(c, &model.User{ID: "alice"})
	require.NoError(t, h.Show(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 5)

	assert.Equal(t, 0, userRepo.findByIDCalls,
		"per-row userRepo.FindByID must not be called (N+1 must be eliminated)")
	assert.Equal(t, 1, userRepo.findManyByIDsCalls,
		"userRepo.FindManyByIDs should be called exactly once per request")
	assert.Equal(t, 5, userRepo.findManyByIDsCallSize,
		"all 5 notifier IDs should be coalesced into a single batch")
	assert.Equal(t, 0, noteRepo.findByIDWithRelationsCalls,
		"per-row noteRepo.FindByIDWithRelations must not be called")
	assert.Equal(t, 1, noteRepo.findManyByIDsWithUserCalls,
		"noteRepo.FindManyByIDsWithUser should be called exactly once per request")

	// Devin #516 INFO-3: batch fetch しても resolved user / note が response
	// に正しく入ることを self-contained で担保する。
	for i, item := range resp {
		assert.NotNilf(t, item["user"], "item[%d].user must be present", i)
		assert.NotNilf(t, item["note"], "item[%d].note must be present", i)
	}
}
