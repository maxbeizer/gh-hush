package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/maxbeizer/gh-hush/internal/config"
	ghclient "github.com/maxbeizer/gh-hush/internal/github"
	"github.com/maxbeizer/gh-hush/internal/model"
	"github.com/maxbeizer/gh-hush/internal/policy"
	"github.com/maxbeizer/gh-hush/internal/report"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type runFunc func(*cobra.Command, io.Writer, io.Writer, config.Config, bool, bool) error

func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	return newRootCommand(stdout, stderr, run)
}

func newRootCommand(stdout, stderr io.Writer, runOperation runFunc) *cobra.Command {
	var configPath string
	var dryRun, confirm bool
	rootCmd := &cobra.Command{
		Use: "gh-hush", Short: "Explainable, policy-driven GitHub notification triage",
		SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provided := cmd.Flags().Changed("config")
			if !provided {
				var err error
				configPath, err = config.DefaultPath()
				if err != nil {
					return fmt.Errorf("resolve default config path: %w", err)
				}
			}
			cfg, _, err := config.Load(configPath)
			if err != nil {
				if !provided && errors.Is(err, os.ErrNotExist) {
					return cmd.Help()
				}
				return err
			}
			return runOperation(cmd, stdout, stderr, cfg, dryRun, confirm)
		},
	}
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.Flags().StringVar(&configPath, "config", "", "override the default user-owned YAML policy path")
	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "classify notifications without prompting or mutating GitHub")
	rootCmd.Flags().BoolVar(&confirm, "confirm", false, "unsubscribe from and mark proposed notifications Done without prompting")
	rootCmd.MarkFlagsMutuallyExclusive("dry-run", "confirm")
	return rootCmd
}

func run(command *cobra.Command, stdout, stderr io.Writer, cfg config.Config, dryRun, confirm bool) error {
	client, err := ghclient.NewCLIClient(command.Context())
	if err != nil {
		return fmt.Errorf("initialize authenticated GitHub client: %w", err)
	}
	login, err := client.CurrentUser(command.Context())
	if err != nil {
		return fmt.Errorf("authenticate with gh before running gh-hush: %w", err)
	}
	if !strings.EqualFold(login, cfg.User) {
		return fmt.Errorf("config user %q does not match authenticated gh user %q", cfg.User, login)
	}
	threads, err := client.ListNotifications(command.Context())
	if err != nil {
		return fmt.Errorf("fetch unread GitHub notifications: %w", err)
	}
	decisions := classifyNotifications(command.Context(), stderr, cfg, client, threads)
	if err := report.Write(stdout, decisions); err != nil {
		return fmt.Errorf("write preview report: %w", err)
	}
	targetCount := countHushActions(decisions)
	if dryRun || targetCount == 0 {
		return nil
	}
	if !confirm {
		if !isTerminal(command.InOrStdin()) || !isTerminal(command.OutOrStdout()) || !isTerminal(command.ErrOrStderr()) {
			_, _ = fmt.Fprintln(stderr, "Preview only: input, preview output, and prompt output must all be interactive terminals. Re-run with --confirm to apply these changes.")
			return nil
		}
		approved, err := promptForConfirmation(command.InOrStdin(), stderr, targetCount)
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if !approved {
			_, _ = fmt.Fprintln(stderr, "No changes made.")
			return nil
		}
	}
	return applyHushActions(command.Context(), stderr, cfg, client, decisions)
}

type notificationEnricher interface {
	Enrich(context.Context, model.Notification, model.EnrichmentRequirements) model.Enrichment
}
type notificationClient interface {
	notificationEnricher
	ListNotifications(context.Context) ([]model.Notification, error)
	GetNotification(context.Context, string) (model.Notification, bool, error)
	UnsubscribeThread(context.Context, string) error
	MarkThreadDone(context.Context, string) error
}

func countHushActions(decisions []model.Decision) int {
	count := 0
	for _, decision := range decisions {
		if decision.Action == model.ActionUnsubscribeAndMarkDone {
			count++
		}
	}
	return count
}

