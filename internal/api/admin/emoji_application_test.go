package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/shiroha-a/mk/internal/core/emojiapplication"
	"github.com/shiroha-a/mk/internal/model"
)

// stubEmojiReviewer records calls and returns canned results.
type stubEmojiReviewer struct {
	app     *model.EmojiApplication
	err     error
	lastID  string
	lastMod string
	lastWhy string
}

func (s *stubEmojiReviewer) Approve(_ context.Context, id, moderatorID string) (*model.EmojiApplication, error) {
	s.lastID, s.lastMod = id, moderatorID
	return s.app, s.err
}

func (s *stubEmojiReviewer) Reject(_ context.Context, id, moderatorID, reason string) (*model.EmojiApplication, error) {
	s.lastID, s.lastMod, s.lastWhy = id, moderatorID, reason
	return s.app, s.err
}

func approvedApp() *model.EmojiApplication {
	emojiID := "e1"
	return &model.EmojiApplication{
		ID: "a1", UserID: "u1", Name: "sushi",
		Status: model.EmojiApplicationApproved, EmojiID: &emojiID,
	}
}

func newEmojiReviewerHandler(t *testing.T, r *stubEmojiReviewer) *apiadmin.Handler {
	t.Helper()
	h := &apiadmin.Handler{}
	h.SetEmojiApplicationReviewer(r)
	return h
}

func TestEmojiApplicationApprove(t *testing.T) {
	rev := &stubEmojiReviewer{app: approvedApp()}
	rec := doPost(newEmojiReviewerHandler(t, rev).EmojiApplicationApprove, `{"applicationId":"a1"}`, adminUser)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "a1", rev.lastID)
	// **押した人を記録する。** 監査で「誰が通したか」を辿れないと、
	// 権限の誤用に気付けない。
	require.Equal(t, "admin1", rev.lastMod)
}

func TestEmojiApplicationRejectPassesReason(t *testing.T) {
	rev := &stubEmojiReviewer{app: &model.EmojiApplication{ID: "a1", Status: model.EmojiApplicationRejected}}
	rec := doPost(newEmojiReviewerHandler(t, rev).EmojiApplicationReject,
		`{"applicationId":"a1","reason":"潰れて読めません"}`, adminUser)

	require.Equal(t, http.StatusOK, rec.Code)
	// 理由は申請者にそのまま届く唯一の文面なので、落とさず渡す。
	require.Equal(t, "潰れて読めません", rev.lastWhy)
}

// **service のエラーを種別ごとに落とす。** すべて 500 にすると、
// モデレーターには「なぜか失敗した」としか見えない。
func TestEmojiApplicationReviewErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"存在しない", emojiapplication.ErrNotFound, http.StatusNotFound, "NO_SUCH_APPLICATION"},
		{"処理済み", emojiapplication.ErrNotPending, http.StatusBadRequest, "ALREADY_PROCESSED"},
		{"同名あり", emojiapplication.ErrDuplicateName, http.StatusBadRequest, "DUPLICATE_NAME"},
		{"未対応の形式", emojiapplication.ErrUnsupportedFileType, http.StatusBadRequest, "UNSUPPORTED_FILE_TYPE"},
		{"画像が消えた", emojiapplication.ErrFileGone, http.StatusBadRequest, "NO_SUCH_FILE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rev := &stubEmojiReviewer{err: tc.err}
			rec := doPost(newEmojiReviewerHandler(t, rev).EmojiApplicationApprove, `{"applicationId":"a1"}`, adminUser)
			require.Equal(t, tc.code, rec.Code)
			require.Contains(t, rec.Body.String(), tc.body)
		})
	}
}

func TestEmojiApplicationApproveRequiresID(t *testing.T) {
	rev := &stubEmojiReviewer{app: approvedApp()}
	rec := doPost(newEmojiReviewerHandler(t, rev).EmojiApplicationApprove, `{}`, adminUser)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, rev.lastID, "id 無しで service が呼ばれた")
}

