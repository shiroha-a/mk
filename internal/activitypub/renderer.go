package activitypub

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shiroha-a/mk/internal/activitypub/mfm"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// URLBuilder constructs canonical URLs for the local instance.
// 同じデータでも複数の場所から参照されるので、ヘルパとして集約しておく。
type URLBuilder struct {
	baseURL string
}

// NewURLBuilder constructs a URLBuilder rooted at baseURL (e.g. "https://example.com").
func NewURLBuilder(baseURL string) *URLBuilder {
	return &URLBuilder{baseURL: baseURL}
}

// UserURI returns the canonical actor URI for a local user.
func (b *URLBuilder) UserURI(userID string) string {
	return b.baseURL + "/users/" + userID
}

// UserInbox returns the inbox URL for a user.
func (b *URLBuilder) UserInbox(userID string) string {
	return b.UserURI(userID) + "/inbox"
}

// UserOutbox returns the outbox URL for a user.
func (b *URLBuilder) UserOutbox(userID string) string {
	return b.UserURI(userID) + "/outbox"
}

// UserFollowers returns the followers collection URL.
func (b *URLBuilder) UserFollowers(userID string) string {
	return b.UserURI(userID) + "/followers"
}

// UserFollowing returns the following collection URL.
func (b *URLBuilder) UserFollowing(userID string) string {
	return b.UserURI(userID) + "/following"
}

// UserKeyURI returns the public key fragment URI used in HTTP signatures.
func (b *URLBuilder) UserKeyURI(userID string) string {
	return b.UserURI(userID) + "#main-key"
}

// SharedInbox returns the shared inbox URL.
func (b *URLBuilder) SharedInbox() string {
	return b.baseURL + "/inbox"
}

// NoteURI returns the canonical note URI.
func (b *URLBuilder) NoteURI(noteID string) string {
	return b.baseURL + "/notes/" + noteID
}

// CreateActivityURI returns the URI of the Create activity wrapping a note.
func (b *URLBuilder) CreateActivityURI(noteID string) string {
	return b.NoteURI(noteID) + "/activity"
}

// VoteActivityURI returns a stable URI for the AP `Note` representing a
// poll vote. URI must be unique per (voter, target) pair to avoid
// collisions when the same user votes on multiple polls. Misskey TS の
// upstream wire format に合わせて fragment 形式 (`#votes/<targetNoteID>`)
// を採用する (#690)。fragment 形式なら remote 側が既存 voter URL の
// 同一性で actor を識別できる。
func (b *URLBuilder) VoteActivityURI(voterID, targetNoteID string) string {
	return b.UserURI(voterID) + "#votes/" + targetNoteID
}

// FollowURI returns the URI for a Follow activity.
// followeeIDが完全なURIの場合はハッシュ化してパスセグメントに埋め込む。
func (b *URLBuilder) FollowURI(followerID, followeeID string) string {
	id := followeeID
	if strings.Contains(followeeID, "/") {
		h := sha256.Sum256([]byte(followeeID))
		id = hex.EncodeToString(h[:16])
	}
	return b.baseURL + "/follows/" + followerID + "/" + id
}

// FollowRelayURI returns the URI for a Follow-Relay activity.
// Path 形式は upstream と一致させる (`/activities/follow-relay/{relayID}`):
// Accept / Reject inbox ハンドラがこの正規表現で relay を特定するため、
// 変更するときは processor 側の regex も一緒に更新すること。
func (b *URLBuilder) FollowRelayURI(relayID string) string {
	return b.baseURL + "/activities/follow-relay/" + relayID
}

// MoveURI returns the URI for a Move activity from srcID to dstURI.
// 本家は `${baseURL}/moves/${src.id}/${dst.id}` を使うが、dstURI はリモートで
// 内部 ID を持たないため、URI を SHA-256 で 16 bytes にハッシュ化して path
// に使う。一意性とショート URL 表現を両立する。
func (b *URLBuilder) MoveURI(srcID, dstURI string) string {
	h := sha256.Sum256([]byte(dstURI))
	return b.baseURL + "/moves/" + srcID + "/" + hex.EncodeToString(h[:16])
}

// ChatMessageURI returns the canonical URI for a chat message.
func (b *URLBuilder) ChatMessageURI(messageID string) string {
	return b.baseURL + "/chat-messages/" + messageID
}

