package entity

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/datatypes"
)

// BufferedReactionsReader reads buffered reaction deltas (Redis-side)
// for a batch of notes. enableReactionsBuffering=true な instance では
// reaction count が Redis 側にバッファされ、定期 flush で DB の
// note.Reactions JSONB に反映されるが、flush までの間は DB が stale で
// timeline で reaction が見えない。PackNotes/PackNoteWithInstance に
// この interface を渡すと merge してから serialize する (#647)。
//
// nil 実装も受け付ける (= buffering 無し / direct writer 使用時)。
type BufferedReactionsReader interface {
	GetBufferedMany(ctx context.Context, noteIDs []string) (map[string]map[string]int64, error)
}

// localEmojiPattern matches `:name:` without `@host` suffix.
var localEmojiPattern = regexp.MustCompile(`^:([\w+\-]+):$`)

// NoteEntity is the note representation returned by API endpoints.
type NoteEntity struct {
	ID                 string            `json:"id"`
	CreatedAt          string            `json:"createdAt"`
	UserID             string            `json:"userId"`
	User               UserLite          `json:"user"`
	Text               *string           `json:"text"`
	CW                 *string           `json:"cw"`
	Visibility         string            `json:"visibility"`
	LocalOnly          bool              `json:"localOnly"`
	ReactionAcceptance *string           `json:"reactionAcceptance"`
	Reactions          datatypes.JSON    `json:"reactions"`
	ReactionCount      int               `json:"reactionCount"`
	ReactionEmojis     map[string]string `json:"reactionEmojis"`
	RenoteCount        int16             `json:"renoteCount"`
	RepliesCount       int16             `json:"repliesCount"`
	ClippedCount       int               `json:"clippedCount"`
	URI                *string           `json:"uri,omitempty"`
	URL                *string           `json:"url,omitempty"`
	ReplyID            *string           `json:"replyId"`
	RenoteID           *string           `json:"renoteId"`
	Reply              *NoteEntity       `json:"reply,omitempty"`
	Renote             *NoteEntity       `json:"renote,omitempty"`
	FileIDs            []string          `json:"fileIds"`
	Files              []any             `json:"files"`
	Tags               []string          `json:"tags,omitempty"`
	Poll               *PollEntity       `json:"poll,omitempty"`
	Emojis             map[string]string `json:"emojis"`
	ChannelID          *string           `json:"channelId,omitempty"`
	Channel            *ChannelLite      `json:"channel,omitempty"`
	VisibleUserIDs     []string          `json:"visibleUserIds"`
	Mentions           []string          `json:"mentions"`
	HasPoll            bool              `json:"hasPoll"`
	MyReaction         *string           `json:"myReaction,omitempty"`
	IsHidden           bool              `json:"isHidden,omitempty"`
}

// ChannelLite is the minimal channel info embedded in NoteEntity.
type ChannelLite struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Color                 string `json:"color"`
	IsSensitive           bool   `json:"isSensitive"`
	AllowRenoteToExternal bool   `json:"allowRenoteToExternal"`
}

// PollEntity is the poll representation in a note.
type PollEntity struct {
	ExpiresAt *string      `json:"expiresAt"`
	Multiple  bool         `json:"multiple"`
	Choices   []PollChoice `json:"choices"`
}

// PollChoice represents a single poll choice.
type PollChoice struct {
	Text    string `json:"text"`
	Votes   int    `json:"votes"`
	IsVoted bool   `json:"isVoted"`
}

// maxNoteEmbedDepth caps how many levels of Renote / Reply are expanded when
// packing. repository 側の preloadNoteRelations は 1 段しか preload しない
// ので通常 1 で十分だが、テストや将来の preload 変更で n.Renote.Renote などが
// 意図せず非 nil になった場合でも応答を bounded に保つためのガード (#416
// Devin review)。
const maxNoteEmbedDepth = 1

// PackNote converts a model.Note to a NoteEntity. Renote / Reply の embed は
// maxNoteEmbedDepth で制限する。
func PackNote(n *model.Note, idGen id.Generator) NoteEntity {
	return packNoteAtDepth(n, idGen, 0)
}

