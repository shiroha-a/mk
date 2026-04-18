package admin_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingListV2EmojiRepo struct {
	*testutil.MockEmojiRepository
}

func (f *failingListV2EmojiRepo) ListV2(_ model.EmojiV2Filter) ([]*model.Emoji, error) {
	return nil, assert.AnError
}

func (f *failingListV2EmojiRepo) CountV2(_ model.EmojiV2Filter) (int64, error) {
	return 0, assert.AnError
}

// --- Drive ------------------------------------------------------------------

func TestDriveFiles_FiltersByOrigin(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockDriveFileRepository()
	localUser := "u1"
	remoteHost := "remote.example"
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d1", UserID: &localUser, Type: "image/png"}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d2", UserHost: &remoteHost, Type: "image/jpeg"}))
	h.SetDriveFileRepo(repo)

	rec := doPost(h.DriveFiles, `{"origin":"remote","limit":10}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "d2", rows[0]["id"])
}

func TestDriveFiles_FiltersByTypePrefix(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockDriveFileRepository()
	u := "u1"
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d1", UserID: &u, Type: "image/png"}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d2", UserID: &u, Type: "video/mp4"}))
	h.SetDriveFileRepo(repo)

	rec := doPost(h.DriveFiles, `{"type":"image/","limit":10}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "d1", rows[0]["id"])
}

func TestDriveShowFile_MissingBothFileIDAndURL(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetDriveFileRepo(testutil.NewMockDriveFileRepository())
	assert.Equal(t, http.StatusBadRequest,
		doPost(h.DriveShowFile, `{}`, adminUser).Code)
}

func TestDriveShowFile_ByFileID(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockDriveFileRepository()
	u := "u1"
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d1", UserID: &u, Name: "a.png", Type: "image/png"}))
	h.SetDriveFileRepo(repo)

	rec := doPost(h.DriveShowFile, `{"fileId":"d1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestDriveCleanup_InvokesDeleteOrphans(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockDriveFileRepository()
	require.NoError(t, repo.Create(&model.DriveFile{ID: "orphan1"}))
	u := "u1"
	require.NoError(t, repo.Create(&model.DriveFile{ID: "kept", UserID: &u}))
	h.SetDriveFileRepo(repo)

	rec := doPost(h.DriveCleanup, `{}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.NotContains(t, repo.Files, "orphan1", "orphan should be deleted")
	assert.Contains(t, repo.Files, "kept", "user-owned file should be kept")
}

func TestDriveCleanRemoteFiles_InvokesDeleteRemoteCache(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockDriveFileRepository()
	host := "remote.example"
	require.NoError(t, repo.Create(&model.DriveFile{ID: "remote1", IsLink: true, UserHost: &host}))
	u := "u1"
	require.NoError(t, repo.Create(&model.DriveFile{ID: "local1", UserID: &u}))
	h.SetDriveFileRepo(repo)

	rec := doPost(h.DriveCleanRemoteFiles, `{}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.NotContains(t, repo.Files, "remote1")
	assert.Contains(t, repo.Files, "local1")
}

// --- Emoji ------------------------------------------------------------------

func TestEmojiListRemote_Pagination(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	host := "remote.example"
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "alpha", Host: &host}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e2", Name: "beta", Host: &host}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e3", Name: "gamma", Host: nil})) // local
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiListRemote, `{"limit":10}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	// local の e3 は除外される
	assert.Len(t, rows, 2)
}

func TestEmojiListRemote_FilterByQuery(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	host := "remote.example"
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "cat_smile", Host: &host}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e2", Name: "dog_run", Host: &host}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiListRemote, `{"query":"cat","limit":10}`, adminUser)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "e1", rows[0]["id"])
}

func TestEmojiCopy_DuplicateNameReturns400(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	// MockEmojiRepository は name@host をキーとして保存するので、既存の
	// smile と、衝突先となる smile_copy の 2 件を入れる。
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "smile", OriginalURL: "https://x"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e_dup", Name: "smile_copy", OriginalURL: "https://y"}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiCopy, `{"emojiId":"e1"}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errField, ok := body["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "DUPLICATE_NAME", errField["code"])
}

// findEmojiByID walks the mock's Emojis map (which is keyed by name@host, not
// id) to fetch a row by id.
func findEmojiByID(t *testing.T, repo *testutil.MockEmojiRepository, id string) *model.Emoji {
	t.Helper()
	e, err := repo.FindByID(id)
	require.NoError(t, err)
	return e
}

func TestEmojiCopy_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "unique", OriginalURL: "https://x"}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiCopy, `{"emojiId":"e1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotEmpty(t, body["id"])
}

func TestEmojiCopy_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiRepo(testutil.NewMockEmojiRepository())
	assert.Equal(t, http.StatusNotFound,
		doPost(h.EmojiCopy, `{"emojiId":"missing"}`, adminUser).Code)
}

func TestEmojiSetCategoryBulk_BatchUpdate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "alpha"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e2", Name: "beta"}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiSetCategoryBulk,
		`{"ids":["e1","e2"],"category":"animals"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, findEmojiByID(t, repo, "e1").Category)
	assert.Equal(t, "animals", *findEmojiByID(t, repo, "e1").Category)
	require.NotNil(t, findEmojiByID(t, repo, "e2").Category)
	assert.Equal(t, "animals", *findEmojiByID(t, repo, "e2").Category)
}

