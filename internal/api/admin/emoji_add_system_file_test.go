package admin_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	coredrive "github.com/shiroha-a/mk/internal/core/drive"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/safehttp"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `admin/emoji/add` が system 所有の複製を作る経路 (#2999) のテスト。
//
// 直している失敗形は「**操作者が自分の drive ファイルを消すと絵文字が壊れる**」で、
// 申請の承認 (#2966) / リモートの複製 (#670) は既に system 所有にしてある。

// systemCopy is the drive file a wired fetcher hands back.
func systemCopy(id, url, webURL, webType string) *model.DriveFile {
	df := &model.DriveFile{ID: id, Type: "image/png", URL: url}
	if webURL != "" {
		df.WebpublicURL = &webURL
	}
	if webType != "" {
		df.WebpublicType = &webType
	}
	return df
}

// ownedDriveFile returns a drive row owned by a local user (= the shape
// `admin/emoji/add` normally receives: モデレーターが直前に上げたファイル)。
func ownedDriveFile(t *testing.T, h *apiadmin.Handler, f *model.DriveFile) *testutil.MockDriveFileRepository {
	t.Helper()
	dr := testutil.NewMockDriveFileRepository()
	require.NoError(t, dr.Create(f))
	h.SetDriveFileRepo(dr)
	return dr
}

func errorID(t *testing.T, body []byte) (code, id string) {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
			ID   string `json:"id"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	return resp.Error.Code, resp.Error.ID
}

// 利用者所有のファイルを指定すると、絵文字は複製のほうを指す。
//
// **元のファイルの URL がどこにも残らないことまで見る。** originalUrl だけを見て
// いると、publicUrl が元ファイル側に残る実装 (= 元を消すと表示が壊れる) を通す。
func TestEmojiAdd_FileIDCopiesToSystemOwnedFile(t *testing.T) {
	h, repo := setupEmojiHandler(t)
	owner := "u1"
	ownedDriveFile(t, h, &model.DriveFile{
		ID: "f_img", UserID: &owner, Type: "image/png", URL: "https://example/user/orig.png",
	})
	fetcher := &fakeEmojiImageFetcher{
		copyDF: systemCopy("sys1", "https://example/system/copy.png", "https://example/system/copy.webp", "image/webp"),
	}
	h.SetEmojiImageFetcher(fetcher)

	rec := doPost(h.EmojiAdd, `{"name":"happy","fileId":"f_img","isSensitive":true}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, fetcher.copyCalls, 1, "利用者所有のファイルは複製しなければならない")
	assert.Equal(t, "f_img", fetcher.copyCalls[0].ID, "複製元は指定された drive ファイル")
	assert.Equal(t, "happy", fetcher.copyNames[0], "複製の名前は絵文字名")
	assert.Equal(t, []bool{true}, fetcher.copySensitive, "isSensitive を複製にも渡す")

	got, err := repo.FindByNameAndHost("happy", nil)
	require.NoError(t, err)
	// 不変条件 (#722): originalUrl は必ず drive_file.url と一致する。webpublic を
	// 入れると DeleteOrphans の guard が外れて複製が消える。
	assert.Equal(t, "https://example/system/copy.png", got.OriginalURL)
	assert.Equal(t, "https://example/system/copy.webp", got.PublicURL)
	require.NotNil(t, got.Type)
	assert.Equal(t, "image/webp", *got.Type)
	assert.NotContains(t, got.OriginalURL, "/user/", "元ファイルの URL を参照し続けてはならない")
	assert.NotContains(t, got.PublicURL, "/user/", "元ファイルの URL を参照し続けてはならない")
	assert.Empty(t, fetcher.deletedIDs, "成功したのに複製を消している")
}