// ErrMentionUserNotFound is returned by MentionResolver implementations
// when the user does not exist (typically deleted or the ID is stale).
// Caller (renderer) は本 sentinel を skip 扱いにし、その他 error は DB
// 障害として log 出力する (#572 item 2)。
var ErrMentionUserNotFound = errors.New("mention user not found")

// MentionResolver resolves a note.Mentions entry (user ID) into the data
// required to build an AS Mention tag. 実装は server/router.go 側で
// UserRepository を wrap する形で提供される。
//
// 戻り値:
//   - 成功: name, uri, nil
//   - ユーザー未存在: "", "", ErrMentionUserNotFound (skip)
//   - DB 障害等: "", "", <DB error> (skip + log; 将来 retry 可能)
//
// uri はローカルユーザーなら urls.UserURI(user.ID)、リモートユーザーなら
// user.URI を返す。name は Misskey 互換で "@username" / "@username@host"。
type MentionResolver interface {
	ResolveMention(userID string) (name, uri string, err error)
}

// FileResolver loads drive files by ID for rendering Note attachments.
type FileResolver interface {
	FindByIDs(ids []string) ([]*model.DriveFile, error)
}

// EmojiResolver resolves custom emoji names to their model data.
type EmojiResolver interface {
	FindByNameAndHost(name string, host *string) (*model.Emoji, error)
}

// NoteResolver resolves a note by ID (for quote renote URI lookup).
type NoteResolver interface {
	FindByID(id string) (*model.Note, error)
}

// PollResolver loads a poll by its parent note ID. Used by RenderNote to
// populate Question fields (oneOf/anyOf/endTime/closed).
type PollResolver interface {
	FindByNoteID(noteID string) (*model.Poll, error)
}

// actorTypeForUser returns the AP actor `type` to emit for a local user.
// 現状: IsBot=true なら Service、それ以外は Person。Application (system actor)
// 出力は将来のシステムアカウント機能で別経路を追加する想定。
func actorTypeForUser(u *model.User) string {
	if u.IsBot {
		return "Service"
	}
	return "Person"
}

// Renderer converts model entities into AS objects.
type Renderer struct {
	urls            *URLBuilder
	mentionResolver MentionResolver
	fileResolver    FileResolver
	emojiResolver   EmojiResolver
	noteResolver    NoteResolver
	pollResolver    PollResolver
	host            string // ローカルホスト名 (MFM変換用)
}

// NewRenderer constructs a Renderer.
func NewRenderer(urls *URLBuilder) *Renderer {
	return &Renderer{urls: urls}
}

// SetMentionResolver attaches a MentionResolver used by RenderNote to populate
// the `tag` field and additional `to` audience entries. nil で無効化できる。
func (r *Renderer) SetMentionResolver(mr MentionResolver) {
	r.mentionResolver = mr
}

// SetFileResolver attaches a FileResolver for Note attachment rendering.
func (r *Renderer) SetFileResolver(fr FileResolver) {
	r.fileResolver = fr
}

// SetPollResolver attaches a PollResolver for Question (poll) rendering.
func (r *Renderer) SetPollResolver(pr PollResolver) {
	r.pollResolver = pr
}

// SetEmojiResolver attaches an EmojiResolver for custom emoji tags.
func (r *Renderer) SetEmojiResolver(er EmojiResolver) {
	r.emojiResolver = er
}

// SetNoteResolver attaches a NoteResolver for quote renote URI lookup.
func (r *Renderer) SetNoteResolver(nr NoteResolver) {
	r.noteResolver = nr
}

// SetHost sets the local hostname for MFM→HTML conversion.
func (r *Renderer) SetHost(host string) {
	r.host = host
}

