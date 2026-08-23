package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/maxbeizer/gh-hush/internal/diagnostic"
	"github.com/maxbeizer/gh-hush/internal/model"
)

const (
	maxResponseBytes = 10 << 20
	maxAttempts      = 3
)

type sleepFunc func(context.Context, time.Duration) error

type CLIClient struct {
	httpClient *http.Client
	token      string
	apiBase    string
	sleep      sleepFunc
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 16
	return &CLIClient{
		httpClient: &http.Client{Timeout: 30 * time.Second, Transport: transport},
		token:      trimmed,
		apiBase:    "https://api.github.com",
	}, nil
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
	if err := c.getPages(ctx, "/notifications?per_page=100", &notifications); err != nil {
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
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound {
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
	if err := c.mutate(ctx, http.MethodDelete, endpoint+"/subscription"); err != nil {
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
	if err := c.mutate(ctx, http.MethodDelete, endpoint); err != nil {
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

// FetchSubject acquires the subject resource requested by policy evaluation.
func (c *CLIClient) FetchSubject(ctx context.Context, thread model.Notification) (model.Resource, error) {
	ctx = diagnostic.WithThread(ctx, thread.ID)
	if thread.Subject.URL == "" {
		return model.Resource{}, errors.New("notification subject did not include an API URL")
	}
	var subject model.Resource
	if err := c.get(ctx, thread.Subject.URL, &subject); err != nil {
		return subject, fmt.Errorf("fetch subject: %w", err)
	}
	return subject, nil
}

// FetchDiscussionComments acquires the complete Discussion comment history.
// It follows every REST Link page and retains only fields represented by the
// common resource model.
func (c *CLIClient) FetchDiscussionComments(ctx context.Context, thread model.Notification) ([]model.Resource, error) {
	ctx = diagnostic.WithThread(ctx, thread.ID)
	if thread.Subject.URL == "" {
		return nil, errors.New("notification Discussion did not include an API URL")
	}
	commentsURL := strings.TrimRight(thread.Subject.URL, "/") + "/comments?per_page=100"
	var comments []model.Resource
	if err := c.getPages(ctx, commentsURL, &comments); err != nil {
		return comments, fmt.Errorf("fetch complete Discussion comment history: %w", err)
	}
	return comments, nil
}

func (c *CLIClient) get(ctx context.Context, endpoint string, target any) error {
	body, _, effectiveURL, err := c.request(ctx, http.MethodGet, endpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode GitHub API response for %q: %w", effectiveURL, err)
	}
	return nil
}

func (c *CLIClient) getPages(ctx context.Context, endpoint string, target any) error {
	next := endpoint
	for next != "" {
		requestURL, err := c.resolve(next)
		if err != nil {
			return err
		}
		body, header, effectiveURL, err := c.request(ctx, http.MethodGet, requestURL)
		if err != nil {
			return err
		}
		switch out := target.(type) {
		case *[]model.Notification:
			var page []model.Notification
			if err := json.Unmarshal(body, &page); err != nil {
				return fmt.Errorf("decode paginated GitHub API response for %q: %w", effectiveURL, err)
			}
			*out = append(*out, page...)
		case *[]model.Resource:
			var page []model.Resource
			if err := json.Unmarshal(body, &page); err != nil {
				return fmt.Errorf("decode paginated GitHub API response for %q: %w", effectiveURL, err)
			}
			*out = append(*out, page...)
		default:
			return errors.New("unsupported paginated response target")
		}
		next = nextLink(header.Get("Link"))
		if next != "" {
			next, err = c.resolvePageLink(effectiveURL, next)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *CLIClient) mutate(ctx context.Context, method, endpoint string) error {
	_, _, _, err := c.request(ctx, method, endpoint)
	return err
}

type apiError struct {
	status                          int
	statusText, requestID, endpoint string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("GitHub API request for %q returned %s (request ID %q)", e.endpoint, e.statusText, e.requestID)
}

func (c *CLIClient) request(ctx context.Context, method, endpoint string) ([]byte, http.Header, string, error) {
	requestURL, err := c.resolve(endpoint)
	if err != nil {
		return nil, nil, "", err
	}
	safeEndpoint := sanitizedEndpoint(requestURL)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			diagnostic.Log(ctx, "request_cancelled", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt))
			return nil, nil, "", err
		}
		diagnostic.Log(ctx, "request_start", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt))
		request, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
		if err != nil {
			return nil, nil, "", fmt.Errorf("create GitHub API request: %w", err)
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("User-Agent", "gh-hush")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		response, requestErr := c.do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				diagnostic.Log(ctx, "request_cancelled", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt))
				return nil, nil, "", ctx.Err()
			}
			retryable := retryableNetworkError(requestErr)
			diagnostic.Log(ctx, "request_failed", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt), diagnostic.String("kind", "network"), diagnostic.Bool("retryable", retryable))
			if !retryable {
				return nil, nil, "", fmt.Errorf("request GitHub API endpoint %q: %w", requestURL, requestErr)
			}
			if attempt == maxAttempts {
				return nil, nil, "", fmt.Errorf("request GitHub API endpoint %q exhausted %d attempts: %w", requestURL, maxAttempts, requestErr)
			}
			delay := backoff(attempt)
			diagnostic.Log(ctx, "retry_scheduled", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt), diagnostic.Int("delay_ms", int(delay/time.Millisecond)))
			if err := c.wait(ctx, delay); err != nil {
				diagnostic.Log(ctx, "request_cancelled", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt))
				return nil, nil, "", err
			}
			continue
		}
		effectiveURL := requestURL
		if response.Request != nil && response.Request.URL != nil {
			effectiveURL = response.Request.URL.Redacted()
		}
		diagnostic.Log(ctx, "response", responseFields(method, sanitizedEndpoint(effectiveURL), attempt, response)...)
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		_ = response.Body.Close()
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, nil, effectiveURL, ctx.Err()
			}
			if !retryableNetworkError(readErr) {
				return nil, nil, effectiveURL, fmt.Errorf("read GitHub API response for %q: %w", effectiveURL, readErr)
			}
			if attempt == maxAttempts {
				return nil, nil, effectiveURL, fmt.Errorf("read GitHub API response for %q exhausted %d attempts: %w", effectiveURL, maxAttempts, readErr)
			}
			if err := c.wait(ctx, backoff(attempt)); err != nil {
				return nil, nil, effectiveURL, err
			}
			continue
		}
		if len(body) > maxResponseBytes {
			return nil, nil, effectiveURL, fmt.Errorf("GitHub API response for %q exceeded %d bytes", effectiveURL, maxResponseBytes)
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return body, response.Header, effectiveURL, nil
		}
		apiErr := &apiError{status: response.StatusCode, statusText: response.Status, requestID: response.Header.Get("X-GitHub-Request-Id"), endpoint: effectiveURL}
		if !retryableStatus(response.StatusCode) {
			return nil, response.Header, effectiveURL, apiErr
		}
		if attempt == maxAttempts {
			return nil, response.Header, effectiveURL, fmt.Errorf("GitHub API request exhausted %d attempts: %w", maxAttempts, apiErr)
		}
		delay := retryDelay(response.Header, attempt)
		diagnostic.Log(ctx, "retry_scheduled", diagnostic.String("method", method), diagnostic.String("endpoint", sanitizedEndpoint(effectiveURL)), diagnostic.Int("attempt", attempt), diagnostic.Int("delay_ms", int(delay/time.Millisecond)))
		if err := c.wait(ctx, delay); err != nil {
			diagnostic.Log(ctx, "request_cancelled", diagnostic.String("method", method), diagnostic.String("endpoint", sanitizedEndpoint(effectiveURL)), diagnostic.Int("attempt", attempt))
			return nil, nil, effectiveURL, err
		}
	}
	panic("unreachable")
}

func sanitizedEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.EscapedPath() == "" {
		return "[invalid]"
	}
	// Query values can contain credentials or notification content. Request
	// diagnostics need only the operation endpoint, so omit the query entirely.
	return parsed.EscapedPath()
}

func responseFields(method, endpoint string, attempt int, response *http.Response) []diagnostic.Field {
	return []diagnostic.Field{
		diagnostic.String("method", method),
		diagnostic.String("endpoint", endpoint),
		diagnostic.Int("attempt", attempt),
		diagnostic.Int("status", response.StatusCode),
		diagnostic.String("request_id", response.Header.Get("X-GitHub-Request-Id")),
		diagnostic.String("rate_limit_remaining", response.Header.Get("X-RateLimit-Remaining")),
		diagnostic.String("rate_limit_reset", response.Header.Get("X-RateLimit-Reset")),
		diagnostic.String("retry_after", response.Header.Get("Retry-After")),
	}
}

func (c *CLIClient) resolve(endpoint string) (string, error) {
	base := c.apiBase
	if base == "" {
		base = "https://api.github.com"
	}
	baseURL, err := url.Parse(strings.TrimRight(base, "/") + "/")
	if err != nil {
		return "", fmt.Errorf("parse GitHub API base URL %q: %w", base, err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse GitHub API endpoint %q: %w", endpoint, err)
	}
	resolved := baseURL.ResolveReference(parsed)
	if baseURL.Opaque != "" || baseURL.User != nil {
		return "", fmt.Errorf("refuse malformed GitHub API base URL %q", baseURL.Redacted())
	}
	if resolved.Opaque != "" || resolved.User != nil {
		return "", fmt.Errorf("refuse GitHub API endpoint with opaque URL or userinfo %q", resolved.Redacted())
	}
	if !sameOrigin(baseURL, resolved) {
		return "", fmt.Errorf("refuse GitHub API endpoint on unexpected origin %q", resolved.Redacted())
	}
	return resolved.String(), nil
}

func (c *CLIClient) resolvePageLink(currentURL, link string) (string, error) {
	current, err := url.Parse(currentURL)
	if err != nil {
		return "", fmt.Errorf("parse current GitHub API page URL %q: %w", currentURL, err)
	}
	reference, err := url.Parse(link)
	if err != nil {
		return "", fmt.Errorf("parse GitHub API pagination link %q: %w", link, err)
	}
	return c.resolve(current.ResolveReference(reference).String())
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && sameAuthority(left.Host, right.Host, left.Scheme)
}

// sameAuthority compares the authority that will be used on the wire. Host
// names are case-insensitive, equivalent IP spellings compare equal, and an
// omitted port is equivalent only to the scheme's default port.
func sameAuthority(left, right, scheme string) bool {
	leftHost, leftPort, leftOK := normalizedAuthority(left, scheme)
	rightHost, rightPort, rightOK := normalizedAuthority(right, scheme)
	if !leftOK || !rightOK || leftPort != rightPort {
		return false
	}
	leftIP, leftIPErr := netip.ParseAddr(leftHost)
	rightIP, rightIPErr := netip.ParseAddr(rightHost)
	if leftIPErr == nil || rightIPErr == nil {
		return leftIPErr == nil && rightIPErr == nil && leftIP == rightIP
	}
	return strings.EqualFold(leftHost, rightHost)
}

func normalizedAuthority(authority, scheme string) (string, string, bool) {
	parsed, err := url.Parse("//" + authority)
	if err != nil || parsed.Host != authority || parsed.User != nil || parsed.Hostname() == "" {
		return "", "", false
	}
	host := parsed.Hostname()
	// net/http converts Unicode DNS names with IDNA before dialing. Unicode
	// simple-fold equivalence is not authority equivalence (for example, Greek
	// sigma variants can map to different A-labels), so require callers to use
	// the unambiguous ASCII/Punycode spelling.
	for i := range len(host) {
		if host[i] >= 0x80 {
			return "", "", false
		}
	}
	// RFC 3986 IP literals must be bracketed. net/url otherwise interprets
	// the final IPv6 component as a port, and accepts an empty trailing port.
	if (strings.Contains(host, ":") && !strings.HasPrefix(authority, "[")) || strings.HasSuffix(authority, ":") {
		return "", "", false
	}
	port := parsed.Port()
	if port == "" {
		switch {
		case strings.EqualFold(scheme, "http"):
			port = "80"
		case strings.EqualFold(scheme, "https"):
			port = "443"
		default:
			return "", "", false
		}
	} else {
		numericPort, err := strconv.ParseUint(port, 10, 16)
		if err != nil || numericPort == 0 {
			return "", "", false
		}
		port = strconv.FormatUint(numericPort, 10)
	}
	return host, port, true
}

func (c *CLIClient) client() *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	return http.DefaultClient
}

