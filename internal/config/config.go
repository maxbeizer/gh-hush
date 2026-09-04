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
	loginPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	teamSlugPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

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
		return Config{}, nil, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, data, nil
}

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

func (c Config) Validate() error {
	var validationErrors []error
	if !validGitHubLogin(c.User) {
		validationErrors = append(validationErrors, errors.New("user must be a valid GitHub login"))
	}
	if !validGitHubLogin(c.GitHubOrganization) {
		validationErrors = append(validationErrors, errors.New("github_organization must be a valid GitHub organization login"))
	}
	required := map[string]*bool{
		"keep.external_organization_issues":               c.Keep.ExternalOrganizationIssues,
		"keep.personally_mentioned":                       c.Keep.PersonallyMentioned,
		"keep.personally_assigned":                        c.Keep.PersonallyAssigned,
		"keep.individually_review_requested":              c.Keep.IndividuallyReviewRequested,
		"keep.active_team_review_requested_pull_requests": c.Keep.ActiveTeamReviewRequestedPullRequests,
		"keep.authored_by_user":                           c.Keep.AuthoredByUser,
		"keep.team_mentioned_discussions":                 c.Keep.TeamMentionedDiscussions,
		"hush.all_other_notifications":                    c.Hush.AllOtherNotifications,
	}
	for field, value := range required {
		if value == nil {
			validationErrors = append(validationErrors, fmt.Errorf("%s is required", field))
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

func validGitHubLogin(login string) bool {
	return loginPattern.MatchString(login) && !strings.HasSuffix(login, "-") && !strings.Contains(login, "--")
}

func Enabled(value *bool) bool { return value != nil && *value }
