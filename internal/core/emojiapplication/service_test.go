package emojiapplication

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

type fakeApps struct {
	rows      map[string]*model.EmojiApplication
	createErr error
	updateErr error
	findErr   error
	// loseRace makes the next UpdateIfPending report "someone else wrote first"
	// while FindByID still sees the row as pending. **読んだ後に負ける**状況は
	// これでしか作れない (両方が同じ map を見ているため)。
	loseRace bool
}

func newFakeApps() *fakeApps { return &fakeApps{rows: map[string]*model.EmojiApplication{}} }

func (f *fakeApps) Create(a *model.EmojiApplication) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.rows[a.ID] = a
	return nil
}

func (f *fakeApps) FindByID(id string) (*model.EmojiApplication, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	a, ok := f.rows[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	// **コピーを返す。** 同じポインタを返すと service の書き換えが保存済みの
	// 行にも反映され、条件付き UPDATE の検証が成立しない (実 DB はコピーを返す)。
	cp := *a
	return &cp, nil
}

func (f *fakeApps) List(string, int, string) ([]model.EmojiApplication, error)       { return nil, nil }
func (f *fakeApps) ListByUser(string, int, string) ([]model.EmojiApplication, error) { return nil, nil }
func (f *fakeApps) CountPending() (int64, error)                                     { return 0, nil }
func (f *fakeApps) UpdateIfPending(a *model.EmojiApplication) (bool, error) {
	if f.updateErr != nil {
		return false, f.updateErr
	}
	if f.loseRace {
		f.loseRace = false
		return false, nil
	}
	// 条件付き UPDATE を模す。読んだ後に他で処理されていたら書かない。
	cur, ok := f.rows[a.ID]
	if !ok || cur.Status != model.EmojiApplicationPending {
		return false, nil
	}
	f.rows[a.ID] = a
	return true, nil
}

type fakeEmojis struct {
	existing *model.Emoji
	err      error
	// remote は host 付きの検索に答える (kind = remote の検証用)。
	remote *model.Emoji
	// 期待する引数。空なら検査しない。
	remoteName string
	remoteHost string
}

// **name も host も見る。** 握り潰すと、呼び出し側が引数を入れ替えても
// 気付けない (レビュー M5)。実測でその変異が素通りした。
func (f *fakeEmojis) FindByNameAndHost(name string, host *string) (*model.Emoji, error) {
	if f.err != nil {
		return nil, f.err
	}
	if host != nil {
		if f.remote == nil {
			return nil, gorm.ErrRecordNotFound
		}
		// 引数が入れ替わっていれば期待と合わない。
		if f.remoteName != "" && name != f.remoteName {
			return nil, gorm.ErrRecordNotFound
		}
		if f.remoteHost != "" && *host != f.remoteHost {
			return nil, gorm.ErrRecordNotFound
		}
		return f.remote, nil
	}
	if f.existing == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return f.existing, nil
}

// okFiles returns a drive file owned by u1 so the happy path passes.
type okFiles struct {
	file *model.DriveFile
	err  error
}

func (f *okFiles) FindByID(string) (*model.DriveFile, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.file != nil {
		return f.file, nil
	}
	owner := "u1"
	return &model.DriveFile{ID: "f1", UserID: &owner, Type: "image/png"}, nil
}

type fixedID struct{ n int }

func (f *fixedID) Generate(time.Time) string { f.n++; return "id" + string(rune('0'+f.n)) }

type fakeCreator struct {
	id        string
	err       error
	called    bool
	deleted   []string
	deleteErr error
}

func (c *fakeCreator) CreateFromApplication(context.Context, *model.EmojiApplication) (string, error) {
	c.called = true
	return c.id, c.err
}

func (c *fakeCreator) DeleteCreatedEmoji(_ context.Context, emojiID string) error {
	c.deleted = append(c.deleted, emojiID)
	return c.deleteErr
}

func newService(t *testing.T, apps *fakeApps, emojis *fakeEmojis, creator EmojiCreator) *Service {
	t.Helper()
	return NewService(apps, emojis, &okFiles{}, &fixedID{}, creator, nil)
}

func validInput() CreateInput {
	return CreateInput{UserID: "u1", Name: "sushi", License: "自作", FileID: "f1"}
}

func TestCreateValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CreateInput)
		want   error
	}{
		{"名前が空", func(in *CreateInput) { in.Name = "" }, ErrInvalidName},
		{"名前に記号", func(in *CreateInput) { in.Name = "su-shi" }, ErrInvalidName},
		{"名前に全角", func(in *CreateInput) { in.Name = "すし" }, ErrInvalidName},
		{"ライセンスが空", func(in *CreateInput) { in.License = "  " }, ErrLicenseRequired},
		{"ファイルが無い", func(in *CreateInput) { in.FileID = "" }, ErrFileRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mutate(&in)
			_, err := newService(t, newFakeApps(), &fakeEmojis{}, nil).Create(in)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// **同名が既にあるなら受け付けない。** 受けると、審査で承認を押した瞬間に
// DUPLICATE_NAME で落ちる。押す前に分かるほうがよい。
func TestCreateRejectsExistingName(t *testing.T) {
	_, err := newService(t, newFakeApps(), &fakeEmojis{existing: &model.Emoji{ID: "e1"}}, nil).Create(validInput())
	require.ErrorIs(t, err, ErrDuplicateName)
}

// **DB 障害を「重複なし」に丸めない (#2792)。** 丸めると、確認できていないのに
// 申請を受け、承認時に落ちる。
func TestCreateSurfacesLookupFailure(t *testing.T) {
	boom := errors.New("db down")
	_, err := newService(t, newFakeApps(), &fakeEmojis{err: boom}, nil).Create(validInput())
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, ErrDuplicateName)
}

func TestCreateMapsDuplicatePending(t *testing.T) {
	apps := newFakeApps()
	apps.createErr = repository.ErrEmojiApplicationDuplicatePending
	_, err := newService(t, apps, &fakeEmojis{}, nil).Create(validInput())
	require.ErrorIs(t, err, ErrAlreadyPending)
}

func TestCreateNormalizesAliases(t *testing.T) {
	in := validInput()
	in.Aliases = []string{"おすし", " ", "おすし", "寿司"}
	in.Category = "  たべもの  "
	app, err := newService(t, newFakeApps(), &fakeEmojis{}, nil).Create(in)
	require.NoError(t, err)
	// 空白と重複は落ちる。
	require.Equal(t, []string{"おすし", "寿司"}, []string(app.Aliases))
	require.Equal(t, "たべもの", *app.Category)
}

func TestApproveRegistersThenCloses(t *testing.T) {
	apps := newFakeApps()
	app := &model.EmojiApplication{ID: "a1", UserID: "u1", Status: model.EmojiApplicationPending}
	apps.rows["a1"] = app
	creator := &fakeCreator{id: "e9"}

	out, err := newService(t, apps, &fakeEmojis{}, creator).Approve(context.Background(), "a1", "mod")
	require.NoError(t, err)
	require.True(t, creator.called, "emoji が作られていない")
	require.Equal(t, model.EmojiApplicationApproved, out.Status)
	require.Equal(t, "e9", *out.EmojiID)
	require.Equal(t, "mod", *out.ProcessedByID)
	require.NotNil(t, out.ProcessedAt)
}

// **emoji の作成に失敗したら申請は pending のまま。** 閉じてしまうと
// 「承認済みなのに絵文字が無い」行が残り、押し直す手段も無くなる。
func TestApproveKeepsPendingWhenCreationFails(t *testing.T) {
	apps := newFakeApps()
	apps.rows["a1"] = &model.EmojiApplication{ID: "a1", Status: model.EmojiApplicationPending}
	boom := errors.New("unsupported")
	creator := &fakeCreator{err: boom}

	_, err := newService(t, apps, &fakeEmojis{}, creator).Approve(context.Background(), "a1", "mod")
	require.ErrorIs(t, err, boom)
	require.Equal(t, model.EmojiApplicationPending, apps.rows["a1"].Status)
	require.Nil(t, apps.rows["a1"].ProcessedAt)
}

func TestRejectRecordsReason(t *testing.T) {
	apps := newFakeApps()
	apps.rows["a1"] = &model.EmojiApplication{ID: "a1", Status: model.EmojiApplicationPending}

	out, err := newService(t, apps, &fakeEmojis{}, nil).Reject(context.Background(), "a1", "mod", " 潰れて読めません ")
	require.NoError(t, err)
	require.Equal(t, model.EmojiApplicationRejected, out.Status)
	require.Equal(t, "潰れて読めません", *out.RejectReason)
}

// **処理済みの申請は二度処理できない。** 2 人のモデレーターが同時に押した
// 場合もここに来る。
func TestApproveAndRejectRequirePending(t *testing.T) {
	for _, status := range []string{model.EmojiApplicationApproved, model.EmojiApplicationRejected, model.EmojiApplicationCanceled} {
		apps := newFakeApps()
		apps.rows["a1"] = &model.EmojiApplication{ID: "a1", Status: status}
		svc := newService(t, apps, &fakeEmojis{}, &fakeCreator{id: "e1"})

		_, err := svc.Approve(context.Background(), "a1", "mod")
		require.ErrorIs(t, err, ErrNotPending, status)
		_, err = svc.Reject(context.Background(), "a1", "mod", "x")
		require.ErrorIs(t, err, ErrNotPending, status)
	}
}

// **他人の申請を取り下げさせない。** id は申請者に見えるので、所有者の確認を
// 落とすと誰でも他人の申請を消せる。
func TestCancelChecksOwner(t *testing.T) {
	apps := newFakeApps()
	apps.rows["a1"] = &model.EmojiApplication{ID: "a1", UserID: "owner", Status: model.EmojiApplicationPending}
	svc := newService(t, apps, &fakeEmojis{}, nil)

	require.ErrorIs(t, svc.Cancel("a1", "someone-else"), ErrForbidden)
	require.Equal(t, model.EmojiApplicationPending, apps.rows["a1"].Status, "他人が取り下げられてしまった")

	require.NoError(t, svc.Cancel("a1", "owner"))
	require.Equal(t, model.EmojiApplicationCanceled, apps.rows["a1"].Status)
}

func TestNotFound(t *testing.T) {
	svc := newService(t, newFakeApps(), &fakeEmojis{}, &fakeCreator{})
	_, err := svc.Approve(context.Background(), "missing", "mod")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, svc.Cancel("missing", "u1"), ErrNotFound)
}