// RenderPerson packs a local user into a Person actor object.
// profile は nil でもよい (その場合 summary 等のフィールドは省略される)。
// publicKeyPEM はリポジトリから取得した RSA 公開鍵 PEM 文字列。
// ed25519PublicKey が non-nil の場合は FEP-521a Multikey 形式で
// `assertionMethod[]` に追加 expose する (#1067 / #1069)。Encode 失敗時は
// silently skip して RSA のみ出力する fail-soft 動作。
func (r *Renderer) RenderPerson(u *model.User, profile *model.UserProfile, publicKeyPEM string, ed25519PublicKey ed25519.PublicKey) *Person {
	uri := r.urls.UserURI(u.ID)

	actorType := actorTypeForUser(u)

	p := &Person{
		Object: Object{
			ID:   uri,
			Type: actorType,
			Name: stringValue(u.Name),
		},
		Inbox:             r.urls.UserInbox(u.ID),
		Outbox:            r.urls.UserOutbox(u.ID),
		Followers:         r.urls.UserFollowers(u.ID),
		Following:         r.urls.UserFollowing(u.ID),
		PreferredUsername: u.Username,
		URL:               uri,
		Endpoints:         Endpoints{SharedInbox: r.urls.SharedInbox()},
		PublicKey: PublicKey{
			ID:           r.urls.UserKeyURI(u.ID),
			Owner:        uri,
			PublicKeyPEM: publicKeyPEM,
		},
		ManuallyApproves: u.IsLocked,
		Discoverable:     u.IsExplorable,
		IsCat:            u.IsCat,
	}
	// Ed25519 鍵を持つ user に対しては assertionMethod に Multikey として
	// 追加 expose する。Fedibird など FEP-521a 対応サーバーが Ed25519 鍵で
	// HTTP Signature を verify できるようにするための拡張 (#1067 / #1069)。
	// keyId fragment は `#ed25519-key` を採用 (Fedibird / Mastodon glitch-soc
	// と同じ慣習)。
	if ed25519PublicKey != nil {
		if mb, err := EncodeEd25519Multikey(ed25519PublicKey); err == nil {
			p.AssertionMethod = []Multikey{{
				ID:                 uri + "#ed25519-key",
				Type:               MultikeyType,
				Controller:         uri,
				PublicKeyMultibase: mb,
			}}
		}
	}
	if u.AvatarURL != nil {
		p.Icon = &Image{Type: "Image", URL: *u.AvatarURL}
	}
	if u.BannerURL != nil {
		p.Image = &Image{Type: "Image", URL: *u.BannerURL}
	}
	if u.MovedToURI != nil && *u.MovedToURI != "" {
		p.MovedTo = *u.MovedToURI
	}
	if u.AlsoKnownAs != nil && *u.AlsoKnownAs != "" {
		p.AlsoKnownAs = APStringList(strings.Split(*u.AlsoKnownAs, ","))
	}
	if u.Featured != nil && *u.Featured != "" {
		p.Featured = *u.Featured
	}
	p.MisskeyRequireSigninToViewContents = u.RequireSigninToViewContents
	p.MisskeyMakeNotesFollowersOnlyBefore = u.MakeNotesFollowersOnlyBefore
	p.MisskeyMakeNotesHiddenBefore = u.MakeNotesHiddenBefore
	// `_misskey_canChat` は CherryPick 互換の chat 連合 capability flag。
	// chatScope == "none" 以外なら true (everyone/followers/following/mutual)
	// と公開する。granular な scope は受信側で local 強制されるので AP には
	// boolean だけ流す (#692)。
	canChat := u.ChatScope != "none"
	p.MisskeyCanChat = &canChat

	// profile から追加フィールドを埋める
	if profile != nil {
		r.enrichPersonFromProfile(p, profile)
	}

	// ユーザーのカスタム絵文字をEmoji tagとして追加
	r.addEmojiTags(&p.Tag, u.Emojis, nil)

	AddContext(p)
	return p
}

// enrichPersonFromProfile fills profile-derived fields into Person.
func (r *Renderer) enrichPersonFromProfile(p *Person, profile *model.UserProfile) {
	if profile.Description != nil && *profile.Description != "" {
		desc := *profile.Description
		// MFM → HTML
		nodes := mfm.Parse(desc)
		p.Summary = mfm.ToHTML(nodes, r.host)
		p.MisskeySummary = desc
	}
	if profile.Birthday != nil && *profile.Birthday != "" {
		p.VcardBday = *profile.Birthday
	}
	if profile.Location != nil && *profile.Location != "" {
		p.VcardAddress = *profile.Location
	}
	if profile.FollowedMessage != nil && *profile.FollowedMessage != "" {
		p.MisskeyFollowedMessage = *profile.FollowedMessage
	}

	// profile.Fields → PropertyValue attachment
	if len(profile.Fields) > 0 {
		var fields []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(profile.Fields, &fields); err == nil && len(fields) > 0 {
			for _, f := range fields {
				p.Attachment = append(p.Attachment, PropertyValue{
					Type:  "PropertyValue",
					Name:  f.Name,
					Value: f.Value,
				})
			}
		}
	}
}

