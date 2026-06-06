package entity

import (
	"encoding/json"
	"strings"

	"github.com/shiroha-a/mk/internal/model"
)

// parseCustomEmojiReaction extracts the (name, host) pair from a reaction
// string. Custom emoji reactions are wrapped in colons (`:smile:` for local
// or `:smile@remote.example.com:` for remote); raw unicode reactions (no
// surrounding colons, e.g. heart) are returned with ok=false. Local
// reactions return host="".
//
// reaction_service.normalizeReaction が local 絵文字を `:name@.:` 形式 (host
// を `.` で表す TS 互換 canonical 形式) で永続化するため、parser 側でも `@.`
// を空 host に正規化する。これを忘れると DB lookup が host="." vs DB の
// host IS NULL で空振りし、reactionEmojis が空のままになる。
func parseCustomEmojiReaction(s string) (name, host string, ok bool) {
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

// emojiNameHost is a (name, host) pair preserving distinct hosts when a
// single note's reactions contain the same emoji name from multiple
// remote hosts. Keying solely by name would collapse such cases.
type emojiNameHost struct {
	name string
	host string
}

// reactionEmojiKey は frontend の lookup (`reaction.substring(1, length-1)`)
// と一致するキーを返す。local は "smile"、remote は "smile@host"。
func reactionEmojiKey(name, host string) string {
	if host == "" {
		return name
	}
	return name + "@" + host
}

// EmojiLookup is the minimal interface required to batch-fetch emoji rows
// for populating UserLite.Emojis and NoteEntity.Emojis. 循環依存を避ける
// ため interface で受け取る (実装は repository.EmojiRepository)。
type EmojiLookup interface {
	FindManyByNamesAndHost(names []string, host *string) ([]*model.Emoji, error)
}

// EmojiResolver caches (name, host) → URL lookups so that packers can
// populate Emojis maps without repeating DB queries. InstanceResolverと同
// パターンで、1回のhost別batch fetch後にO(1)参照する。
//
// nilレシーバは常にno-opを返す (EmojiLookupが未配線な呼出し元向け)。
type EmojiResolver struct {
	cache map[string]string // "name@host" → url
}

// NewEmojiResolver collects unique (name, host) pairs from the notes and
// their authors, then batch-fetches matching emoji rows grouped by host.
//
// lookup==nilなら空cacheのresolverを返す (PopulateXxx は全てno-op)。
func NewEmojiResolver(lookup EmojiLookup, notes []*model.Note) *EmojiResolver {
	r := &EmojiResolver{cache: map[string]string{}}
	if lookup == nil {
		return r
	}
	// host別に絵文字名を集約
	hostNames := map[string]map[string]struct{}{}
	addNames := func(names []string, host *string) {
		if len(names) == 0 {
			return
		}
		h := ""
		if host != nil {
			h = *host
		}
		if hostNames[h] == nil {
			hostNames[h] = map[string]struct{}{}
		}
		for _, n := range names {
			hostNames[h][n] = struct{}{}
		}
	}
	for _, n := range notes {
		if n == nil {
			continue
		}
		addNames(n.Emojis, n.UserHost)
		if n.User != nil {
			addNames(n.User.Emojis, n.User.Host)
		}
		// note.Reactions の JSON キーから custom emoji を抽出して
		// host 別 batch fetch のリストに追加する (#459)。reaction 元の
		// host は note.UserHost と必ずしも一致しないため、reaction
		// 文字列内の `@host` を信頼する。同名の絵文字が複数 host から
		// 届くケースを失わないよう slice で受け取る。
		for _, pair := range collectReactionEmojiNames(n.Reactions) {
			h := pair.host
			var hostPtr *string
			if h != "" {
				hostPtr = &h
			}
			addNames([]string{pair.name}, hostPtr)
		}
	}
	// hostごとにbatch fetch
	for host, nameSet := range hostNames {
		names := make([]string, 0, len(nameSet))
		for n := range nameSet {
			names = append(names, n)
		}
		var hostPtr *string
		if host != "" {
			hostPtr = &host
		}
		emojis, err := lookup.FindManyByNamesAndHost(names, hostPtr)
		if err != nil {
			continue
		}
		for _, e := range emojis {
			url := e.PublicURL
			if url == "" {
				url = e.OriginalURL
			}
			r.cache[e.Name+"@"+host] = url
		}
	}
	return r
}

// PopulateNoteEmojis resolves emoji names stored in note.Emojis to URLs
// and sets entity.Emojis. no-opの場合entity.Emojisは変更しない
// (PackNoteが設定した空mapを維持する)。
func (r *EmojiResolver) PopulateNoteEmojis(note *model.Note, entity *NoteEntity) {
	if r == nil || note == nil || entity == nil || len(note.Emojis) == 0 {
		return
	}
	host := ""
	if note.UserHost != nil {
		host = *note.UserHost
	}
	emojis := make(map[string]string, len(note.Emojis))
	for _, name := range note.Emojis {
		if url, ok := r.cache[name+"@"+host]; ok {
			emojis[name] = url
		}
	}
	if len(emojis) > 0 {
		entity.Emojis = emojis
	}
}

// PopulateNoteReactionEmojis resolves the custom emoji used inside
// note.Reactions and populates entity.ReactionEmojis with `key → url`
// where key matches the frontend's lookup format
// (`reaction.substring(1, length-1)`): `name` for local custom emoji,
// `name@host` for remote. Raw unicode reactions are skipped — frontend
// renders them directly without map lookup. (#459)
func (r *EmojiResolver) PopulateNoteReactionEmojis(note *model.Note, entity *NoteEntity) {
	if r == nil || note == nil || entity == nil || len(note.Reactions) == 0 {
		return
	}
	out := make(map[string]string)
	for _, pair := range collectReactionEmojiNames(note.Reactions) {
		if url, ok := r.cache[pair.name+"@"+pair.host]; ok {
			out[reactionEmojiKey(pair.name, pair.host)] = url
		}
	}
	if len(out) > 0 {
		entity.ReactionEmojis = out
	}
}

// collectReactionEmojiNames decodes the per-note Reactions JSON map and
// returns custom-emoji entries as a slice of (name, host) pairs. Raw
// unicode reactions are filtered out. Same-name-different-host pairs
// are preserved as separate entries (a name-keyed map would collapse
// them and lose remote emoji URLs).
func collectReactionEmojiNames(raw []byte) []emojiNameHost {
	if len(raw) == 0 {
		return nil
	}
	var counts map[string]int
	if err := json.Unmarshal(raw, &counts); err != nil {
		return nil
	}
	var out []emojiNameHost
	for reaction := range counts {
		name, host, ok := parseCustomEmojiReaction(reaction)
		if !ok {
			continue
		}
		out = append(out, emojiNameHost{name: name, host: host})
	}
	return out
}

// PopulateUserEmojis resolves emoji names stored in user.Emojis to URLs
// and sets lite.Emojis.
func (r *EmojiResolver) PopulateUserEmojis(user *model.User, lite *UserLite) {
	if r == nil || user == nil || lite == nil || len(user.Emojis) == 0 {
		return
	}
	host := ""
	if user.Host != nil {
		host = *user.Host
	}
	emojis := make(map[string]string, len(user.Emojis))
	for _, name := range user.Emojis {
		if url, ok := r.cache[name+"@"+host]; ok {
			emojis[name] = url
		}
	}
	if len(emojis) > 0 {
		lite.Emojis = emojis
	}
}