// packPoll maps a preloaded model.Poll onto the Misskey-compatible PollEntity.
// nil 入力 (Poll relation が preload されていない / hasPoll=false) は nil を
// 返し、上位の `json:"poll,omitempty"` で response から省略させる。
//
// IsVoted は viewer context が無いと判定できないため常に false で埋める
// (#690 の最小修正範囲)。viewer の vote 判定は別 path で enrich する想定で、
// 投稿直後の create response では「自分の投稿に自分はまだ vote していない」
// が常に正解なので false 固定でも frontend 表示は正しい。
func packPoll(p *model.Poll) *PollEntity {
	if p == nil {
		return nil
	}
	choices := make([]PollChoice, len(p.Choices))
	for i, text := range p.Choices {
		votes := 0
		if i < len(p.Votes) {
			votes = int(p.Votes[i])
		}
		choices[i] = PollChoice{Text: text, Votes: votes, IsVoted: false}
	}
	var expiresAt *string
	if p.ExpiresAt != nil {
		s := p.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
		expiresAt = &s
	}
	return &PollEntity{
		ExpiresAt: expiresAt,
		Multiple:  p.Multiple,
		Choices:   choices,
	}
}

func packNoteAtDepth(n *model.Note, idGen id.Generator, depth int) NoteEntity {
	createdAt := ""
	if t, err := idGen.ParseTime(n.ID); err == nil {
		createdAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}

	fileIDs := make([]string, 0)
	if n.FileIDs != nil {
		fileIDs = n.FileIDs
	}

	visibleUserIDs := make([]string, 0)
	if n.VisibleUserIDs != nil {
		visibleUserIDs = n.VisibleUserIDs
	}

	mentions := make([]string, 0)
	if n.Mentions != nil {
		mentions = n.Mentions
	}

	entity := NoteEntity{
		ID:                 n.ID,
		CreatedAt:          createdAt,
		UserID:             n.UserID,
		Text:               n.Text,
		CW:                 n.CW,
		Visibility:         string(n.Visibility),
		LocalOnly:          n.LocalOnly,
		ReactionAcceptance: n.ReactionAcceptance,
		Reactions:          normalizeReactionKeys(n.Reactions),
		ReactionCount:      sumReactions(n.Reactions),
		ReactionEmojis:     make(map[string]string),
		RenoteCount:        n.RenoteCount,
		RepliesCount:       n.RepliesCount,
		ClippedCount:       0,
		URI:                n.URI,
		URL:                n.URL,
		ReplyID:            n.ReplyID,
		RenoteID:           n.RenoteID,
		FileIDs:            fileIDs,
		Files:              []any{},
		Tags:               n.Tags,
		Emojis:             make(map[string]string),
		ChannelID:          n.ChannelID,
		VisibleUserIDs:     visibleUserIDs,
		Mentions:           mentions,
		HasPoll:            n.HasPoll,
		Poll:               packPoll(n.Poll),
	}

	if n.User != nil {
		entity.User = PackUserLite(n.User)
	}

	// Renote / Reply の target は repository 層で Preload("Renote.User") /
	// Preload("Reply.User") されている前提で 1 段だけ展開する。preload が
	// 無ければ n.Renote == nil になり、フロントエンドはこれを「削除された投稿」
	// として描画する (renoteId だけが入ってる状態と区別するため #416)。
	if depth < maxNoteEmbedDepth {
		if n.Renote != nil {
			r := packNoteAtDepth(n.Renote, idGen, depth+1)
			entity.Renote = &r
		}
		if n.Reply != nil {
			r := packNoteAtDepth(n.Reply, idGen, depth+1)
			entity.Reply = &r
		}
	}

	return entity
}

// PackNotes packs a slice of notes and populates UserLite.Instance for remote
// users in a single batch fetch via lookup. lookup == nil keeps Instance as
// nil (convenient for handlers not yet wired or for contexts where instance
// embed is unnecessary).
//
// reactionReader が non-nil なら buffered reactions を batch fetch して
// flat 内の各 note.Reactions に in-place merge してから resolver を構築する
// (#647)。これにより enableReactionsBuffering=true でも timeline / show
// レスポンスで最新 reaction count / emoji が返る。reader が nil なら
// 旧挙動 (DB のみ) で動く。ctx は reader.GetBufferedMany に渡され、
// handler 側の deadline / cancellation / tracing が下流に伝播する (#657)。
//
// flattenNotesPlusRelations で top-level + Renote/Reply の target note を
// 1 まとめにしてから resolver を作る。CollectNoteAuthors も flatten 済みの
// スライスから author を拾うので、埋め込み note の remote user にも Instance /
// emoji が正しく載る。
func PackNotes(ctx context.Context, notes []*model.Note, idGen id.Generator, instLookup InstanceLookup, emojiLookup EmojiLookup, reactionReader BufferedReactionsReader) []NoteEntity {
	flat := flattenNotesPlusRelations(notes)
	mergeBufferedReactions(ctx, flat, reactionReader)
	instResolver := NewInstanceResolver(instLookup, CollectNoteAuthors(flat)...)
	emojiResolver := NewEmojiResolver(emojiLookup, flat)
	out := make([]NoteEntity, 0, len(notes))
	for _, n := range notes {
		packed := PackNote(n, idGen)
		applyNoteResolvers(n, &packed, instResolver, emojiResolver)
		out = append(out, packed)
	}
	return out
}

