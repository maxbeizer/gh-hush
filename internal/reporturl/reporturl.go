// Package reporturl selects browser-safe URLs for user-facing output.
package reporturl

import (
	"net/url"
	"strings"
)

const unavailable = "unavailable"

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
	return unavailable
}

// Repository returns a validated browser URL for a repository. GitHub's
// html_url is preferred. Because this client only talks to github.com, a
// missing or unsafe html_url can be reconstructed from a strict owner/name
// pair without trusting API-origin repository data.
func Repository(htmlURL, fullName string) string {
	if candidate := Safe(htmlURL); candidate != unavailable {
		return candidate
	}
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || !repositoryPart(parts[0]) || !repositoryPart(parts[1]) {
		return unavailable
	}
	return "https://github.com/" + parts[0] + "/" + parts[1]
}

func repositoryPart(part string) bool {
	if part == "" || part == "." || part == ".." {
		return false
	}
	for _, char := range part {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}
