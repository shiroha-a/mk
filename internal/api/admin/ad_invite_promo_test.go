package admin_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAdHandler returns a handler with AdRepo wired and optional seed rows.
// AdRepo を使う test の boilerplate (handler 構築 + repo 生成 + seed +
// SetAdRepo) を 1 行に圧縮する (#761)。戻り値の repo を直接 mutate して
// CreateErr 等の error injection も可能。
func setupAdHandler(t *testing.T, seed ...*model.Ad) (*apiadmin.Handler, *testutil.MockAdRepository) {
	t.Helper()
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockAdRepository()
	for _, ad := range seed {
		require.NoError(t, repo.Create(ad))
	}
	h.SetAdRepo(repo)
	return h, repo
}

// setupAvatarDecorationHandler returns a handler with AvatarDecorationRepo
// wired and optional seed rows. Ad helper と同じ contract (#761)。
func setupAvatarDecorationHandler(t *testing.T, seed ...*model.AvatarDecoration) (*apiadmin.Handler, *testutil.MockAvatarDecorationRepository) {
	t.Helper()
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockAvatarDecorationRepository()
	for _, d := range seed {
		require.NoError(t, repo.Create(d))
	}
	h.SetAvatarDecorationRepo(repo)
	return h, repo
}

// setupInviteHandler returns a handler with InviteRepo (RegistrationTicket
// repo) wired and optional seed rows. Ad helper と同じ contract (#761)。
func setupInviteHandler(t *testing.T, seed ...*model.RegistrationTicket) (*apiadmin.Handler, *testutil.MockRegistrationTicketRepository) {
	t.Helper()
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockRegistrationTicketRepository()
	for _, tkt := range seed {
		require.NoError(t, repo.Create(tkt))
	}
	h.SetInviteRepo(repo)
	return h, repo
}

// --- Ad ---------------------------------------------------------------------

func TestAdCreate_Success(t *testing.T) {
	h, repo := setupAdHandler(t)
	rec := doPost(h.AdCreate,
		`{"url":"https://x","imageUrl":"https://y","place":"square","memo":"m","priority":"high","ratio":2,"dayOfWeek":3,"expiresAt":1760000000000,"startsAt":1759000000000,"isSensitive":true}`,
		adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got model.Ad
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "https://x", got.URL)
	assert.Equal(t, "square", got.Place)
	assert.Equal(t, 2, got.Ratio)
	assert.True(t, got.IsSensitive)
	assert.Contains(t, repo.Ads, got.ID)
}

func TestAdCreate_MissingRequired(t *testing.T) {
	h, _ := setupAdHandler(t)
	assert.Equal(t, http.StatusBadRequest,
		doPost(h.AdCreate, `{"url":""}`, adminUser).Code)
}

func TestAdCreate_Defaults(t *testing.T) {
	h, _ := setupAdHandler(t)
	rec := doPost(h.AdCreate, `{"url":"https://x","imageUrl":"https://y"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got model.Ad
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "square", got.Place)
	assert.Equal(t, "middle", got.Priority)
	assert.Equal(t, 1, got.Ratio)
}

func TestAdCreate_RepoError(t *testing.T) {
	h, repo := setupAdHandler(t)
	repo.CreateErr = assertError{}
	rec := doPost(h.AdCreate, `{"url":"https://x","imageUrl":"https://y"}`, adminUser)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestAdList_Paginated(t *testing.T) {
	ads := make([]*model.Ad, 5)
	for i := 0; i < 5; i++ {
		ads[i] = &model.Ad{
			ID: fmt.Sprintf("a%02d", i), URL: "u", ImageURL: "i", Place: "square", Priority: "middle", Ratio: 1,
			StartsAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour),
		}
	}
	h, _ := setupAdHandler(t, ads...)

	rec := doPost(h.AdList, `{"limit":3,"offset":0}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []model.Ad
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 3)
	// id DESC
	assert.Equal(t, "a04", rows[0].ID)
}

