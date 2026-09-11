package emojiapplications_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/shiroha-a/mk/internal/api/emojiapplications"
	"github.com/shiroha-a/mk/internal/core/emojiapplication"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

type stubApps struct {
	rows      []model.EmojiApplication
	byID      map[string]*model.EmojiApplication
	err       error
	createErr error
	created   *model.EmojiApplication
	// lastUser records who the list was scoped to.
	lastUser string
}

func (s *stubApps) Create(a *model.EmojiApplication) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.created = a
	return nil
}
func (s *stubApps) FindByID(id string) (*model.EmojiApplication, error) {
	if a, ok := s.byID[id]; ok {
		// **コピーを返す。** 同じポインタだと service の書き換えが保存済みの
		// 行にも反映され、条件付き UPDATE の検証が成立しない。
		cp := *a
		return &cp, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (s *stubApps) List(string, int, string) ([]model.EmojiApplication, error) { return nil, nil }
func (s *stubApps) ListByUser(userID string, _ int, _ string) ([]model.EmojiApplication, error) {
	s.lastUser = userID
	return s.rows, s.err
}
func (s *stubApps) CountPending() (int64, error) { return 0, nil }
func (s *stubApps) UpdateIfPending(a *model.EmojiApplication) (bool, error) {
	cur, ok := s.byID[a.ID]
	if !ok || cur.Status != model.EmojiApplicationPending {
		return false, nil
	}
	s.byID[a.ID] = a
	return true, nil
}

func doPost(h func(echo.Context) error, body string, user *model.User) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if user != nil {
		c.Set(string(middleware.UserContextKey), user)
	}
	_ = h(c)
	return rec
}

var alice = &model.User{ID: "u1"}

// **一覧は必ず自分の分に絞る。** userId をリクエストから取る形にすると、
// 他人の申請 (却下理由を含む) が読めてしまう。
func TestListMineScopesToCaller(t *testing.T) {
	apps := &stubApps{rows: []model.EmojiApplication{{ID: "a1", Name: "sushi", Status: "pending"}}}
	h := emojiapplications.NewHandler(nil, apps, nil)

	rec := doPost(h.ListMine, `{"userId":"someone-else"}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "u1", apps.lastUser, "呼び出し元以外の申請を引いている")

	var body []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body, 1)
	require.Equal(t, "sushi", body[0]["name"])
}

func TestListMineRequiresAuth(t *testing.T) {
	h := emojiapplications.NewHandler(nil, &stubApps{}, nil)
	rec := doPost(h.ListMine, `{}`, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// **審査したモデレーターは出さない。** 誰が押したかは監査用で、申請者に
// 見せると個人への抗議に繋がりやすい。
func TestPackOmitsModerator(t *testing.T) {
	mod := "mod1"
	reason := "潰れて読めません"
	apps := &stubApps{rows: []model.EmojiApplication{{
		ID: "a1", Name: "kusa", Status: "rejected",
		ProcessedByID: &mod, RejectReason: &reason,
	}}}
	h := emojiapplications.NewHandler(nil, apps, nil)

	rec := doPost(h.ListMine, `{}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "mod1", "審査したモデレーターが漏れている")
	// 却下理由は出す。直して出し直すための唯一の手がかり。
	require.Contains(t, rec.Body.String(), reason)
}

func TestListMineSurfacesFailure(t *testing.T) {
	apps := &stubApps{err: gorm.ErrInvalidDB}
	h := emojiapplications.NewHandler(nil, apps, nil)
	rec := doPost(h.ListMine, `{}`, alice)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// wiredHandler builds a handler with a real service so the auth and validation
// branches are actually reachable.
//
// **svc を nil にしたまま「認証を見た」と書かない。** handler は svc == nil を
// 先に見るので、nil のままでは認証も検証も一度も通らない — 名前だけ合っている
// 空のテストになる。
func wiredHandler(apps *stubApps) *emojiapplications.Handler {
	svc := emojiapplication.NewService(apps, &stubEmojiLookup{}, ownedFile(), &stubIDGen{}, nil, nil)
	return emojiapplications.NewHandler(svc, apps, nil)
}

type stubEmojiLookup struct{}

func (stubEmojiLookup) FindByNameAndHost(string, *string) (*model.Emoji, error) {
	return nil, gorm.ErrRecordNotFound
}

// ownedFile resolves a drive file owned by alice.
func ownedFile() *stubFiles {
	owner := alice.ID
	return &stubFiles{file: &model.DriveFile{ID: "f1", UserID: &owner, Type: "image/png"}}
}

type stubIDGen struct{}

func (stubIDGen) Generate(time.Time) string { return "new1" }

func TestCreateRequiresAuth(t *testing.T) {
	rec := doPost(wiredHandler(&stubApps{}).Create, `{"name":"sushi","license":"自作","fileId":"f1"}`, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestCreateValidatesName(t *testing.T) {
	rec := doPost(wiredHandler(&stubApps{}).Create, `{"name":"su-shi","license":"自作","fileId":"f1"}`, alice)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "INVALID_EMOJI_NAME")
}

func TestCreateRequiresLicense(t *testing.T) {
	rec := doPost(wiredHandler(&stubApps{}).Create, `{"name":"sushi","fileId":"f1"}`, alice)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "LICENSE_REQUIRED")
}

// **申請者は呼び出し元で決まる (レビュー H7)。** ボディの userId を見る形に
// 変えると、他人名義の申請を作られて被害者の一覧に出る。stubApps が Create の
// 引数を捨てていたので、以前はこの変異が全テスト緑で通っていた。
func TestCreateRecordsCallerAsApplicant(t *testing.T) {
	apps := &stubApps{}
	rec := doPost(wiredHandler(apps).Create,
		`{"name":"sushi","license":"自作","fileId":"f1","userId":"victim"}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, apps.created, "申請が保存されていない")
	require.Equal(t, alice.ID, apps.created.UserID,
		"ボディの userId が使われている。他人名義の申請を作られる")
}

func TestCreateSucceeds(t *testing.T) {
	rec := doPost(wiredHandler(&stubApps{}).Create,
		`{"name":"sushi","license":"自作","fileId":"f1","category":"たべもの"}`, alice)
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "sushi", body["name"])
	require.Equal(t, "pending", body["status"])
}

func TestCancelRequiresAuth(t *testing.T) {
	rec := doPost(wiredHandler(&stubApps{}).Cancel, `{"applicationId":"a1"}`, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestCancelRequiresID(t *testing.T) {
	rec := doPost(wiredHandler(&stubApps{}).Cancel, `{}`, alice)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// **他人の申請は「無い」と答える。** 403 を返すと、存在する申請 ID を
// 総当たりで数えられる。
func TestCancelHidesOthersApplications(t *testing.T) {
	apps := &stubApps{byID: map[string]*model.EmojiApplication{
		"a1": {ID: "a1", UserID: "someone-else", Status: model.EmojiApplicationPending},
	}}
	rec := doPost(wiredHandler(apps).Cancel, `{"applicationId":"a1"}`, alice)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "NO_SUCH_APPLICATION")
}

// stubFiles resolves drive files for the preview URL.
type stubFiles struct {
	file *model.DriveFile
	err  error
}

func (s *stubFiles) FindByID(string) (*model.DriveFile, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.file == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return s.file, nil
}

// **残りの error を種別ごとに落とす。** すべて 400 INVALID_PARAM に丸めると、
// 利用者には「何か間違っている」としか伝わらない。
func TestCreateErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*stubApps, *stubEmojiLookupWithHit)
		body   string
		code   int
		expect string
	}{
		{"同名の絵文字がある", func(_ *stubApps, e *stubEmojiLookupWithHit) { e.hit = true },
			`{"name":"sushi","license":"自作","fileId":"f1"}`, http.StatusBadRequest, "DUPLICATE_NAME"},
		{"審査中の重複", func(a *stubApps, _ *stubEmojiLookupWithHit) {
			a.createErr = repository.ErrEmojiApplicationDuplicatePending
		},
			`{"name":"sushi","license":"自作","fileId":"f1"}`, http.StatusBadRequest, "ALREADY_REQUESTED"},
		{"画像が無い", func(*stubApps, *stubEmojiLookupWithHit) {},
			`{"name":"sushi","license":"自作"}`, http.StatusBadRequest, "NO_SUCH_FILE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apps := &stubApps{}
			lookup := &stubEmojiLookupWithHit{}
			tc.setup(apps, lookup)
			svc := emojiapplication.NewService(apps, lookup, ownedFile(), &stubIDGen{}, nil, nil)
			h := emojiapplications.NewHandler(svc, apps, nil)

			rec := doPost(h.Create, tc.body, alice)
			require.Equal(t, tc.code, rec.Code)
			require.Contains(t, rec.Body.String(), tc.expect)
		})
	}
}

type stubEmojiLookupWithHit struct{ hit bool }

func (s *stubEmojiLookupWithHit) FindByNameAndHost(string, *string) (*model.Emoji, error) {
	if s.hit {
		return &model.Emoji{ID: "e1"}, nil
	}
	return nil, gorm.ErrRecordNotFound
}

// DB 障害は 500 のまま残す (#2792)。not-found に丸めない。
func TestCreateSurfacesInternalError(t *testing.T) {
	apps := &stubApps{createErr: gorm.ErrInvalidDB}
	svc := emojiapplication.NewService(apps, &stubEmojiLookup{}, ownedFile(), &stubIDGen{}, nil, nil)
	rec := doPost(emojiapplications.NewHandler(svc, apps, nil).Create,
		`{"name":"sushi","license":"自作","fileId":"f1"}`, alice)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestCancelSucceeds(t *testing.T) {
	apps := &stubApps{byID: map[string]*model.EmojiApplication{
		"a1": {ID: "a1", UserID: "u1", Status: model.EmojiApplicationPending},
	}}
	rec := doPost(wiredHandler(apps).Cancel, `{"applicationId":"a1"}`, alice)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestCancelAlreadyProcessed(t *testing.T) {
	apps := &stubApps{byID: map[string]*model.EmojiApplication{
		"a1": {ID: "a1", UserID: "u1", Status: model.EmojiApplicationApproved},
	}}
	rec := doPost(wiredHandler(apps).Cancel, `{"applicationId":"a1"}`, alice)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "ALREADY_PROCESSED")
}

// **画像が消えていても一覧は出す。** url が空になるだけで、却下理由などの
// 経緯は読めるべき。
func TestPreviewURLTolerable(t *testing.T) {
	fileID := "f1"
	apps := &stubApps{rows: []model.EmojiApplication{{ID: "a1", Name: "sushi", FileID: &fileID}}}

	t.Run("drive にある", func(t *testing.T) {
		h := emojiapplications.NewHandler(nil, apps, &stubFiles{file: &model.DriveFile{URL: "https://x/f1.png"}})
		rec := doPost(h.ListMine, `{}`, alice)
		require.Contains(t, rec.Body.String(), "https://x/f1.png")
	})

	t.Run("drive から消えている", func(t *testing.T) {
		h := emojiapplications.NewHandler(nil, apps, &stubFiles{})
		rec := doPost(h.ListMine, `{}`, alice)
		require.Equal(t, http.StatusOK, rec.Code, "画像が無いだけで一覧が落ちている")
		var body []map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "", body[0]["url"])
	})
}

// **新しく到達可能になった error も種別ごとに落とす。** H2 / M8 で
// create から TOO_LONG / UNSUPPORTED_FILE_TYPE / NO_SUCH_FILE が返るように
// なったので、500 に潰していないことを固定する。
func TestCreateErrorMappingForFileAndLength(t *testing.T) {
	owner := alice.ID
	cases := []struct {
		name   string
		file   *model.DriveFile
		body   string
		expect string
	}{
		{"長すぎるライセンス",
			&model.DriveFile{ID: "f1", UserID: &owner, Type: "image/png"},
			`{"name":"sushi","fileId":"f1","license":"` + strings.Repeat("x", 1100) + `"}`,
			"TOO_LONG"},
		{"画像でない",
			&model.DriveFile{ID: "f1", UserID: &owner, Type: "video/mp4"},
			`{"name":"sushi","fileId":"f1","license":"自作"}`,
			"UNSUPPORTED_FILE_TYPE"},
		{"他人のファイル",
			&model.DriveFile{ID: "f1", UserID: func() *string { s := "someone"; return &s }(), Type: "image/png"},
			`{"name":"sushi","fileId":"f1","license":"自作"}`,
			"NO_SUCH_FILE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apps := &stubApps{}
			svc := emojiapplication.NewService(apps, &stubEmojiLookup{},
				&stubFiles{file: tc.file}, &stubIDGen{}, nil, nil)
			rec := doPost(emojiapplications.NewHandler(svc, apps, nil).Create, tc.body, alice)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), tc.expect)
			require.Nil(t, apps.created, "検証を通さずに申請が保存されている")
		})
	}
}

// stubRemoteLookup answers host-qualified lookups so remote requests can pass.
type stubRemoteLookup struct{ known bool }

func (s *stubRemoteLookup) FindByNameAndHost(_ string, host *string) (*model.Emoji, error) {
	if host != nil {
		if s.known {
			return &model.Emoji{ID: "e-remote"}, nil
		}
		return nil, gorm.ErrRecordNotFound
	}
	return nil, gorm.ErrRecordNotFound
}

// **リモートのインポート申請も error を種別ごとに落とす (#2935)。**
func TestCreateRemoteErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		known  bool
		body   string
		expect string
	}{
		{"知らないリモート絵文字", false,
			`{"kind":"remote","name":"sushi","license":"x","remoteHost":"example.com","remoteName":"s"}`,
			"NO_SUCH_EMOJI"},
		{"host が無い", true,
			`{"kind":"remote","name":"sushi","license":"x","remoteName":"s"}`,
			"INVALID_PARAM"},
		{"未知の kind", true,
			`{"kind":"whatever","name":"sushi","license":"x"}`,
			"INVALID_PARAM"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apps := &stubApps{}
			svc := emojiapplication.NewService(apps, &stubRemoteLookup{known: tc.known},
				ownedFile(), &stubIDGen{}, nil, nil)
			rec := doPost(emojiapplications.NewHandler(svc, apps, nil).Create, tc.body, alice)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), tc.expect)
			require.Nil(t, apps.created, "検証を通さずに申請が保存されている")
		})
	}
}

// リモートの申請が成功すると、取り込み元が応答に出ること。
func TestCreateRemoteSucceeds(t *testing.T) {
	apps := &stubApps{}
	svc := emojiapplication.NewService(apps, &stubRemoteLookup{known: true},
		ownedFile(), &stubIDGen{}, nil, nil)
	rec := doPost(emojiapplications.NewHandler(svc, apps, nil).Create,
		`{"kind":"remote","name":"sushi","license":"x","remoteHost":"example.com","remoteName":"sushi_remote"}`, alice)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "remote", body["kind"])
	require.Equal(t, "example.com", body["remoteHost"])
	require.Equal(t, "sushi_remote", body["remoteName"])
}
