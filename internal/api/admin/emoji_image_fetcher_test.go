package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/drive"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingStorage implements drive.Storage with deterministic URLs and an
// in-memory blob store. Used to give Upload a real backing storage in tests
// without spinning up the LocalStorage filesystem path.
type recordingStorage struct {
	objects map[string][]byte
}

func newRecordingStorage() *recordingStorage {
	return &recordingStorage{objects: map[string][]byte{}}
}

func (r *recordingStorage) Put(accessKey string, body io.Reader) (string, error) {
	b, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	r.objects[accessKey] = b
	return fmt.Sprintf("https://drive.local/%s", accessKey), nil
}

func (r *recordingStorage) Get(accessKey string) (io.ReadCloser, error) {
	b, ok := r.objects[accessKey]
	if !ok {
		return nil, drive.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (r *recordingStorage) Delete(accessKey string) error {
	delete(r.objects, accessKey)
	return nil
}

// We need a drive.Storage interface; use a minimal embedding fake. Build a
// real drive.Service with mock repositories so Upload exercises the full
// path including row creation.
func newDriveService(t *testing.T) (*drive.Service, *testutil.MockDriveFileRepository) {
	t.Helper()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	fileRepo := testutil.NewMockDriveFileRepository()
	folderRepo := testutil.NewMockDriveFolderRepository()
	return drive.NewService(fileRepo, folderRepo, newRecordingStorage(), idGen), fileRepo
}

func TestEmojiImageFetcher_FetchAndStore_Success(t *testing.T) {
	const pixel = "\x89PNG\r\n\x1a\n" // PNG magic so AnalyseFile detects image/png
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.Header.Get("Accept"), "image/")
		assert.Equal(t, "mk-go/test", r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(pixel))
	}))
	t.Cleanup(srv.Close)

	driveSvc, fileRepo := newDriveService(t)
	f := NewEmojiImageFetcher(&http.Client{Timeout: 5 * time.Second, Transport: srv.Client().Transport}, driveSvc, "mk-go/test")

	// nil user で「system 所有 drive file」として upload する (#670)。
	// Misskey TS uploadFromUrl({user: null}) と等価。
	df, err := f.FetchAndStore(context.Background(), srv.URL+"/x.png", nil, "happy")
	require.NoError(t, err)
	require.NotNil(t, df)
	assert.NotEmpty(t, df.URL)
	// drive レコードが永続化されており、UserID は nil (system-owned)
	require.Len(t, fileRepo.Files, 1)
	for _, file := range fileRepo.Files {
		assert.Nil(t, file.UserID, "system drive file must have UserID=nil")
		assert.Nil(t, file.UserHost, "system drive file must have UserHost=nil")
	}
}

func TestEmojiImageFetcher_InvalidScheme(t *testing.T) {
	driveSvc, _ := newDriveService(t)
	f := NewEmojiImageFetcher(&http.Client{}, driveSvc, "")
	_, err := f.FetchAndStore(context.Background(), "file:///etc/passwd", &model.User{ID: "u"}, "x")
	assert.Error(t, err)
}

func TestEmojiImageFetcher_EmptyURL(t *testing.T) {
	driveSvc, _ := newDriveService(t)
	f := NewEmojiImageFetcher(&http.Client{}, driveSvc, "")
	_, err := f.FetchAndStore(context.Background(), "", &model.User{ID: "u"}, "x")
	assert.Error(t, err)
}

func TestEmojiImageFetcher_NotWired(t *testing.T) {
	f := &EmojiImageFetcherImpl{} // both nil
	_, err := f.FetchAndStore(context.Background(), "https://x", &model.User{ID: "u"}, "x")
	assert.Error(t, err)
}

func TestEmojiImageFetcher_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	driveSvc, _ := newDriveService(t)
	f := NewEmojiImageFetcher(&http.Client{Transport: srv.Client().Transport}, driveSvc, "")
	_, err := f.FetchAndStore(context.Background(), srv.URL, &model.User{ID: "u"}, "x")
	assert.Error(t, err)
}

func TestEmojiImageFetcher_OversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// MaxEmojiImageBytes = 8 MiB; send 9 MiB to trigger the cap.
		_, _ = w.Write(make([]byte, 9<<20))
	}))
	t.Cleanup(srv.Close)

	driveSvc, _ := newDriveService(t)
	f := NewEmojiImageFetcher(&http.Client{Transport: srv.Client().Transport}, driveSvc, "")
	_, err := f.FetchAndStore(context.Background(), srv.URL, &model.User{ID: "u"}, "x")
	assert.Error(t, err)
}

func TestEmojiImageFetcher_DialFails(t *testing.T) {
	failingClient := &http.Client{Transport: roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("ssrf block: connection to private IP refused")
	})}
	driveSvc, _ := newDriveService(t)
	f := NewEmojiImageFetcher(failingClient, driveSvc, "")
	_, err := f.FetchAndStore(context.Background(), "http://10.0.0.1/img.png", &model.User{ID: "u"}, "x")
	assert.Error(t, err)
	// 内部詳細はそのまま返す (caller である EmojiCopy 側が log + 静的 message に丸める)
	assert.Contains(t, err.Error(), "fetch image")
}

// TestEmojiImageFetcher_EmptyNameFallsBackToURLPath は name="" のとき URL の
// 最終 path セグメントが drive file 名として使われる Misskey TS 互換 fallback
// を guard する。
func TestEmojiImageFetcher_EmptyNameFallsBackToURLPath(t *testing.T) {
	const pixel = "\x89PNG\r\n\x1a\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(pixel))
	}))
	t.Cleanup(srv.Close)

	driveSvc, fileRepo := newDriveService(t)
	f := NewEmojiImageFetcher(&http.Client{Timeout: 5 * time.Second, Transport: srv.Client().Transport}, driveSvc, "")

	df, err := f.FetchAndStore(context.Background(), srv.URL+"/path/to/x.png", &model.User{ID: "u"}, "")
	require.NoError(t, err)
	require.NotNil(t, df)
	// drive レコードが name="x.png" で作成される
	require.Len(t, fileRepo.Files, 1)
	for _, file := range fileRepo.Files {
		assert.Equal(t, "x.png", file.Name)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
