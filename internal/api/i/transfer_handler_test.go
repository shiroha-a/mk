package i

import (
	"errors"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
)

// stubTransferEnqueuer records calls and optionally returns an error.
type stubTransferEnqueuer struct {
	exportCalls []queue.ExportPayload
	importCalls []queue.ImportPayload
	exportErr   error
	importErr   error
}

func (s *stubTransferEnqueuer) EnqueueExport(p queue.ExportPayload) error {
	if s.exportErr != nil {
		return s.exportErr
	}
	s.exportCalls = append(s.exportCalls, p)
	return nil
}

func (s *stubTransferEnqueuer) EnqueueImport(p queue.ImportPayload) error {
	if s.importErr != nil {
		return s.importErr
	}
	s.importCalls = append(s.importCalls, p)
	return nil
}

// ownedFileID / ownerID は import 系テストが使う「u1 が所有する drive file」。
const (
	ownedFileID = "f1"
	ownerID     = "u1"
)

func newTransferHandler() (*Handler, *stubTransferEnqueuer) {
	h, enq, _ := newTransferHandlerWithDrive()
	return h, enq
}

// newTransferHandlerWithDrive wires a drive file repo seeded with a file owned
// by ownerID (ownedFileID) so that the import-* ownership gate (#1555) passes
// for the happy path. Negative cases insert/lookup other files via the returned
// mock.
func newTransferHandlerWithDrive() (*Handler, *stubTransferEnqueuer, *testutil.MockDriveFileRepository) {
	h, _, _, _ := newTestHandler(&testing.T{})
	enq := &stubTransferEnqueuer{}
	h.SetTransferEnqueuer(enq)
	driveRepo := testutil.NewMockDriveFileRepository()
	uid := ownerID
	driveRepo.Files[ownedFileID] = &model.DriveFile{ID: ownedFileID, UserID: &uid}
	h.SetDriveFileRepo(driveRepo)
	return h, enq, driveRepo
}

// --- Export handlers ---

