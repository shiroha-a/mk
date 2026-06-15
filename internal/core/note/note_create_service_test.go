package note_test

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubError is a sentinel error used in tests.
var stubError = errors.New("stub error")

// failingNoteRepoCreate fails on Create.
type failingNoteRepoCreate struct{ *testutil.MockNoteRepository }

func (f *failingNoteRepoCreate) Create(_ *model.Note) error { return stubError }

// failingNoteRepoUpdate fails on Update only.
type failingNoteRepoUpdate struct{ *testutil.MockNoteRepository }

func (f *failingNoteRepoUpdate) Update(_ *model.Note, _ string, _ any) error {
	return stubError
}

// failingPollRepo fails on Create.
type failingPollRepo struct{}

func (f *failingPollRepo) Create(_ *model.Poll) error                 { return stubError }
func (f *failingPollRepo) FindByNoteID(_ string) (*model.Poll, error) { return nil, stubError }
func (f *failingPollRepo) IncrementVote(_ string, _ int, _ int) error { return nil }
func (f *failingPollRepo) ListExpiredUnnotified(_ time.Time, _ int) ([]*model.Poll, error) {
	return nil, nil
}
func (f *failingPollRepo) MarkNotified(_ string, _ time.Time) error { return nil }
func (f *failingPollRepo) ListRecommendation(_ string, _ []string, _ bool, _, _ int) ([]string, error) {
	return nil, nil
}

// findFailNoteRepo creates successfully but FindByIDWithRelations always
// fails (CreateService uses the full-relations variant for finalNote since
// fanout / hooks need Renote/Reply embed; #425 split)。
type findFailNoteRepo struct{ *testutil.MockNoteRepository }

func (f *findFailNoteRepo) FindByIDWithRelations(_ string) (*model.Note, error) {
	return nil, stubError
}

func newCreateService(t *testing.T) (*note.CreateService, *testutil.MockNoteRepository, *testutil.MockPollRepository) {
	t.Helper()
	noteRepo := testutil.NewMockNoteRepository()
	pollRepo := testutil.NewMockPollRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := note.NewCreateService(noteRepo, pollRepo, idGen, nil)
	return svc, noteRepo, pollRepo
}

// newCreateServiceWithFollowing wires a MockFollowingRepository so tests can
// exercise the followers-visibility main-stream gate (#1472). Returns the
// service plus both repos so tests can plant rows directly.
func newCreateServiceWithFollowing(t *testing.T) (*note.CreateService, *testutil.MockNoteRepository, *testutil.MockFollowingRepository) {
	t.Helper()
	noteRepo := testutil.NewMockNoteRepository()
	pollRepo := testutil.NewMockPollRepository()
	followingRepo := testutil.NewMockFollowingRepository()
	idGen, _ := id.NewGenerator("aidx")
	svc := note.NewCreateService(noteRepo, pollRepo, idGen, followingRepo)
	return svc, noteRepo, followingRepo
}

func TestCreateService_Success(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)

	user := &model.User{ID: "user1", Username: "alice"}
	text := "Hello"
	created, err := svc.Create(note.CreateInput{
		User:       user,
		Text:       &text,
		Visibility: model.NoteVisibilityPublic,
	})

	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "Hello", *created.Text)
	assert.Equal(t, model.NoteVisibilityPublic, created.Visibility)
	assert.Equal(t, user.ID, created.UserID)
	assert.Len(t, noteRepo.Notes, 1)
}

// TestCreateService_NoExtract verifies the noExtract* flags suppress mention /
// hashtag / emoji extraction (#1538). Without them the same text would populate
// Tags / Mentions / Emojis.
func TestCreateService_NoExtract(t *testing.T) {
	svc, _, _ := newCreateService(t)
	user := &model.User{ID: "user1", Username: "alice"}
	text := "hello #tag @bob :smile:"

	withExtract, err := svc.Create(note.CreateInput{User: user, Text: &text, Visibility: model.NoteVisibilityPublic})
	require.NoError(t, err)
	assert.NotEmpty(t, withExtract.Tags, "デフォルトでは hashtag を抽出する")

	created, err := svc.Create(note.CreateInput{
		User:              user,
		Text:              &text,
		Visibility:        model.NoteVisibilityPublic,
		NoExtractMentions: true,
		NoExtractHashtags: true,
		NoExtractEmojis:   true,
	})
	require.NoError(t, err)
	assert.Empty(t, created.Tags, "noExtractHashtags で hashtag 抽出を抑止")
	assert.Empty(t, created.Mentions, "noExtractMentions で mention 抽出を抑止")
	assert.Empty(t, created.Emojis, "noExtractEmojis で emoji 抽出を抑止")
}

func TestCreateService_DefaultVisibility(t *testing.T) {
	svc, _, _ := newCreateService(t)

	user := &model.User{ID: "user1"}
	text := "x"
	created, err := svc.Create(note.CreateInput{User: user, Text: &text})

	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityPublic, created.Visibility)
}

// #1024: canPublicNote=false の user の public note は home に降格する
// (upstream Misskey TS NoteCreateService と同 silencing 挙動)。channel
// 内の note は降格対象外。
type silencingStub struct{ silenced bool }

func (s *silencingStub) IsSilenced(_ string) bool { return s.silenced }

