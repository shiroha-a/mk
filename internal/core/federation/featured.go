package federation

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/model"
)

// featuredPinLimit bounds how many pinned notes we import from a remote actor's
// featured collection. upstream ApPersonService.updateFeatured の `.slice(0, 5)`
// と同値。
const featuredPinLimit = 5

// featuredScanLimit bounds how many collection entries we look at.
//
// upstream は items を**全件**解決してから Note に絞り、先頭 5 件を採る。取得
// 回数が相手の申告する件数で決まるので、巨大なコレクションを置くだけで取得を
// 増幅させられる。得られるピン留めは同じなので、走査する件数の方に上限を置く。
const featuredScanLimit = 50

// featuredNoteResolveDepth is the note-resolution depth featured imports start
// at.
//
// **0 で始めてはいけない。** ノート解決の内側で新しい actor が作られたとき、
// depth 0 だと「通常の配送で初めて観測した」場合と区別できず、その actor の
// featured 取り込みが再び走る。1 段ごとに featuredPinLimit 分岐するので、
// 深さが取れると取得が指数的に膨らむ (#2552)。
const featuredNoteResolveDepth = 1

// updateFeatured imports the notes advertised by a remote actor's `featured`
// collection into `user_note_pining`.
//
// Mirrors upstream ApPersonService.updateFeatured: リモートユーザーのみ、
// featured が宣言されている場合のみ、Collection / OrderedCollection の先頭から
// Note を featuredPinLimit 件まで取り込み、既存のピン留めを置き換える。
//
// **best-effort。** ここでの失敗が actor の取得そのものを巻き戻すことは無い
// (upstream も `.catch()` でログのみ)。
func (r *Resolver) updateFeatured(user *model.User) {
	if r.pinningRepo == nil || r.pinningIDGen == nil {
		return
	}
	if user == nil || user.Host == nil || user.Featured == nil || *user.Featured == "" {
		return
	}
	featured := *user.Featured

	// featured の URL は actor 文書の中の申告値なので、別ホストを指せる。
	// **ここで縛らないと、他人のサーバーのコレクションを自分のピン留めとして
	// 取り込ませられる。**
	if user.URI == nil || *user.URI == "" {
		return
	}
	if err := assertRequestHostMatches(*user.URI, featured); err != nil {
		slog.Warn("federation: featured collection host mismatch",
			"userId", user.ID, "featured", featured, "err", err)
		return
	}
	if !r.hostAllowedForURI(featured) {
		return
	}

	items, ok := r.fetchFeaturedItems(featured)
	if !ok {
		// 取得や解釈に失敗したときは既存のピン留めを触らない。**空のコレク
		// ションと区別せずに置き換えると、相手が一時的に落ちているだけで
		// ピン留めが消える。**
		return
	}

	noteIDs := r.resolveFeaturedNotes(user, items)
	pins := make([]*model.UserNotePining, 0, len(noteIDs))
	now := r.clock()
	for i, noteID := range noteIDs {
		// user_note_pining は id の降順で読まれる。コレクションの並びを保つ
		// ため、後ろの要素ほど古い時刻で採番する (upstream の `td -= 1000`
		// と同じ手)。
		pins = append(pins, &model.UserNotePining{
			ID:     r.pinningIDGen.Generate(now.Add(-time.Duration(i+1) * time.Second)),
			UserID: user.ID,
			NoteID: noteID,
		})
	}
	if err := r.pinningRepo.ReplaceByUser(user.ID, pins); err != nil {
		slog.Warn("federation: failed to store featured pins",
			"userId", user.ID, "count", len(pins), "err", err)
		return
	}
	slog.Debug("federation: imported featured pins", "userId", user.ID, "count", len(pins))
}

