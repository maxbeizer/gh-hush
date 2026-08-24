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
	"time"

	"github.com/maxbeizer/gh-hush/internal/application"
	"github.com/maxbeizer/gh-hush/internal/config"
	"github.com/maxbeizer/gh-hush/internal/diagnostic"
	ghclient "github.com/maxbeizer/gh-hush/internal/github"
	"github.com/maxbeizer/gh-hush/internal/model"
	"github.com/maxbeizer/gh-hush/internal/policy"
	"github.com/maxbeizer/gh-hush/internal/report"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Version is replaced with the release tag by GoReleaser.
var Version = "dev"

type runFunc func(*cobra.Command, io.Writer, io.Writer, config.Config, bool, bool, bool) error

func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	return newRootCommand(stdout, stderr, run)
}

func newRootCommand(stdout, stderr io.Writer, runOperation runFunc) *cobra.Command {
	var configPath string
	var dryRun, confirm, debug bool
	rootCmd := &cobra.Command{
		Use: "gh-hush", Short: "Explainable, policy-driven GitHub notification triage",
		Version: Version, SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs,
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
			return runOperation(cmd, stdout, stderr, cfg, dryRun, confirm, debug)
		},
	}
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.Flags().StringVar(&configPath, "config", "", "override the default user-owned YAML policy path")
	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "classify notifications without prompting or mutating GitHub")
	rootCmd.Flags().BoolVar(&confirm, "confirm", false, "unsubscribe from and mark proposed notifications Done without prompting")
	rootCmd.Flags().BoolVar(&debug, "debug", false, "write request and workflow diagnostics to stderr")
	rootCmd.MarkFlagsMutuallyExclusive("dry-run", "confirm")
	return rootCmd
}

func run(command *cobra.Command, stdout, stderr io.Writer, cfg config.Config, dryRun, confirm, debug bool) error {
	ctx := command.Context()
	if debug {
		logger := diagnostic.New(stderr)
		ctx = diagnostic.WithLogger(ctx, logger)
		stderr = logger
		command.SetContext(ctx)
	}
	diagnostic.Log(diagnostic.WithPhase(ctx, "startup"), "workflow_start")
	runStart := now()
	var confirmationWait time.Duration
	printTotalRuntime := func() {
		_, _ = fmt.Fprintf(stderr, "total runtime: %s (excludes interactive confirmation wait)\n", formatDuration(now().Sub(runStart)-confirmationWait))
	}
	inboxStart := now()
	client, err := ghclient.NewCLIClient(ctx)
	if err != nil {
		diagnostic.Log(diagnostic.WithPhase(ctx, "authentication"), "operation_failed", diagnostic.String("operation", "token_lookup"))
		return fmt.Errorf("initialize authenticated GitHub client: %w", err)
	}
	authCtx := diagnostic.WithPhase(ctx, "authentication")
	login, err := client.CurrentUser(authCtx)
	if err != nil {
		diagnostic.Log(authCtx, "operation_failed", diagnostic.String("operation", "current_user"))
		return fmt.Errorf("authenticate with gh before running gh-hush: %w", err)
	}
	if !strings.EqualFold(login, cfg.User) {
		diagnostic.Log(authCtx, "operation_failed", diagnostic.String("operation", "user_match"))
		return fmt.Errorf("config user %q does not match authenticated gh user %q", cfg.User, login)
	}
	listCtx := diagnostic.WithPhase(ctx, "listing")
	threads, err := client.ListNotifications(listCtx)
	if err != nil {
		diagnostic.Log(listCtx, "operation_failed", diagnostic.String("operation", "list_notifications"))
		return fmt.Errorf("fetch unread GitHub notifications: %w", err)
	}
	diagnostic.Log(listCtx, "operation_complete", diagnostic.String("operation", "list_notifications"), diagnostic.Int("count", len(threads)))
	_, _ = fmt.Fprintf(stderr, "authenticated and listed %d unread %s in %s\n", len(threads), notificationWord(len(threads)), formatDuration(now().Sub(inboxStart)))
	evaluator := policy.NewEvaluator(cfg, client)
	decisions := classifyNotifications(ctx, stderr, evaluator, threads)
	reportCtx := diagnostic.WithPhase(ctx, "report")
	reportStart := now()
	if err := report.Write(stdout, decisions); err != nil {
		diagnostic.Log(reportCtx, "operation_failed", diagnostic.String("operation", "write_preview"))
		return fmt.Errorf("write preview report: %w", err)
	}
	diagnostic.Log(reportCtx, "operation_complete", diagnostic.String("operation", "write_preview"), diagnostic.Int("count", len(decisions)))
	_, _ = fmt.Fprintf(stderr, "generated preview report in %s\n", formatDuration(now().Sub(reportStart)))
	targetCount := countHushActions(decisions)
	if dryRun || targetCount == 0 {
		printTotalRuntime()
		return nil
	}
	if !confirm {
		if !isTerminal(command.InOrStdin()) || !isTerminal(command.OutOrStdout()) || !isTerminal(command.ErrOrStderr()) {
			_, _ = fmt.Fprintln(stderr, "Preview only: input, preview output, and prompt output must all be interactive terminals. Re-run with --confirm to apply these changes.")
			printTotalRuntime()
			return nil
		}
		waitStart := now()
		approved, err := promptForConfirmation(command.InOrStdin(), stderr, targetCount)
		confirmationWait = now().Sub(waitStart)
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if !approved {
			_, _ = fmt.Fprintln(stderr, "No changes made.")
			printTotalRuntime()
			return nil
		}
	}
	err = application.Apply(ctx, stderr, cfg, client, decisions, isTerminal(stderr))
	printTotalRuntime()
	return err
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

func classifyNotifications(ctx context.Context, stderr io.Writer, evaluator *policy.Evaluator, threads []model.Notification) []model.Decision {
	ctx = diagnostic.WithPhase(ctx, "classification")
	classifyStart := now()
	progress := newClassificationProgress(stderr, isTerminal(stderr) && !diagnostic.Enabled(ctx))
	progress.start(len(threads))
	if len(threads) == 0 {
		_, _ = fmt.Fprintf(stderr, "classified 0/0 notifications in %s\n", formatDuration(now().Sub(classifyStart)))
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
				workCtx := diagnostic.WithThread(ctx, thread.ID)
				diagnostic.Log(workCtx, "worker_start")
				decision := evaluator.EvaluateForPreview(workCtx, thread)
				if workCtx.Err() != nil {
					diagnostic.Log(workCtx, "worker_cancelled")
				} else {
					diagnostic.Log(workCtx, "worker_complete")
				}
				results <- result{index, decision}
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
	_, _ = fmt.Fprintf(stderr, "classified %d/%d %s in %s\n", len(threads), len(threads), notificationWord(len(threads)), formatDuration(now().Sub(classifyStart)))
	return decisions
}