// RenderNote packs a local note into a Note object.
func (r *Renderer) RenderNote(n *model.Note, idGen id.Generator) *Note {
	text := stringValue(n.Text)

	// MFM → HTML変換
	var htmlContent string
	var nodes []*mfm.Node
	if text != "" {
		nodes = mfm.Parse(text)
		htmlContent = mfm.ToHTML(nodes, r.host)
	}

	out := &Note{
		Object: Object{
			ID:   r.urls.NoteURI(n.ID),
			Type: "Note",
		},
		AttributedTo: r.urls.UserURI(n.UserID),
		Content:      htmlContent,
		Published:    parseNoteTime(n.ID, idGen),
		Sensitive:    n.CW != nil && *n.CW != "",
	}

	// _misskey_content / source: 標準ノードのみなら省略 (TS版 noMisskeyContent)
	if text != "" && !mfm.IsSimple(nodes) {
		out.MisskeyContent = text
		out.Source = &Source{
			Content:   text,
			MediaType: "text/x.misskeymarkdown",
		}
	}

	if n.CW != nil {
		out.Summary = *n.CW
	}
	if n.ReplyID != nil {
		// リモート note への reply の場合、InReplyTo は相手側 (note.URI) を
		// 指すべき。mk-go の local URL (`https://<self>/notes/<id>`) に
		// してしまうと連合先が `inReplyTo` を解決できず reply chain が切れる
		// (drop-in #369 で発覚)。
		out.InReplyTo = r.resolveNoteURI(*n.ReplyID)
	}

	// Quote renote: text付きrenoteは_misskey_quote + quoteUrlを付ける
	if n.RenoteID != nil && text != "" {
		quoteURI := r.resolveNoteURI(*n.RenoteID)
		if quoteURI != "" {
			out.MisskeyQuote = quoteURI
			out.QuoteURL = quoteURI
		}
	}

	// 添付ファイル
	r.addAttachments(out, n)

	// Hashtag tags
	for _, tag := range n.Tags {
		out.Tag = append(out.Tag, Hashtag{
			Type: "Hashtag",
			Href: "https://" + r.host + "/tags/" + tag,
			Name: "#" + tag,
		})
	}

	// Custom emoji tags
	r.addEmojiTags(&out.Tag, n.Emojis, nil)

	to, cc := r.addressing(n)

	// Resolve mentions into Tag entries and add mentioned user URIs to `to`
	// so receiving instances (特に Misskey) が mention 通知を作成できる。
	// mentionResolver 未設定なら tag は付けない (テスト互換)。
	if r.mentionResolver != nil && len(n.Mentions) > 0 {
		seenTo := make(map[string]struct{}, len(to))
		for _, v := range to {
			seenTo[v] = struct{}{}
		}
		for _, uid := range n.Mentions {
			name, uri, err := r.mentionResolver.ResolveMention(uid)
			switch {
			case err == nil:
				// 成功
			case errors.Is(err, ErrMentionUserNotFound):
				// ユーザー削除済 / ID 不整合 — silent skip
				continue
			default:
				// DB 障害等 — log を残して skip。retry 戦略は caller (deliver
				// queue) 側に委ねる。
				slog.Warn("renderer: mention resolution failed",
					"userId", uid, "noteId", n.ID, "err", err)
				continue
			}
			if uri == "" {
				continue
			}
			out.Tag = append(out.Tag, NewMention(uri, name))
			if _, dup := seenTo[uri]; !dup {
				to = append(to, uri)
				seenTo[uri] = struct{}{}
			}
		}
	}

	out.To = to
	out.CC = cc

	// Poll (Question) の asPoll レンダリング。HasPoll が true かつ pollResolver
	// が設定されている場合、Note type を "Question" に変更し、選択肢を oneOf/anyOf
	// として出力する。upstream renderNote の asPoll 部分と同等。
	if n.HasPoll && r.pollResolver != nil {
		if poll, err := r.pollResolver.FindByNoteID(n.ID); err == nil {
			r.applyPoll(out, poll)
		}
	}

	AddContext(out)
	return out
}

// applyPoll converts a Note into a Question by changing the type and populating
// oneOf (single choice) or anyOf (multiple choice) + endTime/closed fields.
func (r *Renderer) applyPoll(out *Note, poll *model.Poll) {
	out.Type = "Question"
	choices := make([]QuestionChoice, len(poll.Choices))
	for i, c := range poll.Choices {
		var count int
		if i < len(poll.Votes) {
			count = int(poll.Votes[i])
		}
		choices[i] = QuestionChoice{
			Type: "Note",
			Name: c,
			Replies: &QuestionChoiceReplies{
				Type:       "Collection",
				TotalItems: count,
			},
		}
	}
	if poll.Multiple {
		out.AnyOf = choices
	} else {
		out.OneOf = choices
	}
	if poll.ExpiresAt != nil {
		ts := poll.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
		out.EndTime = ts
		// 期限切れの場合は closed もセット
		if poll.ExpiresAt.Before(time.Now()) {
			out.Closed = ts
		}
	}
}