// fetchFeaturedItems fetches the featured collection and returns its raw
// entries. ok=false は「取得または解釈に失敗した」で、空コレクション (items 0
// 件) とは区別する。
func (r *Resolver) fetchFeaturedItems(uri string) ([]json.RawMessage, bool) {
	body, err := r.fetcher.FetchObject(uri)
	if err != nil {
		slog.Warn("federation: failed to fetch featured collection", "uri", uri, "err", err)
		return nil, false
	}
	var col struct {
		// APType は `"type": ["OrderedCollection"]` を受ける。string 決め打ちだと
		// collection ごと unmarshal に失敗して featured の取り込みが丸ごと落ちる
		// (この経路は Normalize を通らない生 fetch、#2662)。
		Type activitypub.APType `json:"type"`
		// APRawList は単一 object も 1 件として拾う。`[]json.RawMessage`
		// 決め打ちだと、単一 object や無関係な片方のスカラーだけで
		// **collection ごと unmarshal に失敗し、その actor の featured
		// 取り込みが丸ごと落ちる** (下の error 分岐で `nil, false`。既存の
		// ピンは温存される、#2662)。
		Items        activitypub.APRawList `json:"items"`
		OrderedItems activitypub.APRawList `json:"orderedItems"`
	}
	if err := json.Unmarshal(body, &col); err != nil {
		slog.Warn("federation: featured collection is not a JSON object", "uri", uri, "err", err)
		return nil, false
	}
	// upstream は type に応じて片方だけ読む (Collection→items /
	// OrderedCollection→orderedItems)。Collection / OrderedCollection 以外は
	// 受け付けない。
	switch strings.ToLower(col.Type.String()) {
	case "collection":
		return col.Items, true
	case "orderedcollection":
		return col.OrderedItems, true
	}
	slog.Warn("federation: featured is not a collection", "uri", uri, "type", col.Type.String())
	return nil, false
}

// resolveFeaturedNotes turns collection entries into local note IDs, in order.
func (r *Resolver) resolveFeaturedNotes(user *model.User, items []json.RawMessage) []string {
	actorURI := *user.URI
	noteIDs := make([]string, 0, featuredPinLimit)
	seen := make(map[string]bool, featuredPinLimit)
	scanned := 0
	for _, item := range items {
		if len(noteIDs) >= featuredPinLimit || scanned >= featuredScanLimit {
			break
		}
		scanned++
		uri, apType := featuredItemRef(item)
		if uri == "" {
			continue
		}
		// inline で type が分かるものは、取得する前に落とす。URI だけの要素は
		// 型が分からないので、解決してみて Note にならなければ捨てる。
		if apType != "" && !strings.EqualFold(apType, "note") {
			continue
		}
		// コレクションに他ホストの URI を混ぜてこられる。相手のホストに限る。
		// **`user.Host` と文字列比較しない。** IDN は保存形と URI 中の表記が
		// 揃うとは限らないので、featured の URL と同じ punycode 対応の比較を使う。
		if err := assertRequestHostMatches(actorURI, uri); err != nil {
			continue
		}
		note, err := r.resolveNoteDepth(uri, featuredNoteResolveDepth, false, false)
		if err != nil || note == nil {
			continue
		}
		// **ピン留めできるのは自分の投稿だけ** (upstream の i/pin も同じ)。
		// これを見ないと、他人の投稿を自分のプロフィールに並べられる。
		// upstream updateFeatured は著者を見ないので、そのぶん厳しい。
		if note.UserID != user.ID {
			continue
		}
		if seen[note.ID] {
			continue
		}
		seen[note.ID] = true
		noteIDs = append(noteIDs, note.ID)
	}
	return noteIDs
}

// featuredItemRef extracts the URI and (when inlined) the AP type of a
// collection entry. 要素は URI 文字列か、埋め込まれたオブジェクトのどちらか。
func featuredItemRef(item json.RawMessage) (uri, apType string) {
	var asString string
	if err := json.Unmarshal(item, &asString); err == nil {
		return asString, ""
	}
	var obj struct {
		ID   string          `json:"id"`
		Type json.RawMessage `json:"type"`
	}
	if err := json.Unmarshal(item, &obj); err != nil {
		return "", ""
	}
	return obj.ID, singleAPType(obj.Type)
}

// singleAPType reads an AP `type`, which may be a string or an array of them.
func singleAPType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// **他の type 判定 (`activitypub.TypeOf` / `APType` / `apTypeOf` /
	// `flattenType`) と同じく「先頭要素が string ならそれ」にする。**
	// `[]string` への一括 unmarshal だと `["Note", 42]` で空になり、
	// head 方式との差が生まれる (#2662)。
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return activitypub.TypeOf(v)
}
