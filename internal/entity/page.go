package entity

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// PackPage converts a model.Page into the map shape returned by /api/pages/*
// and embedded as UserDetailed.pinnedPage. Returns nil when p is nil so
// callers can assign the result directly to a nilable field.
// When an idGen is supplied, createdAt is derived from the aidx ID and
// rendered in the same "2006-01-02T15:04:05.000Z" format used by other
// entity packers (PackNote / PackDriveFile etc). updatedAt is rendered with
// the same format for consistency.
//
// Use PackPageWithContext when the caller can supply the page owner +
// per-viewer state — list endpoints (/api/i/pages, /api/users/pages,
// /api/pages/featured) and the page detail (/api/pages/show) all need
// `page.user` populated or the frontend MkPagePreview / page.vue templates
// fail to render (`page.user.username` is unconditional).
func PackPage(p *model.Page, idGens ...id.Generator) map[string]any {
	if p == nil {
		return nil
	}
	const tsFormat = "2006-01-02T15:04:05.000Z"
	out := map[string]any{
		"id":                  p.ID,
		"updatedAt":           p.UpdatedAt.UTC().Format(tsFormat),
		"title":               p.Title,
		"name":                p.Name,
		"summary":             p.Summary,
		"alignCenter":         p.AlignCenter,
		"hideTitleWhenPinned": p.HideTitleWhenPinned,
		"font":                p.Font,
		"userId":              p.UserID,
		"eyeCatchingImageId":  p.EyeCatchingImageID,
		"content":             rawJSONBytes(p.Content),
		"variables":           rawJSONBytes(p.Variables),
		"script":              p.Script,
		"visibility":          string(p.Visibility),
		"likedCount":          p.LikedCount,
	}
	if len(idGens) > 0 && idGens[0] != nil {
		if t, err := idGens[0].ParseTime(p.ID); err == nil {
			out["createdAt"] = t.UTC().Format(tsFormat)
		}
	}
	return out
}

// PackPageContext carries per-call enrichment data for PackPageWithContext.
// All fields are optional; nil leaves the corresponding output field
// **omitted entirely** (never null) so the frontend cannot crash on
// `page.user.username` etc.
type PackPageContext struct {
	// IDGen drives the aidx → createdAt derivation just like the variadic
	// arg on PackPage.
	IDGen id.Generator
	// Owner is the page.user owner. Required by frontend MkPagePreview
	// (`page.user.username` is read unconditionally); when nil the `user`
	// field is omitted entirely. List callers MUST therefore drop the row
	// when their lookup misses (= upstream packMany semantics), otherwise
	// MkPagePreview crashes on the next `page.user.username` access.
	Owner *model.User
	// EyeCatchingImage is the pre-packed Drive file when Page.EyeCatchingImageID
	// is set. nil means "either no image or the file lookup missed"; both
	// render the same in MkPagePreview (the `v-if="page.eyeCatchingImage"`
	// guard simply hides the block).
	EyeCatchingImage map[string]any
	// IsLiked is the viewer's like state. nil omits the field (upstream
	// `pages/show` returns undefined when there is no logged-in viewer).
	IsLiked *bool
}

// PackPageWithContext extends PackPage by adding `user`, `eyeCatchingImage`,
// and `isLiked` fields when the context provides them. Upstream
// PageEntityService.pack always populates `user`; mk-go list endpoints used
// to omit it and the frontend MkPagePreview crashed silently (template
// `page.user.username` reference) — see #1134.
func PackPageWithContext(p *model.Page, ctx PackPageContext) map[string]any {
	out := PackPage(p, ctx.IDGen)
	if out == nil {
		return nil
	}
	if ctx.Owner != nil {
		out["user"] = PackUserLite(ctx.Owner)
	}
	// 注: Owner が nil の場合は user field を omit する。null 出しすると
	// frontend MkPagePreview の page.user.username で再 throw するので、
	// caller (list handler) は owner lookup miss の行を skip する責務がある
	// (#1134 で対応した list endpoint 群はすべて owner 確保 → pack の順)。
	if ctx.EyeCatchingImage != nil {
		out["eyeCatchingImage"] = ctx.EyeCatchingImage
	}
	if ctx.IsLiked != nil {
		out["isLiked"] = *ctx.IsLiked
	}
	return out
}

// rawJSONBytes returns the raw JSON bytes as json.RawMessage so JSON encoders
// emit jsonb column content verbatim. Empty bytes become nil (JSON null).
func rawJSONBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}