func TestCreateService_SilencedPublicNoteDemotedToHome(t *testing.T) {
	svc, _, _ := newCreateService(t)
	svc.SetSilencingProvider(&silencingStub{silenced: true})

	user := &model.User{ID: "user1"}
	text := "hello"
	created, err := svc.Create(note.CreateInput{
		User: user, Text: &text, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityHome, created.Visibility,
		"silenced user の public note は home に降格")
}

func TestCreateService_NonSilencedPublicNoteUnchanged(t *testing.T) {
	svc, _, _ := newCreateService(t)
	svc.SetSilencingProvider(&silencingStub{silenced: false})

	user := &model.User{ID: "user1"}
	text := "hello"
	created, err := svc.Create(note.CreateInput{
		User: user, Text: &text, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityPublic, created.Visibility,
		"非 silenced user は public のまま")
}

func TestCreateService_SilencedHomeNoteUnchanged(t *testing.T) {
	// 既に home / followers / specified の note は降格対象外 (= base 維持)。
	svc, _, _ := newCreateService(t)
	svc.SetSilencingProvider(&silencingStub{silenced: true})

	user := &model.User{ID: "user1"}
	text := "hello"
	created, err := svc.Create(note.CreateInput{
		User: user, Text: &text, Visibility: model.NoteVisibilityHome,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityHome, created.Visibility)
}

// channel 内の public note は channel 機構が露出範囲を管理するので silencing
// 対象外 (upstream NoteCreateService.ts も同 logic)。channelHook stub で
// channel 存在チェックを通過させて、降格されずに public のまま保存される
// ことを確認する。
type stubChannelHook struct{ exists bool }

func (h *stubChannelHook) EnsureChannelExists(_ string) error {
	if !h.exists {
		return note.ErrChannelNotFound
	}
	return nil
}

func (h *stubChannelHook) OnNotePosted(_, _, _ string) {}

func TestCreateService_SilencedChannelNoteNotDemoted(t *testing.T) {
	svc, _, _ := newCreateService(t)
	svc.SetSilencingProvider(&silencingStub{silenced: true})
	svc.SetChannelHook(&stubChannelHook{exists: true})

	user := &model.User{ID: "user1"}
	text := "hello"
	channelID := "ch1"
	created, err := svc.Create(note.CreateInput{
		User: user, Text: &text, Visibility: model.NoteVisibilityPublic,
		ChannelID: &channelID,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityPublic, created.Visibility,
		"silenced user でも channel post は降格しない (channel 機構が露出管理)")
}

func TestCreateService_SilencingProviderNilSkipped(t *testing.T) {
	// silencingProvider 未配線時は降格 logic が動かない (= 旧挙動互換)。
	svc, _, _ := newCreateService(t)
	user := &model.User{ID: "user1"}
	text := "hi"
	created, err := svc.Create(note.CreateInput{
		User: user, Text: &text, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityPublic, created.Visibility)
}

func TestCreateService_RequiresContent(t *testing.T) {
	svc, _, _ := newCreateService(t)

	_, err := svc.Create(note.CreateInput{User: &model.User{ID: "user1"}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, note.ErrNoteContentRequired))
}

func TestCreateService_NilUser(t *testing.T) {
	svc, _, _ := newCreateService(t)

	text := "x"
	_, err := svc.Create(note.CreateInput{Text: &text})
	require.Error(t, err)
}

func TestCreateService_FileIDsOnly(t *testing.T) {
	svc, _, _ := newCreateService(t)

	user := &model.User{ID: "user1"}
	created, err := svc.Create(note.CreateInput{
		User:    user,
		FileIDs: []string{"file1"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"file1"}, []string(created.FileIDs))
}

func TestCreateService_RenoteOnly(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)

	// renote先のノートを事前に作成しておく
	noteRepo.Notes["note1"] = &model.Note{
		ID:         "note1",
		UserID:     "other",
		Visibility: model.NoteVisibilityPublic,
	}

	user := &model.User{ID: "user1"}
	renoteID := "note1"
	created, err := svc.Create(note.CreateInput{
		User:     user,
		RenoteID: &renoteID,
	})
	require.NoError(t, err)
	assert.Equal(t, &renoteID, created.RenoteID)
	// pure renoteなのでrenoteCountが+1される
	assert.Equal(t, int16(1), noteRepo.Notes["note1"].RenoteCount)
}

func TestCreateService_VisibleUserIDs(t *testing.T) {
	svc, _, _ := newCreateService(t)

	user := &model.User{ID: "user1"}
	text := "secret"
	created, err := svc.Create(note.CreateInput{
		User:           user,
		Text:           &text,
		Visibility:     model.NoteVisibilitySpecified,
		VisibleUserIDs: []string{"u2", "u3"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"u2", "u3"}, []string(created.VisibleUserIDs))
}

func TestCreateService_WithPoll(t *testing.T) {
	svc, _, pollRepo := newCreateService(t)

	user := &model.User{ID: "user1"}
	text := "vote!"
	expires := time.Now().Add(24 * time.Hour)
	created, err := svc.Create(note.CreateInput{
		User: user,
		Text: &text,
		Poll: &note.PollInput{
			Choices:   []string{"A", "B"},
			Multiple:  true,
			ExpiresAt: &expires,
		},
	})
	require.NoError(t, err)
	assert.True(t, created.HasPoll)
	assert.Len(t, pollRepo.Polls, 1)
	assert.True(t, pollRepo.Polls[created.ID].Multiple)
	assert.NotNil(t, pollRepo.Polls[created.ID].ExpiresAt)
}

func TestCreateService_MentionExtraction(t *testing.T) {
	svc, _, _ := newCreateService(t)

	user := &model.User{ID: "user1"}
	text := "hi @alice and @bob"
	created, err := svc.Create(note.CreateInput{User: user, Text: &text})
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "bob"}, []string(created.Mentions))
}

// 連合配信時に renderer.addEmojiTags が note.Emojis を walk するので、
// note 作成時点で text + cw 中の :code: トークンを抽出して埋めておく必要が
// ある (#629)。空の場合は note.Emojis は空配列のまま。
func TestCreateService_EmojiExtraction(t *testing.T) {
	svc, _, _ := newCreateService(t)
	user := &model.User{ID: "user1"}

	t.Run("text only", func(t *testing.T) {
		text := "hello :foo: and :bar: with :foo: again"
		created, err := svc.Create(note.CreateInput{User: user, Text: &text})
		require.NoError(t, err)
		assert.Equal(t, []string{"foo", "bar"}, []string(created.Emojis))
	})

	t.Run("text + cw merged and dedup'd", func(t *testing.T) {
		text := "body :foo: :baz:"
		cw := "warning :bar: :foo:"
		created, err := svc.Create(note.CreateInput{User: user, Text: &text, CW: &cw})
		require.NoError(t, err)
		assert.Equal(t, []string{"foo", "baz", "bar"}, []string(created.Emojis))
	})

	t.Run("plain text yields empty Emojis", func(t *testing.T) {
		text := "no custom emoji here, only 😀"
		created, err := svc.Create(note.CreateInput{User: user, Text: &text})
		require.NoError(t, err)
		assert.Empty(t, []string(created.Emojis))
	})
}

func TestCreateService_MentionResolution_WithUserRepo(t *testing.T) {
	svc, _, _ := newCreateService(t)
	userRepo := testutil.NewMockUserRepository()
	remoteHost := "remote.example"
	userRepo.Users["uA"] = &model.User{
		ID:            "uA",
		Username:      "alice",
		UsernameLower: "alice",
	}
	userRepo.Users["uB"] = &model.User{
		ID:            "uB",
		Username:      "bob",
		UsernameLower: "bob",
		Host:          &remoteHost,
	}
	svc.SetUserRepo(userRepo)

	author := &model.User{ID: "author1"}
	text := "hi @alice and @bob@remote.example and @ghost"
	created, err := svc.Create(note.CreateInput{User: author, Text: &text})
	require.NoError(t, err)
	// Resolved IDs only; unknown @ghost is skipped.
	assert.ElementsMatch(t, []string{"uA", "uB"}, []string(created.Mentions))
}

// countingUserRepo wraps MockUserRepository to assert that mention resolution
// issues at most one repo round-trip per host (#300 1-5).
type countingUserRepo struct {
	*testutil.MockUserRepository
	calls atomic.Int64
}

func (c *countingUserRepo) FindManyByUsernamesAndHost(usernames []string, host *string) ([]*model.User, error) {
	c.calls.Add(1)
	return c.MockUserRepository.FindManyByUsernamesAndHost(usernames, host)
}

// 全 mention が local 単一 host のとき、host 種類 = 1 なので
// FindManyByUsernamesAndHost は 1 回しか呼ばれない (#300 1-5)。
func TestCreateService_MentionResolution_SingleQueryForLocalOnly(t *testing.T) {
	svc, _, _ := newCreateService(t)
	mock := testutil.NewMockUserRepository()
	mock.Users["uA"] = &model.User{ID: "uA", UsernameLower: "alice"}
	mock.Users["uB"] = &model.User{ID: "uB", UsernameLower: "bob"}
	mock.Users["uC"] = &model.User{ID: "uC", UsernameLower: "carol"}
	repo := &countingUserRepo{MockUserRepository: mock}
	svc.SetUserRepo(repo)

	author := &model.User{ID: "author1"}
	text := "hi @alice and @bob and @carol"
	created, err := svc.Create(note.CreateInput{User: author, Text: &text})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"uA", "uB", "uC"}, []string(created.Mentions))
	assert.Equal(t, int64(1), repo.calls.Load(),
		"3 local mentions must be resolved with a single batch query")
}

// host 種類数だけ batch query が走る。host 内の mention 数には依存しない。
func TestCreateService_MentionResolution_OneQueryPerHost(t *testing.T) {
	svc, _, _ := newCreateService(t)
	mock := testutil.NewMockUserRepository()
	hostA := "a.example"
	hostB := "b.example"
	mock.Users["uLocal"] = &model.User{ID: "uLocal", UsernameLower: "alice"}
	mock.Users["uA1"] = &model.User{ID: "uA1", UsernameLower: "bob", Host: &hostA}
	mock.Users["uA2"] = &model.User{ID: "uA2", UsernameLower: "carol", Host: &hostA}
	mock.Users["uB1"] = &model.User{ID: "uB1", UsernameLower: "dave", Host: &hostB}
	repo := &countingUserRepo{MockUserRepository: mock}
	svc.SetUserRepo(repo)

	author := &model.User{ID: "author1"}
	text := "@alice @bob@a.example @carol@a.example @dave@b.example"
	created, err := svc.Create(note.CreateInput{User: author, Text: &text})
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{"uLocal", "uA1", "uA2", "uB1"},
		[]string(created.Mentions))
	// 3 distinct hosts: local + a.example + b.example
	assert.Equal(t, int64(3), repo.calls.Load(),
		"4 mentions across 3 hosts must batch into 3 queries (one per host)")
}

// mention の出現順は note.Mentions の順序にそのまま反映される。
func TestCreateService_MentionResolution_PreservesOrder(t *testing.T) {
	svc, _, _ := newCreateService(t)
	userRepo := testutil.NewMockUserRepository()
	hostA := "a.example"
	userRepo.Users["uA"] = &model.User{ID: "uA", UsernameLower: "alice"}
	userRepo.Users["uB"] = &model.User{ID: "uB", UsernameLower: "bob", Host: &hostA}
	userRepo.Users["uC"] = &model.User{ID: "uC", UsernameLower: "carol"}
	svc.SetUserRepo(userRepo)

	author := &model.User{ID: "author1"}
	// alice → bob@remote → carol を期待。
	text := "@alice then @bob@a.example then @carol"
	created, err := svc.Create(note.CreateInput{User: author, Text: &text})
	require.NoError(t, err)
	assert.Equal(t, []string{"uA", "uB", "uC"}, []string(created.Mentions))
}

// FindManyByUsernamesAndHost が err を返した host は当該 mention だけ
// drop される (元実装と同等)。他 host の解決は影響を受けない。
func TestCreateService_MentionResolution_FailedHostIsSkipped(t *testing.T) {
	svc, _, _ := newCreateService(t)
	mock := testutil.NewMockUserRepository()
	hostA := "a.example"
	mock.Users["uLocal"] = &model.User{ID: "uLocal", UsernameLower: "alice"}
	mock.Users["uA1"] = &model.User{ID: "uA1", UsernameLower: "bob", Host: &hostA}
	repo := &flakyUserRepo{
		MockUserRepository: mock,
		failHost:           hostA,
	}
	svc.SetUserRepo(repo)

	author := &model.User{ID: "author1"}
	text := "@alice and @bob@a.example"
	created, err := svc.Create(note.CreateInput{User: author, Text: &text})
	require.NoError(t, err)
	// hostA が err なので uA1 は drop、ローカルの uLocal だけ残る。
	assert.Equal(t, []string{"uLocal"}, []string(created.Mentions))
}

// flakyUserRepo は指定 host の FindManyByUsernamesAndHost で err を返す。
type flakyUserRepo struct {
	*testutil.MockUserRepository
	failHost string
}

func (f *flakyUserRepo) FindManyByUsernamesAndHost(usernames []string, host *string) ([]*model.User, error) {
	if host != nil && *host == f.failHost {
		return nil, stubError
	}
	return f.MockUserRepository.FindManyByUsernamesAndHost(usernames, host)
}

func TestCreateService_NoteRepoCreateError(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	noteRepo := &failingNoteRepoCreate{MockNoteRepository: testutil.NewMockNoteRepository()}
	pollRepo := testutil.NewMockPollRepository()
	svc := note.NewCreateService(noteRepo, pollRepo, idGen, nil)

	user := &model.User{ID: "u1"}
	text := "x"
	_, err := svc.Create(note.CreateInput{User: user, Text: &text})
	require.Error(t, err)
}

func TestCreateService_PollRepoCreateError(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	noteRepo := testutil.NewMockNoteRepository()
	pollRepo := &failingPollRepo{}
	svc := note.NewCreateService(noteRepo, pollRepo, idGen, nil)

	user := &model.User{ID: "u1"}
	text := "x"
	_, err := svc.Create(note.CreateInput{
		User: user,
		Text: &text,
		Poll: &note.PollInput{Choices: []string{"A", "B"}},
	})
	require.Error(t, err)
}

func TestCreateService_NoteRepoUpdateError(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	noteRepo := &failingNoteRepoUpdate{MockNoteRepository: testutil.NewMockNoteRepository()}
	pollRepo := testutil.NewMockPollRepository()
	svc := note.NewCreateService(noteRepo, pollRepo, idGen, nil)

	user := &model.User{ID: "u1"}
	text := "x"
	_, err := svc.Create(note.CreateInput{
		User: user,
		Text: &text,
		Poll: &note.PollInput{Choices: []string{"A", "B"}},
	})
	require.Error(t, err)
}

func TestCreateService_FindByIDWithRelationsFails(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	noteRepo := &findFailNoteRepo{MockNoteRepository: testutil.NewMockNoteRepository()}
	pollRepo := testutil.NewMockPollRepository()
	svc := note.NewCreateService(noteRepo, pollRepo, idGen, nil)

	user := &model.User{ID: "u1"}
	text := "x"
	created, err := svc.Create(note.CreateInput{User: user, Text: &text})
	require.NoError(t, err)
	// FindByIDWithRelations が失敗しても in.User が埋められて返る (#425)。
	assert.Equal(t, user, created.User)
}

func TestExtractMentions(t *testing.T) {
	tests := []struct {
		text     string
		expected []string
	}{
		{"Hello @alice @bob", []string{"alice", "bob"}},
		{"No mentions", nil},
		{"@solo", []string{"solo"}},
		{"@", nil},
		{"", nil},
		// 重複は除去される
		{"@alice and @alice again", []string{"alice"}},
		// リモートメンション
		{"hi @alice@example.com", []string{"alice@example.com"}},
		// テキスト内の埋め込み
		{"foo@bar baz@qux", []string{"bar", "qux"}},
	}
	for _, tc := range tests {
		t.Run(tc.text, func(t *testing.T) {
			assert.Equal(t, tc.expected, note.ExtractMentions(tc.text))
		})
	}
}

func TestExtractMentionStructs(t *testing.T) {
	out := note.ExtractMentionStructs("hi @alice and @bob@example.com and @alice")
	require.Len(t, out, 2)
	assert.Equal(t, "alice", out[0].Username)
	assert.Equal(t, "", out[0].Host)
	assert.Equal(t, "bob", out[1].Username)
	assert.Equal(t, "example.com", out[1].Host)

	assert.Empty(t, note.ExtractMentionStructs("nothing here"))
}

func TestCreateService_ReplyTargetNotFound(t *testing.T) {
	svc, _, _ := newCreateService(t)

	user := &model.User{ID: "u1"}
	text := "x"
	replyID := "ghost"
	_, err := svc.Create(note.CreateInput{User: user, Text: &text, ReplyID: &replyID})
	require.ErrorIs(t, err, note.ErrReplyTargetNotFound)
}

func TestCreateService_RenoteTargetNotFound(t *testing.T) {
	svc, _, _ := newCreateService(t)

	user := &model.User{ID: "u1"}
	renoteID := "ghost"
	_, err := svc.Create(note.CreateInput{User: user, RenoteID: &renoteID})
	require.ErrorIs(t, err, note.ErrRenoteTargetNotFound)
}

func TestCreateService_CannotReplyToInvisibleNote(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteRepo.Notes["secret"] = &model.Note{
		ID: "secret", UserID: "author", Visibility: model.NoteVisibilityFollowers,
	}

	user := &model.User{ID: "viewer"}
	text := "x"
	replyID := "secret"
	_, err := svc.Create(note.CreateInput{User: user, Text: &text, ReplyID: &replyID})
	require.ErrorIs(t, err, note.ErrCannotReplyToInvisibleNote)
}

func TestCreateService_CannotRenoteInvisibleNote(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteRepo.Notes["secret"] = &model.Note{
		ID: "secret", UserID: "author", Visibility: model.NoteVisibilityFollowers,
	}

	user := &model.User{ID: "viewer"}
	renoteID := "secret"
	_, err := svc.Create(note.CreateInput{User: user, RenoteID: &renoteID})
	require.ErrorIs(t, err, note.ErrCannotRenoteInvisibleNote)
}

func TestCreateService_ReplyHappyPath(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteRepo.Notes["target"] = &model.Note{
		ID: "target", UserID: "author", Visibility: model.NoteVisibilityPublic,
	}

	user := &model.User{ID: "u1"}
	text := "ack"
	replyID := "target"
	created, err := svc.Create(note.CreateInput{User: user, Text: &text, ReplyID: &replyID})
	require.NoError(t, err)
	assert.Equal(t, &replyID, created.ReplyID)
	// repliesCountが更新される
	assert.Equal(t, int16(1), noteRepo.Notes["target"].RepliesCount)
}

func TestCreateService_QuoteRenoteDoesNotIncrementRenoteCount(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteRepo.Notes["target"] = &model.Note{
		ID: "target", UserID: "author", Visibility: model.NoteVisibilityPublic,
	}

	user := &model.User{ID: "u1"}
	text := "quoted!"
	renoteID := "target"
	_, err := svc.Create(note.CreateInput{User: user, Text: &text, RenoteID: &renoteID})
	require.NoError(t, err)
	// quote renoteなのでrenoteCountは増えない
	assert.Equal(t, int16(0), noteRepo.Notes["target"].RenoteCount)
}

func TestIsPureRenote(t *testing.T) {
	target := "x"
	choices := []string{"a", "b"}

	cases := []struct {
		name string
		in   note.CreateInput
		want bool
	}{
		{"no renote", note.CreateInput{}, false},
		{"pure", note.CreateInput{RenoteID: &target}, true},
		{"with text", note.CreateInput{RenoteID: &target, Text: ptrString("hi")}, false},
		{"with cw", note.CreateInput{RenoteID: &target, CW: ptrString("warn")}, false},
		{"with file", note.CreateInput{RenoteID: &target, FileIDs: []string{"f1"}}, false},
		{"with poll", note.CreateInput{RenoteID: &target, Poll: &note.PollInput{Choices: choices}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, note.IsPureRenoteForTest(tc.in))
		})
	}
}

