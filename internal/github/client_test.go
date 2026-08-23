package github

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
	"sync"
	"testing"
	"time"

	"github.com/maxbeizer/gh-hush/internal/model"
)

func testClient(server *httptest.Server) *CLIClient {
	return &CLIClient{httpClient: server.Client(), apiBase: server.URL, token: "test", sleep: func(context.Context, time.Duration) error { return nil }}
}

func TestNewCLIClientPinsTokenLookupToGitHubDotComAPIHost(t *testing.T) {
	var args []string
	client, err := newCLIClient(context.Background(), func(_ context.Context, got ...string) ([]byte, error) {
		args = append([]string(nil), got...)
		return []byte("test-token\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"auth", "token", "--hostname", "github.com"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("token arguments=%v want=%v", args, want)
	}
	if client.apiBase != "https://api.github.com" || client.token != "test-token" {
		t.Fatalf("client API base=%q token=%q", client.apiBase, client.token)
	}
}

func TestListNotificationsUsesUnreadOnlyDefaultAndPaginates(t *testing.T) {
	var server *httptest.Server
	var queries []string
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id":"second","unread":true}]`))
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/notifications?per_page=100&page=2>; rel="next"`, server.URL))
		_, _ = w.Write([]byte(`[{"id":"first","unread":true}]`))
	}))
	defer server.Close()
	got, err := testClient(server).ListNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{got[0].ID, got[1].ID}
	if !reflect.DeepEqual(ids, []string{"first", "second"}) {
		t.Fatalf("IDs=%v", ids)
	}
	for _, query := range queries {
		if strings.Contains(query, "all=") || !strings.Contains(query, "per_page=100") {
			t.Fatalf("query=%q", query)
		}
	}
}

func TestGetNotificationFetchesOnlyRequestedThreadAndHandlesHistoricalAndMissing(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/notifications/threads/missing":
			http.NotFound(w, r)
		case "/notifications/threads/historical":
			_, _ = w.Write([]byte(`{"id":"historical","unread":false}`))
		default:
			_, _ = w.Write([]byte(`{"id":"123","unread":true,"reason":"subscribed"}`))
		}
	}))
	defer server.Close()
	client := testClient(server)

	got, found, err := client.GetNotification(context.Background(), "123")
	if err != nil || !found || got.ID != "123" || !got.Unread {
		t.Fatalf("notification=%#v found=%v err=%v", got, found, err)
	}
	historical, found, err := client.GetNotification(context.Background(), "historical")
	if err != nil || !found || historical.Unread {
		t.Fatalf("historical=%#v found=%v err=%v", historical, found, err)
	}
	_, found, err = client.GetNotification(context.Background(), "missing")
	if err != nil || found {
		t.Fatalf("missing found=%v err=%v", found, err)
	}
	want := []string{"/notifications/threads/123", "/notifications/threads/historical", "/notifications/threads/missing"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths=%v want=%v", paths, want)
	}
}

func TestPaginationResolvesRelativeLinksAgainstCurrentPage(t *testing.T) {
	var requestURIs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURIs = append(requestURIs, r.URL.RequestURI())
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id":"second"}]`))
			return
		}
		w.Header().Set("Link", `<?per_page=100&page=2>; type="application/json"; rel="next"`)
		_, _ = w.Write([]byte(`[{"id":"first"}]`))
	}))
	defer server.Close()

	notifications, err := testClient(server).ListNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 2 {
		t.Fatalf("notifications=%v", notifications)
	}
	want := []string{
		"/notifications?per_page=100",
		"/notifications?per_page=100&page=2",
	}
	if !reflect.DeepEqual(requestURIs, want) {
		t.Fatalf("request URIs=%v want=%v", requestURIs, want)
	}
}

