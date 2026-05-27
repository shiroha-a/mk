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

	"github.com/shiroha-a/mk/internal/activitypub/mfm"
	"github.com/shiroha-a/mk/internal/misc/searchnorm"
)

// MaxUserTags は user.tags に格納する hashtag の最大件数。upstream
// (i/update / ApPersonService) の `.splice(0, 32)` と一致させる。
const MaxUserTags = 32

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
