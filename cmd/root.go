package cmd

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

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
				return errors.New("--config PATH is required for --dry-run")
			}

			return runDryRun(cmd, stdout, stderr, configPath, writeManifest)
		},
	}

	rootCmd.Flags().StringVar(&configPath, "config", "", "path to a user-owned YAML policy file")
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

	client := ghclient.NewCLIClient()
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

	decisions := make([]model.Decision, 0, len(threads))
	for _, thread := range threads {
		enrichment := client.Enrich(ctxCommand.Context(), thread)
		decisions = append(decisions, policy.Classify(cfg, thread, enrichment))
	}

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