// **未配線なら 500。** reviewer が nil のまま 200 を返すと、押しても何も
// 起きないのに成功したように見える。
func TestEmojiApplicationApproveUnwired(t *testing.T) {
	rec := doPost((&apiadmin.Handler{}).EmojiApplicationApprove, `{"applicationId":"a1"}`, adminUser)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// 監査に残す情報が出ていること。承認だけでなく却下も残す。
func TestEmojiApplicationResponseShape(t *testing.T) {
	rev := &stubEmojiReviewer{app: approvedApp()}
	rec := doPost(newEmojiReviewerHandler(t, rev).EmojiApplicationApprove, `{"applicationId":"a1"}`, adminUser)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "a1", body["id"])
	require.Equal(t, "approved", body["status"])
	require.Equal(t, "e1", body["emojiId"])
}

// ---- CreateFromApplication (承認時の emoji 生成) ----
//
// **承認が検証を迂回する経路になっていないことを固定する。** admin/emoji/add は
// MIME allowlist / webpublic variant の優先 / 重複チェックを持つ。承認だけが
// それらを漏らすと、承認が「検証を迂回して絵文字を登録する方法」になる。
// レビューで、この関数のカバレッジが 0% で 4 通りの迂回が全部素通りすると
// 実測された。

func ownApplication() *model.EmojiApplication {
	fileID := "f1"
	cat := "たべもの"
	return &model.EmojiApplication{
		ID: "a1", UserID: "u1", Kind: model.EmojiApplicationKindOwn,
		Status: model.EmojiApplicationPending, Name: "sushi",
		Category: &cat, License: "自作", FileID: &fileID,
	}
}

// newEmojiRepoWith returns a mock emoji repository, optionally pre-seeded with
// a conflicting local emoji.
func newEmojiRepoWith(existingName string) *testutil.MockEmojiRepository {
	r := testutil.NewMockEmojiRepository()
	if existingName != "" {
		r.Emojis[existingName+"@"] = &model.Emoji{ID: "e-existing", Name: existingName}
	}
	return r
}

// newDriveRepoWith returns a mock drive repository holding the given file.
func newDriveRepoWith(f *model.DriveFile) *testutil.MockDriveFileRepository {
	r := testutil.NewMockDriveFileRepository()
	if f != nil {
		r.Files[f.ID] = f
	}
	return r
}

// pngFile is a drive file the emoji pipeline accepts, with a webpublic variant
// so the "生の URL を使っていないか" assertion has something to catch.
func pngFile() *model.DriveFile {
	webURL := "https://x/webpublic.webp"
	webType := "image/webp"
	owner := "u1"
	return &model.DriveFile{
		ID: "f1", URL: "https://x/original.png", Type: "image/png", UserID: &owner,
		WebpublicURL: &webURL, WebpublicType: &webType,
	}
}

func newCreatorHandler(t *testing.T, emojis *testutil.MockEmojiRepository, files *testutil.MockDriveFileRepository) *apiadmin.Handler {
	t.Helper()
	gen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	h := apiadmin.NewHandler(nil, nil, nil, nil, gen)
	h.SetEmojiRepo(emojis)
	h.SetDriveFileRepo(files)
	return h
}

// **MIME の allowlist を通ること。** 抜けると、承認が SVG 等を登録する経路になる。
func TestCreateFromApplicationChecksMIME(t *testing.T) {
	emojis := newEmojiRepoWith("")
	files := newDriveRepoWith(&model.DriveFile{
		ID: "f1", URL: "https://x/a.svg", Type: "image/svg+xml",
		UserID: func() *string { u := "u1"; return &u }(),
	})

	_, err := newCreatorHandler(t, emojis, files).CreateFromApplication(context.Background(), ownApplication())
	require.ErrorIs(t, err, emojiapplication.ErrUnsupportedFileType)
	require.Empty(t, emojis.Emojis, "検証を通さずに emoji が作られている")
}

// **承認の直前にも重複を見ること。** 申請の受付時にも見ているが、審査を待つ間に
// 同じ名前が登録されうる。
func TestCreateFromApplicationRechecksDuplicate(t *testing.T) {
	emojis := newEmojiRepoWith("sushi")
	_, err := newCreatorHandler(t, emojis, newDriveRepoWith(pngFile())).
		CreateFromApplication(context.Background(), ownApplication())
	require.ErrorIs(t, err, emojiapplication.ErrDuplicateName)
	require.Len(t, emojis.Emojis, 1, "重複を弾いたのに emoji が増えている")
}

