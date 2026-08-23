// Package application safely applies proposed notification updates.
package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/diagnostic"
	"github.com/maxbeizer/gh-hush/internal/model"
	"github.com/maxbeizer/gh-hush/internal/policy"
)

// Client is the GitHub notification capability required by the application
// workflow. The production GitHub client implements it directly.
type Client interface {
	GetNotification(context.Context, string) (model.Notification, bool, error)
	Enrich(context.Context, model.Notification, model.EnrichmentRequirements) model.Enrichment
	UnsubscribeThread(context.Context, string) error
	MarkThreadDone(context.Context, string) error
}

// Four in-flight threads allow useful overlap without producing the request
// burst that one goroutine per notification would create.
const maxWorkers = 4

var now = time.Now

type summary struct {
	Targets, Missing, NoLongerUnread, Protected, RevalidationFailed int
	UnsubscribeSucceeded, UnsubscribeFailed                         int
	DoneSucceeded, DoneFailed                                       int
}

type result struct {
	index    int
	summary  summary
	messages []string
	failure  error
}

// Apply revalidates and applies eligible preview decisions. It owns bounded
// scheduling, ordered reporting, progress, and the unsubscribe-before-Done
// safety invariant. interactive controls whether progress is rendered in place.
func Apply(ctx context.Context, output io.Writer, cfg config.Config, client Client, decisions []model.Decision, interactive bool) error {
	ctx = diagnostic.WithPhase(ctx, "apply")
	applyStart := now()
	if diagnostic.Enabled(ctx) {
		interactive = false
	}
	targets := make([]model.Decision, 0, countTargets(decisions))
	for _, decision := range decisions {
		if decision.Action == model.ActionUnsubscribeAndMarkDone {
			targets = append(targets, decision)
		}
	}

	total := summary{Targets: len(targets)}
	workerCount := min(maxWorkers, len(targets))
	progress := newProgress(output, interactive)
	progress.start(len(targets))

	type job struct {
		index   int
		preview model.Decision
	}
	jobs := make(chan job)
	results := make(chan result, workerCount)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for work := range jobs {
				workCtx := diagnostic.WithThread(ctx, work.preview.Thread.ID)
				diagnostic.Log(workCtx, "worker_start")
				if err := ctx.Err(); err != nil {
					diagnostic.Log(workCtx, "worker_cancelled")
					results <- result{index: work.index, failure: err}
					continue
				}
				outcome := applyOne(workCtx, cfg, client, work.preview)
				outcome.index = work.index
				if errors.Is(outcome.failure, context.Canceled) || errors.Is(outcome.failure, context.DeadlineExceeded) {
					diagnostic.Log(workCtx, "worker_cancelled")
				} else {
					diagnostic.Log(workCtx, "worker_complete", diagnostic.Bool("failed", outcome.failure != nil))
				}
				results <- outcome
			}
		}()
	}

	ordered := make([]result, len(targets))
	next, running, completed := 0, 0, 0
	record := func(outcome result) {
		ordered[outcome.index] = outcome
		running--
		completed++
		progress.update(completed)
	}
	cancelled := ctx.Err() != nil
	for running > 0 || (!cancelled && next < len(targets)) {
		if ctx.Err() != nil {
			cancelled = true
		}
		if cancelled || next == len(targets) {
			record(<-results)
			continue
		}
		select {
		case jobs <- job{index: next, preview: targets[next]}:
			next++
			running++
		case outcome := <-results:
			record(outcome)
		case <-ctx.Done():
			cancelled = true
		}
	}
	close(jobs)
	workers.Wait()
	progress.finish(completed, cancelled)

	// Finalize the live line before durable, preview-ordered diagnostics.
	var failures []error
	for index := 0; index < next; index++ {
		outcome := ordered[index]
		addSummary(&total, outcome.summary)
		for _, message := range outcome.messages {
			_, _ = fmt.Fprintln(output, message)
		}
		if outcome.failure != nil {
			failures = append(failures, outcome.failure)
		}
	}
	if err := ctx.Err(); err != nil && next < len(targets) {
		failures = append(failures, err)
	}
	_, _ = fmt.Fprintf(output, "application summary: targets=%d; missing=%d; no_longer_unread=%d; protected=%d; revalidation_failed=%d; unsubscribe_succeeded=%d; unsubscribe_failed=%d; done_succeeded=%d; done_failed=%d; elapsed=%s\n",
		total.Targets, total.Missing, total.NoLongerUnread, total.Protected, total.RevalidationFailed,
		total.UnsubscribeSucceeded, total.UnsubscribeFailed, total.DoneSucceeded, total.DoneFailed, formatDuration(now().Sub(applyStart)))
	if len(failures) > 0 {
		return fmt.Errorf("one or more notification updates did not complete safely: %w", errors.Join(failures...))
	}
	return nil
}

