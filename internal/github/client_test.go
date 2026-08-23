package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxbeizer/gh-hush/internal/github/transport"
	"github.com/maxbeizer/gh-hush/internal/model"
)

func testClient(server *httptest.Server) *CLIClient {
	return &CLIClient{transport: transport.New(transport.Config{HTTPClient: server.Client(), BaseURL: server.URL, Token: "test", Sleep: func(context.Context, time.Duration) error { return nil }})}
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
	item := model.Notification{Subject: model.Subject{Type: "Discussion", URL: server.URL + "/repos/github/repo/discussions/7"}}
	e := testClient(server).Enrich(context.Background(), item, model.EnrichmentRequirements{Subject: true, DiscussionComments: true})
	if e.SubjectErr != nil || e.DiscussionCommentsErr != nil || len(e.DiscussionComments) != 2 {
		t.Fatalf("enrichment=%+v", e)
	}
	want := []string{"/repos/github/repo/discussions/7", "/repos/github/repo/discussions/7/comments?per_page=100", "/repos/github/repo/discussions/7/comments?per_page=100&page=2"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths=%v want=%v", paths, want)
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
