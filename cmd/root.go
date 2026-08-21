package cmd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/maxbeizer/gh-hush/internal/config"
	ghclient "github.com/maxbeizer/gh-hush/internal/github"
	"github.com/maxbeizer/gh-hush/internal/manifest"
	"github.com/maxbeizer/gh-hush/internal/model"
	"github.com/maxbeizer/gh-hush/internal/policy"
	"github.com/maxbeizer/gh-hush/internal/report"
	"github.com/spf13/cobra"
)

var errApplyNotImplemented = errors.New("manifest apply is intentionally unavailable in v1; review dry-run behavior before adding mutations")

// NewRootCommand constructs the gh-hush command.
func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	var configPath string
	var dryRun bool
	var writeManifest string
	var applyManifest string

	rootCmd := &cobra.Command{
		Use:           "gh-hush",
		Short:         "Explainable, policy-driven GitHub notification triage",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if applyManifest != "" {
				if dryRun || configPath != "" || writeManifest != "" {
					return errors.New("--apply-manifest cannot be combined with --dry-run, --config, or --write-manifest")
				}
				return fmt.Errorf("%w: %s", errApplyNotImplemented, applyManifest)
			}
			if !dryRun {
				return errors.New("no operation selected; use --dry-run (GitHub mutations are not implemented)")
			}
			if configPath == "" {
				var err error
				configPath, err = config.DefaultPath()
				if err != nil {
					return fmt.Errorf("resolve default config path: %w", err)
				}
			}

			return runDryRun(cmd, stdout, stderr, configPath, writeManifest)
		},
	}

	rootCmd.Flags().StringVar(&configPath, "config", "", "override the default user-owned YAML policy path")
	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "classify notifications without mutating GitHub")
	rootCmd.Flags().StringVar(&writeManifest, "write-manifest", "", "write proposed unsubscribe actions to a new JSON file")
	rootCmd.Flags().StringVar(&applyManifest, "apply-manifest", "", "apply a reviewed manifest (not implemented in v1)")

	return rootCmd
}

func runDryRun(ctxCommand *cobra.Command, stdout, stderr io.Writer, configPath, manifestPath string) error {
	cfg, rawConfig, err := config.Load(configPath)
	if err != nil {
		return err
	}

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
		return fmt.Errorf("write dry-run report: %w", err)
	}

	if manifestPath != "" {
		configHash := fmt.Sprintf("%x", sha256.Sum256(rawConfig))
		m := manifest.Build(login, configHash, decisions)
		if err := manifest.WriteNew(manifestPath, m); err != nil {
			return fmt.Errorf("write manifest: %w", err)
		}
		_, _ = fmt.Fprintf(stderr, "wrote review-required manifest to %s\n", manifestPath)
	}

	return nil
}

type notificationEnricher interface {
	Enrich(context.Context, model.Notification) model.Enrichment
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
				enrichment := client.Enrich(ctx, thread)
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
