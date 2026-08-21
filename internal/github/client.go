package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/maxbeizer/gh-hush/internal/model"
)

// CLIClient uses the authenticated gh CLI session for GitHub API requests.
type CLIClient struct{}

// NewCLIClient creates a GitHub client backed by gh api.
func NewCLIClient() *CLIClient {
	return &CLIClient{}
}

// CurrentUser returns the login authenticated by gh.
func (c *CLIClient) CurrentUser(ctx context.Context) (string, error) {
	var user model.User
	if err := c.get(ctx, "user", &user); err != nil {
		return "", err
	}
	if user.Login == "" {
		return "", errors.New("GitHub API returned an empty authenticated login")
	}
	return user.Login, nil
}

// ListNotifications fetches every page of the authenticated user's notifications.
func (c *CLIClient) ListNotifications(ctx context.Context) ([]model.Notification, error) {
	output, err := runGH(ctx, "api", "--paginate", "--slurp", "-H", "Accept: application/vnd.github+json", "/notifications?per_page=100")
	if err != nil {
		return nil, err
	}

	var pages [][]model.Notification
	if err := json.Unmarshal(output, &pages); err != nil {
		return nil, fmt.Errorf("decode paginated notifications: %w", err)
	}
	var notifications []model.Notification
	for _, page := range pages {
		notifications = append(notifications, page...)
	}
	return notifications, nil
}

// Enrich fetches evidence needed by the deterministic keep rules.
func (c *CLIClient) Enrich(ctx context.Context, thread model.Notification) model.Enrichment {
	if !requiresEnrichment(thread.Subject.Type) {
		return model.Enrichment{}
	}
	if thread.Subject.URL == "" {
		return model.Enrichment{Err: errors.New("notification subject did not include an API URL")}
	}

	var enrichment model.Enrichment
	if err := c.get(ctx, thread.Subject.URL, &enrichment.Subject); err != nil {
		enrichment.Err = fmt.Errorf("fetch subject: %w", err)
		return enrichment
	}
	if thread.Subject.LatestCommentURL != "" {
		if err := c.get(ctx, thread.Subject.LatestCommentURL, &enrichment.LatestComment); err != nil {
			enrichment.Err = fmt.Errorf("fetch latest comment: %w", err)
		}
	}
	return enrichment
}

func (c *CLIClient) get(ctx context.Context, endpoint string, target any) error {
	apiEndpoint, err := normalizeEndpoint(endpoint)
	if err != nil {
		return err
	}
	output, err := runGH(ctx, "api", "-H", "Accept: application/vnd.github+json", apiEndpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("decode GitHub API response for %q: %w", endpoint, err)
	}
	return nil
}

func normalizeEndpoint(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse GitHub API endpoint %q: %w", endpoint, err)
	}
	if !parsed.IsAbs() {
		return endpoint, nil
	}
	if parsed.Path == "" {
		return "", fmt.Errorf("GitHub API endpoint %q has no path", endpoint)
	}
	if parsed.RawQuery != "" {
		return parsed.EscapedPath() + "?" + parsed.RawQuery, nil
	}
	return parsed.EscapedPath(), nil
}

func requiresEnrichment(subjectType string) bool {
	switch subjectType {
	case "Issue", "PullRequest", "Discussion":
		return true
	default:
		return false
	}
}

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "gh", args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), message)
	}
	return output, nil
}
