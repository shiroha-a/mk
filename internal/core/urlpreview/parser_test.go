package urlpreview

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHTML_OGP(t *testing.T) {
	html := `<html><head>
		<meta property="og:title" content="Test Page">
		<meta property="og:description" content="A description">
		<meta property="og:image" content="https://example.com/img.png">
		<meta property="og:site_name" content="Example">
		<meta property="og:url" content="https://example.com/page">
		<link rel="icon" href="/favicon.ico">
	</head><body></body></html>`

	r := ParseHTML(strings.NewReader(html), "https://example.com/original")
	require.NotNil(t, r)
	assert.Equal(t, "Test Page", *r.Title)
	assert.Equal(t, "A description", *r.Description)
	assert.Equal(t, "https://example.com/img.png", *r.Thumbnail)
	// favicon は base URL (#639) で絶対 URL に解決される
	assert.Equal(t, "https://example.com/favicon.ico", *r.Icon)
	assert.Equal(t, "Example", *r.Sitename)
	assert.Equal(t, "https://example.com/page", r.URL)
}

func TestParseHTML_TwitterCard(t *testing.T) {
	html := `<html><head>
		<meta name="twitter:title" content="Tweet Title">
		<meta name="twitter:description" content="Tweet desc">
		<meta name="twitter:image" content="https://example.com/tw.png">
	</head><body></body></html>`

	r := ParseHTML(strings.NewReader(html), "https://example.com")
	assert.Equal(t, "Tweet Title", *r.Title)
	assert.Equal(t, "Tweet desc", *r.Description)
	assert.Equal(t, "https://example.com/tw.png", *r.Thumbnail)
}

func TestParseHTML_FallbackToTitle(t *testing.T) {
	html := `<html><head><title>Fallback Title</title></head><body></body></html>`
	r := ParseHTML(strings.NewReader(html), "https://example.com")
	assert.Equal(t, "Fallback Title", *r.Title)
	assert.Nil(t, r.Description)
}

func TestParseHTML_ActivityPub(t *testing.T) {
	html := `<html><head>
		<link rel="alternate" type="application/activity+json" href="https://example.com/users/alice">
	</head><body></body></html>`
	r := ParseHTML(strings.NewReader(html), "https://example.com")
	require.NotNil(t, r.ActivityPub)
	assert.Equal(t, "https://example.com/users/alice", *r.ActivityPub)
}

func TestParseHTML_Empty(t *testing.T) {
	r := ParseHTML(strings.NewReader(""), "https://example.com")
	assert.Equal(t, "https://example.com", r.URL)
	assert.Nil(t, r.Title)
}

func TestParseHTML_InvalidHTML(t *testing.T) {
	r := ParseHTML(strings.NewReader("<not valid <<< html"), "https://example.com")
	assert.Equal(t, "https://example.com", r.URL)
}

// #2106 L45: og:url が javascript:/data: 等の危険な scheme の場合、url field は安全な
// pageURL にフォールバックする (素通ししない)。
func TestParseHTML_DangerousCanonicalURL(t *testing.T) {
	html := `<html><head>
		<meta property="og:title" content="x">
		<meta property="og:url" content="javascript:alert(1)">
	</head></html>`
	r := ParseHTML(strings.NewReader(html), "https://example.com/original")
	if r.URL != "https://example.com/original" {
		t.Fatalf("dangerous og:url should fall back to pageURL, got %q", r.URL)
	}
}
