package admin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
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

// stubEmojiImportEnqueuer records the last payload and optionally returns err.
type stubEmojiImportEnqueuer struct {
	lastUserID string
	lastFileID string
	err        error
}

func (s *stubEmojiImportEnqueuer) EnqueueImportCustomEmojis(p queue.ImportCustomEmojisPayload) error {
	if s.err != nil {
		return s.err
	}
	s.lastUserID = p.UserID
	s.lastFileID = p.FileID
	return nil
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

func TestDriveFiles_FiltersByUserID(t *testing.T) {
	// admin/user/<id> のドライブタブは userId だけを送ってくる (#471)。
	// upstream は userId 指定時に origin / hostname を読まないので、それに
	// 合わせて他ユーザーの remote ファイルが混ざらないことを確認する。
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockDriveFileRepository()
	target := "u_target"
	other := "u_other"
	otherHost := "remote.example"
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d_t1", UserID: &target, Type: "image/png"}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d_t2", UserID: &target, UserHost: &otherHost, Type: "image/jpeg"}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d_o1", UserID: &other, UserHost: &otherHost, Type: "image/png"}))
	h.SetDriveFileRepo(repo)

	rec := doPost(h.DriveFiles, `{"userId":"u_target","limit":10}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	gotIDs := make(map[string]bool, len(rows))
	for _, r := range rows {
		gotIDs[r["id"].(string)] = true
	}
	assert.True(t, gotIDs["d_t1"])
	assert.True(t, gotIDs["d_t2"])
	assert.False(t, gotIDs["d_o1"])
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

func TestDriveFiles_FiltersBySystemToken(t *testing.T) {
	// #686: userId="@system" 指定時は system 所有 (UserID IS NULL) の drive
	// file のみを返す。emoji copy / import zip 経路で蓄積される system file
	// を admin UI から可視化するための経路。
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockDriveFileRepository()
	u := "u1"
	remoteUser := "u_remote"
	host := "remote.example"
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d_sys1", UserID: nil, Type: "image/png"}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d_sys2", UserID: nil, Type: "image/jpeg"}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d_user", UserID: &u, Type: "image/png"}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d_remote", UserID: &remoteUser, UserHost: &host, Type: "image/png"}))
	h.SetDriveFileRepo(repo)

	rec := doPost(h.DriveFiles, `{"userId":"@system","limit":10}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	gotIDs := make(map[string]bool, len(rows))
	for _, r := range rows {
		gotIDs[r["id"].(string)] = true
	}
	assert.True(t, gotIDs["d_sys1"], "system file should be listed")
	assert.True(t, gotIDs["d_sys2"], "system file should be listed")
	assert.False(t, gotIDs["d_user"], "user-owned file must not appear")
	assert.False(t, gotIDs["d_remote"], "remote-owned file must not appear")
}

func TestDriveFiles_SystemTokenWithTypeFilter(t *testing.T) {
	// #686: @system token は type prefix filter と組み合わせ可能。MIME 種別で
	// system file を絞り込めることを保証する (image/* だけ見たい等)。
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockDriveFileRepository()
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d_sys_img", UserID: nil, Type: "image/png"}))
	require.NoError(t, repo.Create(&model.DriveFile{ID: "d_sys_zip", UserID: nil, Type: "application/zip"}))
	h.SetDriveFileRepo(repo)

	rec := doPost(h.DriveFiles, `{"userId":"@system","type":"image/","limit":10}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "d_sys_img", rows[0]["id"])
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
	// Misskey TS 互換 (#650 問題 1): copy は元 name のまま local 化するため、
	// 同名の local emoji が既存だと DUPLICATE_NAME を返す。
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	remoteHost := "remote.example"
	// remote の "smile" を copy しようとするが、local にも同名 "smile" 存在。
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "smile", Host: &remoteHost, OriginalURL: "https://x"}))
	require.NoError(t, repo.Create(&model.Emoji{ID: "e_local", Name: "smile", OriginalURL: "https://y"}))
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
	// remote → local copy: copy 先が host=nil で同名 local emoji が無い
	// ので 200 を返す (Misskey TS 互換、#650 問題 1)。
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	remoteHost := "remote.example"
	require.NoError(t, repo.Create(&model.Emoji{ID: "e1", Name: "unique", Host: &remoteHost, OriginalURL: "https://x"}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiCopy, `{"emojiId":"e1"}`, adminUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotEmpty(t, body["id"])
	// copied emoji は元 name "unique" で local 化されている
	copied := findEmojiByID(t, repo, body["id"].(string))
	assert.Equal(t, "unique", copied.Name)
	assert.Nil(t, copied.Host)
}

func TestEmojiCopy_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiRepo(testutil.NewMockEmojiRepository())
	assert.Equal(t, http.StatusNotFound,
		doPost(h.EmojiCopy, `{"emojiId":"missing"}`, adminUser).Code)
}

// fakeEmojiImageFetcher captures FetchAndStore arguments and returns a
// scripted response so tests can verify EmojiCopy plumbs the fetcher
// correctly (#670).
type fakeEmojiImageFetcher struct {
	calls []struct {
		URL  string
		Name string
		User *model.User
	}
	returnDF  *model.DriveFile
	returnErr error
}

func (f *fakeEmojiImageFetcher) FetchAndStore(_ context.Context, url string, user *model.User, name string) (*model.DriveFile, error) {
	f.calls = append(f.calls, struct {
		URL  string
		Name string
		User *model.User
	}{url, name, user})
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	return f.returnDF, nil
}

// TestEmojiCopy_StoresInDrive verifies that a wired fetcher is invoked and
// the returned drive URL replaces the source's OriginalURL/PublicURL on the
// new local emoji (#670). Without this rewrite the local copy stays bound to
// the remote server's URL and breaks when that server deletes the file.
func TestEmojiCopy_StoresInDrive(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	remoteHost := "remote.example"
	require.NoError(t, repo.Create(&model.Emoji{
		ID: "src1", Name: "happy", Host: &remoteHost,
		OriginalURL: "https://remote.example/emoji/happy.png",
		PublicURL:   "https://remote.example/emoji/happy.png",
	}))
	h.SetEmojiRepo(repo)

	webpub := "https://local.example/files/webpub.png"
	webpubType := "image/webp"
	fetcher := &fakeEmojiImageFetcher{
		returnDF: &model.DriveFile{
			ID:            "df1",
			URL:           "https://local.example/files/orig.png",
			WebpublicURL:  &webpub,
			WebpublicType: &webpubType,
			Type:          "image/png",
		},
	}
	h.SetEmojiImageFetcher(fetcher)

	rec := doPost(h.EmojiCopy, `{"emojiId":"src1"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	copied := findEmojiByID(t, repo, body["id"].(string))

	// fetcher was called exactly once with the source's OriginalURL, the
	// emoji name (used as drive file name), and a nil user (system-owned
	// drive file, matching Misskey TS uploadFromUrl({user: null}) — see
	// emoji.go EmojiCopy comment).
	require.Len(t, fetcher.calls, 1)
	assert.Equal(t, "https://remote.example/emoji/happy.png", fetcher.calls[0].URL)
	assert.Equal(t, "happy", fetcher.calls[0].Name)
	assert.Nil(t, fetcher.calls[0].User, "drive file must be system-owned (user nil)")

	// New emoji's URLs point at the drive file, not the remote source.
	assert.Equal(t, "https://local.example/files/orig.png", copied.OriginalURL)
	assert.Equal(t, "https://local.example/files/webpub.png", copied.PublicURL)
	require.NotNil(t, copied.Type)
	// Webpublic type is preferred when present (matches Misskey TS behaviour).
	assert.Equal(t, "image/webp", *copied.Type)
}

// TestEmojiCopy_StoresInDrive_NoWebpublic falls back to df.URL / df.Type
// when the drive file has no webpublic variant (e.g. small images).
func TestEmojiCopy_StoresInDrive_NoWebpublic(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	remoteHost := "remote.example"
	require.NoError(t, repo.Create(&model.Emoji{
		ID: "src1", Name: "tiny", Host: &remoteHost,
		OriginalURL: "https://remote.example/emoji/tiny.png",
	}))
	h.SetEmojiRepo(repo)

	fetcher := &fakeEmojiImageFetcher{
		returnDF: &model.DriveFile{
			ID:   "df1",
			URL:  "https://local.example/files/tiny.png",
			Type: "image/png",
		},
	}
	h.SetEmojiImageFetcher(fetcher)

	rec := doPost(h.EmojiCopy, `{"emojiId":"src1"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	copied := findEmojiByID(t, repo, body["id"].(string))

	assert.Equal(t, "https://local.example/files/tiny.png", copied.OriginalURL)
	assert.Equal(t, "https://local.example/files/tiny.png", copied.PublicURL)
	require.NotNil(t, copied.Type)
	assert.Equal(t, "image/png", *copied.Type)
}

// TestEmojiCopy_FetcherErrorReturns500 verifies that a fetch failure aborts
// the copy (no row created) instead of silently falling back to the remote
// URL — falling back would re-introduce the #670 symptom.
func TestEmojiCopy_FetcherErrorReturns500(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	remoteHost := "remote.example"
	require.NoError(t, repo.Create(&model.Emoji{
		ID: "src1", Name: "happy", Host: &remoteHost,
		OriginalURL: "https://remote.example/emoji/happy.png",
	}))
	h.SetEmojiRepo(repo)

	fetcher := &fakeEmojiImageFetcher{returnErr: assert.AnError}
	h.SetEmojiImageFetcher(fetcher)

	rec := doPost(h.EmojiCopy, `{"emojiId":"src1"}`, adminUser)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	// fetcher が失敗したら row は作られない (#670 fallback regression 防止)。
	// 元 src が 1 件だけ残っており、host=nil の copy が増えていないことを
	// repo の内部 map から直接確認する。
	assert.Len(t, repo.Emojis, 1, "no new emoji row should have been created on fetch failure")
}

// TestEmojiCopy_FetcherUnsetUsesLegacyURL keeps the pre-#670 behaviour for
// environments that have not wired a fetcher (test handlers, deployments
// before the upgrade, etc). Without this fallback every test handler would
// have to wire a drive stub.
func TestEmojiCopy_FetcherUnsetUsesLegacyURL(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	remoteHost := "remote.example"
	require.NoError(t, repo.Create(&model.Emoji{
		ID: "src1", Name: "happy", Host: &remoteHost,
		OriginalURL: "https://remote.example/emoji/happy.png",
		PublicURL:   "https://remote.example/emoji/happy.png",
	}))
	h.SetEmojiRepo(repo)

	rec := doPost(h.EmojiCopy, `{"emojiId":"src1"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	copied := findEmojiByID(t, repo, body["id"].(string))
	assert.Equal(t, "https://remote.example/emoji/happy.png", copied.OriginalURL)
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

// #466: frontend が remote emoji 検索ボックスから uri / publicUrl /
// originalUrl filter を渡してくる。handler / model / repo がこれらを
// 受けないと silent ignore で「検索しても全件出る」状態になる。
func TestEmojiListV2_URIFilter(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	repo := testutil.NewMockEmojiRepository()
	hostA := "alpha.example"
	hostB := "beta.example"
	uriA := "https://alpha.example/emojis/x"
	uriB := "https://beta.example/emojis/y"
	require.NoError(t, repo.Create(&model.Emoji{
		ID: "e1", Name: "x", Host: &hostA, URI: &uriA,
		OriginalURL: "https://alpha-cdn.example/x.png",
		PublicURL:   "https://alpha-cdn.example/x.png",
	}))
	require.NoError(t, repo.Create(&model.Emoji{
		ID: "e2", Name: "y", Host: &hostB, URI: &uriB,
		OriginalURL: "https://beta-cdn.example/y.png",
		PublicURL:   "https://beta-cdn.example/y.png",
	}))
	h.SetEmojiRepo(repo)

	t.Run("uri filter narrows to alpha", func(t *testing.T) {
		rec := doPost(h.EmojiListV2, `{"query":{"uri":"alpha.example"}}`, adminUser)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		emojis := resp["emojis"].([]any)
		require.Len(t, emojis, 1)
		assert.Equal(t, "e1", emojis[0].(map[string]any)["id"])
	})
	t.Run("publicUrl filter narrows to beta", func(t *testing.T) {
		rec := doPost(h.EmojiListV2, `{"query":{"publicUrl":"beta-cdn"}}`, adminUser)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		emojis := resp["emojis"].([]any)
		require.Len(t, emojis, 1)
		assert.Equal(t, "e2", emojis[0].(map[string]any)["id"])
	})
	t.Run("originalUrl filter narrows to alpha", func(t *testing.T) {
		rec := doPost(h.EmojiListV2, `{"query":{"originalUrl":"alpha-cdn"}}`, adminUser)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		emojis := resp["emojis"].([]any)
		require.Len(t, emojis, 1)
		assert.Equal(t, "e1", emojis[0].(map[string]any)["id"])
	})
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

// --- thin nil-repo smoke tests ---
//
// nil repo 経路で 204 / 200 が返ることを担保する軽量テスト。

func TestDriveCleanRemoteFiles(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.DriveCleanRemoteFiles, `{}`, adminUser).Code)
}

func TestDriveCleanup(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.DriveCleanup, `{}`, adminUser).Code)
}

func TestDriveFilesAdmin(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.DriveFiles, `{}`, adminUser).Code)
}

func TestDriveShowFile_Empty(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusBadRequest, doPost(h.DriveShowFile, `{}`, adminUser).Code)
}