// PackNoteWithInstance is a single-note convenience wrapper: pack + populate.
//
// **Single-note only.** Each call spins up a fresh InstanceResolver (1 DB
// query via lookup.FindManyByHosts). For a slice of notes, call `PackNotes`
// instead — calling this in a loop produces N+1 queries.
//
// reactionReader / ctx 引数の意味は PackNotes と同じ (#647 / #657)。
func PackNoteWithInstance(ctx context.Context, n *model.Note, idGen id.Generator, instLookup InstanceLookup, emojiLookup EmojiLookup, reactionReader BufferedReactionsReader) NoteEntity {
	flat := flattenNotesPlusRelations([]*model.Note{n})
	mergeBufferedReactions(ctx, flat, reactionReader)
	packed := PackNote(n, idGen)
	instResolver := NewInstanceResolver(instLookup, CollectNoteAuthors(flat)...)
	emojiResolver := NewEmojiResolver(emojiLookup, flat)
	applyNoteResolvers(n, &packed, instResolver, emojiResolver)
	return packed
}

// mergeBufferedReactions fetches buffered deltas for the given notes via
// reader and overwrites each note.Reactions with the merged JSON. Pure
// data transform; receiver-side mutation is intentional (#647) so that
// downstream EmojiResolver / PackNote pick up the merged map automatically.
//
// reader が nil または notes が空ならば no-op。ctx は reader への伝播用
// (#657)。
func mergeBufferedReactions(ctx context.Context, notes []*model.Note, reader BufferedReactionsReader) {
	if reader == nil || len(notes) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ids := make([]string, 0, len(notes))
	seen := make(map[string]struct{}, len(notes))
	for _, n := range notes {
		if n == nil || n.ID == "" {
			continue
		}
		if _, dup := seen[n.ID]; dup {
			continue
		}
		seen[n.ID] = struct{}{}
		ids = append(ids, n.ID)
	}
	if len(ids) == 0 {
		return
	}
	deltas, err := reader.GetBufferedMany(ctx, ids)
	if err != nil || len(deltas) == 0 {
		// reader 失敗時は stale な DB 値を返す (旧挙動と同等)。
		return
	}
	// 同一 *Note ポインタが flat に複数回現れうる (例: 2 件の note が同じ renote
	// 先を持つと、repository の batch hydration では両者の .Renote が同一ポインタを
	// 指す)。mergeReactionsJSON は加算的なので、同じオブジェクトに 2 回適用すると
	// delta が二重加算される。ポインタ単位で dedup して各オブジェクトへの適用を
	// 1 回に限定する (id 単位ではなく: preload 経由の別オブジェクト同 id はそれぞれ
	// base から 1 回ずつ merge されるべきで、正しい)。
	applied := make(map[*model.Note]struct{}, len(notes))
	for _, n := range notes {
		if n == nil {
			continue
		}
		if _, done := applied[n]; done {
			continue
		}
		applied[n] = struct{}{}
		if d, ok := deltas[n.ID]; ok && len(d) > 0 {
			n.Reactions = mergeReactionsJSON(n.Reactions, d)
		}
	}
}

// mergeReactionsJSON merges buffered deltas into the existing reactions
// JSONB and returns a new datatypes.JSON. 0 以下になったキーは結果から
// 取り除く (TS の mergeReactions と同等)。core/reaction.MergeReactions
// と同じロジックだが、layer 上 entity → core 依存を作らないために
// inline 実装している。
func mergeReactionsJSON(reactionsJSON datatypes.JSON, buffered map[string]int64) datatypes.JSON {
	if len(buffered) == 0 {
		return reactionsJSON
	}
	reactions := make(map[string]int64)
	if len(reactionsJSON) > 0 {
		var f map[string]float64
		if err := json.Unmarshal(reactionsJSON, &f); err == nil {
			for k, v := range f {
				reactions[k] = int64(v)
			}
		}
	}
	for k, delta := range buffered {
		reactions[k] += delta
		if reactions[k] <= 0 {
			delete(reactions, k)
		}
	}
	if len(reactions) == 0 {
		return datatypes.JSON([]byte("{}"))
	}
	data, err := json.Marshal(reactions)
	if err != nil {
		return reactionsJSON
	}
	return datatypes.JSON(data)
}

