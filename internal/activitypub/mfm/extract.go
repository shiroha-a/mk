package mfm

// CollectEmojiCodes parses each input as MFM and returns the union of
// custom emoji names (from :code: tokens) found across all texts, dedup'd
// and ordered by first appearance.
//
// 用途: note 作成 / user プロフィール更新時に text + cw 等を渡して
// note.Emojis / user.Emojis に格納する emoji 名一覧を得る (#629)。
// 受信側 (federation/resolver) は AP Tag から拾うが、送信側はこちらで
// MFM AST を walk して拾う。
//
// 返り値の各要素は \`:\` を含まない bare name (例: "foo")。Empty input は
// nil を返す。
// CollectHashtags parses each input as MFM and returns the hashtag tags
// (from #tag tokens) found across all texts, ordered by first appearance and
// dedup'd exactly. Because extraction goes through the full MFM parser,
// hashtags inside code blocks, URLs, links and mentions are correctly excluded
// — unlike a flat regex scan.
//
// 返り値は `#` を含まない bare tag (例: "golang")。Empty input は nil を返す。
// case-insensitive dedup / 長さ truncation などの呼び出し側固有の正規化は
// 行わない (hashtag.Extract がそれを担う)。
func CollectHashtags(texts ...string) []string {
	if len(texts) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Type == NodeHashtag {
			if tag, ok := n.Props["hashtag"].(string); ok && tag != "" {
				if _, dup := seen[tag]; !dup {
					seen[tag] = struct{}{}
					out = append(out, tag)
				}
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, t := range texts {
		if t == "" {
			continue
		}
		for _, n := range Parse(t) {
			walk(n)
		}
	}
	return out
}

func CollectEmojiCodes(texts ...string) []string {
	if len(texts) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Type == NodeEmojiCode {
			if name, ok := n.Props["name"].(string); ok && name != "" {
				if _, dup := seen[name]; !dup {
					seen[name] = struct{}{}
					out = append(out, name)
				}
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, t := range texts {
		if t == "" {
			continue
		}
		for _, n := range Parse(t) {
			walk(n)
		}
	}
	return out
}