// **webpublic variant を優先すること。** 生の URL / type を使うと、
// admin/emoji/add で登録した絵文字と配信物が食い違う。
func TestCreateFromApplicationPrefersWebpublic(t *testing.T) {
	emojis := newEmojiRepoWith("")
	emojiID, err := newCreatorHandler(t, emojis, newDriveRepoWith(pngFile())).
		CreateFromApplication(context.Background(), ownApplication())
	require.NoError(t, err)
	require.NotEmpty(t, emojiID)
	created := emojis.Emojis["sushi@"]
	require.NotNil(t, created, "emoji が作られていない")

	require.Equal(t, "https://x/original.png", created.OriginalURL)
	require.Equal(t, "https://x/webpublic.webp", created.PublicURL, "webpublic を優先していない")
	require.NotNil(t, created.Type)
	require.Equal(t, "image/webp", *created.Type)

	// 申請の内容がそのまま emoji へ写ること。
	require.Equal(t, "sushi", created.Name)
	require.Equal(t, "自作", *created.License)
	require.Equal(t, "たべもの", *created.Category)
}

// **申請から承認までの間にファイルが消えることがある。** 利用者が drive から
// 消せるので、500 ではなく「もう無い」と伝える。
func TestCreateFromApplicationHandlesMissingFile(t *testing.T) {
	_, err := newCreatorHandler(t, newEmojiRepoWith(""), newDriveRepoWith(nil)).
		CreateFromApplication(context.Background(), ownApplication())
	require.ErrorIs(t, err, emojiapplication.ErrFileGone)
}

func TestCreateFromApplicationRequiresFile(t *testing.T) {
	app := ownApplication()
	app.FileID = nil
	_, err := newCreatorHandler(t, newEmojiRepoWith(""), newDriveRepoWith(pngFile())).
		CreateFromApplication(context.Background(), app)
	require.ErrorIs(t, err, emojiapplication.ErrFileGone)
}

// **承認側でも所有者を見る (レビュー Low 3)。** 申請時の検証が入る前に作られた
// 行が残っている可能性があり、多層にしておけば「申請時の検証を落としたら
// 承認が素通りする」形にもならない。
func TestCreateFromApplicationChecksOwner(t *testing.T) {
	other := "someone-else"
	f := pngFile()
	f.UserID = &other
	emojis := newEmojiRepoWith("")

	_, err := newCreatorHandler(t, emojis, newDriveRepoWith(f)).
		CreateFromApplication(context.Background(), ownApplication())
	require.ErrorIs(t, err, emojiapplication.ErrFileGone)
	require.Empty(t, emojis.Emojis, "他人のファイルで emoji が作られている")
}

// **modlog に `info.emoji` が入ること (レビュー H6 / M2)。**
//
// upstream の `modlog.ModLog.vue` は `log.info.emoji.name` を無条件に読む。
// 欠けると Vue が render 例外を握って Comment ノードを返すので、**その 1 件が
// 監査ログから静かに消える**。コメントだけでは守れないのでテストで固定する。
func TestEmojiApplicationLogsEmojiForModLogUI(t *testing.T) {
	for _, tc := range []struct {
		name   string
		call   func(*apiadmin.Handler) func(echo.Context) error
		app    *model.EmojiApplication
		decide string
	}{
		{"承認", func(h *apiadmin.Handler) func(echo.Context) error { return h.EmojiApplicationApprove },
			approvedApp(), "approved"},
		{"却下", func(h *apiadmin.Handler) func(echo.Context) error { return h.EmojiApplicationReject },
			&model.EmojiApplication{ID: "a1", UserID: "u1", Name: "kusa", Status: model.EmojiApplicationRejected},
			"rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEmojiReviewerHandler(t, &stubEmojiReviewer{app: tc.app})
			logs := attachModLog(t, h)

			rec := doPost(tc.call(h), `{"applicationId":"a1","reason":"x"}`, adminUser)
			require.Equal(t, http.StatusOK, rec.Code)

			// **Log は fire-and-forget (goroutine)。** 同期で読むと空のことがある。
			require.Eventually(t, func() bool { return len(logs.Snapshot()) == 1 },
				time.Second, 5*time.Millisecond, "モデレーションログが残っていない")
			entries := logs.Snapshot()

			// Info は jsonb なので展開して読む。
			var info map[string]any
			require.NoError(t, json.Unmarshal(entries[0].Info, &info))

			emoji, ok := info["emoji"].(map[string]any)
			require.True(t, ok, "info.emoji が無い。管理画面の modlog がこの行を描けず静かに消える")
			require.Equal(t, tc.app.Name, emoji["name"], "info.emoji.name が申請の名前と違う")
			require.Equal(t, tc.decide, info["emojiApplicationDecided"])
			require.Equal(t, "a1", info["emojiApplicationId"])
		})
	}
}