// 既に system 所有のファイルは複製しない (利用者の drive 操作から既に切れている)。
func TestEmojiAdd_FileIDSkipsCopyForSystemOwnedFile(t *testing.T) {
	h, repo := setupEmojiHandler(t)
	ownedDriveFile(t, h, &model.DriveFile{
		ID: "f_sys", Type: "image/png", URL: "https://example/system/already.png",
	})
	fetcher := &fakeEmojiImageFetcher{copyDF: systemCopy("sys2", "https://example/system/copy.png", "", "")}
	h.SetEmojiImageFetcher(fetcher)

	rec := doPost(h.EmojiAdd, `{"name":"happy","fileId":"f_sys"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, fetcher.copyCalls, "system 所有のファイルを二重に複製している")
	got, err := repo.FindByNameAndHost("happy", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://example/system/already.png", got.OriginalURL)
}

// `userId` が NULL でも `userHost` があるリモートキャッシュは system 所有ではない。
// `admin/drive/clean-remote-files` で消えるので、複製しないと同じ形で壊れる。
func TestEmojiAdd_FileIDCopiesRemoteCachedFile(t *testing.T) {
	h, repo := setupEmojiHandler(t)
	host := "remote.example"
	ownedDriveFile(t, h, &model.DriveFile{
		ID: "f_remote", UserHost: &host, Type: "image/png", URL: "https://remote.example/cached.png",
	})
	fetcher := &fakeEmojiImageFetcher{copyDF: systemCopy("sys3", "https://example/system/copy.png", "", "")}
	h.SetEmojiImageFetcher(fetcher)

	rec := doPost(h.EmojiAdd, `{"name":"happy","fileId":"f_remote"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, fetcher.copyCalls, 1, "リモートキャッシュは system 所有ではない")
	got, err := repo.FindByNameAndHost("happy", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://example/system/copy.png", got.OriginalURL)
}

// 複製した実体の MIME が allowlist 外なら弾き、作った複製を片付ける。
// `Upload` は AnalyseFile でバイト列から型を引き直すので、行の宣言と実体が
// ずれていると allowlist 外の型が絵文字として登録される。
func TestEmojiAdd_FileIDRejectsCopiedNonImageAndCleansUp(t *testing.T) {
	h, repo := setupEmojiHandler(t)
	owner := "u1"
	ownedDriveFile(t, h, &model.DriveFile{
		ID: "f_img", UserID: &owner, Type: "image/png", URL: "https://example/user/orig.png",
	})
	bad := systemCopy("sys4", "https://example/system/copy.bin", "", "")
	bad.Type = "text/plain"
	fetcher := &fakeEmojiImageFetcher{copyDF: bad}
	h.SetEmojiImageFetcher(fetcher)

	rec := doPost(h.EmojiAdd, `{"name":"happy","fileId":"f_img"}`, adminUser)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	code, _ := errorID(t, rec.Body.Bytes())
	assert.Equal(t, "UNSUPPORTED_FILE_TYPE", code)
	assert.Equal(t, []string{"sys4"}, fetcher.deletedIDs, "弾いた複製は誰からも参照されないので消す")
	_, err := repo.FindByNameAndHost("happy", nil)
	assert.Error(t, err, "弾いたのに絵文字が作られている")
}

// 複製の失敗は種別ごとに分ける (#2792)。実体がもう無い / 大きすぎるは 400、
// ストレージや DB の障害は 500。error id は承認経路と同じものを返す。
func TestEmojiAdd_FileIDCopyFailureMapping(t *testing.T) {
	cases := []struct {
		name     string
		copyErr  error
		status   int
		code     string
		id       string
		deleteOK bool
	}{
		{
			name:    "object gone",
			copyErr: fmt.Errorf("read source file: %w", coredrive.ErrObjectNotFound),
			status:  http.StatusBadRequest,
			code:    "NO_SUCH_FILE",
			id:      "fc46b5a4-6b92-4c33-ac66-b806659bb5cf",
		},
		{
			name:    "too large",
			copyErr: fmt.Errorf("read source file: %w", safehttp.ErrResponseTooLarge),
			status:  http.StatusBadRequest,
			code:    "EMOJI_IMAGE_TOO_LARGE",
			id:      "6b1d5f0a-3c9e-4f27-9a4d-7e2b8c1f0d64",
		},
		{
			name:    "storage failure",
			copyErr: assert.AnError,
			status:  http.StatusInternalServerError,
			code:    "INTERNAL_ERROR",
			id:      "c2f7a3d1-58be-4e09-bb26-0d4a9e7f3c15",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := setupEmojiHandler(t)
			owner := "u1"
			ownedDriveFile(t, h, &model.DriveFile{
				ID: "f_img", UserID: &owner, Type: "image/png", URL: "https://example/user/orig.png",
			})
			fetcher := &fakeEmojiImageFetcher{copyErr: tc.copyErr}
			h.SetEmojiImageFetcher(fetcher)

			rec := doPost(h.EmojiAdd, `{"name":"happy","fileId":"f_img"}`, adminUser)
			require.Equal(t, tc.status, rec.Code)
			code, id := errorID(t, rec.Body.Bytes())
			assert.Equal(t, tc.code, code)
			assert.Equal(t, tc.id, id, "承認経路と同じ error id を返す")
			// **元ファイルを参照する絵文字を作らない。** 作ると直そうとしている
			// 依存をそのまま残すことになる。
			_, err := repo.FindByNameAndHost("happy", nil)
			assert.Error(t, err, "複製に失敗したのに絵文字が作られている")
		})
	}
}

// fetcher が nil, nil を返しても panic させない (実装の契約違反を 500 に落とす)。
func TestEmojiAdd_FileIDCopyReturnsNoFile(t *testing.T) {
	h, repo := setupEmojiHandler(t)
	owner := "u1"
	ownedDriveFile(t, h, &model.DriveFile{
		ID: "f_img", UserID: &owner, Type: "image/png", URL: "https://example/user/orig.png",
	})
	h.SetEmojiImageFetcher(&fakeEmojiImageFetcher{})

	rec := doPost(h.EmojiAdd, `{"name":"happy","fileId":"f_img"}`, adminUser)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	_, err := repo.FindByNameAndHost("happy", nil)
	assert.Error(t, err)
}

// 絵文字の作成に失敗したら複製を片付ける (誰からも参照されない孤児になる)。
func TestEmojiAdd_CreateFailureDeletesSystemCopy(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.SetEmojiRepo(&failingCreateEmojiRepo{testutil.NewMockEmojiRepository()})
	owner := "u1"
	ownedDriveFile(t, h, &model.DriveFile{
		ID: "f_img", UserID: &owner, Type: "image/png", URL: "https://example/user/orig.png",
	})
	fetcher := &fakeEmojiImageFetcher{copyDF: systemCopy("sys5", "https://example/system/copy.png", "", "")}
	h.SetEmojiImageFetcher(fetcher)

	rec := doPost(h.EmojiAdd, `{"name":"happy","fileId":"f_img"}`, adminUser)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, []string{"sys5"}, fetcher.deletedIDs, "作成に失敗した複製が孤児として残っている")
}

