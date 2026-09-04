package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/diagnostic"
	"github.com/maxbeizer/gh-hush/internal/model"
	"github.com/maxbeizer/gh-hush/internal/policy"
	"github.com/spf13/cobra"
)

const validConfigYAML = `
user: octocat
github_organization: github
team_slugs:
  - github/notifications
keep:
  external_organization_issues: true
  personally_mentioned: true
  personally_assigned: true
  individually_review_requested: true
  active_team_review_requested_pull_requests: true
  authored_by_user: true
  team_mentioned_discussions: true
hush:
  all_other_notifications: true
`

func TestVersionFlag(t *testing.T) {
	original := Version
	Version = "v0.1.0-test"
	t.Cleanup(func() { Version = original })

	var out strings.Builder
	command := NewRootCommand(&out, io.Discard)
	command.SetArgs([]string{"--version"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "gh-hush version v0.1.0-test\n"; got != want {
		t.Fatalf("output=%q want=%q", got, want)
	}
}

func TestDefaultOperationShowsHelpWhenConfigMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out strings.Builder
	command := NewRootCommand(&out, io.Discard)
	command.SetArgs(nil)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("output=%q", out.String())
	}
}
func TestNoArgsRunsDefaultOperation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "gh-hush")
	_ = os.MkdirAll(dir, 0755)
	_ = os.WriteFile(filepath.Join(dir, "config.yml"), []byte(validConfigYAML), 0600)
	called := false
	command := newRootCommand(io.Discard, io.Discard, func(_ *cobra.Command, _, _ io.Writer, cfg config.Config, dry, confirm, debug bool) error {
		called = true
		if cfg.User != "octocat" || dry || confirm || debug {
			t.Fail()
		}
		return nil
	})
	command.SetArgs(nil)
	if err := command.Execute(); err != nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}
func TestDebugFlagIsOptIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "gh-hush")
	_ = os.MkdirAll(dir, 0755)
	_ = os.WriteFile(filepath.Join(dir, "config.yml"), []byte(validConfigYAML), 0600)
	called := false
	command := newRootCommand(io.Discard, io.Discard, func(_ *cobra.Command, _, _ io.Writer, _ config.Config, _, _, debug bool) error {
		called = true
		if !debug {
			t.Fatal("--debug was not passed to the operation")
		}
		return nil
	})
	command.SetArgs([]string{"--debug"})
	if err := command.Execute(); err != nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestDryRunAndConfirmAreMutuallyExclusive(t *testing.T) {
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"--dry-run", "--confirm"})
	if err := command.Execute(); err == nil {
		t.Fatal("expected error")
	}
}
func TestPreviewEvidenceFailureSafetyKeepIsNotEligible(t *testing.T) {
	safetyKeep := model.Decision{
		Thread:          model.Notification{ID: "safe"},
		Action:          model.ActionKeep,
		EnrichmentError: "request exhausted 3 attempts",
	}
	if got := countHushActions([]model.Decision{safetyKeep}); got != 0 {
		t.Fatalf("countHushActions() = %d, want zero eligible targets", got)
	}

	eligible := model.Decision{Thread: model.Notification{ID: "eligible"}, Action: model.ActionUnsubscribeAndMarkDone}
	if got := countHushActions([]model.Decision{safetyKeep, eligible}); got != 1 {
		t.Fatalf("countHushActions() = %d, want the mixed preview's one eligible target", got)
	}
}

func TestPromptNamesBothEffectsAndDefaultsNo(t *testing.T) {
	for _, tt := range []struct {
		answer string
		want   bool
	}{{"y\n", true}, {"YES\n", true}, {"\x1b[200~y\x1b[201~\n", true}, {"\n", false}, {"no\n", false}} {
		var out strings.Builder
		got, err := promptForConfirmation(strings.NewReader(tt.answer), &out, 3)
		if err != nil || got != tt.want || !strings.Contains(out.String(), "Unsubscribe from and mark 3 notifications Done? [y/N]") {
			t.Fatalf("got=%v err=%v out=%q", got, err, out.String())
		}
	}
}

