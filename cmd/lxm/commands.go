package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aiyor/lxm/internal/apply"
	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/fleet"
	"github.com/aiyor/lxm/internal/lxd"
	"github.com/aiyor/lxm/internal/output"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/recipe"
	"github.com/canonical/lxd/shared/api"
	"github.com/spf13/cobra"
)

func registerCommands(rootCmd *cobra.Command, opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) {
	rootCmd.AddCommand(newApplyCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newPlanCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newDiffCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newListCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newStatusCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newInitCmd(opts, ctx, stdout, stderr, logger))
	rootCmd.AddCommand(newRunCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newScriptCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newSnapshotCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newRollbackCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newSSHCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newShellCmd(opts, ctx, stdout, stderr, getSvc, logger))
	rootCmd.AddCommand(newIncludeCmd(opts, ctx, stdout, stderr, logger))
	rootCmd.AddCommand(newCompileCmd(opts, ctx, stdout, stderr, logger))
	rootCmd.AddCommand(newDoctorCmd(opts, ctx, stdout, stderr, getSvc, logger))
}

func newApplyCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply <file|dir>",
		Short: "Reconcile desired state (manifests) against live LXD containers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			info, err := os.Stat(target)
			if err != nil {
				return &exitError{code: 5, err: fmt.Errorf("target %q not found: %w", target, err)}
			}

			if opts.prune && !info.IsDir() {
				return &exitError{code: 2, err: fmt.Errorf("--prune is only allowed on directory targets")}
			}

			if opts.renameTo != "" && info.IsDir() {
				return &exitError{code: 2, err: fmt.Errorf("--rename-to is only allowed on single-file targets")}
			}

			configFiles, err := discoverYAMLFiles(target, opts.includeHidden, logger)
			if err != nil || len(configFiles) == 0 {
				return &exitError{code: 5, err: fmt.Errorf("no YAML manifests found in target %q", target)}
			}

			var loaded []*config.Config
			for _, configFile := range configFiles {
				conf, err := config.LoadConfig(configFile)
				if err != nil {
					return &exitError{code: 3, err: fmt.Errorf("loading manifest %q: %w", configFile, err)}
				}
				configBaseDir := filepath.Dir(configFile)
				if err := conf.Validate(configBaseDir); err != nil {
					return &exitError{code: 3, err: fmt.Errorf("config validation %q: %w", configFile, err)}
				}

				if opts.wait {
					conf.WaitPolicy.Required = true
				}
				loaded = append(loaded, conf)
			}

			sel, err := fleet.NewSelector(fleet.SelectorOpts{
				Groups:        opts.groupFilters,
				ExcludeGroups: opts.excludeFilters,
				Name:          opts.nameFilter,
			})
			if err != nil {
				return &exitError{code: 2, err: err}
			}

			selectedConfigs, err := sel.FilterConfigs(loaded)
			if err != nil {
				return &exitError{code: 5, err: err}
			}

			if opts.renameTo != "" && !info.IsDir() {
				for _, conf := range selectedConfigs {
					conf.Name = opts.renameTo
				}
			}

			svc, err := getSvc()
			if err != nil {
				return err
			}

			liveSnapshots, err := fetchLiveSnapshots(svc)
			if err != nil {
				return &exitError{code: 4, err: fmt.Errorf("fetching live instance state: %w", err)}
			}

			reconciler := plan.NewReconciler()
			hasRebuild := svc.HasExtension("instances_rebuild")

			combinedPlan := &plan.Plan{
				Schema: "lxm/plan/v1",
				Steps:  []plan.Step{},
			}

			for _, conf := range selectedConfigs {
				p, err := reconciler.Compute(conf, liveSnapshots, hasRebuild)
				if err != nil {
					return &exitError{code: 3, err: fmt.Errorf("computing reconciliation plan: %w", err)}
				}
				combinedPlan.Steps = append(combinedPlan.Steps, p.Steps...)
			}

			if opts.prune && info.IsDir() && svc != nil {
				inv, err := fleet.GetInventory(svc)
				if err == nil && inv != nil {
					orphans := fleet.FindOrphans(inv.Instances, loaded, sel)
					for _, orphan := range orphans {
						orphanETag := ""
						if snap, ok := liveSnapshots[orphan.Name]; ok {
							orphanETag = snap.ETag
						}
						orphanStep := plan.Step{
							Container: orphan.Name,
							Action:    "delete",
							Changed:   true,
							ETag:      orphanETag,
							Diff: []plan.FieldDiff{
								{Field: "status", Old: "present", New: "absent"},
							},
						}
						combinedPlan.Steps = append(combinedPlan.Steps, orphanStep)
					}
				}
			}

			combinedPlan.Summary = computePlanSummary(combinedPlan.Steps)
			lastComputedPlan = combinedPlan

			executor := apply.NewExecutor(svc)
			report, applyErr := executor.Apply(ctx, combinedPlan, apply.ApplyOpts{
				Jobs:         5,
				DryRun:       opts.dryRun,
				Force:        opts.force,
				Prune:        opts.prune,
				IsSingleFile: !info.IsDir(),
				NoStart:      opts.noStart,
			})
			lastApplyReport = report

			if applyErr != nil {
				return &exitError{code: 1, err: applyErr}
			}

			if report != nil && report.ExitCode != 0 {
				errMsg := "apply failed"
				if len(report.Errors) > 0 {
					errMsg = report.Errors[0].Message
				}
				return &exitError{code: report.ExitCode, err: errors.New(errMsg)}
			}

			if opts.format == "text" && report != nil {
				fmt.Fprintf(stdout, "Applied %d step(s) across %d container(s)\n", len(report.Results), len(selectedConfigs))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&opts.nameFilter, "name", "", "Filter container name by pattern")
	cmd.Flags().StringVar(&opts.renameTo, "rename-to", "", "Rename container (single-file target only)")
	cmd.Flags().BoolVar(&opts.prune, "prune", false, "Garbage-collect orphaned managed containers (deletes containers with user.lxm.managed=true missing from target dir)")
	cmd.Flags().BoolVar(&opts.noStart, "no-start", false, "Do not start stopped containers after apply")
	return cmd
}

func newPlanCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan <file|dir>",
		Short: "Compute and print the reconciliation Plan without mutating live state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.format != "text" && opts.format != "json" {
				return &exitError{code: 2, err: fmt.Errorf("invalid format %q, expected text|json", opts.format)}
			}

			target := args[0]
			info, err := os.Stat(target)
			if err != nil {
				return &exitError{code: 5, err: fmt.Errorf("target %q not found: %w", target, err)}
			}

			if opts.prune && !info.IsDir() {
				return &exitError{code: 2, err: fmt.Errorf("--prune is only allowed on directory targets")}
			}

			configFiles, err := discoverYAMLFiles(target, opts.includeHidden, logger)
			if err != nil || len(configFiles) == 0 {
				return &exitError{code: 5, err: fmt.Errorf("no manifests found in target %q", target)}
			}

			var loaded []*config.Config
			for _, configFile := range configFiles {
				conf, err := config.LoadConfig(configFile)
				if err != nil {
					return &exitError{code: 3, err: fmt.Errorf("loading manifest %q: %w", configFile, err)}
				}
				configBaseDir := filepath.Dir(configFile)
				if err := conf.Validate(configBaseDir); err != nil {
					return &exitError{code: 3, err: fmt.Errorf("config validation %q: %w", configFile, err)}
				}
				loaded = append(loaded, conf)
			}

			sel, err := fleet.NewSelector(fleet.SelectorOpts{
				Groups:        opts.groupFilters,
				ExcludeGroups: opts.excludeFilters,
				Name:          opts.nameFilter,
			})
			if err != nil {
				return &exitError{code: 2, err: err}
			}

			selectedConfigs, err := sel.FilterConfigs(loaded)
			if err != nil {
				return &exitError{code: 5, err: err}
			}

			liveSnapshots := make(map[string]*plan.InstanceSnapshot)
			hasRebuild := false

			svc, err := getSvc()
			if err == nil && svc != nil {
				liveSnapshots, _ = fetchLiveSnapshots(svc)
				hasRebuild = svc.HasExtension("instances_rebuild")
			}

			reconciler := plan.NewReconciler()
			combinedPlan := &plan.Plan{
				Schema: "lxm/plan/v1",
				Steps:  []plan.Step{},
			}

			for _, conf := range selectedConfigs {
				p, err := reconciler.Compute(conf, liveSnapshots, hasRebuild)
				if err != nil {
					return &exitError{code: 3, err: fmt.Errorf("computing reconciliation plan: %w", err)}
				}
				combinedPlan.Steps = append(combinedPlan.Steps, p.Steps...)
			}

			if opts.prune && info.IsDir() && svc != nil {
				inv, err := fleet.GetInventory(svc)
				if err == nil && inv != nil {
					orphans := fleet.FindOrphans(inv.Instances, loaded, sel)
					for _, orphan := range orphans {
						orphanETag := ""
						if snap, ok := liveSnapshots[orphan.Name]; ok {
							orphanETag = snap.ETag
						}
						orphanStep := plan.Step{
							Container: orphan.Name,
							Action:    "delete",
							Changed:   true,
							ETag:      orphanETag,
							Diff: []plan.FieldDiff{
								{Field: "status", Old: "present", New: "absent"},
							},
						}
						combinedPlan.Steps = append(combinedPlan.Steps, orphanStep)
					}
				}
			}

			combinedPlan.Summary = computePlanSummary(combinedPlan.Steps)
			lastComputedPlan = combinedPlan

			if opts.format == "text" {
				fmt.Fprintf(stdout, "Plan: %d to create, %d to update, %d to recreate, %d to delete, %d noop across %d manifest(s)\n",
					combinedPlan.Summary.Create, combinedPlan.Summary.Update, combinedPlan.Summary.Recreate, combinedPlan.Summary.Delete, combinedPlan.Summary.Noop, len(selectedConfigs))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&opts.nameFilter, "name", "", "Filter container name")
	cmd.Flags().BoolVar(&opts.prune, "prune", false, "Garbage-collect orphaned managed containers (deletes containers with user.lxm.managed=true missing from target dir)")
	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	return cmd
}

func newDiffCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff <config-file> <container>",
		Short: "Show Plan scoped to a single container",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			configFile := args[0]
			containerName := args[1]

			conf, err := config.LoadConfig(configFile)
			if err != nil {
				return &exitError{code: 3, err: fmt.Errorf("loading manifest %q: %w", configFile, err)}
			}
			if err := conf.Validate(filepath.Dir(configFile)); err != nil {
				return &exitError{code: 3, err: fmt.Errorf("config validation %q: %w", configFile, err)}
			}
			conf.Name = containerName

			liveSnapshots := make(map[string]*plan.InstanceSnapshot)
			hasRebuild := false

			svc, err := getSvc()
			if err == nil && svc != nil {
				liveSnapshots, _ = fetchLiveSnapshots(svc)
				hasRebuild = svc.HasExtension("instances_rebuild")
			}

			reconciler := plan.NewReconciler()
			p, err := reconciler.Compute(conf, liveSnapshots, hasRebuild)
			if err != nil {
				return &exitError{code: 3, err: fmt.Errorf("computing diff: %w", err)}
			}
			lastComputedPlan = p

			if opts.format == "text" {
				if len(p.Steps) > 0 {
					step := p.Steps[0]
					fmt.Fprintf(stdout, "Diff for container %s (action: %s, changed: %v):\n", containerName, step.Action, step.Changed)
					for _, d := range step.Diff {
						fmt.Fprintf(stdout, "  - %s: old=%v -> new=%v\n", d.Field, d.Old, d.New)
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	return cmd
}

func newListCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List fleet inventory (managed containers and live state)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.format != "text" && opts.format != "json" {
				return &exitError{code: 2, err: fmt.Errorf("invalid format %q, expected text|json", opts.format)}
			}
			svc, err := getSvc()
			if err != nil {
				return err
			}
			inv, err := fleet.GetInventory(svc)
			if err != nil {
				return &exitError{code: 4, err: fmt.Errorf("failed to list instances: %w", err)}
			}

			sel, err := fleet.NewSelector(fleet.SelectorOpts{
				Groups:        opts.groupFilters,
				ExcludeGroups: opts.excludeFilters,
				Name:          opts.nameFilter,
			})
			if err != nil {
				return &exitError{code: 2, err: err}
			}

			var filtered []fleet.InstanceStatus
			for _, inst := range inv.Instances {
				if sel.Matches(inst.Name, inst.Groups) {
					filtered = append(filtered, inst)
				}
			}

			if len(inv.Instances) > 0 && len(filtered) == 0 {
				return &exitError{code: 5, err: fmt.Errorf("no containers found matching filter criteria")}
			}

			lastCommandResults = nil
			for _, inst := range filtered {
				lastCommandResults = append(lastCommandResults, output.ResultItem{
					Container: inst.Name,
					Action:    inst.Status,
					OK:        true,
				})
			}

			if opts.format == "text" {
				w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tTYPE\tSTATUS\tMANAGED\tGROUPS\tIMAGE\tIP")
				for _, inst := range filtered {
					managedStr := "false"
					if inst.Managed {
						managedStr = "true"
					}
					groupsStr := strings.Join(inst.Groups, ",")
					if groupsStr == "" {
						groupsStr = "-"
					}
					ipStr := strings.Join(inst.IPs, ",")
					if ipStr == "" {
						ipStr = "-"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", inst.Name, inst.Type, inst.Status, managedStr, groupsStr, inst.Image, ipStr)
				}
				_ = w.Flush()
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.nameFilter, "name", "", "Filter container name by pattern")
	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	return cmd
}

func newStatusCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Show cloud-init, network, recipe, and snapshot status for a container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := getSvc()
			if err != nil {
				return err
			}
			name := args[0]
			inst, _, err := svc.GetInstance(name)
			if err != nil || inst == nil {
				return &exitError{code: 5, err: fmt.Errorf("container %q not found: %w", name, err)}
			}

			lastCommandResults = []output.ResultItem{
				{
					Container: inst.Name,
					Action:    inst.Status,
					OK:        true,
				},
			}

			ip, _ := svc.GetIP(name)
			recipeHashes := make(map[string]string)
			for k, v := range inst.Config {
				if strings.HasPrefix(k, "user.lxm.recipe.") && strings.HasSuffix(k, ".hash") {
					rName := strings.TrimPrefix(k, "user.lxm.recipe.")
					rName = strings.TrimSuffix(rName, ".hash")
					recipeHashes[rName] = v
				}
			}

			if opts.format == "text" {
				w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintf(w, "Container:\t%s\n", inst.Name)
				fmt.Fprintf(w, "Status:\t%s (Code %d)\n", inst.Status, inst.StatusCode)
				fmt.Fprintf(w, "Architecture:\t%s\n", inst.Architecture)
				fmt.Fprintf(w, "IP Address:\t%s\n", ip)
				fmt.Fprintf(w, "Managed:\t%s\n", inst.Config["user.lxm.managed"])
				fmt.Fprintf(w, "Groups:\t%s\n", inst.Config["user.lxm.groups"])

				fmt.Fprintln(w, "\nRecipe Hash Trail:")
				if len(recipeHashes) == 0 {
					fmt.Fprintln(w, "  (none)")
				} else {
					for r, h := range recipeHashes {
						fmt.Fprintf(w, "  - %s: %s\n", r, h)
					}
				}
				_ = w.Flush()
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	return cmd
}

func parseEnvVars(envSlice []string) (map[string]string, error) {
	if len(envSlice) == 0 {
		return nil, nil
	}
	m := make(map[string]string)
	for _, item := range envSlice {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid environment variable format %q (expected KEY=VAL)", item)
		}
		m[parts[0]] = parts[1]
	}
	if err := recipe.ValidateEnvKeys(m); err != nil {
		return nil, err
	}
	return m, nil
}

func newRunCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <target> <script>",
		Short: "Run a script across targeted fleet containers",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := getSvc()
			if err != nil {
				return err
			}
			target := args[0]
			scriptPath := args[1]

			runAs := opts.runAs
			if runAs == "" {
				runAs = "root"
			}

			envMap, err := parseEnvVars(opts.envVars)
			if err != nil {
				return &exitError{code: 3, err: err}
			}

			if opts.dryRun {
				if opts.format == "text" {
					fmt.Fprintf(stdout, "[DRY-RUN] Would run script %q on target %q\n", scriptPath, target)
				}
				return nil
			}

			// If target is a single container name
			inst, _, err := svc.GetInstance(target)
			if err == nil && inst != nil {
				res, _, execErr := recipe.ExecuteRecipeScriptContext(cmd.Context(), svc, target, scriptPath, "", runAs, envMap, 0)
				if execErr != nil || res.ExitCode != 0 {
					errMsg := res.Stderr
					if errMsg == "" && execErr != nil {
						errMsg = execErr.Error()
					}
					return &exitError{code: 6, err: fmt.Errorf("executing script on %q: %s", target, errMsg)}
				}
				if opts.format == "text" && len(res.Stdout) > 0 {
					fmt.Fprint(stdout, res.Stdout)
				}
				return nil
			}

			// Target is a directory or fleet target
			configFiles, err := discoverYAMLFiles(target, opts.includeHidden, logger)
			if err != nil || len(configFiles) == 0 {
				return &exitError{code: 5, err: fmt.Errorf("target container or directory %q not found", target)}
			}

			var loaded []*config.Config
			for _, configFile := range configFiles {
				conf, err := config.LoadConfig(configFile)
				if err != nil {
					return &exitError{code: 3, err: fmt.Errorf("loading manifest %q: %w", configFile, err)}
				}
				if err := conf.Validate(filepath.Dir(configFile)); err != nil {
					return &exitError{code: 3, err: fmt.Errorf("config validation %q: %w", configFile, err)}
				}
				loaded = append(loaded, conf)
			}

			sel, err := fleet.NewSelector(fleet.SelectorOpts{
				Groups:        opts.groupFilters,
				ExcludeGroups: opts.excludeFilters,
				Name:          opts.nameFilter,
			})
			if err != nil {
				return &exitError{code: 2, err: err}
			}

			selectedConfigs, err := sel.FilterConfigs(loaded)
			if err != nil {
				return &exitError{code: 5, err: err}
			}

			var errs []string
			for _, conf := range selectedConfigs {
				// H5 Fix: Pass conf.ConfigBaseDir directly (not filepath.Dir)
				res, _, execErr := recipe.ExecuteRecipeScriptContext(cmd.Context(), svc, conf.Name, scriptPath, conf.ConfigBaseDir, runAs, envMap, 0)
				if execErr != nil || res.ExitCode != 0 {
					errs = append(errs, fmt.Sprintf("%s: exit %d", conf.Name, res.ExitCode))
				}
			}

			if len(errs) > 0 {
				return &exitError{code: 6, err: fmt.Errorf("run failed on containers: %s", strings.Join(errs, ", "))}
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&opts.runAs, "run-as", "root", "User to run script as")
	cmd.Flags().StringSliceVar(&opts.envVars, "env", nil, "Environment variables")
	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Preview execution without modifying containers")
	return cmd
}

func newScriptCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "script <name> <path> [user]",
		Short: "Run a single script on a container",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := getSvc()
			if err != nil {
				return err
			}
			name := args[0]
			scriptPath := args[1]

			runAs := opts.runAs
			if len(args) == 3 {
				runAs = args[2]
			}
			if runAs == "" {
				runAs = "root"
			}

			envMap, err := parseEnvVars(opts.envVars)
			if err != nil {
				return &exitError{code: 3, err: err}
			}

			_, _, err = svc.GetInstance(name)
			if err != nil {
				return &exitError{code: 5, err: fmt.Errorf("container %q not found: %w", name, err)}
			}

			if opts.dryRun {
				if opts.format == "text" {
					fmt.Fprintf(stdout, "[DRY-RUN] Would run script %q on container %q\n", scriptPath, name)
				}
				return nil
			}

			res, _, err := recipe.ExecuteRecipeScriptContext(cmd.Context(), svc, name, scriptPath, "", runAs, envMap, 0)
			if err != nil || res.ExitCode != 0 {
				errMsg := res.Stderr
				if errMsg == "" && err != nil {
					errMsg = err.Error()
				}
				return &exitError{code: 6, err: fmt.Errorf("executing script %q on %q: %s", scriptPath, name, errMsg)}
			}

			if opts.format == "text" && len(res.Stdout) > 0 {
				fmt.Fprint(stdout, res.Stdout)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.runAs, "run-as", "root", "User to run script as")
	cmd.Flags().StringSliceVar(&opts.envVars, "env", nil, "Environment variables")
	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Preview execution without modifying container")
	return cmd
}

func newSnapshotCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	var gcFlag bool
	var deleteSnapName string
	var olderThanStr string

	cmd := &cobra.Command{
		Use:   "snapshot [name] [snapshot_name]",
		Short: "Manage instance snapshots",
		Args:  cobra.MaximumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := getSvc()
			if err != nil {
				return err
			}

			if len(args) > 0 {
				switch args[0] {
				case "gc":
					gcFlag = true
					args = args[1:]
				case "list":
					args = args[1:]
				case "create":
					if len(args) < 3 {
						return &exitError{code: 2, err: fmt.Errorf("snapshot create requires container name and snapshot name")}
					}
					args = args[1:]
				case "delete":
					if len(args) < 3 {
						return &exitError{code: 2, err: fmt.Errorf("snapshot delete requires container name and snapshot name")}
					}
					deleteSnapName = args[2]
					args = []string{args[1]}
				}
			}

			if gcFlag {
				targetName := ""
				if len(args) > 0 {
					targetName = args[0]
				}
				prefix := opts.prefix
				if prefix == "" {
					prefix = "user.lxm.snap."
				}

				retentionDuration := 7 * 24 * time.Hour
				if olderThanStr != "" {
					if d, err := time.ParseDuration(olderThanStr); err == nil {
						retentionDuration = d
					} else if strings.HasSuffix(olderThanStr, "d") {
						daysStr := strings.TrimSuffix(olderThanStr, "d")
						if days, err := strconv.Atoi(daysStr); err == nil {
							retentionDuration = time.Duration(days) * 24 * time.Hour
						}
					}
				}

				var instancesToGC []string
				if targetName != "" {
					instancesToGC = append(instancesToGC, targetName)
				} else {
					insts, err := svc.ListInstances()
					if err != nil {
						return &exitError{code: 4, err: fmt.Errorf("listing instances for snapshot GC: %w", err)}
					}
					for _, full := range insts {
						instancesToGC = append(instancesToGC, full.Name)
					}
				}

				prunedCount := 0
				now := time.Now()
				for _, name := range instancesToGC {
					snaps, err := svc.GetInstanceSnapshots(name)
					if err != nil {
						continue
					}
					for _, s := range snaps {
						if strings.HasPrefix(s.Name, prefix) {
							if olderThanStr == "" || now.Sub(s.CreatedAt) >= retentionDuration {
								if opts.dryRun {
									prunedCount++
									continue
								}
								if err := svc.DeleteInstanceSnapshot(name, s.Name); err == nil {
									prunedCount++
								}
							}
						}
					}
				}

				if opts.format == "text" {
					if opts.dryRun {
						fmt.Fprintf(stdout, "[DRY-RUN] Would prune %d snapshot(s) matching prefix %q\n", prunedCount, prefix)
					} else {
						fmt.Fprintf(stdout, "Pruned %d snapshot(s) matching prefix %q\n", prunedCount, prefix)
					}
				}
				return nil
			}

			if len(args) == 0 {
				return &exitError{code: 2, err: fmt.Errorf("accepts at least 1 arg(s), received 0")}
			}

			name := args[0]
			_, _, err = svc.GetInstance(name)
			if err != nil {
				return &exitError{code: 5, err: fmt.Errorf("container %q not found: %w", name, err)}
			}

			if deleteSnapName != "" {
				if opts.dryRun {
					if opts.format == "text" {
						fmt.Fprintf(stdout, "[DRY-RUN] Would delete snapshot %q for instance %q\n", deleteSnapName, name)
					}
					return nil
				}
				if err := svc.DeleteInstanceSnapshot(name, deleteSnapName); err != nil {
					return &exitError{code: 4, err: fmt.Errorf("deleting snapshot %q on %q: %w", deleteSnapName, name, err)}
				}
				if opts.format == "text" {
					fmt.Fprintf(stdout, "Deleted snapshot %q for instance %q\n", deleteSnapName, name)
				}
				return nil
			}

			if len(args) == 2 {
				snapName := args[1]
				if opts.dryRun {
					if opts.format == "text" {
						fmt.Fprintf(stdout, "[DRY-RUN] Would create snapshot %q for instance %q\n", snapName, name)
					}
					return nil
				}
				req := api.InstanceSnapshotsPost{Name: snapName}
				if err := svc.CreateInstanceSnapshotContext(cmd.Context(), name, req); err != nil {
					return &exitError{code: 4, err: fmt.Errorf("creating snapshot %q on %q: %w", snapName, name, err)}
				}
				if opts.format == "text" {
					fmt.Fprintf(stdout, "Created snapshot %q for instance %q\n", snapName, name)
				}
				return nil
			}

			snaps, err := svc.GetInstanceSnapshots(name)
			if err != nil {
				return &exitError{code: 4, err: fmt.Errorf("listing snapshots for %q: %w", name, err)}
			}

			if opts.format == "text" {
				w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "SNAPSHOT\tSTATEFUL\tCREATED")
				for _, s := range snaps {
					fmt.Fprintf(w, "%s\t%v\t%s\n", s.Name, s.Stateful, s.CreatedAt.Format(time.RFC3339))
				}
				_ = w.Flush()
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&gcFlag, "gc", false, "Prune aged/recipe snapshots")
	cmd.Flags().StringVar(&olderThanStr, "older-than", "", "Age threshold for snapshot GC (e.g. 7d, 24h)")
	cmd.Flags().StringVar(&deleteSnapName, "delete", "", "Delete specified snapshot name")
	cmd.Flags().StringVar(&opts.prefix, "prefix", "user.lxm.snap.", "Snapshot prefix for GC")
	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Preview snapshot operations without modifying LXD state")
	return cmd
}

func newRollbackCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollback <name> <snapshot>",
		Short: "Restore an instance to a previous snapshot",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := getSvc()
			if err != nil {
				return err
			}
			name := args[0]
			snapName := args[1]

			_, _, err = svc.GetInstance(name)
			if err != nil {
				return &exitError{code: 5, err: fmt.Errorf("container %q not found: %w", name, err)}
			}

			if opts.dryRun {
				if opts.format == "text" {
					fmt.Fprintf(stdout, "[DRY-RUN] Would restore container %q to snapshot %q\n", name, snapName)
				}
				return nil
			}

			if err := svc.RestoreInstanceSnapshotContext(cmd.Context(), name, snapName); err != nil {
				return &exitError{code: 4, err: fmt.Errorf("restoring container %q to snapshot %q: %w", name, snapName, err)}
			}

			if opts.format == "text" {
				fmt.Fprintf(stdout, "Successfully restored container %q to snapshot %q\n", name, snapName)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Preview rollback operation")
	return cmd
}

func isHostKeyBypassArg(arg string) bool {
	clean := strings.ToLower(strings.TrimSpace(arg))
	// Normalize the config-file space form (KEY VALUE) to KEY=VALUE, matching
	// the '=' grammar OpenSSH also accepts ('-o "UserKnownHostsFile /dev/null"').
	// Without this, the dedup (which recognizes the space form) drops lxm's
	// default store while the bypass branch never fires, leaving strict
	// checking against an empty store.
	if !strings.Contains(clean, "=") {
		if sp := strings.IndexAny(clean, " \t"); sp > 0 {
			clean = clean[:sp] + "=" + strings.TrimSpace(clean[sp+1:])
		}
	}
	if strings.HasPrefix(clean, "stricthostkeychecking=") {
		val := strings.TrimPrefix(clean, "stricthostkeychecking=")
		return val == "no" || val == "off" || val == "false" || val == "0"
	}
	if strings.HasPrefix(clean, "userknownhostsfile=") {
		val := strings.TrimPrefix(clean, "userknownhostsfile=")
		return val == "/dev/null" || val == "none"
	}
	return false
}

// sshUserOptions returns the ssh options the user supplied explicitly
// (via -o KEY=VALUE, -o KEY VALUE, -oKEY=VALUE, -p PORT, or -pPORT). Keys
// are normalized to lowercase because OpenSSH config keywords are
// case-insensitive. OpenSSH honors the FIRST occurrence of an option, so
// lxm must not emit its own defaults for an option the user already set:
// lxm's defaults are built from this map, skipping keys the user provided
// (UG5 B4). Without the skip, lxm's `-o Port=22` / `-o
// StrictHostKeyChecking=yes` placed before the user's `-p 2222` / `-o
// StrictHostKeyChecking=no` silently won (first occurrence wins in
// OpenSSH), defeating the documented overrides.
func sshUserOptions(args []string) map[string]bool {
	set := make(map[string]bool)
	mark := func(key string) {
		if key != "" {
			set[strings.ToLower(key)] = true
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" && i+1 < len(args):
			mark("Port")
			i++
		case strings.HasPrefix(a, "-p") && len(a) > 2:
			if _, err := strconv.Atoi(a[2:]); err == nil {
				mark("Port")
			}
		case strings.HasPrefix(a, "-o"):
			opt := strings.TrimPrefix(a, "-o")
			if opt == "" && i+1 < len(args) {
				opt = args[i+1]
				i++
			}
			opt = strings.TrimSpace(opt)
			// Accept both KEY=VALUE and the config-file space form KEY VALUE.
			var key string
			if eq := strings.IndexByte(opt, '='); eq > 0 {
				key = opt[:eq]
			} else if sp := strings.IndexAny(opt, " \t"); sp > 0 {
				key = opt[:sp]
			}
			mark(key)
		}
	}
	return set
}

func newSSHCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:                "ssh <name> [ssh_args...]",
		Short:              "Open an SSH session to a container (hardened host-key verification)",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &exitError{code: 2, err: fmt.Errorf("accepts at least 1 arg(s), received 0")}
			}

			var extraSSHArgs []string
			isInsecure := false
			user := ""
			identity := ""
			isDryRun := opts.dryRun
			var name string

			i := 0
			for i < len(args) {
				arg := args[i]
				if arg == "--" {
					extraSSHArgs = append(extraSSHArgs, args[i+1:]...)
					break
				}
				if strings.EqualFold(arg, "--format=json") || (strings.EqualFold(arg, "--format") && i+1 < len(args) && strings.EqualFold(args[i+1], "json")) {
					return &exitError{code: 2, err: fmt.Errorf("interactive command ssh rejects --format json")}
				}
				if strings.HasPrefix(arg, "--format=") {
					if strings.EqualFold(strings.TrimPrefix(arg, "--format="), "json") {
						return &exitError{code: 2, err: fmt.Errorf("interactive command ssh rejects --format json")}
					}
					i++
					continue
				}
				if arg == "--format" && i+1 < len(args) {
					if strings.EqualFold(args[i+1], "json") {
						return &exitError{code: 2, err: fmt.Errorf("interactive command ssh rejects --format json")}
					}
					i += 2
					continue
				}
				if arg == "--insecure" {
					isInsecure = true
					i++
					continue
				}
				if arg == "--dry-run" {
					isDryRun = true
					i++
					continue
				}
				if strings.HasPrefix(arg, "--user=") {
					user = strings.TrimPrefix(arg, "--user=")
					i++
					continue
				}
				if (arg == "--user" || arg == "-u" || arg == "--run-as") && i+1 < len(args) {
					user = args[i+1]
					i += 2
					continue
				}
				if strings.HasPrefix(arg, "--run-as=") {
					user = strings.TrimPrefix(arg, "--run-as=")
					i++
					continue
				}
				if strings.HasPrefix(arg, "-i=") || strings.HasPrefix(arg, "--identity=") {
					identity = strings.SplitN(arg, "=", 2)[1]
					i++
					continue
				}
				if (arg == "-i" || arg == "--identity") && i+1 < len(args) {
					identity = args[i+1]
					i += 2
					continue
				}

				if strings.HasPrefix(arg, "-") {
					extraSSHArgs = append(extraSSHArgs, arg)
					if (arg == "-o" || arg == "-p" || arg == "-c" || arg == "-b" || arg == "-e" || arg == "-l" || arg == "-m" || arg == "-S") && i+1 < len(args) {
						extraSSHArgs = append(extraSSHArgs, args[i+1])
						i += 2
						continue
					}
					i++
					continue
				}

				if name == "" {
					name = arg
				} else {
					extraSSHArgs = append(extraSSHArgs, arg)
				}
				i++
			}

			if strings.EqualFold(opts.format, "json") {
				return &exitError{code: 2, err: fmt.Errorf("interactive command ssh rejects --format json")}
			}

			if name == "" {
				return &exitError{code: 2, err: fmt.Errorf("accepts at least 1 arg(s), received 0")}
			}

			svc, err := getSvc()
			if err != nil {
				return err
			}

			inst, _, err := svc.GetInstance(name)
			if err != nil {
				return &exitError{code: 5, err: fmt.Errorf("container %q not found: %w", name, err)}
			}

			var ip string
			fullInstances, err := svc.ListInstances()
			if err == nil {
				for _, full := range fullInstances {
					if full.Name == name {
						if full.State != nil && full.State.Network != nil {
							for _, net := range full.State.Network {
								for _, addr := range net.Addresses {
									if addr.Family == "inet" && addr.Address != "127.0.0.1" {
										ip = addr.Address
										break
									}
								}
								if ip != "" {
									break
								}
							}
						}
					}
				}
			}

			if ip == "" {
				if getIPer, ok := svc.(interface{ GetIP(string) (string, error) }); ok {
					if testIP, err := getIPer.GetIP(name); err == nil && testIP != "" {
						ip = testIP
					}
				}
			}

			if ip == "" || inst.Status == "Stopped" || inst.StatusCode == api.Stopped {
				if isDryRun {
					ip = "<no-ipv4>"
				} else {
					return &exitError{code: 6, err: fmt.Errorf("container %q has no IPv4 address / is not running", name)}
				}
			}

			targetUser := user
			if targetUser == "" {
				if opts.runAs != "" {
					targetUser = opts.runAs
				} else if u, ok := inst.Config["user.lxm.user"]; ok && u != "" {
					targetUser = u
				} else {
					targetUser = "root"
				}
			}

			knownMgr := fleet.DefaultKnownHostsManager()

			hasHostKeyBypass := false
			for j, a := range extraSSHArgs {
				if a == "-o" && j+1 < len(extraSSHArgs) {
					if isHostKeyBypassArg(extraSSHArgs[j+1]) {
						hasHostKeyBypass = true
						break
					}
				} else if strings.HasPrefix(a, "-o") {
					opt := strings.TrimPrefix(a, "-o")
					if isHostKeyBypassArg(opt) {
						hasHostKeyBypass = true
						break
					}
				}
			}

			// Options the user set explicitly must not be shadowed by lxm's
			// defaults: OpenSSH honors the first occurrence, and lxm's own
			// options precede the user's passthrough args in the argv.
			userOpts := sshUserOptions(extraSSHArgs)

			var execArgs []string
			if isInsecure {
				fmt.Fprintln(stderr, "WARNING: Host key verification disabled by --insecure flag")
				execArgs = []string{
					"-o", fmt.Sprintf("HostName=%s", ip),
					"-o", "Port=22",
					"-o", "UserKnownHostsFile=/dev/null",
					"-o", "StrictHostKeyChecking=no",
				}
			} else {
				// Shared defaults for both the strict and the user-bypass
				// paths; each is skipped when the user supplied that option
				// (keys are case-normalized in sshUserOptions).
				if !userOpts["hostname"] {
					execArgs = append(execArgs, "-o", fmt.Sprintf("HostName=%s", ip))
				}
				if !userOpts["port"] {
					execArgs = append(execArgs, "-o", "Port=22")
				}
				if !userOpts["userknownhostsfile"] {
					execArgs = append(execArgs, "-o", fmt.Sprintf("UserKnownHostsFile=%s", knownMgr.KnownHostsFile))
				}

				if hasHostKeyBypass {
					fmt.Fprintln(stderr, "WARNING: Host key verification disabled via -o flag")
					if !userOpts["stricthostkeychecking"] {
						// The user disabled verification via the known-hosts
						// file (UserKnownHostsFile=/dev/null) but left
						// StrictHostKeyChecking unset; strict checking against
						// an empty key store can never connect, so complete
						// the bypass they asked for — the warning above
						// already told them verification is disabled. Never
						// emit the strict default here: OpenSSH honors the
						// first occurrence.
						execArgs = append(execArgs, "-o", "StrictHostKeyChecking=no")
					}
				} else {
					if !userOpts["stricthostkeychecking"] {
						execArgs = append(execArgs, "-o", "StrictHostKeyChecking=yes")
					}
					if !isDryRun {
						if err := knownMgr.EnsureHostKeyRegisteredContext(ctx, name, ip, 22); err != nil {
							return &exitError{code: 6, err: fmt.Errorf("host key registration failed for %q (%s): %w", name, ip, err)}
						}
					}
					// UG5 B2: known_hosts entries are keyed by container name,
					// but OpenSSH only consults the typed name for the
					// known-hosts lookup when that name is DNS-resolvable;
					// otherwise it falls back to HostName (the IP) and the
					// registered key never matches. HostKeyAlias pins the
					// lookup to the container name so first-connect strict
					// verification works on hosts without per-container DNS.
					if !userOpts["hostkeyalias"] {
						execArgs = append(execArgs, "-o", fmt.Sprintf("HostKeyAlias=%s", name))
					}
				}
			}

			if identity != "" {
				execArgs = append(execArgs, "-o", "IdentitiesOnly=yes", "-i", identity)
			}

			execArgs = append(execArgs, fmt.Sprintf("%s@%s", targetUser, name))
			execArgs = append(execArgs, extraSSHArgs...)

			if isDryRun {
				fmt.Fprintf(stdout, "Dry-run: ssh %s\n", strings.Join(execArgs, " "))
				return nil
			}

			//nolint:gosec // G204: execArgs contains validated SSH parameters and target user/name
			sshCmd := exec.CommandContext(ctx, "ssh", execArgs...)
			sshCmd.Stdin = os.Stdin
			sshCmd.Stdout = stdout
			sshCmd.Stderr = stderr

			if err := sshCmd.Run(); err != nil {
				return &exitError{code: 6, err: fmt.Errorf("ssh session failed: %w", err)}
			}
			return nil
		},
	}
	return cmd
}

func newShellCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell <name>",
		Short: "Open an interactive shell in a container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.format == "json" {
				return &exitError{code: 2, err: fmt.Errorf("interactive command shell rejects --format json")}
			}
			svc, err := getSvc()
			if err != nil {
				return err
			}
			name := args[0]
			_, _, err = svc.GetInstance(name)
			if err != nil {
				return &exitError{code: 5, err: fmt.Errorf("container %q not found: %w", name, err)}
			}

			runAs := opts.runAs
			if runAs == "" {
				runAs = "root"
			}

			uid, err := svc.ResolveUID(name, runAs)
			if err != nil {
				return &exitError{code: 6, err: fmt.Errorf("resolving user %q in %q: %w", runAs, name, err)}
			}

			shellCmd := []string{"/bin/bash", "-l"}
			if err := svc.InteractiveExecInstance(name, shellCmd, uid, nil); err != nil {
				return &exitError{code: 6, err: fmt.Errorf("interactive shell failed: %w", err)}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.runAs, "run-as", "", "User to run shell as")
	return cmd
}

func newInitCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [target_dir]",
		Short: "Scaffold a new fleet directory with base config and template manifests",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetDir := "."
			if len(args) > 0 {
				targetDir = args[0]
			}

			if err := os.MkdirAll(targetDir, 0755); err != nil {
				return &exitError{code: 2, err: fmt.Errorf("creating directory %q: %w", targetDir, err)}
			}

			basePath := filepath.Join(targetDir, "_base.yaml")
			configDir := filepath.Join(targetDir, "config")
			devPath := filepath.Join(configDir, "dev.yaml")

			var existing []string
			if _, err := os.Stat(basePath); err == nil {
				existing = append(existing, basePath)
			}
			if _, err := os.Stat(devPath); err == nil {
				existing = append(existing, devPath)
			}

			if len(existing) > 0 && !opts.force {
				return &exitError{
					code: 2,
					err:  fmt.Errorf("target files already exist: %s (use --force to overwrite)", strings.Join(existing, ", ")),
				}
			}

			if err := os.MkdirAll(configDir, 0755); err != nil {
				return &exitError{code: 2, err: fmt.Errorf("creating directory %q: %w", configDir, err)}
			}
			if err := os.MkdirAll(filepath.Join(targetDir, ".lxm"), 0755); err != nil {
				return &exitError{code: 2, err: fmt.Errorf("creating directory .lxm: %w", err)}
			}

			baseContent := `schema: lxm/config/v2
base: true
user: ubuntu
wait:
  cloud_init: 10m
  network: 60s
`
			devContent := `schema: lxm/config/v2
include:
  - ../_base.yaml
name: dev-station
status: present
image: ubuntu:22.04
groups: [dev]
`
			//nolint:gosec // G306: starter manifest files are intended to be standard readable config files (0644)
			if err := os.WriteFile(basePath, []byte(baseContent), 0644); err != nil {
				return &exitError{code: 2, err: fmt.Errorf("writing _base.yaml: %w", err)}
			}
			//nolint:gosec // G306: starter manifest files are intended to be standard readable config files (0644)
			if err := os.WriteFile(devPath, []byte(devContent), 0644); err != nil {
				return &exitError{code: 2, err: fmt.Errorf("writing config/dev.yaml: %w", err)}
			}

			if opts.format == "text" {
				fmt.Fprintf(stdout, "Initialized lxm fleet in %s:\n  - %s\n  - %s\n", targetDir, basePath, devPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	return cmd
}

func newIncludeCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, logger *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "include <config_dir> <include_file>",
		Short: "Add an include directive to all configs in a directory",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}

func newCompileCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compile <target>",
		Short: "Emit resolved v2 manifests and migrate legacy v1 configs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			if _, statErr := os.Stat(target); statErr != nil {
				return &exitError{code: 5, err: fmt.Errorf("target %q not found: %w", target, statErr)}
			}

			configFiles, err := discoverYAMLFiles(target, opts.includeHidden, logger)
			if err != nil {
				return &exitError{code: 3, err: fmt.Errorf("discovering YAML files in target %q: %w", target, err)}
			}

			var compiled []string
			var totalWarnings []string

			for _, file := range configFiles {
				raw, err := os.ReadFile(file)
				if err != nil {
					return &exitError{code: 3, err: fmt.Errorf("reading manifest %q: %w", file, err)}
				}

				migratedBytes, warnings, err := config.MigrateManifest(raw)
				if err != nil {
					return &exitError{code: 3, err: fmt.Errorf("migrating manifest %q: %w", file, err)}
				}

				totalWarnings = append(totalWarnings, warnings...)

				savedPath, err := config.SaveMigratedFile(file, migratedBytes, opts.inPlace)
				if err != nil {
					return &exitError{code: 3, err: fmt.Errorf("saving compiled manifest %q: %w", file, err)}
				}
				compiled = append(compiled, savedPath)
			}

			if opts.format == "text" {
				for _, w := range totalWarnings {
					fmt.Fprintf(stderr, "Warning: %s\n", w)
				}
				fmt.Fprintf(stdout, "Successfully compiled %d manifest(s):\n", len(compiled))
				for _, p := range compiled {
					fmt.Fprintf(stdout, "  - %s\n", p)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.inPlace, "in-place", false, "Overwrite source YAML files in place")
	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	return cmd
}

func newDoctorCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor [target_dir]",
		Short: "Run fleet and host diagnostic checks",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetDir := "."
			if len(args) > 0 {
				targetDir = args[0]
			}

			var checks []string
			var warnings []string

			if !opts.skipRemote {
				svc, err := getSvc()
				if err != nil {
					return err
				}
				_ = svc
				checks = append(checks, "[OK] LXD socket reachable")
			} else {
				checks = append(checks, "[SKIP] Remote LXD socket check skipped")
			}

			// Check lxd group membership
			currentUser, uErr := user.Current()
			if uErr == nil {
				lxdGroup, gErr := user.LookupGroup("lxd")
				if gErr == nil {
					gids, _ := currentUser.GroupIds()
					inGroup := false
					for _, gid := range gids {
						if gid == lxdGroup.Gid {
							inGroup = true
							break
						}
					}
					if inGroup || currentUser.Uid == "0" {
						checks = append(checks, "[OK] lxd group membership")
					} else {
						warnings = append(warnings, fmt.Sprintf("User %s is not in 'lxd' group (gid %s)", currentUser.Username, lxdGroup.Gid))
						checks = append(checks, "[WARN] lxd group membership")
					}
				} else {
					checks = append(checks, "[SKIP] Group 'lxd' not found on host system")
				}
			} else {
				checks = append(checks, "[SKIP] Unable to inspect current user group membership")
			}

			// Check kernel idmapped mounts
			if _, err := os.Stat("/proc/self/uid_map"); err == nil {
				checks = append(checks, "[OK] Kernel idmapped mounts support")
			} else {
				warnings = append(warnings, "Kernel idmapped mounts support (/proc/self/uid_map) not detected")
				checks = append(checks, "[WARN] Kernel idmapped mounts support")
			}

			// Check /dev/kvm accessibility
			if file, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
				_ = file.Close()
				checks = append(checks, "[OK] KVM hardware virtualization (/dev/kvm accessible)")
			} else {
				warnings = append(warnings, "KVM hardware virtualization (/dev/kvm) not accessible; VMs will run without hardware acceleration or fail to launch")
				checks = append(checks, "[WARN] KVM hardware virtualization")
			}

			// Check un-migrated configs and sensitive path mounts
			configFiles, err := discoverYAMLFiles(targetDir, true, logger)
			if err == nil && len(configFiles) > 0 {
				unmigratedCount := 0
				for _, cf := range configFiles {
					probe, perr := probeManifestFile(cf)
					if perr != nil {
						// Not YAML we can classify; not an lxm manifest concern.
						continue
					}
					// Skip unrelated YAML (mkdocs.yml, Taskfile.yml, ...): lxm
					// manifests declare a schema or carry name/image/base.
					if probe.Schema == "" && probe.Name == "" && probe.Image == "" && !probe.Base {
						continue
					}
					if probe.Schema == "lxm/config/v2" {
						// Base files are intentionally not standalone-loadable
						// (no name) — never report them as un-migrated.
						if probe.Base {
							continue
						}
						if _, err := config.LoadConfig(cf); err != nil {
							unmigratedCount++
							warnings = append(warnings, fmt.Sprintf("Config %s fails to load: %v", cf, err))
						}
						continue
					}
					// v1 schema or legacy no-schema manifest → un-migrated.
					unmigratedCount++
					warnings = append(warnings, fmt.Sprintf("Un-migrated config (missing schema: lxm/config/v2): %s", cf))
				}
				if unmigratedCount == 0 {
					checks = append(checks, "[OK] All discovered configs migrated to lxm/config/v2")
				}
			}

			if opts.format == "text" {
				fmt.Fprintln(stdout, "Running lxm doctor diagnostic checks...")
				for _, c := range checks {
					fmt.Fprintln(stdout, c)
				}
				for _, w := range warnings {
					fmt.Fprintf(stderr, "Warning: %s\n", w)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.skipRemote, "skip-remote", false, "Skip remote checks")
	cmd.Flags().StringVar(&opts.format, "format", "text", "Output format (text|json)")
	return cmd
}

func fetchLiveSnapshots(svc lxd.InstanceService) (map[string]*plan.InstanceSnapshot, error) {
	instances, err := svc.ListInstances()
	if err != nil {
		return nil, err
	}

	result := make(map[string]*plan.InstanceSnapshot)
	for _, full := range instances {
		instName := full.Name
		inst, etag, err := svc.GetInstance(instName)
		if err != nil || inst == nil {
			inst = &full.Instance
			etag = ""
		}
		instType := inst.Type
		if instType == "" {
			instType = full.Type
		}
		if instType == "" {
			instType = "container"
		}
		result[instName] = &plan.InstanceSnapshot{
			Name:            inst.Name,
			Type:            instType,
			Status:          inst.Status,
			StatusCode:      int(inst.StatusCode),
			Architecture:    inst.Architecture,
			Config:          inst.Config,
			ExpandedConfig:  full.ExpandedConfig,
			Devices:         inst.Devices,
			ExpandedDevices: full.ExpandedDevices,
			Profiles:        inst.Profiles,
			Ephemeral:       inst.Ephemeral,
			ETag:            etag,
			HasSnapshots:    len(full.Snapshots) > 0,
		}
	}
	return result, nil
}

func computePlanSummary(steps []plan.Step) plan.PlanSummary {
	var s plan.PlanSummary
	for _, step := range steps {
		switch step.Action {
		case "create":
			s.Create++
		case "update":
			s.Update++
		case "recreate":
			s.Recreate++
		case "delete":
			s.Delete++
		case "start":
			s.Start++
		case "stop":
			s.Stop++
		case "noop":
			s.Noop++
		}
	}
	return s
}
