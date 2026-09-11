package cmd

import (
	"context"
	"errors"
	"fmt"
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

func TestDefaultOperationExplainsHowToInitializeMissingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"--dry-run"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), filepath.Join(home, "gh-hush", "config.yml")) ||
		!strings.Contains(err.Error(), "gh hush init-config --user") {
		t.Fatalf("error=%v", err)
	}
}

func TestInitConfigCreatesValidConservativePolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	var out strings.Builder
	command := NewRootCommand(&out, io.Discard)
	command.SetArgs([]string{"init-config", "--user", "octocat", "--github-organization", "github", "--team", "github/notifications"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "gh-hush", "config.yml")
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("generated configuration is invalid: %v", err)
	}
	if cfg.User != "octocat" || cfg.GitHubOrganization != "github" || len(cfg.TeamSlugs) != 1 ||
		!config.Enabled(cfg.Keep.ExternalOrganizationIssues) || !config.Enabled(cfg.Keep.TeamMentionedDiscussions) ||
		!config.Enabled(cfg.Hush.AllOtherNotifications) {
		t.Fatalf("generated configuration=%#v", cfg)
	}
	if !strings.Contains(out.String(), "Created conservative starter configuration: "+path) {
		t.Fatalf("output=%q", out.String())
	}
}

func TestInitConfigRequiresIdentityAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"init-config", "--config", path})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "will not guess identity") {
		t.Fatalf("missing identity error=%v", err)
	}
	original := []byte("leave me alone\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	command = NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"init-config", "--config", path, "--user", "octocat", "--github-organization", "github"})
	if err := command.Execute(); err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite error=%v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("file=%q err=%v", got, err)
	}
}
func TestNoArgsRunsDefaultOperation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "gh-hush")
	_ = os.MkdirAll(dir, 0755)
	_ = os.WriteFile(filepath.Join(dir, "config.yml"), []byte(validConfigYAML), 0600)
	called := false
	command := newRootCommand(io.Discard, io.Discard, func(_ *cobra.Command, _, _ io.Writer, cfg config.Config, dry, confirm, quiet, debug bool) error {
		called = true
		if cfg.User != "octocat" || dry || confirm || quiet || debug {
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
	command := newRootCommand(io.Discard, io.Discard, func(_ *cobra.Command, _, _ io.Writer, _ config.Config, _, _, _, debug bool) error {
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

func TestValidateConfigDoesNotRunOperation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(validConfigYAML), 0600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	command := newRootCommand(&out, io.Discard, func(*cobra.Command, io.Writer, io.Writer, config.Config, bool, bool, bool, bool) error {
		t.Fatal("run operation should not be called")
		return nil
	})
	command.SetArgs([]string{"validate-config", "--config", path})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Configuration is valid: "+path) {
		t.Fatalf("output=%q", out.String())
	}
}

func TestValidateConfigRejectsExplicitEmptyPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"validate-config", "--config", ""})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), `read config ""`) {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateConfigReportsInvalidSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("user: octocat\nunexpected: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"validate-config", "--config", path})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), `unknown configuration field "unexpected"`) {
		t.Fatalf("error=%v", err)
	}
}

func TestDryRunAndConfirmAreMutuallyExclusive(t *testing.T) {
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"--dry-run", "--confirm"})
	if err := command.Execute(); err == nil {
		t.Fatal("expected error")
	}
}

func TestQuietAndDebugAreMutuallyExclusive(t *testing.T) {
	command := NewRootCommand(io.Discard, io.Discard)
	command.SetArgs([]string{"--quiet", "--debug"})
	if err := command.Execute(); err == nil {
		t.Fatal("expected error")
	}
}

