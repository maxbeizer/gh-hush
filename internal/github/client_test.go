package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/maxbeizer/gh-hush/internal/model"
)

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