func ptrString(s string) *string { return &s }

// waitHook blocks until done receives a signal or 5s timeout.
func waitHook(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hook did not fire within timeout")
	}
}

// recordingHook captures fanout invocations for tests.
type recordingHook struct {
	called atomic.Bool
	note   *model.Note
	user   *model.User
	done   chan struct{}
}

func newRecordingHook() *recordingHook {
	return &recordingHook{done: make(chan struct{}, 1)}
}

func (h *recordingHook) OnNoteCreated(n *model.Note, u *model.User) {
	h.called.Store(true)
	h.note = n
	h.user = u
	select {
	case h.done <- struct{}{}:
	default:
	}
}

func TestCreateService_FanoutHookInvoked(t *testing.T) {
	svc, _, _ := newCreateService(t)
	hook := newRecordingHook()
	svc.SetFanoutHook(hook)

	user := &model.User{ID: "u1"}
	text := "hello"
	created, err := svc.Create(note.CreateInput{User: user, Text: &text})
	require.NoError(t, err)
	waitHook(t, hook.done)
	assert.True(t, hook.called.Load())
	assert.Equal(t, created.ID, hook.note.ID)
	assert.Equal(t, user, hook.user)
}

// recordingNotificationHook captures notification calls for tests.
type recordingNotificationHook struct {
	called       atomic.Bool
	gotNote      *model.Note
	gotAuthor    *model.User
	gotReplyTgt  *model.Note
	gotRenoteTgt *model.Note
	done         chan struct{}
}

