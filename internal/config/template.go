package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Initialize writes a conservative starter v3 policy to a new path. It never
// replaces an existing file; callers must explicitly supply identity values.
func Initialize(path, user, organization string, teamSlugs []string) error {
	data := RecommendedConfigYAML(user, organization, teamSlugs)
	if _, err := Parse(data); err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	return writeNewFile(path, data)
}

func writeNewFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create config %q: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write config %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close config %q: %w", path, err)
	}
	return nil
}

// RecommendedConfigYAML renders a commented v3 policy reproducing the default
// keep behavior gh-hush shipped before v3: protect work outside the
// organization, work directed at the user, the user's team's active reviews,
// and team-mentioned Discussions, then hush everything else.
func RecommendedConfigYAML(user, organization string, teamSlugs []string) []byte {
	var b strings.Builder
	b.WriteString("version: 3\n\n")
	b.WriteString("identity:\n")
	fmt.Fprintf(&b, "  user: %s\n", user)
	fmt.Fprintf(&b, "  organization: %s\n", organization)
	writeTeams(&b, "  ", teamSlugs)
	b.WriteString("\ndefaults:\n")
	b.WriteString("  action: hush              # terminal fallback for anything no rule kept\n")
	b.WriteString("  on_missing_evidence: keep # conservative safety posture when GitHub evidence is unavailable\n\n")
	b.WriteString("rules:\n")
	block := strings.ReplaceAll(recommendedRuleBlock(len(teamSlugs) > 0), "ORGANIZATION_PLACEHOLDER", organization)
	b.WriteString(block)
	return []byte(b.String())
}

func recommendedRuleBlock(hasTeams bool) string {
	var b strings.Builder
	b.WriteString(`  - name: keep work outside my organization
    action: keep
    when:
      repository:
        owner_not: ORGANIZATION_PLACEHOLDER

  - name: keep personal mentions
    action: keep
    when:
      reason: [mention]

  - name: keep work assigned to me
    action: keep
    when:
      any:
        - reason: [assign]
        - all:
            - subject_type: [Issue, PullRequest]
            - assignee: me

  - name: keep review requests for me
    action: keep
    when:
      all:
        - subject_type: [PullRequest]
        - review_requested: me

  - name: keep work I authored
    action: keep
    when:
      any:
        - reason: [author]
        - author: me
`)
	if hasTeams {
		b.WriteString(`
  - name: keep my team's active reviews
    action: keep
    when:
      all:
        - subject_type: [PullRequest]
        - state: open
        - review_requested_team: my_teams

  - name: keep team-mentioned discussions
    action: keep
    when:
      all:
        - subject_type: [Discussion]
        - mentions_team: my_teams
          search: [body, comments]
`)
	}
	return b.String()
}

func writeTeams(b *strings.Builder, indent string, teamSlugs []string) {
	if len(teamSlugs) == 0 {
		b.WriteString(indent + "teams: []\n")
		return
	}
	b.WriteString(indent + "teams:\n")
	for _, team := range teamSlugs {
		fmt.Fprintf(b, "%s  - %s\n", indent, team)
	}
}

// legacyConfig is the pre-v3 flat schema, parsed only to migrate it forward.
type legacyConfig struct {
	User                string                   `yaml:"user"`
	GitHubOrganization  string                   `yaml:"github_organization"`
	TeamSlugs           []string                 `yaml:"team_slugs"`
	Keep                legacyKeep               `yaml:"keep"`
	WatchedRepositories map[string]legacyWatched `yaml:"watched_repositories"`
	Hush                map[string]any           `yaml:"hush"`
	Version             *int                     `yaml:"version"`
}

type legacyKeep struct {
	ExternalOrganizationIssues            *bool `yaml:"external_organization_issues"`
	PersonallyMentioned                   *bool `yaml:"personally_mentioned"`
	PersonallyAssigned                    *bool `yaml:"personally_assigned"`
	IndividuallyReviewRequested           *bool `yaml:"individually_review_requested"`
	ActiveTeamReviewRequestedPullRequests *bool `yaml:"active_team_review_requested_pull_requests"`
	AuthoredByUser                        *bool `yaml:"authored_by_user"`
	TeamMentionedDiscussions              *bool `yaml:"team_mentioned_discussions"`
}

type legacyWatched struct {
	AllNotifications *bool `yaml:"all_notifications"`
	OpenPullRequests *bool `yaml:"open_pull_requests"`
	OpenIssues       *bool `yaml:"open_issues"`
	OpenDiscussions  *bool `yaml:"open_discussions"`
}

func enabled(value *bool) bool { return value != nil && *value }

// Migrate rewrites a pre-v3 configuration document as an equivalent v3 policy.
// It is mechanical: each enabled keep switch and watched repository becomes an
// explicit rule, preserving the original protection semantics.
func Migrate(data []byte) ([]byte, error) {
	if current, err := Parse(data); err == nil && current.Version == Version {
		return nil, errors.New("configuration is already version 3")
	}
	var legacy legacyConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&legacy); err != nil {
		return nil, legacyDecodeError(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("configuration must contain exactly one YAML document")
	}
	if legacy.Version != nil && *legacy.Version >= Version {
		return nil, errors.New("configuration is already version 3")
	}
	if !validGitHubLogin(legacy.User) || !validGitHubLogin(legacy.GitHubOrganization) {
		return nil, errors.New("existing configuration is missing a valid user and github_organization")
	}

	var b strings.Builder
	b.WriteString("version: 3\n\n")
	b.WriteString("identity:\n")
	fmt.Fprintf(&b, "  user: %s\n", legacy.User)
	fmt.Fprintf(&b, "  organization: %s\n", legacy.GitHubOrganization)
	writeTeams(&b, "  ", legacy.TeamSlugs)
	b.WriteString("\ndefaults:\n")
	b.WriteString("  action: hush\n")
	b.WriteString("  on_missing_evidence: keep\n\n")
	b.WriteString("rules:\n")

	rules := migrateRules(legacy)
	if len(rules) == 0 {
		b.WriteString("  []\n")
	} else {
		b.WriteString(strings.Join(rules, "\n"))
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
	}

	migrated := []byte(b.String())
	if _, err := Parse(migrated); err != nil {
		return nil, fmt.Errorf("migration produced an invalid configuration: %w", err)
	}
	return migrated, nil
}