type redirectPolicyError struct{ err error }

func (e *redirectPolicyError) Error() string { return e.err.Error() }
func (e *redirectPolicyError) Unwrap() error { return e.err }

func (c *CLIClient) do(request *http.Request) (*http.Response, error) {
	client := *c.client()
	originalCheckRedirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		expectedMethod := next.Method
		if len(via) > 0 {
			expectedMethod = via[len(via)-1].Method
		}
		validate := func() error {
			if next.URL == nil {
				return &redirectPolicyError{err: errors.New("refuse GitHub API redirect with no URL")}
			}
			// Opaque controls the HTTP request target (and, with an HTTP proxy,
			// can be interpreted as an absolute URI) independently of URL.Host.
			// Redirects created by net/http do not need it, so do not allow a
			// custom callback to use it to bypass the checked authority.
			if next.URL.Opaque != "" {
				return &redirectPolicyError{err: errors.New("refuse GitHub API redirect with an opaque URL")}
			}
			if _, err := c.resolve(next.URL.String()); err != nil {
				return &redirectPolicyError{err: err}
			}
			// Request.Host overrides the HTTP Host authority. Permit casing and
			// default-port spelling differences, but no authority that could
			// select a different virtual host for the Authorization-bearing request.
			if next.Host != "" && !sameAuthority(next.Host, next.URL.Host, next.URL.Scheme) {
				return &redirectPolicyError{err: fmt.Errorf("refuse GitHub API redirect with unexpected Host override %q", next.Host)}
			}
			if next.Method != expectedMethod {
				return &redirectPolicyError{err: fmt.Errorf("refuse GitHub API redirect that changes method from %s to %s", expectedMethod, next.Method)}
			}
			return nil
		}
		if err := validate(); err != nil {
			return err
		}
		// net/http copies headers from the initial request on every hop and may
		// strip Authorization before CheckRedirect when equivalent IP literals
		// use different spellings. After validating the authority, carry forward
		// the value from the request that was actually sent instead. This both
		// restores safe stripped credentials and preserves a callback's decision
		// to remove or replace Authorization on every subsequent hop.
		if len(via) > 0 {
			setAuthorization(next.Header, authorizationValues(via[len(via)-1].Header))
		}
		if originalCheckRedirect != nil {
			err := originalCheckRedirect(next, via)
			if err != nil {
				if err == http.ErrUseLastResponse {
					return err
				}
				return &redirectPolicyError{err: err}
			}
			// CheckRedirect may modify the request. Revalidate before Client.Do
			// sends it so a custom callback cannot bypass the origin or method
			// invariants after the initial redirect validation.
			return validate()
		}
		if len(via) >= 10 {
			return &redirectPolicyError{err: errors.New("stopped after 10 redirects")}
		}
		return nil
	}
	return client.Do(request)
}

