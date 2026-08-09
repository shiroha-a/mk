package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
)

// stubEmojiLookup is a focused mock — we don't need the full
// EmojiRepository interface for the redirect handler's contract.
type stubEmojiLookup struct {
	emojis map[string]*model.Emoji // key: "name|host" (host="" for local)
	err    error
}

func (s *stubEmojiLookup) FindByNameAndHost(name string, host *string) (*model.Emoji, error) {
	if s.err != nil {
		return nil, s.err
	}
	key := name + "|"
	if host != nil {
		key += *host
	}
	e, ok := s.emojis[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return e, nil
}

func newEmojiTestContext(t *testing.T, path, query string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	url := "/emoji/" + path
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/emoji/:path")
	c.SetParamNames("path")
	c.SetParamValues(path)
	return c, rec
}

func TestEmojiRedirectHandler_LocalEmoji(t *testing.T) {
	repo := &stubEmojiLookup{emojis: map[string]*model.Emoji{
		"smile|": {Name: "smile", PublicURL: "https://cdn.example/local-smile.png"},
	}}
	h := emojiRedirectHandler(repo)

	c, rec := newEmojiTestContext(t, "smile.webp", "")
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "https://cdn.example/local-smile.png", rec.Header().Get(echo.HeaderLocation))
	assert.Equal(t, "public, max-age=86400", rec.Header().Get(echo.HeaderCacheControl))
}

func TestEmojiRedirectHandler_LocalCanonicalDotSuffix(t *testing.T) {
	// `:smile@.:` (ReactionService.normalizeReaction が永続化する形) は
	// frontend が `/emoji/smile@..webp` で投げてくる。`@.` は host=NULL
	// と等価に扱う。
	repo := &stubEmojiLookup{emojis: map[string]*model.Emoji{
		"smile|": {Name: "smile", PublicURL: "https://cdn.example/local-smile.png"},
	}}
	h := emojiRedirectHandler(repo)

	c, rec := newEmojiTestContext(t, "smile@..webp", "")
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "https://cdn.example/local-smile.png", rec.Header().Get(echo.HeaderLocation))
}

func TestEmojiRedirectHandler_RemoteEmoji(t *testing.T) {
	repo := &stubEmojiLookup{emojis: map[string]*model.Emoji{
		"meow_yikes|remote.example": {
			Name:      "meow_yikes",
			PublicURL: "https://r/meow.png",
		},
	}}
	h := emojiRedirectHandler(repo)

	c, rec := newEmojiTestContext(t, "meow_yikes@remote.example.webp", "")
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "https://r/meow.png", rec.Header().Get(echo.HeaderLocation))
}

func TestEmojiRedirectHandler_PublicURLFallbackToOriginal(t *testing.T) {
	// publicUrl 空 → originalUrl にフォールバック (upstream `||` と同等)
	repo := &stubEmojiLookup{emojis: map[string]*model.Emoji{
		"x|host.example": {Name: "x", PublicURL: "", OriginalURL: "https://r/x_orig.png"},
	}}
	h := emojiRedirectHandler(repo)

	c, rec := newEmojiTestContext(t, "x@host.example.webp", "")
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "https://r/x_orig.png", rec.Header().Get(echo.HeaderLocation))
}

func TestEmojiRedirectHandler_NotFound_404(t *testing.T) {
	repo := &stubEmojiLookup{emojis: map[string]*model.Emoji{}}
	h := emojiRedirectHandler(repo)

	c, rec := newEmojiTestContext(t, "ghost.webp", "")
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestEmojiRedirectHandler_NotFound_FallbackQuery(t *testing.T) {
	// ?fallback クエリ付きの場合は static-assets の placeholder へ 302
	repo := &stubEmojiLookup{emojis: map[string]*model.Emoji{}}
	h := emojiRedirectHandler(repo)

	c, rec := newEmojiTestContext(t, "ghost.webp", "fallback=1")
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, emojiRedirectFallback, rec.Header().Get(echo.HeaderLocation))
}

func TestEmojiRedirectHandler_BadPath_404(t *testing.T) {
	repo := &stubEmojiLookup{}
	h := emojiRedirectHandler(repo)

	cases := []string{
		"foo.png",   // 拡張子違反
		"x!y.webp",  // 記号違反 (regex の charset 外)
		"foo+.webp", // 記号違反
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			c, rec := newEmojiTestContext(t, p, "")
			require.NoError(t, h(c))
			// regex で弾かれるか、guard で 4xx
			assert.Contains(t, []int{http.StatusNotFound, http.StatusBadRequest}, rec.Code,
				"unexpected status %d for path %q", rec.Code, p)
		})
	}
}

func TestEmojiRedirectHandler_BadAtChunks_400(t *testing.T) {
	// regex は `[a-zA-Z0-9\-_@\.]+?\.webp$` なので "a@b@c.webp" は通る。
	// path split 後に chunks > 2 で 400 を返す。
	repo := &stubEmojiLookup{}
	h := emojiRedirectHandler(repo)

	c, rec := newEmojiTestContext(t, "a@b@c.webp", "")
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEmojiRedirectHandler_RepoErrorTreatedAsNotFound(t *testing.T) {
	repo := &stubEmojiLookup{err: errors.New("db down")}
	h := emojiRedirectHandler(repo)

	c, rec := newEmojiTestContext(t, "smile.webp", "")
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestEmojiRedirectHandler_EmptyURLs_404(t *testing.T) {
	// publicUrl も originalUrl も空の row は redirect 先が無いので 404。
	repo := &stubEmojiLookup{emojis: map[string]*model.Emoji{
		"empty|": {Name: "empty", PublicURL: "", OriginalURL: ""},
	}}
	h := emojiRedirectHandler(repo)

	c, rec := newEmojiTestContext(t, "empty.webp", "")
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// upstream (`ServerService.ts` の /emoji/:path) が付けている CSP を揃える (#2404)。
//
// **この応答は redirect なので CSP は実際には効かない** (ブラウザは最終的な応答の
// header を適用する)。upstream も redirect の前に付けており同じ状態で、header 集合を
// 一致させるためのもの。効果を期待して置いているわけではない。
func TestEmojiRedirectHandler_SetsAssetCSP(t *testing.T) {
	repo := &stubEmojiLookup{emojis: map[string]*model.Emoji{
		"smile|": {Name: "smile", PublicURL: "https://cdn.example/local-smile.png"},
	}}
	h := emojiRedirectHandler(repo)

	c, rec := newEmojiTestContext(t, "smile.webp", "")
	require.NoError(t, h(c))
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "default-src 'none'; style-src 'unsafe-inline'",
		rec.Header().Get("Content-Security-Policy"))
}
