package activitypub

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func newRenderer() *Renderer {
	return NewRenderer(NewURLBuilder("https://example.com"))
}

func TestURLBuilder_Helpers(t *testing.T) {
	b := NewURLBuilder("https://example.com")
	assert.Equal(t, "https://example.com/users/u1", b.UserURI("u1"))
	assert.Equal(t, "https://example.com/users/u1/inbox", b.UserInbox("u1"))
	assert.Equal(t, "https://example.com/users/u1/outbox", b.UserOutbox("u1"))
	assert.Equal(t, "https://example.com/users/u1/followers", b.UserFollowers("u1"))
	assert.Equal(t, "https://example.com/users/u1/following", b.UserFollowing("u1"))
	assert.Equal(t, "https://example.com/users/u1#main-key", b.UserKeyURI("u1"))
	assert.Equal(t, "https://example.com/inbox", b.SharedInbox())
	assert.Equal(t, "https://example.com/notes/n1", b.NoteURI("n1"))
	assert.Equal(t, "https://example.com/notes/n1/activity", b.CreateActivityURI("n1"))
	// 短いIDはそのまま使われる
	assert.Equal(t, "https://example.com/follows/a/b", b.FollowURI("a", "b"))
}

func TestURLBuilder_FollowURI_FullURI(t *testing.T) {
	b := NewURLBuilder("https://example.com")
	// 完全なURIはハッシュ化されてパスセグメントに入る
	uri := b.FollowURI("alice", "https://remote.example/users/bob")
	assert.True(t, strings.HasPrefix(uri, "https://example.com/follows/alice/"), "URI should start with follows prefix")
	assert.NotContains(t, uri, "https://remote.example")
	// 同じ入力で同じ出力
	assert.Equal(t, uri, b.FollowURI("alice", "https://remote.example/users/bob"))
}

func TestRenderer_RenderPerson(t *testing.T) {
	r := newRenderer()
	avatar := "https://example.com/avatar.png"
	name := "Alice"
	u := &model.User{
		ID:           "u1",
		Username:     "alice",
		Name:         &name,
		AvatarURL:    &avatar,
		IsLocked:     true,
		IsExplorable: true,
	}
	p := r.RenderPerson(u, nil, "PUBKEY")
	assert.Equal(t, "https://example.com/users/u1", p.ID)
	assert.Equal(t, "Person", p.Type)
	assert.Equal(t, "alice", p.PreferredUsername)
	assert.Equal(t, "Alice", p.Name)
	assert.Equal(t, "PUBKEY", p.PublicKey.PublicKeyPEM)
	assert.True(t, p.ManuallyApproves)
	assert.True(t, p.Discoverable)
	require.NotNil(t, p.Icon)
	assert.Equal(t, avatar, p.Icon.URL)
}

func TestRenderer_RenderPerson_NoOptionalFields(t *testing.T) {
	r := newRenderer()
	u := &model.User{ID: "u1", Username: "alice"}
	p := r.RenderPerson(u, nil, "PUBKEY")
	assert.Empty(t, p.Name)
	assert.Nil(t, p.Icon)
	assert.False(t, p.ManuallyApproves)
}

func TestRenderer_RenderPerson_MisskeyCanChat(t *testing.T) {
	// chatScope=='none' の local user は AP 上 `_misskey_canChat: false` を
	// expose し、それ以外は true。pointer なので両ケースで non-nil (#692)。
	cases := []struct {
		scope string
		want  bool
	}{
		{"", true},
		{"everyone", true},
		{"followers", true},
		{"following", true},
		{"mutual", true},
		{"none", false},
	}
	r := newRenderer()
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			u := &model.User{ID: "u1", Username: "alice", ChatScope: tc.scope}
			p := r.RenderPerson(u, nil, "PUBKEY")
			require.NotNil(t, p.MisskeyCanChat)
			assert.Equal(t, tc.want, *p.MisskeyCanChat)
		})
	}
}

func newIDGen(t *testing.T) id.Generator {
	t.Helper()
	g, _ := id.NewGenerator("aidx")
	return g
}

func TestRenderer_RenderNote_Public(t *testing.T) {
	r := newRenderer()
	idGen := newIDGen(t)
	noteID := idGen.Generate(time.Now())
	text := "hello"
	cw := "warning"
	n := &model.Note{
		ID:         noteID,
		UserID:     "author",
		Text:       &text,
		CW:         &cw,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(n, idGen)
	assert.Equal(t, "Note", out.Type)
	assert.Equal(t, "hello", out.Content)
	assert.Equal(t, "warning", out.Summary)
	assert.True(t, out.Sensitive)
	assert.Contains(t, out.To, Public)
	assert.Contains(t, out.CC, "https://example.com/users/author/followers")
}

func TestRenderer_RenderNote_Home(t *testing.T) {
	r := newRenderer()
	idGen := newIDGen(t)
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Visibility: model.NoteVisibilityHome,
	}
	out := r.RenderNote(n, idGen)
	assert.Contains(t, out.To, "https://example.com/users/author/followers")
	assert.Contains(t, out.CC, Public)
}