func TestEmojiSetLicenseBulk_BatchUpdate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "alpha"}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiSetLicenseBulk, `{"ids":["e1"],"license":"CC-BY-4.0"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, findEmojiByID(t, repo, "e1").License)
	assert.Equal(t, "CC-BY-4.0", *findEmojiByID(t, repo, "e1").License)
}

func TestEmojiSetAliasesBulk_BatchUpdate(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "alpha"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e2", Name: "beta"}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiSetAliasesBulk,
		`{"ids":["e1","e2"],"aliases":["x","y"]}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, []string(findEmojiByID(t, repo, "e1").Aliases), []string{"x", "y"})
	assert.Equal(t, []string(findEmojiByID(t, repo, "e2").Aliases), []string{"x", "y"})
}

func TestEmojiAddAliasesBulk_Merges(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "alpha", Aliases: []string{"existing"}}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiAddAliasesBulk,
		`{"ids":["e1"],"aliases":["new1","new2"]}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	// dedupe + preserve order
	assert.Equal(t, []string(findEmojiByID(t, repo, "e1").Aliases), []string{"existing", "new1", "new2"})
}

func TestEmojiAddAliasesBulk_DedupesAgainstExisting(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "alpha", Aliases: []string{"a", "b"}}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiAddAliasesBulk,
		`{"ids":["e1"],"aliases":["b","c"]}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, []string(findEmojiByID(t, repo, "e1").Aliases), []string{"a", "b", "c"})
}

func TestEmojiRemoveAliasesBulk_Filters(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "alpha", Aliases: []string{"a", "b", "c"}}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiRemoveAliasesBulk,
		`{"ids":["e1"],"aliases":["b"]}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, []string(findEmojiByID(t, repo, "e1").Aliases), []string{"a", "c"})
}

func TestEmojiDeleteBulk_Batch(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "alpha"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e2", Name: "beta"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e3", Name: "gamma"}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiDeleteBulk, `{"ids":["e1","e3"]}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	// Mock は name@ キーなので ID で存在確認するには FindByID を使う
	_, err := repo.FindByID("e1")
	assert.Error(t, err, "e1 should be deleted")
	_, err = repo.FindByID("e2")
	assert.NoError(t, err, "e2 should remain")
	_, err = repo.FindByID("e3")
	assert.Error(t, err, "e3 should be deleted")
}