func TestAdUpdate_PartialFields(t *testing.T) {
	h, repo := setupAdHandler(t,
		&model.Ad{ID: "a1", URL: "old", Place: "square", Priority: "middle", Ratio: 1, ImageURL: "i"},
	)

	rec := doPost(h.AdUpdate,
		`{"id":"a1","url":"new","priority":"high"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "new", repo.Ads["a1"].URL)
	assert.Equal(t, "high", repo.Ads["a1"].Priority)
}

func TestAdUpdate_NotFound(t *testing.T) {
	h, _ := setupAdHandler(t)
	assert.Equal(t, http.StatusNotFound,
		doPost(h.AdUpdate, `{"id":"missing","url":"x"}`, adminUser).Code)
}

func TestAdUpdate_MissingID(t *testing.T) {
	h, _ := setupAdHandler(t)
	assert.Equal(t, http.StatusBadRequest,
		doPost(h.AdUpdate, `{}`, adminUser).Code)
}

// Explicit expiresAt:0 in Update はクライアントが明示的に 1970-01-01 を指定した
// ケースとして扱う (nil なら変更なし)。handler 側の millisOrNow 適用で now に
// 読み替えてしまう regression を防ぐ。
func TestAdUpdate_ExplicitZeroExpiresAtStays1970(t *testing.T) {
	h, repo := setupAdHandler(t,
		&model.Ad{
			ID: "a1", URL: "u", Place: "square", Priority: "middle", Ratio: 1, ImageURL: "i",
			StartsAt:  time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		},
	)

	rec := doPost(h.AdUpdate, `{"id":"a1","expiresAt":0}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	// time.UnixMilli(0) は UTC 1970-01-01。time.Now() と大きく異なること。
	got := repo.Ads["a1"].ExpiresAt
	assert.Equal(t, int64(0), got.UnixMilli(),
		"explicit expiresAt:0 should map to UNIX epoch, not time.Now()")
}

func TestAdDelete_WithRepo(t *testing.T) {
	h, repo := setupAdHandler(t, &model.Ad{ID: "a1"})

	rec := doPost(h.AdDelete, `{"id":"a1"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.NotContains(t, repo.Ads, "a1")
}

// --- AvatarDecorations ------------------------------------------------------

func TestAvatarDecorationsCreate_Success(t *testing.T) {
	h, _ := setupAvatarDecorationHandler(t)
	rec := doPost(h.AvatarDecorationsCreate,
		`{"name":"deco","description":"d","url":"https://i","roleIdsThatCanBeUsedThisDecoration":["r1","r2"]}`,
		adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got model.AvatarDecoration
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "deco", got.Name)
	assert.Equal(t, []string(got.RoleIDs), []string{"r1", "r2"})
}

func TestAvatarDecorationsCreate_MissingName(t *testing.T) {
	h, _ := setupAvatarDecorationHandler(t)
	assert.Equal(t, http.StatusBadRequest,
		doPost(h.AvatarDecorationsCreate, `{"url":"https://i"}`, adminUser).Code)
}

func TestAvatarDecorationsList_WithRepo(t *testing.T) {
	h, _ := setupAvatarDecorationHandler(t,
		&model.AvatarDecoration{ID: "d1", Name: "a"},
		&model.AvatarDecoration{ID: "d2", Name: "b"},
	)
	rec := doPost(h.AvatarDecorationsList, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []model.AvatarDecoration
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 2)
}

func TestAvatarDecorationsUpdate_Success(t *testing.T) {
	h, repo := setupAvatarDecorationHandler(t,
		&model.AvatarDecoration{ID: "d1", Name: "old", URL: "u"},
	)
	rec := doPost(h.AvatarDecorationsUpdate,
		`{"id":"d1","name":"new","roleIdsThatCanBeUsedThisDecoration":["r"]}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "new", repo.Decorations["d1"].Name)
	assert.Equal(t, []string(repo.Decorations["d1"].RoleIDs), []string{"r"})
}

func TestAvatarDecorationsUpdate_NotFound(t *testing.T) {
	h, _ := setupAvatarDecorationHandler(t)
	assert.Equal(t, http.StatusNotFound,
		doPost(h.AvatarDecorationsUpdate, `{"id":"missing","name":"x"}`, adminUser).Code)
}

func TestAvatarDecorationsUpdate_MissingID(t *testing.T) {
	h, _ := setupAvatarDecorationHandler(t)
	assert.Equal(t, http.StatusBadRequest,
		doPost(h.AvatarDecorationsUpdate, `{}`, adminUser).Code)
}

func TestAvatarDecorationsDelete_WithRepo(t *testing.T) {
	h, repo := setupAvatarDecorationHandler(t, &model.AvatarDecoration{ID: "d1"})
	rec := doPost(h.AvatarDecorationsDelete, `{"id":"d1"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.NotContains(t, repo.Decorations, "d1")
}

// --- Invite -----------------------------------------------------------------

func TestInviteCreate_Default(t *testing.T) {
	h, _ := setupInviteHandler(t)
	rec := doPost(h.InviteCreate, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.NotEmpty(t, rows[0]["code"])
	// createdAt が含まれる (Misskey 本家互換)
	assert.NotNil(t, rows[0]["createdAt"])
	assert.Equal(t, false, rows[0]["used"])
}

func TestInviteCreate_MultipleCount(t *testing.T) {
	h, _ := setupInviteHandler(t)
	rec := doPost(h.InviteCreate, `{"count":5}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 5)
}

func TestInviteCreate_CountClampedToMax(t *testing.T) {
	h, _ := setupInviteHandler(t)
	rec := doPost(h.InviteCreate, `{"count":250}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	assert.Len(t, rows, 100)
}

func TestInviteCreate_InvalidExpiresAt(t *testing.T) {
	h, _ := setupInviteHandler(t)
	rec := doPost(h.InviteCreate, `{"expiresAt":"not-a-date"}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInviteList_FilterUnused(t *testing.T) {
	usedBy := "u1"
	usedAt := time.Now()
	h, _ := setupInviteHandler(t,
		&model.RegistrationTicket{ID: "t1", Code: "c1"},
		&model.RegistrationTicket{ID: "t2", Code: "c2", UsedByID: &usedBy, UsedAt: &usedAt},
	)

	rec := doPost(h.InviteList, `{"type":"unused"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "t1", rows[0]["id"])
	assert.Equal(t, false, rows[0]["used"])
}

// --- Promo ------------------------------------------------------------------

// stubNoteFinder satisfies admin.NoteFinder with just FindByID.
type stubNoteFinder struct {
	note *model.Note
	err  error
}

func (s *stubNoteFinder) FindByID(_ string) (*model.Note, error) { return s.note, s.err }

type stubPromoRepo struct {
	created  *model.PromoNote
	exists   bool
	existErr error
}

func (s *stubPromoRepo) Create(p *model.PromoNote) error {
	s.created = p
	return nil
}
func (s *stubPromoRepo) ListActive(_ time.Time) ([]*model.PromoNote, error) { return nil, nil }
func (s *stubPromoRepo) Exists(_ string) (bool, error)                      { return s.exists, s.existErr }

func TestPromoCreate_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	promo := &stubPromoRepo{}
	h.SetPromoNoteRepo(promo)
	note := &model.Note{ID: "n1", UserID: "authorU"}
	h.SetNoteFinder(&stubNoteFinder{note: note})

	expires := time.Now().Add(24 * time.Hour).UnixMilli()
	body := fmt.Sprintf(`{"noteId":"n1","expiresAt":%d}`, expires)
	rec := doPost(h.PromoCreate, body, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, promo.created)
	assert.Equal(t, "n1", promo.created.NoteID)
	assert.Equal(t, "authorU", promo.created.UserID)
}

func TestPromoCreate_MissingNoteID(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetPromoNoteRepo(&stubPromoRepo{})
	h.SetNoteFinder(&stubNoteFinder{})
	assert.Equal(t, http.StatusBadRequest,
		doPost(h.PromoCreate, `{}`, adminUser).Code)
}

func TestPromoCreate_NoSuchNote(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetPromoNoteRepo(&stubPromoRepo{})
	h.SetNoteFinder(&stubNoteFinder{err: assertError{}})
	rec := doPost(h.PromoCreate, `{"noteId":"missing","expiresAt":1}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPromoCreate_AlreadyPromoted(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetPromoNoteRepo(&stubPromoRepo{exists: true})
	h.SetNoteFinder(&stubNoteFinder{note: &model.Note{ID: "n1", UserID: "u"}})
	rec := doPost(h.PromoCreate, `{"noteId":"n1","expiresAt":1}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errField, ok := body["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ALREADY_PROMOTED", errField["code"])
}

// --- thin nil-repo smoke tests ---
//
// nil repo 経路 (newTestHandler は ad / avatar / invite / promo の repo を
// wire しない) で 204 / 200 が返ることを担保する軽量テスト。

func TestAdCreate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.AdCreate, `{}`, adminUser).Code)
}

func TestAdDelete(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.AdDelete, `{}`, adminUser).Code)
}

func TestAdList(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.AdList, `{}`, adminUser).Code)
}

func TestAdUpdate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.AdUpdate, `{}`, adminUser).Code)
}

func TestAvatarDecorationsCreate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.AvatarDecorationsCreate, `{}`, adminUser).Code)
}

func TestAvatarDecorationsDelete(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.AvatarDecorationsDelete, `{}`, adminUser).Code)
}

func TestAvatarDecorationsList(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.AvatarDecorationsList, `{}`, adminUser).Code)
}

func TestAvatarDecorationsUpdate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.AvatarDecorationsUpdate, `{}`, adminUser).Code)
}

func TestInviteCreate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.InviteCreate, `{}`, adminUser).Code)
}

// --- moderation log assertions (#662 + #665) ---

func TestAdCreate_WritesModerationLog(t *testing.T) {
	h, _ := setupAdHandler(t)
	repo := attachModLog(t, h)

	rec := doPost(h.AdCreate, `{"url":"https://x","imageUrl":"https://y"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "createAd", repo.Snapshot()[0].Type)
}

func TestAdUpdate_WritesModerationLog(t *testing.T) {
	h, _ := setupAdHandler(t,
		&model.Ad{ID: "ad1", URL: "https://x", ImageURL: "https://y"},
	)
	repo := attachModLog(t, h)

	url := "https://updated"
	body := fmt.Sprintf(`{"id":"ad1","url":%q}`, url)
	rec := doPost(h.AdUpdate, body, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "updateAd", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	require.NotNil(t, info["before"])
	require.NotNil(t, info["after"])
}

func TestAdDelete_WritesModerationLog(t *testing.T) {
	h, _ := setupAdHandler(t,
		&model.Ad{ID: "ad1", URL: "https://x", ImageURL: "https://y"},
	)
	repo := attachModLog(t, h)

	rec := doPost(h.AdDelete, `{"id":"ad1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "deleteAd", repo.Snapshot()[0].Type)
}

func TestInviteCreate_WritesModerationLog(t *testing.T) {
	h, _ := setupInviteHandler(t)
	repo := attachModLog(t, h)

	rec := doPost(h.InviteCreate, `{"count":2}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "createInvitation", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	require.NotNil(t, info["invitations"])
}

func TestAvatarDecorationsCreate_WritesModerationLog(t *testing.T) {
	h, _ := setupAvatarDecorationHandler(t)
	repo := attachModLog(t, h)

	rec := doPost(h.AvatarDecorationsCreate, `{"name":"crown","url":"https://x"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "createAvatarDecoration", repo.Snapshot()[0].Type)
}

func TestAvatarDecorationsUpdate_WritesModerationLog(t *testing.T) {
	h, _ := setupAvatarDecorationHandler(t,
		&model.AvatarDecoration{ID: "d1", Name: "crown"},
	)
	repo := attachModLog(t, h)

	rec := doPost(h.AvatarDecorationsUpdate, `{"id":"d1","name":"jewel"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "updateAvatarDecoration", repo.Snapshot()[0].Type)
}

func TestAvatarDecorationsDelete_WritesModerationLog(t *testing.T) {
	h, _ := setupAvatarDecorationHandler(t,
		&model.AvatarDecoration{ID: "d1", Name: "doomed"},
	)
	repo := attachModLog(t, h)

	rec := doPost(h.AvatarDecorationsDelete, `{"id":"d1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "deleteAvatarDecoration", repo.Snapshot()[0].Type)
}

func TestInviteList(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.InviteList, `{}`, adminUser).Code)
}

func TestPromoCreate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.PromoCreate, `{}`, adminUser).Code)
}
