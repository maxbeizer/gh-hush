package reporturl

import "testing"

func TestSafeSelectsFirstBrowserURLOrUnavailable(t *testing.T) {
	const fallback = "https://github.example/org/repo/"
	tests := []struct {
		name       string
		candidates []string
		want       string
	}{
		{"preserves candidate", []string{"https://github.com/org/repo/issues/1/", fallback}, "https://github.com/org/repo/issues/1/"},
		{"falls back", []string{"://bad", fallback}, fallback},
		{"rejects GitHub API host", []string{"https://api.github.com/repos/org/repo", fallback}, fallback},
		{"rejects API subdomain", []string{"https://api.github.example/repos/org/repo", fallback}, fallback},
		{"rejects trailing-dot API host", []string{"https://api.github.com./repos/org/repo", fallback}, fallback},
		{"rejects enterprise API path", []string{"https://github.example/API/v3/repos/org/repo", fallback}, fallback},
		{"rejects escaped enterprise API path", []string{"https://github.example/%61pi/v3/repos/org/repo", fallback}, fallback},
		{"allows similar browser path", []string{"https://github.example/apis/1", fallback}, "https://github.example/apis/1"},
		{"rejects userinfo", []string{"https://user@github.com/org/repo", fallback}, fallback},
		{"rejects non-HTTP scheme", []string{"ftp://github.com/org/repo"}, "unavailable"},
		{"unavailable", nil, "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Safe(tt.candidates...); got != tt.want {
				t.Fatalf("Safe()=%q want=%q", got, tt.want)
			}
		})
	}
}
