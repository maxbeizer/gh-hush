package cmd

import (
	"context"
	"io"
	"path/filepath"
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

func TestConfigFlagIsAnOptionalOverride(t *testing.T) {
	command := NewRootCommand(io.Discard, io.Discard)
	configFlag := command.Flag("config")
	if configFlag == nil {
		t.Fatal("config flag is missing")
	}
	if configFlag.DefValue != "" {
		t.Fatalf("config flag default = %q, want empty override", configFlag.DefValue)
	}
	if !strings.Contains(configFlag.Usage, "override") {
		t.Fatalf("config flag usage = %q, want override wording", configFlag.Usage)
	}
}

func TestMissingDefaultConfigShowsHelp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout strings.Builder
	command := NewRootCommand(&stdout, io.Discard)
	command.SetArgs([]string{"--dry-run"})

	if err := command.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want help without an error", err)
	}
	for _, expected := range []string{"Usage:", "--dry-run", "--config"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("help output missing %q", expected)
		}
	}
}

func TestMissingExplicitConfigReturnsError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	explicitPath := filepath.Join(t.TempDir(), "missing.yml")
	var stdout strings.Builder
	command := NewRootCommand(&stdout, io.Discard)
	command.SetArgs([]string{"--dry-run", "--config", explicitPath})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("Execute() error = %v, want config read error", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no help output", stdout.String())
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

func (e *trackingEnricher) Enrich(context.Context, model.Notification, model.EnrichmentRequirements) model.Enrichment {
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
