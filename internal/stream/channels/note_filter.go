package channels

import (
	"encoding/json"
	"slices"
	"time"

	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/core/wordmute"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/stream"
)

// viewerIDFromCtx extracts the authenticated user's ID from a ChannelContext.
// Returns "" for anonymous connections so wordmute.MatchNote treats every
// note as "not authored by self" (i.e. eligible for muting). Both the
// notesfilter / wordmute helpers handle empty viewerID safely.
func viewerIDFromCtx(ctx stream.ChannelContext) string {
	u, ok := ctx.User().(*model.User)
	if !ok || u == nil {
		return ""
	}
	return u.ID
}

// viewerUserFromCtx returns the authenticated *model.User (or nil for an
// anonymous connection), guarding against the typed-nil that ctx.User() may
// return (same trap viewerIDFromCtx handles).
func viewerUserFromCtx(ctx stream.ChannelContext) *model.User {
	u, ok := ctx.User().(*model.User)
	if !ok || u == nil {
		return nil
	}
	return u
}

// embedProbe is the minimal embed metadata needed to decide whether an embedded
// renote/reply must be hidden, WITHOUT decoding the whole NoteEntity. Author
// preference fields ride the embed's UserLite (populated only when the embed
// author was preloaded at pack time).
type embedProbe struct {
	UserID         string   `json:"userId"`
	Visibility     string   `json:"visibility"`
	VisibleUserIDs []string `json:"visibleUserIds"`
	Mentions       []string `json:"mentions"`
	CreatedAt      string   `json:"createdAt"`
	User           struct {
		ID                           string `json:"id"`
		RequireSigninToViewContents  *bool  `json:"requireSigninToViewContents"`
		MakeNotesHiddenBefore        *int   `json:"makeNotesHiddenBefore"`
		MakeNotesFollowersOnlyBefore *int   `json:"makeNotesFollowersOnlyBefore"`
	} `json:"user"`
}

// hideEmbeds blanks embedded renote/reply content the connection's viewer is
// not allowed to see, right before a channel Sends the note (#1536). It pulls
// the viewer + per-connection following snapshot from ctx; the snapshot makes
// the follower check O(1) in-memory (no DB query per subscriber).
func hideEmbeds(ctx stream.ChannelContext, payload []byte) []byte {
	return hideEmbedsForViewer(payload, viewerUserFromCtx(ctx), ctx.FollowingSnapshot(), time.Now().UnixMilli())
}