// **他人の drive ファイルを申請の素材にさせない (レビュー H2)。**
//
// 許すと (a) 応答に含まれる URL から他人のファイルを読める
// (`/files/:accessKey` は認証なしの GET で URL そのものが capability)、
// (b) 承認すると他人の画像がサーバーの絵文字として登録される。
// aidx は連番を含むので、他人のファイル ID は隣接探索で当たる。
func TestCreateRejectsForeignFile(t *testing.T) {
	other := "someone-else"
	files := &okFiles{file: &model.DriveFile{ID: "f1", UserID: &other, Type: "image/png"}}
	svc := NewService(newFakeApps(), &fakeEmojis{}, files, &fixedID{}, nil, nil)

	_, err := svc.Create(validInput())
	// **「他人のもの」とは答えない。** 区別できると ID の存在確認に使える。
	require.ErrorIs(t, err, ErrFileGone)
}

// 未紐付け (userId が NULL) も拒否する。誰のものとも言えないファイルは
// 申請の素材にしない (applyMediaUpdate と同じ判断)。
func TestCreateRejectsUnownedFile(t *testing.T) {
	files := &okFiles{file: &model.DriveFile{ID: "f1", UserID: nil, Type: "image/png"}}
	svc := NewService(newFakeApps(), &fakeEmojis{}, files, &fixedID{}, nil, nil)
	_, err := svc.Create(validInput())
	require.ErrorIs(t, err, ErrFileGone)
}

