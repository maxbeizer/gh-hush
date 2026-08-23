package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maxbeizer/gh-hush/internal/diagnostic"
)

func testTransport(server *httptest.Server) *Client {
	client := New(Config{HTTPClient: server.Client(), BaseURL: server.URL, Token: "test"})
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client
}

func TestRequestAppliesAuthenticationAndSafeDiagnosticsAcrossRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if got := r.Header.Get("Authorization"); got != "Bearer token-must-not-appear" {
			t.Errorf("Authorization=%q", got)
		}
		for name, want := range map[string]string{"Accept": "application/vnd.github+json", "User-Agent": "gh-hush", "X-GitHub-Api-Version": "2022-11-28"} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("%s=%q want=%q", name, got, want)
			}
		}
		w.Header().Set("X-GitHub-Request-Id", fmt.Sprintf("request-%d", attempts))
		w.Header().Set("X-RateLimit-Remaining", "42")
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "sensitive response body", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	var output strings.Builder
	ctx := diagnostic.WithLogger(context.Background(), diagnostic.New(&output))
	ctx = diagnostic.WithPhase(ctx, "listing")
	client := testTransport(server)
	client.token = "token-must-not-appear"
	if _, err := client.Request(ctx, http.MethodGet, "/notifications?per_page=100&secret=value"); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{
		"event=request_start phase=listing method=GET endpoint=/notifications attempt=1",
		"event=response phase=listing method=GET endpoint=/notifications attempt=1 status=503 request_id=request-1 rate_limit_remaining=42 retry_after=0",
		"event=retry_scheduled phase=listing method=GET endpoint=/notifications attempt=1 delay_ms=0",
		"event=request_start phase=listing method=GET endpoint=/notifications attempt=2",
		"status=200 request_id=request-2 rate_limit_remaining=42",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("debug output missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"token-must-not-appear", "Authorization", "sensitive response body", "per_page=100", "secret=value"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("debug output exposed %q: %s", forbidden, got)
		}
	}
}

func TestRequestHidesUnsafeNetworkAndReadErrors(t *testing.T) {
	const secret = "network-secret"
	sentinel := errors.New("failed at https://user:password@api.github.test/path?token=" + secret)
	for _, test := range []struct {
		name      string
		transport roundTripFunc
	}{
		{
			name: "network",
			transport: func(*http.Request) (*http.Response, error) {
				return nil, sentinel
			},
		},
		{
			name: "response read",
			transport: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: &errorBody{err: sentinel}}, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			ctx := diagnostic.WithLogger(context.Background(), diagnostic.New(&output))
			client := New(Config{BaseURL: "https://api.github.test", Token: secret, HTTPClient: &http.Client{Transport: test.transport}})
			_, err := client.Request(ctx, http.MethodGet, "/items?access_token="+secret)
			if err == nil || !errors.Is(err, sentinel) {
				t.Fatalf("err=%v", err)
			}
			for label, text := range map[string]string{"error": err.Error(), "diagnostics": output.String()} {
				for _, forbidden := range []string{secret, "access_token", "user:password", "token="} {
					if strings.Contains(text, forbidden) {
						t.Errorf("%s exposed %q: %s", label, forbidden, text)
					}
				}
			}
		})
	}
}

