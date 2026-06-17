// Package reactionlegacy holds the frozen legacy text-reaction alias table
// shared by the reaction service (core/reaction) and the note entity packer
// (internal/entity). It lives in its own low-level package so the entity layer
// can reference it without importing core/reaction (which would create an
// import cycle, since core/reaction transitively depends on internal/entity).
//
// 本テーブルは pre-2018 の text reaction (like / love 等) を Unicode 絵文字へ
// 写像する。Misskey 本家 ReactionService の `legacies` 表と同一で、upstream 側
// でも凍結済み (新規 entry は追加されない) のため共有定数として切り出している。
package reactionlegacy

// legacies maps a legacy text reaction like "like" or "love" to its Unicode
// equivalent. Mirrors upstream ReactionService `legacies` (ReactionService.ts).
// Frozen: do not add entries — new reactions use custom emoji, not text keys.
var legacies = map[string]string{
	"like":     "👍",
	"love":     "❤", // ハート、異体字セレクタを入れない
	"laugh":    "😆",
	"hmm":      "🤔",
	"surprise": "😮",
	"congrats": "🎉",
	"angry":    "💢",
	"confused": "😥",
	"rip":      "😇",
	"pudding":  "🍮",
	"star":     "⭐",
}

// Convert maps a legacy text reaction (e.g. "like") to its Unicode equivalent.
// ok is false when key is not a legacy alias, in which case the caller should
// leave the reaction unchanged.
func Convert(key string) (string, bool) {
	v, ok := legacies[key]
	return v, ok
}