func newRecordingNotificationHook() *recordingNotificationHook {
	return &recordingNotificationHook{done: make(chan struct{}, 1)}
}

func (h *recordingNotificationHook) OnNoteCreated(n *model.Note, u *model.User, reply, renote *model.Note) {
	h.called.Store(true)
	h.gotNote = n
	h.gotAuthor = u
	h.gotReplyTgt = reply
	h.gotRenoteTgt = renote
	select {
	case h.done <- struct{}{}:
	default:
	}
}

func TestCreateService_NotificationHookInvoked(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteRepo.Notes["target"] = &model.Note{
		ID: "target", UserID: "author", Visibility: model.NoteVisibilityPublic,
	}
	hook := newRecordingNotificationHook()
	svc.SetNotificationHook(hook)

	user := &model.User{ID: "u1"}
	text := "hi"
	replyID := "target"
	_, err := svc.Create(note.CreateInput{User: user, Text: &text, ReplyID: &replyID})
	require.NoError(t, err)
	waitHook(t, hook.done)
	assert.True(t, hook.called.Load())
	assert.Equal(t, "target", hook.gotReplyTgt.ID)
}

func TestCreateService_FanoutHookCalledOnFallbackPath(t *testing.T) {
	// FindByIDWithUser失敗時もfanoutは発火する
	idGen, _ := id.NewGenerator("aidx")
	noteRepo := &findFailNoteRepo{MockNoteRepository: testutil.NewMockNoteRepository()}
	pollRepo := testutil.NewMockPollRepository()
	svc := note.NewCreateService(noteRepo, pollRepo, idGen, nil)
	hook := newRecordingHook()
	svc.SetFanoutHook(hook)

	user := &model.User{ID: "u1"}
	text := "hi"
	_, err := svc.Create(note.CreateInput{User: user, Text: &text})
	require.NoError(t, err)
	waitHook(t, hook.done)
	assert.True(t, hook.called.Load())
	// fallbackパスではin.Userが埋め込まれる
	assert.Equal(t, user, hook.note.User)
}

// recordingFederationHook captures federation hook calls for tests.
type recordingFederationHook struct {
	called atomic.Bool
	note   *model.Note
	user   *model.User
	done   chan struct{}
}

func newRecordingFederationHook() *recordingFederationHook {
	return &recordingFederationHook{done: make(chan struct{}, 1)}
}

func (h *recordingFederationHook) OnNoteCreated(n *model.Note, u *model.User) {
	h.called.Store(true)
	h.note = n
	h.user = u
	select {
	case h.done <- struct{}{}:
	default:
	}
}

func TestCreateService_FederationHookInvoked(t *testing.T) {
	svc, _, _ := newCreateService(t)
	hook := newRecordingFederationHook()
	svc.SetFederationHook(hook)

	user := &model.User{ID: "u1"}
	text := "hello"
	created, err := svc.Create(note.CreateInput{User: user, Text: &text})
	require.NoError(t, err)
	waitHook(t, hook.done)
	assert.True(t, hook.called.Load())
	assert.Equal(t, created.ID, hook.note.ID)
}

// recordingChannelHook captures channel hook calls for tests.
type recordingChannelHook struct {
	ensureCalled bool
	ensureID     string
	ensureErr    error
	postedCalled atomic.Bool
	postedID     string
	postedDone   chan struct{}
}

func newRecordingChannelHook() *recordingChannelHook {
	return &recordingChannelHook{postedDone: make(chan struct{}, 1)}
}

func (h *recordingChannelHook) EnsureChannelExists(channelID string) error {
	h.ensureCalled = true
	h.ensureID = channelID
	return h.ensureErr
}

func (h *recordingChannelHook) OnNotePosted(channelID, _, _ string) {
	h.postedCalled.Store(true)
	h.postedID = channelID
	select {
	case h.postedDone <- struct{}{}:
	default:
	}
}

func TestCreateService_ChannelHookEnsureAndOnPosted(t *testing.T) {
	svc, _, _ := newCreateService(t)
	hook := newRecordingChannelHook()
	svc.SetChannelHook(hook)

	user := &model.User{ID: "u1"}
	text := "hello"
	channelID := "ch1"
	_, err := svc.Create(note.CreateInput{User: user, Text: &text, ChannelID: &channelID})
	require.NoError(t, err)
	// EnsureChannelExistsはCreate内で同期的に呼ばれる
	assert.True(t, hook.ensureCalled)
	assert.Equal(t, "ch1", hook.ensureID)
	// OnNotePostedは非同期
	waitHook(t, hook.postedDone)
	assert.True(t, hook.postedCalled.Load())
	assert.Equal(t, "ch1", hook.postedID)
}