func TestClassifyNotificationsPreservesOrder(t *testing.T) {
	items := []model.Notification{notification("1", "subscribed"), notification("2", "mention")}
	var out strings.Builder
	got := classifyNotifications(context.Background(), &out, policy.NewEvaluator(testConfig(), &fakeClient{}), items)
	if got[0].Thread.ID != "1" || got[1].Thread.ID != "2" || !strings.Contains(out.String(), "unread notifications") {
		t.Fatalf("got=%#v out=%s", got, out.String())
	}
}

func TestDebugWorkflowEventsCoverClassification(t *testing.T) {
	var output strings.Builder
	logger := diagnostic.New(&output)
	ctx := diagnostic.WithLogger(context.Background(), logger)
	item := notification("thread-1", "subscribed")

	classifyNotifications(ctx, logger, policy.NewEvaluator(testConfig(), &fakeClient{}), []model.Notification{item})
	got := output.String()
	for _, want := range []string{
		"event=worker_start phase=classification thread_id=thread-1",
		"event=worker_complete phase=classification thread_id=thread-1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("debug output missing %q:\n%s", want, got)
		}
	}
}

type fakeClient struct{}

func (*fakeClient) FetchSubject(_ context.Context, _ model.Notification) (model.Resource, error) {
	return model.Resource{}, nil
}

func (*fakeClient) FetchDiscussionComments(_ context.Context, _ model.Notification) ([]model.Resource, error) {
	return nil, nil
}

func notification(id, reason string) model.Notification {
	return model.Notification{ID: id, Unread: true, Reason: reason, Repository: model.Repository{FullName: "github/repo"}, Subject: model.Subject{Type: "Issue", URL: "subject"}}
}
func testConfig() config.Config {
	on := true
	off := false
	cfg := config.Config{User: "octocat", GitHubOrganization: "github", Keep: config.Keep{ExternalOrganizationIssues: &on, PersonallyMentioned: &on, PersonallyAssigned: &off, IndividuallyReviewRequested: &off, AuthoredByUser: &off, TeamMentionedDiscussions: &off}}
	cfg.Hush.AllOtherNotifications = &on
	return cfg
}

// setClock installs a deterministic clock that returns the provided instants in
// order, repeating the final instant once exhausted so timing output never
// depends on wall-clock time or sleeping.
func setClock(t *testing.T, instants ...time.Time) {
	t.Helper()
	original := now
	var mu sync.Mutex
	index := 0
	now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		instant := instants[index]
		if index < len(instants)-1 {
			index++
		}
		return instant
	}
	t.Cleanup(func() { now = original })
}

func TestFormatDurationPrecision(t *testing.T) {
	for _, tt := range []struct {
		in   time.Duration
		want string
	}{
		{-5 * time.Second, "0ms"},
		{0, "0ms"},
		{845 * time.Millisecond, "845ms"},
		{time.Second, "1.0s"},
		{18400 * time.Millisecond, "18.4s"},
		{42700 * time.Millisecond, "42.7s"},
		{62300 * time.Millisecond, "1m02.3s"},
		{125 * time.Second, "2m05.0s"},
	} {
		if got := formatDuration(tt.in); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClassifyReportsElapsed(t *testing.T) {
	t.Run("notifications", func(t *testing.T) {
		setClock(t, time.Unix(0, 0), time.Unix(0, 0).Add(1200*time.Millisecond))
		var out strings.Builder
		classifyNotifications(context.Background(), &out, policy.NewEvaluator(testConfig(), &fakeClient{}), []model.Notification{notification("1", "subscribed"), notification("2", "mention")})
		if !strings.Contains(out.String(), "classified 2/2 notifications in 1.2s") {
			t.Fatalf("classification timing missing: %q", out.String())
		}
	})

	t.Run("empty inbox", func(t *testing.T) {
		setClock(t, time.Unix(0, 0), time.Unix(0, 0).Add(25*time.Millisecond))
		var out strings.Builder
		classifyNotifications(context.Background(), &out, policy.NewEvaluator(testConfig(), &fakeClient{}), nil)
		if got, want := out.String(), "No unread notifications to classify.\nclassified 0/0 notifications in 25ms\n"; got != want {
			t.Fatalf("output=%q want=%q", got, want)
		}
	})
}
