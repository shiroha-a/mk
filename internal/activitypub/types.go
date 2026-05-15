// Package activitypub provides ActivityStreams types and the helpers for
// rendering local model entities into AP-compatible JSON-LD documents.
package activitypub

import (
	"encoding/json"
	"slices"
)

// APStringList accepts JSON values that may be either a single string or a
// []string, and exposes them uniformly as []string. ActivityPub spec で
// 同一 field を string / array 両方の形で送ってくる実装がいる (例: alsoKnownAs
// は upstream Misskey #17275 で array/string 両対応化済)。受信側は両形式を
// 受け、JSON 送出時は []string と同じ array shape で emit する。
type APStringList []string

// UnmarshalJSON decodes either a JSON array of strings or a single string.
// null / 空 input は nil slice として解釈する。
func (l *APStringList) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*l = nil
		return nil
	}
	// 先頭の whitespace を skip し、最初の non-space 文字が '[' なら array、
	// それ以外なら single string として fall back。
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			var arr []string
			if err := json.Unmarshal(data, &arr); err != nil {
				return err
			}
			*l = arr
			return nil
		}
		break
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*l = []string{s}
	return nil
}

// ContextURL is the standard ActivityStreams 2.0 JSON-LD context.
const ContextURL = "https://www.w3.org/ns/activitystreams"

// SecurityContextURL is the W3C security vocabulary used for HTTP Signatures.
const SecurityContextURL = "https://w3id.org/security/v1"

// MultikeyContextURL is the W3C Multikey vocabulary (FEP-521a) used to expose
// Ed25519 public keys via Person.assertionMethod[]. Fedibird / Mastodon
// glitch-soc などが解釈可能。
const MultikeyContextURL = "https://w3id.org/security/multikey/v1"

// DataIntegrityContextURL is the W3C VC Data Integrity vocabulary that defines
// the `assertionMethod` term used to attach Multikey entries to actors.
const DataIntegrityContextURL = "https://w3id.org/security/data-integrity/v1"

// Public is the magic IRI used to denote a publicly addressable activity.
const Public = "https://www.w3.org/ns/activitystreams#Public"

// ValidActorTypes lists the AP object types acceptable as an Actor.
// Mirrors Misskey's `validActor` array in core/activitypub/type.ts so that
// inbound documents of any other Object type are rejected at parse time.
var ValidActorTypes = []string{
	"Person", "Service", "Group", "Organization", "Application",
}

// IsValidActorType reports whether t is one of the recognized actor types.
// Empty strings are rejected.
func IsValidActorType(t string) bool {
	return slices.Contains(ValidActorTypes, t)
}

// IsBotActorType reports whether the given actor type maps to an automated
// (non-human) account. Misskey treats both Service and Application as bots.
func IsBotActorType(t string) bool {
	return t == "Service" || t == "Application"
}

// MimeType is the canonical Content-Type for ActivityPub objects.
const MimeType = `application/activity+json`

// LDMimeType is the more specific JSON-LD content type with profile parameter.
const LDMimeType = `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`

// Object is the base type for any AS object. 拡張するときは embed する。
type Object struct {
	Context any    `json:"@context,omitempty"`
	ID      string `json:"id,omitempty"`
	Type    string `json:"type,omitempty"`
	Name    string `json:"name,omitempty"`
}

// PublicKey is the embedded JSON-LD object describing a user's signing key.
type PublicKey struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	PublicKeyPEM string `json:"publicKeyPem"`
}

// Multikey represents a single FEP-521a Multikey entry expose-d via
// Person.assertionMethod[]. Used by mk-go to publish Ed25519 public keys
// alongside the legacy RSA-only `publicKey` field, so capability-aware
// receivers can verify HTTP Signatures with Ed25519 if the local user owns
// an Ed25519 keypair.
//
// PublicKeyMultibase encodes the raw key with the standard "z" + base58btc +
// multicodec prefix format (see internal/activitypub/multikey.go).
type Multikey struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Controller         string `json:"controller"`
	PublicKeyMultibase string `json:"publicKeyMultibase"`
}

// MultikeyType is the canonical `type` value for a Multikey entry.
const MultikeyType = "Multikey"

// Endpoints holds the endpoints sub-object for an Actor.
type Endpoints struct {
	SharedInbox string `json:"sharedInbox,omitempty"`
}