// **負けた承認の emoji を実際に消すこと (レビュー M3)。**
//
// service 側は「負けたら呼ぶ」ことを固定しているが、呼ばれた先が no-op でも
// 気付かない。R5 が防ぎたかった「却下済みなのに絵文字が使える」が復活する。
func TestDeleteCreatedEmoji(t *testing.T) {
	emojis := newEmojiRepoWith("sushi")
	h := newCreatorHandler(t, emojis, newDriveRepoWith(pngFile()))

	require.NoError(t, h.DeleteCreatedEmoji(context.Background(), "e-existing"))
	require.Empty(t, emojis.Emojis, "emoji が消えていない")
}

// 既に無い emoji を指定しても失敗しない (二重に走っても壊れない)。
func TestDeleteCreatedEmojiIgnoresMissing(t *testing.T) {
	h := newCreatorHandler(t, newEmojiRepoWith(""), newDriveRepoWith(pngFile()))
	require.NoError(t, h.DeleteCreatedEmoji(context.Background(), "gone"))
	require.NoError(t, h.DeleteCreatedEmoji(context.Background(), ""))
}

// **重複の確認に失敗したことを隠さない (レビュー Low 1)。**
//
// DB 障害を「重複なし」(false) に潰すと、確認できていないのにモデレーターが
// 承認を押し、DUPLICATE_NAME で落ちる。null で「分からない」を出す。
func TestEmojiApplicationListMarksUnknownConflict(t *testing.T) {
	h := &apiadmin.Handler{}
	h.SetEmojiApplicationRepo(&stubAppsRepo{rows: []model.EmojiApplication{
		{ID: "a1", UserID: "u1", Name: "sushi", Status: model.EmojiApplicationPending},
	}})
	h.SetEmojiRepo(&failingEmojiRepo{})

	rec := doPost(h.EmojiApplicationList, `{"filter":"pending"}`, adminUser)
	require.Equal(t, http.StatusOK, rec.Code)

	var body []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body, 1)
	require.Contains(t, body[0], "nameConflict")
	require.Nil(t, body[0]["nameConflict"], "DB 障害が「重複なし」に潰されている")
}

// **既定の filter は審査待ち (レビュー Low 2)。** all にすると、審査タブを
// 開いたときに処理済みが混ざって審査待ちが埋もれる。
func TestEmojiApplicationListDefaultsToPending(t *testing.T) {
	repo := &stubAppsRepo{}
	h := &apiadmin.Handler{}
	h.SetEmojiApplicationRepo(repo)

	doPost(h.EmojiApplicationList, `{}`, adminUser)
	require.Equal(t, "pending", repo.lastFilter)

	// limit のクランプ。際限なく受けると 1 リクエストで drive を大量に引く。
	doPost(h.EmojiApplicationList, `{"limit":500}`, adminUser)
	require.Equal(t, 30, repo.lastLimit, "limit がクランプされていない")
}

type stubAppsRepo struct {
	rows       []model.EmojiApplication
	lastFilter string
	lastLimit  int
}

func (s *stubAppsRepo) Create(*model.EmojiApplication) error { return nil }
func (s *stubAppsRepo) FindByID(string) (*model.EmojiApplication, error) {
	return nil, gorm.ErrRecordNotFound
}
func (s *stubAppsRepo) List(filter string, limit int, _ string) ([]model.EmojiApplication, error) {
	s.lastFilter, s.lastLimit = filter, limit
	return s.rows, nil
}
func (s *stubAppsRepo) ListByUser(string, int, string) ([]model.EmojiApplication, error) {
	return nil, nil
}
func (s *stubAppsRepo) CountPending() (int64, error)                          { return 0, nil }
func (s *stubAppsRepo) UpdateIfPending(*model.EmojiApplication) (bool, error) { return true, nil }

// failingEmojiRepo fails every lookup so the "確認できなかった" branch runs.
type failingEmojiRepo struct{ testutil.MockEmojiRepository }

func (failingEmojiRepo) FindByNameAndHost(string, *string) (*model.Emoji, error) {
	return nil, gorm.ErrInvalidDB
}
