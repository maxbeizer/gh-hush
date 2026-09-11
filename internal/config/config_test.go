package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const validYAML = `
version: 3
identity:
  user: octocat
  organization: github
  teams:
    - github/notifications
defaults:
  action: hush
  on_missing_evidence: keep
rules:
  - name: keep work outside my organization
    action: keep
    when:
      repository:
        owner_not: github
  - name: keep personal mentions
    action: keep
    when:
      reason: [mention]
  - name: keep my team's active reviews
    action: keep
    when:
      all:
        - subject_type: [PullRequest]
        - state: open
        - review_requested_team: my_teams
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

func TestParseAcceptsValidV3(t *testing.T) {
	cfg, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Version != 3 || cfg.Identity.User != "octocat" || cfg.Identity.Organization != "github" ||
		len(cfg.Identity.Teams) != 1 || cfg.Defaults.Action != "hush" || cfg.Defaults.OnMissingEvidence != "keep" ||
		len(cfg.Rules) != 3 || cfg.Rules[0].Name != "keep work outside my organization" {
		t.Fatalf("parsed config = %#v", cfg)
	}
}

func TestParseValidation(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"valid", validYAML, ""},
		{"wrong version", strings.Replace(validYAML, "version: 3", "version: 2", 1), "version must be 3"},
		{"legacy keep field", strings.Replace(validYAML, "rules:", "keep:\n  personally_mentioned: true\nrules:", 1), "older schema"},
		{"bad user", strings.Replace(validYAML, "user: octocat", "user: octo--cat", 1), "identity.user must be a valid GitHub login"},
		{"bad org", strings.Replace(validYAML, "organization: github", "organization: git--hub", 1), "identity.organization"},
		{"team outside org", strings.Replace(validYAML, "github/notifications", "other/notifications", 1), "must belong"},
		{"duplicate team", strings.Replace(validYAML, "    - github/notifications\n", "    - github/notifications\n    - GITHUB/notifications\n", 1), "duplicate"},
		{"bad default action", strings.Replace(validYAML, "action: hush", "action: silence", 1), "defaults.action"},
		{"bad missing posture", strings.Replace(validYAML, "on_missing_evidence: keep", "on_missing_evidence: maybe", 1), "on_missing_evidence"},
		{"bad rule action", strings.Replace(validYAML, "    action: keep", "    action: silence", 1), "must be keep or hush"},
		{"missing rule name", strings.Replace(validYAML, "  - name: keep personal mentions\n    action: keep\n    when:\n      reason: [mention]\n", "  - action: keep\n    when:\n      reason: [mention]\n", 1), "name is required"},
		{"duplicate rule name", validYAML + "  - name: keep personal mentions\n    action: keep\n", "duplicate name"},
		{"unknown predicate", strings.Replace(validYAML, "      reason: [mention]", "      nonsense: true", 1), `unknown predicate "nonsense"`},
		{"multiple documents", validYAML + "---\nversion: 3\n", "exactly one"},
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

func TestLegacyFieldGuidancePointsAtMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	legacy := `user: octocat
github_organization: github
team_slugs:
  - github/notifications
keep:
  personally_mentioned: true
hush:
  all_other_notifications: true
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "migrate-config") {
		t.Fatalf("error = %v, want migrate-config guidance", err)
	}
	// The AI-prompt summary line must also point at migration, not fall back to
	// the generic "fix the errors" guidance.
	if !strings.Contains(err.Error(), "uses an older schema. Run: gh hush migrate-config") {
		t.Fatalf("error = %v, want AI-prompt migration guidance", err)
	}
}