func TestRenderer_RenderNote_Followers(t *testing.T) {
	r := newRenderer()
	idGen := newIDGen(t)
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Visibility: model.NoteVisibilityFollowers,
	}
	out := r.RenderNote(n, idGen)
	assert.Contains(t, out.To, "https://example.com/users/author/followers")
	assert.Empty(t, out.CC)
}

func TestRenderer_RenderNote_Specified(t *testing.T) {
	r := newRenderer()
	idGen := newIDGen(t)
	n := &model.Note{
		ID:             idGen.Generate(time.Now()),
		UserID:         "author",
		Visibility:     model.NoteVisibilitySpecified,
		VisibleUserIDs: pq.StringArray{"u2", "u3"},
	}
	out := r.RenderNote(n, idGen)
	assert.Contains(t, out.To, "https://example.com/users/u2")
	assert.Contains(t, out.To, "https://example.com/users/u3")
}

// stubMentionResolver returns fixed data keyed by userID.
type stubMentionResolver struct {
	entries map[string]struct{ name, uri string }
}

func (s *stubMentionResolver) ResolveMention(userID string) (name, uri string, err error) {
	e, exists := s.entries[userID]
	if !exists {
		return "", "", ErrMentionUserNotFound
	}
	return e.name, e.uri, nil
}

func TestRenderer_RenderNote_WithMentions(t *testing.T) {
	r := newRenderer()
	r.SetMentionResolver(&stubMentionResolver{entries: map[string]struct{ name, uri string }{
		"uA": {name: "@alice", uri: "https://example.com/users/uA"},
		"uB": {name: "@bob@remote.example", uri: "https://remote.example/users/bob"},
	}})
	idGen := newIDGen(t)
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Visibility: model.NoteVisibilityPublic,
		Mentions:   pq.StringArray{"uA", "uB", "unknown"},
	}
	out := r.RenderNote(n, idGen)

	require.Len(t, out.Tag, 2)
	// unknown user is skipped; both known users end up in tag and to.
	tagged := map[string]string{}
	for _, t := range out.Tag {
		m := t.(Mention)
		tagged[m.Href] = m.Name
	}
	assert.Equal(t, "@alice", tagged["https://example.com/users/uA"])
	assert.Equal(t, "@bob@remote.example", tagged["https://remote.example/users/bob"])
	assert.Contains(t, out.To, "https://example.com/users/uA")
	assert.Contains(t, out.To, "https://remote.example/users/bob")
}