func TestResolutionErrorsHideInputCredentials(t *testing.T) {
	for _, test := range []struct {
		name     string
		baseURL  string
		endpoint string
	}{
		{name: "invalid base", baseURL: "https://base-secret%zz.example", endpoint: "/items"},
		{name: "invalid endpoint", baseURL: "https://api.github.test", endpoint: "https://endpoint-secret%zz.example/items?token=hidden"},
		{name: "endpoint userinfo", baseURL: "https://api.github.test", endpoint: "https://user:password@api.github.test/items?token=hidden"},
		{name: "foreign origin", baseURL: "https://api.github.test", endpoint: "https://credential.example/items?token=hidden"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := New(Config{BaseURL: test.baseURL})
			_, err := client.Request(context.Background(), http.MethodGet, test.endpoint)
			if err == nil {
				t.Fatal("error=nil")
			}
			for _, forbidden := range []string{"base-secret", "endpoint-secret", "user:password", "credential.example", "token=hidden"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("error exposed %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestRequestCancellationDoesNotStartRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testTransport(server).Request(ctx, http.MethodGet, "/notifications")
	if !errors.Is(err, context.Canceled) || requests != 0 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestPagesResolvesLinksAgainstFinalRedirectAndRejectsForeignOrigin(t *testing.T) {
	t.Run("relative to final redirect URL", func(t *testing.T) {
		var uris []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uris = append(uris, r.URL.RequestURI())
			switch r.URL.Path {
			case "/notifications":
				http.Redirect(w, r, "/redirected/pages/first", http.StatusTemporaryRedirect)
			case "/redirected/pages/first":
				w.Header().Set("Link", `<next?page=2>; type="application/json"; rel="next"`)
				_, _ = w.Write([]byte("first"))
			case "/redirected/pages/next":
				_, _ = w.Write([]byte("second"))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		var pages []string
		err := testTransport(server).Pages(context.Background(), "/notifications?per_page=100", func(response Response) error {
			pages = append(pages, string(response.Body))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pages, []string{"first", "second"}) || !reflect.DeepEqual(uris, []string{"/notifications?per_page=100", "/redirected/pages/first", "/redirected/pages/next?page=2"}) {
			t.Fatalf("pages=%v uris=%v", pages, uris)
		}
	})

	t.Run("query-only link uses full final URL", func(t *testing.T) {
		var uris []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uris = append(uris, r.URL.RequestURI())
			switch {
			case r.URL.Path == "/start":
				http.Redirect(w, r, "/final/items?cursor=private", http.StatusTemporaryRedirect)
			case r.URL.Query().Get("page") == "2":
				_, _ = w.Write([]byte("second"))
			default:
				w.Header().Set("Link", `<?page=2>; rel="next"`)
				_, _ = w.Write([]byte("first"))
			}
		}))
		defer server.Close()
		var endpoints []string
		err := testTransport(server).Pages(context.Background(), "/start?token=initial", func(response Response) error {
			endpoints = append(endpoints, response.Endpoint)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(uris, []string{"/start?token=initial", "/final/items?cursor=private", "/final/items?page=2"}) || !reflect.DeepEqual(endpoints, []string{"/final/items", "/final/items"}) {
			t.Fatalf("uris=%v endpoints=%v", uris, endpoints)
		}
	})

	t.Run("malformed next link hides credentials", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Link", `<https://link-secret%zz.example/page?token=hidden>; rel="next"`)
			_, _ = w.Write([]byte("page"))
		}))
		defer server.Close()
		err := testTransport(server).Pages(context.Background(), "/page1?initial=private", func(Response) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "parse GitHub API pagination link") {
			t.Fatalf("err=%v", err)
		}
		for _, forbidden := range []string{"link-secret", "token=hidden", "initial=private"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Errorf("pagination error exposed %q: %v", forbidden, err)
			}
		}
	})

	t.Run("foreign next link", func(t *testing.T) {
		foreignRequests := 0
		foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignRequests++ }))
		defer foreign.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Link", fmt.Sprintf(`<%s/page2>; rel="next"`, foreign.URL))
			_, _ = w.Write([]byte("page"))
		}))
		defer server.Close()
		err := testTransport(server).Pages(context.Background(), "/page1", func(Response) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "unexpected origin") || foreignRequests != 0 {
			t.Fatalf("err=%v foreign requests=%d", err, foreignRequests)
		}
	})
}

func TestRequestRejectsCredentialBearingURLsOutsideConfiguredOrigin(t *testing.T) {
	foreignRequests := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignRequests++ }))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("base server should not be called") }))
	defer server.Close()
	client := testTransport(server)
	for _, endpoint := range []string{foreign.URL + "/notifications", server.URL + "@" + foreign.URL + "/notifications", "http:user@example.test"} {
		if _, err := client.Request(context.Background(), http.MethodGet, endpoint); err == nil {
			t.Errorf("Request(%q) error=nil", endpoint)
		}
	}
	if foreignRequests != 0 {
		t.Fatalf("foreign requests=%d", foreignRequests)
	}
}