func TestPaginationResolvesRelativeLinksAgainstFinalRedirectURL(t *testing.T) {
	var requestURIs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURIs = append(requestURIs, r.URL.RequestURI())
		switch r.URL.Path {
		case "/notifications":
			http.Redirect(w, r, "/redirected/pages/first", http.StatusTemporaryRedirect)
		case "/redirected/pages/first":
			w.Header().Set("Link", `<next?page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"id":"first"}]`))
		case "/redirected/pages/next":
			_, _ = w.Write([]byte(`[{"id":"second"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	notifications, err := testClient(server).ListNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 2 || notifications[0].ID != "first" || notifications[1].ID != "second" {
		t.Fatalf("notifications=%v", notifications)
	}
	want := []string{
		"/notifications?per_page=100",
		"/redirected/pages/first",
		"/redirected/pages/next?page=2",
	}
	if !reflect.DeepEqual(requestURIs, want) {
		t.Fatalf("request URIs=%v want=%v", requestURIs, want)
	}
}

func TestPaginationRejectsLinksToAnotherOrigin(t *testing.T) {
	foreignRequests := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		foreignRequests++
	}))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", fmt.Sprintf(`<%s/notifications?page=2>; rel="next"`, foreign.URL))
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	_, err := testClient(server).ListNotifications(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected origin") {
		t.Fatalf("error=%v", err)
	}
	if foreignRequests != 0 {
		t.Fatalf("foreign requests=%d, want 0", foreignRequests)
	}
}

func TestRedirectsCannotBypassOriginValidation(t *testing.T) {
	foreignRequests := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		foreignRequests++
	}))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/notifications", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	_, err := testClient(server).ListNotifications(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected origin") {
		t.Fatalf("error=%v", err)
	}
	if foreignRequests != 0 {
		t.Fatalf("foreign requests=%d, want 0", foreignRequests)
	}
}

func TestMutationRedirectCannotChangeDeleteToGet(t *testing.T) {
	redirectedRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirectedRequests++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()

	err := testClient(server).UnsubscribeThread(context.Background(), "123")
	if err == nil || !strings.Contains(err.Error(), "changes method from DELETE to GET") {
		t.Fatalf("error=%v", err)
	}
	if redirectedRequests != 0 {
		t.Fatalf("redirected requests=%d, want 0", redirectedRequests)
	}
}

func TestSameOriginRedirectPreservesMutationAndCustomRedirectCheck(t *testing.T) {
	redirectChecks := 0
	redirectedRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirectedRequests++
			if r.Method != http.MethodDelete {
				t.Errorf("redirected method=%s, want DELETE", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer test" {
				t.Errorf("redirected Authorization=%q", r.Header.Get("Authorization"))
			}
			if r.Header.Get("X-Custom-Redirect") != "checked" {
				t.Errorf("custom redirect header=%q", r.Header.Get("X-Custom-Redirect"))
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	client := testClient(server)
	client.httpClient.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
		redirectChecks++
		request.Header.Set("X-Custom-Redirect", "checked")
		return nil
	}
	if err := client.UnsubscribeThread(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	if redirectChecks != 1 || redirectedRequests != 1 {
		t.Fatalf("redirect checks=%d redirected requests=%d", redirectChecks, redirectedRequests)
	}
}

func TestCustomRedirectCheckCannotRewriteSafeRequest(t *testing.T) {
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
		{
			name: "foreign origin",
			rewrite: func(request *http.Request) {
				request.URL, _ = request.URL.Parse(foreign.URL + "/redirected")
			},
			want: "unexpected origin",
		},
		{
			name: "foreign Host override",
			rewrite: func(request *http.Request) {
				request.Host = "foreign.example"
			},
			want: "unexpected Host override",
		},
		{
			name: "opaque request target",
			rewrite: func(request *http.Request) {
				request.URL.Opaque = "//foreign.example/redirected"
			},
			want: "opaque URL",
		},
		{
			name: "URL userinfo",
			rewrite: func(request *http.Request) {
				request.URL.User = url.UserPassword("foreign", "credential")
			},
			want: "userinfo",
		},
		{
			name: "mutation method",
			rewrite: func(request *http.Request) {
				request.Method = http.MethodGet
			},
			want: "changes method from DELETE to GET",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			redirectedRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirected" {
					redirectedRequests++
				}
				http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
			}))
			defer server.Close()

			client := testClient(server)
			client.httpClient.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
				test.rewrite(request)
				return nil
			}
			err := client.UnsubscribeThread(context.Background(), "123")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
			if redirectedRequests != 0 {
				t.Fatalf("redirected requests=%d, want 0", redirectedRequests)
			}
		})
	}
	if foreignRequests != 0 {
		t.Fatalf("foreign requests=%d, want 0", foreignRequests)
	}
}

func TestOriginAndHostComparisonNormalizesCaseAndDefaultPorts(t *testing.T) {
	parse := func(raw string) *url.URL {
		t.Helper()
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	for _, test := range []struct {
		name        string
		left, right string
		want        bool
	}{
		{"host case", "https://API.GITHUB.TEST/path", "https://api.github.test/other", true},
		{"implicit and explicit HTTPS port", "https://api.github.test/path", "https://api.github.test:443/other", true},
		{"implicit and explicit HTTP port", "http://api.github.test/path", "http://api.github.test:80/other", true},
		{"non-default port", "https://api.github.test/path", "https://api.github.test:8443/other", false},
		{"scheme", "https://api.github.test/path", "http://api.github.test:443/other", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sameOrigin(parse(test.left), parse(test.right)); got != test.want {
				t.Fatalf("sameOrigin(%q, %q)=%v, want %v", test.left, test.right, got, test.want)
			}
		})
	}
}

func TestAuthorityValidationRejectsMalformedOrUnsupportedValues(t *testing.T) {
	for _, authority := range []string{
		"", "api.github.test:", "api.github.test:0", "api.github.test:65536",
		"api.github.test:not-a-port", "user@api.github.test", "2001:db8::1",
		"σ.example", "[fe80::1%25en0]", "[fe80::1%en0]",
	} {
		t.Run(authority, func(t *testing.T) {
			if _, _, ok := normalizedAuthority(authority, "https"); ok {
				t.Fatalf("normalizedAuthority(%q) unexpectedly succeeded", authority)
			}
		})
	}
	if !sameAuthority("[2001:0db8::1]", "[2001:db8::1]:443", "https") {
		t.Fatal("equivalent bracketed IPv6 authorities were rejected")
	}
	if sameAuthority("σ.example", "ς.example", "https") {
		t.Fatal("Unicode simple-fold equivalents must use explicit ASCII/Punycode authorities")
	}
	if sameAuthority("127.0.0.1", "127.000.000.001", "https") {
		t.Fatal("non-standard IPv4 spelling must not be treated as an equivalent IP address")
	}
	if !sameAuthority("api.github.test:0443", "API.GITHUB.TEST:443", "https") {
		t.Fatal("equivalent numeric port and DNS casing were rejected")
	}
}

func TestResolveRejectsOpaqueURLsAndUserinfo(t *testing.T) {
	client := &CLIClient{apiBase: "https://api.github.test"}
	for _, endpoint := range []string{
		"https:opaque-target",
		"https://user:password@api.github.test/notifications",
	} {
		if _, err := client.resolve(endpoint); err == nil {
			t.Fatalf("resolve(%q) error = nil", endpoint)
		}
	}
	client.apiBase = "https://user:password@api.github.test"
	if _, err := client.resolve("/notifications"); err == nil {
		t.Fatal("resolve() accepted base URL userinfo")
	}
}

func TestEquivalentIPv6RedirectPreservesAuthorization(t *testing.T) {
	requests := 0
	client := &CLIClient{
		apiBase: "https://[2001:0db8::1]",
		token:   "test",
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return &http.Response{
					StatusCode: http.StatusTemporaryRedirect,
					Status:     "307 Temporary Redirect",
					Header:     http.Header{"Location": []string{"https://[2001:db8::1]/redirected"}},
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			}
			if got := request.Header.Get("Authorization"); got != "Bearer test" {
				t.Errorf("redirected Authorization=%q, want preserved bearer token", got)
			}
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Status:     "204 No Content",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})},
	}
	if err := client.UnsubscribeThread(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d, want 2", requests)
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
			client := &CLIClient{
				apiBase: "https://api.github.test",
				token:   "test",
				httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					requests++
					if requests > 1 && request.Header.Get("Authorization") != test.want {
						t.Errorf("request %d Authorization=%q, want %q", requests, request.Header.Get("Authorization"), test.want)
					}
					status, location := http.StatusTemporaryRedirect, "/second"
					switch requests {
					case 2:
						location = "/final"
					case 3:
						status, location = http.StatusNoContent, ""
					}
					header := make(http.Header)
					if location != "" {
						header.Set("Location", location)
					}
					return &http.Response{
						StatusCode: status,
						Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
						Header:     header,
						Body:       io.NopCloser(strings.NewReader("")),
					}, nil
				})},
			}
			redirects := 0
			client.httpClient.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
				redirects++
				if redirects == 1 {
					if got := request.Header.Get("Authorization"); got != "Bearer test" {
						t.Errorf("first redirect Authorization=%q", got)
					}
					test.set(request.Header)
				} else if got := request.Header.Get("Authorization"); got != test.want {
					t.Errorf("second redirect Authorization=%q, want %q", got, test.want)
				}
				return nil
			}
			if err := client.UnsubscribeThread(context.Background(), "123"); err != nil {
				t.Fatal(err)
			}
			if requests != 3 || redirects != 2 {
				t.Fatalf("requests=%d redirects=%d, want 3 and 2", requests, redirects)
			}
		})
	}
}

func TestDefaultPortRedirectAndEquivalentHostOverrideArePreserved(t *testing.T) {
	requests := 0
	client := &CLIClient{
		apiBase: "https://api.github.test",
		token:   "test",
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return &http.Response{
					StatusCode: http.StatusTemporaryRedirect,
					Status:     "307 Temporary Redirect",
					Header:     http.Header{"Location": []string{"https://api.github.test:443/redirected"}},
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			}
			if request.URL.Host != "api.github.test:443" || request.Host != "API.GITHUB.TEST" {
				t.Errorf("redirect URL host=%q Host override=%q", request.URL.Host, request.Host)
			}
			if request.Method != http.MethodDelete || request.Header.Get("Authorization") != "Bearer test" {
				t.Errorf("redirect method=%q Authorization=%q", request.Method, request.Header.Get("Authorization"))
			}
			if request.Header.Get("X-Safe-Customization") != "preserved" {
				t.Errorf("safe custom header=%q", request.Header.Get("X-Safe-Customization"))
			}
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Status:     "204 No Content",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})},
	}
	client.httpClient.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
		request.Host = "API.GITHUB.TEST"
		request.Header.Set("X-Safe-Customization", "preserved")
		return nil
	}
	if err := client.UnsubscribeThread(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d, want 2", requests)
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

	err := testClient(server).UnsubscribeThread(context.Background(), "123")
	if err == nil || !strings.Contains(err.Error(), server.URL+"/final") || !strings.Contains(err.Error(), "request-123") {
		t.Fatalf("error=%v, want final redirect endpoint and request ID", err)
	}
}

func TestRedirectPolicyFailureIsNotRetried(t *testing.T) {
	requests, waits := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	client := testClient(server)
	client.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		// DeadlineExceeded implements net.Error and would otherwise be mistaken
		// for a retryable transport timeout.
		return context.DeadlineExceeded
	}
	client.sleep = func(context.Context, time.Duration) error {
		waits++
		return nil
	}
	err := client.UnsubscribeThread(context.Background(), "123")
	if !errors.Is(err, context.DeadlineExceeded) || requests != 1 || waits != 0 {
		t.Fatalf("err=%v requests=%d waits=%d", err, requests, waits)
	}
}

type timeoutWrappingError struct{ err error }

func (e timeoutWrappingError) Error() string   { return e.err.Error() }
func (e timeoutWrappingError) Unwrap() error   { return e.err }
func (e timeoutWrappingError) Timeout() bool   { return true }
func (e timeoutWrappingError) Temporary() bool { return true }

func TestRedirectPolicyErrorWrappingUseLastResponseIsNotRetried(t *testing.T) {
	requests, waits := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	client := testClient(server)
	client.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return timeoutWrappingError{err: fmt.Errorf("custom policy: %w", http.ErrUseLastResponse)}
	}
	client.sleep = func(context.Context, time.Duration) error {
		waits++
		return nil
	}
	err := client.UnsubscribeThread(context.Background(), "123")
	if !errors.Is(err, http.ErrUseLastResponse) || requests != 1 || waits != 0 {
		t.Fatalf("err=%v requests=%d waits=%d", err, requests, waits)
	}
}

func TestCustomRedirectCheckCanReturnUseLastResponse(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	client := testClient(server)
	client.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	err := client.UnsubscribeThread(context.Background(), "123")
	if err == nil || !strings.Contains(err.Error(), "307 Temporary Redirect") || requests != 1 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestRedirectErrorResponseBodyIsAlreadyClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	client := testClient(server)
	client.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("stop redirect")
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.do(request)
	if err == nil || response == nil {
		t.Fatalf("response=%v err=%v", response, err)
	}
	_, readErr := io.ReadAll(response.Body)
	if readErr == nil || !strings.Contains(readErr.Error(), "read on closed response body") {
		t.Fatalf("read error=%v, want closed redirect response body", readErr)
	}
}

func TestUnsubscribeThenDoneUseCorrectDeleteEndpointsInOrder(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := testClient(server)
	if err := client.UnsubscribeThread(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkThreadDone(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	want := []string{"DELETE /notifications/threads/123/subscription", "DELETE /notifications/threads/123"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestTransientRequestsRetryAtMostThreeAttempts(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	if _, err := testClient(server).ListNotifications(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func Test429HonorsRetryAfter(t *testing.T) {
	attempts := 0
	var waits []time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "7")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	client := testClient(server)
	client.sleep = func(_ context.Context, duration time.Duration) error {
		waits = append(waits, duration)
		return nil
	}
	if _, err := client.ListNotifications(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || !reflect.DeepEqual(waits, []time.Duration{7 * time.Second}) {
		t.Fatalf("attempts=%d waits=%v", attempts, waits)
	}
}

func TestRetryDelayHonorsRateLimitReset(t *testing.T) {
	reset := time.Now().Add(5 * time.Second).Unix()
	header := make(http.Header)
	header.Set("X-RateLimit-Reset", fmt.Sprint(reset))
	delay := retryDelay(header, 1)
	if delay <= 3*time.Second || delay > 5*time.Second {
		t.Fatalf("retryDelay()=%v, want delay until reset", delay)
	}
}

func TestAuthPermissionValidationAndNotFoundFailuresAreNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts++
				http.Error(w, "no", status)
			}))
			defer server.Close()
			_, err := testClient(server).ListNotifications(context.Background())
			if err == nil || attempts != 1 {
				t.Fatalf("err=%v attempts=%d", err, attempts)
			}
		})
	}
}

func TestRetryExhaustionAndCancellation(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { attempts++; http.Error(w, "no", http.StatusBadGateway) }))
	defer server.Close()
	_, err := testClient(server).ListNotifications(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exhausted 3 attempts") || attempts != 3 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts = 0
	_, err = testClient(server).ListNotifications(ctx)
	if !errors.Is(err, context.Canceled) || attempts != 0 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestCancellationDuringResponseReadStopsRetriesImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts, waits := 0, 0
	client := &CLIClient{
		apiBase: "https://api.github.test",
		token:   "test",
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       &cancelingBody{cancel: cancel},
			}, nil
		})},
		sleep: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
	}

	_, err := client.ListNotifications(ctx)
	if !errors.Is(err, context.Canceled) || attempts != 1 || waits != 0 {
		t.Fatalf("err=%v attempts=%d waits=%d", err, attempts, waits)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type cancelingBody struct {
	cancel context.CancelFunc
}

func (b *cancelingBody) Read([]byte) (int, error) {
	b.cancel()
	return 0, io.ErrUnexpectedEOF
}

func (*cancelingBody) Close() error { return nil }

func TestMutationRetryExhaustionUsesThreeAttempts(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.Method != http.MethodDelete || r.URL.Path != "/notifications/threads/123/subscription" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "temporary", http.StatusGatewayTimeout)
	}))
	defer server.Close()
	err := testClient(server).UnsubscribeThread(context.Background(), "123")
	if err == nil || !strings.Contains(err.Error(), "exhausted 3 attempts") || attempts != 3 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestEnrichFetchesCompletePaginatedDiscussionHistory(t *testing.T) {
	var server *httptest.Server
	var mu sync.Mutex
	var paths []string
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		switch {
		case r.URL.Path == "/repos/github/repo/discussions/7":
			_, _ = w.Write([]byte(`{"body":"discussion"}`))
		case r.URL.Query().Get("page") == "2":
			_, _ = w.Write([]byte(`[{"body":"historical @github/notifications"}]`))
		default:
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/github/repo/discussions/7/comments?per_page=100&page=2>; rel="next"`, server.URL))
			_, _ = w.Write([]byte(`[{"body":"first"}]`))
		}
	}))
	defer server.Close()
	client := testClient(server)
	item := model.Notification{Subject: model.Subject{Type: "Discussion", URL: server.URL + "/repos/github/repo/discussions/7"}}
	e := client.Enrich(context.Background(), item, model.EnrichmentRequirements{Subject: true, DiscussionComments: true})
	if e.SubjectErr != nil || e.DiscussionCommentsErr != nil || len(e.DiscussionComments) != 2 {
		t.Fatalf("enrichment=%+v", e)
	}
	if e.DiscussionComments[1].Body != "historical @github/notifications" {
		t.Fatalf("comments=%+v", e.DiscussionComments)
	}
	wantPaths := []string{
		"/repos/github/repo/discussions/7",
		"/repos/github/repo/discussions/7/comments?per_page=100",
		"/repos/github/repo/discussions/7/comments?per_page=100&page=2",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths=%v want=%v", paths, wantPaths)
	}
}

func TestEnrichFailureIsFieldSpecific(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "comments") {
			http.Error(w, "bad", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"body":"ok"}`))
	}))
	defer server.Close()
	item := model.Notification{Subject: model.Subject{URL: server.URL + "/discussion"}}
	e := testClient(server).Enrich(context.Background(), item, model.EnrichmentRequirements{Subject: true, DiscussionComments: true})
	if e.SubjectErr != nil || e.DiscussionCommentsErr == nil {
		t.Fatalf("enrichment=%+v", e)
	}
}