func promptForConfirmation(input io.Reader, output io.Writer, count int) (bool, error) {
	if _, err := fmt.Fprintf(output, "Unsubscribe from and mark %d notifications Done? [y/N] ", count); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer = strings.TrimSpace(answer)
	// Some interactive terminal hosts leave bracketed-paste markers around pasted
	// input. They are invisible when echoed, so an apparent "y" would otherwise
	// be interpreted as a refusal.
	answer = strings.TrimPrefix(answer, "\x1b[200~")
	answer = strings.TrimSuffix(answer, "\x1b[201~")
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func isTerminal(stream any) bool {
	file, ok := stream.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

type applicationSummary struct {
	Targets, Missing, NoLongerUnread, Protected, RevalidationFailed int
	UnsubscribeSucceeded, UnsubscribeFailed                         int
	DoneSucceeded, DoneFailed                                       int
}

// Four in-flight threads allow useful overlap without producing the request
// burst that one goroutine per notification would create.
const applyMaxWorkers = 4

type applicationResult struct {
	index    int
	summary  applicationSummary
	messages []string
	failure  error
}

func applyHushActions(ctx context.Context, stderr io.Writer, cfg config.Config, client notificationClient, decisions []model.Decision) error {
	return applyHushActionsWithProgressMode(ctx, stderr, cfg, client, decisions, isTerminal(stderr))
}

func applyHushActionsWithProgressMode(ctx context.Context, stderr io.Writer, cfg config.Config, client notificationClient, decisions []model.Decision, interactive bool) error {
	targets := make([]model.Decision, 0, countHushActions(decisions))
	for _, decision := range decisions {
		if decision.Action == model.ActionUnsubscribeAndMarkDone {
			targets = append(targets, decision)
		}
	}

	summary := applicationSummary{Targets: len(targets)}
	workerCount := min(applyMaxWorkers, len(targets))
	progress := newApplicationProgress(stderr, interactive)
	progress.start(len(targets))

	type job struct {
		index   int
		preview model.Decision
	}
	jobs := make(chan job)
	results := make(chan applicationResult, workerCount)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for work := range jobs {
				if err := ctx.Err(); err != nil {
					results <- applicationResult{index: work.index, failure: err}
					continue
				}
				result := applyHushAction(ctx, cfg, client, work.preview)
				result.index = work.index
				results <- result
			}
		}()
	}

	ordered := make([]applicationResult, len(targets))
	next, running, completed := 0, 0, 0
	recordResult := func(result applicationResult) {
		ordered[result.index] = result
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
			recordResult(<-results)
			continue
		}
		select {
		case jobs <- job{index: next, preview: targets[next]}:
			next++
			running++
		case result := <-results:
			recordResult(result)
		case <-ctx.Done():
			cancelled = true
		}
	}
	close(jobs)
	workers.Wait()
	progress.finish(completed, cancelled)

	// The live status line is finalized before ordered, durable diagnostics are
	// emitted, so a later progress update can never overwrite a skip message.
	var failures []error
	for index := 0; index < next; index++ {
		result := ordered[index]
		addApplicationSummary(&summary, result.summary)
		for _, message := range result.messages {
			_, _ = fmt.Fprintln(stderr, message)
		}
		if result.failure != nil {
			failures = append(failures, result.failure)
		}
	}
	if err := ctx.Err(); err != nil && next < len(targets) {
		failures = append(failures, err)
	}
	_, _ = fmt.Fprintf(stderr, "application summary: targets=%d; missing=%d; no_longer_unread=%d; protected=%d; revalidation_failed=%d; unsubscribe_succeeded=%d; unsubscribe_failed=%d; done_succeeded=%d; done_failed=%d\n",
		summary.Targets, summary.Missing, summary.NoLongerUnread, summary.Protected, summary.RevalidationFailed,
		summary.UnsubscribeSucceeded, summary.UnsubscribeFailed, summary.DoneSucceeded, summary.DoneFailed)
	if len(failures) > 0 {
		return fmt.Errorf("one or more notification updates did not complete safely: %w", errors.Join(failures...))
	}
	return nil
}

