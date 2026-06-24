package drive

import "testing"

func TestBrowserSafeContentType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"image/png", "image/png"},
		{"image/jpeg", "image/jpeg"},
		{"video/mp4", "video/mp4"},
		{"audio/mpeg", "audio/mpeg"},
		// non-browsersafe → octet-stream (XSS 防御)
		{"text/html; charset=utf-8", "application/octet-stream"},
		{"image/svg+xml", "application/octet-stream"},
		{"text/xml; charset=utf-8", "application/octet-stream"},
		{"application/xml", "application/octet-stream"},
		{"text/plain; charset=utf-8", "application/octet-stream"},
		{"application/javascript", "application/octet-stream"},
	}
	for _, tc := range cases {
		if got := BrowserSafeContentType(tc.in); got != tc.want {
			t.Errorf("BrowserSafeContentType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