func TestCreateService_ChannelNotFound(t *testing.T) {
	svc, _, _ := newCreateService(t)
	hook := newRecordingChannelHook()
	hook.ensureErr = errors.New("missing")
	svc.SetChannelHook(hook)

	user := &model.User{ID: "u1"}
	text := "hello"
	channelID := "ch1"
	_, err := svc.Create(note.CreateInput{User: user, Text: &text, ChannelID: &channelID})
	assert.ErrorIs(t, err, note.ErrChannelNotFound)
}

func TestCreateService_NoChannelHookSkipsCheck(t *testing.T) {
	svc, _, _ := newCreateService(t)
	user := &model.User{ID: "u1"}
	text := "hello"
	channelID := "ch1"
	_, err := svc.Create(note.CreateInput{User: user, Text: &text, ChannelID: &channelID})
	require.NoError(t, err)
}

// recordingAntennaHook captures antenna hook calls for tests.
type recordingAntennaHook struct {
	called atomic.Bool
	note   *model.Note
	done   chan struct{}
}

func newRecordingAntennaHook() *recordingAntennaHook {
	return &recordingAntennaHook{done: make(chan struct{}, 1)}
}

func (h *recordingAntennaHook) OnNoteCreated(n *model.Note, _ *model.User) {
	h.called.Store(true)
	h.note = n
	select {
	case h.done <- struct{}{}:
	default:
	}
}

func TestCreateService_AntennaHookInvoked(t *testing.T) {
	svc, _, _ := newCreateService(t)
	hook := newRecordingAntennaHook()
	svc.SetAntennaHook(hook)

	user := &model.User{ID: "u1"}
	text := "hello"
	created, err := svc.Create(note.CreateInput{User: user, Text: &text})
	require.NoError(t, err)
	waitHook(t, hook.done)
	assert.True(t, hook.called.Load())
	assert.Equal(t, created.ID, hook.note.ID)
}

// recordingIndexHook records calls into note.IndexHook so the create / delete
// services の連携経路を検証できる。
type recordingIndexHook struct {
	created     *model.Note
	deleted     *model.Note
	createdDone chan struct{}
}

func newRecordingIndexHook() *recordingIndexHook {
	return &recordingIndexHook{createdDone: make(chan struct{}, 1)}
}

func (h *recordingIndexHook) OnNoteCreated(n *model.Note) {
	h.created = n
	select {
	case h.createdDone <- struct{}{}:
	default:
	}
}
func (h *recordingIndexHook) OnNoteDeleted(n *model.Note) { h.deleted = n }

func TestCreateService_IndexHookInvoked(t *testing.T) {
	svc, _, _ := newCreateService(t)
	hook := newRecordingIndexHook()
	svc.SetIndexHook(hook)

	user := &model.User{ID: "u1"}
	text := "hello"
	created, err := svc.Create(note.CreateInput{User: user, Text: &text})
	require.NoError(t, err)
	waitHook(t, hook.createdDone)
	require.NotNil(t, hook.created)
	assert.Equal(t, created.ID, hook.created.ID)
}

// recordingChartHook captures the chart hook firing path so we can
// verify NoteCreateService.Create / NoteDeleteService.Delete fan it
// out alongside the other registered hooks.
type recordingChartHook struct {
	created *model.Note
	done    chan struct{}
}

func newRecordingChartHook() *recordingChartHook {
	return &recordingChartHook{done: make(chan struct{}, 1)}
}

func (h *recordingChartHook) OnNoteCreated(n *model.Note) {
	h.created = n
	select {
	case h.done <- struct{}{}:
	default:
	}
}

func TestCreateService_ChartHookInvoked(t *testing.T) {
	svc, _, _ := newCreateService(t)
	hook := newRecordingChartHook()
	svc.SetChartHook(hook)

	user := &model.User{ID: "u1"}
	text := "hello"
	created, err := svc.Create(note.CreateInput{User: user, Text: &text})
	require.NoError(t, err)
	waitHook(t, hook.done)
	require.NotNil(t, hook.created)
	assert.Equal(t, created.ID, hook.created.ID)
}

// recordingHashtagHook は HashtagHook の発火を観測するための test double。
type recordingHashtagHook struct {
	note   *model.Note
	author *model.User
	done   chan struct{}
}

func newRecordingHashtagHook() *recordingHashtagHook {
	return &recordingHashtagHook{done: make(chan struct{}, 1)}
}

func (h *recordingHashtagHook) OnNoteCreated(n *model.Note, a *model.User) {
	h.note = n
	h.author = a
	select {
	case h.done <- struct{}{}:
	default:
	}
}

func TestCreateService_HashtagHookInvoked(t *testing.T) {
	svc, _, _ := newCreateService(t)
	hook := newRecordingHashtagHook()
	svc.SetHashtagHook(hook)

	user := &model.User{ID: "u1"}
	text := "hello #foo"
	created, err := svc.Create(note.CreateInput{User: user, Text: &text})
	require.NoError(t, err)
	waitHook(t, hook.done)
	require.NotNil(t, hook.note)
	assert.Equal(t, created.ID, hook.note.ID)
	require.NotNil(t, hook.author)
	assert.Equal(t, user.ID, hook.author.ID)
}

func TestIsPureRenote_ModelNote(t *testing.T) {
	renoteID := "r1"
	text := "x"
	cw := "x"
	cases := []struct {
		name string
		n    *model.Note
		want bool
	}{
		{"nil", nil, false},
		{"no renote", &model.Note{}, false},
		{"pure", &model.Note{RenoteID: &renoteID}, true},
		{"with text", &model.Note{RenoteID: &renoteID, Text: &text}, false},
		{"with cw", &model.Note{RenoteID: &renoteID, CW: &cw}, false},
		{"with files", &model.Note{RenoteID: &renoteID, FileIDs: []string{"f"}}, false},
		{"with poll", &model.Note{RenoteID: &renoteID, HasPoll: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, note.IsPureRenote(tc.n))
		})
	}
}

// --- Phase 7-1 follow-up (#254): 追加 sentinel error 検出テスト ---

func strPtr254(s string) *string { return &s }

func TestCreateService_ExpiredPoll(t *testing.T) {
	svc, _, _ := newCreateService(t)
	past := time.Now().Add(-1 * time.Hour)
	text := "vote!"
	_, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"},
		Text: &text,
		Poll: &note.PollInput{Choices: []string{"a", "b"}, ExpiresAt: &past},
	})
	assert.ErrorIs(t, err, note.ErrCannotCreateAlreadyExpiredPoll)
}

func TestCreateService_CannotRenoteToAPureRenote(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteID := "pure_renote_target"
	renoteTargetInner := "other_note"
	noteRepo.Notes[noteID] = &model.Note{
		ID: noteID, UserID: "other", Visibility: model.NoteVisibilityPublic,
		RenoteID: &renoteTargetInner, // pure renote: text/cw/files/poll なし
	}
	_, err := svc.Create(note.CreateInput{
		User:     &model.User{ID: "u1"},
		RenoteID: strPtr254(noteID),
	})
	assert.ErrorIs(t, err, note.ErrCannotRenoteToAPureRenote)
}

func TestCreateService_CannotReplyToAPureRenote(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteID := "pure_renote_target_2"
	inner := "other_note"
	noteRepo.Notes[noteID] = &model.Note{
		ID: noteID, UserID: "other", Visibility: model.NoteVisibilityPublic,
		RenoteID: &inner,
	}
	text := "hi"
	_, err := svc.Create(note.CreateInput{
		User:    &model.User{ID: "u1"},
		Text:    &text,
		ReplyID: strPtr254(noteID),
	})
	assert.ErrorIs(t, err, note.ErrCannotReplyToAPureRenote)
}

