package cmd

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/model"
)

func TestApplyManifestIsUnavailable(t *testing.T) {
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"--apply-manifest", "reviewed.json"})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "intentionally unavailable in v1") {
		t.Fatalf("Execute() error = %v, want unavailable-in-v1 error", err)
	}
}

func TestNoImplicitOperation(t *testing.T) {
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs(nil)

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "no operation selected") {
		t.Fatalf("Execute() error = %v, want explicit operation error", err)
	}
}

func TestClassifyNotificationsReportsProgressAndPreservesOrder(t *testing.T) {
	const notificationCount = 30
	threads := make([]model.Notification, notificationCount)
	for index := range threads {
		threads[index] = model.Notification{
			ID:     string(rune('A' + index)),
			Reason: "subscribed",
			Repository: model.Repository{
				FullName: "example/repo",
			},
			Subject: model.Subject{
				Type: "PullRequest",
			},
		}
	}

	enricher := &trackingEnricher{}
	var progress strings.Builder
	decisions := classifyNotifications(context.Background(), &progress, testConfig(), enricher, threads)

	if enricher.maxConcurrent < 2 {
		t.Fatalf("classifyNotifications() max concurrency = %d, want at least 2", enricher.maxConcurrent)
	}
	for index, decision := range decisions {
		if decision.Thread.ID != threads[index].ID {
			t.Fatalf("decision %d thread ID = %q, want %q", index, decision.Thread.ID, threads[index].ID)
		}
	}
	for _, expected := range []string{
		"classifying 30 notifications (read-only)...",
		"classified 25/30 notifications",
		"classified 30/30 notifications",
	} {
		if !strings.Contains(progress.String(), expected) {
			t.Errorf("progress missing %q", expected)
		}
	}
}

type trackingEnricher struct {
	mu            sync.Mutex
	concurrent    int
	maxConcurrent int
}

func (e *trackingEnricher) Enrich(context.Context, model.Notification) model.Enrichment {
	e.mu.Lock()
	e.concurrent++
	e.maxConcurrent = max(e.maxConcurrent, e.concurrent)
	e.mu.Unlock()

	time.Sleep(time.Millisecond)

	e.mu.Lock()
	e.concurrent--
	e.mu.Unlock()
	return model.Enrichment{}
}

func testConfig() config.Config {
	enabled := true
	return config.Config{
		User:               "octocat",
		GitHubOrganization: "github",
		Keep: config.Keep{
			ExternalOrganizationIssues:  &enabled,
			PersonallyMentioned:         &enabled,
			PersonallyAssigned:          &enabled,
			IndividuallyReviewRequested: &enabled,
			AuthoredByUser:              &enabled,
			TeamMentionedDiscussions:    &enabled,
		},
	}
}
