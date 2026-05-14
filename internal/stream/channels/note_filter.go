package channels

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/core/wordmute"
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
type notePayload struct {
	UserID   string     `json:"userId"`
	Text     *string    `json:"text"`
	CW       *string    `json:"cw"`
	RenoteID *string    `json:"renoteId"`
	ReplyID  *string    `json:"replyId"`
	FileIDs  []string   `json:"fileIds"`
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
		// 残り 2 escape hatch (replyToMe / selfThread)。isMe は上で抜けている。
		return replyToMe || selfThread

	case replyGateLocal:
		// local-timeline は anonymous で reply gate 不要、authenticated 時のみ
		// `!this.withReplies && !following[note.userId].withReplies` → 3 escape
		// hatch のみで pass。
		if !followeeWithReplies && !paramWithReplies {
			return replyToMe || selfThread
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