func applyHushAction(ctx context.Context, cfg config.Config, client notificationClient, preview model.Decision) applicationResult {
	var result applicationResult
	current, found, err := client.GetNotification(ctx, preview.Thread.ID)
	if err != nil {
		result.summary.RevalidationFailed++
		result.failure = fmt.Errorf("%s: revalidation thread fetch failed: %w", preview.URL, err)
		return result
	}
	if !found {
		result.summary.Missing++
		result.messages = append(result.messages, fmt.Sprintf("skip %s: notification thread record is no longer available", preview.URL))
		return result
	}
	// The per-thread endpoint returns historical Done records too. unread=false
	// is ambiguous: it can mean read-but-still-inbox or historical/Done. Skip it
	// rather than claiming that this GET proves current inbox membership.
	if !current.Unread {
		result.summary.NoLongerUnread++
		result.messages = append(result.messages, fmt.Sprintf("skip %s: target is no longer unread; GitHub's REST API cannot distinguish read inbox entries from Done history", preview.URL))
		return result
	}
	enrichment := client.Enrich(ctx, current, policy.EnrichmentRequirements(cfg, current))
	fresh := policy.Classify(cfg, current, enrichment)
	if fresh.EnrichmentError != "" {
		result.summary.RevalidationFailed++
		result.failure = fmt.Errorf("%s: revalidation evidence fetch failed: %s", fresh.URL, fresh.EnrichmentError)
		result.messages = append(result.messages, fmt.Sprintf("skip %s: required revalidation evidence was unavailable; no mutation attempted", fresh.URL))
		return result
	}
	if fresh.Action == model.ActionKeep {
		result.summary.Protected++
		result.messages = append(result.messages, fmt.Sprintf("skip %s: target became protected: %s", fresh.URL, ruleDescriptions(fresh.Rules)))
		return result
	}
	if err := ctx.Err(); err != nil {
		result.failure = err
		return result
	}
	if err := client.UnsubscribeThread(ctx, current.ID); err != nil {
		result.summary.UnsubscribeFailed++
		result.failure = fmt.Errorf("%s: unsubscribe failed: %w", fresh.URL, err)
		return result // Never mark Done if unsubscribe failed.
	}
	result.summary.UnsubscribeSucceeded++
	if err := client.MarkThreadDone(ctx, current.ID); err != nil {
		result.summary.DoneFailed++
		result.failure = fmt.Errorf("%s: unsubscribe succeeded but Done failed: %w", fresh.URL, err)
		return result
	}
	result.summary.DoneSucceeded++
	return result
}

func addApplicationSummary(total *applicationSummary, delta applicationSummary) {
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

func classifyNotifications(ctx context.Context, stderr io.Writer, cfg config.Config, client notificationEnricher, threads []model.Notification) []model.Decision {
	progress := newClassificationProgress(stderr, isTerminal(stderr))
	progress.start(len(threads))
	if len(threads) == 0 {
		return nil
	}
	const maxWorkers = 8
	workerCount := min(maxWorkers, len(threads))
	type result struct {
		index    int
		decision model.Decision
	}
	jobs := make(chan int)
	results := make(chan result)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				thread := threads[index]
				enrichment := client.Enrich(ctx, thread, policy.EnrichmentRequirements(cfg, thread))
				results <- result{index, policy.Classify(cfg, thread, enrichment)}
			}
		}()
	}
	go func() {
		for index := range threads {
			jobs <- index
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	decisions := make([]model.Decision, len(threads))
	completed := 0
	for classified := range results {
		decisions[classified.index] = classified.decision
		completed++
		progress.update(completed)
	}
	progress.finish()
	return decisions
}