func TestRedirectEnforcesOriginHostAndMethodAfterCustomPolicy(t *testing.T) {
	foreignRequests := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignRequests++
		if r.Header.Get("Authorization") != "" {
			t.Errorf("foreign Authorization=%q", r.Header.Get("Authorization"))
		}
	}))
	defer foreign.Close()

	for _, test := range []struct {
		name    string
		rewrite func(*http.Request)
		want    string
	}{
		{"foreign origin", func(r *http.Request) { r.URL, _ = url.Parse(foreign.URL + "/redirected") }, "unexpected origin"},
		{"Host override", func(r *http.Request) { r.Host = "foreign.example" }, "unexpected Host override"},
		{"opaque target", func(r *http.Request) { r.URL.Opaque = "//foreign.example/redirected" }, "opaque URL"},
		{"userinfo", func(r *http.Request) { r.URL.User = url.UserPassword("user", "credential") }, "userinfo"},
		{"method", func(r *http.Request) { r.Method = http.MethodGet }, "changes method from DELETE to GET"},
	} {
		t.Run(test.name, func(t *testing.T) {
			redirected := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirected" {
					redirected++
				}
				http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
			}))
			defer server.Close()
			client := testTransport(server)
			client.httpClient.CheckRedirect = func(r *http.Request, _ []*http.Request) error { test.rewrite(r); return nil }
			_, err := client.Request(context.Background(), http.MethodDelete, "/start")
			if err == nil || !strings.Contains(err.Error(), test.want) || redirected != 0 {
				t.Fatalf("err=%v redirected=%d", err, redirected)
			}
		})
	}
	if foreignRequests != 0 {
		t.Fatalf("foreign requests=%d", foreignRequests)
	}
}

func TestSameOriginRedirectPreservesAuthorizationAndSafeCustomization(t *testing.T) {
	redirectChecks, redirected := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected++
			if r.Method != http.MethodDelete || r.Header.Get("Authorization") != "Bearer test" || r.Header.Get("X-Custom") != "checked" {
				t.Errorf("redirected method=%s Authorization=%q custom=%q", r.Method, r.Header.Get("Authorization"), r.Header.Get("X-Custom"))
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := testTransport(server)
	client.httpClient.CheckRedirect = func(r *http.Request, _ []*http.Request) error {
		redirectChecks++
		r.Host = strings.ToUpper(r.URL.Hostname()) + ":" + r.URL.Port()
		r.Header.Set("X-Custom", "checked")
		return nil
	}
	if _, err := client.Request(context.Background(), http.MethodDelete, "/start"); err != nil {
		t.Fatal(err)
	}
	if redirectChecks != 1 || redirected != 1 {
		t.Fatalf("redirect checks=%d redirected=%d", redirectChecks, redirected)
	}
}

func TestNativeRedirectCannotChangeMutationMethod(t *testing.T) {
	redirected := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected++
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()
	_, err := testTransport(server).Request(context.Background(), http.MethodDelete, "/start")
	if err == nil || !strings.Contains(err.Error(), "changes method from DELETE to GET") || redirected != 0 {
		t.Fatalf("err=%v redirected=%d", err, redirected)
	}
}

func TestMultiHopRedirectPreservesCallbackAuthorizationChanges(t *testing.T) {
	for _, test := range []struct {
		name string
		set  func(http.Header)
		want string
	}{
		{name: "removal", set: func(header http.Header) { header.Del("Authorization") }},
		{name: "replacement", set: func(header http.Header) { header.Set("Authorization", "Bearer callback") }, want: "Bearer callback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			client := New(Config{BaseURL: "https://api.github.test", Token: "test", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if requests > 1 && request.Header.Get("Authorization") != test.want {
					t.Errorf("request %d Authorization=%q want=%q", requests, request.Header.Get("Authorization"), test.want)
				}
				switch requests {
				case 1:
					return response(http.StatusTemporaryRedirect, http.Header{"Location": []string{"/second"}}, ""), nil
				case 2:
					return response(http.StatusTemporaryRedirect, http.Header{"Location": []string{"/final"}}, ""), nil
				default:
					return response(http.StatusNoContent, nil, ""), nil
				}
			})}})
			redirects := 0
			client.httpClient.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
				redirects++
				if redirects == 1 {
					test.set(request.Header)
				}
				return nil
			}
			if _, err := client.Request(context.Background(), http.MethodDelete, "/start"); err != nil {
				t.Fatal(err)
			}
			if requests != 3 || redirects != 2 {
				t.Fatalf("requests=%d redirects=%d", requests, redirects)
			}
		})
	}
}

