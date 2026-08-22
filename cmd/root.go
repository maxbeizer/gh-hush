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

// NewRootCommand constructs the gh-hush command.
func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	return newRootCommand(stdout, stderr, run)
}

func newRootCommand(stdout, stderr io.Writer, runOperation runFunc) *cobra.Command {
	var configPath string
	var dryRun bool
	var confirm bool

	rootCmd := &cobra.Command{
		Use:           "gh-hush",
		Short:         "Explainable, policy-driven GitHub notification triage",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			configProvided := cmd.Flags().Changed("config")
			if !configProvided {
				var err error
				configPath, err = config.DefaultPath()
				if err != nil {
					return fmt.Errorf("resolve default config path: %w", err)
				}
			}

			cfg, _, err := config.Load(configPath)
			if err != nil {
				if !configProvided && errors.Is(err, os.ErrNotExist) {
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
	rootCmd.Flags().BoolVar(&confirm, "confirm", false, "apply proposed unsubscriptions without prompting")
	rootCmd.MarkFlagsMutuallyExclusive("dry-run", "confirm")

	return rootCmd
}

func run(ctxCommand *cobra.Command, stdout, stderr io.Writer, cfg config.Config, dryRun, confirm bool) error {
	client, err := ghclient.NewCLIClient(ctxCommand.Context())
	if err != nil {
		return fmt.Errorf("initialize authenticated GitHub client: %w", err)
	}
	login, err := client.CurrentUser(ctxCommand.Context())
	if err != nil {
		return fmt.Errorf("authenticate with gh before running gh-hush: %w", err)
	}
	if !strings.EqualFold(login, cfg.User) {
		return fmt.Errorf("config user %q does not match authenticated gh user %q", cfg.User, login)
	}

	threads, err := client.ListNotifications(ctxCommand.Context())
	if err != nil {
		return fmt.Errorf("fetch GitHub notifications: %w", err)
	}

	decisions := classifyNotifications(ctxCommand.Context(), stderr, cfg, client, threads)

	if err := report.Write(stdout, decisions); err != nil {
		return fmt.Errorf("write preview report: %w", err)
	}

	unsubscribeCount := countUnsubscriptions(decisions)
	if dryRun || unsubscribeCount == 0 {
		return nil
	}
	if !confirm {
		if !isTerminal(ctxCommand.InOrStdin()) || !isTerminal(ctxCommand.OutOrStdout()) || !isTerminal(ctxCommand.ErrOrStderr()) {
			_, _ = fmt.Fprintln(stderr, "Dry run only: input, preview output, and prompt output must all be interactive terminals. Re-run with --confirm to apply these changes.")
			return nil
		}
		approved, err := promptForConfirmation(ctxCommand.InOrStdin(), stderr, unsubscribeCount)
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if !approved {
			_, _ = fmt.Fprintln(stderr, "No changes made.")
			return nil
		}
	}

	return applyUnsubscriptions(ctxCommand.Context(), stderr, client, decisions)
}

type notificationEnricher interface {
	Enrich(context.Context, model.Notification, model.EnrichmentRequirements) model.Enrichment
}

type notificationUnsubscriber interface {
	UnsubscribeNotification(context.Context, string) error
}

func countUnsubscriptions(decisions []model.Decision) int {
	count := 0
	for _, decision := range decisions {
		if decision.Action == model.ActionUnsubscribe {
			count++
		}
	}
	return count
}

func promptForConfirmation(input io.Reader, output io.Writer, count int) (bool, error) {
	if _, err := fmt.Fprintf(output, "Unsubscribe from %d notifications? [y/N] ", count); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func isTerminal(stream any) bool {
	file, ok := stream.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func applyUnsubscriptions(ctx context.Context, stderr io.Writer, client notificationUnsubscriber, decisions []model.Decision) error {
	total := countUnsubscriptions(decisions)
	_, _ = fmt.Fprintf(stderr, "applying %d notification updates (unsubscribe and mark read)...\n", total)

	completed := 0
	var failures []error
	for _, decision := range decisions {
		if decision.Action != model.ActionUnsubscribe {
			continue
		}
		if err := client.UnsubscribeNotification(ctx, decision.Thread.ID); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", decision.URL, err))
			continue
		}
		completed++
	}
	_, _ = fmt.Fprintf(stderr, "completed %d/%d notification updates\n", completed, total)
	if len(failures) > 0 {
		return fmt.Errorf("failed to complete %d notification updates: %w", len(failures), errors.Join(failures...))
	}
	return nil
}

func classifyNotifications(
	ctx context.Context,
	stderr io.Writer,
	cfg config.Config,
	client notificationEnricher,
	threads []model.Notification,
) []model.Decision {
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
				requirements := policy.EnrichmentRequirements(cfg, thread)
				enrichment := client.Enrich(ctx, thread, requirements)
				results <- result{
					index:    index,
					decision: policy.Classify(cfg, thread, enrichment),
				}
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

	_, _ = fmt.Fprintf(stderr, "classifying %d notifications (read-only)...\n", len(threads))
	decisions := make([]model.Decision, len(threads))
	completed := 0
	for classified := range results {
		decisions[classified.index] = classified.decision
		completed++
		if completed%25 == 0 || completed == len(threads) {
			_, _ = fmt.Fprintf(stderr, "classified %d/%d notifications\n", completed, len(threads))
		}
	}
	return decisions
}