func TestRenderer_RenderNote_WithMentions_DuplicateTo(t *testing.T) {
	// When a mention URI is already in `to` (e.g., Specified visibility with the
	// same user targeted), we must not duplicate it.
	r := newRenderer()
	r.SetMentionResolver(&stubMentionResolver{entries: map[string]struct{ name, uri string }{
		"u2": {name: "@bob", uri: "https://example.com/users/u2"},
	}})
	idGen := newIDGen(t)
	n := &model.Note{
		ID:             idGen.Generate(time.Now()),
		UserID:         "author",
		Visibility:     model.NoteVisibilitySpecified,
		VisibleUserIDs: pq.StringArray{"u2"},
		Mentions:       pq.StringArray{"u2"},
	}
	out := r.RenderNote(n, idGen)
	// u2 URI appears only once.
	count := 0
	for _, v := range out.To {
		if v == "https://example.com/users/u2" {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestRenderer_RenderNote_Reply(t *testing.T) {
	r := newRenderer()
	idGen := newIDGen(t)
	parentID := "parent"
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		ReplyID:    &parentID,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(n, idGen)
	// noteResolver が未設定なので local URI にフォールバックする。
	assert.Equal(t, "https://example.com/notes/parent", out.InReplyTo)
}

// リモート note への reply は note.URI を使い、ローカル URL にしない。
// drop-in #369 で TS-B が `https://a/notes/<a-copy-id>` を fetch して 404 に
// なる問題を防ぐための回帰テスト。
func TestRenderer_RenderNote_Reply_RemoteTarget(t *testing.T) {
	parentID := "parent-remote"
	remoteURI := "https://remote.example/notes/original-id"
	remoteNote := &model.Note{
		ID:  parentID,
		URI: &remoteURI,
	}
	r := NewRenderer(NewURLBuilder("https://example.com"))
	r.SetNoteResolver(&stubNoteResolver{note: remoteNote})

	idGen := newIDGen(t)
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		ReplyID:    &parentID,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(n, idGen)
	assert.Equal(t, remoteURI, out.InReplyTo)
}

func TestRenderer_RenderNote_InvalidIDFallback(t *testing.T) {
	r := newRenderer()
	idGen := newIDGen(t)
	n := &model.Note{
		ID:         "not-a-real-id",
		UserID:     "author",
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(n, idGen)
	assert.NotEmpty(t, out.Published) // フォールバックでもpublishedは入る
}

func TestRenderer_RenderCreate(t *testing.T) {
	r := newRenderer()
	idGen := newIDGen(t)
	noteID := idGen.Generate(time.Now())
	n := &model.Note{ID: noteID, UserID: "author", Visibility: model.NoteVisibilityPublic}
	c := r.RenderCreate(n, idGen)
	assert.Equal(t, "Create", c.Type)
	assert.Equal(t, "https://example.com/users/author", c.Actor)
	assert.NotNil(t, c.Object)
}

func TestRenderer_RenderFollow(t *testing.T) {
	r := newRenderer()
	f := r.RenderFollow("alice", "https://remote.example/users/bob")
	assert.Equal(t, "Follow", f.Type)
	assert.Equal(t, "https://example.com/users/alice", f.Actor)
	assert.Equal(t, "https://remote.example/users/bob", f.Object)
	// IDはURI部分がハッシュ化されている
	assert.Contains(t, f.ID, "https://example.com/follows/alice/")
	assert.NotContains(t, f.ID, "https://remote.example")
}

func TestRenderer_RenderAccept(t *testing.T) {
	r := newRenderer()
	inner := map[string]any{"type": "Follow"}
	a := r.RenderAccept("alice", inner)
	assert.Equal(t, "Accept", a.Type)
	assert.Equal(t, "https://example.com/users/alice", a.Actor)
	assert.NotNil(t, a.Object)
	// id は Misskey/CherryPick の InboxProcessorService が必須としている
	// ("skip: activity id is not a string" で弾かれる)。{baseURL}/{UUID}
	// 形式で必ず埋める。
	require.NotEmpty(t, a.ID)
	assert.True(t, strings.HasPrefix(a.ID, "https://example.com/"), "id should be rooted at baseURL: %s", a.ID)
}

func TestRenderer_RenderAcceptIDUnique(t *testing.T) {
	// 同じrendererからの複数のAcceptは毎回違うidを持つ (UUIDベース)。
	r := newRenderer()
	a1 := r.RenderAccept("alice", map[string]any{"type": "Follow"})
	a2 := r.RenderAccept("alice", map[string]any{"type": "Follow"})
	assert.NotEqual(t, a1.ID, a2.ID)
}

func TestRenderer_RenderFollowRelay(t *testing.T) {
	r := newRenderer()
	f := r.RenderFollowRelay("rel123", "relay-user-id")
	assert.Equal(t, "Follow", f.Type)
	assert.Equal(t, "https://example.com/users/relay-user-id", f.Actor)
	// id は /activities/follow-relay/{relayID} 固定 (processor 側が regex で検出)
	assert.Equal(t, "https://example.com/activities/follow-relay/rel123", f.ID)
	// object は AS Public (relay は全 public 投稿を購読するため)
	assert.Equal(t, Public, f.Object)
}

func TestURLBuilder_FollowRelayURI(t *testing.T) {
	b := NewURLBuilder("https://example.com")
	assert.Equal(t, "https://example.com/activities/follow-relay/abc", b.FollowRelayURI("abc"))
}

func TestStringValue(t *testing.T) {
	s := "x"
	assert.Equal(t, "x", stringValue(&s))
	assert.Equal(t, "", stringValue(nil))
}

func TestRenderer_RenderLike(t *testing.T) {
	r := newRenderer()
	reactor := &model.User{ID: "alice"}
	l := r.RenderLike(reactor, "https://remote.example/notes/n1", "🎉", "https://example.com/likes/l1")
	assert.Equal(t, "Like", l.Type)
	assert.Equal(t, "https://example.com/users/alice", l.Actor)
	assert.Equal(t, "https://remote.example/notes/n1", l.Object)
	assert.Equal(t, "🎉", l.Content)
	assert.Equal(t, "🎉", l.MisskeyReaction)
	assert.Equal(t, "https://example.com/likes/l1", l.ID)
}

func TestRenderer_RenderLike_MisskeyReactionField(t *testing.T) {
	r := newRenderer()
	reactor := &model.User{ID: "alice"}
	l := r.RenderLike(reactor, "https://remote.example/notes/n1", "⭐", "https://example.com/likes/l2")
	// content と _misskey_reaction が両方セットされること
	assert.Equal(t, "⭐", l.Content)
	assert.Equal(t, "⭐", l.MisskeyReaction)
}

// Custom emoji リアクションは Like.Tag に Emoji オブジェクトを 1 件詰めて
// 連合先で icon URL を解決させる必要がある (#634)。
func TestRenderer_RenderLike_LocalCustomEmojiTag(t *testing.T) {
	r := newRenderer()
	r.SetEmojiResolver(&stubEmojiResolver{emojis: map[string]*model.Emoji{
		"smile": {Name: "smile", PublicURL: "https://example.com/files/smile.webp"},
	}})
	reactor := &model.User{ID: "alice"}
	// canonical local form (TS が `:name@.:` で送るのを mk-go も再現)
	l := r.RenderLike(reactor, "https://remote.example/notes/n1", ":smile@.:", "https://example.com/likes/l3")
	require.Len(t, l.Tag, 1)
	et, ok := l.Tag[0].(EmojiTag)
	require.True(t, ok)
	assert.Equal(t, "Emoji", et.Type)
	assert.Equal(t, ":smile:", et.Name)
	assert.Equal(t, "https://example.com/files/smile.webp", et.Icon.URL)
}

func TestRenderer_RenderLike_RemoteCustomEmojiTag(t *testing.T) {
	r := newRenderer()
	r.SetEmojiResolver(&stubEmojiResolver{emojis: map[string]*model.Emoji{
		"foo": {Name: "foo", PublicURL: "https://remote.example/emojis/foo.webp"},
	}})
	reactor := &model.User{ID: "alice"}
	l := r.RenderLike(reactor, "https://remote.example/notes/n1", ":foo@remote.example:", "https://example.com/likes/l4")
	require.Len(t, l.Tag, 1)
	et, ok := l.Tag[0].(EmojiTag)
	require.True(t, ok)
	assert.Equal(t, ":foo:", et.Name)
	assert.Equal(t, "https://remote.example/emojis/foo.webp", et.Icon.URL)
}

func TestRenderer_RenderLike_LegacyLocalNoSuffix(t *testing.T) {
	r := newRenderer()
	r.SetEmojiResolver(&stubEmojiResolver{emojis: map[string]*model.Emoji{
		"legacy": {Name: "legacy", PublicURL: "https://example.com/files/legacy.webp"},
	}})
	reactor := &model.User{ID: "alice"}
	// `:name:` (no `@.` suffix) — TS 旧形式の reaction 文字列がたまたま渡ってきても tag を出せること
	l := r.RenderLike(reactor, "https://remote.example/notes/n1", ":legacy:", "https://example.com/likes/l5")
	require.Len(t, l.Tag, 1)
	et, ok := l.Tag[0].(EmojiTag)
	require.True(t, ok)
	assert.Equal(t, ":legacy:", et.Name)
}

func TestRenderer_RenderLike_UnicodeEmojiNoTag(t *testing.T) {
	// Unicode 絵文字は Tag 不要 (MFM 上で `:` で囲まれていない)。
	r := newRenderer()
	reactor := &model.User{ID: "alice"}
	l := r.RenderLike(reactor, "https://remote.example/notes/n1", "🎉", "https://example.com/likes/l6")
	assert.Empty(t, l.Tag)
}

func TestRenderer_RenderLike_UnknownCustomEmojiNoTag(t *testing.T) {
	// emojiResolver にヒットしない名前は Tag に何も詰まらない (silent skip)。
	// 連合相手は icon URL なしで text fallback、reaction 文字列だけは Content
	// に残すので heart fallback には落ちない。
	r := newRenderer()
	r.SetEmojiResolver(&stubEmojiResolver{emojis: map[string]*model.Emoji{}})
	reactor := &model.User{ID: "alice"}
	l := r.RenderLike(reactor, "https://remote.example/notes/n1", ":missing@.:", "https://example.com/likes/l7")
	assert.Empty(t, l.Tag)
	assert.Equal(t, ":missing@.:", l.Content)
}

func TestRenderer_RenderLike_NoEmojiResolverNoTag(t *testing.T) {
	// emojiResolver 未配線時 (テスト simplification 等) は Tag を出さない。
	// existing TestRenderer_RenderLike (unicode) のロジックを壊さない確認。
	r := newRenderer()
	reactor := &model.User{ID: "alice"}
	l := r.RenderLike(reactor, "https://remote.example/notes/n1", ":any@.:", "https://example.com/likes/l8")
	assert.Empty(t, l.Tag)
}

func TestRenderer_RenderUndoLike(t *testing.T) {
	r := newRenderer()
	reactor := &model.User{ID: "alice"}
	like := r.RenderLike(reactor, "https://remote.example/notes/n1", "🎉", "https://example.com/likes/l1")
	u := r.RenderUndoLike(reactor, like)
	assert.Equal(t, "Undo", u.Type)
	assert.Equal(t, "https://example.com/users/alice", u.Actor)
	assert.Equal(t, "https://example.com/likes/l1/undo", u.ID)
	require.NotNil(t, u.Object)
}

func TestRenderer_RenderAnnounce(t *testing.T) {
	r := newRenderer()
	renoter := &model.User{ID: "alice"}
	a := r.RenderAnnounce(renoter, "renote1", "https://remote.example/notes/orig")
	assert.Equal(t, "Announce", a.Type)
	assert.Equal(t, "https://example.com/users/alice", a.Actor)
	assert.Equal(t, "https://remote.example/notes/orig", a.Object)
	assert.Equal(t, "https://example.com/notes/renote1/activity", a.ID)
	assert.Contains(t, a.To, Public)
	assert.Contains(t, a.CC, "https://example.com/users/alice/followers")
}

func TestRenderer_RenderUndoAnnounce(t *testing.T) {
	r := newRenderer()
	renoter := &model.User{ID: "alice"}
	announce := r.RenderAnnounce(renoter, "renote1", "https://remote.example/notes/orig")
	u := r.RenderUndoAnnounce(renoter, announce)
	assert.Equal(t, "Undo", u.Type)
	assert.Equal(t, "https://example.com/users/alice", u.Actor)
	assert.Equal(t, announce.ID+"/undo", u.ID)
}

func TestRenderer_RenderDelete(t *testing.T) {
	r := newRenderer()
	author := &model.User{ID: "alice"}
	d := r.RenderDelete(author, "https://example.com/notes/n1")
	assert.Equal(t, "Delete", d.Type)
	assert.Equal(t, "https://example.com/users/alice", d.Actor)
	assert.Equal(t, "https://example.com/notes/n1#Delete", d.ID)
	tomb, ok := d.Object.(Tombstone)
	require.True(t, ok)
	assert.Equal(t, "Tombstone", tomb.Type)
	assert.Equal(t, "https://example.com/notes/n1", tomb.ID)
	assert.Contains(t, d.To, Public)
}

func TestRenderer_RenderPerson_Bot(t *testing.T) {
	r := newRenderer()
	u := &model.User{ID: "u1", Username: "bot", IsBot: true}
	p := r.RenderPerson(u, nil, "PUBKEY")
	assert.Equal(t, "Service", p.Type)
}

func TestRenderer_RenderPerson_WithProfile(t *testing.T) {
	r := newRenderer()
	r.SetHost("example.com")
	desc := "**hello** world"
	bday := "2000-01-01"
	loc := "Tokyo"
	followedMsg := "Thanks!"
	fields := datatypes.JSON(`[{"name":"Website","value":"https://example.com"}]`)
	u := &model.User{ID: "u1", Username: "alice", IsCat: true}
	profile := &model.UserProfile{
		Description:     &desc,
		Birthday:        &bday,
		Location:        &loc,
		FollowedMessage: &followedMsg,
		Fields:          fields,
	}
	p := r.RenderPerson(u, profile, "PUBKEY")
	assert.Contains(t, p.Summary, "<b>hello</b>")
	assert.Equal(t, desc, p.MisskeySummary)
	assert.Equal(t, "2000-01-01", p.VcardBday)
	assert.Equal(t, "Tokyo", p.VcardAddress)
	assert.Equal(t, "Thanks!", p.MisskeyFollowedMessage)
	assert.True(t, p.IsCat)
	require.Len(t, p.Attachment, 1)
	pv, ok := p.Attachment[0].(PropertyValue)
	require.True(t, ok)
	assert.Equal(t, "PropertyValue", pv.Type)
	assert.Equal(t, "Website", pv.Name)
	assert.Equal(t, "https://example.com", pv.Value)
}

func TestRenderer_RenderPerson_BannerAndMovedTo(t *testing.T) {
	r := newRenderer()
	banner := "https://example.com/banner.png"
	movedTo := "https://other.example/users/alice"
	alsoKnownAs := "https://other.example/users/alice,https://third.example/users/a"
	featured := "https://example.com/users/u1/collections/featured"
	u := &model.User{
		ID:          "u1",
		Username:    "alice",
		BannerURL:   &banner,
		MovedToURI:  &movedTo,
		AlsoKnownAs: &alsoKnownAs,
		Featured:    &featured,
	}
	p := r.RenderPerson(u, nil, "PUBKEY")
	require.NotNil(t, p.Image)
	assert.Equal(t, banner, p.Image.URL)
	assert.Equal(t, movedTo, p.MovedTo)
	assert.Equal(t, []string{"https://other.example/users/alice", "https://third.example/users/a"}, p.AlsoKnownAs)
	assert.Equal(t, featured, p.Featured)
}

func TestRenderer_RenderPerson_MisskeyExtensionFields(t *testing.T) {
	r := newRenderer()
	before := 30
	u := &model.User{
		ID:                           "u1",
		Username:                     "alice",
		RequireSigninToViewContents:  true,
		MakeNotesFollowersOnlyBefore: &before,
		MakeNotesHiddenBefore:        nil,
	}
	p := r.RenderPerson(u, nil, "PUBKEY")
	assert.True(t, p.MisskeyRequireSigninToViewContents)
	require.NotNil(t, p.MisskeyMakeNotesFollowersOnlyBefore)
	assert.Equal(t, 30, *p.MisskeyMakeNotesFollowersOnlyBefore)
	assert.Nil(t, p.MisskeyMakeNotesHiddenBefore)
}

func TestRenderer_RenderPerson_MisskeyExtensionFields_Defaults(t *testing.T) {
	r := newRenderer()
	u := &model.User{ID: "u1", Username: "alice"}
	p := r.RenderPerson(u, nil, "PUBKEY")
	// デフォルト (false / nil) は omitempty で出力されない
	assert.False(t, p.MisskeyRequireSigninToViewContents)
	assert.Nil(t, p.MisskeyMakeNotesFollowersOnlyBefore)
	assert.Nil(t, p.MisskeyMakeNotesHiddenBefore)
}

func TestMisskeyContext_ContainsNewFields(t *testing.T) {
	ctx := MisskeyContext
	assert.Contains(t, ctx, "_misskey_requireSigninToViewContents")
	assert.Contains(t, ctx, "_misskey_makeNotesFollowersOnlyBefore")
	assert.Contains(t, ctx, "_misskey_makeNotesHiddenBefore")
	assert.Contains(t, ctx, "_misskey_license")
	assert.Contains(t, ctx, "freeText")
}

// stubFileResolver returns fixed drive files.
type stubFileResolver struct {
	files []*model.DriveFile
	err   error
}

func (s *stubFileResolver) FindByIDs(_ []string) ([]*model.DriveFile, error) {
	return s.files, s.err
}

func TestRenderer_RenderNote_WithAttachment(t *testing.T) {
	r := newRenderer()
	comment := "photo"
	r.SetFileResolver(&stubFileResolver{files: []*model.DriveFile{
		{Type: "image/png", URL: "https://example.com/files/f1.png", Comment: &comment, IsSensitive: false},
		{Type: "video/mp4", URL: "https://example.com/files/f2.mp4", IsSensitive: true},
	}})
	idGen := newIDGen(t)
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Visibility: model.NoteVisibilityPublic,
		FileIDs:    pq.StringArray{"f1", "f2"},
	}
	out := r.RenderNote(n, idGen)
	require.Len(t, out.Attachment, 2)
	doc0, ok := out.Attachment[0].(Document)
	require.True(t, ok)
	assert.Equal(t, "Document", doc0.Type)
	assert.Equal(t, "image/png", doc0.MediaType)
	assert.Equal(t, "photo", doc0.Name)
	assert.False(t, doc0.Sensitive)
	doc1, ok := out.Attachment[1].(Document)
	require.True(t, ok)
	assert.True(t, doc1.Sensitive)
	// sensitive ファイルがあるのでNote自体もsensitiveになる
	assert.True(t, out.Sensitive)
}

func TestRenderer_RenderNote_AttachmentError(t *testing.T) {
	r := newRenderer()
	r.SetFileResolver(&stubFileResolver{err: errors.New("db error")})
	idGen := newIDGen(t)
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Visibility: model.NoteVisibilityPublic,
		FileIDs:    pq.StringArray{"f1"},
	}
	out := r.RenderNote(n, idGen)
	// エラー時は添付なしで進む
	assert.Empty(t, out.Attachment)
}

func TestRenderer_RenderNote_WithMFMContent(t *testing.T) {
	r := newRenderer()
	r.SetHost("example.com")
	idGen := newIDGen(t)
	text := "**bold** and `code`"
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Text:       &text,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(n, idGen)
	assert.Contains(t, out.Content, "<b>bold</b>")
	assert.Contains(t, out.Content, "<code>code</code>")
	// MFMマークアップあり → _misskey_content / source が設定される
	assert.Equal(t, text, out.MisskeyContent)
	require.NotNil(t, out.Source)
	assert.Equal(t, text, out.Source.Content)
	assert.Equal(t, "text/x.misskeymarkdown", out.Source.MediaType)
}

func TestRenderer_RenderNote_SimpleContent(t *testing.T) {
	r := newRenderer()
	idGen := newIDGen(t)
	text := "hello world"
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Text:       &text,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(n, idGen)
	assert.Equal(t, "hello world", out.Content)
	// 単純テキストのみ → _misskey_content / source 省略
	assert.Empty(t, out.MisskeyContent)
	assert.Nil(t, out.Source)
}

// stubNoteResolver returns a fixed note.
type stubNoteResolver struct {
	note *model.Note
	err  error
}

func (s *stubNoteResolver) FindByID(_ string) (*model.Note, error) {
	return s.note, s.err
}

func TestRenderer_RenderNote_QuoteRenote(t *testing.T) {
	r := newRenderer()
	remoteURI := "https://remote.example/notes/orig"
	r.SetNoteResolver(&stubNoteResolver{note: &model.Note{URI: &remoteURI}})
	idGen := newIDGen(t)
	renoteID := "renote-target"
	text := "quoting this"
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Text:       &text,
		RenoteID:   &renoteID,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(n, idGen)
	assert.Equal(t, remoteURI, out.MisskeyQuote)
	assert.Equal(t, remoteURI, out.QuoteURL)
}

func TestRenderer_RenderNote_QuoteRenote_LocalFallback(t *testing.T) {
	r := newRenderer()
	// noteResolver返すノートにURIがない → ローカルURIフォールバック
	r.SetNoteResolver(&stubNoteResolver{note: &model.Note{}})
	idGen := newIDGen(t)
	renoteID := "local-note"
	text := "quoting"
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Text:       &text,
		RenoteID:   &renoteID,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(n, idGen)
	assert.Equal(t, "https://example.com/notes/local-note", out.MisskeyQuote)
}

func TestRenderer_RenderNote_HashtagTag(t *testing.T) {
	r := newRenderer()
	r.SetHost("example.com")
	idGen := newIDGen(t)
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Visibility: model.NoteVisibilityPublic,
		Tags:       pq.StringArray{"golang", "misskey"},
	}
	out := r.RenderNote(n, idGen)
	require.Len(t, out.Tag, 2)
	ht0, ok := out.Tag[0].(Hashtag)
	require.True(t, ok)
	assert.Equal(t, "Hashtag", ht0.Type)
	assert.Equal(t, "#golang", ht0.Name)
	assert.Equal(t, "https://example.com/tags/golang", ht0.Href)
}

// stubEmojiResolver returns a fixed emoji.
type stubEmojiResolver struct {
	emojis map[string]*model.Emoji
}

func (s *stubEmojiResolver) FindByNameAndHost(name string, _ *string) (*model.Emoji, error) {
	e, ok := s.emojis[name]
	if !ok {
		return nil, errors.New("not found")
	}
	return e, nil
}

func TestRenderer_RenderNote_EmojiTag(t *testing.T) {
	r := newRenderer()
	emojiURI := "https://example.com/emojis/blobcat"
	r.SetEmojiResolver(&stubEmojiResolver{emojis: map[string]*model.Emoji{
		"blobcat": {Name: "blobcat", PublicURL: "https://example.com/files/blobcat.webp", URI: &emojiURI},
	}})
	idGen := newIDGen(t)
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Visibility: model.NoteVisibilityPublic,
		Emojis:     pq.StringArray{"blobcat", "unknown"},
	}
	out := r.RenderNote(n, idGen)
	// "unknown"は解決失敗してスキップされる
	require.Len(t, out.Tag, 1)
	et, ok := out.Tag[0].(EmojiTag)
	require.True(t, ok)
	assert.Equal(t, "Emoji", et.Type)
	assert.Equal(t, ":blobcat:", et.Name)
	assert.Equal(t, "https://example.com/files/blobcat.webp", et.Icon.URL)
	assert.Equal(t, emojiURI, et.ID)
}

