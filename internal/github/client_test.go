package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/maxbeizer/gh-hush/internal/model"
)

func TestListNotificationsInvokesGHAndFlattensPages(t *testing.T) {
	var gotArgs []string
	client := &CLIClient{commandRunner: func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(`[[{"id":"first"},{"id":"second"}],[{"id":"third"}]]`), nil
	}}

	notifications, err := client.ListNotifications(context.Background())
	if err != nil {
		t.Fatalf("ListNotifications() error = %v", err)
	}
	wantArgs := []string{
		"api", "--paginate", "--slurp", "-H",
		"Accept: application/vnd.github+json", "/notifications?per_page=100",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("ListNotifications() gh args = %#v, want %#v", gotArgs, wantArgs)
	}
	wantIDs := []string{"first", "second", "third"}
	gotIDs := make([]string, len(notifications))
	for i := range notifications {
		gotIDs[i] = notifications[i].ID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("ListNotifications() IDs = %#v, want %#v", gotIDs, wantIDs)
	}
}

func TestUnsubscribeNotificationUnsubscribesAndMarksThreadRead(t *testing.T) {
	var gotCalls [][]string
	client := &CLIClient{commandRunner: func(_ context.Context, args ...string) ([]byte, error) {
		gotCalls = append(gotCalls, append([]string(nil), args...))
		return nil, nil
	}}

	if err := client.UnsubscribeNotification(context.Background(), "12345"); err != nil {
		t.Fatalf("UnsubscribeNotification() error = %v", err)
	}
	wantCalls := [][]string{
		{
			"api", "--method", "DELETE", "-H",
			"Accept: application/vnd.github+json", "/notifications/threads/12345/subscription",
		},
		{
			"api", "--method", "PATCH", "-H",
			"Accept: application/vnd.github+json", "/notifications/threads/12345",
		},
	}
	if !reflect.DeepEqual(gotCalls, wantCalls) {
		t.Fatalf("UnsubscribeNotification() gh calls = %#v, want %#v", gotCalls, wantCalls)
	}
}

func TestUnsubscribeNotificationReportsMarkReadFailureAfterUnsubscribing(t *testing.T) {
	markReadErr := errors.New("patch failed")
	var gotCalls [][]string
	client := &CLIClient{commandRunner: func(_ context.Context, args ...string) ([]byte, error) {
		gotCalls = append(gotCalls, append([]string(nil), args...))
		if len(gotCalls) == 2 {
			return nil, markReadErr
		}
		return nil, nil
	}}

	err := client.UnsubscribeNotification(context.Background(), "12345")
	if !errors.Is(err, markReadErr) {
		t.Fatalf("UnsubscribeNotification() error = %v, want wrapped %v", err, markReadErr)
	}
	if !strings.Contains(err.Error(), "mark unsubscribed notification thread \"12345\" as read") {
		t.Fatalf("UnsubscribeNotification() error = %q, want mark-as-read context", err)
	}
	wantCalls := [][]string{
		{
			"api", "--method", "DELETE", "-H",
			"Accept: application/vnd.github+json", "/notifications/threads/12345/subscription",
		},
		{
			"api", "--method", "PATCH", "-H",
			"Accept: application/vnd.github+json", "/notifications/threads/12345",
		},
	}
	if !reflect.DeepEqual(gotCalls, wantCalls) {
		t.Fatalf("UnsubscribeNotification() gh calls = %#v, want %#v", gotCalls, wantCalls)
	}
}

func TestUnsubscribeNotificationDoesNotMarkReadWhenUnsubscribeFails(t *testing.T) {
	unsubscribeErr := errors.New("delete failed")
	calls := 0
	client := &CLIClient{commandRunner: func(_ context.Context, _ ...string) ([]byte, error) {
		calls++
		return nil, unsubscribeErr
	}}

	err := client.UnsubscribeNotification(context.Background(), "12345")
	if !errors.Is(err, unsubscribeErr) {
		t.Fatalf("UnsubscribeNotification() error = %v, want wrapped %v", err, unsubscribeErr)
	}
	if calls != 1 {
		t.Fatalf("command runner calls = %d, want 1", calls)
	}
}

func TestUnsubscribeNotificationRejectsEmptyID(t *testing.T) {
	client := &CLIClient{commandRunner: func(context.Context, ...string) ([]byte, error) {
		t.Fatal("command runner called for empty thread ID")
		return nil, nil
	}}
	if err := client.UnsubscribeNotification(context.Background(), " "); err == nil {
		t.Fatal("UnsubscribeNotification() error = nil, want empty ID error")
	}
}

