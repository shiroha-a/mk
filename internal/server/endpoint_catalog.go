package server

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// apiEndpointNames returns the deduped names of every registered POST /api/*
// route — the endpoint catalog. `/api/endpoints` lists them, and `/api/endpoint`
// (#1695) uses it to distinguish known endpoints from unknown ones. Names are
// the route path with the leading "/api/" stripped (e.g. "notes/create").
// GET-only routes, the no-prefix routes, and the "*" catchall are excluded.
func apiEndpointNames(e *echo.Echo) []string {
	names := make([]string, 0)
	seen := make(map[string]struct{})
	for _, r := range e.Routes() {
		if r.Method != http.MethodPost {
			continue
		}
		name := strings.TrimPrefix(r.Path, "/api/")
		if name == r.Path || name == "" || name == "*" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

// isRegisteredAPIEndpoint reports whether name is a registered POST /api/* route.
func isRegisteredAPIEndpoint(e *echo.Echo, name string) bool {
	for _, n := range apiEndpointNames(e) {
		if n == name {
			return true
		}
	}
	return false
}
