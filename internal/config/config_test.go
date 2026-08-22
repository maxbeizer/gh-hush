package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPath(t *testing.T) {
	t.Run("uses XDG config home", func(t *testing.T) {
		configHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configHome)

		got, err := DefaultPath()
		if err != nil {
			t.Fatalf("DefaultPath() error = %v", err)
		}
		want := filepath.Join(configHome, "gh-hush", "config.yml")
		if got != want {
			t.Fatalf("DefaultPath() = %q, want %q", got, want)
		}
	})

	t.Run("rejects relative XDG config home", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "relative")
		if _, err := DefaultPath(); err == nil {
			t.Fatal("DefaultPath() error = nil, want relative-path error")
		}
	})
}

func TestValidGitHubLogin(t *testing.T) {
	tests := []struct {
		name  string
		login string
		valid bool
	}{
		{name: "one character", login: "a", valid: true},
		{name: "39 characters", login: strings.Repeat("a", 39), valid: true},
		{name: "single internal hyphen", login: "octo-cat", valid: true},
		{name: "leading hyphen", login: "-octocat"},
		{name: "trailing hyphen", login: "octocat-"},
		{name: "consecutive hyphens", login: "octo--cat"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validGitHubLogin(tt.login); got != tt.valid {
				t.Fatalf("validGitHubLogin(%q) = %t, want %t", tt.login, got, tt.valid)
			}
		})
	}
}

func TestParseValidation(t *testing.T) {
	valid := `
user: octocat
github_organization: github
run_mode: ad_hoc
discussion_team_slugs:
  - github/notifications
keep:
  external_organization_issues: true
  personally_mentioned: true
  personally_assigned: true
  individually_review_requested: true
  authored_by_user: true
  team_mentioned_discussions: true
unsubscribe:
  all_other_notifications: true
output:
  default_mode: dry_run
  include_keep_decisions: true
  include_unsubscribe_decisions: true
  include_decision_reasons: true
`

	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{name: "valid", config: valid},
		{name: "unknown field", config: valid + "unexpected: true\n", wantErr: "field unexpected not found"},
		{name: "missing required flag", config: strings.Replace(valid, "  authored_by_user: true\n", "", 1), wantErr: "keep.authored_by_user is required"},
		{name: "scheduled mode rejected", config: strings.Replace(valid, "run_mode: ad_hoc", "run_mode: scheduled", 1), wantErr: `run_mode must be "ad_hoc"`},
		{name: "hidden decisions rejected", config: strings.Replace(valid, "include_keep_decisions: true", "include_keep_decisions: false", 1), wantErr: "output.include_keep_decisions must be true"},
		{name: "catch all required", config: strings.Replace(valid, "all_other_notifications: true", "all_other_notifications: false", 1), wantErr: "unsubscribe.all_other_notifications must be true"},
		{name: "invalid user login", config: strings.Replace(valid, "user: octocat", "user: octo--cat", 1), wantErr: "user must be a valid GitHub login"},
		{name: "invalid organization login", config: strings.Replace(valid, "github_organization: github", "github_organization: github-", 1), wantErr: "github_organization must be a valid GitHub organization login"},
		{name: "invalid team syntax", config: strings.Replace(valid, "github/notifications", `"@github/notifications"`, 1), wantErr: "must use org/team-slug form"},
		{name: "invalid team organization login", config: strings.Replace(valid, "github/notifications", "git--hub/notifications", 1), wantErr: "must use org/team-slug form"},
		{name: "team must match organization", config: strings.Replace(valid, "github/notifications", "other/notifications", 1), wantErr: "must belong to github_organization"},
		{name: "duplicate teams rejected", config: strings.Replace(valid, "  - github/notifications\n", "  - github/notifications\n  - GITHUB/notifications\n", 1), wantErr: "contains duplicate"},
		{name: "multiple documents rejected", config: valid + "---\nuser: another\n", wantErr: "exactly one YAML document"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.config))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