func TestListNotificationsEmptyResponse(t *testing.T) {
	client := &CLIClient{commandRunner: func(context.Context, ...string) ([]byte, error) {
		return []byte(`[]`), nil
	}}

	notifications, err := client.ListNotifications(context.Background())
	if err != nil {
		t.Fatalf("ListNotifications() error = %v", err)
	}
	if len(notifications) != 0 {
		t.Fatalf("ListNotifications() returned %d notifications, want 0", len(notifications))
	}
}

func TestListNotificationsErrors(t *testing.T) {
	runnerErr := errors.New("runner failed")
	tests := []struct {
		name        string
		output      string
		runnerErr   error
		wantError   string
		wantWrapped error
	}{
		{
			name:      "malformed JSON",
			output:    `not JSON`,
			wantError: "decode paginated notifications",
		},
		{
			name:        "command runner failure",
			runnerErr:   runnerErr,
			wantError:   runnerErr.Error(),
			wantWrapped: runnerErr,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &CLIClient{commandRunner: func(context.Context, ...string) ([]byte, error) {
				return []byte(test.output), test.runnerErr
			}}
			_, err := client.ListNotifications(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ListNotifications() error = %v, want error containing %q", err, test.wantError)
			}
			if test.wantWrapped != nil && !errors.Is(err, test.wantWrapped) {
				t.Fatalf("ListNotifications() error = %v, want wrapped %v", err, test.wantWrapped)
			}
		})
	}
}

func TestGetHTTPResponseErrors(t *testing.T) {
	tests := []struct {
		name      string
		handler   http.HandlerFunc
		wantError string
	}{
		{
			name: "non-2xx includes request ID",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-GitHub-Request-Id", "request-123")
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			},
			wantError: `returned 503 Service Unavailable (request ID "request-123")`,
		},
		{
			name: "malformed JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"id":`))
			},
			wantError: "decode GitHub API response",
		},
		{
			name: "oversized response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+1))
			},
			wantError: fmt.Sprintf("exceeded %d bytes", maxResponseBytes),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			client := &CLIClient{httpClient: server.Client(), token: "test"}

			var target model.Resource
			err := client.get(context.Background(), server.URL+"/resource", &target)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("get() error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

func TestEnrichReportsMissingRequiredSubjectURL(t *testing.T) {
	client := &CLIClient{commandRunner: func(context.Context, ...string) ([]byte, error) {
		t.Fatal("command runner called for missing subject URL")
		return nil, nil
	}}

	enrichment := client.Enrich(
		context.Background(),
		model.Notification{},
		model.EnrichmentRequirements{Subject: true},
	)
	if enrichment.SubjectErr == nil || enrichment.SubjectErr.Error() != "notification subject did not include an API URL" {
		t.Fatalf("Enrich() subject error = %v", enrichment.SubjectErr)
	}
}

func TestEnrichFetchesOnlyRequiredEvidence(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"html_url":"https://github.com/example/repo/1","body":"evidence"}`))
	}))
	defer server.Close()

	client := &CLIClient{httpClient: server.Client(), token: "test"}
	thread := model.Notification{Subject: model.Subject{
		Type:             "PullRequest",
		URL:              server.URL + "/subject",
		LatestCommentURL: server.URL + "/comment",
	}}

	enrichment := client.Enrich(context.Background(), thread, model.EnrichmentRequirements{Subject: true})
	if enrichment.SubjectErr != nil || enrichment.LatestCommentErr != nil {
		t.Fatalf("Enrich() errors = subject %v, comment %v", enrichment.SubjectErr, enrichment.LatestCommentErr)
	}
	if requests["/subject"] != 1 {
		t.Fatalf("subject requests = %d, want 1", requests["/subject"])
	}
	if requests["/comment"] != 0 {
		t.Fatalf("latest comment requests = %d, want 0", requests["/comment"])
	}

	requests = make(map[string]int)
	client.Enrich(context.Background(), thread, model.EnrichmentRequirements{})
	if len(requests) != 0 {
		t.Fatalf("requests with no requirements = %#v, want none", requests)
	}
}

func TestEnrichTracksRequiredFailuresIndependently(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/subject" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"body":"@github/notifications"}`))
	}))
	defer server.Close()

	client := &CLIClient{httpClient: server.Client(), token: "test"}
	thread := model.Notification{Subject: model.Subject{
		Type:             "Discussion",
		URL:              server.URL + "/subject",
		LatestCommentURL: server.URL + "/comment",
	}}

	enrichment := client.Enrich(context.Background(), thread, model.EnrichmentRequirements{Subject: true, LatestComment: true})
	if enrichment.SubjectErr == nil {
		t.Fatal("Enrich() subject error = nil, want failure")
	}
	if enrichment.LatestCommentErr != nil {
		t.Fatalf("Enrich() latest comment error = %v, want nil", enrichment.LatestCommentErr)
	}
	if enrichment.LatestComment.Body != "@github/notifications" {
		t.Fatalf("Enrich() latest comment body = %q", enrichment.LatestComment.Body)
	}
}
