package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/maxbeizer/gh-hush/internal/diagnostic"
	"github.com/maxbeizer/gh-hush/internal/github/transport"
	"github.com/maxbeizer/gh-hush/internal/model"
)

type apiTransport interface {
	Request(context.Context, string, string) (transport.Response, error)
	Pages(context.Context, string, func(transport.Response) error) error
}

type CLIClient struct {
	transport apiTransport
}

func NewCLIClient(ctx context.Context) (*CLIClient, error) {
	return newCLIClient(ctx, runGH)
}

func newCLIClient(ctx context.Context, runner func(context.Context, ...string) ([]byte, error)) (*CLIClient, error) {
	// The API origin below is GitHub.com, so select that host explicitly. Without
	// --hostname, GH_HOST could make gh return an enterprise token that must never
	// be sent to api.github.com.
	token, err := runner(ctx, "auth", "token", "--hostname", "github.com")
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(token))
	if trimmed == "" {
		return nil, errors.New("gh auth token returned an empty token")
	}
	httpTransport := http.DefaultTransport.(*http.Transport).Clone()
	httpTransport.MaxIdleConns = 16
	httpTransport.MaxIdleConnsPerHost = 16
	return &CLIClient{transport: transport.New(transport.Config{
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: httpTransport},
		Token:      trimmed,
		BaseURL:    "https://api.github.com",
	})}, nil
}

func (c *CLIClient) CurrentUser(ctx context.Context) (string, error) {
	var user model.User
	if err := c.get(ctx, "/user", &user); err != nil {
		return "", err
	}
	if user.Login == "" {
		return "", errors.New("GitHub API returned an empty authenticated login")
	}
	return user.Login, nil
}

// ListNotifications returns unread notifications. GitHub's all=true listing
// also includes historical threads marked Done, and the REST representation
// does not distinguish those threads from read notifications still in the
// inbox. Restricting discovery to unread notifications avoids reprocessing
// that ambiguous history.
func (c *CLIClient) ListNotifications(ctx context.Context) ([]model.Notification, error) {
	var notifications []model.Notification
	if err := getPages(c.transport, ctx, "/notifications?per_page=100", &notifications); err != nil {
		return nil, fmt.Errorf("list unread notifications: %w", err)
	}
	return notifications, nil
}

// GetNotification returns the thread record, which may be historical and
// already marked Done. found means only that the record is retrievable; callers
// must not interpret it as proof that the thread is currently in the inbox.
// A missing thread record is reported with found=false.
func (c *CLIClient) GetNotification(ctx context.Context, threadID string) (notification model.Notification, found bool, err error) {
	ctx = diagnostic.WithThread(ctx, threadID)
	endpoint, err := notificationThreadEndpoint(threadID)
	if err != nil {
		return model.Notification{}, false, err
	}
	if err := c.get(ctx, endpoint, &notification); err != nil {
		var apiErr *transport.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return model.Notification{}, false, nil
		}
		return model.Notification{}, false, fmt.Errorf("get notification thread %q: %w", threadID, err)
	}
	return notification, true, nil
}

func (c *CLIClient) UnsubscribeThread(ctx context.Context, threadID string) error {
	ctx = diagnostic.WithThread(ctx, threadID)
	endpoint, err := notificationThreadEndpoint(threadID)
	if err != nil {
		return err
	}
	if _, err := c.transport.Request(ctx, http.MethodDelete, endpoint+"/subscription"); err != nil {
		return fmt.Errorf("unsubscribe notification thread %q: %w", threadID, err)
	}
	return nil
}

func (c *CLIClient) MarkThreadDone(ctx context.Context, threadID string) error {
	ctx = diagnostic.WithThread(ctx, threadID)
	endpoint, err := notificationThreadEndpoint(threadID)
	if err != nil {
		return err
	}
	if _, err := c.transport.Request(ctx, http.MethodDelete, endpoint); err != nil {
		return fmt.Errorf("mark notification thread %q Done: %w", threadID, err)
	}
	return nil
}

func notificationThreadEndpoint(threadID string) (string, error) {
	if strings.TrimSpace(threadID) == "" {
		return "", errors.New("notification thread ID is empty")
	}
	return "/notifications/threads/" + url.PathEscape(threadID), nil
}

// Enrich fetches only required evidence. Discussion comments use the REST
// endpoint GET /repos/{owner}/{repo}/discussions/{number}/comments and follow
// every Link page; only comment bodies are retained by the model.
func (c *CLIClient) Enrich(ctx context.Context, thread model.Notification, requirements model.EnrichmentRequirements) model.Enrichment {
	ctx = diagnostic.WithThread(ctx, thread.ID)
	var enrichment model.Enrichment
	if requirements.Subject {
		if thread.Subject.URL == "" {
			enrichment.SubjectErr = errors.New("notification subject did not include an API URL")
		} else if err := c.get(ctx, thread.Subject.URL, &enrichment.Subject); err != nil {
			enrichment.SubjectErr = fmt.Errorf("fetch subject: %w", err)
		}
	}
	if requirements.DiscussionComments {
		if thread.Subject.URL == "" {
			enrichment.DiscussionCommentsErr = errors.New("notification Discussion did not include an API URL")
		} else {
			commentsURL := strings.TrimRight(thread.Subject.URL, "/") + "/comments?per_page=100"
			if err := getPages(c.transport, ctx, commentsURL, &enrichment.DiscussionComments); err != nil {
				enrichment.DiscussionCommentsErr = fmt.Errorf("fetch complete Discussion comment history: %w", err)
			}
		}
	}
	return enrichment
}

func (c *CLIClient) get(ctx context.Context, endpoint string, target any) error {
	response, err := c.transport.Request(ctx, http.MethodGet, endpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(response.Body, target); err != nil {
		return fmt.Errorf("decode GitHub API response for %q: %w", response.Endpoint, err)
	}
	return nil
}

func getPages(t apiTransport, ctx context.Context, endpoint string, target any) error {
	return t.Pages(ctx, endpoint, func(response transport.Response) error {
		switch out := target.(type) {
		case *[]model.Notification:
			var page []model.Notification
			if err := json.Unmarshal(response.Body, &page); err != nil {
				return fmt.Errorf("decode paginated GitHub API response for %q: %w", response.Endpoint, err)
			}
			*out = append(*out, page...)
		case *[]model.Resource:
			var page []model.Resource
			if err := json.Unmarshal(response.Body, &page); err != nil {
				return fmt.Errorf("decode paginated GitHub API response for %q: %w", response.Endpoint, err)
			}
			*out = append(*out, page...)
		default:
			return errors.New("unsupported paginated response target")
		}
		return nil
	})
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