// Image is a generic ActivityStreams Image (used for icon/image fields).
type Image struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// Person represents a user actor.
type Person struct {
	Object
	Inbox                               string    `json:"inbox"`
	Outbox                              string    `json:"outbox"`
	Followers                           string    `json:"followers"`
	Following                           string    `json:"following"`
	PreferredUsername                   string    `json:"preferredUsername"`
	Summary                             string    `json:"summary,omitempty"`
	URL                                 string    `json:"url,omitempty"`
	Endpoints                           Endpoints `json:"endpoints,omitzero"`
	PublicKey                           PublicKey `json:"publicKey"`
	Icon                                *Image    `json:"icon,omitempty"`
	Image                               *Image    `json:"image,omitempty"`
	Attachment                          []any     `json:"attachment,omitempty"`
	Tag                                 []any     `json:"tag,omitempty"`
	Featured                            string    `json:"featured,omitempty"`
	ManuallyApproves                    bool      `json:"manuallyApprovesFollowers,omitempty"`
	Discoverable                        bool      `json:"discoverable,omitempty"`
	IsCat                               bool      `json:"isCat,omitempty"`
	VcardBday                           string    `json:"vcard:bday,omitempty"`
	VcardAddress                        string    `json:"vcard:Address,omitempty"`
	MisskeySummary                      string    `json:"_misskey_summary,omitempty"`
	MisskeyFollowedMessage              string    `json:"_misskey_followedMessage,omitempty"`
	MisskeyRequireSigninToViewContents  bool      `json:"_misskey_requireSigninToViewContents,omitempty"`
	MisskeyMakeNotesFollowersOnlyBefore *int      `json:"_misskey_makeNotesFollowersOnlyBefore,omitempty"`
	MisskeyMakeNotesHiddenBefore        *int      `json:"_misskey_makeNotesHiddenBefore,omitempty"`
	// MisskeyCanChat は CherryPick の chat 連合 capability flag (#692)。
	// 受信側 instance が DM を受け付けるか (false なら拒絶) を表す boolean。
	// pointer で持つことで「未指定 (= 旧実装 / 互換) → 許可」を区別する。
	MisskeyCanChat *bool        `json:"_misskey_canChat,omitempty"`
	MovedTo        string       `json:"movedTo,omitempty"`
	AlsoKnownAs    APStringList `json:"alsoKnownAs,omitempty"`
	// AssertionMethod は FEP-521a Multikey 形式で expose する追加公開鍵リスト
	// (mk-go では現状 Ed25519 のみ)。omitempty なので Ed25519 鍵を持たない
	// user / TS で signup した user では出力されず、drop-in 互換を維持する
	// (#1067 / #1069)。
	AssertionMethod []Multikey `json:"assertionMethod,omitempty"`
}

// Note represents a note object (microblog post).
type Note struct {
	Object
	AttributedTo   string   `json:"attributedTo"`
	Content        string   `json:"content"`
	Source         *Source  `json:"source,omitempty"`
	Published      string   `json:"published"`
	To             []string `json:"to"`
	CC             []string `json:"cc,omitempty"`
	InReplyTo      string   `json:"inReplyTo,omitempty"`
	Summary        string   `json:"summary,omitempty"`
	Sensitive      bool     `json:"sensitive,omitempty"`
	Tag            []any    `json:"tag,omitempty"`
	Attachment     []any    `json:"attachment,omitempty"`
	MisskeyContent string   `json:"_misskey_content,omitempty"`
	MisskeyQuote   string   `json:"_misskey_quote,omitempty"`
	QuoteURL       string   `json:"quoteUrl,omitempty"`
	// MisskeyTalk は CherryPick / レガシー Misskey のチャット連合 flag (#692)。
	// `_misskey_talk: true` が立った Note は ApInboxService で chat message
	// として処理される (notes テーブルではなく chat_messages テーブルに保存)。
	MisskeyTalk bool `json:"_misskey_talk,omitempty"`
	// Name は AP poll vote (Question への投票) で choice 名を運ぶ field。
	// Misskey TS の vote AP payload は `{type: "Note", name: "<choice>",
	// inReplyTo: <poll URI>}` 形式で、ApNoteService が name + inReplyTo の
	// 組合せで vote と判定する。通常の投稿には付かない (#690)。
	Name string `json:"name,omitempty"`
	// Question (poll) fields — AP Question typeで使用
	OneOf   []QuestionChoice `json:"oneOf,omitempty"`
	AnyOf   []QuestionChoice `json:"anyOf,omitempty"`
	EndTime string           `json:"endTime,omitempty"`
	Closed  string           `json:"closed,omitempty"`
}

// QuestionChoice represents a single choice in an AP Question (poll).
type QuestionChoice struct {
	Type    string                 `json:"type"` // "Note"
	Name    string                 `json:"name"`
	Replies *QuestionChoiceReplies `json:"replies,omitempty"`
}

// QuestionChoiceReplies holds the vote count for a poll choice.
type QuestionChoiceReplies struct {
	Type       string `json:"type"` // "Collection"
	TotalItems int    `json:"totalItems"`
}

// Mention is an ActivityStreams Mention tag used inside Note.tag to inform
// receiving instances which actors are mentioned in the note content.
// href は actor URI (ローカルなら urls.UserURI, リモートなら user.URI)、
// name は Misskey 互換の "@username" / "@username@host" 形式。
type Mention struct {
	Type string `json:"type"` // "Mention"
	Href string `json:"href"`
	Name string `json:"name,omitempty"`
}

