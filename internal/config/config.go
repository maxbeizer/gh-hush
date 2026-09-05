package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	loginPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	teamSlugPattern     = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	unknownFieldPattern = regexp.MustCompile(`^line ([0-9]+): field ([^ ]+) not found in type .+$`)
)

const configSchemaURL = "https://github.com/maxbeizer/gh-hush/blob/main/config.schema.json"

func DefaultPath() (string, error) {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		if !filepath.IsAbs(configHome) {
			return "", errors.New("XDG_CONFIG_HOME must be an absolute path")
		}
		return filepath.Join(configHome, "gh-hush", "config.yml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "gh-hush", "config.yml"), nil
}

// Config is the complete notification policy. Unknown YAML fields are rejected.
type Config struct {
	User               string   `yaml:"user"`
	GitHubOrganization string   `yaml:"github_organization"`
	TeamSlugs          []string `yaml:"team_slugs"`
	Keep               Keep     `yaml:"keep"`
	Hush               struct {
		AllOtherNotifications *bool `yaml:"all_other_notifications"`
	} `yaml:"hush"`
}

type Keep struct {
	ExternalOrganizationIssues            *bool `yaml:"external_organization_issues"`
	PersonallyMentioned                   *bool `yaml:"personally_mentioned"`
	PersonallyAssigned                    *bool `yaml:"personally_assigned"`
	IndividuallyReviewRequested           *bool `yaml:"individually_review_requested"`
	ActiveTeamReviewRequestedPullRequests *bool `yaml:"active_team_review_requested_pull_requests"`
	AuthoredByUser                        *bool `yaml:"authored_by_user"`
	TeamMentionedDiscussions              *bool `yaml:"team_mentioned_discussions"`
}

func Load(path string) (Config, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, nil, fmt.Errorf("read config %q: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return Config{}, nil, fmt.Errorf("validate config %q: %w\nAI prompt: %s", path, err, configFixPrompt(path, err))
	}
	return cfg, data, nil
}

func Parse(data []byte) (Config, error) {
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, configDecodeError(err)
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

func (c Config) Validate() error {
	var validationErrors []error
	if !validGitHubLogin(c.User) {
		validationErrors = append(validationErrors, errors.New("user must be a valid GitHub login"))
	}
	if !validGitHubLogin(c.GitHubOrganization) {
		validationErrors = append(validationErrors, errors.New("github_organization must be a valid GitHub organization login"))
	}
	required := []struct {
		name  string
		value *bool
	}{
		{"keep.external_organization_issues", c.Keep.ExternalOrganizationIssues},
		{"keep.personally_mentioned", c.Keep.PersonallyMentioned},
		{"keep.personally_assigned", c.Keep.PersonallyAssigned},
		{"keep.individually_review_requested", c.Keep.IndividuallyReviewRequested},
		{"keep.active_team_review_requested_pull_requests", c.Keep.ActiveTeamReviewRequestedPullRequests},
		{"keep.authored_by_user", c.Keep.AuthoredByUser},
		{"keep.team_mentioned_discussions", c.Keep.TeamMentionedDiscussions},
		{"hush.all_other_notifications", c.Hush.AllOtherNotifications},
	}
	for _, field := range required {
		if field.value == nil {
			validationErrors = append(validationErrors, fmt.Errorf("%s is required", field.name))
		}
	}
	if c.Hush.AllOtherNotifications != nil && !*c.Hush.AllOtherNotifications {
		validationErrors = append(validationErrors, errors.New("hush.all_other_notifications must be true"))
	}

	seenTeams := make(map[string]struct{}, len(c.TeamSlugs))
	for _, team := range c.TeamSlugs {
		parts := strings.Split(team, "/")
		if len(parts) != 2 || !validGitHubLogin(parts[0]) || !teamSlugPattern.MatchString(parts[1]) {
			validationErrors = append(validationErrors, fmt.Errorf("team_slugs entry %q must use org/team-slug form", team))
			continue
		}
		if !strings.EqualFold(parts[0], c.GitHubOrganization) {
			validationErrors = append(validationErrors, fmt.Errorf("team_slugs entry %q must belong to github_organization %q", team, c.GitHubOrganization))
		}
		key := strings.ToLower(team)
		if _, exists := seenTeams[key]; exists {
			validationErrors = append(validationErrors, fmt.Errorf("team_slugs contains duplicate %q", team))
		}
		seenTeams[key] = struct{}{}
	}
	return errors.Join(validationErrors...)
}

func configFixPrompt(path string, err error) string {
	if strings.Contains(err.Error(), `"discussion_team_slugs" was renamed to "team_slugs"`) {
		return fmt.Sprintf("In %q, rename discussion_team_slugs to team_slugs and add keep.active_team_review_requested_pull_requests as true or false.", path)
	}
	return fmt.Sprintf("Fix the configuration errors above in %q, preserving the policy's intent.", path)
}

func configDecodeError(err error) error {
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return fmt.Errorf("decode YAML: %w", err)
	}

	problems := make([]string, 0, len(typeErr.Errors))
	for _, problem := range typeErr.Errors {
		match := unknownFieldPattern.FindStringSubmatch(problem)
		if match == nil {
			problems = append(problems, problem)
			continue
		}
		line, field := match[1], match[2]
		switch field {
		case "discussion_team_slugs":
			problems = append(problems, fmt.Sprintf(`line %s: "discussion_team_slugs" was renamed to "team_slugs" in v0.2.0; also add "active_team_review_requested_pull_requests: true" (or false) under "keep"`, line))
		case "unsubscribe":
			problems = append(problems, fmt.Sprintf(`line %s: "unsubscribe" was replaced by "hush"`, line))
		case "run_mode", "output":
			problems = append(problems, fmt.Sprintf(`line %s: %q is no longer supported and must be removed`, line, field))
		default:
			problems = append(problems, fmt.Sprintf(`line %s: unknown configuration field %q; see %s`, line, field, configSchemaURL))
		}
	}
	return fmt.Errorf("decode YAML:\n  %s", strings.Join(problems, "\n  "))
}

func validGitHubLogin(login string) bool {
	return loginPattern.MatchString(login) && !strings.HasSuffix(login, "-") && !strings.Contains(login, "--")
}

func Enabled(value *bool) bool { return value != nil && *value }
