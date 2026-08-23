// Package transport provides the credential-bearing, origin-locked GitHub API
// transport used by the GitHub adapter.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/maxbeizer/gh-hush/internal/diagnostic"
)

const (
	maxResponseBytes = 10 << 20
	maxAttempts      = 3
	defaultBaseURL   = "https://api.github.com"
)

type sleepFunc func(context.Context, time.Duration) error

// Config contains the dependencies and credential for a Client.
type Config struct {
	HTTPClient *http.Client
	Token      string
	BaseURL    string
}

// Client owns authenticated request construction, origin confinement,
// redirects, bounded response reads, retries, and pagination traversal.
type Client struct {
	httpClient *http.Client
	token      string
	baseURL    string
	sleep      sleepFunc
}

func New(config Config) *Client {
	return &Client{httpClient: config.HTTPClient, token: config.Token, baseURL: config.BaseURL}
}

// Response is the bounded successful representation exposed to the adapter.
// Endpoint contains only the escaped path, never URL credentials or a query.
type Response struct {
	Body     []byte
	Endpoint string
}

type requestResult struct {
	Response
	effectiveURL string
	header       http.Header
}

// APIError describes a non-successful GitHub response without retaining its body.
type APIError struct {
	StatusCode int
	statusText string
	requestID  string
	endpoint   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub API request for %q returned %s (request ID %q)", e.endpoint, e.statusText, e.requestID)
}

type safeError struct {
	message string
	cause   error
}

func (e *safeError) Error() string { return e.message }
func (e *safeError) Unwrap() error { return e.cause }

func hideError(message string, cause error) error {
	return &safeError{message: message, cause: cause}
}

// Request performs one logical authenticated API operation. Transient attempts
// and redirects remain internal to the transport.
func (c *Client) Request(ctx context.Context, method, endpoint string) (Response, error) {
	result, err := c.request(ctx, method, endpoint)
	return result.Response, err
}

func (c *Client) request(ctx context.Context, method, endpoint string) (requestResult, error) {
	requestURL, err := c.resolve(endpoint)
	if err != nil {
		return requestResult{}, err
	}
	safeEndpoint := sanitizedEndpoint(requestURL)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			diagnostic.Log(ctx, "request_cancelled", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt))
			return requestResult{}, err
		}
		diagnostic.Log(ctx, "request_start", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt))
		request, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
		if err != nil {
			return requestResult{}, hideError("create GitHub API request", err)
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("User-Agent", "gh-hush")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		response, requestErr := c.do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				diagnostic.Log(ctx, "request_cancelled", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt))
				return requestResult{}, ctx.Err()
			}
			retryable := retryableNetworkError(requestErr)
			diagnostic.Log(ctx, "request_failed", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt), diagnostic.String("kind", "network"), diagnostic.Bool("retryable", retryable))
			if !retryable {
				message := fmt.Sprintf("request GitHub API endpoint %q failed", safeEndpoint)
				var redirectErr *redirectPolicyError
				if errors.As(requestErr, &redirectErr) {
					message += ": " + redirectErr.Error()
				}
				return requestResult{}, hideError(message, requestErr)
			}
			if attempt == maxAttempts {
				return requestResult{}, hideError(fmt.Sprintf("request GitHub API endpoint %q exhausted %d attempts", safeEndpoint, maxAttempts), requestErr)
			}
			delay := backoff(attempt)
			diagnostic.Log(ctx, "retry_scheduled", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt), diagnostic.Int("delay_ms", int(delay/time.Millisecond)))
			if err := c.wait(ctx, delay); err != nil {
				diagnostic.Log(ctx, "request_cancelled", diagnostic.String("method", method), diagnostic.String("endpoint", safeEndpoint), diagnostic.Int("attempt", attempt))
				return requestResult{}, err
			}
			continue
		}
		effectiveURL := requestURL
		if response.Request != nil && response.Request.URL != nil {
			effectiveURL = response.Request.URL.String()
		}
		effectiveEndpoint := sanitizedEndpoint(effectiveURL)
		diagnostic.Log(ctx, "response", responseFields(method, effectiveEndpoint, attempt, response)...)
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		_ = response.Body.Close()
		if readErr != nil {
			if ctx.Err() != nil {
				return requestResult{}, ctx.Err()
			}
			if !retryableNetworkError(readErr) {
				return requestResult{}, hideError(fmt.Sprintf("read GitHub API response for %q failed", effectiveEndpoint), readErr)
			}
			if attempt == maxAttempts {
				return requestResult{}, hideError(fmt.Sprintf("read GitHub API response for %q exhausted %d attempts", effectiveEndpoint, maxAttempts), readErr)
			}
			if err := c.wait(ctx, backoff(attempt)); err != nil {
				return requestResult{}, err
			}
			continue
		}
		if len(body) > maxResponseBytes {
			return requestResult{}, fmt.Errorf("GitHub API response for %q exceeded %d bytes", effectiveEndpoint, maxResponseBytes)
		}
		result := requestResult{
			Response:     Response{Body: body, Endpoint: effectiveEndpoint},
			effectiveURL: effectiveURL,
			header:       response.Header,
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return result, nil
		}
		statusText := strconv.Itoa(response.StatusCode)
		if text := http.StatusText(response.StatusCode); text != "" {
			statusText += " " + text
		}
		apiErr := &APIError{StatusCode: response.StatusCode, statusText: statusText, requestID: response.Header.Get("X-GitHub-Request-Id"), endpoint: effectiveEndpoint}
		if !retryableStatus(response.StatusCode) {
			return requestResult{}, apiErr
		}
		if attempt == maxAttempts {
			return requestResult{}, fmt.Errorf("GitHub API request exhausted %d attempts: %w", maxAttempts, apiErr)
		}
		delay := retryDelay(response.Header, attempt)
		diagnostic.Log(ctx, "retry_scheduled", diagnostic.String("method", method), diagnostic.String("endpoint", sanitizedEndpoint(effectiveURL)), diagnostic.Int("attempt", attempt), diagnostic.Int("delay_ms", int(delay/time.Millisecond)))
		if err := c.wait(ctx, delay); err != nil {
			diagnostic.Log(ctx, "request_cancelled", diagnostic.String("method", method), diagnostic.String("endpoint", sanitizedEndpoint(effectiveURL)), diagnostic.Int("attempt", attempt))
			return requestResult{}, err
		}
	}
	panic("unreachable")
}