// addAttachments loads drive files and adds Document entries to the note.
func (r *Renderer) addAttachments(out *Note, n *model.Note) {
	if r.fileResolver == nil || len(n.FileIDs) == 0 {
		return
	}
	files, err := r.fileResolver.FindByIDs(n.FileIDs)
	if err != nil {
		slog.Warn("renderer: failed to load drive files", "noteId", n.ID, "err", err)
		return
	}
	for _, f := range files {
		doc := Document{
			Type:      "Document",
			MediaType: f.Type,
			URL:       f.URL,
			Name:      stringValue(f.Comment),
			Sensitive: f.IsSensitive,
		}
		out.Attachment = append(out.Attachment, doc)
		if f.IsSensitive {
			out.Sensitive = true
		}
	}
}

// resolveNoteURI returns the canonical URI for a note (remote URI or local).
func (r *Renderer) resolveNoteURI(noteID string) string {
	if r.noteResolver != nil {
		if note, err := r.noteResolver.FindByID(noteID); err == nil {
			if note.URI != nil && *note.URI != "" {
				return *note.URI
			}
		}
	}
	return r.urls.NoteURI(noteID)
}

// addEmojiTags resolves custom emoji names and appends EmojiTag entries.
func (r *Renderer) addEmojiTags(tags *[]any, emojiNames []string, host *string) {
	if r.emojiResolver == nil || len(emojiNames) == 0 {
		return
	}
	for _, name := range emojiNames {
		emoji, err := r.emojiResolver.FindByNameAndHost(name, host)
		if err != nil {
			continue
		}
		tag := EmojiTag{
			Type: "Emoji",
			Name: ":" + emoji.Name + ":",
			Icon: Image{Type: "Image", URL: emoji.PublicURL},
		}
		if emoji.URI != nil {
			tag.ID = *emoji.URI
		}
		// #731: upstream Misskey TS の renderEmoji と同じく `_misskey_license`
		// を出力する。受信側 mk-go / Misskey TS は wrapper を見て license を
		// 取り込む (extractEmojiTags)。emoji.License が nil でも、wrapper を
		// 出力して FreeText: null を明示することで「license 未設定」を
		// 連合先に伝える (upstream renderer の挙動と一致)。
		tag.License = &MisskeyLicense{FreeText: emoji.License}
		*tags = append(*tags, tag)
	}
}

// RenderQuestionUpdate builds the AP `Update(Question)` activity broadcast to
// followers when a local poll receives a vote (#690)。Misskey TS の
// PollService.deliverQuestionUpdate と同等で、配信先 instance が remote
// follower 視点で count を最新化できる。RenderNote が既に Question 互換の
// asPoll 出力をするので、それを wrap するだけ。
//
// Activity ID は `<noteURI>#updates/<RFC3339Nano>` で構成し、同一秒内に
// 連続投票が発生した場合の Activity ID 衝突を防ぐ。受信側 instance は
// Activity ID で idempotency dedup するため、衝突するとうしろ側 Update が
// drop されて count が古いまま固定される (#690 review)。
func (r *Renderer) RenderQuestionUpdate(n *model.Note, idGen id.Generator) *Update {
	question := r.RenderNote(n, idGen)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	u := &Update{
		Activity: Activity{
			Object: Object{
				ID:   r.urls.NoteURI(n.ID) + "#updates/" + now,
				Type: "Update",
			},
			Actor:     r.urls.UserURI(n.UserID),
			Published: now,
			To:        question.To,
			CC:        question.CC,
		},
		Object: question,
	}
	AddContext(u)
	return u
}