// hideEmbedsForViewer is the testable core of hideEmbeds. It returns the
// ORIGINAL payload verbatim (zero copy) unless an embed is genuinely hideable
// for viewer, in which case it decodes a FRESH NoteEntity, blanks the
// hide-eligible embeds, and re-marshals — never mutating the shared input
// buffer / publisher body cache. snap maps followeeID->withReplies (only key
// presence is used). nil viewer / nil snap fail closed (followers/specified
// hidden, public kept).
func hideEmbedsForViewer(payload []byte, viewer *model.User, snap map[string]bool, nowMs int64) []byte {
	var probe struct {
		Renote json.RawMessage `json:"renote"`
		Reply  json.RawMessage `json:"reply"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return payload
	}
	hideRenote := embedRawHideable(probe.Renote, viewer, snap, nowMs)
	hideReply := embedRawHideable(probe.Reply, viewer, snap, nowMs)
	if !hideRenote && !hideReply {
		// 隠す embed が無い (= no-embed / public / own / follower) → verbatim。
		return payload
	}
	var full entity.NoteEntity
	if err := json.Unmarshal(payload, &full); err != nil {
		return payload
	}
	if hideRenote && full.Renote != nil {
		entity.HideNoteEntity(full.Renote)
	}
	if hideReply && full.Reply != nil {
		entity.HideNoteEntity(full.Reply)
	}
	out, err := json.Marshal(&full)
	if err != nil {
		return payload
	}
	return out
}

// embedRawHideable decodes the embed-probe metadata from a raw embed object and
// runs the shared decision. Absent/null embed or a probe-decode error → false
// (nothing to hide / cannot probe).
func embedRawHideable(raw json.RawMessage, viewer *model.User, snap map[string]bool, nowMs int64) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var p embedProbe
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	follows := func(id string) bool {
		if snap == nil {
			return false
		}
		_, ok := snap[id]
		return ok
	}
	return corenote.HideEmbedDecision(viewer, embedFactsFromProbe(p, nowMs), follows, nowMs)
}

func embedFactsFromProbe(p embedProbe, fallbackMs int64) corenote.EmbedFacts {
	f := corenote.EmbedFacts{
		AuthorID:       p.UserID,
		Visibility:     p.Visibility,
		VisibleUserIDs: p.VisibleUserIDs,
		Mentions:       p.Mentions,
		CreatedAtMs:    parseCreatedAtMsStream(p.CreatedAt, fallbackMs),
	}
	if p.User.ID != "" {
		f.AuthorPrefsKnown = true
		if p.User.RequireSigninToViewContents != nil {
			f.RequireSigninToViewContents = *p.User.RequireSigninToViewContents
		}
		f.MakeNotesHiddenBefore = p.User.MakeNotesHiddenBefore
		f.MakeNotesFollowersOnlyBefore = p.User.MakeNotesFollowersOnlyBefore
	}
	return f
}

func parseCreatedAtMsStream(s string, fallbackMs int64) int64 {
	if s == "" {
		return fallbackMs
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fallbackMs
	}
	return t.UnixMilli()
}

// noteFilter provides client-controlled filtering for timeline channels.
// タイムラインチャンネルのconnectパラメータで指定されるフィルタ条件。
//
// #1063 以降、reply の表示可否は noteFilter からは判定しない (= per-channel
// な upstream Misskey TS 互換ロジックに分離した、replyShouldEmit 参照)。
// `WithReplies` field は upstream `local-timeline` / `hybrid-timeline` の
// connect param と互換性を保つために残しつつ、shouldEmit からは参照しない。
type noteFilter struct {
	WithRenotes bool `json:"withRenotes"`
	WithReplies bool `json:"withReplies"`
	WithFiles   bool `json:"withFiles"`
}

// defaultNoteFilter returns a filter with default values matching TS behavior.
func defaultNoteFilter() noteFilter {
	return noteFilter{
		WithRenotes: true,
		WithReplies: false,
		WithFiles:   false,
	}
}

// parseNoteFilter parses filter parameters from the connect params JSON.
func parseNoteFilter(params json.RawMessage) noteFilter {
	f := defaultNoteFilter()
	if len(params) > 0 {
		_ = json.Unmarshal(params, &f)
	}
	return f
}

// replyMeta is the embedded `note.reply` slice. upstream Misskey TS の
// NoteEntityService.pack が `reply.userId` / `reply.visibility` を埋めてくる
// 前提に揃える (#1063)。embed が欠如している場合、reply gate は 3 escape
// hatch のうち "reply.userId" を参照する 2 つを判定できないので
// conservative に drop する側に倒す。
type replyMeta struct {
	UserID     string `json:"userId"`
	Visibility string `json:"visibility"`
}

// notePayload is the minimal structure needed for filtering decisions.
// hardMutedWords (#787) も同 struct で見るので user / cw を含める。
// #1063 で reply 関連を replyMeta 経由で取り込み、reply gate で参照する。
// Mentions は #1195 で追加。reply gate の mention escape hatch で「viewer
// が note.mentions に含まれていれば withReplies 設定に関係なく pass」を
// 判定するのに使う。NoteEntityService.pack は packed note に `mentions:
// string[]` を出力するので JSON tag で拾える。
type notePayload struct {
	UserID   string     `json:"userId"`
	Text     *string    `json:"text"`
	CW       *string    `json:"cw"`
	RenoteID *string    `json:"renoteId"`
	ReplyID  *string    `json:"replyId"`
	FileIDs  []string   `json:"fileIds"`
	Mentions []string   `json:"mentions"`
	Reply    *replyMeta `json:"reply,omitempty"`
}

// shouldEmit returns true if the note passes non-reply filter conditions
// (renote / file / hardmute). reply の表示可否は channel ごとに異なる
// semantics で別途 replyShouldEmit が判定する (#1063)。
//
// hardMuteRules (#787) には viewer の user_profile.hardMutedWords (jsonb) を
// そのまま渡す。空 / nil なら hard mute は no-op。viewerID は self skip 用、
// streaming は authenticated path のみ呼ばれるので空文字は anonymous 扱い。
func (f *noteFilter) shouldEmit(payload []byte, hardMuteRules []byte, viewerID string) bool {
	var note notePayload
	if err := json.Unmarshal(payload, &note); err != nil {
		// パース失敗時はそのまま送信
		return true
	}

	// 純リノート（テキストなし + renoteIdあり + ファイルなし）をフィルタ
	// timeline_filter.goのisPureRenoteと一致させる
	if !f.WithRenotes && note.Text == nil && note.RenoteID != nil && len(note.FileIDs) == 0 {
		return false
	}

	// ファイル付きのみモード
	if f.WithFiles && len(note.FileIDs) == 0 {
		return false
	}

	// hardMutedWords filter (#787)。rules 空なら no-op。
	if len(hardMuteRules) > 0 {
		text, cw := "", ""
		if note.Text != nil {
			text = *note.Text
		}
		if note.CW != nil {
			cw = *note.CW
		}
		if wordmute.MatchNote(hardMuteRules, note.UserID, viewerID, text, cw) {
			return false
		}
	}

	return true
}

// replyGateMode controls how replyShouldEmit decides emission per timeline
// channel. upstream Misskey TS の各 channel impl と semantics を揃える
// (#1063)。
type replyGateMode int

const (
	// replyGateHome is the homeTimeline semantics. snapshot が
	// `note.userId` の withReplies=true を持つなら通常 pass (visibility が
	// followers なら follow 関係を別途 check)、それ以外は 3 escape hatch のみ
	// (reply.userId === me / note.userId === me / reply.userId === note.userId)。
	// upstream `home-timeline.ts` には withReplies connect param が無いので、
	// paramWithReplies は無視する。
	replyGateHome replyGateMode = iota

	// replyGateHybrid is the hybridTimeline semantics. replyGateHome と同じ
	// だが connect param `withReplies` を followee.withReplies と OR で評価する
	// (upstream `hybrid-timeline.ts`)。
	replyGateHybrid

	// replyGateLocal is the localTimeline semantics. upstream `local-timeline`
	// は authenticated viewer 限定で reply を gate し、`!this.withReplies && !
	// this.following[note.userId]?.withReplies` のとき 3 escape hatch を判定する
	// (= anonymous は anyway pass-through、つまり global と同じ)。
	replyGateLocal

	// replyGateGlobal disables reply filtering entirely. upstream
	// `global-timeline` は reply 制御を持たない。
	replyGateGlobal
)

// replyShouldEmit decides whether to emit `payload` given the channel-mode
// semantics, the viewer's followee snapshot, and the connect param
// `withReplies`. payload は packed NoteEntity の JSON。
//
// snapshot は ChannelContext.FollowingSnapshot() の返り値 (followeeID →
// withReplies)。anonymous / lookup 未配線時は nil。nil 時は `snap[X]` が
// 必ず false / "key 無し" を返すので gate は 3 escape hatch のみで動く。
//
// paramWithReplies は connect 時の `withReplies` boolean。upstream で param
// を持たない home-timeline では呼び出し側が常に false を渡す。
//
// Defensive 設計の早期 return が 2 段ある:
//
//  1. mode == replyGateGlobal は unmarshal すら行わずに pass-through する
//     (upstream `global-timeline.ts` には reply gate が無いため hot path
//     のコストも最小化する意図)。
//  2. JSON unmarshal 失敗時も pass-through する。payload が壊れているなら
//     gate しないほうが「全 user に表示しない」より副作用が小さいため。
//     これは shouldEmit と同じポリシー。
func replyShouldEmit(payload []byte, viewerID string, snap map[string]bool, paramWithReplies bool, mode replyGateMode) bool {
	if mode == replyGateGlobal {
		return true
	}
	var note notePayload
	if err := json.Unmarshal(payload, &note); err != nil {
		return true
	}
	if note.ReplyID == nil {
		return true
	}

	// upstream `isMe = this.user!.id === note.userId` の escape hatch。
	// 自分の reply は followee snapshot や withReplies 設定に依存せず常に pass。
	isMe := viewerID != "" && note.UserID == viewerID

	// localTimeline / hybridTimeline は anonymous viewer に対して reply gate
	// を一切かけない (upstream `if (note.reply && this.user && ...)` の
	// `this.user` 条件相当)。upstream hybrid-timeline は requireCredential=true
	// なので「anonymous hybrid」状態は upstream に存在しないが、mk-go は legacy
	// 互換で anonymous viewer にも hybrid 購読を許しており、その場合 followee
	// snapshot を一切持たない (snap=nil)。default 分岐に落とすと selfThread 以外
	// の reply が全て drop されて旧 mk-go (`WithReplies=true → 全 pass`) より
	// 厳しい挙動になるので、anonymous local と同じく pass-through に倒す。
	// homeTimeline は Init で anonymous 自体を no-op にするのでここには来ない。
	if (mode == replyGateLocal || mode == replyGateHybrid) && viewerID == "" {
		return true
	}

	if isMe {
		return true
	}

	// reply embed が欠如している場合は reply.userId 参照ができないので
	// "reply.userId === me" / "reply.userId === note.userId" の判定が
	// 不能。conservative に drop して fanout 側 / DB 側で正しく拾われる
	// (= リロード時に表示される) フォールバックに倒す。
	if note.Reply == nil {
		return false
	}

	replyToMe := viewerID != "" && note.Reply.UserID == viewerID
	selfThread := note.Reply.UserID == note.UserID
	// mk-go 独自 escape hatch (#1195): viewer が note.mentions に含まれて
	// いれば、withReplies / followee.withReplies の設定に関係なく reply gate を
	// pass する。upstream Misskey TS は home/hybrid/local channel いずれもこの
	// escape を持たないので意図的 deviation。
	isMentioned := viewerID != "" && slices.Contains(note.Mentions, viewerID)

	var followeeWithReplies bool
	if snap != nil {
		followeeWithReplies = snap[note.UserID]
	}

	switch mode {
	case replyGateHome, replyGateHybrid:
		// upstream `home-timeline.ts` / `hybrid-timeline.ts` の reply gate。
		// home は paramWithReplies を見ず、hybrid のみ followee.withReplies と
		// OR で評価する点が違うので mode で switch する。
		paramOR := mode == replyGateHybrid && paramWithReplies
		if followeeWithReplies || paramOR {
			// followers visibility への reply は follow 関係を別途 check
			// (upstream の "自分のフォローしていないユーザーの visibility:
			// followers な投稿への返信は弾く" branch)。snap に reply.userId
			// が含まれていない (= follow していない) かつ自分宛でもないなら drop。
			if note.Reply.Visibility == "followers" && !replyToMe {
				if _, follows := snap[note.Reply.UserID]; !follows {
					return false
				}
			}
			return true
		}
		// 残り 3 escape hatch (replyToMe / selfThread / isMentioned)。
		// isMe は上で抜けている。isMentioned は mk-go 独自 (#1195)。
		return replyToMe || selfThread || isMentioned

	case replyGateLocal:
		// local-timeline は anonymous で reply gate 不要、authenticated 時のみ
		// `!this.withReplies && !following[note.userId].withReplies` → 3 escape
		// hatch + isMentioned で pass。isMentioned は mk-go 独自 (#1195)。
		if !followeeWithReplies && !paramWithReplies {
			return replyToMe || selfThread || isMentioned
		}
		return true
	}
	// 未知の replyGateMode は保守的に pass-through する。replyGateGlobal は
	// 関数頭で早期 return 済み、それ以外の既存 mode は上の switch ですべて
	// return する設計なので、ここに到達するのは将来 mode を追加し忘れた
	// 場合のみ。production には影響しないが「panic より degrade」に倒す方が
	// 既存 streaming filter の defensive 方針と整合する。
	return true
}