func TestCreateService_CannotReplyToSpecifiedVisibility(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteID := "specified_target"
	noteRepo.Notes[noteID] = &model.Note{
		ID: noteID, UserID: "u1", Visibility: model.NoteVisibilitySpecified,
		VisibleUserIDs: []string{"u1"},
	}
	text := "hi"
	_, err := svc.Create(note.CreateInput{
		User:       &model.User{ID: "u1"},
		Text:       &text,
		ReplyID:    strPtr254(noteID),
		Visibility: model.NoteVisibilityPublic, // widerで不許可
	})
	assert.ErrorIs(t, err, note.ErrCannotReplyToSpecifiedVisibility)
}

func TestCreateService_YouHaveBeenBlocked_Renote(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	blockRepo := testutil.NewMockBlockingRepository()
	svc.SetBlockingRepo(blockRepo)

	// block: target user が actor を block している
	require.NoError(t, blockRepo.Create(&model.Blocking{ID: "b1", BlockerID: "target_user", BlockeeID: "u1"}))

	noteID := "renote_target_blocked"
	noteRepo.Notes[noteID] = &model.Note{
		ID: noteID, UserID: "target_user", Visibility: model.NoteVisibilityPublic,
	}
	_, err := svc.Create(note.CreateInput{
		User:     &model.User{ID: "u1"},
		RenoteID: strPtr254(noteID),
	})
	assert.ErrorIs(t, err, note.ErrYouHaveBeenBlocked)
}

func TestCreateService_YouHaveBeenBlocked_Reply(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	blockRepo := testutil.NewMockBlockingRepository()
	svc.SetBlockingRepo(blockRepo)

	require.NoError(t, blockRepo.Create(&model.Blocking{ID: "b1", BlockerID: "target_user", BlockeeID: "u1"}))

	noteID := "reply_target_blocked"
	noteRepo.Notes[noteID] = &model.Note{
		ID: noteID, UserID: "target_user", Visibility: model.NoteVisibilityPublic,
	}
	text := "hi"
	_, err := svc.Create(note.CreateInput{
		User:    &model.User{ID: "u1"},
		Text:    &text,
		ReplyID: strPtr254(noteID),
	})
	assert.ErrorIs(t, err, note.ErrYouHaveBeenBlocked)
}

func TestCreateService_NoSuchFile(t *testing.T) {
	svc, _, _ := newCreateService(t)
	fileRepo := testutil.NewMockDriveFileRepository()
	svc.SetDriveFileRepo(fileRepo)

	text := "hello"
	_, err := svc.Create(note.CreateInput{
		User:    &model.User{ID: "u1"},
		Text:    &text,
		FileIDs: []string{"nonexistent_file"},
	})
	assert.ErrorIs(t, err, note.ErrNoSuchFile)
}

func TestCreateService_ContainsProhibitedWords(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", ProhibitedWords: []string{"badword"}}
	svc.SetMetaRepo(metaRepo)

	text := "contains badword here"
	_, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"},
		Text: &text,
	})
	assert.ErrorIs(t, err, note.ErrContainsProhibitedWords)
}

// PR #1107 perf regression guard: Create が meta を **1 回だけ** fetch する
// ことを assert。旧版は matchesSensitiveWords / checkProhibitedWords が
// それぞれ独立に Fetch を呼んでいて L1 cache cold な環境では note 作成
// あたり 2 round-trip 発生していた。caller 側で 1 度 fetch して両 helper
// に渡す pure 関数 shape に refactor 済 — その不変条件を guard する。
func TestCreateService_MetaFetchOnlyOncePerCreate(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{
		ID:              "m1",
		SensitiveWords:  []string{"spoiler"},
		ProhibitedWords: []string{"badword"},
	}
	svc.SetMetaRepo(metaRepo)

	text := "hello world"
	_, err := svc.Create(note.CreateInput{
		User:       &model.User{ID: "u1"},
		Text:       &text,
		Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, metaRepo.FetchCalls,
		"Create should fetch meta exactly once (consolidated across sensitive + prohibited check)")
}

// meta.sensitiveWords にマッチする text を持つ public note は home に降格する
// (upstream NoteCreateService と同 semantics、テストユーザー報告の症状)。
func TestCreateService_SensitiveWordsDemotesPublicToHome(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", SensitiveWords: []string{"spoiler"}}
	svc.SetMetaRepo(metaRepo)

	text := "this contains a spoiler about the ending"
	created, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"}, Text: &text, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityHome, created.Visibility,
		"public note matching sensitiveWords must be demoted to home")
}

// mk-go independent hardening (Ed25519 / TOTP replay と同系列): upstream
// は `cw ?? text` で CW を設定すれば text 側の sensitive 検出を bypass
// できる known gap を持つが、mk-go は CW と text を独立に check して bypass
// を塞ぐ。harmless な CW を被せても text に sensitive word があれば降格する。
func TestCreateService_SensitiveWordsCWHarmlessTextMatch_StrictDemotes(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", SensitiveWords: []string{"spoiler"}}
	svc.SetMetaRepo(metaRepo)

	cw := "harmless cw"
	text := "this body contains a spoiler"
	created, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"}, Text: &text, CW: &cw, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityHome, created.Visibility,
		"mk-go strict: text 側に sensitive word があれば CW の有無に関わらず降格 (= upstream bypass を塞ぐ)")
}

// 空 CW + text match のケースも同じく降格する (CW="" は意味的に「CW なし」
// だが upstream は non-nullish 扱いで text を skip していた、mk-go は両方
// check するので両者の挙動差は出ない)。
func TestCreateService_SensitiveWordsEmptyCWTextMatch_StrictDemotes(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", SensitiveWords: []string{"spoiler"}}
	svc.SetMetaRepo(metaRepo)

	empty := ""
	text := "this body contains a spoiler"
	created, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"}, Text: &text, CW: &empty, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityHome, created.Visibility,
		"mk-go strict: 空 CW でも text を独立に check するので降格する")
}

// 両 field とも match するケースは当然降格する (negative case
// "NeitherMatch" と pair で、4 象限の片側 2 つを test 経由で固める)。
func TestCreateService_SensitiveWordsBothCWAndTextMatch_Demotes(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", SensitiveWords: []string{"spoiler"}}
	svc.SetMetaRepo(metaRepo)

	cw := "spoiler tag"
	text := "this body also contains a spoiler"
	created, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"}, Text: &text, CW: &cw, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityHome, created.Visibility)
}

// neither match のケースは降格しない。CW="" + text="clean" + word="spoiler"。
func TestCreateService_SensitiveWordsNeitherMatch_NotDemoted(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", SensitiveWords: []string{"spoiler"}}
	svc.SetMetaRepo(metaRepo)

	cw := "harmless"
	text := "clean body"
	created, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"}, Text: &text, CW: &cw, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityPublic, created.Visibility)
}

// CW フィールドも match 対象 (upstream は cw ?? text を見るので、CW が match
// するだけでも降格)。
func TestCreateService_SensitiveWordsMatchesCWAlone(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", SensitiveWords: []string{"nsfw"}}
	svc.SetMetaRepo(metaRepo)

	cw := "nsfw content warning"
	text := "innocent body text"
	created, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"}, Text: &text, CW: &cw, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityHome, created.Visibility)
}

// upstream-compat regex フィルタが解釈されること。
func TestCreateService_SensitiveWordsRegexFilter(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", SensitiveWords: []string{`/SPOIL.*/i`}}
	svc.SetMetaRepo(metaRepo)

	text := "this is a spoiler"
	created, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"}, Text: &text, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityHome, created.Visibility)
}

// 元 visibility が home / followers / specified のときは降格対象外
// (upstream も `data.visibility === 'public'` でしか降格しない)。
func TestCreateService_SensitiveWordsHomeNoteUnchanged(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", SensitiveWords: []string{"spoiler"}}
	svc.SetMetaRepo(metaRepo)

	text := "spoiler in home post"
	created, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"}, Text: &text, Visibility: model.NoteVisibilityHome,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityHome, created.Visibility,
		"home note は元から home なので降格は no-op")
}

