package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/aiyor/lxm/internal/lxd"
	"github.com/spf13/cobra"
)

type cmdOptions struct {
	dryRun         bool
	debug          bool
	wait           bool
	force          bool
	includeHidden  bool
	groupFilters   []string
	excludeFilters []string

	// Subcommand flags
	nameFilter string
	renameTo   string
	prune      bool
	noStart    bool
	format     string
	runAs      string
	envVars    []string
	prefix     string
	gc         bool
	outDir     string
	inPlace    bool
	skipRemote bool
	insecure   bool
}

type serviceGetter func() (lxd.InstanceService, error)

func newRootCmd(ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) (*cobra.Command, *cmdOptions) {
	opts := &cmdOptions{}

	rootCmd := &cobra.Command{
		Use:           "lxm",
		Short:         "Declarative LXD dev fleet manager",
		Long:          "lxm is a tool for declarative reconciliation and management of LXD development container fleets.",
		Version:       fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			fmtLower := strings.ToLower(opts.format)
			if fmtLower != "" && fmtLower != "text" && fmtLower != "json" {
				return &exitError{code: 2, err: fmt.Errorf("invalid format %q, expected text|json", opts.format)}
			}
			if opts.debug {
				logLevelVar.Set(slog.LevelDebug)
				logger.Debug("Debug mode enabled. Verbose logging activated.")
			} else {
				logLevelVar.Set(slog.LevelInfo)
			}
			if opts.dryRun {
				logger.Info("Dry-run mode enabled. No changes will be made.")
			}
			if opts.wait {
				logger.Info("Wait mode enabled. Will wait for cloud-init to finish.")
			}
			if opts.force {
				logger.Info("Force mode enabled. Will re-run recipes even if hashes match.")
			}
			if opts.includeHidden {
				logger.Info("Including hidden (_-prefixed) config files.")
			}
			if len(opts.groupFilters) > 0 {
				logger.Info("Group filter enabled", "groups", opts.groupFilters)
			}
			if len(opts.excludeFilters) > 0 {
				logger.Info("Exclude group filter enabled", "exclude", opts.excludeFilters)
			}
			return nil
		},
	}

	pf := rootCmd.PersistentFlags()
	pf.BoolVar(&opts.dryRun, "dry-run", false, "Show what would change without applying")
	pf.BoolVar(&opts.debug, "debug", false, "Show verbose output")
	pf.BoolVar(&opts.wait, "wait", false, "Wait for cloud-init to finish")
	pf.BoolVar(&opts.force, "force", false, "Re-run recipes even if hashes match")
	pf.BoolVar(&opts.includeHidden, "include-hidden", false, "Include _-prefixed base config files")
	pf.StringVar(&opts.format, "format", "text", "Output format (text, json)")
	pf.StringSliceVarP(&opts.groupFilters, "group", "g", nil, "Filter to containers matching ANY tag (OR)")
	pf.StringSliceVar(&opts.excludeFilters, "exclude-group", nil, "Exclude containers matching ANY tag (OR)")

	registerCommands(rootCmd, opts, ctx, stdout, stderr, getSvc, logger)

	return rootCmd, opts
}
