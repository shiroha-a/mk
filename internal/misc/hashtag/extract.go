// Package hashtag provides hashtag extraction from note text.
//
// Misskey TS (mfm-js) は MFM パーサで text を構造化してから hashtag
// ノードを取り出す。mk-go も同等の MFM パーサ (internal/activitypub/mfm)
// を持つので、本パッケージはそれに hashtag 抽出を委譲し、note.tags 固有の
// 正規化 (長さ truncate / 大文字小文字 dedup) だけを担う。連合経由で受け
// 取る ActivityPub Object には別途 tag 配列が付くので、本パッケージは
// local note 作成時の text/cw 由来 hashtag だけを担当する。
package hashtag

import (
	"strings"
	"unicode/utf8"

	"github.com/shiroha-a/mk/internal/activitypub/mfm"
	"github.com/shiroha-a/mk/internal/misc/searchnorm"
)

// MaxUserTags は user.tags に格納する hashtag の最大件数。upstream
// (i/update / ApPersonService) の `.splice(0, 32)` と一致させる。
const MaxUserTags = 32

// MaxNoteTags は note.tags に格納する hashtag の最大件数。upstream
// NoteCreateService の `.splice(0, 32)` と一致させる。
const MaxNoteTags = 32

// MaxTagLength は note.tags 列の varchar(128) 制約に合わせた tag 長
// 上限。これを超える tag は truncate される (drop ではなく trim にする
// のは Misskey TS の挙動互換)。
const MaxTagLength = 128

// Extract pulls hashtag names out of text fragments (typically the note's
// body and CW). Extraction goes through the full MFM parser, so hashtags
// inside code blocks, URLs, links and mentions are correctly excluded (the
// previous implementation used a regex scan with code-block / URL masking).
//
// Order is preserved by first occurrence; case-insensitive duplicates collapse
// to a single entry with the first-seen original case kept (Misskey TS も同じ
// 挙動)。Empty / whitespace-only inputs return nil. Tags longer than
// MaxTagLength are truncated.
func Extract(parts ...string) []string {
	tags := mfm.CollectHashtags(parts...)
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if len(tag) > MaxTagLength {
			tag = tag[:MaxTagLength]
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ExtractNoteTags extracts hashtags from the given text fragments and returns
// them in the form stored in note.tags: filtered to <=128 code points (dropped,
// not truncated), capped at MaxNoteTags, and normalized via searchnorm.Normalize
// (NFKC + lowercase), de-duplicated. Mirrors upstream NoteCreateService
// (`extractHashtags(...).filter(t => Array.from(t).length <= 128).splice(0, 32)`
// then `tags.map(normalizeForSearch)` on store, #1948-18). search-by-tag は同じ
// normalizeForSearch を query に適用するため、stored 値と一致する。
func ExtractNoteTags(parts ...string) []string {
	return NormalizeNoteTags(mfm.CollectHashtags(parts...))
}

// NormalizeNoteTags applies the note.tags store normalization to a pre-extracted
// tag list (used for AP-received apHashtags where the tags come from the remote
// Object rather than MFM extraction). >128 code-point tags are dropped, the list
// is capped at MaxNoteTags, each is normalized, and duplicates collapse (#1948-18).
func NormalizeNoteTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	// upstream の順序 (NoteCreateService.ts:581,632) は raw dedup(extractHashtags の
	// unique) → filter(<=128 code point) → splice(0,32) → map(normalizeForSearch)。
	// 順序が重要: cap を normalize より**前**に効かせないと、case/width 衝突で空いた
	// cap slot に upstream が落とす tag が残り、>32 tag note で stored tags がずれる
	// (#1948-18)。よって RAW のまま dedup + 128filter + 32cap してから normalize する。
	rawSeen := make(map[string]struct{}, len(tags))
	capped := make([]string, 0, MaxNoteTags)
	for _, tag := range tags {
		if utf8.RuneCountInString(tag) > MaxTagLength {
			continue
		}
		if _, dup := rawSeen[tag]; dup {
			continue
		}
		rawSeen[tag] = struct{}{}
		capped = append(capped, tag)
		if len(capped) >= MaxNoteTags {
			break
		}
	}
	// normalize して post-normalize の clean dedup を行う。upstream は normalize 後の
	// duplicate (例 'Misskey'/'misskey' → ['misskey','misskey']) をそのまま格納するが、
	// `@> ARRAY[]` 検索・trends 集計は duplicate に非感応なので clean array にする
	// (surviving source tag 集合は cap を先に効かせたので upstream と一致)。
	normSeen := make(map[string]struct{}, len(capped))
	out := make([]string, 0, len(capped))
	for _, tag := range capped {
		n := searchnorm.Normalize(tag)
		if n == "" {
			continue
		}
		if _, ok := normSeen[n]; ok {
			continue
		}
		normSeen[n] = struct{}{}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ExtractUserTags extracts hashtags from the given text fragments and returns
// them in the form stored in user.tags: normalized via searchnorm.Normalize
// (NFKC + lowercase), de-duplicated, and capped at MaxUserTags. This mirrors
// Misskey's i/update / ApPersonService
// (`extractHashtags(...).map(normalizeForSearch).splice(0, 32)`), so the stored
// values match the normalized tag used by the hashtags/users containment query.
//
// local の profile description / remote の AP person.tag (Hashtag entry を
// "#tag" 文字列化したもの) のどちらを渡しても同じ正規化結果になる。
func ExtractUserTags(parts ...string) []string {
	raw := Extract(parts...)
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, tag := range raw {
		n := searchnorm.Normalize(tag)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
		if len(out) >= MaxUserTags {
			break
		}
	}
	return out
}