// legacyDecodeError turns yaml.v3 unknown-field errors from strict legacy
// decoding into guidance that names renamed and removed pre-v3 fields instead
// of leaking the Go type name.
func legacyDecodeError(err error) error {
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return fmt.Errorf("read existing configuration: %w", err)
	}
	unknownField := regexp.MustCompile(`^line ([0-9]+): field ([^ ]+) not found in type .+$`)
	problems := make([]string, 0, len(typeErr.Errors))
	for _, problem := range typeErr.Errors {
		match := unknownField.FindStringSubmatch(problem)
		if match == nil {
			problems = append(problems, problem)
			continue
		}
		line, field := match[1], match[2]
		switch field {
		case "discussion_team_slugs":
			problems = append(problems, fmt.Sprintf(`line %s: %q was renamed to "team_slugs" in v0.2.0; rename it before migrating to version %d`, line, field, Version))
		case "unsubscribe":
			problems = append(problems, fmt.Sprintf(`line %s: %q was replaced by "hush"; update it before migrating to version %d`, line, field, Version))
		case "run_mode", "output":
			problems = append(problems, fmt.Sprintf(`line %s: %q is no longer supported and must be removed before migrating to version %d`, line, field, Version))
		default:
			problems = append(problems, fmt.Sprintf(`line %s: unknown configuration field %q cannot be migrated automatically; remove it or fix the typo`, line, field))
		}
	}
	return fmt.Errorf("read existing configuration:\n  %s", strings.Join(problems, "\n  "))
}

func migrateRules(legacy legacyConfig) []string {
	hasTeams := len(legacy.TeamSlugs) > 0
	var rules []string
	if enabled(legacy.Keep.ExternalOrganizationIssues) {
		rules = append(rules, fmt.Sprintf(`  - name: keep work outside my organization
    action: keep
    when:
      repository:
        owner_not: %s
`, legacy.GitHubOrganization))
	}
	if enabled(legacy.Keep.PersonallyMentioned) {
		rules = append(rules, `  - name: keep personal mentions
    action: keep
    when:
      reason: [mention]
`)
	}
	if enabled(legacy.Keep.PersonallyAssigned) {
		rules = append(rules, `  - name: keep work assigned to me
    action: keep
    when:
      any:
        - reason: [assign]
        - all:
            - subject_type: [Issue, PullRequest]
            - assignee: me
`)
	}
	if enabled(legacy.Keep.IndividuallyReviewRequested) {
		rules = append(rules, `  - name: keep review requests for me
    action: keep
    when:
      all:
        - subject_type: [PullRequest]
        - review_requested: me
`)
	}
	if enabled(legacy.Keep.AuthoredByUser) {
		rules = append(rules, `  - name: keep work I authored
    action: keep
    when:
      any:
        - reason: [author]
        - author: me
`)
	}
	if enabled(legacy.Keep.ActiveTeamReviewRequestedPullRequests) && hasTeams {
		rules = append(rules, `  - name: keep my team's active reviews
    action: keep
    when:
      all:
        - subject_type: [PullRequest]
        - state: open
        - review_requested_team: my_teams
`)
	}
	if enabled(legacy.Keep.TeamMentionedDiscussions) && hasTeams {
		rules = append(rules, `  - name: keep team-mentioned discussions
    action: keep
    when:
      all:
        - subject_type: [Discussion]
        - mentions_team: my_teams
          search: [body, comments]
`)
	}
	rules = append(rules, migrateWatchedRules(legacy.WatchedRepositories)...)
	return rules
}

func migrateWatchedRules(watched map[string]legacyWatched) []string {
	names := make([]string, 0, len(watched))
	for name := range watched {
		names = append(names, name)
	}
	sort.Strings(names)
	var rules []string
	for _, name := range names {
		entry := watched[name]
		var conditions []string
		if enabled(entry.AllNotifications) {
			rules = append(rules, fmt.Sprintf(`  - name: watch all notifications in %s
    action: keep
    when:
      repository:
        any_of: [%s]
`, name, name))
			continue
		}
		if enabled(entry.OpenPullRequests) {
			conditions = append(conditions, `          - all: [{subject_type: [PullRequest]}, {state: open}]`)
		}
		if enabled(entry.OpenIssues) {
			conditions = append(conditions, `          - all: [{subject_type: [Issue]}, {state: open}]`)
		}
		if enabled(entry.OpenDiscussions) {
			conditions = append(conditions, `          - all: [{subject_type: [Discussion]}, {state_not: closed}]`)
		}
		if len(conditions) == 0 {
			continue
		}
		rules = append(rules, fmt.Sprintf(`  - name: watch %s
    action: keep
    when:
      all:
        - repository:
            any_of: [%s]
        - any:
%s
`, name, name, strings.Join(conditions, "\n")))
	}
	return rules
}