// **申請の時点で MIME を見る。** 承認まで通してから落ちると、モデレーターが
// 押した後にエラーになる。
func TestCreateRejectsNonImage(t *testing.T) {
	owner := "u1"
	files := &okFiles{file: &model.DriveFile{ID: "f1", UserID: &owner, Type: "video/mp4"}}
	svc := NewService(newFakeApps(), &fakeEmojis{}, files, &fixedID{}, nil, nil)
	_, err := svc.Create(validInput())
	require.ErrorIs(t, err, ErrUnsupportedFileType)
}

// **未配線なら通さない (fail-closed)。** 検証できないものを通すと、配線を
// 落とした瞬間に穴が開く。
func TestCreateRejectsWhenFilesUnwired(t *testing.T) {
	svc := NewService(newFakeApps(), &fakeEmojis{}, nil, &fixedID{}, nil, nil)
	_, err := svc.Create(validInput())
	require.ErrorIs(t, err, ErrFileGone)
}

// DB 障害を not-found に丸めない (#2792)。
func TestCreateSurfacesFileLookupFailure(t *testing.T) {
	boom := errors.New("db down")
	svc := NewService(newFakeApps(), &fakeEmojis{}, &okFiles{err: boom}, &fixedID{}, nil, nil)
	_, err := svc.Create(validInput())
	require.ErrorIs(t, err, boom)
}