func TestEmojiImportZip_UnknownFileReturns400(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetDriveFileRepo(testutil.NewMockDriveFileRepository())
	h.SetEmojiImportEnqueuer(&stubEmojiImportEnqueuer{})
	rec := doPost(h.EmojiImportZip, `{"fileId":"missing"}`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- EmojiListV2 -------------------------------------------------------------

func TestEmojiListV2_Basic(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "smile", PublicURL: "https://example.com/smile.png"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e2", Name: "wave", OriginalURL: "https://example.com/wave-orig.png"}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiListV2, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	emojis := resp["emojis"].([]any)
	assert.Len(t, emojis, 2)
	assert.EqualValues(t, 2, resp["count"])
	assert.EqualValues(t, 2, resp["allCount"])
	assert.EqualValues(t, 1, resp["allPages"])
	// v2はpackDetailedAdmin相当: publicUrl/originalUrlを直接返す（computed urlは含まない）
	for _, raw := range emojis {
		em := raw.(map[string]any)
		_, hasPublicURL := em["publicUrl"]
		assert.True(t, hasPublicURL, "v2 emoji should have publicUrl field")
	}
}

func TestEmojiListV2_NilRepo(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.EmojiListV2, `{}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp["emojis"].([]any), 0)
	assert.EqualValues(t, 0, resp["allCount"])
}

func TestEmojiListV2_InvalidJSON(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := doPost(h.EmojiListV2, `invalid`, adminUser)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEmojiListV2_HostType(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	host := "remote.example"
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "local_only"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e2", Name: "remote_one", Host: &host}))
	h.SetEmojiRepo(repo)

	// local のみ
	rec := doPost(h.EmojiListV2, `{"query":{"hostType":"local"}}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	emojis := resp["emojis"].([]any)
	assert.Len(t, emojis, 1)
	assert.Equal(t, "e1", emojis[0].(map[string]any)["id"])

	// remote のみ
	rec = doPost(h.EmojiListV2, `{"query":{"hostType":"remote"}}`, adminUser)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	emojis = resp["emojis"].([]any)
	assert.Len(t, emojis, 1)
	assert.Equal(t, "e2", emojis[0].(map[string]any)["id"])

	// all（デフォルト）
	rec = doPost(h.EmojiListV2, `{"query":{"hostType":"all"}}`, adminUser)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	emojis = resp["emojis"].([]any)
	assert.Len(t, emojis, 2)
}

func TestEmojiListV2_QueryFilter(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "cat_smile"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e2", Name: "dog_run"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e3", Name: "caterpillar"}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiListV2, `{"query":{"name":"cat"}}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	emojis := resp["emojis"].([]any)
	assert.Len(t, emojis, 2, "cat_smile and caterpillar should match")
	assert.EqualValues(t, 2, resp["allCount"])
}

func TestEmojiListV2_Pagination(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	for i := 0; i < 5; i++ {
		id := "e" + string(rune('1'+i))
		require.NoError(t, repo.Create(&model.Emoji{ID: id, Name: "emoji_" + id}))
	}
	h.SetEmojiRepo(repo)

	// page 1, limit 2
	rec := doPost(h.EmojiListV2, `{"limit":2,"page":1}`, adminUser)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	emojis := resp["emojis"].([]any)
	assert.Len(t, emojis, 2)
	assert.EqualValues(t, 5, resp["allCount"])
	assert.EqualValues(t, 3, resp["allPages"])

	// page 3 — 残り1件
	rec = doPost(h.EmojiListV2, `{"limit":2,"page":3}`, adminUser)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	emojis = resp["emojis"].([]any)
	assert.Len(t, emojis, 1)
	assert.EqualValues(t, 1, resp["count"])
}

func TestEmojiListV2_SortKeys(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "beta"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e2", Name: "alpha"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e3", Name: "gamma"}))
	h.SetEmojiRepo(repo)

	// name ASC
	rec := doPost(h.EmojiListV2, `{"sortKeys":["+name"]}`, adminUser)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	emojis := resp["emojis"].([]any)
	require.Len(t, emojis, 3)
	assert.Equal(t, "alpha", emojis[0].(map[string]any)["name"])
	assert.Equal(t, "beta", emojis[1].(map[string]any)["name"])
	assert.Equal(t, "gamma", emojis[2].(map[string]any)["name"])

	// デフォルト: id DESC
	rec = doPost(h.EmojiListV2, `{}`, adminUser)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	emojis = resp["emojis"].([]any)
	require.Len(t, emojis, 3)
	assert.Equal(t, "e3", emojis[0].(map[string]any)["id"])
	assert.Equal(t, "e1", emojis[2].(map[string]any)["id"])
}

func TestEmojiListV2_SensitiveFilter(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "safe", IsSensitive: false}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e2", Name: "nsfw", IsSensitive: true}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiListV2, `{"query":{"isSensitive":true}}`, adminUser)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	emojis := resp["emojis"].([]any)
	assert.Len(t, emojis, 1)
	assert.Equal(t, "e2", emojis[0].(map[string]any)["id"])
}

func TestEmojiListV2_LimitClamped(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "a"}))
	h.SetEmojiRepo(repo)

	// limit > 100 は100にクランプされる
	rec := doPost(h.EmojiListV2, `{"limit":999}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.EqualValues(t, 1, resp["allPages"])
}

func TestEmojiListV2_ListV2Error(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiRepo(&failingListV2EmojiRepo{testutil.NewMockEmojiRepository()})
	rec := doPost(h.EmojiListV2, `{}`, adminUser)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