func TestEquivalentIPv6RedirectPreservesAuthorization(t *testing.T) {
	requests := 0
	client := New(Config{BaseURL: "https://[2001:0db8::1]", Token: "test", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return response(http.StatusTemporaryRedirect, http.Header{"Location": []string{"https://[2001:db8::1]/redirected"}}, ""), nil
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test" {
			t.Errorf("Authorization=%q", got)
		}
		return response(http.StatusNoContent, nil, ""), nil
	})}})
	if _, err := client.Request(context.Background(), http.MethodDelete, "/start"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestHTTPErrorReportsFinalRedirectEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/final" {
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("X-GitHub-Request-Id", "request-123")
		http.Error(w, "invalid", http.StatusUnprocessableEntity)
	}))
	defer server.Close()
	_, err := testTransport(server).Request(context.Background(), http.MethodDelete, "/start?secret=hidden")
	if err == nil || !strings.Contains(err.Error(), `"/final"`) || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "secret=hidden") || !strings.Contains(err.Error(), "request-123") {
		t.Fatalf("err=%v", err)
	}
}

func TestAPIErrorHidesUnsafeEffectiveURLStatusAndBody(t *testing.T) {
	const secret = "response-secret"
	responseURL, err := url.Parse("https://user:password@api.github.test/final?token=" + secret + "#fragment-secret")
	if err != nil {
		t.Fatal(err)
	}
	client := New(Config{BaseURL: "https://api.github.test", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnprocessableEntity,
			Status:     "422 unsafe-status-" + secret,
			Header:     http.Header{"X-Github-Request-Id": []string{"request-123"}},
			Body:       io.NopCloser(strings.NewReader("body-" + secret)),
			Request:    &http.Request{URL: responseURL},
		}, nil
	})}})
	_, err = client.Request(context.Background(), http.MethodGet, "/start?initial="+secret)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), `"/final"`) {
		t.Fatalf("err=%v apiErr=%+v", err, apiErr)
	}
	for _, forbidden := range []string{secret, "unsafe-status", "user:password", "token=", "fragment-secret", "body-"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("API error exposed %q: %v", forbidden, err)
		}
	}
}

func TestCustomRedirectPolicyCanUseLastResponse(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := testTransport(server)
	client.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	_, err := client.Request(context.Background(), http.MethodDelete, "/start")
	if err == nil || !strings.Contains(err.Error(), "307 Temporary Redirect") || requests != 1 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestRedirectPolicyFailureIsNotRetried(t *testing.T) {
	requests, waits := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := testTransport(server)
	const secret = "redirect-secret"
	callbackErr := fmt.Errorf("failed at https://user:password@example.test/path?token=%s: %w", secret, context.DeadlineExceeded)
	client.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return callbackErr }
	client.sleep = func(context.Context, time.Duration) error { waits++; return nil }
	_, err := client.Request(context.Background(), http.MethodDelete, "/start?initial="+secret)
	if !errors.Is(err, callbackErr) || !errors.Is(err, context.DeadlineExceeded) || requests != 1 || waits != 0 {
		t.Fatalf("err=%v requests=%d waits=%d", err, requests, waits)
	}
	for _, forbidden := range []string{secret, "user:password", "example.test", "token="} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("redirect policy error exposed %q: %v", forbidden, err)
		}
	}
}

