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
	"github.com/maxbeizer/gh-hush/internal/model"
	"github.com/spf13/cobra"
)

const validConfigYAML = `
user: octocat
github_organization: github
discussion_team_slugs:
  - github/notifications
keep:
  external_organization_issues: true
  personally_mentioned: true
  personally_assigned: true
  individually_review_requested: true
  authored_by_user: true
  team_mentioned_discussions: true
hush:
  all_other_notifications: true
`

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
	command := newRootCommand(io.Discard, io.Discard, func(_ *cobra.Command, _, _ io.Writer, cfg config.Config, dry, confirm bool) error {
		called = true
		if cfg.User != "octocat" || dry || confirm {
			t.Fail()
		}
		return nil
	})
	command.SetArgs(nil)
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

func TestApplyHushActionsSuccessAndEndpointOrdering(t *testing.T) {
	item := notification("1", "subscribed")
	client := &fakeClient{}
	var progress strings.Builder
	err := applyHushActions(context.Background(), &progress, testConfig(), client, []model.Decision{{Thread: item, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(client.calls, ",") != "unsubscribe:1,done:1" {
		t.Fatalf("calls=%v", client.calls)
	}
	for _, text := range []string{"unsubscribe_succeeded=1", "done_succeeded=1", "verification_succeeded=1"} {
		if !strings.Contains(progress.String(), text) {
			t.Errorf("progress missing %q: %s", text, progress.String())
		}
	}
}

func TestApplyDoesNotMarkDoneAfterUnsubscribeFailureAndContinues(t *testing.T) {
	first, second := notification("1", "subscribed"), notification("2", "subscribed")
	client := &fakeClient{unsubscribeFailures: map[string]error{"1": errors.New("request exhausted 3 attempts")}}
	var progress strings.Builder
	err := applyHushActions(context.Background(), &progress, testConfig(), client, []model.Decision{{Thread: first, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}, {Thread: second, Action: model.ActionUnsubscribeAndMarkDone, URL: "two"}})
	if err == nil || !strings.Contains(err.Error(), "exhausted 3 attempts") {
		t.Fatalf("err=%v", err)
	}
	requireOperations(t, client, "1", "get,enrich,unsubscribe")
	requireOperations(t, client, "2", "get,enrich,unsubscribe,done,get")
	if !strings.Contains(progress.String(), "unsubscribe_failed=1") || !strings.Contains(progress.String(), "verification_succeeded=1") {
		t.Fatalf("summary=%s", progress.String())
	}
}

func TestApplyContinuesAfterDoneRetryExhaustion(t *testing.T) {
	first, second := notification("1", "subscribed"), notification("2", "subscribed")
	client := &fakeClient{doneFailures: map[string]error{"1": errors.New("request exhausted 3 attempts")}}
	var out strings.Builder
	err := applyHushActions(context.Background(), &out, testConfig(), client, []model.Decision{
		{Thread: first, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"},
		{Thread: second, Action: model.ActionUnsubscribeAndMarkDone, URL: "two"},
	})
	if err == nil || !strings.Contains(err.Error(), "Done failed") || !strings.Contains(err.Error(), "exhausted 3 attempts") {
		t.Fatalf("err=%v", err)
	}
	requireOperations(t, client, "1", "get,enrich,unsubscribe,done")
	requireOperations(t, client, "2", "get,enrich,unsubscribe,done,get")
	if !strings.Contains(out.String(), "done_failed=1") || !strings.Contains(out.String(), "verification_succeeded=1") {
		t.Fatalf("summary=%s", out.String())
	}
}

func TestApplyPartialDoneAndVerificationFailures(t *testing.T) {
	t.Run("Done failure", func(t *testing.T) {
		item := notification("1", "subscribed")
		client := &fakeClient{doneFailures: map[string]error{"1": errors.New("done failed")}}
		var out strings.Builder
		err := applyHushActions(context.Background(), &out, testConfig(), client, []model.Decision{{Thread: item, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}})
		if err == nil || !strings.Contains(err.Error(), "unsubscribe succeeded but Done failed") || !strings.Contains(out.String(), "done_failed=1") {
			t.Fatalf("err=%v out=%s", err, out.String())
		}
	})
	t.Run("verification still present", func(t *testing.T) {
		item := notification("1", "subscribed")
		client := &fakeClient{getResults: map[string][]fakeGetResult{"1": {{thread: item, found: true}, {thread: item, found: true}}}}
		var out strings.Builder
		err := applyHushActions(context.Background(), &out, testConfig(), client, []model.Decision{{Thread: item, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}})
		if err == nil || !strings.Contains(err.Error(), "verification still found") || !strings.Contains(out.String(), "verification_failed=1") {
			t.Fatalf("err=%v out=%s", err, out.String())
		}
	})
}

func TestApplyRevalidationEvidenceFailureReturnsErrorAndContinues(t *testing.T) {
	first, second := notification("1", "subscribed"), notification("2", "subscribed")
	client := &fakeClient{enrichments: map[string]model.Enrichment{"1": {SubjectErr: errors.New("request exhausted 3 attempts")}}}
	cfg := testConfig()
	on := true
	cfg.Keep.PersonallyAssigned = &on
	var out strings.Builder
	err := applyHushActions(context.Background(), &out, cfg, client, []model.Decision{
		{Thread: first, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"},
		{Thread: second, Action: model.ActionUnsubscribeAndMarkDone, URL: "two"},
	})
	if err == nil || !strings.Contains(err.Error(), "revalidation evidence fetch failed") || !strings.Contains(err.Error(), "exhausted 3 attempts") {
		t.Fatalf("err=%v", err)
	}
	requireOperations(t, client, "1", "get,enrich")
	requireOperations(t, client, "2", "get,enrich,unsubscribe,done,get")
	if !strings.Contains(out.String(), "revalidation_failed=1") || !strings.Contains(out.String(), "verification_succeeded=1") {
		t.Fatalf("summary=%s", out.String())
	}
}

func TestApplyVerificationRetryExhaustionReturnsErrorAndContinues(t *testing.T) {
	first, second := notification("1", "subscribed"), notification("2", "subscribed")
	client := &fakeClient{getResults: map[string][]fakeGetResult{
		"1": {{thread: first, found: true}, {err: errors.New("request exhausted 3 attempts")}},
	}}
	var out strings.Builder
	err := applyHushActions(context.Background(), &out, testConfig(), client, []model.Decision{
		{Thread: first, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"},
		{Thread: second, Action: model.ActionUnsubscribeAndMarkDone, URL: "two"},
	})
	if err == nil || !strings.Contains(err.Error(), "verification failed") || !strings.Contains(err.Error(), "exhausted 3 attempts") {
		t.Fatalf("err=%v", err)
	}
	requireOperations(t, client, "1", "get,enrich,unsubscribe,done,get")
	requireOperations(t, client, "2", "get,enrich,unsubscribe,done,get")
	if !strings.Contains(out.String(), "verification_failed=1") || !strings.Contains(out.String(), "verification_succeeded=1") {
		t.Fatalf("summary=%s", out.String())
	}
}

func TestApplyRevalidationSkipsDisappearedOrNewlyProtected(t *testing.T) {
	t.Run("disappeared", func(t *testing.T) {
		item := notification("1", "subscribed")
		client := &fakeClient{getResults: map[string][]fakeGetResult{"1": {{found: false}}}}
		var out strings.Builder
		if err := applyHushActions(context.Background(), &out, testConfig(), client, []model.Decision{{Thread: item, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}}); err != nil {
			t.Fatal(err)
		}
		if len(client.calls) != 0 || !strings.Contains(out.String(), "disappeared=1") {
			t.Fatalf("calls=%v out=%s", client.calls, out.String())
		}
	})
	t.Run("protected", func(t *testing.T) {
		preview := notification("1", "subscribed")
		fresh := notification("1", "mention")
		client := &fakeClient{getResults: map[string][]fakeGetResult{"1": {{thread: fresh, found: true}}}}
		var out strings.Builder
		if err := applyHushActions(context.Background(), &out, testConfig(), client, []model.Decision{{Thread: preview, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}}); err != nil {
			t.Fatal(err)
		}
		if len(client.calls) != 0 || !strings.Contains(out.String(), "protected=1") || !strings.Contains(out.String(), "keep.personal_mention") {
			t.Fatalf("calls=%v out=%s", client.calls, out.String())
		}
	})
}

func TestApplyUsesBoundedConcurrencyAndPreservesPerThreadOrdering(t *testing.T) {
	const targetCount = 8
	client := newBlockingClient(targetCount)
	done := make(chan error, 1)
	go func() {
		done <- applyHushActions(context.Background(), io.Discard, testConfig(), client, hushDecisions(targetCount))
	}()

	for range applyMaxWorkers {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("workers did not make progress concurrently")
		}
	}
	select {
	case id := <-client.started:
		t.Fatalf("started more than %d concurrent threads before a worker was released; extra=%s", applyMaxWorkers, id)
	case <-time.After(50 * time.Millisecond):
	}
	close(client.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if client.maximumActive() != applyMaxWorkers {
		t.Fatalf("maximum active=%d want=%d", client.maximumActive(), applyMaxWorkers)
	}
	for index := 1; index <= targetCount; index++ {
		requireOperations(t, &client.fakeClient, fmt.Sprint(index), "get,enrich,unsubscribe,done,get")
	}
}

func TestApplyCancellationStopsFurtherScheduling(t *testing.T) {
	client := newBlockingClient(20)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- applyHushActions(ctx, io.Discard, testConfig(), client, hushDecisions(20))
	}()
	for range applyMaxWorkers {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("apply did not stop promptly after cancellation")
	}
	if got := client.startedCount(); got != applyMaxWorkers {
		t.Fatalf("started=%d want=%d; work continued scheduling after cancellation", got, applyMaxWorkers)
	}
}

func TestApplyAggregatesFailuresInTargetOrder(t *testing.T) {
	client := &fakeClient{unsubscribeFailures: map[string]error{
		"1": errors.New("failure-one"),
		"2": errors.New("failure-two"),
	}}
	var out strings.Builder
	err := applyHushActions(context.Background(), &out, testConfig(), client, hushDecisions(2))
	if err == nil {
		t.Fatal("expected aggregate error")
	}
	message := err.Error()
	if one, two := strings.Index(message, "failure-one"), strings.Index(message, "failure-two"); one < 0 || two < 0 || one >= two {
		t.Fatalf("failures not in target order: %s", message)
	}
	if !strings.Contains(out.String(), "unsubscribe_failed=2") {
		t.Fatalf("summary=%s", out.String())
	}
}

func TestClassifyNotificationsPreservesOrder(t *testing.T) {
	items := []model.Notification{notification("1", "subscribed"), notification("2", "mention")}
	var out strings.Builder
	got := classifyNotifications(context.Background(), &out, testConfig(), &fakeClient{}, items)
	if got[0].Thread.ID != "1" || got[1].Thread.ID != "2" || !strings.Contains(out.String(), "active notifications") {
		t.Fatalf("got=%#v out=%s", got, out.String())
	}
}

type fakeGetResult struct {
	thread model.Notification
	found  bool
	err    error
}

type fakeClient struct {
	mu                  sync.Mutex
	calls               []string
	operations          map[string][]string
	getResults          map[string][]fakeGetResult
	getIndexes          map[string]int
	enrichments         map[string]model.Enrichment
	unsubscribeFailures map[string]error
	doneFailures        map[string]error
}

func (f *fakeClient) ListNotifications(context.Context) ([]model.Notification, error) {
	return nil, errors.New("unexpected list call")
}
func (f *fakeClient) GetNotification(ctx context.Context, id string) (model.Notification, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.Notification{}, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordOperationLocked(id, "get")
	if f.getIndexes == nil {
		f.getIndexes = make(map[string]int)
	}
	index := f.getIndexes[id]
	f.getIndexes[id]++
	if configured, ok := f.getResults[id]; ok {
		if index >= len(configured) {
			return model.Notification{}, false, errors.New("unexpected get call")
		}
		result := configured[index]
		return result.thread, result.found, result.err
	}
	if index == 0 {
		return notification(id, "subscribed"), true, nil
	}
	return model.Notification{}, false, nil
}
func (f *fakeClient) Enrich(_ context.Context, thread model.Notification, _ model.EnrichmentRequirements) model.Enrichment {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordOperationLocked(thread.ID, "enrich")
	return f.enrichments[thread.ID]
}
func (f *fakeClient) UnsubscribeThread(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordOperationLocked(id, "unsubscribe")
	f.calls = append(f.calls, "unsubscribe:"+id)
	return f.unsubscribeFailures[id]
}
func (f *fakeClient) MarkThreadDone(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordOperationLocked(id, "done")
	f.calls = append(f.calls, "done:"+id)
	return f.doneFailures[id]
}
func (f *fakeClient) recordOperationLocked(id, operation string) {
	if f.operations == nil {
		f.operations = make(map[string][]string)
	}
	f.operations[id] = append(f.operations[id], operation)
}
func requireOperations(t *testing.T, client *fakeClient, id, want string) {
	t.Helper()
	client.mu.Lock()
	defer client.mu.Unlock()
	if got := strings.Join(client.operations[id], ","); got != want {
		t.Fatalf("operations for %s=%q want=%q (all mutations=%v)", id, got, want, client.calls)
	}
}

type blockingClient struct {
	fakeClient
	stateMu sync.Mutex
	gate    chan struct{}
	started chan string
	gets    map[string]int
	active  int
	maximum int
	starts  int
}

func newBlockingClient(targetCount int) *blockingClient {
	return &blockingClient{
		gate:    make(chan struct{}),
		started: make(chan string, targetCount),
		gets:    make(map[string]int),
	}
}

func (c *blockingClient) GetNotification(ctx context.Context, id string) (model.Notification, bool, error) {
	c.fakeClient.mu.Lock()
	c.fakeClient.recordOperationLocked(id, "get")
	c.fakeClient.mu.Unlock()
	c.stateMu.Lock()
	call := c.gets[id]
	c.gets[id]++
	if call == 0 {
		c.active++
		c.starts++
		if c.active > c.maximum {
			c.maximum = c.active
		}
	}
	c.stateMu.Unlock()
	if call > 0 {
		return model.Notification{}, false, nil
	}
	c.started <- id
	select {
	case <-c.gate:
	case <-ctx.Done():
		c.stateMu.Lock()
		c.active--
		c.stateMu.Unlock()
		return model.Notification{}, false, ctx.Err()
	}
	c.stateMu.Lock()
	c.active--
	c.stateMu.Unlock()
	return notification(id, "subscribed"), true, nil
}
func (c *blockingClient) maximumActive() int {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.maximum
}
func (c *blockingClient) startedCount() int {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.starts
}

func hushDecisions(count int) []model.Decision {
	decisions := make([]model.Decision, count)
	for index := range count {
		thread := notification(fmt.Sprint(index+1), "subscribed")
		decisions[index] = model.Decision{Thread: thread, Action: model.ActionUnsubscribeAndMarkDone, URL: thread.ID}
	}
	return decisions
}

func notification(id, reason string) model.Notification {
	return model.Notification{ID: id, Reason: reason, Repository: model.Repository{FullName: "github/repo"}, Subject: model.Subject{Type: "Issue", URL: "subject"}}
}
func testConfig() config.Config {
	on := true
	off := false
	cfg := config.Config{User: "octocat", GitHubOrganization: "github", Keep: config.Keep{ExternalOrganizationIssues: &on, PersonallyMentioned: &on, PersonallyAssigned: &off, IndividuallyReviewRequested: &off, AuthoredByUser: &off, TeamMentionedDiscussions: &off}}
	cfg.Hush.AllOtherNotifications = &on
	return cfg
}
