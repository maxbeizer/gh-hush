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

	"github.com/maxbeizer/gh-hush/internal/predicate"
	"gopkg.in/yaml.v3"
)

var (
	loginPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	teamSlugPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

const (
	configSchemaURL = "https://github.com/maxbeizer/gh-hush/blob/main/config.schema.json"

	// Version is the only configuration schema version this release accepts.
	Version = 3
)

// Actions are the two terminal outcomes a rule or the default may select.
const (
	ActionKeep = "keep"
	ActionHush = "hush"
)

// Missing-evidence postures decide what happens when a rule needs GitHub
// evidence that could not be fetched.
const (
	OnMissingKeep = "keep"
	OnMissingHush = "hush"
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

// Config is the complete v3 notification policy: identity, a global safety
// posture and terminal fallback, and an ordered list of match/action rules
// evaluated first-match-wins. Unknown YAML fields are rejected.
type Config struct {
	Version  int      `yaml:"version"`
	Identity Identity `yaml:"identity"`
	Defaults Defaults `yaml:"defaults"`
	Rules    []Rule   `yaml:"rules"`
}

// Identity is the acting user, their primary organization, and their teams.
type Identity struct {
	User         string   `yaml:"user"`
	Organization string   `yaml:"organization"`
	Teams        []string `yaml:"teams"`
}

// Defaults hold the terminal fallback action and the global safety posture
// applied when a rule's required evidence is unavailable.
type Defaults struct {
	Action            string `yaml:"action"`
	OnMissingEvidence string `yaml:"on_missing_evidence"`
}

// Rule is one user-authored match/action entry. name is the report identity,
// action is keep or hush, and when is the predicate tree. An omitted when
// matches every notification, which makes the rule an unconditional catch-all.
type Rule struct {
	Name   string         `yaml:"name"`
	Action string         `yaml:"action"`
	When   predicate.Node `yaml:"when"`
}

// PredicateIdentity projects the configured identity into the predicate layer.
func (c Config) PredicateIdentity() predicate.Identity {
	return predicate.Identity{User: c.Identity.User, Organization: c.Identity.Organization, Teams: c.Identity.Teams}
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
	if c.Version != Version {
		validationErrors = append(validationErrors, fmt.Errorf("version must be %d; run gh hush migrate-config to upgrade an older file", Version))
	}
	if !validGitHubLogin(c.Identity.User) {
		validationErrors = append(validationErrors, errors.New("identity.user must be a valid GitHub login"))
	}
	if !validGitHubLogin(c.Identity.Organization) {
		validationErrors = append(validationErrors, errors.New("identity.organization must be a valid GitHub organization login"))
	}
	validationErrors = append(validationErrors, c.validateTeams()...)

	switch c.Defaults.Action {
	case ActionKeep, ActionHush:
	case "":
		validationErrors = append(validationErrors, errors.New("defaults.action is required and must be keep or hush"))
	default:
		validationErrors = append(validationErrors, fmt.Errorf("defaults.action %q must be keep or hush", c.Defaults.Action))
	}
	switch c.Defaults.OnMissingEvidence {
	case OnMissingKeep, OnMissingHush:
	case "":
		validationErrors = append(validationErrors, errors.New("defaults.on_missing_evidence is required and must be keep or hush"))
	default:
		validationErrors = append(validationErrors, fmt.Errorf("defaults.on_missing_evidence %q must be keep or hush", c.Defaults.OnMissingEvidence))
	}

	seenNames := make(map[string]struct{}, len(c.Rules))
	for i, rule := range c.Rules {
		if strings.TrimSpace(rule.Name) == "" {
			validationErrors = append(validationErrors, fmt.Errorf("rules[%d].name is required", i))
		} else if _, exists := seenNames[strings.ToLower(rule.Name)]; exists {
			validationErrors = append(validationErrors, fmt.Errorf("rules contains duplicate name %q", rule.Name))
		} else {
			seenNames[strings.ToLower(rule.Name)] = struct{}{}
		}
		switch rule.Action {
		case ActionKeep, ActionHush:
		case "":
			validationErrors = append(validationErrors, fmt.Errorf("rules[%d] (%q) action is required and must be keep or hush", i, rule.Name))
		default:
			validationErrors = append(validationErrors, fmt.Errorf("rules[%d] (%q) action %q must be keep or hush", i, rule.Name, rule.Action))
		}
	}
	return errors.Join(validationErrors...)
}

func (c Config) validateTeams() []error {
	var validationErrors []error
	seenTeams := make(map[string]struct{}, len(c.Identity.Teams))
	for _, team := range c.Identity.Teams {
		parts := strings.Split(team, "/")
		if len(parts) != 2 || !validGitHubLogin(parts[0]) || !teamSlugPattern.MatchString(parts[1]) {
			validationErrors = append(validationErrors, fmt.Errorf("identity.teams entry %q must use org/team-slug form", team))
			continue
		}
		if !strings.EqualFold(parts[0], c.Identity.Organization) {
			validationErrors = append(validationErrors, fmt.Errorf("identity.teams entry %q must belong to identity.organization %q", team, c.Identity.Organization))
		}
		key := strings.ToLower(team)
		if _, exists := seenTeams[key]; exists {
			validationErrors = append(validationErrors, fmt.Errorf("identity.teams contains duplicate %q", team))
		}
		seenTeams[key] = struct{}{}
	}
	return validationErrors
}

func configFixPrompt(path string, err error) string {
	message := err.Error()
	if strings.Contains(message, "migrate-config") || strings.Contains(message, "older schema") || strings.Contains(message, "version must be") || strings.Contains(message, "discussion_team_slugs") {
		return fmt.Sprintf("The configuration at %q uses an older schema. Run: gh hush migrate-config --config %q to rewrite it as version %d.", path, path, Version)
	}
	return fmt.Sprintf("Fix the configuration errors above in %q, preserving the policy's intent.", path)
}

func configDecodeError(err error) error {
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return fmt.Errorf("decode YAML: %w", err)
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
		case "user", "keep", "hush", "watched_repositories", "team_slugs", "github_organization":
			problems = append(problems, fmt.Sprintf(`line %s: %q belongs to an older schema; run gh hush migrate-config to upgrade to version %d`, line, field, Version))
		default:
			problems = append(problems, fmt.Sprintf(`line %s: unknown configuration field %q; see %s`, line, field, configSchemaURL))
		}
	}
	return fmt.Errorf("decode YAML:\n  %s", strings.Join(problems, "\n  "))
}

func validGitHubLogin(login string) bool {
	return loginPattern.MatchString(login) && !strings.HasSuffix(login, "-") && !strings.Contains(login, "--")
}