func TestPublishedSchemaEnforcesRuntimeConstraints(t *testing.T) {
	path := filepath.Join("..", "..", "config.schema.json")
	schema, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatalf("compile config.schema.json: %v", err)
	}
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"valid", validYAML, true},
		{"wrong version", strings.Replace(validYAML, "version: 3", "version: 2", 1), false},
		{"unknown top-level field", validYAML + "unexpected: true\n", false},
		{"missing defaults", strings.Replace(validYAML, "defaults:\n  action: hush\n  on_missing_evidence: keep\n", "", 1), false},
		{"bad default action", strings.Replace(validYAML, "action: hush", "action: silence", 1), false},
		{"bad rule action", strings.Replace(validYAML, "    action: keep", "    action: silence", 1), false},
		{"unknown predicate key", strings.Replace(validYAML, "      reason: [mention]", "      nonsense: true", 1), false},
		{"empty predicate", strings.Replace(validYAML, "      reason: [mention]", "      {}", 1), false},
		{"empty string predicate", strings.Replace(validYAML, "      reason: [mention]", "      author: \"\"", 1), false},
		{"non-string predicate value", strings.Replace(validYAML, "      reason: [mention]", "      author: true", 1), false},
		{"invalid state value", strings.Replace(validYAML, "      reason: [mention]", "      state: nonsense", 1), false},
		{"search without mention sibling", strings.Replace(validYAML, "      reason: [mention]", "      reason: [mention]\n      search: [comments]", 1), false},
		{"recursive composition", strings.Replace(validYAML, "      reason: [mention]", "      any:\n        - all:\n            - not:\n                reason: [subscribed]\n            - subject_type: [Issue]", 1), true},
		{"recommended template", string(RecommendedConfigYAML("octocat", "github", []string{"github/notifications"})), true},
		{"recommended template without teams", string(RecommendedConfigYAML("octocat", "github", nil)), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, runtimeErr := Parse([]byte(tt.input))
			if tt.valid && runtimeErr != nil {
				t.Fatalf("runtime rejected valid config: %v", runtimeErr)
			}
			if !tt.valid && runtimeErr == nil {
				t.Fatal("runtime accepted invalid config")
			}

			var document any
			if err := yaml.Unmarshal([]byte(tt.input), &document); err != nil {
				t.Fatal(err)
			}
			schemaErr := schema.Validate(document)
			if tt.valid && schemaErr != nil {
				t.Fatalf("schema rejected valid config: %v", schemaErr)
			}
			if !tt.valid && schemaErr == nil {
				t.Fatal("schema accepted invalid config")
			}
		})
	}
}

func TestInitializeWritesValidRecommendedPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := Initialize(path, "octocat", "github", []string{"github/notifications"}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("generated configuration is invalid: %v", err)
	}
	if cfg.Identity.User != "octocat" || len(cfg.Rules) == 0 {
		t.Fatalf("generated config = %#v", cfg)
	}
	if err := Initialize(path, "octocat", "github", nil); err == nil {
		t.Fatal("Initialize() overwrote an existing file")
	}
}

func TestMigrateProducesEquivalentV3(t *testing.T) {
	legacy := `user: octocat
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
watched_repositories:
  github/watched:
    open_pull_requests: true
    open_discussions: true
  github/allofit:
    all_notifications: true
hush:
  all_other_notifications: true
`
	migrated, err := Migrate([]byte(legacy))
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	cfg, err := Parse(migrated)
	if err != nil {
		t.Fatalf("migrated config is invalid: %v\n%s", err, migrated)
	}
	if cfg.Version != 3 || cfg.Identity.User != "octocat" || cfg.Identity.Organization != "github" {
		t.Fatalf("migrated identity = %#v", cfg.Identity)
	}
	var names []string
	for _, rule := range cfg.Rules {
		names = append(names, rule.Name)
	}
	for _, want := range []string{
		"keep work outside my organization",
		"keep personal mentions",
		"keep work assigned to me",
		"keep review requests for me",
		"keep work I authored",
		"keep my team's active reviews",
		"keep team-mentioned discussions",
		"watch all notifications in github/allofit",
		"watch github/watched",
	} {
		if !containsString(names, want) {
			t.Fatalf("migrated rules %v missing %q\n%s", names, want, migrated)
		}
	}
}

func TestMigrateRejectsUnknownAndMultiDocumentInput(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"unknown field", "user: octocat\ngithub_organization: github\nmystery: true\n", "unknown configuration field \"mystery\" cannot be migrated automatically"},
		{"renamed discussion_team_slugs", "user: octocat\ngithub_organization: github\ndiscussion_team_slugs: [github/notifications]\n", `was renamed to "team_slugs"`},
		{"removed unsubscribe", "user: octocat\ngithub_organization: github\nunsubscribe: true\n", `was replaced by "hush"`},
		{"removed run_mode", "user: octocat\ngithub_organization: github\nrun_mode: auto\n", "no longer supported"},
		{"multiple documents", "user: octocat\ngithub_organization: github\n---\nuser: other\n", "exactly one"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Migrate([]byte(tt.input)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Migrate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMigrateRejectsAlreadyV3(t *testing.T) {
	if _, err := Migrate([]byte(validYAML)); err == nil || !strings.Contains(err.Error(), "already version 3") {
		t.Fatalf("Migrate() error = %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
