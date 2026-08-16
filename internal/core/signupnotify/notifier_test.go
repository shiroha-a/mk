package signupnotify

import (
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubBot struct {
	user *model.User
	err  error
}

func (s *stubBot) Ensure() (*model.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.user, nil
}

type stubResolver struct {
	user     *model.User
	err      error
	lastURI  string
	resolved int
}

func (s *stubResolver) ResolveActor(uri string) (*model.User, error) {
	s.lastURI = uri
	s.resolved++
	if s.err != nil {
		return nil, s.err
	}
	return s.user, nil
}

type stubNotes struct {
	last note.CreateInput
	err  error
	n    int
}

func (s *stubNotes) Create(in note.CreateInput) (*model.Note, error) {
	s.last = in
	s.n++
	if s.err != nil {
		return nil, s.err
	}
	return &model.Note{ID: "note-1"}, nil
}

func strPtr(s string) *string { return &s }

func newTestNotifier(t *testing.T) (*Notifier, *stubBot, *stubResolver, *stubNotes) {
	t.Helper()
	bot := &stubBot{user: &model.User{ID: "bot-1", Username: "registration_service"}}
	resolver := &stubResolver{user: &model.User{
		ID: "remote-1", Username: "alice", Host: strPtr("remote.example"),
	}}
	notes := &stubNotes{}
	return NewNotifier(bot, resolver, notes, "https://mk.example"), bot, resolver, notes
}

func testApplication() *model.SignupApplication {
	return &model.SignupApplication{
		ID:              "app-1",
		ContactHost:     "remote.example",
		ContactRemoteID: "9abc",
		ContactUsername: "alice",
		Status:          model.SignupApplicationApproved,
	}
}

func TestSend_Approved(t *testing.T) {
	n, _, resolver, notes := newTestNotifier(t)

	require.NoError(t, n.send(testApplication(), true))

	// actor URI は Misskey の規約から組み立てる (`${url}/users/${id}`)。
	assert.Equal(t, "https://remote.example/users/9abc", resolver.lastURI)

	require.Equal(t, 1, notes.n)
	in := notes.last
	assert.Equal(t, "bot-1", in.User.ID)
	// **DM であること。** 公開の投稿にすると、申請していることが他人に見える。
	assert.Equal(t, model.NoteVisibilitySpecified, in.Visibility)
	assert.Equal(t, []string{"remote-1"}, in.VisibleUserIDs)

	require.NotNil(t, in.Text)
	assert.Contains(t, *in.Text, "@alice@remote.example")
	assert.Contains(t, *in.Text, "承認されました")
	assert.Contains(t, *in.Text, "https://mk.example/signup-application")
}

func TestSend_Rejected(t *testing.T) {
	n, _, _, notes := newTestNotifier(t)

	require.NoError(t, n.send(testApplication(), false))

	require.NotNil(t, notes.last.Text)
	body := *notes.last.Text
	assert.Contains(t, body, "承認されませんでした")
	// **却下の理由は書かない。** 審査基準を推測されるため。
	assert.NotContains(t, body, "理由")
	// 却下なのに登録ページへ誘導しない。
	assert.NotContains(t, body, "/signup-application")
	// **どのサーバーからの通知か分かるようにする。**
	assert.Contains(t, body, "https://mk.example")
}

// **秘密は載せない。** 登録 URL も招待コードも要らない — 申請者は登録ページで
// MiAuth を通せば続きから進める。
func TestSend_CarriesNoSecret(t *testing.T) {
	n, _, _, notes := newTestNotifier(t)
	app := testApplication()
	app.TicketID = strPtr("ticket-secret-1")

	require.NoError(t, n.send(app, true))
	assert.NotContains(t, *notes.last.Text, "ticket-secret-1")
}

// 解決結果が別 host のユーザーなら送らない。**誤った相手に承認の通知が飛ぶ。**
func TestSend_RejectsContactOnAnotherHost(t *testing.T) {
	tests := []struct {
		name string
		user *model.User
	}{
		{name: "different host", user: &model.User{ID: "x", Username: "alice", Host: strPtr("evil.example")}},
		{name: "local user", user: &model.User{ID: "x", Username: "alice"}},
		{name: "nil", user: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, _, resolver, notes := newTestNotifier(t)
			resolver.user = tt.user

			assert.Error(t, n.send(testApplication(), true))
			assert.Zero(t, notes.n, "送らないこと")
		})
	}
}

// host の大文字小文字の違いで弾かないこと。
func TestSend_HostComparisonIsCaseInsensitive(t *testing.T) {
	n, _, resolver, notes := newTestNotifier(t)
	resolver.user = &model.User{ID: "remote-1", Username: "alice", Host: strPtr("Remote.Example")}

	require.NoError(t, n.send(testApplication(), true))
	assert.Equal(t, 1, notes.n)
}

func TestSend_Failures(t *testing.T) {
	t.Run("bot unavailable", func(t *testing.T) {
		n, bot, _, notes := newTestNotifier(t)
		bot.err = errors.New("boom")
		assert.Error(t, n.send(testApplication(), true))
		assert.Zero(t, notes.n)
	})

	t.Run("contact unresolvable", func(t *testing.T) {
		n, _, resolver, notes := newTestNotifier(t)
		resolver.err = errors.New("boom")
		assert.Error(t, n.send(testApplication(), true))
		assert.Zero(t, notes.n)
	})

	t.Run("note create fails", func(t *testing.T) {
		n, _, _, notes := newTestNotifier(t)
		notes.err = errors.New("boom")
		assert.Error(t, n.send(testApplication(), true))
	})

	t.Run("incomplete contact", func(t *testing.T) {
		n, _, _, notes := newTestNotifier(t)
		assert.Error(t, n.send(nil, true))
		assert.Error(t, n.send(&model.SignupApplication{ID: "x"}, true))
		assert.Error(t, n.send(&model.SignupApplication{ID: "x", ContactHost: "remote.example"}, true))
		assert.Zero(t, notes.n)
	})

	t.Run("not wired", func(t *testing.T) {
		assert.Error(t, NewNotifier(nil, nil, nil, "").send(testApplication(), true))
	})
}

// 公開 URL が無くても本文は成立すること (誘導の行が落ちるだけ)。
func TestSend_WithoutPublicURL(t *testing.T) {
	bot := &stubBot{user: &model.User{ID: "bot-1"}}
	resolver := &stubResolver{user: &model.User{ID: "remote-1", Username: "alice", Host: strPtr("remote.example")}}
	notes := &stubNotes{}
	n := NewNotifier(bot, resolver, notes, "")

	require.NoError(t, n.send(testApplication(), true))
	assert.Contains(t, *notes.last.Text, "承認されました")
	assert.NotContains(t, *notes.last.Text, "signup-application")
}

func TestApplicationURL_TrimsTrailingSlash(t *testing.T) {
	n := NewNotifier(nil, nil, nil, "https://mk.example/")
	assert.Equal(t, "https://mk.example/signup-application", n.applicationURL())
}

// 失敗してもログを出すだけで、呼び出し側には伝播しない (best-effort)。
func TestSendLogged_SwallowsErrors(t *testing.T) {
	n, bot, _, _ := newTestNotifier(t)
	bot.err = errors.New("boom")
	assert.NotPanics(t, func() { n.sendLogged(testApplication(), true) })
}