// RenderVote builds the AP `Create(Note)` activity used to deliver a poll
// vote to the remote poll author's inbox (#690)。Misskey TS の vote wire
// format に合わせて Note は `name = <choice>` + `inReplyTo = <poll URI>` +
// 空 `content` で構成する。target は voted-on poll note (remote)。targetURI
// はリモート側の note.URI を使う (local URL で render すると相手が解決でき
// ない)。authorURI は target.UserID から URLBuilder 経由で組み立てる。
func (r *Renderer) RenderVote(voter *model.User, target *model.Note, targetURI, authorURI, choiceName string) *Create {
	now := time.Now().UTC().Format(time.RFC3339)
	noteID := r.urls.VoteActivityURI(voter.ID, target.ID)
	note := &Note{
		Object: Object{
			ID:   noteID,
			Type: "Note",
		},
		AttributedTo: r.urls.UserURI(voter.ID),
		Content:      "",
		Published:    now,
		To:           []string{authorURI},
		InReplyTo:    targetURI,
		Name:         choiceName,
	}
	c := &Create{
		Activity: Activity{
			Object: Object{
				ID:   r.urls.CreateActivityURI(noteID),
				Type: "Create",
			},
			Actor:     r.urls.UserURI(voter.ID),
			Published: now,
			To:        []string{authorURI},
		},
		Object: note,
	}
	AddContext(c)
	return c
}

// RenderCreate wraps a Note into a Create activity addressed to the same audience.
func (r *Renderer) RenderCreate(n *model.Note, idGen id.Generator) *Create {
	note := r.RenderNote(n, idGen)
	c := &Create{
		Activity: Activity{
			Object: Object{
				ID:   r.urls.CreateActivityURI(n.ID),
				Type: "Create",
			},
			Actor:     r.urls.UserURI(n.UserID),
			Published: note.Published,
			To:        note.To,
			CC:        note.CC,
		},
		Object: note,
	}
	AddContext(c)
	return c
}

// RenderFollow returns a Follow activity from follower → followeeURI.
func (r *Renderer) RenderFollow(followerID, followeeURI string) *Follow {
	f := &Follow{
		Activity: Activity{
			Object: Object{
				ID:   r.urls.FollowURI(followerID, followeeURI),
				Type: "Follow",
			},
			Actor: r.urls.UserURI(followerID),
		},
		Object: followeeURI,
	}
	AddContext(f)
	return f
}

// RenderFollowRelay returns the special-purpose Follow activity used to
// subscribe the instance's relay system actor to a relay endpoint.
//
// Mirrors upstream ApRendererService.renderFollowRelay:
//   - id: `${baseURL}/activities/follow-relay/${relayID}`
//   - actor: relay actor's local URI
//   - object: the ActivityStreams Public IRI (= subscribe to everything)
//
// The id format is load-bearing — inbox Accept/Reject activities are
// matched against `/activities/follow-relay/([\w-]+)$` to look the
// relay up when marking its status.
func (r *Renderer) RenderFollowRelay(relayID string, relayActorID string) *Follow {
	f := &Follow{
		Activity: Activity{
			Object: Object{
				ID:   r.urls.FollowRelayURI(relayID),
				Type: "Follow",
			},
			Actor: r.urls.UserURI(relayActorID),
		},
		Object: Public,
	}
	AddContext(f)
	return f
}

// RenderAccept wraps an inner activity in an Accept.
//
// id は Misskey TS `addContext` 互換で `{baseURL}/{UUID}` 形式で必ず入れる。
// id なしの Accept は受信側 (Misskey / CherryPick) の InboxProcessorService
// で "skip: activity id is not a string" として弾かれるため、フォロー承認
// フローが成立しない。
func (r *Renderer) RenderAccept(actorID string, inner any) *Accept {
	a := &Accept{
		Activity: Activity{
			Object: Object{
				ID:   r.urls.baseURL + "/" + uuid.NewString(),
				Type: "Accept",
			},
			Actor: r.urls.UserURI(actorID),
		},
		Object: inner,
	}
	AddContext(a)
	return a
}

// RenderReject returns a Reject activity wrapping the given inner object
// (typically a Follow). Used for:
//   - local user rejecting an inbound Follow request
//   - local user removing an existing remote follower (invalidate)
//
// 本家 UserFollowingService と同じく actor=rejecting user、object=original Follow。
func (r *Renderer) RenderReject(actorID string, inner any) *Reject {
	a := &Reject{
		Activity: Activity{
			Object: Object{
				ID:   r.urls.baseURL + "/" + uuid.NewString(),
				Type: "Reject",
			},
			Actor: r.urls.UserURI(actorID),
		},
		Object: inner,
	}
	AddContext(a)
	return a
}

