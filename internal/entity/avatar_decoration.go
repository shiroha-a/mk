package entity

import (
	"encoding/json"
	"sync"
)

// AvatarDecorationLookup resolves avatar decoration ids to their public URL
// (the static asset that the frontend renders on top of an avatar). Returns
// ok=false when the id is not in the catalog (e.g. admin deleted it after
// users装着済み) so the entry can be filtered out — mirrors upstream
// UserEntityService.pack which does .filter(...some(...)) before mapping.
type AvatarDecorationLookup interface {
	LookupURL(id string) (url string, ok bool)
}

// avatarDecorationLookup is process-wide so PackUserLite can enrich without
// changing its signature (called from 100+ sites). Router wires this once at
// startup via SetAvatarDecorationLookup. nil disables enrichment, which keeps
// existing tests that don't bother wiring the lookup green (the response will
// simply omit url, identical to pre-#521 behaviour).
var (
	avatarDecorationLookupMu sync.RWMutex
	avatarDecorationLookup   AvatarDecorationLookup
)

// SetAvatarDecorationLookup wires the catalog used by PackUserLite to embed
// `url` on each avatarDecorations entry. Pass nil to clear (used in tests).
func SetAvatarDecorationLookup(l AvatarDecorationLookup) {
	avatarDecorationLookupMu.Lock()
	avatarDecorationLookup = l
	avatarDecorationLookupMu.Unlock()
}

// EmojiDecorationLookup resolves a decoration entry that points at a local
// custom emoji instead of the admin-managed catalog (#2975). It must only
// return emojis that are still usable as a decoration — local (host IS NULL)
// and not sensitive — so that flipping `emoji.isSensitive` or deleting the row
// removes the decoration from every profile without a separate cleanup pass.
//
// name は**現在の名前**を返す。保存済みの jsonb は装着した時点の名前を持つが、
// `admin/emoji/update` は名前を変えられるので、そちらを出すと古い名前が残る。
type EmojiDecorationLookup interface {
	LookupEmojiDecoration(emojiID string) (url string, name string, ok bool)
}

// emojiDecorationLookup mirrors avatarDecorationLookup: process-wide so
// PackUserLite can resolve without a signature change. Router wires this once
// at startup via SetEmojiDecorationLookup.
var (
	emojiDecorationLookupMu sync.RWMutex
	emojiDecorationLookup   EmojiDecorationLookup
)

// SetEmojiDecorationLookup wires the resolver used by PackUserLite for
// emoji-backed avatarDecorations entries. Pass nil to clear (used in tests).
//
// **nil のときは絵文字由来のエントリを落とす** (fail-closed)。カタログ由来は
// 未配線でも `url` 抜きで残すが、こちらは「まだセンシティブでないか」を
// 判断する術がないので、配線されていない環境で出すわけにいかない。
func SetEmojiDecorationLookup(l EmojiDecorationLookup) {
	emojiDecorationLookupMu.Lock()
	emojiDecorationLookup = l
	emojiDecorationLookupMu.Unlock()
}

// AvatarDecorationItem is the per-entry shape sent to clients. Mirrors
// upstream Misskey's UserEntityService output: {id, angle, flipH, offsetX,
// offsetY, url}. `url` is resolved from the avatar_decoration catalog.
//
// angle / flipH / offsetX / offsetY は upstream UserEntityService が
// `ud.angle || undefined` 等で falsy 値を省くため (#1781)、omitempty で
// 0 / false のとき出力しない (json-schema 上 optional)。id / url は常に出す。
type AvatarDecorationItem struct {
	ID      string  `json:"id"`
	Angle   float64 `json:"angle,omitempty"`
	FlipH   bool    `json:"flipH,omitempty"`
	OffsetX float64 `json:"offsetX,omitempty"`
	OffsetY float64 `json:"offsetY,omitempty"`
	URL     string  `json:"url"`
	// Scale shrinks the decoration (#2975). mk-go 独自の additive field で、
	// upstream には無い。**省略は 1 (= upstream と同じ大きさ)** なので、
	// 既定のままの要素には出ない。`MkAvatar` の `.decoration` はアバターの
	// 2 倍の枠に描くため、余白を持たないカスタム絵文字は既定だとアイコンを
	// 覆ってしまう。**縮小しか表現しない** (1 を超える値は API が弾く)。
	Scale *float64 `json:"scale,omitempty"`
	// EmojiName is set only for emoji-backed decorations (#2975). mk-go 独自の
	// additive field で、upstream には無い。クライアントは `url` だけで描画
	// できるので、これは「絵文字由来である」ことを伝えるためのもの
	// (設定画面が名前を出し、再編集のときに同じ絵文字を送り直せる)。
	EmojiName string `json:"emojiName,omitempty"`
}