func TestRequestBoundsResponseAndClosesBody(t *testing.T) {
	closed := false
	client := New(Config{BaseURL: "https://api.github.test", Token: "test", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: &trackingBody{Reader: strings.NewReader(strings.Repeat("x", maxResponseBytes+1)), closed: &closed}}, nil
	})}})
	_, err := client.Request(context.Background(), http.MethodGet, "/large")
	if err == nil || !strings.Contains(err.Error(), "exceeded") || !closed {
		t.Fatalf("err=%v closed=%v", err, closed)
	}
}

func TestRetriesTransientStatusesAtMostThreeTimesAndHonorsRetryAfter(t *testing.T) {
	attempts := 0
	var waits []time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "7")
		http.Error(w, "temporary", http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := testTransport(server)
	client.sleep = func(_ context.Context, duration time.Duration) error { waits = append(waits, duration); return nil }
	_, err := client.Request(context.Background(), http.MethodDelete, "/mutation")
	var apiErr *APIError
	if err == nil || !strings.Contains(err.Error(), "exhausted 3 attempts") || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests || attempts != 3 || !reflect.DeepEqual(waits, []time.Duration{7 * time.Second, 7 * time.Second}) {
		t.Fatalf("err=%v apiErr=%+v attempts=%d waits=%v", err, apiErr, attempts, waits)
	}
}

func TestNonTransientAPIErrorIsTypedAndNotRetried(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("X-GitHub-Request-Id", "request-123")
		http.Error(w, "secret body", http.StatusNotFound)
	}))
	defer server.Close()
	_, err := testTransport(server).Request(context.Background(), http.MethodGet, "/missing")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound || attempts != 1 || strings.Contains(err.Error(), "secret body") {
		t.Fatalf("err=%v apiErr=%+v attempts=%d", err, apiErr, attempts)
	}
}

func TestCancellationDuringResponseReadStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts, waits := 0, 0
	client := New(Config{BaseURL: "https://api.github.test", Token: "test", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: &cancelingBody{cancel: cancel}}, nil
	})}})
	client.sleep = func(context.Context, time.Duration) error { waits++; return nil }
	_, err := client.Request(ctx, http.MethodGet, "/cancel")
	if !errors.Is(err, context.Canceled) || attempts != 1 || waits != 0 {
		t.Fatalf("err=%v attempts=%d waits=%d", err, attempts, waits)
	}
}

func TestOriginComparisonAcceptsEquivalentAuthoritiesAndRejectsAmbiguousOnes(t *testing.T) {
	for _, test := range []struct {
		base, endpoint string
		allowed        bool
	}{
		{"https://API.GITHUB.TEST", "https://api.github.test:443/path", true},
		{"http://127.0.0.1", "http://127.0.0.1:80/path", true},
		{"https://[2001:0db8::1]", "https://[2001:db8::1]:443/path", true},
		{"https://api.github.test", "https://api.github.test:8443/path", false},
		{"https://api.github.test", "http://api.github.test:443/path", false},
		{"https://σ.example", "https://σ.example/path", false},
		{"https://api.github.test:", "/path", false},
		{"https://user:password@api.github.test", "/path", false},
	} {
		client := New(Config{BaseURL: test.base, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(http.StatusNoContent, nil, ""), nil })}})
		_, err := client.Request(context.Background(), http.MethodGet, test.endpoint)
		if (err == nil) != test.allowed {
			t.Errorf("base=%q endpoint=%q err=%v allowed=%v", test.base, test.endpoint, err, test.allowed)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func response(status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

type trackingBody struct {
	io.Reader
	closed *bool
}

func (b *trackingBody) Close() error { *b.closed = true; return nil }

type cancelingBody struct{ cancel context.CancelFunc }

func (b *cancelingBody) Read([]byte) (int, error) { b.cancel(); return 0, io.ErrUnexpectedEOF }
func (*cancelingBody) Close() error               { return nil }

type errorBody struct{ err error }

func (b *errorBody) Read([]byte) (int, error) { return 0, b.err }
func (*errorBody) Close() error               { return nil }