// channel 内の note は降格対象外 (channel が露出範囲を別管理するため
// upstream NoteCreateService も同じく対象外)。
func TestCreateService_SensitiveWordsChannelNoteNotDemoted(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", SensitiveWords: []string{"spoiler"}}
	svc.SetMetaRepo(metaRepo)

	channelID := "ch1"
	text := "spoiler in channel post"
	created, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"}, Text: &text, Visibility: model.NoteVisibilityPublic, ChannelID: &channelID,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityPublic, created.Visibility,
		"channel 内 note は sensitiveWords でも降格しない")
}

// sensitiveWords が空 (旧 mk-go の挙動) なら通常通り public のまま。
func TestCreateService_SensitiveWordsEmptyMetaUnchanged(t *testing.T) {
	svc, _, _ := newCreateService(t)
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "m1", SensitiveWords: nil}
	svc.SetMetaRepo(metaRepo)

	text := "regular content"
	created, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"}, Text: &text, Visibility: model.NoteVisibilityPublic,
	})
	require.NoError(t, err)
	assert.Equal(t, model.NoteVisibilityPublic, created.Visibility)
}

func TestCreateService_ContainsTooManyMentions(t *testing.T) {
	svc, _, _ := newCreateService(t)
	// 21 メンションで default limit (20) 超過
	mentions := ""
	for i := 0; i < 21; i++ {
		mentions += "@user" + strPtr254Str(i) + " "
	}
	_, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "u1"},
		Text: &mentions,
	})
	assert.ErrorIs(t, err, note.ErrContainsTooManyMentions)
}

func strPtr254Str(i int) string {
	// base36: 0-9,a-z
	const chars = "0123456789abcdefghijklmnopqrstuvwxyz"
	if i < 36 {
		return string(chars[i])
	}
	return string(chars[i/36]) + string(chars[i%36])
}

// Devin review #270: IsPureRenote check must not leak note structure to
// unauthorized viewers. invisible な pure renote への reply/renote では
// 「見えない」エラーを返し、pure renote であることが推測できないようにする。
func TestCreateService_InvisiblePureRenote_NoLeak_Reply(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteID := "invisible_pure_renote"
	inner := "original_note"
	noteRepo.Notes[noteID] = &model.Note{
		ID: noteID, UserID: "other_user",
		Visibility: model.NoteVisibilityFollowers, // viewerはfollowしていない
		RenoteID:   &inner,                        // pure renote
	}
	text := "hi"
	_, err := svc.Create(note.CreateInput{
		User:    &model.User{ID: "u1"},
		Text:    &text,
		ReplyID: strPtr254(noteID),
	})
	// pure renote error ではなく invisible error が返るべき
	assert.ErrorIs(t, err, note.ErrCannotReplyToInvisibleNote)
	assert.NotErrorIs(t, err, note.ErrCannotReplyToAPureRenote)
}

func TestCreateService_InvisiblePureRenote_NoLeak_Renote(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteID := "invisible_pure_renote_2"
	inner := "original_note"
	noteRepo.Notes[noteID] = &model.Note{
		ID: noteID, UserID: "other_user",
		Visibility: model.NoteVisibilityFollowers,
		RenoteID:   &inner,
	}
	_, err := svc.Create(note.CreateInput{
		User:     &model.User{ID: "u1"},
		RenoteID: strPtr254(noteID),
	})
	// pure renote error ではなく invisible error
	assert.ErrorIs(t, err, note.ErrCannotRenoteInvisibleNote)
	assert.NotErrorIs(t, err, note.ErrCannotRenoteToAPureRenote)
}

// Devin review #270: 重複 fileId で false positive にならないこと。
func TestCreateService_DuplicateFileIDs_Accepted(t *testing.T) {
	svc, _, _ := newCreateService(t)
	fileRepo := testutil.NewMockDriveFileRepository()
	svc.SetDriveFileRepo(fileRepo)

	uid := "u1"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &uid}

	text := "hello"
	_, err := svc.Create(note.CreateInput{
		User:    &model.User{ID: uid},
		Text:    &text,
		FileIDs: []string{"f1", "f1"}, // 重複
	})
	assert.NoError(t, err)
}

// 所有者違いの file は拒否される (regression)。
func TestCreateService_NoSuchFile_WrongOwner(t *testing.T) {
	svc, _, _ := newCreateService(t)
	fileRepo := testutil.NewMockDriveFileRepository()
	svc.SetDriveFileRepo(fileRepo)

	otherUID := "other_user"
	fileRepo.Files["f1"] = &model.DriveFile{ID: "f1", UserID: &otherUID}

	text := "hello"
	_, err := svc.Create(note.CreateInput{
		User:    &model.User{ID: "u1"},
		Text:    &text,
		FileIDs: []string{"f1"},
	})
	assert.ErrorIs(t, err, note.ErrNoSuchFile)
}

// --- MainStreamPublisher emit (Phase 7-4b-5 / #301) ---

// stubMainStreamPublisher captures PublishMainEvent calls for test assertions.
type stubMainStreamPublisher struct {
	calls []mainEventCall
}

type mainEventCall struct {
	userID    string
	eventType string
	body      any
}

func (s *stubMainStreamPublisher) PublishMainEvent(userID, eventType string, body any) {
	s.calls = append(s.calls, mainEventCall{userID, eventType, body})
}

// eventsByType returns userIDs that received the given event type.
func (s *stubMainStreamPublisher) userIDsOf(eventType string) []string {
	var out []string
	for _, c := range s.calls {
		if c.eventType == eventType {
			out = append(out, c.userID)
		}
	}
	return out
}

func TestCreateService_PublishesReplyToLocalTarget(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)
	// 相手のノート (local)
	noteRepo.Notes["r1"] = &model.Note{
		ID: "r1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	replyID := "r1"
	text := "hi"
	_, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "alice"}, Text: &text, ReplyID: &replyID,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"bob"}, pub.userIDsOf("reply"))
}

func TestCreateService_SkipsReplyToSelf(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)
	noteRepo.Notes["r1"] = &model.Note{
		ID: "r1", UserID: "alice", Visibility: model.NoteVisibilityPublic,
	}
	replyID := "r1"
	text := "self reply"
	_, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "alice"}, Text: &text, ReplyID: &replyID,
	})
	require.NoError(t, err)
	assert.Empty(t, pub.userIDsOf("reply"))
}

func TestCreateService_SkipsReplyToRemoteTarget(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)
	remoteHost := "remote.example"
	noteRepo.Notes["r1"] = &model.Note{
		ID: "r1", UserID: "remoteuser", UserHost: &remoteHost,
		Visibility: model.NoteVisibilityPublic,
	}
	replyID := "r1"
	text := "hi"
	_, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "alice"}, Text: &text, ReplyID: &replyID,
	})
	require.NoError(t, err)
	assert.Empty(t, pub.userIDsOf("reply"))
}

func TestCreateService_PublishesRenoteToLocalTarget(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)
	noteRepo.Notes["r1"] = &model.Note{
		ID: "r1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	renoteID := "r1"
	_, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "alice"}, RenoteID: &renoteID,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"bob"}, pub.userIDsOf("renote"))
}

func TestCreateService_PublishesMentionToLocalUser(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	_ = noteRepo
	userRepo := testutil.NewMockUserRepository()
	userRepo.Users["uA"] = &model.User{ID: "uA", Username: "alice", UsernameLower: "alice"}
	remoteHost := "remote.example"
	userRepo.Users["uB"] = &model.User{
		ID: "uB", Username: "bob", UsernameLower: "bob", Host: &remoteHost,
	}
	svc.SetUserRepo(userRepo)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)

	text := "hi @alice and @bob@remote.example"
	_, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "author1"}, Text: &text,
	})
	require.NoError(t, err)
	// local user (alice) にだけ mention emit、remote bob は skip。
	assert.Equal(t, []string{"uA"}, pub.userIDsOf("mention"))
}