func TestQuietFlagIsPassedToOperationAndDocumentedInHelp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(validConfigYAML), 0600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	called := false
	command := newRootCommand(&out, io.Discard, func(_ *cobra.Command, _, _ io.Writer, _ config.Config, _, _, quiet, _ bool) error {
		called = true
		if !quiet {
			t.Fatal("--quiet was not passed to the operation")
		}
		return nil
	})
	command.SetArgs([]string{"--quiet", "--config", path})
	if err := command.Execute(); err != nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}

	out.Reset()
	command = NewRootCommand(&out, io.Discard)
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--quiet") || !strings.Contains(out.String(), "concise result to stderr") {
		t.Fatalf("help=%q", out.String())
	}
}
func TestRunQuietOutcomes(t *testing.T) {
	eligible := model.Decision{Thread: notification("1", "subscribed"), Action: model.ActionUnsubscribeAndMarkDone}

	t.Run("confirmed", func(t *testing.T) {
		var stderr strings.Builder
		applied := false
		command := &cobra.Command{}
		command.SetIn(strings.NewReader("must not be read"))
		err := runQuiet(command, &stderr, []model.Decision{eligible}, false, true, false, func() error {
			applied = true
			_, _ = fmt.Fprintln(&stderr, "Done: 1 notification updated.")
			return nil
		})
		if err != nil || !applied || stderr.String() != "Done: 1 notification updated.\n" {
			t.Fatalf("err=%v applied=%v stderr=%q", err, applied, stderr.String())
		}
	})

	t.Run("interactive approved", func(t *testing.T) {
		var stderr strings.Builder
		command := &cobra.Command{}
		command.SetIn(strings.NewReader("y\n"))
		applied := false
		err := runQuiet(command, &stderr, []model.Decision{eligible}, false, false, true, func() error {
			applied = true
			_, _ = fmt.Fprintln(&stderr, "Done: 1 notification updated.")
			return nil
		})
		if err != nil || !applied || stderr.String() != "Unsubscribe from and mark 1 notifications Done? [y/N] Done: 1 notification updated.\n" {
			t.Fatalf("err=%v applied=%v stderr=%q", err, applied, stderr.String())
		}
	})

	t.Run("declined", func(t *testing.T) {
		var stderr strings.Builder
		command := &cobra.Command{}
		command.SetIn(strings.NewReader("n\n"))
		err := runQuiet(command, &stderr, []model.Decision{eligible}, false, false, true, func() error {
			t.Fatal("declined run applied changes")
			return nil
		})
		if err != nil || stderr.String() != "Unsubscribe from and mark 1 notifications Done? [y/N] No changes made.\n" {
			t.Fatalf("err=%v stderr=%q", err, stderr.String())
		}
	})

	t.Run("dry run", func(t *testing.T) {
		var stderr strings.Builder
		err := runQuiet(&cobra.Command{}, &stderr, []model.Decision{eligible}, true, false, false, func() error {
			t.Fatal("dry run applied changes")
			return nil
		})
		if err != nil || stderr.String() != "Would update 1 notification.\n" {
			t.Fatalf("err=%v stderr=%q", err, stderr.String())
		}
	})

	t.Run("no target", func(t *testing.T) {
		var stderr strings.Builder
		err := runQuiet(&cobra.Command{}, &stderr, nil, false, false, false, func() error {
			t.Fatal("no-target run applied changes")
			return nil
		})
		if err != nil || stderr.String() != "Done: no notification updates needed.\n" {
			t.Fatalf("err=%v stderr=%q", err, stderr.String())
		}
	})

	t.Run("non-interactive", func(t *testing.T) {
		err := runQuiet(&cobra.Command{}, io.Discard, []model.Decision{eligible}, false, false, false, func() error {
			t.Fatal("non-interactive run applied changes")
			return nil
		})
		if err == nil || err.Error() != "confirmation requires an interactive terminal; rerun with --confirm" {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("apply failure", func(t *testing.T) {
		want := errors.New("1 of 1 notification updates failed: unsubscribe failed")
		err := runQuiet(&cobra.Command{}, io.Discard, []model.Decision{eligible}, false, true, false, func() error { return want })
		if !errors.Is(err, want) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestRunQuietClassificationFailuresAreActionable(t *testing.T) {
	decisions := []model.Decision{
		{Thread: notification("1", "subscribed"), Action: model.ActionKeep, EnrichmentError: "subject request failed"},
		{Thread: notification("2", "subscribed"), Action: model.ActionUnsubscribeAndMarkDone, EnrichmentError: "comments request failed"},
	}
	for _, dryRun := range []bool{false, true} {
		var stderr strings.Builder
		err := runQuiet(&cobra.Command{}, &stderr, decisions, dryRun, true, false, func() error {
			t.Fatal("classification failure applied changes")
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "classification failed for 2 notifications") ||
			!strings.Contains(err.Error(), "notification 1: subject request failed") ||
			!strings.Contains(err.Error(), "notification 2: comments request failed") || stderr.Len() != 0 {
			t.Fatalf("dryRun=%v err=%v stderr=%q", dryRun, err, stderr.String())
		}
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
