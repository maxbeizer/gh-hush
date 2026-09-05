package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `
user: octocat
github_organization: github
team_slugs:
  - github/notifications
keep:
  external_organization_issues: true
  personally_mentioned: true
  personally_assigned: true
  individually_review_requested: true
  active_team_review_requested_pull_requests: true
  authored_by_user: true
  team_mentioned_discussions: true
hush:
  all_other_notifications: true
`

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	got, err := DefaultPath()
	if err != nil || got != filepath.Join(home, "gh-hush", "config.yml") {
		t.Fatalf("DefaultPath() = %q, %v", got, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := DefaultPath(); err == nil {
		t.Fatal("expected relative path error")
	}
}

func TestParseValidationAndHardSchemaBreak(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"valid", validYAML, ""},
		{"old unsubscribe rejected", strings.Replace(validYAML, "hush:", "unsubscribe:", 1), "field unsubscribe not found"},
		{"run mode removed", validYAML + "run_mode: ad_hoc\n", "field run_mode not found"},
		{"output removed", validYAML + "output:\n  default_mode: dry_run\n", "field output not found"},
		{"unknown", validYAML + "unexpected: true\n", "field unexpected not found"},
		{"required keep flag", strings.Replace(validYAML, "  authored_by_user: true\n", "", 1), "keep.authored_by_user is required"},
		{"required active team PR flag", strings.Replace(validYAML, "  active_team_review_requested_pull_requests: true\n", "", 1), "keep.active_team_review_requested_pull_requests is required"},
		{"required hush", strings.Replace(validYAML, "  all_other_notifications: true\n", "", 1), "hush.all_other_notifications is required"},
		{"hush must be true", strings.Replace(validYAML, "all_other_notifications: true", "all_other_notifications: false", 1), "hush.all_other_notifications must be true"},
		{"bad user", strings.Replace(validYAML, "user: octocat", "user: octo--cat", 1), "valid GitHub login"},
		{"bad team", strings.Replace(validYAML, "github/notifications", "other/notifications", 1), "must belong"},
		{"duplicate", strings.Replace(validYAML, "  - github/notifications\n", "  - github/notifications\n  - GITHUB/notifications\n", 1), "duplicate"},
		{"multiple documents", validYAML + "---\nuser: other\n", "exactly one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.input))
			if tt.want == "" && err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("Parse() error = %v, want %q", err, tt.want)
			}
		})
	}
}