// RenderLike returns a Like activity for the given reaction.
// targetURI は対象ノートの canonical URI (リモートなら note.URI、ローカルなら
// urls.NoteURI(note.ID))。
//
// reaction が custom emoji 形式 (`:name:` / `:name@.:` / `:name@host:`) の
// 場合は addEmojiTags で Like.Tag に Emoji オブジェクトを 1 件詰める。
// 連合相手側 (mk-go 受信側 / Misskey TS / CherryPick) は extractEmojiTags
// で Tag を拾って icon URL を local emoji table に upsert する設計なので、
// ここで Tag を出さないと remote 側で custom emoji 解決できず、heart
// fallback / 表示なしになる (#634)。Unicode 絵文字や fallback heart は
// `:` で囲まれていないので Tag 不要。
func (r *Renderer) RenderLike(reactor *model.User, targetURI string, reaction string, likeID string) *Like {
	l := &Like{
		Activity: Activity{
			Object: Object{
				ID:   likeID,
				Type: "Like",
			},
			Actor: r.urls.UserURI(reactor.ID),
		},
		Object:          targetURI,
		Content:         reaction,
		MisskeyReaction: reaction,
	}
	if name, host, ok := parseCustomEmojiReactionForTag(reaction); ok {
		var hostPtr *string
		if host != "" {
			h := host
			hostPtr = &h
		}
		r.addEmojiTags(&l.Tag, []string{name}, hostPtr)
	}
	AddContext(l)
	return l
}

// parseCustomEmojiReactionForTag extracts (name, host) from a canonical
// reaction string for emoji Tag emission. Returns ok=false for unicode
// (no surrounding colons) and fallback reactions. Local canonical "@."
// suffix is normalised to host="".
//
// activitypub 層に entity.parseCustomEmojiReaction と等価のミニ実装を持
// たせる: layer 規則上 activitypub → entity の依存は禁止 (entity →
// activitypub.Renderer の逆向き依存は許容) なので、両側で別々に持つ。
func parseCustomEmojiReactionForTag(s string) (name, host string, ok bool) {
	if len(s) < 3 || s[0] != ':' || s[len(s)-1] != ':' {
		return "", "", false
	}
	inner := s[1 : len(s)-1]
	if at := strings.Index(inner, "@"); at >= 0 {
		host = inner[at+1:]
		if host == "." {
			host = ""
		}
		return inner[:at], host, true
	}
	return inner, "", true
}

// RenderUndoLike wraps a previously emitted Like in an Undo activity.
func (r *Renderer) RenderUndoLike(reactor *model.User, like *Like) *Undo {
	u := &Undo{
		Activity: Activity{
			Object: Object{
				ID:   like.ID + "/undo",
				Type: "Undo",
			},
			Actor: r.urls.UserURI(reactor.ID),
		},
		Object: like,
	}
	AddContext(u)
	return u
}

// RenderAnnounce returns an Announce activity for a pure renote.
// targetURI は元ノートの URI (リモート / ローカル)。renoteID は renote 自身の ID。
func (r *Renderer) RenderAnnounce(renoter *model.User, renoteID string, targetURI string) *Announce {
	a := &Announce{
		Activity: Activity{
			Object: Object{
				ID:   r.urls.NoteURI(renoteID) + "/activity",
				Type: "Announce",
			},
			Actor: r.urls.UserURI(renoter.ID),
			To:    []string{Public},
			CC:    []string{r.urls.UserFollowers(renoter.ID)},
		},
		Object: targetURI,
	}
	AddContext(a)
	return a
}

// RenderUndoAnnounce wraps a previously emitted Announce in an Undo activity.
func (r *Renderer) RenderUndoAnnounce(renoter *model.User, announce *Announce) *Undo {
	u := &Undo{
		Activity: Activity{
			Object: Object{
				ID:   announce.ID + "/undo",
				Type: "Undo",
			},
			Actor: r.urls.UserURI(renoter.ID),
		},
		Object: announce,
	}
	AddContext(u)
	return u
}

// RenderDelete returns a Delete activity targeting the note URI. Object is a
// Tombstone so receivers can match it to a previously known note.
func (r *Renderer) RenderDelete(author *model.User, noteURI string) *Delete {
	d := &Delete{
		Activity: Activity{
			Object: Object{
				ID:   noteURI + "#Delete",
				Type: "Delete",
			},
			Actor: r.urls.UserURI(author.ID),
			To:    []string{Public},
		},
		Object: Tombstone{
			ID:   noteURI,
			Type: "Tombstone",
		},
	}
	AddContext(d)
	return d
}