// resolveAvatarDecorations parses the raw `user.avatarDecorations` jsonb bytes
// into enriched items. Entries whose id no longer exists in the catalog are
// silently dropped (TS upstream does the same — a deleted decoration must not
// keep rendering on user profiles). Returns an empty slice (not nil) so the
// JSON output is `[]` rather than `null`, which is what the frontend expects.
//
// 各 item の `url` は remote origin なら media proxy 経由へ書き換える (#1529)。
// frontend の MkAvatar は decoration.url を <img src> へ直接載せるため。
func resolveAvatarDecorations(raw []byte) []AvatarDecorationItem {
	out := []AvatarDecorationItem{}
	if len(raw) == 0 {
		return out
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) == 0 {
		return out
	}
	avatarDecorationLookupMu.RLock()
	lookup := avatarDecorationLookup
	avatarDecorationLookupMu.RUnlock()
	emojiDecorationLookupMu.RLock()
	emojiLookup := emojiDecorationLookup
	emojiDecorationLookupMu.RUnlock()
	for _, r := range rows {
		idVal, _ := r["id"].(string)
		if idVal == "" {
			continue
		}
		item := AvatarDecorationItem{ID: idVal}
		if v, ok := r["angle"].(float64); ok {
			item.Angle = v
		}
		if v, ok := r["flipH"].(bool); ok {
			item.FlipH = v
		}
		if v, ok := r["offsetX"].(float64); ok {
			item.OffsetX = v
		}
		if v, ok := r["offsetY"].(float64); ok {
			item.OffsetY = v
		}
		// **既定 (1) では出さない。** 既存の行は `scale` を持たないので、
		// 無い = 1 として扱う。値が 1 のときも出さないことで、クライアントから
		// 見た shape を「既定なら upstream と同一」に保つ。
		if v, ok := r["scale"].(float64); ok && v != 1 {
			scale := v
			item.Scale = &scale
		}
		// 絵文字由来のエントリ (#2975) は catalog ではなく絵文字側で解決する。
		// **判別子は `emojiName` の有無**で、`id` の意味は変えていない
		// (絵文字由来なら `emoji` 行の id を指す)。
		if storedName, _ := r["emojiName"].(string); storedName != "" {
			if emojiLookup == nil {
				// 未配線では「まだセンシティブでないか」を判断できないので
				// 落とす (fail-closed)。catalog 側と扱いが違うのは、あちらが
				// url を欠くだけなのに対し、こちらは出すこと自体が判断を要する
				// ため。
				continue
			}
			url, name, ok := emojiLookup.LookupEmojiDecoration(idVal)
			if !ok {
				// 削除された / センシティブになった / リモートに変わった絵文字は
				// ここで消える。catalog 由来の silent drop と同じ形。
				continue
			}
			// catalog 由来と同じく proxy 経由へ書き換える (#1529)。ローカル
			// 絵文字なので通常は no-op (自オリジン / object storage) だが、
			// 判定材料を 1 箇所に揃えておく。
			item.URL = ProxyMediaURL(url)
			// 保存済みの名前ではなく**現在の名前**を出す (rename 追従)。
			item.EmojiName = name
			out = append(out, item)
			continue
		}
		if lookup != nil {
			url, ok := lookup.LookupURL(idVal)
			if !ok {
				// catalog から消えた decoration はクライアント側で render
				// できないので silent drop。upstream TS も同じ filter を行う。
				continue
			}
			// **admin 設定の URL は remote origin を指せる。** frontend の
			// MkAvatar は decoration.url を <img src> へ直接載せるので、生 URL を
			// 返すと閲覧者の IP が相手サーバーへ渡り、CSP enforce の構成では
			// 画像が消える。静止画設定時の getStaticImageUrl も、proxy 済みなら
			// sig を保ったまま static=1 を足せる (生 URL のままだと allowlist に
			// avatar_decoration.url が無く 403 + max-age=86400)。
			item.URL = ProxyMediaURL(url)
		}
		out = append(out, item)
	}
	return out
}