// legacy の `url` 直接指定は取り込まない (所有者の概念が無く、外部 URL も指せる)。
func TestEmojiAdd_URLPathDoesNotCopy(t *testing.T) {
	h, repo := setupEmojiHandler(t)
	fetcher := &fakeEmojiImageFetcher{copyDF: systemCopy("sys6", "https://example/system/copy.png", "", "")}
	h.SetEmojiImageFetcher(fetcher)

	rec := doPost(h.EmojiAdd, `{"name":"happy","url":"https://cdn.example/happy.png"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, fetcher.copyCalls)
	assert.Empty(t, fetcher.calls, "url 経路で HTTP 取り込みを走らせない")
	got, err := repo.FindByNameAndHost("happy", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example/happy.png", got.OriginalURL)
}

// fetcher 未配線なら drive の URL をそのまま使う (EmojiCopy と同じ degradation)。
// 未配線そのものは router の起動時チェックが知らせる。
func TestEmojiAdd_FetcherUnsetKeepsDriveURL(t *testing.T) {
	h, repo := setupEmojiHandler(t)
	owner := "u1"
	ownedDriveFile(t, h, &model.DriveFile{
		ID: "f_img", UserID: &owner, Type: "image/png", URL: "https://example/user/orig.png",
	})

	rec := doPost(h.EmojiAdd, `{"name":"happy","fileId":"f_img"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)
	got, err := repo.FindByNameAndHost("happy", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://example/user/orig.png", got.OriginalURL)
}