// **申請者は入力ではなく呼び出し元で決まる (レビュー H7)。**
func TestCreateRecordsCallerAsApplicant(t *testing.T) {
	apps := newFakeApps()
	svc := NewService(apps, &fakeEmojis{}, &okFiles{}, &fixedID{}, nil, nil)

	app, err := svc.Create(validInput())
	require.NoError(t, err)
	require.Equal(t, "u1", app.UserID)
	require.Equal(t, "u1", apps.rows[app.ID].UserID)
}

// **2 人が同じ申請を開いていた場合、後から押した側は負ける (レビュー M1)。**
//
// 読んでから書くだけだと last-write-wins になり、承認で emoji を作った直後に
// 却下が被さって「絵文字は存在するのに申請は却下、emojiId も消える」状態になる。
// 窓は「一覧を開いてから押すまで」の全期間なので実際に起きる。
func TestConcurrentReviewLosesGracefully(t *testing.T) {
	apps := newFakeApps()
	apps.rows["a1"] = &model.EmojiApplication{ID: "a1", UserID: "u1", Status: model.EmojiApplicationPending}
	svc := newService(t, apps, &fakeEmojis{}, &fakeCreator{id: "e9"})

	_, err := svc.Approve(context.Background(), "a1", "mod1")
	require.NoError(t, err)

	_, err = svc.Reject(context.Background(), "a1", "mod2", "潰れて読めません")
	require.ErrorIs(t, err, ErrNotPending, "後から押した却下が承認を上書きしている")

	final := apps.rows["a1"]
	require.Equal(t, model.EmojiApplicationApproved, final.Status)
	require.Equal(t, "e9", *final.EmojiID, "emojiId が消えている")
	require.Equal(t, "mod1", *final.ProcessedByID)
	require.Nil(t, final.RejectReason, "却下理由が混ざっている")
}

// 取り下げも同じ。処理済みの申請は取り下げられない。
func TestCancelLosesToReview(t *testing.T) {
	apps := newFakeApps()
	apps.rows["a1"] = &model.EmojiApplication{ID: "a1", UserID: "u1", Status: model.EmojiApplicationPending}
	svc := newService(t, apps, &fakeEmojis{}, &fakeCreator{id: "e1"})

	_, err := svc.Approve(context.Background(), "a1", "mod1")
	require.NoError(t, err)
	require.ErrorIs(t, svc.Cancel("a1", "u1"), ErrNotPending)
	require.Equal(t, model.EmojiApplicationApproved, apps.rows["a1"].Status)
}

