package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/github/transport"
	"github.com/maxbeizer/gh-hush/internal/model"
	"github.com/maxbeizer/gh-hush/internal/policy"
	"github.com/maxbeizer/gh-hush/internal/report"
)

func testClient(server *httptest.Server) *CLIClient {
	return &CLIClient{transport: transport.New(transport.Config{HTTPClient: server.Client(), BaseURL: server.URL, Token: "test"})}
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
	if client.transport == nil {
		t.Fatal("client transport is nil")
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

func TestDecodeErrorsUseSanitizedEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`decode-body-secret`))
	}))
	defer server.Close()
	client := testClient(server)
	var target model.User
	err := client.get(context.Background(), "/user?access_token=decode-secret#fragment-secret", &target)
	if err == nil || !strings.Contains(err.Error(), `"/user"`) {
		t.Fatalf("err=%v", err)
	}
	for _, forbidden := range []string{"decode-secret", "fragment-secret", "decode-body-secret", "access_token", server.URL} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("decode error exposed %q: %v", forbidden, err)
		}
	}
}

func TestGetNotificationHandlesHistoricalAndMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notifications/threads/missing":
			http.NotFound(w, r)
		case "/notifications/threads/historical":
			_, _ = w.Write([]byte(`{"id":"historical","unread":false}`))
		default:
			_, _ = w.Write([]byte(`{"id":"123","unread":true}`))
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

func TestEvidenceAdapterFetchesSubjectAndCompletePaginatedDiscussionHistory(t *testing.T) {
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
	subject, subjectErr := client.FetchSubject(context.Background(), item)
	comments, commentsErr := client.FetchDiscussionComments(context.Background(), item)
	if subjectErr != nil || commentsErr != nil || subject.Body != "discussion" || len(comments) != 2 {
		t.Fatalf("subject=%+v subjectErr=%v comments=%+v commentsErr=%v", subject, subjectErr, comments, commentsErr)
	}
	if comments[1].Body != "historical @github/notifications" {
		t.Fatalf("comments=%+v", comments)
	}
	want := []string{"/repos/github/repo/discussions/7", "/repos/github/repo/discussions/7/comments?per_page=100", "/repos/github/repo/discussions/7/comments?per_page=100&page=2"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths=%v want=%v", paths, want)
	}
}

func TestEvidenceAdapterReturnsAccumulatedDiscussionCommentsWithPaginationError(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			http.Error(w, "later page unavailable", http.StatusBadRequest)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/discussion/comments?per_page=100&page=2>; rel="next"`, server.URL))
		_, _ = w.Write([]byte(`[{"body":"first page @github/notifications"}]`))
	}))
	defer server.Close()

	item := model.Notification{Subject: model.Subject{Type: "Discussion", URL: server.URL + "/discussion"}}
	comments, err := testClient(server).FetchDiscussionComments(context.Background(), item)
	if len(comments) != 1 || comments[0].Body != "first page @github/notifications" {
		t.Fatalf("comments=%+v", comments)
	}
	if err == nil || !strings.Contains(err.Error(), "fetch complete Discussion comment history: ") || !strings.Contains(err.Error(), "400 Bad Request") {
		t.Fatalf("error=%v", err)
	}
}

func TestEvidenceAdapterReturnsPartiallyDecodedSubjectWithError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"html_url":"https://github.test/discussion/7","body":123}`))
	}))
	defer server.Close()

	item := model.Notification{Subject: model.Subject{URL: server.URL + "/discussion"}}
	subject, err := testClient(server).FetchSubject(context.Background(), item)
	if subject.HTMLURL != "https://github.test/discussion/7" {
		t.Fatalf("subject=%+v", subject)
	}
	if err == nil || !strings.Contains(err.Error(), "fetch subject: decode GitHub API response") {
		t.Fatalf("error=%v", err)
	}
}

type previewFixtureTransport struct {
	notifications []byte
	subjects      map[string][]byte
	failures      map[string]error
}

func (t *previewFixtureTransport) Request(_ context.Context, _ string, endpoint string) (transport.Response, error) {
	if err := t.failures[endpoint]; err != nil {
		return transport.Response{}, err
	}
	body, ok := t.subjects[endpoint]
	if !ok {
		return transport.Response{}, fmt.Errorf("unexpected endpoint %q", endpoint)
	}
	return transport.Response{Body: body, Endpoint: endpoint}, nil
}

