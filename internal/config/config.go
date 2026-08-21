package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	loginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	teamPattern  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9_.-]+$`)
)

// Config is the user-owned v1 notification triage policy.
type Config struct {
	User                string   `yaml:"user"`
	GitHubOrganization  string   `yaml:"github_organization"`
	RunMode             string   `yaml:"run_mode"`
	DiscussionTeamSlugs []string `yaml:"discussion_team_slugs"`
	Keep                Keep     `yaml:"keep"`
	Unsubscribe         struct {
		AllOtherNotifications *bool `yaml:"all_other_notifications"`
	} `yaml:"unsubscribe"`
	Output struct {
		DefaultMode                 string `yaml:"default_mode"`
		IncludeKeepDecisions        *bool  `yaml:"include_keep_decisions"`
		IncludeUnsubscribeDecisions *bool  `yaml:"include_unsubscribe_decisions"`
		IncludeDecisionReasons      *bool  `yaml:"include_decision_reasons"`
	} `yaml:"output"`
}

// Keep controls the ordered v1 keep rules.
type Keep struct {
	ExternalOrganizationIssues  *bool `yaml:"external_organization_issues"`
	PersonallyMentioned         *bool `yaml:"personally_mentioned"`
	PersonallyAssigned          *bool `yaml:"personally_assigned"`
	IndividuallyReviewRequested *bool `yaml:"individually_review_requested"`
	AuthoredByUser              *bool `yaml:"authored_by_user"`
	TeamMentionedDiscussions    *bool `yaml:"team_mentioned_discussions"`
}

// Load reads and validates a configuration file.
func Load(path string) (Config, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, nil, fmt.Errorf("read config %q: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return Config{}, nil, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, data, nil
}

// Parse decodes and validates YAML configuration.
func Parse(data []byte) (Config, error) {
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode YAML: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("config must contain exactly one YAML document")
		}
		return Config{}, fmt.Errorf("decode YAML: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks the complete v1 schema and safety invariants.
func (c Config) Validate() error {
	var validationErrors []error
	if !loginPattern.MatchString(c.User) {
		validationErrors = append(validationErrors, errors.New("user must be a valid GitHub login"))
	}
	if !loginPattern.MatchString(c.GitHubOrganization) {
		validationErrors = append(validationErrors, errors.New("github_organization must be a valid GitHub organization login"))
	}
	if c.RunMode != "ad_hoc" {
		validationErrors = append(validationErrors, errors.New(`run_mode must be "ad_hoc"`))
	}
	if c.Output.DefaultMode != "dry_run" {
		validationErrors = append(validationErrors, errors.New(`output.default_mode must be "dry_run"`))
	}

	required := map[string]*bool{
		"keep.external_organization_issues":    c.Keep.ExternalOrganizationIssues,
		"keep.personally_mentioned":            c.Keep.PersonallyMentioned,
		"keep.personally_assigned":             c.Keep.PersonallyAssigned,
		"keep.individually_review_requested":   c.Keep.IndividuallyReviewRequested,
		"keep.authored_by_user":                c.Keep.AuthoredByUser,
		"keep.team_mentioned_discussions":      c.Keep.TeamMentionedDiscussions,
		"unsubscribe.all_other_notifications":  c.Unsubscribe.AllOtherNotifications,
		"output.include_keep_decisions":        c.Output.IncludeKeepDecisions,
		"output.include_unsubscribe_decisions": c.Output.IncludeUnsubscribeDecisions,
		"output.include_decision_reasons":      c.Output.IncludeDecisionReasons,
	}
	for field, value := range required {
		if value == nil {
			validationErrors = append(validationErrors, fmt.Errorf("%s is required", field))
		}
	}

	if c.Unsubscribe.AllOtherNotifications != nil && !*c.Unsubscribe.AllOtherNotifications {
		validationErrors = append(validationErrors, errors.New("unsubscribe.all_other_notifications must be true in v1"))
	}
	for field, value := range map[string]*bool{
		"output.include_keep_decisions":        c.Output.IncludeKeepDecisions,
		"output.include_unsubscribe_decisions": c.Output.IncludeUnsubscribeDecisions,
		"output.include_decision_reasons":      c.Output.IncludeDecisionReasons,
	} {
		if value != nil && !*value {
			validationErrors = append(validationErrors, fmt.Errorf("%s must be true in v1", field))
		}
	}

	seenTeams := make(map[string]struct{}, len(c.DiscussionTeamSlugs))
	for _, team := range c.DiscussionTeamSlugs {
		if !teamPattern.MatchString(team) {
			validationErrors = append(validationErrors, fmt.Errorf("discussion_team_slugs entry %q must use org/team-slug form", team))
			continue
		}
		org := strings.SplitN(team, "/", 2)[0]
		if !strings.EqualFold(org, c.GitHubOrganization) {
			validationErrors = append(validationErrors, fmt.Errorf("discussion_team_slugs entry %q must belong to github_organization %q", team, c.GitHubOrganization))
		}
		key := strings.ToLower(team)
		if _, exists := seenTeams[key]; exists {
			validationErrors = append(validationErrors, fmt.Errorf("discussion_team_slugs contains duplicate %q", team))
		}
		seenTeams[key] = struct{}{}
	}

	return errors.Join(validationErrors...)
}

// Enabled reports whether a required policy flag is enabled.
func Enabled(value *bool) bool {
	return value != nil && *value
}