// **DB 障害を「他の人が処理済み」に丸めない。** 丸めると、障害中にモデレーターが
// 「誰かが先に処理した」と誤解して一覧を引き直し続けることになる。
func TestReviewSurfacesUpdateFailure(t *testing.T) {
	boom := errors.New("db down")
	for _, tc := range []struct {
		name string
		call func(*Service) error
	}{
		{"承認", func(s *Service) error { _, e := s.Approve(context.Background(), "a1", "mod"); return e }},
		{"却下", func(s *Service) error { _, e := s.Reject(context.Background(), "a1", "mod", "x"); return e }},
		{"取り下げ", func(s *Service) error { return s.Cancel("a1", "u1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apps := newFakeApps()
			apps.rows["a1"] = &model.EmojiApplication{ID: "a1", UserID: "u1", Status: model.EmojiApplicationPending}
			apps.updateErr = boom
			svc := newService(t, apps, &fakeEmojis{}, &fakeCreator{id: "e1"})

			err := tc.call(svc)
			require.ErrorIs(t, err, boom)
			require.NotErrorIs(t, err, ErrNotPending, "DB 障害を「処理済み」に丸めている")
		})
	}
}

// creator が未配線なら承認しない。emoji を作れないのに申請だけ閉じると、
// 「承認済みなのに絵文字が無い」行が残る。
func TestApproveRequiresCreator(t *testing.T) {
	apps := newFakeApps()
	apps.rows["a1"] = &model.EmojiApplication{ID: "a1", Status: model.EmojiApplicationPending}
	_, err := newService(t, apps, &fakeEmojis{}, nil).Approve(context.Background(), "a1", "mod")
	require.Error(t, err)
	require.Equal(t, model.EmojiApplicationPending, apps.rows["a1"].Status)
}

// FindByID の DB 障害を not-found に丸めない (#2792)。
func TestPendingSurfacesLookupFailure(t *testing.T) {
	boom := errors.New("db down")
	apps := newFakeApps()
	apps.findErr = boom
	svc := newService(t, apps, &fakeEmojis{}, &fakeCreator{})

	_, err := svc.Approve(context.Background(), "a1", "mod")
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, ErrNotFound)
}

// nil を渡しても落ちない (未配線の構成)。
func TestNewResultNotifierAcceptsNil(t *testing.T) {
	require.Nil(t, NewResultNotifier(nil))
}

// **他人の申請は、処理済みでも「無い」と同じ応答にする (レビュー M2)。**
//
// pending の判定を所有者確認より先に行うと、他人の処理済み申請に対して
// ErrNotPending が返り、存在しない ID (ErrNotFound) と区別できてしまう。
// ID 列挙のオラクルになる。
func TestCancelDoesNotLeakOthersApplications(t *testing.T) {
	apps := newFakeApps()
	apps.rows["mine"] = &model.EmojiApplication{ID: "mine", UserID: "u1", Status: model.EmojiApplicationApproved}
	apps.rows["theirs-pending"] = &model.EmojiApplication{ID: "theirs-pending", UserID: "other", Status: model.EmojiApplicationPending}
	apps.rows["theirs-done"] = &model.EmojiApplication{ID: "theirs-done", UserID: "other", Status: model.EmojiApplicationApproved}
	svc := newService(t, apps, &fakeEmojis{}, nil)

	// 他人のものは pending でも処理済みでも同じ error になること。
	pendingErr := svc.Cancel("theirs-pending", "u1")
	doneErr := svc.Cancel("theirs-done", "u1")
	require.ErrorIs(t, pendingErr, ErrForbidden)
	require.ErrorIs(t, doneErr, ErrForbidden,
		"他人の処理済み申請が ErrNotPending として区別できている (ID 列挙のオラクル)")

	// 自分のものなら、処理済みだと分かってよい。
	require.ErrorIs(t, svc.Cancel("mine", "u1"), ErrNotPending)
}

// **申請側と承認側で MIME の判定が一致していること (レビュー R3)。**
//
// 申請が prefix 判定だと `image/svg+xml` が通り、承認で必ず落ちる。
// svg は本文中にそのまま埋め込まれるので XSS になり、承認側は allowlist で
// 止めている — 2 層が別のルールを持つと「押した後にエラー」が svg でだけ残る。
func TestCreateUsesTheSameAllowlistAsApproval(t *testing.T) {
	for _, mime := range []string{"image/svg+xml", "image/heic", "image/jxl", "image/anything"} {
		t.Run(mime, func(t *testing.T) {
			owner := "u1"
			files := &okFiles{file: &model.DriveFile{ID: "f1", UserID: &owner, Type: mime}}
			svc := NewService(newFakeApps(), &fakeEmojis{}, files, &fixedID{}, nil, nil)
			_, err := svc.Create(validInput())
			require.ErrorIs(t, err, ErrUnsupportedFileType,
				"%s が申請で通っている (承認で必ず落ちる)", mime)
		})
	}
	// allowlist にあるものは通ること。
	for _, mime := range []string{"image/png", "image/gif", "image/webp"} {
		owner := "u1"
		files := &okFiles{file: &model.DriveFile{ID: "f1", UserID: &owner, Type: mime}}
		svc := NewService(newFakeApps(), &fakeEmojis{}, files, &fixedID{}, nil, nil)
		_, err := svc.Create(validInput())
		require.NoError(t, err, "%s が申請で弾かれている", mime)
	}
}

