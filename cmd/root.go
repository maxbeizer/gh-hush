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

type runFunc func(*cobra.Command, io.Writer, io.Writer, config.Config, bool, bool, bool, bool) error

func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	return newRootCommand(stdout, stderr, run)
}

func newRootCommand(stdout, stderr io.Writer, runOperation runFunc) *cobra.Command {
	var configPath string
	var dryRun, confirm, quiet, debug bool
	resolveConfigPath := func(cmd *cobra.Command) (string, bool, error) {
		provided := cmd.Flags().Changed("config")
		if provided {
			return configPath, true, nil
		}
		path, err := config.DefaultPath()
		if err != nil {
			return "", false, fmt.Errorf("resolve default config path: %w", err)
		}
		return path, false, nil
	}
	resolveConfig := func(cmd *cobra.Command) (config.Config, string, bool, error) {
		path, provided, err := resolveConfigPath(cmd)
		if err != nil {
			return config.Config{}, "", false, err
		}
		cfg, _, err := config.Load(path)
		return cfg, path, provided, err
	}
	rootCmd := &cobra.Command{
		Use: "gh-hush", Short: "Explainable, policy-driven GitHub notification triage",
		Version: Version, SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, path, provided, err := resolveConfig(cmd)
			if err != nil {
				if !provided && errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("default config not found at %q; create a conservative starter config with: gh hush init-config --user YOUR-GITHUB-LOGIN --github-organization YOUR-ORGANIZATION", path)
				}
				return err
			}
			return runOperation(cmd, stdout, stderr, cfg, dryRun, confirm, quiet, debug)
		},
	}
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "override the default user-owned YAML policy path")
	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "classify notifications without prompting or mutating GitHub")
	rootCmd.Flags().BoolVar(&confirm, "confirm", false, "unsubscribe from and mark proposed notifications Done without prompting")
	rootCmd.Flags().BoolVar(&quiet, "quiet", false, "suppress the preview and print only a concise result to stderr")
	rootCmd.Flags().BoolVar(&debug, "debug", false, "write request and workflow diagnostics to stderr")
	rootCmd.MarkFlagsMutuallyExclusive("dry-run", "confirm")
	rootCmd.MarkFlagsMutuallyExclusive("quiet", "debug")
	var initUser, initOrganization string
	var initTeams []string
	initCmd := &cobra.Command{
		Use:   "init-config",
		Short: "Create a conservative starter configuration without overwriting files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if initUser == "" || initOrganization == "" {
				return errors.New("--user and --github-organization are required; gh-hush will not guess identity or team policy")
			}
			path, _, err := resolveConfigPath(cmd)
			if err != nil {
				return err
			}
			if err := config.Initialize(path, initUser, initOrganization, initTeams); err != nil {
				return err
			}
			_, err = fmt.Fprintf(stdout, "Created conservative starter configuration: %s\nReview it, then run: gh hush validate-config --config %q\n", path, path)
			return err
		},
	}
	initCmd.Flags().StringVar(&initUser, "user", "", "GitHub login that must match the authenticated gh account")
	initCmd.Flags().StringVar(&initOrganization, "github-organization", "", "primary GitHub organization login")
	initCmd.Flags().StringSliceVar(&initTeams, "team", nil, "team to protect in org/team-slug form (repeatable)")
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(&cobra.Command{
		Use:   "validate-config",
		Short: "Validate the configuration without contacting GitHub",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, path, _, err := resolveConfig(cmd)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(stdout, "Configuration is valid: %s\n", path)
			return err
		},
	})
	return rootCmd
}

func run(command *cobra.Command, stdout, stderr io.Writer, cfg config.Config, dryRun, confirm, quiet, debug bool) error {
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
		if !quiet {
			_, _ = fmt.Fprintf(stderr, "total runtime: %s (excludes interactive confirmation wait)\n", formatDuration(now().Sub(runStart)-confirmationWait))
		}
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
	if !quiet {
		_, _ = fmt.Fprintf(stderr, "authenticated and listed %d unread %s in %s\n", len(threads), notificationWord(len(threads)), formatDuration(now().Sub(inboxStart)))
	}
	evaluator := policy.NewEvaluator(cfg, client)
	classificationOutput := stderr
	if quiet {
		classificationOutput = io.Discard
	}
	decisions := classifyNotifications(ctx, classificationOutput, evaluator, threads)
	if !quiet {
		reportCtx := diagnostic.WithPhase(ctx, "report")
		reportStart := now()
		if err := report.Write(stdout, decisions); err != nil {
			diagnostic.Log(reportCtx, "operation_failed", diagnostic.String("operation", "write_preview"))
			return fmt.Errorf("write preview report: %w", err)
		}
		diagnostic.Log(reportCtx, "operation_complete", diagnostic.String("operation", "write_preview"), diagnostic.Int("count", len(decisions)))
		_, _ = fmt.Fprintf(stderr, "generated preview report in %s\n", formatDuration(now().Sub(reportStart)))
	}
	targetCount := countHushActions(decisions)
	if quiet {
		interactive := isTerminal(command.InOrStdin()) && isTerminal(command.ErrOrStderr())
		return runQuiet(command, stderr, decisions, dryRun, confirm, interactive, func() error {
			return application.ApplyQuiet(ctx, stderr, cfg, client, decisions)
		})
	}
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

func runQuiet(command *cobra.Command, stderr io.Writer, decisions []model.Decision, dryRun, confirm, interactive bool, apply func() error) error {
	if err := quietClassificationError(decisions); err != nil {
		return err
	}
	targetCount := countHushActions(decisions)
	if dryRun {
		_, _ = fmt.Fprintf(stderr, "Would update %d %s.\n", targetCount, notificationWord(targetCount))
		return nil
	}
	if targetCount == 0 {
		_, _ = fmt.Fprintln(stderr, "Done: no notification updates needed.")
		return nil
	}
	if !confirm {
		if !interactive {
			return errors.New("confirmation requires an interactive terminal; rerun with --confirm")
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
	return apply()
}

func quietClassificationError(decisions []model.Decision) error {
	var details []string
	for _, decision := range decisions {
		if decision.EnrichmentError != "" {
			details = append(details, fmt.Sprintf("notification %s: %s", decision.Thread.ID, decision.EnrichmentError))
		}
	}
	if len(details) == 0 {
		return nil
	}
	return fmt.Errorf("classification failed for %d %s: %s", len(details), notificationWord(len(details)), strings.Join(details, "; "))
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