func (t *previewFixtureTransport) Pages(_ context.Context, endpoint string, visit func(transport.Response) error) error {
	if endpoint != "/notifications?per_page=100" {
		return fmt.Errorf("unexpected paginated endpoint %q", endpoint)
	}
	return visit(transport.Response{Body: t.notifications, Endpoint: "/notifications"})
}

func TestRealisticPreviewPayloadsNeverPrintAPIURLs(t *testing.T) {
	subjects := []struct{ subjectType, apiPath, htmlPath string }{
		{"Issue", "issues/1", "issues/1"},
		{"PullRequest", "pulls/2", "pull/2"},
		{"Discussion", "discussions/3", "discussions/3"},
		{"Commit", "commits/abc123", "commit/abc123"},
		{"Release", "releases/5", "releases/tag/v1.0.0"},
		{"CheckSuite", "check-suites/6", "actions/runs/6"},
	}
	fixture := &previewFixtureTransport{subjects: map[string][]byte{}, failures: map[string]error{}}
	var notifications []model.Notification
	var exactURLs []string
	for index, subject := range subjects {
		apiURL := "https://api.github.com/repos/acme/repo/" + subject.apiPath
		htmlURL := "https://github.com/acme/repo/" + subject.htmlPath
		notifications = append(notifications, model.Notification{
			ID: fmt.Sprint(index + 1), Unread: true, Reason: "subscribed",
			Repository: model.Repository{FullName: "acme/repo", HTMLURL: "https://github.com/acme/repo"},
			Subject:    model.Subject{Title: subject.subjectType + " fixture", Type: subject.subjectType, URL: apiURL},
		})
		fixture.subjects[apiURL], _ = json.Marshal(model.Resource{HTMLURL: htmlURL})
		exactURLs = append(exactURLs, htmlURL)
	}

	// Exercise both enrichment failure and an unsafe repository html_url. The
	// repository full_name remains sufficient for a browser-facing fallback.
	const failedAPIURL = "https://api.github.com/repos/acme/fallback/issues/7"
	notifications = append(notifications, model.Notification{
		ID: "7", Unread: true, Reason: "mention",
		Repository: model.Repository{FullName: "acme/fallback", HTMLURL: "https://api.github.com/repos/acme/fallback"},
		Subject:    model.Subject{Title: "fallback fixture", Type: "Issue", URL: failedAPIURL},
	})
	fixture.failures[failedAPIURL] = errors.New("display enrichment unavailable")
	fixture.notifications, _ = json.Marshal(notifications)

	client := &CLIClient{transport: fixture}
	threads, err := client.ListNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	evaluator := policy.NewEvaluator(config.Config{}, client)
	decisions := make([]model.Decision, len(threads))
	for index, thread := range threads {
		decisions[index] = evaluator.EvaluateForPreview(context.Background(), thread)
	}
	var output bytes.Buffer
	if err := report.Write(&output, decisions); err != nil {
		t.Fatal(err)
	}
	preview := output.String()
	if strings.Contains(preview, "api.github.com") {
		t.Fatalf("preview leaked an API URL:\n%s", preview)
	}
	for _, exactURL := range exactURLs {
		if !strings.Contains(preview, "URL: "+exactURL+"\n") {
			t.Errorf("preview missing exact subject URL %q:\n%s", exactURL, preview)
		}
	}
	if !strings.Contains(preview, "URL: https://github.com/acme/fallback\n") {
		t.Errorf("preview missing safe repository fallback:\n%s", preview)
	}
}

func TestEvidenceAdapterReportsEachFieldFailureIndependently(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "comments") {
			http.Error(w, "bad", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"body":"ok"}`))
	}))
	defer server.Close()
	item := model.Notification{Subject: model.Subject{URL: server.URL + "/discussion"}}
	client := testClient(server)
	subject, subjectErr := client.FetchSubject(context.Background(), item)
	_, commentsErr := client.FetchDiscussionComments(context.Background(), item)
	if subjectErr != nil || subject.Body != "ok" || commentsErr == nil {
		t.Fatalf("subject=%+v subjectErr=%v commentsErr=%v", subject, subjectErr, commentsErr)
	}
}