// Pages traverses same-origin GitHub Link pagination. The callback sees each
// bounded successful page and controls representation decoding.
func (c *Client) Pages(ctx context.Context, endpoint string, visit func(Response) error) error {
	next := endpoint
	for next != "" {
		result, err := c.request(ctx, http.MethodGet, next)
		if err != nil {
			return err
		}
		if err := visit(result.Response); err != nil {
			return err
		}
		next = nextLink(result.header.Get("Link"))
		if next != "" {
			next, err = c.resolvePageLink(result.effectiveURL, next)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func sanitizedEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.EscapedPath() == "" {
		return "[invalid]"
	}
	return parsed.EscapedPath()
}

func responseFields(method, endpoint string, attempt int, response *http.Response) []diagnostic.Field {
	return []diagnostic.Field{
		diagnostic.String("method", method), diagnostic.String("endpoint", endpoint), diagnostic.Int("attempt", attempt),
		diagnostic.Int("status", response.StatusCode), diagnostic.String("request_id", response.Header.Get("X-GitHub-Request-Id")),
		diagnostic.String("rate_limit_remaining", response.Header.Get("X-RateLimit-Remaining")), diagnostic.String("rate_limit_reset", response.Header.Get("X-RateLimit-Reset")),
		diagnostic.String("retry_after", response.Header.Get("Retry-After")),
	}
}

func (c *Client) resolve(endpoint string) (string, error) {
	base := c.baseURL
	if base == "" {
		base = defaultBaseURL
	}
	baseURL, err := url.Parse(strings.TrimRight(base, "/") + "/")
	if err != nil {
		return "", hideError("parse GitHub API base URL", err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", hideError("parse GitHub API endpoint", err)
	}
	resolved := baseURL.ResolveReference(parsed)
	if baseURL.Opaque != "" || baseURL.User != nil {
		return "", errors.New("refuse malformed GitHub API base URL")
	}
	if resolved.Opaque != "" || resolved.User != nil {
		return "", fmt.Errorf("refuse GitHub API endpoint with opaque URL or userinfo for %q", sanitizedEndpoint(resolved.String()))
	}
	if !sameOrigin(baseURL, resolved) {
		return "", fmt.Errorf("refuse GitHub API endpoint on unexpected origin for %q", sanitizedEndpoint(resolved.String()))
	}
	return resolved.String(), nil
}

func (c *Client) resolvePageLink(currentURL, link string) (string, error) {
	current, err := url.Parse(currentURL)
	if err != nil {
		return "", hideError("parse current GitHub API page URL", err)
	}
	reference, err := url.Parse(link)
	if err != nil {
		return "", hideError("parse GitHub API pagination link", err)
	}
	return c.resolve(current.ResolveReference(reference).String())
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && sameAuthority(left.Host, right.Host, left.Scheme)
}

// sameAuthority compares the authority used on the wire. DNS names are
// case-insensitive, equivalent IP spellings compare equal, and an omitted port
// is equivalent only to the scheme's default port.
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
	for i := range len(host) {
		if host[i] >= 0x80 {
			return "", "", false
		}
	}
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

type redirectPolicyError struct{ err error }

func (e *redirectPolicyError) Error() string { return e.err.Error() }
func (e *redirectPolicyError) Unwrap() error { return e.err }

func (c *Client) client() *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	return http.DefaultClient
}

func (c *Client) do(request *http.Request) (*http.Response, error) {
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
			if next.URL.Opaque != "" {
				return &redirectPolicyError{err: errors.New("refuse GitHub API redirect with an opaque URL")}
			}
			if _, err := c.resolve(next.URL.String()); err != nil {
				return &redirectPolicyError{err: err}
			}
			if next.Host != "" && !sameAuthority(next.Host, next.URL.Host, next.URL.Scheme) {
				return &redirectPolicyError{err: errors.New("refuse GitHub API redirect with unexpected Host override")}
			}
			if next.Method != expectedMethod {
				return &redirectPolicyError{err: fmt.Errorf("refuse GitHub API redirect that changes method from %s to %s", expectedMethod, next.Method)}
			}
			return nil
		}
		if err := validate(); err != nil {
			return err
		}
		// net/http copies headers from the first request and may strip
		// Authorization before this callback. Once the next authority is proven
		// equivalent, carry forward the value from the request actually sent. This
		// also preserves a custom policy's removal or replacement over many hops.
		if len(via) > 0 {
			setAuthorization(next.Header, authorizationValues(via[len(via)-1].Header))
		}
		if originalCheckRedirect != nil {
			err := originalCheckRedirect(next, via)
			if err != nil {
				if err == http.ErrUseLastResponse {
					return err
				}
				return &redirectPolicyError{err: hideError("custom GitHub API redirect policy rejected redirect", err)}
			}
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
func (c *Client) wait(ctx context.Context, duration time.Duration) error {
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
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE)
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