func TestDriveShowFile_NotFound(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNotFound, doPost(h.DriveShowFile, `{"fileId":"ghost"}`, adminUser).Code)
}

func TestEmojiAddAliasesBulk(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.EmojiAddAliasesBulk, `{}`, adminUser).Code)
}

func TestEmojiCopy(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// emojiId 欠落は 400
	assert.Equal(t, http.StatusBadRequest, doPost(h.EmojiCopy, `{}`, adminUser).Code)
}

func TestEmojiDeleteBulk(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.EmojiDeleteBulk, `{}`, adminUser).Code)
}

func TestEmojiListRemote(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusOK, doPost(h.EmojiListRemote, `{}`, adminUser).Code)
}

func TestEmojiRemoveAliasesBulk(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.EmojiRemoveAliasesBulk, `{}`, adminUser).Code)
}

func TestEmojiSetAliasesBulk(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.EmojiSetAliasesBulk, `{}`, adminUser).Code)
}

func TestEmojiSetCategoryBulk(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.EmojiSetCategoryBulk, `{}`, adminUser).Code)
}

func TestEmojiSetLicenseBulk(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusNoContent, doPost(h.EmojiSetLicenseBulk, `{}`, adminUser).Code)
}

// --- EmojiImportZip 詳細 (旧 handler_stubs_test.go から移動) ---

