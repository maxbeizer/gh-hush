package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/diagnostic"
	"github.com/maxbeizer/gh-hush/internal/model"
)

func apply(ctx context.Context, output io.Writer, cfg config.Config, client Client, decisions []model.Decision) error {
	return Apply(ctx, output, cfg, client, decisions, false)
}

func TestApplyHushActionsSuccessAndEndpointOrdering(t *testing.T) {
	item := notification("1", "subscribed")
	client := &fakeClient{}
	var progress strings.Builder
	err := apply(context.Background(), &progress, testConfig(), client, []model.Decision{{Thread: item, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(client.calls, ",") != "unsubscribe:1,done:1" {
		t.Fatalf("calls=%v", client.calls)
	}
	for _, text := range []string{"unsubscribe_succeeded=1", "done_succeeded=1"} {
		if !strings.Contains(progress.String(), text) {
			t.Errorf("progress missing %q: %s", text, progress.String())
		}
	}
}

func TestApplyDoesNotMarkDoneAfterUnsubscribeFailureAndContinues(t *testing.T) {
	first, second := notification("1", "subscribed"), notification("2", "subscribed")
	client := &fakeClient{unsubscribeFailures: map[string]error{"1": errors.New("request exhausted 3 attempts")}}
	var progress strings.Builder
	err := apply(context.Background(), &progress, testConfig(), client, []model.Decision{{Thread: first, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}, {Thread: second, Action: model.ActionUnsubscribeAndMarkDone, URL: "two"}})
	if err == nil || !strings.Contains(err.Error(), "exhausted 3 attempts") {
		t.Fatalf("err=%v", err)
	}
	requireOperations(t, client, "1", "get,enrich,unsubscribe")
	requireOperations(t, client, "2", "get,enrich,unsubscribe,done")
	if !strings.Contains(progress.String(), "unsubscribe_failed=1") || !strings.Contains(progress.String(), "done_succeeded=1") {
		t.Fatalf("summary=%s", progress.String())
	}
}

func TestApplyContinuesAfterDoneRetryExhaustion(t *testing.T) {
	first, second := notification("1", "subscribed"), notification("2", "subscribed")
	client := &fakeClient{doneFailures: map[string]error{"1": errors.New("request exhausted 3 attempts")}}
	var out strings.Builder
	err := apply(context.Background(), &out, testConfig(), client, []model.Decision{
		{Thread: first, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"},
		{Thread: second, Action: model.ActionUnsubscribeAndMarkDone, URL: "two"},
	})
	if err == nil || !strings.Contains(err.Error(), "Done failed") || !strings.Contains(err.Error(), "exhausted 3 attempts") {
		t.Fatalf("err=%v", err)
	}
	requireOperations(t, client, "1", "get,enrich,unsubscribe,done")
	requireOperations(t, client, "2", "get,enrich,unsubscribe,done")
	if !strings.Contains(out.String(), "done_failed=1") || !strings.Contains(out.String(), "done_succeeded=1") {
		t.Fatalf("summary=%s", out.String())
	}
}

func TestApplyReportsDoneFailureAndDoesNotVerifyThreadDisappearance(t *testing.T) {
	t.Run("Done failure", func(t *testing.T) {
		item := notification("1", "subscribed")
		client := &fakeClient{doneFailures: map[string]error{"1": errors.New("done failed")}}
		var out strings.Builder
		err := apply(context.Background(), &out, testConfig(), client, []model.Decision{{Thread: item, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}})
		if err == nil || !strings.Contains(err.Error(), "unsubscribe succeeded but Done failed") || !strings.Contains(out.String(), "done_failed=1") {
			t.Fatalf("err=%v out=%s", err, out.String())
		}
	})
	t.Run("successful Done is final", func(t *testing.T) {
		item := notification("1", "subscribed")
		// A second GET would return the historical record, as GitHub does after
		// Done. The operation must end after GitHub accepts MarkThreadDone.
		client := &fakeClient{getResults: map[string][]fakeGetResult{"1": {{thread: item, found: true}, {thread: item, found: true}}}}
		var out strings.Builder
		if err := apply(context.Background(), &out, testConfig(), client, []model.Decision{{Thread: item, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}}); err != nil {
			t.Fatal(err)
		}
		requireOperations(t, client, "1", "get,enrich,unsubscribe,done")
		if !strings.Contains(out.String(), "done_succeeded=1") || strings.Contains(out.String(), "verification") {
			t.Fatalf("out=%s", out.String())
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
	err := apply(context.Background(), &out, cfg, client, []model.Decision{
		{Thread: first, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"},
		{Thread: second, Action: model.ActionUnsubscribeAndMarkDone, URL: "two"},
	})
	if err == nil || !strings.Contains(err.Error(), "revalidation evidence fetch failed") || !strings.Contains(err.Error(), "exhausted 3 attempts") {
		t.Fatalf("err=%v", err)
	}
	requireOperations(t, client, "1", "get,enrich")
	requireOperations(t, client, "2", "get,enrich,unsubscribe,done")
	if !strings.Contains(out.String(), "revalidation_failed=1") || !strings.Contains(out.String(), "done_succeeded=1") {
		t.Fatalf("summary=%s", out.String())
	}
}

func TestApplyRevalidationSkipsMissingNoLongerUnreadOrNewlyProtected(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		item := notification("1", "subscribed")
		client := &fakeClient{getResults: map[string][]fakeGetResult{"1": {{found: false}}}}
		var out strings.Builder
		if err := apply(context.Background(), &out, testConfig(), client, []model.Decision{{Thread: item, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}}); err != nil {
			t.Fatal(err)
		}
		if len(client.calls) != 0 || !strings.Contains(out.String(), "missing=1") {
			t.Fatalf("calls=%v out=%s", client.calls, out.String())
		}
	})
	t.Run("no longer unread", func(t *testing.T) {
		item := notification("1", "subscribed")
		readRecord := item
		readRecord.Unread = false
		client := &fakeClient{getResults: map[string][]fakeGetResult{"1": {{thread: readRecord, found: true}}}}
		var out strings.Builder
		if err := apply(context.Background(), &out, testConfig(), client, []model.Decision{{Thread: item, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}}); err != nil {
			t.Fatal(err)
		}
		requireOperations(t, client, "1", "get")
		if !strings.Contains(out.String(), "no_longer_unread=1") || !strings.Contains(out.String(), "cannot distinguish read inbox entries from Done history") {
			t.Fatalf("out=%s", out.String())
		}
	})
	t.Run("protected", func(t *testing.T) {
		preview := notification("1", "subscribed")
		fresh := notification("1", "mention")
		client := &fakeClient{getResults: map[string][]fakeGetResult{"1": {{thread: fresh, found: true}}}}
		var out strings.Builder
		if err := apply(context.Background(), &out, testConfig(), client, []model.Decision{{Thread: preview, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"}}); err != nil {
			t.Fatal(err)
		}
		if len(client.calls) != 0 || !strings.Contains(out.String(), "protected=1") || !strings.Contains(out.String(), "keep.personal_mention") {
			t.Fatalf("calls=%v out=%s", client.calls, out.String())
		}
	})
}

func TestApplyWithNoTargetsReportsEmptyProgressAndSummary(t *testing.T) {
	setClock(t, time.Unix(0, 0), time.Unix(0, 0).Add(42700*time.Millisecond))
	var out strings.Builder
	client := &fakeClient{}

	if err := apply(context.Background(), &out, testConfig(), client, []model.Decision{
		{Thread: notification("1", "mention"), Action: model.ActionKeep, URL: "one"},
	}); err != nil {
		t.Fatal(err)
	}

	want := "No notification updates to apply.\n" +
		"application summary: targets=0; missing=0; no_longer_unread=0; protected=0; revalidation_failed=0; unsubscribe_succeeded=0; unsubscribe_failed=0; done_succeeded=0; done_failed=0; elapsed=42.7s\n"
	if got := out.String(); got != want {
		t.Fatalf("output mismatch\n got: %q\nwant: %q", got, want)
	}
	if len(client.calls) != 0 {
		t.Fatalf("unexpected mutations: %v", client.calls)
	}
}

func TestApplyInteractiveProgressFinishesBeforeDurableDiagnostics(t *testing.T) {
	item := notification("1", "subscribed")
	client := &fakeClient{getResults: map[string][]fakeGetResult{"1": {{found: false}}}}
	var out strings.Builder

	if err := Apply(context.Background(), &out, testConfig(), client, []model.Decision{
		{Thread: item, Action: model.ActionUnsubscribeAndMarkDone, URL: "one"},
	}, true); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	final := strings.Index(got, "✓ Finished applying 1/1 notification update (100%)")
	diagnostic := strings.Index(got, "skip one: notification thread record is no longer available")
	if final < 0 || diagnostic < 0 || final >= diagnostic {
		t.Fatalf("progress was not finalized before diagnostic: %q", got)
	}
	if newline := strings.Index(got[final:], "\n"); newline < 0 || final+newline >= diagnostic {
		t.Fatalf("diagnostic does not begin after the live line's newline: %q", got)
	}
	if carriageReturn := strings.LastIndex(got, "\r"); carriageReturn >= diagnostic {
		t.Fatalf("progress update could overwrite durable diagnostic: %q", got)
	}
	if !strings.Contains(got[diagnostic:], "application summary: targets=1; missing=1") {
		t.Fatalf("aggregate summary missing after diagnostic: %q", got)
	}
}

func TestApplyUsesBoundedConcurrencyAndPreservesPerThreadOrdering(t *testing.T) {
	const targetCount = 8
	client := newBlockingClient(targetCount)
	done := make(chan error, 1)
	go func() {
		done <- apply(context.Background(), io.Discard, testConfig(), client, hushDecisions(targetCount))
	}()

	for range maxWorkers {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("workers did not make progress concurrently")
		}
	}
	select {
	case id := <-client.started:
		t.Fatalf("started more than %d concurrent threads before a worker was released; extra=%s", maxWorkers, id)
	case <-time.After(50 * time.Millisecond):
	}
	close(client.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if client.maximumActive() != maxWorkers {
		t.Fatalf("maximum active=%d want=%d", client.maximumActive(), maxWorkers)
	}
	for index := 1; index <= targetCount; index++ {
		requireOperations(t, &client.fakeClient, fmt.Sprint(index), "get,enrich,unsubscribe,done")
	}
}

func TestApplyCancellationStopsFurtherScheduling(t *testing.T) {
	client := newBlockingClient(20)
	ctx, cancel := context.WithCancel(context.Background())
	var out strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- apply(ctx, &out, testConfig(), client, hushDecisions(20))
	}()
	for range maxWorkers {
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
	if got := client.startedCount(); got != maxWorkers {
		t.Fatalf("started=%d want=%d; work continued scheduling after cancellation", got, maxWorkers)
	}
	if got := out.String(); !strings.Contains(got, "Stopped applying after 4/20 notification updates (20%)") {
		t.Fatalf("cancellation progress did not report partial completion: %q", got)
	}
}

func TestApplyCancellationAfterAllTargetsScheduledStillReportsStopped(t *testing.T) {
	client := newBlockingClient(2)
	ctx, cancel := context.WithCancel(context.Background())
	var out strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- apply(ctx, &out, testConfig(), client, hushDecisions(2))
	}()
	for range 2 {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	if err := <-done; err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context cancellation", err)
	}
	got := out.String()
	if !strings.Contains(got, "Stopped applying after 2/2 notification updates (100%)") || strings.Contains(got, "✓ Finished applying") {
		t.Fatalf("cancellation was presented as successful completion: %q", got)
	}
}

func TestApplyWithPreCancelledContextDoesNotWaitForResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- apply(ctx, &out, testConfig(), &fakeClient{}, hushDecisions(2))
	}()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("apply waited for a worker result even though no work was scheduled")
	}
	if got := out.String(); !strings.Contains(got, "Stopped applying after 0/2 notification updates (0%)") {
		t.Fatalf("pre-cancelled progress mismatch: %q", got)
	}
}

func TestApplyAggregatesFailuresInTargetOrder(t *testing.T) {
	client := &fakeClient{unsubscribeFailures: map[string]error{
		"1": errors.New("failure-one"),
		"2": errors.New("failure-two"),
	}}
	var out strings.Builder
	err := apply(context.Background(), &out, testConfig(), client, hushDecisions(2))
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

func TestDebugEvents(t *testing.T) {
	var output strings.Builder
	logger := diagnostic.New(&output)
	ctx := diagnostic.WithLogger(context.Background(), logger)
	item := notification("thread-1", "subscribed")
	client := &fakeClient{}

	if err := Apply(ctx, logger, testConfig(), client, []model.Decision{{Thread: item, Action: model.ActionUnsubscribeAndMarkDone}}, false); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{
		"event=worker_start phase=apply thread_id=thread-1",
		"event=worker_complete phase=apply thread_id=thread-1 failed=false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("debug output missing %q:\n%s", want, got)
		}
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

func TestApplySummaryReportsAggregateElapsed(t *testing.T) {
	setClock(t, time.Unix(0, 0), time.Unix(0, 0).Add(3*time.Second))
	client := &fakeClient{}
	var out strings.Builder
	if err := apply(context.Background(), &out, testConfig(), client, hushDecisions(1)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "elapsed=3.0s") || !strings.Contains(out.String(), "done_succeeded=1") {
		t.Fatalf("summary missing elapsed or counters: %q", out.String())
	}
}
