// Package reporturl selects browser-safe URLs for user-facing output.
package reporturl

import (
	"net/url"
	"strings"
)

// Safe returns the first absolute HTTP(S) browser URL in candidates. GitHub
// API hosts and enterprise-style /api paths are rejected so API endpoints are
// never presented as report links.
func Safe(candidates ...string) string {
	for _, candidate := range candidates {
		parsed, err := url.Parse(candidate)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			continue
		}
		host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
		path := strings.ToLower(parsed.Path)
		if host == "api.github.com" || strings.HasPrefix(host, "api.") || path == "/api" || strings.HasPrefix(path, "/api/") {
			continue
		}
		return candidate
	}
	return "unavailable"
}