func TestCreateService_SkipsMentionToSelf(t *testing.T) {
	svc, _, _ := newCreateService(t)
	userRepo := testutil.NewMockUserRepository()
	userRepo.Users["author1"] = &model.User{
		ID: "author1", Username: "author1", UsernameLower: "author1",
	}
	svc.SetUserRepo(userRepo)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)

	text := "hi @author1"
	_, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "author1"}, Text: &text,
	})
	require.NoError(t, err)
	assert.Empty(t, pub.userIDsOf("mention"))
}

func TestCreateService_NoMainStreamPublisher_NoEmit(t *testing.T) {
	svc, noteRepo, _ := newCreateService(t)
	noteRepo.Notes["r1"] = &model.Note{
		ID: "r1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	// SetMainStreamPublisher を呼ばず、reply を作成しても panic / error
	// しないことだけ確認。
	replyID := "r1"
	text := "hi"
	_, err := svc.Create(note.CreateInput{
		User: &model.User{ID: "alice"}, Text: &text, ReplyID: &replyID,
	})
	require.NoError(t, err)
}

// --- #1472 visibility gate (publishNoteMainEvents) ---

// followers visibility note を non-follower reply target に publish しない
// (本文 / CW / Files が main stream で leak するのを塞ぐ)。
func TestCreateService_SkipsReplyMainEvent_FollowersToNonFollower(t *testing.T) {
	svc, noteRepo, _ := newCreateServiceWithFollowing(t)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)
	// reply target bob は alice (author) を follow していない
	noteRepo.Notes["r1"] = &model.Note{
		ID: "r1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	replyID := "r1"
	text := "secret followers reply"
	_, err := svc.Create(note.CreateInput{
		User:       &model.User{ID: "alice"},
		Text:       &text,
		ReplyID:    &replyID,
		Visibility: model.NoteVisibilityFollowers,
	})
	require.NoError(t, err)
	assert.Empty(t, pub.userIDsOf("reply"), "non-follower reply target には followers note の main event を流さない")
}

// follower 関係がある reply target には従来通り publish する (regress
// guard: visibility gate が overshoot して legitimate notification を抑え
// ていないか)。
func TestCreateService_PublishesReplyMainEvent_FollowersToFollower(t *testing.T) {
	svc, noteRepo, followingRepo := newCreateServiceWithFollowing(t)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)
	noteRepo.Notes["r1"] = &model.Note{
		ID: "r1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	// bob → alice の follow を planting (= bob は alice を follow している)
	followingRepo.Followings["f1"] = &model.Following{
		ID: "f1", FollowerID: "bob", FolloweeID: "alice",
	}
	replyID := "r1"
	text := "hi follower"
	_, err := svc.Create(note.CreateInput{
		User:       &model.User{ID: "alice"},
		Text:       &text,
		ReplyID:    &replyID,
		Visibility: model.NoteVisibilityFollowers,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"bob"}, pub.userIDsOf("reply"))
}

// specified visibility note を visibleUserIDs 外の reply target に publish
// しない (DM 用 visibility 経路で reply target が ill-formed に含まれて
// しまった場合の defense-in-depth)。
func TestCreateService_SkipsReplyMainEvent_SpecifiedExcludesNonRecipient(t *testing.T) {
	svc, noteRepo, _ := newCreateServiceWithFollowing(t)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)
	noteRepo.Notes["r1"] = &model.Note{
		ID: "r1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	replyID := "r1"
	text := "specified secret"
	_, err := svc.Create(note.CreateInput{
		User:           &model.User{ID: "alice"},
		Text:           &text,
		ReplyID:        &replyID,
		Visibility:     model.NoteVisibilitySpecified,
		VisibleUserIDs: []string{"charlie"}, // bob は含まれない
	})
	require.NoError(t, err)
	assert.Empty(t, pub.userIDsOf("reply"))
}

// specified visibility で reply target が visibleUserIDs に含まれていれば
// publish する。slices.Contains 経路の regress guard。
func TestCreateService_PublishesReplyMainEvent_SpecifiedIncludesRecipient(t *testing.T) {
	svc, noteRepo, _ := newCreateServiceWithFollowing(t)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)
	noteRepo.Notes["r1"] = &model.Note{
		ID: "r1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	replyID := "r1"
	text := "dm"
	_, err := svc.Create(note.CreateInput{
		User:           &model.User{ID: "alice"},
		Text:           &text,
		ReplyID:        &replyID,
		Visibility:     model.NoteVisibilitySpecified,
		VisibleUserIDs: []string{"bob"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"bob"}, pub.userIDsOf("reply"))
}

// mention: followers note を non-follower mentioned user に publish しない。
// #1472 でいちばん見落としていた経路 — `notifyVisibleToTarget` は notification
// 経路で gate するが main stream は独立 publish なので、ここで止めないと
// 本文 / CW が realtime に漏れる。
func TestCreateService_SkipsMentionMainEvent_FollowersToNonFollower(t *testing.T) {
	svc, _, _ := newCreateServiceWithFollowing(t)
	userRepo := testutil.NewMockUserRepository()
	userRepo.Users["uA"] = &model.User{ID: "uA", Username: "alice", UsernameLower: "alice"}
	svc.SetUserRepo(userRepo)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)

	// followingRepo は空 = uA は author を follow していない
	text := "secret @alice"
	_, err := svc.Create(note.CreateInput{
		User:       &model.User{ID: "author1"},
		Text:       &text,
		Visibility: model.NoteVisibilityFollowers,
	})
	require.NoError(t, err)
	assert.Empty(t, pub.userIDsOf("mention"), "non-follower mention に followers note の main event を流さない")
}

// specified visibility で mentioned user が visibleUserIDs に含まれない場合
// (mention 経路で resolve された user が visible 指定外) も skip する。
// upstream #17363 / mk-go #1444 の specified note 通知ルールを main event 側
// にも適用する。
func TestCreateService_SkipsMentionMainEvent_SpecifiedExcludesNonRecipient(t *testing.T) {
	svc, _, _ := newCreateServiceWithFollowing(t)
	userRepo := testutil.NewMockUserRepository()
	userRepo.Users["uA"] = &model.User{ID: "uA", Username: "alice", UsernameLower: "alice"}
	userRepo.Users["uC"] = &model.User{ID: "uC", Username: "charlie", UsernameLower: "charlie"}
	svc.SetUserRepo(userRepo)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)

	text := "specified @alice @charlie"
	_, err := svc.Create(note.CreateInput{
		User:           &model.User{ID: "author1"},
		Text:           &text,
		Visibility:     model.NoteVisibilitySpecified,
		VisibleUserIDs: []string{"uA"}, // charlie は含まれない
	})
	require.NoError(t, err)
	// alice (uA) のみ通知、charlie (uC) は visibleUserIds 外なので skip
	assert.Equal(t, []string{"uA"}, pub.userIDsOf("mention"))
}

// renote target の visibility gate: quote renote (= Text / CW を伴う) を
// followers 設定で投げて renote target が non-follower の場合、本文ごと
// main stream で leak するのを塞ぐ。
func TestCreateService_SkipsRenoteMainEvent_FollowersToNonFollower(t *testing.T) {
	svc, noteRepo, _ := newCreateServiceWithFollowing(t)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)
	noteRepo.Notes["r1"] = &model.Note{
		ID: "r1", UserID: "bob", Visibility: model.NoteVisibilityPublic,
	}
	renoteID := "r1"
	text := "quote secret"
	_, err := svc.Create(note.CreateInput{
		User:       &model.User{ID: "alice"},
		Text:       &text,
		RenoteID:   &renoteID,
		Visibility: model.NoteVisibilityFollowers,
	})
	require.NoError(t, err)
	assert.Empty(t, pub.userIDsOf("renote"))
}

func TestSafeGo_RecoversPanic(t *testing.T) {
	done := make(chan struct{})
	note.SafeGoForTest(func() {
		defer func() { close(done) }()
		panic("test panic")
	})
	select {
	case <-done:
		// パニックから回復して正常終了
	case <-time.After(5 * time.Second):
		t.Fatal("safeGo did not recover panic")
	}
}
