package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/model"
	"github.com/spf13/cobra"
)

func TestDefaultOperationShowsHelpWhenConfigMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout strings.Builder
	command := NewRootCommand(&stdout, io.Discard)
	command.SetArgs(nil)

	if err := command.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want help without an error", err)
	}
	for _, expected := range []string{"Usage:", "--dry-run", "--confirm"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("help output missing %q", expected)
		}
	}
}

func TestNoArgsRunsDefaultOperation(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "gh-hush")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yml"), []byte(validConfigYAML), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	called := false
	command := newRootCommand(io.Discard, io.Discard, func(_ *cobra.Command, _, _ io.Writer, cfg config.Config, dryRun, confirm bool) error {
		called = true
		if cfg.User != "octocat" {
			t.Errorf("config user = %q, want octocat", cfg.User)
		}
		if dryRun || confirm {
			t.Errorf("default operation flags = dry-run %v, confirm %v; want both false", dryRun, confirm)
		}
		return nil
	})
	command.SetArgs(nil)

	if err := command.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want default operation", err)
	}
	if !called {
		t.Fatal("default operation was not invoked")
	}
}

func TestDryRunAndConfirmAreMutuallyExclusive(t *testing.T) {
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"--dry-run", "--confirm"})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "dry-run confirm") {
		t.Fatalf("Execute() error = %v, want mutually exclusive flags error", err)
	}
}

const validConfigYAML = `
user: octocat
github_organization: github
run_mode: ad_hoc
discussion_team_slugs:
  - github/notifications
keep:
  external_organization_issues: true
  personally_mentioned: true
  personally_assigned: true
  individually_review_requested: true
  authored_by_user: true
  team_mentioned_discussions: true
unsubscribe:
  all_other_notifications: true
output:
  default_mode: dry_run
  include_keep_decisions: true
  include_unsubscribe_decisions: true
  include_decision_reasons: true
`

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

func TestIsTerminalRejectsRedirectedStreams(t *testing.T) {
	redirectedFile, err := os.CreateTemp(t.TempDir(), "preview")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer redirectedFile.Close()

	for name, stream := range map[string]any{
		"in-memory input":  strings.NewReader("y\n"),
		"in-memory output": &strings.Builder{},
		"redirected file":  redirectedFile,
	} {
		t.Run(name, func(t *testing.T) {
			if isTerminal(stream) {
				t.Fatalf("isTerminal(%s) = true, want false", name)
			}
		})
	}
}

func TestPromptForConfirmationDefaultsToNo(t *testing.T) {
	tests := []struct {
		answer string
		want   bool
	}{
		{answer: "y\n", want: true},
		{answer: "YES\n", want: true},
		{answer: "\n", want: false},
		{answer: "no\n", want: false},
	}
	for _, test := range tests {
		var output strings.Builder
		got, err := promptForConfirmation(strings.NewReader(test.answer), &output, 3)
		if err != nil {
			t.Fatalf("promptForConfirmation(%q) error = %v", test.answer, err)
		}
		if got != test.want {
			t.Errorf("promptForConfirmation(%q) = %v, want %v", test.answer, got, test.want)
		}
		if !strings.Contains(output.String(), "Unsubscribe from 3 notifications? [y/N]") {
			t.Errorf("prompt output = %q", output.String())
		}
	}
}

func TestApplyUnsubscriptionsAppliesOnlyDisplayedUnsubscribeDecisions(t *testing.T) {
	client := &recordingUnsubscriber{failID: "third"}
	decisions := []model.Decision{
		{Thread: model.Notification{ID: "first"}, Action: model.ActionUnsubscribe, URL: "first-url"},
		{Thread: model.Notification{ID: "second"}, Action: model.ActionKeep, URL: "second-url"},
		{Thread: model.Notification{ID: "third"}, Action: model.ActionUnsubscribe, URL: "third-url"},
	}
	var progress strings.Builder

	err := applyUnsubscriptions(context.Background(), &progress, client, decisions)
	if err == nil || !strings.Contains(err.Error(), "third-url") {
		t.Fatalf("applyUnsubscriptions() error = %v, want third failure", err)
	}
	if got := strings.Join(client.ids, ","); got != "first,third" {
		t.Fatalf("unsubscribe IDs = %q, want first,third", got)
	}
	if !strings.Contains(progress.String(), "completed 1/2 notification updates") {
		t.Fatalf("progress = %q, want partial result", progress.String())
	}
}

type recordingUnsubscriber struct {
	ids    []string
	failID string
}

func (u *recordingUnsubscriber) UnsubscribeNotification(_ context.Context, id string) error {
	u.ids = append(u.ids, id)
	if id == u.failID {
		return errors.New("boom")
	}
	return nil
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