func applyOne(ctx context.Context, cfg config.Config, client Client, preview model.Decision) result {
	var outcome result
	current, found, err := client.GetNotification(ctx, preview.Thread.ID)
	if err != nil {
		outcome.summary.RevalidationFailed++
		outcome.failure = fmt.Errorf("%s: revalidation thread fetch failed: %w", preview.URL, err)
		return outcome
	}
	if !found {
		outcome.summary.Missing++
		outcome.messages = append(outcome.messages, fmt.Sprintf("skip %s: notification thread record is no longer available", preview.URL))
		return outcome
	}
	// The per-thread endpoint also returns historical Done records. unread=false
	// cannot distinguish those records from read entries that remain in inbox.
	if !current.Unread {
		outcome.summary.NoLongerUnread++
		outcome.messages = append(outcome.messages, fmt.Sprintf("skip %s: target is no longer unread; GitHub's REST API cannot distinguish read inbox entries from Done history", preview.URL))
		return outcome
	}
	enrichment := client.Enrich(ctx, current, policy.EnrichmentRequirements(cfg, current))
	fresh := policy.Classify(cfg, current, enrichment)
	if fresh.EnrichmentError != "" {
		outcome.summary.RevalidationFailed++
		outcome.failure = fmt.Errorf("%s: revalidation evidence fetch failed: %s", fresh.URL, fresh.EnrichmentError)
		outcome.messages = append(outcome.messages, fmt.Sprintf("skip %s: required revalidation evidence was unavailable; no mutation attempted", fresh.URL))
		return outcome
	}
	if fresh.Action == model.ActionKeep {
		outcome.summary.Protected++
		outcome.messages = append(outcome.messages, fmt.Sprintf("skip %s: target became protected: %s", fresh.URL, ruleDescriptions(fresh.Rules)))
		return outcome
	}
	if err := ctx.Err(); err != nil {
		outcome.failure = err
		return outcome
	}
	if err := client.UnsubscribeThread(ctx, current.ID); err != nil {
		outcome.summary.UnsubscribeFailed++
		outcome.failure = fmt.Errorf("%s: unsubscribe failed: %w", fresh.URL, err)
		return outcome // Never mark Done if unsubscribe failed.
	}
	outcome.summary.UnsubscribeSucceeded++
	if err := client.MarkThreadDone(ctx, current.ID); err != nil {
		outcome.summary.DoneFailed++
		outcome.failure = fmt.Errorf("%s: unsubscribe succeeded but Done failed: %w", fresh.URL, err)
		return outcome
	}
	outcome.summary.DoneSucceeded++
	return outcome
}

func countTargets(decisions []model.Decision) int {
	count := 0
	for _, decision := range decisions {
		if decision.Action == model.ActionUnsubscribeAndMarkDone {
			count++
		}
	}
	return count
}

func addSummary(total *summary, delta summary) {
	total.Missing += delta.Missing
	total.NoLongerUnread += delta.NoLongerUnread
	total.Protected += delta.Protected
	total.RevalidationFailed += delta.RevalidationFailed
	total.UnsubscribeSucceeded += delta.UnsubscribeSucceeded
	total.UnsubscribeFailed += delta.UnsubscribeFailed
	total.DoneSucceeded += delta.DoneSucceeded
	total.DoneFailed += delta.DoneFailed
}

func ruleDescriptions(rules []model.Rule) string {
	descriptions := make([]string, len(rules))
	for i, rule := range rules {
		descriptions[i] = fmt.Sprintf("%s (%s)", rule.ID, rule.Evidence)
	}
	return strings.Join(descriptions, "; ")
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		minutes := int(d / time.Minute)
		seconds := (d % time.Minute).Seconds()
		return fmt.Sprintf("%dm%04.1fs", minutes, seconds)
	}
}