// **長さは文字数で見る (レビュー R4)。** varchar(N) は文字数なのに len() は
// バイト数なので、バイトで数えると日本語は列の約 1/3 しか使えない。
func TestCreateCountsLengthInRunes(t *testing.T) {
	svc := func(apps *fakeApps) *Service {
		return NewService(apps, &fakeEmojis{}, &okFiles{}, &fixedID{}, nil, nil)
	}

	// 1024 文字 (3072 バイト) は列に入るので通ること。
	in := validInput()
	in.License = strings.Repeat("あ", 1024)
	_, err := svc(newFakeApps()).Create(in)
	require.NoError(t, err, "1024 文字のライセンスが弾かれている (バイトで数えている)")

	// 1025 文字は弾くこと。
	in.License = strings.Repeat("あ", 1025)
	_, err = svc(newFakeApps()).Create(in)
	require.ErrorIs(t, err, ErrTooLong)

	// category も同じ。
	in = validInput()
	in.Category = strings.Repeat("あ", 128)
	_, err = svc(newFakeApps()).Create(in)
	require.NoError(t, err, "128 文字のカテゴリが弾かれている")

	in.Category = strings.Repeat("あ", 129)
	_, err = svc(newFakeApps()).Create(in)
	require.ErrorIs(t, err, ErrTooLong)
}

// **承認が競合に負けたら、作った emoji を片付ける (レビュー R5)。**
//
// 残すと、申請は「却下」で通知も却下なのに絵文字だけ登録済みで使える状態になる。
// M1 前の「絵文字があるのに申請は却下」と症状が同じ。
func TestApproveCleansUpWhenLosingRace(t *testing.T) {
	apps := newFakeApps()
	apps.rows["a1"] = &model.EmojiApplication{ID: "a1", UserID: "u1", Status: model.EmojiApplicationPending}
	creator := &fakeCreator{id: "e-orphan"}
	svc := newService(t, apps, &fakeEmojis{}, creator)

	// **読んだ時点では pending だが、書く直前に他のモデレーターが処理する。**
	apps.loseRace = true

	_, err := svc.Approve(context.Background(), "a1", "mod1")
	require.ErrorIs(t, err, ErrNotPending)
	require.True(t, creator.called, "emoji が作られていない (前提が崩れている)")
	require.Equal(t, []string{"e-orphan"}, creator.deleted,
		"競合に負けた承認の emoji が残っている")

	// 申請は書き換わっていないこと。
	require.Equal(t, model.EmojiApplicationPending, apps.rows["a1"].Status)
}

// 片付けに失敗しても審査の結果は変わらない (通知や状態を壊さない)。
func TestApproveSurvivesCleanupFailure(t *testing.T) {
	apps := newFakeApps()
	apps.rows["a1"] = &model.EmojiApplication{ID: "a1", UserID: "u1", Status: model.EmojiApplicationPending}
	apps.loseRace = true
	creator := &fakeCreator{id: "e1", deleteErr: errors.New("delete failed")}
	svc := newService(t, apps, &fakeEmojis{}, creator)

	_, err := svc.Approve(context.Background(), "a1", "mod1")
	require.ErrorIs(t, err, ErrNotPending, "削除の失敗が審査の結果に混ざっている")
}

