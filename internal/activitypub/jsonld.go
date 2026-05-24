package activitypub

import (
	"encoding/json"
	"strings"
)

// Note on scope:
//
// piprate/json-gold によるフル JSON-LD パイプラインは、独自の DocumentLoader と
// Compact 出力の予測しにくさが欠点で、厳密な struct タグ駆動のパースとは相性が
// 悪い。ここでは Misskey 本家相当の "context-aware な軽量正規化" を実装し、
// 受信 JSON のキー名を canonical な短形式へ揃える。これにより既存の json.
// Unmarshal-based dispatcher を維持したまま、Mastodon 系の prefix 付きキーや
// IRI 直記述、type の配列形式、JSON-LD 言語マップを処理できる。

// asPrefix is the canonical IRI prefix for ActivityStreams 2.0 terms.
const asPrefix = "https://www.w3.org/ns/activitystreams#"

// secPrefix is the canonical IRI prefix for the W3C security vocabulary used
// by HTTP Signatures.
const secPrefix = "https://w3id.org/security#"

// canonicalTerms maps JSON-LD aliases / IRIs to short names that match the
// json struct tags used elsewhere in the package. キーは小文字化せずそのまま
// 比較する。
var canonicalTerms = map[string]string{
	// AS short forms (no-op, listed for completeness so the lookup works for
	// both directions).
	"type":                      "type",
	"actor":                     "actor",
	"object":                    "object",
	"target":                    "target",
	"to":                        "to",
	"cc":                        "cc",
	"bcc":                       "bcc",
	"id":                        "id",
	"name":                      "name",
	"summary":                   "summary",
	"content":                   "content",
	"inReplyTo":                 "inReplyTo",
	"published":                 "published",
	"updated":                   "updated",
	"sensitive":                 "sensitive",
	"attributedTo":              "attributedTo",
	"inbox":                     "inbox",
	"outbox":                    "outbox",
	"followers":                 "followers",
	"following":                 "following",
	"endpoints":                 "endpoints",
	"sharedInbox":               "sharedInbox",
	"preferredUsername":         "preferredUsername",
	"publicKey":                 "publicKey",
	"publicKeyPem":              "publicKeyPem",
	"owner":                     "owner",
	"href":                      "href",
	"url":                       "url",
	"icon":                      "icon",
	"image":                     "image",
	"tag":                       "tag",
	"attachment":                "attachment",
	"manuallyApprovesFollowers": "manuallyApprovesFollowers",
	"discoverable":              "discoverable",

	// "as:" prefix.
	"as:type":              "type",
	"as:actor":             "actor",
	"as:object":            "object",
	"as:target":            "target",
	"as:to":                "to",
	"as:cc":                "cc",
	"as:bcc":               "bcc",
	"as:name":              "name",
	"as:summary":           "summary",
	"as:content":           "content",
	"as:inReplyTo":         "inReplyTo",
	"as:published":         "published",
	"as:updated":           "updated",
	"as:sensitive":         "sensitive",
	"as:attributedTo":      "attributedTo",
	"as:inbox":             "inbox",
	"as:outbox":            "outbox",
	"as:followers":         "followers",
	"as:following":         "following",
	"as:endpoints":         "endpoints",
	"as:sharedInbox":       "sharedInbox",
	"as:preferredUsername": "preferredUsername",
	"as:url":               "url",
	"as:icon":              "icon",
	"as:image":             "image",
	"as:tag":               "tag",
	"as:attachment":        "attachment",

	// Security vocab prefix.
	"sec:publicKey":    "publicKey",
	"sec:publicKeyPem": "publicKeyPem",
	"sec:owner":        "owner",
}

// Normalize re-encodes a JSON-LD activity body so that AS terms appear in
// their canonical short form. 入力が JSON object でも JSON 配列でも受け付け、
// 不明なキー / scalar はそのまま透過する。
func Normalize(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	normalized := normalizeValue(raw)
	return json.Marshal(normalized)
}

// normalizeValue is the recursive worker that transforms map keys and unwraps
// JSON-LD specific value containers (`@value`, type arrays, etc.).
func normalizeValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		// `{"@value": "foo"}` のような JSON-LD value object はスカラーに展開する。
		// `@language` などの annotation は捨てる (本実装は単一言語のみ扱う)。
		if val, ok := x["@value"]; ok {
			return normalizeValue(val)
		}
		// `{"@id": "..."}` を持つ object は実質的に IRI 参照なので id 文字列に
		// 縮約する。これにより `"object": {"@id": "https://..."}` のような形式
		// が string と同等に扱える。
		if id, ok := x["@id"]; ok && len(x) == 1 {
			if s, ok := id.(string); ok {
				return s
			}
		}
		out := make(map[string]any, len(x))
		for k, child := range x {
			canonical, ok := canonicalKey(k)
			if !ok {
				switch k {
				case "@id":
					// @id 単独 unwrap には引っかからなかったので id として残す。
					canonical = "id"
				case "@type":
					// type 配列の処理は下の flattenType に任せる。
					canonical = "type"
				case "@context":
					// 標準 JSON-LD context (array / object / AS context URL) は後段
					// dispatcher が使わないので破棄する。ただし CherryPick group chat
					// は note の @context に chat room URI を string で載せる規約なので、
					// room URI の場合のみ保持して下流 (handleChatRoomMessageCreate) が
					// room を識別できるようにする (#1209)。
					if s, ok := child.(string); ok && strings.Contains(s, "/chat/rooms/") {
						out["@context"] = s
					}
					continue
				case "@value", "@language", "@graph", "@list":
					// 後段 dispatcher が使わない reserved keyword は破棄する。
					continue
				default:
					// 未知キーはそのまま残す (Misskey 拡張など unknown vocabulary
					// が落ちないようにする)。
					canonical = k
				}
			}
			normalizedChild := normalizeValue(child)
			// type が配列で来た場合は最初の AS type 文字列を採用する。
			if canonical == "type" {
				if s := flattenType(normalizedChild); s != "" {
					out[canonical] = s
					continue
				}
			}
			out[canonical] = normalizedChild
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = normalizeValue(item)
		}
		return out
	default:
		return x
	}
}

// canonicalKey resolves a JSON property name to its canonical short form.
// 戻り値の bool は「マッピングが見つかったか」を示す。
func canonicalKey(k string) (string, bool) {
	if v, ok := canonicalTerms[k]; ok {
		return v, true
	}
	// AS IRI の直記述 ("https://www.w3.org/ns/activitystreams#type") を短縮。
	if local, ok := strings.CutPrefix(k, asPrefix); ok && local != "" {
		if v, ok := canonicalTerms[local]; ok {
			return v, true
		}
		return local, true
	}
	if local, ok := strings.CutPrefix(k, secPrefix); ok && local != "" {
		if v, ok := canonicalTerms["sec:"+local]; ok {
			return v, true
		}
		return local, true
	}
	return "", false
}

// flattenType reduces a possibly-array AS type into a single string. 配列の
// 場合は最初の文字列要素を採用する。文字列以外は空を返す。
func flattenType(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