func TestExport_NotesNoAuthIfEnqueuerMissing(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	rec := post(h.ExportNotes, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestExport_AllTypesEnqueue(t *testing.T) {
	cases := []struct {
		name    string
		handler func(h *Handler) func(c any) error
	}{}
	// map each endpoint to its expected payload type
	h, enq := newTransferHandler()
	user := &model.User{ID: "u1"}

	type ex struct {
		name string
		fn   func(c *Handler) func() error
		kind string
	}

	// Use a simple test invocation approach: call handler via post helper.
	assertOK := func(recCode int) {
		assert.Equal(t, http.StatusNoContent, recCode)
	}

	// notes
	assertOK(post(h.ExportNotes, `{}`, user).Code)
	// following
	assertOK(post(h.ExportFollowing, `{}`, user).Code)
	// blocking
	assertOK(post(h.ExportBlocking, `{}`, user).Code)
	// mute
	assertOK(post(h.ExportMute, `{}`, user).Code)
	// favorites
	assertOK(post(h.ExportFavorites, `{}`, user).Code)
	// user-lists
	assertOK(post(h.ExportUserLists, `{}`, user).Code)
	// antennas
	assertOK(post(h.ExportAntennas, `{}`, user).Code)
	// clips
	assertOK(post(h.ExportClips, `{}`, user).Code)

	assert.Len(t, enq.exportCalls, 8)
	// verify the types are distinct
	seen := make(map[string]bool)
	for _, c := range enq.exportCalls {
		seen[c.Type] = true
		assert.Equal(t, "u1", c.UserID)
	}
	assert.Len(t, seen, 8)
	_ = cases
}

func TestExport_EnqueuerFailure(t *testing.T) {
	h, enq := newTransferHandler()
	enq.exportErr = errors.New("enq boom")
	rec := post(h.ExportNotes, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Import handlers ---

func TestImport_AllTypesEnqueue(t *testing.T) {
	h, enq := newTransferHandler()
	user := &model.User{ID: "u1"}

	assertOK := func(recCode int) {
		assert.Equal(t, http.StatusNoContent, recCode)
	}

	assertOK(post(h.ImportFollowing, `{"fileId":"f1"}`, user).Code)
	assertOK(post(h.ImportBlocking, `{"fileId":"f1"}`, user).Code)
	assertOK(post(h.ImportMuting, `{"fileId":"f1"}`, user).Code)
	assertOK(post(h.ImportUserLists, `{"fileId":"f1"}`, user).Code)
	assertOK(post(h.ImportAntennas, `{"fileId":"f1"}`, user).Code)

	assert.Len(t, enq.importCalls, 5)
	for _, c := range enq.importCalls {
		assert.Equal(t, "u1", c.UserID)
		assert.Equal(t, "f1", c.FileID)
	}
}

func TestImport_MissingFileID(t *testing.T) {
	h, _ := newTransferHandler()
	rec := post(h.ImportFollowing, `{}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestImport_NoEnqueuer(t *testing.T) {
	// drive repo は所有 file を保持させ、ownership gate を通過させたうえで
	// enqueuer 未配線の 500 経路を踏ませる。
	h, _, _, _ := newTestHandler(t)
	driveRepo := testutil.NewMockDriveFileRepository()
	uid := ownerID
	driveRepo.Files[ownedFileID] = &model.DriveFile{ID: ownedFileID, UserID: &uid}
	h.SetDriveFileRepo(driveRepo)
	rec := post(h.ImportFollowing, `{"fileId":"f1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestImport_EnqueuerError(t *testing.T) {
	h, enq := newTransferHandler()
	enq.importErr = errors.New("enq boom")
	rec := post(h.ImportFollowing, `{"fileId":"f1"}`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestImport_InvalidJSON(t *testing.T) {
	h, _ := newTransferHandler()
	rec := post(h.ImportFollowing, `not json`, &model.User{ID: "u1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// 他人所有の drive file を指定すると NO_SUCH_FILE で弾かれ、enqueue されない
// (cross-user import ownership bypass #1555 の回帰防止)。全 import handler で
// 同じ fail-closed 挙動を確認する。
func TestImport_OtherUsersFileRejected(t *testing.T) {
	for name, makeFn := range map[string]func(*Handler) func(c echo.Context) error{
		"following":  func(h *Handler) func(c echo.Context) error { return h.ImportFollowing },
		"blocking":   func(h *Handler) func(c echo.Context) error { return h.ImportBlocking },
		"muting":     func(h *Handler) func(c echo.Context) error { return h.ImportMuting },
		"user-lists": func(h *Handler) func(c echo.Context) error { return h.ImportUserLists },
		"antennas":   func(h *Handler) func(c echo.Context) error { return h.ImportAntennas },
	} {
		t.Run(name, func(t *testing.T) {
			h, enq, driveRepo := newTransferHandlerWithDrive()
			// otherID 所有の file を別 ID で seed する。
			otherID := "u2"
			driveRepo.Files["other"] = &model.DriveFile{ID: "other", UserID: &otherID}
			rec := post(makeFn(h), `{"fileId":"other"}`, &model.User{ID: ownerID})
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "NO_SUCH_FILE")
			// 本家は import endpoint ごとに固有の noSuchFile UUID を持つ (#1555)。
			wantID := map[string]string{
				"following":  "b98644cf-a5ac-4277-a502-0b8054a709a3",
				"blocking":   "ebb53e5f-6574-9c0c-0b92-7ca6def56d7e",
				"muting":     "e674141e-bd2a-ba85-e616-aefb187c9c2a",
				"user-lists": "ea9cc34f-c415-4bc6-a6fe-28ac40357049",
				"antennas":   "3b71d086-c3fa-431c-b01d-ded65a777172",
			}[name]
			assert.Contains(t, rec.Body.String(), wantID, "per-endpoint noSuchFile UUID must match upstream")
			assert.Empty(t, enq.importCalls)
		})
	}
}

// 不在 fileId は NO_SUCH_FILE で弾かれる (存在判定 oracle を作らないため
// 他人所有と同一 shape)。
func TestImport_MissingFileRejected(t *testing.T) {
	h, enq, _ := newTransferHandlerWithDrive()
	rec := post(h.ImportFollowing, `{"fileId":"ghost"}`, &model.User{ID: ownerID})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_FILE")
	assert.Empty(t, enq.importCalls)
}

// userId NULL の system file (emoji copy / import zip 保管先等) も所有者では
// ないので NO_SUCH_FILE。NULL handling を欠くと nil-deref か誤許可になる。
func TestImport_SystemFileRejected(t *testing.T) {
	h, enq, driveRepo := newTransferHandlerWithDrive()
	driveRepo.Files["sys"] = &model.DriveFile{ID: "sys", UserID: nil}
	rec := post(h.ImportFollowing, `{"fileId":"sys"}`, &model.User{ID: ownerID})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_FILE")
	assert.Empty(t, enq.importCalls)
}

// drive repo 未配線時は fail-closed (NO_SUCH_FILE)。所有権検証ができない以上
// import を通すと cross-user read が成立するため。
func TestImport_NoDriveRepoFailsClosed(t *testing.T) {
	h, _, _ := newTransferHandlerWithDrive()
	h.SetDriveFileRepo(nil)
	rec := post(h.ImportFollowing, `{"fileId":"f1"}`, &model.User{ID: ownerID})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "NO_SUCH_FILE")
}

// 自分所有の file は import が enqueue される (happy path / 回帰防止)。
func TestImport_OwnedFileAccepted(t *testing.T) {
	h, enq, _ := newTransferHandlerWithDrive()
	rec := post(h.ImportFollowing, `{"fileId":"f1"}`, &model.User{ID: ownerID})
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Len(t, enq.importCalls, 1)
	assert.Equal(t, ownedFileID, enq.importCalls[0].FileID)
	assert.Equal(t, ownerID, enq.importCalls[0].UserID)
}