// **審査したら必ず通知すること (レビュー Low 3)。** 呼び出しを落としても
// 状態は正しいままなので、テストが無いと「結果が届かない」回帰が緑で通る。
func TestReviewNotifiesApplicant(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Service) error
	}{
		{"承認", func(s *Service) error { _, e := s.Approve(context.Background(), "a1", "mod"); return e }},
		{"却下", func(s *Service) error { _, e := s.Reject(context.Background(), "a1", "mod", "x"); return e }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apps := newFakeApps()
			apps.rows["a1"] = &model.EmojiApplication{ID: "a1", UserID: "u1", Status: model.EmojiApplicationPending}
			notifier := &recordingNotifier{}
			svc := NewService(apps, &fakeEmojis{}, &okFiles{}, &fixedID{}, &fakeCreator{id: "e1"}, notifier)

			require.NoError(t, tc.call(svc))
			require.Len(t, notifier.sent, 1, "申請者に通知していない")
			require.Equal(t, "a1", notifier.sent[0])
		})
	}
}

// 取り下げでは通知しない。自分で押したので届ける意味が無い。
func TestCancelDoesNotNotify(t *testing.T) {
	apps := newFakeApps()
	apps.rows["a1"] = &model.EmojiApplication{ID: "a1", UserID: "u1", Status: model.EmojiApplicationPending}
	notifier := &recordingNotifier{}
	svc := NewService(apps, &fakeEmojis{}, &okFiles{}, &fixedID{}, nil, notifier)

	require.NoError(t, svc.Cancel("a1", "u1"))
	require.Empty(t, notifier.sent)
}

// **取り下げも競合に負けたら 204 を返さない (レビュー Low 4)。**
func TestCancelReportsLostRace(t *testing.T) {
	apps := newFakeApps()
	apps.rows["a1"] = &model.EmojiApplication{ID: "a1", UserID: "u1", Status: model.EmojiApplicationPending}
	apps.loseRace = true
	svc := newService(t, apps, &fakeEmojis{}, nil)

	require.ErrorIs(t, svc.Cancel("a1", "u1"), ErrNotPending,
		"競合に負けたのに成功を返している")
}

type recordingNotifier struct{ sent []string }

func (r *recordingNotifier) NotifyEmojiApplicationProcessed(_ context.Context, app *model.EmojiApplication) error {
	r.sent = append(r.sent, app.ID)
	return nil
}

func remoteInput() CreateInput {
	return CreateInput{
		UserID: "u1", Kind: model.EmojiApplicationKindRemote,
		Name: "sushi", License: "リモートから取り込み",
		RemoteHost: "example.com", RemoteName: "sushi_remote",
	}
}

// **リモート絵文字の申請も同じ共通検証を通ること (#2935)。**
func TestCreateRemote(t *testing.T) {
	apps := newFakeApps()
	emojis := &fakeEmojis{remote: &model.Emoji{ID: "e-remote", Name: "sushi_remote"}}
	svc := NewService(apps, emojis, &okFiles{}, &fixedID{}, nil, nil)

	app, err := svc.Create(remoteInput())
	require.NoError(t, err)
	require.Equal(t, model.EmojiApplicationKindRemote, app.Kind)
	require.Equal(t, "example.com", *app.RemoteHost)
	require.Equal(t, "sushi_remote", *app.RemoteName)
	// **自作用の列は空のまま。** kind を取り違えると承認で別の経路に入る。
	require.Nil(t, app.FileID)
}

// **host / name が要ること。** 無いと承認時に取り直す手がかりが無い。
func TestCreateRemoteRequiresHostAndName(t *testing.T) {
	emojis := &fakeEmojis{remote: &model.Emoji{ID: "e1"}}
	for _, tc := range []struct {
		name   string
		mutate func(*CreateInput)
	}{
		{"host が空", func(in *CreateInput) { in.RemoteHost = "" }},
		{"name が空", func(in *CreateInput) { in.RemoteName = " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := remoteInput()
			tc.mutate(&in)
			_, err := NewService(newFakeApps(), emojis, &okFiles{}, &fixedID{}, nil, nil).Create(in)
			require.ErrorIs(t, err, ErrRemoteRequired)
		})
	}
}

// **知らないリモート絵文字は受け付けない。** 承認時に取り直せないので、
// 申請の時点で引けることを確かめる。
func TestCreateRemoteRejectsUnknownEmoji(t *testing.T) {
	svc := NewService(newFakeApps(), &fakeEmojis{}, &okFiles{}, &fixedID{}, nil, nil)
	_, err := svc.Create(remoteInput())
	require.ErrorIs(t, err, ErrNoSuchRemoteEmoji)
}

