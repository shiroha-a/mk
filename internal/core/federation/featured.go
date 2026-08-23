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
		// **いま取り込み中の note は解決しに行かない。** 著者が自分の投稿を
		// ピン留めしていると、ここが ResolveNote(A) の内側から同じ A を要求する
		// 形になり、note の singleflight が**自分が握っている in-flight entry を
		// 自分で待つ**状態になって永久に止まる (#2684)。quote 側は同じ形の
		// guard を既に持っている (resolveQuoteURI、#1527)。
		//
		// **2 つの台帳を両方見る。** どちらか片方では取りこぼす:
		//   - resolvingNotes は singleflight の鍵 (取得 URI)。resolveNoteOnce で
		//     しか書かれないので、**inbox 直送では空**
		//   - ingesting は正規化後の document id。inbox 直送でも立つが、
		//     **取得 URI と id が食い違う別名 URL では引けない** (#2686)
		//
		// **skip する前に既存行を引く。** ReplaceByUser は delete-then-insert
		// なので、ここで落とすと集合ごと書き直して**生きているピンが消える**。
		// 引くのは取得 URI と document id の両方 — 行は document id で保存
		// されるので、別名 URL では取得 URI だけでは必ず空振りする
		// (#2684 review MED-1)。行が現れる経路は、取り込み中の Create が
		// 台帳の区間内で走る場合と、inbox 直送の IngestNoteWithCreated
		// (台帳を触らない) が別 goroutine で先に作る場合。
		//
		// 既存行が無ければこの回のピンは取りこぼす。外側の ingest が終わってから
		// 追加する手立てが無いため。次の actor 更新で拾い直される (テスト済み)。
		// ただし actor TTL は既定 24 時間なので、その間は欠けたままになる。
		//
		// **upstream とは挙動が違う。** upstream は Promise.all で全件を
		// まとめて解決し、Resolver.history に当たった 1 件が throw すると
		// updateFeatured ごと reject して既存のピン集合をそのまま残す
		// (all-or-nothing)。mk-go は 1 件だけ落として残りを反映する。
		// 取り込める分を取り込むほうが実害が小さいのでこちらを維持する
		// (docs/divergence.md)。
		//
		// 鍵の flag を (false, false) に固定しているのは、**すぐ下の
		// resolveNoteDepth に同じ値を渡している**から (`featuredNoteResolveDepth`
		// は深さであって flag ではない)。他の鍵形は次のとおり:
		//
		//   - ephemeral 鍵 (`eph\0…`): **updateFeatured に到達しない**。
		//     resolveNoteAuthor が ephemeral を resolveActorEphemeral
		//     (skipFeatured=true) へ回し、resolveActorOnceWithID 側も
		//     !ephemeral で守っている。鍵が別という以前に経路が無い
		//   - ap/show の cross-host 鍵 (`xhost\0…`): resolvingNotes では引けない
		//     (鍵の形が違う) が、**ingesting 側で引ける**ので二重 fetch は起きない
		//     (#2686 で 2 回 → 1 回になった)。代わりにそのピンはこの回では入らず、
		//     次の actor 更新まで遅れる
		//   - inbox 直送 (`IngestNoteWithCreated`) は resolveNoteOnce を通らない
		//     ので resolvingNotes に載らない。そちらは ingesting (document id 側)
		//     で見る。見落とすと**同じ note をもう一度 fetch して内側の ingest が
		//     先に行を作り**、外側の Create が UNIQUE に当たって dedup 経路へ落ち、
		//     created=false になる。呼び出し側はそれで通知とチャートのフックを
		//     飛ばすので、**言及・返信の通知が黙って消える** (#2686)。
		//
		// **別名 URL では inbox 直送側を取りこぼす。** featured が
		// `/@user/x` を載せていて document の id が `/notes/x` のとき、
		// ingesting は id 側の鍵なので引けない。ここで docID を得るには
		// fetch するしかなく、それは呼び出し順を変える話になるので分けた
		// (#2695)。実測では正規形が created=false → true に直り、別名は
		// created=false のまま。
		var note *model.Note
		if inflight := r.noteIngestInFlight(uri); inflight {
			// ingesting は document id 側の鍵なので、引くのも同じ URI でよい。
			note = r.noteByAnyURI(uri, uri)
			if note == nil {
				continue
			}
		} else if docID, inflight := r.noteResolveInFlight(uri, false, false); inflight {
			note = r.noteByAnyURI(uri, docID)
			if note == nil {
				continue
			}
		} else {
			resolved, err := r.resolveNoteDepth(uri, featuredNoteResolveDepth, false, false)
			if err != nil || resolved == nil {
				continue
			}
			note = resolved
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