// NewMention constructs a Mention with the fixed `Type` label pre-filled.
// 呼び出し側で毎回 Type を書かなくて済むようにした factory。name は
// 任意 (空文字でも許容される)。
func NewMention(href, name string) Mention {
	return Mention{Type: "Mention", Href: href, Name: name}
}

// Source represents the original markup of a note (Markdown / MFM).
type Source struct {
	Content   string `json:"content"`
	MediaType string `json:"mediaType"`
}

// Document represents an attached file (image/video/audio/...) in a Note.
//
// Width / Height は `as:width` / `as:height` (ActivityStreams 拡張)。
// Icon は Document を表示する際のサムネイル候補で、Misskey TS 系が
// 連合させる。Blurhash は Misskey の `_misskey_blurhash` カスタム拡張で、
// frontend がメディアロード前にぼかし背景を表示するために使う。
// いずれも欠落している remote 実装も多いので omitempty で受ける。
type Document struct {
	Type      string `json:"type"` // "Document"
	MediaType string `json:"mediaType"`
	URL       string `json:"url"`
	Name      string `json:"name,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Icon      *Image `json:"icon,omitempty"`
	Blurhash  string `json:"_misskey_blurhash,omitempty"`
}

// PropertyValue represents a profile field (schema.org PropertyValue).
type PropertyValue struct {
	Type  string `json:"type"` // "PropertyValue"
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Hashtag is an ActivityStreams Hashtag tag.
type Hashtag struct {
	Type string `json:"type"` // "Hashtag"
	Href string `json:"href"`
	Name string `json:"name"` // "#tag"
}

// EmojiTag represents a toot:Emoji tag for custom emoji.
type EmojiTag struct {
	Type    string `json:"type"` // "Emoji"
	Name    string `json:"name"` // ":name:"
	Icon    Image  `json:"icon"`
	ID      string `json:"id,omitempty"`
	Updated string `json:"updated,omitempty"`
	// License は upstream Misskey TS の AP renderer (renderEmoji) が
	// `_misskey_license: { freeText: ... }` 構造で federate する license 情報
	// (#731)。AP 標準には emoji license 概念が無いので Misskey 拡張として
	// 送受信する。nil = 受信時 license フィールド無し / 送信時は wrapper を
	// 出さない、non-nil = wrapper を出力 (FreeText が空でも対称性のため
	// オブジェクトは出す)。`,omitempty` はポインタ nil でフィールド省略する。
	License *MisskeyLicense `json:"_misskey_license,omitempty"`
}

// MisskeyLicense is the upstream `_misskey_license` AP extension wrapper.
// Misskey TS の renderEmoji 互換 (`{freeText: emoji.license}`)。
type MisskeyLicense struct {
	FreeText *string `json:"freeText"`
}

// Activity is the base type embedded by all activity types.
type Activity struct {
	Object
	Actor     string   `json:"actor"`
	Published string   `json:"published,omitempty"`
	To        []string `json:"to,omitempty"`
	CC        []string `json:"cc,omitempty"`
}

// Create wraps a Note (or other Object) inside a Create activity.
type Create struct {
	Activity
	Object any `json:"object"`
}

// Follow represents a Follow activity.
type Follow struct {
	Activity
	Object string `json:"object"` // followee actor URI
}

// Accept represents an Accept activity.
type Accept struct {
	Activity
	Object any `json:"object"`
}

// Reject represents a Reject activity.
type Reject struct {
	Activity
	Object any `json:"object"`
}

// Undo represents an Undo activity.
type Undo struct {
	Activity
	Object any `json:"object"`
}

// Delete represents a Delete activity.
type Delete struct {
	Activity
	Object any `json:"object"`
}

// Update represents an Update activity.
type Update struct {
	Activity
	Object any `json:"object"`
}

// Like represents a Like (reaction) activity.
type Like struct {
	Activity
	Object          string `json:"object"`                      // target note URI
	Content         string `json:"content,omitempty"`           // reaction emoji (standard)
	MisskeyReaction string `json:"_misskey_reaction,omitempty"` // Misskey拡張: contentより優先
	// Tag は Misskey/CherryPick がカスタム絵文字リアクションを連合させる
	// 際に乗せる Emoji オブジェクト群。Note ingestion と同じ形なので
	// federation.extractEmojiTags / upsertEmojis でそのまま処理できる。
	Tag []any `json:"tag,omitempty"`
}

// Tombstone represents a deleted object placeholder used as the object of a
// Delete activity.
type Tombstone struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Announce represents a Renote (boost) activity.
type Announce struct {
	Activity
	Object string `json:"object"` // target note URI
}

// Flag represents an abuse-report forwarding activity delivered from the
// local instance's system actor to the origin instance of a reported user.
// Content is the moderator-visible comment; Object is the target user's URI.
type Flag struct {
	Activity
	Content string `json:"content"`
	Object  string `json:"object"`
}

// Move represents an account-migration activity. Per Mastodon / Misskey
// convention, actor == object (the old account URI) and target is the new
// account URI. Follower instances use this signal to re-follow the target.
type Move struct {
	Activity
	Object string `json:"object"`
	Target string `json:"target"`
}

// MisskeyContext は Misskey/Mastodon/schema.org 拡張語彙を含むコンテキストオブジェクト。
// TS版 contexts.ts と同等。
var MisskeyContext = map[string]any{
	"misskey":                               "https://misskey-hub.net/ns#",
	"toot":                                  "http://joinmastodon.org/ns#",
	"schema":                                "http://schema.org/",
	"vcard":                                 "http://www.w3.org/2006/vcard/ns#",
	"Key":                                   SecurityContextURL + "#Key",
	"Hashtag":                               ContextURL + "#Hashtag",
	"sensitive":                             ContextURL + "#sensitive",
	"quoteUrl":                              "https://misskey-hub.net/ns#quoteUrl",
	"Emoji":                                 "toot:Emoji",
	"featured":                              "toot:featured",
	"discoverable":                          "toot:discoverable",
	"PropertyValue":                         "schema:PropertyValue",
	"value":                                 "schema:value",
	"isCat":                                 "misskey:isCat",
	"_misskey_content":                      "misskey:_misskey_content",
	"_misskey_quote":                        "misskey:_misskey_quote",
	"_misskey_reaction":                     "misskey:_misskey_reaction",
	"_misskey_talk":                         "misskey:_misskey_talk",
	"_misskey_canChat":                      "misskey:_misskey_canChat",
	"_misskey_votes":                        "misskey:_misskey_votes",
	"_misskey_summary":                      "misskey:_misskey_summary",
	"_misskey_followedMessage":              "misskey:_misskey_followedMessage",
	"_misskey_requireSigninToViewContents":  "misskey:_misskey_requireSigninToViewContents",
	"_misskey_makeNotesFollowersOnlyBefore": "misskey:_misskey_makeNotesFollowersOnlyBefore",
	"_misskey_makeNotesHiddenBefore":        "misskey:_misskey_makeNotesHiddenBefore",
	"_misskey_license":                      "misskey:_misskey_license",
	"freeText":                              map[string]string{"@id": "misskey:freeText", "@type": "schema:text"},
}

// fullContext は全AP出力で使われる完全なJSON-LDコンテキスト。
// 不変として扱うこと。各オブジェクトには newContext() で新しいコピーを渡す。
var fullContext = []any{ContextURL, SecurityContextURL, MisskeyContext}

// personContext は Person actor 専用の拡張コンテキスト。FEP-521a の
// `assertionMethod` (Multikey) を JSON-LD term として認識させるため、
// fullContext に Multikey / Data-Integrity vocabulary を append している。
// Note / Activity 等の他オブジェクトは Multikey を含まないので fullContext
// 側は変更せず、Person 出力のみ context を拡張する設計 (#1067 / #1069)。
var personContext = []any{
	ContextURL,
	SecurityContextURL,
	MultikeyContextURL,
	DataIntegrityContextURL,
	MisskeyContext,
}

// newContext returns a fresh copy of fullContext so callers cannot
// accidentally mutate the shared template via append.
func newContext() []any {
	c := make([]any, len(fullContext))
	copy(c, fullContext)
	return c
}

// newPersonContext returns a fresh copy of personContext (= fullContext +
// Multikey / Data-Integrity vocab) for Person actor output.
func newPersonContext() []any {
	c := make([]any, len(personContext))
	copy(c, personContext)
	return c
}

// AddContext attaches the standard AS+security+Misskey context to any object
// that embeds Object. 配列で持つことで複数 vocabulary を表現する。
// Person 専用には Multikey / Data-Integrity vocabulary を追加で含めて、
// `assertionMethod` term を JSON-LD として正しく解釈可能にする。
func AddContext(o any) {
	ctx := newContext()
	switch v := o.(type) {
	case *Person:
		v.Context = newPersonContext()
		return
	case *Note:
		v.Context = ctx
	case *Create:
		v.Context = ctx
	case *Follow:
		v.Context = ctx
	case *Accept:
		v.Context = ctx
	case *Reject:
		v.Context = ctx
	case *Undo:
		v.Context = ctx
	case *Delete:
		v.Context = ctx
	case *Update:
		v.Context = ctx
	case *Like:
		v.Context = ctx
	case *Announce:
		v.Context = ctx
	case *Flag:
		v.Context = ctx
	case *Move:
		v.Context = ctx
	}
}
