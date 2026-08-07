package sentry

import (
	"strings"
	"testing"

	sentrygo "github.com/getsentry/sentry-go"
)

// scrubEvent is unexported, so this file uses the internal test package while
// sentry_test.go stays external.
func TestScrubEvent(t *testing.T) {
	tests := []struct {
		name  string
		event *sentrygo.Event
		check func(t *testing.T, got *sentrygo.Event)
	}{
		{
			name:  "nil event is passed through",
			event: nil,
			check: func(t *testing.T, got *sentrygo.Event) {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
			},
		},
		{
			name:  "event without a request is passed through",
			event: &sentrygo.Event{},
			check: func(t *testing.T, got *sentrygo.Event) {
				if got == nil || got.Request != nil {
					t.Errorf("expected an event with no request, got %+v", got)
				}
			},
		},
		{
			name: "api token in the query string is redacted",
			event: &sentrygo.Event{Request: &sentrygo.Request{
				QueryString: "i=super-secret&limit=10",
			}},
			check: func(t *testing.T, got *sentrygo.Event) {
				if strings.Contains(got.Request.QueryString, "super-secret") {
					t.Errorf("token leaked: %q", got.Request.QueryString)
				}
				if !strings.Contains(got.Request.QueryString, "limit=10") {
					t.Errorf("non-secret param was dropped: %q", got.Request.QueryString)
				}
			},
		},
		{
			// 解析できないクエリは丸ごと捨てる。原文を残すと token が通る。
			name: "unparseable query string is dropped entirely",
			event: &sentrygo.Event{Request: &sentrygo.Request{
				QueryString: "i=%zz",
			}},
			check: func(t *testing.T, got *sentrygo.Event) {
				if got.Request.QueryString != "" {
					t.Errorf("expected the query to be dropped, got %q", got.Request.QueryString)
				}
			},
		},
		{
			name: "body and cookies are cleared defensively",
			event: &sentrygo.Event{Request: &sentrygo.Request{
				Data:    `{"i":"super-secret"}`,
				Cookies: "session=super-secret",
			}},
			check: func(t *testing.T, got *sentrygo.Event) {
				if got.Request.Data != "" {
					t.Errorf("body survived: %q", got.Request.Data)
				}
				if got.Request.Cookies != "" {
					t.Errorf("cookies survived: %q", got.Request.Cookies)
				}
			},
		},
		{
			name: "query in the url is redacted",
			event: &sentrygo.Event{Request: &sentrygo.Request{
				URL: "https://example.test/api/i?i=super-secret",
			}},
			check: func(t *testing.T, got *sentrygo.Event) {
				if strings.Contains(got.Request.URL, "super-secret") {
					t.Errorf("token leaked through the url: %q", got.Request.URL)
				}
			},
		},
		{
			name: "sensitive headers are removed",
			event: &sentrygo.Event{Request: &sentrygo.Request{
				Headers: map[string]string{
					"Authorization": "Bearer super-secret",
					"Content-Type":  "application/json",
				},
			}},
			check: func(t *testing.T, got *sentrygo.Event) {
				if _, ok := got.Request.Headers["Authorization"]; ok {
					t.Error("Authorization header survived")
				}
				if got.Request.Headers["Content-Type"] != "application/json" {
					t.Errorf("harmless header was dropped: %+v", got.Request.Headers)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, scrubEvent(tt.event, nil))
		})
	}
}