func TestEmojiImportZip_NoFileID(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// fileId missing → 400 InvalidParam
	assert.Equal(t, http.StatusBadRequest, doPost(h.EmojiImportZip, `{}`, adminUser).Code)
}

func TestEmojiImportZip_MalformedJSON(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusBadRequest, doPost(h.EmojiImportZip, `not-json`, adminUser).Code)
}

func TestEmojiImportZip_NoEnqueuer(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// enqueuer not set → 204 (no-op fallback so tests without worker still pass)
	assert.Equal(t, http.StatusNoContent, doPost(h.EmojiImportZip, `{"fileId":"f1"}`, adminUser).Code)
}

func TestEmojiImportZip_NilUser(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiImportEnqueuer(&stubEmojiImportEnqueuer{})
	assert.Equal(t, http.StatusNoContent, doPost(h.EmojiImportZip, `{"fileId":"f1"}`, nil).Code)
}

func TestEmojiImportZip_Success(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	stub := &stubEmojiImportEnqueuer{}
	h.SetEmojiImportEnqueuer(stub)
	rec := doPost(h.EmojiImportZip, `{"fileId":"f1"}`, adminUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "f1", stub.lastFileID)
	assert.Equal(t, "admin1", stub.lastUserID)
}

func TestEmojiImportZip_EnqueueFailure(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiImportEnqueuer(&stubEmojiImportEnqueuer{err: assertError{}})
	rec := doPost(h.EmojiImportZip, `{"fileId":"f1"}`, adminUser)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- moderation log assertions (#661 + #650) ---

func TestEmojiAdd_WritesModerationLog(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiRepo(testutil.NewMockEmojiRepository())
	repo := attachModLog(t, h)

	rec := doPost(h.EmojiAdd, `{"name":"smile","url":"https://example/smile.png"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "addCustomEmoji", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.NotEmpty(t, info["emojiId"])
	assert.NotNil(t, info["emoji"])
}

func TestEmojiUpdate_WritesModerationLog_WithExtendedFields(t *testing.T) {
	// #650 問題 2 + #661: Misskey 互換の license/isSensitive/localOnly が
	// 永続化されること、updateCustomEmoji log に before/after が入ること。
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	require.NoError(t, emojiRepo.Create(&model.Emoji{ID: "e1", Name: "smile"}))
	h.SetEmojiRepo(emojiRepo)
	repo := attachModLog(t, h)

	body := `{"id":"e1","name":"smile2","license":"CC0","isSensitive":true,"localOnly":true}`
	rec := doPost(h.EmojiUpdate, body, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// DB 更新の検証
	updated, err := emojiRepo.FindByID("e1")
	require.NoError(t, err)
	assert.Equal(t, "smile2", updated.Name)
	require.NotNil(t, updated.License)
	assert.Equal(t, "CC0", *updated.License)
	assert.True(t, updated.IsSensitive)
	assert.True(t, updated.LocalOnly)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "updateCustomEmoji", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.Equal(t, "e1", info["emojiId"])
	require.NotNil(t, info["before"])
	require.NotNil(t, info["after"])
}

func TestEmojiUpdate_NoSuchEmojiOnMissingID(t *testing.T) {
	// #650 問題 2: id が DB に無いケースは NO_SUCH_EMOJI を返す。以前は
	// UpdateFields が RowsAffected を見ずに常に nil 返却で 204 になっていた。
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiRepo(testutil.NewMockEmojiRepository())
	rec := doPost(h.EmojiUpdate, `{"id":"ghost","name":"x"}`, adminUser)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errField, ok := body["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "NO_SUCH_EMOJI", errField["code"])
}

func TestEmojiDelete_WritesModerationLog(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	require.NoError(t, emojiRepo.Create(&model.Emoji{ID: "e1", Name: "doomed"}))
	h.SetEmojiRepo(emojiRepo)
	repo := attachModLog(t, h)

	rec := doPost(h.EmojiDelete, `{"id":"e1"}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	logs := repo.Snapshot()
	assert.Equal(t, "deleteCustomEmoji", logs[0].Type)
	var info map[string]any
	require.NoError(t, json.Unmarshal(logs[0].Info, &info))
	assert.Equal(t, "e1", info["emojiId"])
	assert.NotNil(t, info["emoji"])
}

func TestEmojiCopy_WritesModerationLog(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	remoteHost := "remote.example"
	require.NoError(t, emojiRepo.Create(&model.Emoji{ID: "e1", Name: "wave", Host: &remoteHost}))
	h.SetEmojiRepo(emojiRepo)
	repo := attachModLog(t, h)

	rec := doPost(h.EmojiCopy, `{"emojiId":"e1"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 1 }, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, "addCustomEmoji", repo.Snapshot()[0].Type)
}

func TestEmojiDeleteBulk_WritesPerEmojiLog(t *testing.T) {
	// Misskey TS 互換: delete-bulk は対象絵文字数だけ deleteCustomEmoji
	// log を出す (CustomEmojiService.deleteBulk の for ループ相当)。
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	require.NoError(t, emojiRepo.Create(&model.Emoji{ID: "e1", Name: "a"}))
	require.NoError(t, emojiRepo.Create(&model.Emoji{ID: "e2", Name: "b"}))
	require.NoError(t, emojiRepo.Create(&model.Emoji{ID: "e3", Name: "c"}))
	h.SetEmojiRepo(emojiRepo)
	repo := attachModLog(t, h)

	rec := doPost(h.EmojiDeleteBulk, `{"ids":["e1","e2","e3"]}`, adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == 3 }, 500*time.Millisecond, 5*time.Millisecond)
	for _, l := range repo.Snapshot() {
		assert.Equal(t, "deleteCustomEmoji", l.Type)
	}
}

// TestEmojiDeleteBulk_BatchedSingleInsert guards the #671 contract: a bulk
// delete of N emoji results in exactly 1 CreateMany call (not N Create
// calls), so 数千件の operation でも N goroutine + N round-trips に
// fan-out しない。
func TestEmojiDeleteBulk_BatchedSingleInsert(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	emojiRepo := testutil.NewMockEmojiRepository()
	const n = 50
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("e%d", i)
		ids = append(ids, id)
		require.NoError(t, emojiRepo.Create(&model.Emoji{ID: id, Name: id}))
	}
	h.SetEmojiRepo(emojiRepo)
	repo := attachModLog(t, h)

	body, err := json.Marshal(map[string]any{"ids": ids})
	require.NoError(t, err)
	rec := doPost(h.EmojiDeleteBulk, string(body), adminUser)
	require.Equal(t, http.StatusNoContent, rec.Code)

	require.Eventually(t, func() bool { return len(repo.Snapshot()) == n }, 1*time.Second, 5*time.Millisecond)

	create, createMany := repo.CallCounts()
	assert.Equal(t, 0, create, "Create must not be called per-item (would defeat #671 batching)")
	assert.Equal(t, 1, createMany, "CreateMany must be called exactly once for the whole batch")
}