// **リモートでも同名のローカル絵文字があれば弾く。** 承認を押してから
// DUPLICATE_NAME で落ちるのを防ぐ (own と同じ理由)。
func TestCreateRemoteRejectsDuplicateLocalName(t *testing.T) {
	emojis := &fakeEmojis{
		existing: &model.Emoji{ID: "e-local"},
		remote:   &model.Emoji{ID: "e-remote"},
	}
	svc := NewService(newFakeApps(), emojis, &okFiles{}, &fixedID{}, nil, nil)
	_, err := svc.Create(remoteInput())
	require.ErrorIs(t, err, ErrDuplicateName)
}

// **未知の kind は弾く。** 既定 (空文字) は own に倒す。
//
// **ライセンスを空にして試す (レビュー Low 4)。** fixture の
// `remoteInput()` はライセンスを埋めているので、そのままだと「kind より先に
// ライセンスを見る」順序の誤りを隠す。リモートのダイアログは license を任意と
// して空で送るので、**実際に通るのは空のほう**。
func TestCreateRejectsUnknownKind(t *testing.T) {
	for _, license := range []string{"リモートから取り込み", ""} {
		in := remoteInput()
		in.Kind = "whatever"
		in.License = license
		_, err := NewService(newFakeApps(), &fakeEmojis{}, &okFiles{}, &fixedID{}, nil, nil).Create(in)
		require.ErrorIsf(t, err, ErrInvalidKind, "license=%q", license)
	}
}

// host / name の長さも列に収める。
func TestCreateRemoteChecksLength(t *testing.T) {
	emojis := &fakeEmojis{remote: &model.Emoji{ID: "e1"}}
	in := remoteInput()
	in.RemoteHost = strings.Repeat("あ", 129)
	_, err := NewService(newFakeApps(), emojis, &okFiles{}, &fixedID{}, nil, nil).Create(in)
	require.ErrorIs(t, err, ErrTooLong)
}

// **DB 障害を「知らない絵文字」に丸めない (#2792)。**
func TestCreateRemoteSurfacesLookupFailure(t *testing.T) {
	boom := errors.New("db down")
	svc := NewService(newFakeApps(), &fakeEmojis{err: boom}, &okFiles{}, &fixedID{}, nil, nil)
	_, err := svc.Create(remoteInput())
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, ErrNoSuchRemoteEmoji)
}

// **name と host を取り違えていないこと (レビュー M5)。**
//
// 入れ替えると本番では全リモート申請が NO_SUCH_EMOJI になるが、偽物が引数を
// 握り潰していると気付けない。非対称な値を与えて突き合わせる。
func TestCreateRemotePassesArgumentsInOrder(t *testing.T) {
	emojis := &fakeEmojis{
		remote:     &model.Emoji{ID: "e-remote"},
		remoteName: "sushi_remote",
		remoteHost: "example.com",
	}
	svc := NewService(newFakeApps(), emojis, &okFiles{}, &fixedID{}, nil, nil)

	_, err := svc.Create(remoteInput())
	require.NoError(t, err, "name と host が入れ替わっている")
}

// リモートではライセンスを必須にしない (レビュー M1)。
func TestCreateRemoteAllowsEmptyLicense(t *testing.T) {
	emojis := &fakeEmojis{remote: &model.Emoji{ID: "e1"}}
	in := remoteInput()
	in.License = ""

	app, err := NewService(newFakeApps(), emojis, &okFiles{}, &fixedID{}, nil, nil).Create(in)
	require.NoError(t, err, "リモートでライセンスが必須になっている")
	require.Equal(t, "", app.License)
}

// 自作では引き続き必須。
func TestCreateOwnStillRequiresLicense(t *testing.T) {
	in := validInput()
	in.License = ""
	_, err := NewService(newFakeApps(), &fakeEmojis{}, &okFiles{}, &fixedID{}, nil, nil).Create(in)
	require.ErrorIs(t, err, ErrLicenseRequired)
}
