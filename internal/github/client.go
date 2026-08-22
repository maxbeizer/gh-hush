package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/maxbeizer/gh-hush/internal/model"
)

const maxResponseBytes = 10 << 20

type commandRunner func(context.Context, ...string) ([]byte, error)

// CLIClient uses the authenticated gh CLI session for GitHub API requests.
type CLIClient struct {
	httpClient    *http.Client
	token         string
	commandRunner commandRunner
}

// NewCLIClient creates a GitHub client backed by gh api.
func NewCLIClient(ctx context.Context) (*CLIClient, error) {
	token, err := runGH(ctx, "auth", "token")
	if err != nil {
		return nil, err
	}
	trimmedToken := strings.TrimSpace(string(token))
	if trimmedToken == "" {
		return nil, errors.New("gh auth token returned an empty token")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 16
	return &CLIClient{
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
		token:         trimmedToken,
		commandRunner: runGH,
	}, nil
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
	output, err := c.run(ctx, "api", "--paginate", "--slurp", "-H", "Accept: application/vnd.github+json", "/notifications?per_page=100")
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

// Enrich fetches only the evidence required by the enabled policy.
func (c *CLIClient) Enrich(ctx context.Context, thread model.Notification, requirements model.EnrichmentRequirements) model.Enrichment {
	var enrichment model.Enrichment
	if requirements.Subject {
		if thread.Subject.URL == "" {
			enrichment.SubjectErr = errors.New("notification subject did not include an API URL")
		} else if err := c.get(ctx, thread.Subject.URL, &enrichment.Subject); err != nil {
			enrichment.SubjectErr = fmt.Errorf("fetch subject: %w", err)
		}
	}
	if requirements.LatestComment && thread.Subject.LatestCommentURL != "" {
		if err := c.get(ctx, thread.Subject.LatestCommentURL, &enrichment.LatestComment); err != nil {
			enrichment.LatestCommentErr = fmt.Errorf("fetch latest comment: %w", err)
		}
	}
	return enrichment
}

func (c *CLIClient) get(ctx context.Context, endpoint string, target any) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse GitHub API endpoint %q: %w", endpoint, err)
	}
	if parsed.IsAbs() {
		return c.getHTTP(ctx, parsed, target)
	}

	output, err := c.run(ctx, "api", "-H", "Accept: application/vnd.github+json", endpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("decode GitHub API response for %q: %w", endpoint, err)
	}
	return nil
}

func (c *CLIClient) getHTTP(ctx context.Context, endpoint *url.URL, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create GitHub API request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("User-Agent", "gh-hush")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request GitHub API endpoint %q: %w", endpoint.Redacted(), err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read GitHub API response for %q: %w", endpoint.Redacted(), err)
	}
	if len(body) > maxResponseBytes {
		return fmt.Errorf("GitHub API response for %q exceeded %d bytes", endpoint.Redacted(), maxResponseBytes)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf(
			"GitHub API request for %q returned %s (request ID %q)",
			endpoint.Redacted(),
			response.Status,
			response.Header.Get("X-GitHub-Request-Id"),
		)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode GitHub API response for %q: %w", endpoint.Redacted(), err)
	}
	return nil
}

func (c *CLIClient) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.commandRunner != nil {
		return c.commandRunner(ctx, args...)
	}
	return runGH(ctx, args...)
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
