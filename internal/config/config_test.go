package config

import (
	"strings"
	"testing"
)

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
		{name: "invalid team syntax", config: strings.Replace(valid, "github/notifications", `"@github/notifications"`, 1), wantErr: "must use org/team-slug form"},
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