func authorizationValues(header http.Header) []string {
	var values []string
	for name, entries := range header {
		if strings.EqualFold(name, "Authorization") {
			values = append(values, entries...)
		}
	}
	return values
}

func setAuthorization(header http.Header, values []string) {
	for name := range header {
		if strings.EqualFold(name, "Authorization") {
			delete(header, name)
		}
	}
	for _, value := range values {
		header.Add("Authorization", value)
	}
}

func (c *CLIClient) wait(ctx context.Context, duration time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func retryableStatus(status int) bool {
	return status == 429 || status == 502 || status == 503 || status == 504
}
func retryableNetworkError(err error) bool {
	var redirectErr *redirectPolicyError
	if errors.As(err, &redirectErr) {
		return false
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsTemporary {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE)
}
func backoff(attempt int) time.Duration {
	base := 100 * time.Millisecond * time.Duration(1<<(attempt-1))
	return base + time.Duration(rand.Int64N(int64(base/2)+1))
}
func retryDelay(header http.Header, attempt int) time.Duration {
	if value := header.Get("Retry-After"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil {
			return max(time.Duration(seconds)*time.Second, 0)
		}
		if when, err := http.ParseTime(value); err == nil {
			return max(time.Until(when), 0)
		}
	}
	if value := header.Get("X-RateLimit-Reset"); value != "" {
		if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
			return max(time.Until(time.Unix(unix, 0)), 0)
		}
	}
	return backoff(attempt)
}
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(part, ";")
		isNext := false
		for _, parameter := range segments[1:] {
			name, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if ok && strings.EqualFold(name, "rel") && strings.Trim(value, `"`) == "next" {
				isNext = true
				break
			}
		}
		if isNext {
			return strings.Trim(strings.TrimSpace(segments[0]), "<>")
		}
	}
	return ""
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