// RenderFlag returns a Flag activity that forwards an abuse report to the
// origin instance of a remote user. Mirrors upstream ApRendererService
// renderFlag — the actor is the local instance's system account (to
// anonymize the reporter), object is the target user's canonical URI, and
// content is the moderator-visible comment.
func (r *Renderer) RenderFlag(actor *model.User, targetURI, content string) *Flag {
	f := &Flag{
		Activity: Activity{
			Object: Object{Type: "Flag"},
			Actor:  r.urls.UserURI(actor.ID),
		},
		Object:  targetURI,
		Content: content,
	}
	AddContext(f)
	return f
}

// RenderMove returns a Move activity announcing that src has migrated to
// dstURI. Matches upstream ApRendererService.renderMove: actor == object ==
// srcURI, target == dstURI. ID is "{baseURL}/moves/{srcID}/{dstID}" where
// dstID is derived from dstURI (final path segment) so that receiving
// servers see a deterministic activity id per (src, dst) pair.
//
// To は follower collection URI を指す。DeliverToFollowers で各 follower の
// inbox へ直接配送するため To だけでは配送経路は決まらないが、受信側の一部
// AP 実装は to/cc を見て visibility を判定するため (Announce / Delete と同様)
// 明示的に addressing を付ける。
func (r *Renderer) RenderMove(src *model.User, dstURI string) *Move {
	srcURI := r.urls.UserURI(src.ID)
	m := &Move{
		Activity: Activity{
			Object: Object{
				ID:   r.urls.MoveURI(src.ID, dstURI),
				Type: "Move",
			},
			Actor: srcURI,
			To:    []string{r.urls.UserFollowers(src.ID)},
		},
		Object: srcURI,
		Target: dstURI,
	}
	AddContext(m)
	return m
}

// RenderChatMessage returns a CherryPick-compatible Create activity wrapping
// a Note flagged with `_misskey_talk: true`. ApRendererService.renderChatMessage
// と同じ wire format を出すことで CherryPick / レガシー Misskey との 1-on-1
// DM 連合互換性を確保する (#692)。
//
// 受け手側 (cherrypick の ApInboxService) は Note.`_misskey_talk` flag を見て
// chat_messages テーブルに保存する。chat 専用 type ではなく標準 Note を
// 使う設計なので、未対応の受信実装でも単なる note の Create として黙殺
// (致命的なエラーは起きない) のが利点。
func (r *Renderer) RenderChatMessage(msg *model.ChatMessage, senderURI, recipientURI string, published string) *Create {
	noteURI := r.urls.ChatMessageURI(msg.ID)
	note := &Note{
		Object: Object{
			ID:   noteURI,
			Type: "Note",
		},
		AttributedTo: senderURI,
		Published:    published,
		To:           []string{recipientURI},
		MisskeyTalk:  true,
	}
	if msg.Text != nil {
		note.Content = *msg.Text
	}
	c := &Create{
		Activity: Activity{
			Object: Object{
				// Create activity の id は Note URI に "/activity" を付与した
				// 派生 URI とする (cherrypick も同様に renderActivity 系で
				// Create id を Note id から派生させる慣習)。
				ID:   noteURI + "/activity",
				Type: "Create",
			},
			Actor:     senderURI,
			Published: published,
			To:        []string{recipientURI},
		},
		Object: note,
	}
	AddContext(c)
	return c
}

// addressing computes to/cc lists for a note based on visibility.
func (r *Renderer) addressing(n *model.Note) (to []string, cc []string) {
	switch n.Visibility {
	case model.NoteVisibilityPublic:
		to = []string{Public}
		cc = []string{r.urls.UserFollowers(n.UserID)}
	case model.NoteVisibilityHome:
		to = []string{r.urls.UserFollowers(n.UserID)}
		cc = []string{Public}
	case model.NoteVisibilityFollowers:
		to = []string{r.urls.UserFollowers(n.UserID)}
	case model.NoteVisibilitySpecified:
		to = make([]string, 0, len(n.VisibleUserIDs))
		for _, uid := range n.VisibleUserIDs {
			to = append(to, r.urls.UserURI(uid))
		}
	}
	return to, cc
}

func stringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func parseNoteTime(noteID string, idGen id.Generator) string {
	t, err := idGen.ParseTime(noteID)
	if err != nil {
		slog.Warn("renderer: failed to parse note ID for timestamp, using time.Now()", "noteId", noteID, "err", err)
		return time.Now().UTC().Format(time.RFC3339)
	}
	return t.UTC().Format(time.RFC3339)
}