// applyNoteResolvers fills Instance + emojis on the packed entity and its
// embedded renote/reply children. Preload は 1 段だけなので Renote.Renote や
// Reply.Reply は常に nil で、深い再帰には成らない。
func applyNoteResolvers(n *model.Note, e *NoteEntity, instResolver *InstanceResolver, emojiResolver *EmojiResolver) {
	instResolver.FillUserLite(&e.User)
	emojiResolver.PopulateNoteEmojis(n, e)
	emojiResolver.PopulateNoteReactionEmojis(n, e)
	if n.User != nil {
		emojiResolver.PopulateUserEmojis(n.User, &e.User)
	}
	if n.Renote != nil && e.Renote != nil {
		applyNoteResolvers(n.Renote, e.Renote, instResolver, emojiResolver)
	}
	if n.Reply != nil && e.Reply != nil {
		applyNoteResolvers(n.Reply, e.Reply, instResolver, emojiResolver)
	}
}

// flattenNotesPlusRelations returns notes plus any preloaded Renote/Reply
// targets (1 level deep, since GORM only preloads the relations we ask for).
// resolver 構築時に embed 先 note の author / emoji も拾うために使う。
func flattenNotesPlusRelations(notes []*model.Note) []*model.Note {
	// 最悪ケース (全 note が Renote + Reply 両方を持つ) で *3 要素になるので
	// 容量もそれに合わせる。多くの note は片方以下なので余剰確保だが、
	// timeline fetch 30 件 × 2 の再アロケートよりは安上がり。
	flat := make([]*model.Note, 0, len(notes)*3)
	for _, n := range notes {
		if n == nil {
			continue
		}
		flat = append(flat, n)
		if n.Renote != nil {
			flat = append(flat, n.Renote)
		}
		if n.Reply != nil {
			flat = append(flat, n.Reply)
		}
	}
	return flat
}

// CollectNoteAuthors returns the author `User` pointer of each note that has
// one preloaded. Used by packers and handlers when building an
// InstanceResolver over a pre-fetched slice of notes.
//
// note.User のみを拾う。reply/renote の author も含めたい場合は呼び出し側で
// flattenNotesPlusRelations を事前適用してから渡すこと (PackNotes はそうして
// いる)。
func CollectNoteAuthors(notes []*model.Note) []*model.User {
	users := make([]*model.User, 0, len(notes))
	for _, n := range notes {
		if n == nil || n.User == nil {
			continue
		}
		users = append(users, n.User)
	}
	return users
}

// NormalizeReactionKey converts a legacy `:name:` key to the canonical
// `:name@.:` form used by the frontend. Remote and non-custom reactions
// are returned unchanged.
func NormalizeReactionKey(key string) string {
	if m := localEmojiPattern.FindStringSubmatch(key); m != nil {
		return ":" + m[1] + "@.:"
	}
	return key
}

// normalizeReactionKeys rewrites reaction JSONB keys so that legacy
// `:name:` entries are merged into `:name@.:`.
// TS時代のレコードとmk時代のレコードが同一キーに集約される。
func normalizeReactionKeys(raw datatypes.JSON) datatypes.JSON {
	if len(raw) == 0 {
		// golden Note.reactions は Record (object) 必須。reactions 未設定の note
		// (create 直後の in-memory note 等、DB default '{}' を経ていないもの) は
		// nil datatypes.JSON が JSON null になり drift するため {} に coalesce する
		// (#1312。channels pinnedNoteIds #1283 と同種の null-object drift)。
		return datatypes.JSON("{}")
	}
	var m map[string]float64
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	normalized := make(map[string]float64, len(m))
	for k, v := range m {
		nk := NormalizeReactionKey(k)
		normalized[nk] += v
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return raw
	}
	return data
}

// sumReactions decodes the reactions JSONB and sums all values.
func sumReactions(raw datatypes.JSON) int {
	if len(raw) == 0 {
		return 0
	}
	var m map[string]float64
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0
	}
	total := 0
	for _, v := range m {
		total += int(v)
	}
	return total
}