func TestRenderer_RenderPerson_EmojiTag(t *testing.T) {
	r := newRenderer()
	r.SetEmojiResolver(&stubEmojiResolver{emojis: map[string]*model.Emoji{
		"verified": {Name: "verified", PublicURL: "https://example.com/files/verified.webp"},
	}})
	u := &model.User{
		ID:       "u1",
		Username: "alice",
		Emojis:   pq.StringArray{"verified"},
	}
	p := r.RenderPerson(u, nil, "PUBKEY")
	require.Len(t, p.Tag, 1)
	et, ok := p.Tag[0].(EmojiTag)
	require.True(t, ok)
	assert.Equal(t, ":verified:", et.Name)
}

func TestRenderer_RenderNote_ContextIncludesMisskey(t *testing.T) {
	r := newRenderer()
	idGen := newIDGen(t)
	n := &model.Note{
		ID:         idGen.Generate(time.Now()),
		UserID:     "author",
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(n, idGen)
	ctx, ok := out.Context.([]any)
	require.True(t, ok)
	assert.Contains(t, ctx, ContextURL)
	assert.Contains(t, ctx, SecurityContextURL)
	// MisskeyContextのmapが含まれているか確認
	var hasMk bool
	for _, c := range ctx {
		if m, ok := c.(map[string]any); ok {
			if _, exists := m["misskey"]; exists {
				hasMk = true
			}
		}
	}
	assert.True(t, hasMk, "context should include MisskeyContext map")
}

func TestRenderer_SetResolvers(t *testing.T) {
	r := newRenderer()
	// カバレッジ用: 各setter呼び出し
	r.SetFileResolver(nil)
	r.SetEmojiResolver(nil)
	r.SetNoteResolver(nil)
	r.SetHost("example.com")
	assert.Equal(t, "example.com", r.host)
}

func TestRenderer_RenderFlag(t *testing.T) {
	r := newRenderer()
	actor := &model.User{ID: "instance"}
	flag := r.RenderFlag(actor, "https://remote.example/users/alice", "spam comment")
	assert.Equal(t, "Flag", flag.Type)
	assert.Equal(t, "https://example.com/users/instance", flag.Actor)
	assert.Equal(t, "https://remote.example/users/alice", flag.Object)
	assert.Equal(t, "spam comment", flag.Content)
	// AddContext 済 (Context が設定される)
	assert.NotNil(t, flag.Context)
}

// --- Poll (Question) rendering ---

type stubPollResolver struct {
	polls map[string]*model.Poll
}

func (s *stubPollResolver) FindByNoteID(noteID string) (*model.Poll, error) {
	if p, ok := s.polls[noteID]; ok {
		return p, nil
	}
	return nil, assert.AnError
}

func TestRenderer_RenderNote_SingleChoicePoll(t *testing.T) {
	r := newRenderer()
	past := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	r.SetPollResolver(&stubPollResolver{polls: map[string]*model.Poll{
		"n1": {
			NoteID:    "n1",
			Multiple:  false,
			Choices:   []string{"A", "B", "C"},
			Votes:     []int64{10, 5, 3},
			ExpiresAt: &past,
		},
	}})
	idGen, _ := id.NewGenerator("aidx")
	note := &model.Note{
		ID: "n1", UserID: "u1", HasPoll: true,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(note, idGen)
	assert.Equal(t, "Question", out.Type)
	require.Len(t, out.OneOf, 3)
	assert.Empty(t, out.AnyOf)
	assert.Equal(t, "A", out.OneOf[0].Name)
	assert.Equal(t, 10, out.OneOf[0].Replies.TotalItems)
	assert.Equal(t, "B", out.OneOf[1].Name)
	assert.NotEmpty(t, out.EndTime)
	assert.NotEmpty(t, out.Closed, "expired poll should have closed")
}

func TestRenderer_RenderNote_MultipleChoicePoll(t *testing.T) {
	r := newRenderer()
	future := time.Now().Add(24 * time.Hour)
	r.SetPollResolver(&stubPollResolver{polls: map[string]*model.Poll{
		"n2": {
			NoteID:    "n2",
			Multiple:  true,
			Choices:   []string{"X", "Y"},
			Votes:     []int64{1, 2},
			ExpiresAt: &future,
		},
	}})
	idGen, _ := id.NewGenerator("aidx")
	note := &model.Note{
		ID: "n2", UserID: "u1", HasPoll: true,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(note, idGen)
	assert.Equal(t, "Question", out.Type)
	assert.Empty(t, out.OneOf)
	require.Len(t, out.AnyOf, 2)
	assert.Equal(t, "X", out.AnyOf[0].Name)
	assert.NotEmpty(t, out.EndTime)
	assert.Empty(t, out.Closed, "active poll should not be closed")
}

func TestRenderer_RenderNote_PollWithoutExpiry(t *testing.T) {
	r := newRenderer()
	r.SetPollResolver(&stubPollResolver{polls: map[string]*model.Poll{
		"n3": {
			NoteID:   "n3",
			Multiple: false,
			Choices:  []string{"Yes", "No"},
			Votes:    []int64{0, 0},
		},
	}})
	idGen, _ := id.NewGenerator("aidx")
	note := &model.Note{
		ID: "n3", UserID: "u1", HasPoll: true,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(note, idGen)
	assert.Equal(t, "Question", out.Type)
	require.Len(t, out.OneOf, 2)
	assert.Empty(t, out.EndTime)
	assert.Empty(t, out.Closed)
}

func TestRenderer_RenderNote_NoPollResolver(t *testing.T) {
	r := newRenderer()
	// pollResolver未設定ならHasPoll=trueでもNoteのまま
	idGen, _ := id.NewGenerator("aidx")
	note := &model.Note{
		ID: "n4", UserID: "u1", HasPoll: true,
		Visibility: model.NoteVisibilityPublic,
	}
	out := r.RenderNote(note, idGen)
	assert.Equal(t, "Note", out.Type)
	assert.Empty(t, out.OneOf)
}
