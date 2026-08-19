package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/aiyor/lxm/internal/config"
	"github.com/spf13/cobra"
)

// newDiskCmd constructs the `lxm disk` command and its subcommands.
func newDiskCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	var poolFilter string

	cmd := &cobra.Command{
		Use:   "disk <command>",
		Short: "Manage storage disks and custom volumes",
	}

	gcCmd := &cobra.Command{
		Use:     "gc [file|dir]",
		Aliases: []string{"prune"},
		Short:   "Garbage collect orphaned managed storage volumes",
		Long: "Scans for custom storage volumes carrying the user.lxm.managed marker\n" +
			"that are not referenced by any disk declared in the loaded manifests.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "."
			if len(args) > 0 {
				target = args[0]
			}

			svc, err := getSvc()
			if err != nil {
				return err
			}

			// 1. Discover manifests to determine referenced volumes.
			// Safety: Must fail closed if any manifest fails to load.
			configFiles, err := discoverYAMLFiles(target, opts.includeHidden, logger)
			if err != nil {
				return &exitError{code: 5, err: fmt.Errorf("discovering manifests in %q: %w", target, err)}
			}

			referencedVols := make(map[string]bool)
			for _, f := range configFiles {
				conf, err := config.LoadConfig(f)
				if err != nil {
					return &exitError{code: 3, err: fmt.Errorf("loading manifest %q: %w", f, err)}
				}
				for _, d := range conf.Disks {
					if d.Status != "absent" && d.Source != "" {
						pool := d.Pool
						if pool == "" {
							pool = "default"
						}
						referencedVols[pool+"/"+d.Source] = true
					}
				}
			}

			// 2. Discover pools across the server.
			var pools []string
			if poolFilter != "" {
				pools = []string{poolFilter}
			} else {
				allPools, err := svc.GetStoragePoolNames(ctx)
				if err != nil {
					return &exitError{code: 4, err: fmt.Errorf("listing storage pools: %w", err)}
				}
				pools = allPools
			}

			type orphanVol struct {
				pool     string
				name     string
				instance string
				disk     string
				size     string
			}

			var orphans []orphanVol

			for _, pool := range pools {
				vols, err := svc.GetStoragePoolVolumes(ctx, pool)
				if err != nil {
					continue
				}
				for _, v := range vols {
					if v.Type != "custom" {
						continue
					}
					if v.Config == nil || v.Config["user.lxm.managed"] != "true" {
						continue // foreign volume: safe guard
					}
					volKey := pool + "/" + v.Name
					if !referencedVols[volKey] {
						orphans = append(orphans, orphanVol{
							pool:     pool,
							name:     v.Name,
							instance: v.Config["user.lxm.instance"],
							disk:     v.Config["user.lxm.disk"],
							size:     v.Config["size"],
						})
					}
				}
			}

			if len(orphans) == 0 {
				fmt.Fprintln(stdout, "No orphaned managed storage volumes found.")
				return nil
			}

			// 3. Print preview table
			fmt.Fprintf(stdout, "ORPHANED MANAGED STORAGE VOLUMES (%d):\n", len(orphans))
			fmt.Fprintf(stdout, "%-12s %-24s %-16s %-12s %-10s %s\n", "POOL", "VOLUME", "CREATOR INSTANCE", "DISK NAME", "SIZE", "STATUS")
			for _, o := range orphans {
				inst := o.instance
				if inst == "" {
					inst = "-"
				}
				disk := o.disk
				if disk == "" {
					disk = "-"
				}
				size := o.size
				if size == "" {
					size = "-"
				}
				fmt.Fprintf(stdout, "%-12s %-24s %-16s %-12s %-10s %s\n", o.pool, o.name, inst, disk, size, "Unreferenced")
			}

			if opts.dryRun {
				fmt.Fprintln(stdout, "\n[dry-run] No volumes were deleted.")
				return nil
			}

			if !opts.force {
				fmt.Fprintf(stdout, "\nAre you sure you want to permanently delete %d volume(s)? [y/N]: ", len(orphans))
				reader := bufio.NewReader(cmd.InOrStdin())
				input, _ := reader.ReadString('\n')
				input = strings.TrimSpace(strings.ToLower(input))
				if input != "y" && input != "yes" {
					fmt.Fprintln(stdout, "Operation aborted.")
					return nil
				}
			}

			deleted := 0
			for _, o := range orphans {
				err := svc.DeleteStoragePoolVolume(ctx, o.pool, "custom", o.name)
				if err != nil {
					fmt.Fprintf(stderr, "Error deleting volume %s/%s: %v\n", o.pool, o.name, err)
				} else {
					deleted++
				}
			}

			fmt.Fprintf(stdout, "Successfully deleted %d volume(s).\n", deleted)
			return nil
		},
	}

	gcCmd.Flags().StringVar(&poolFilter, "pool", "", "Filter to a specific storage pool")
	cmd.AddCommand(gcCmd)
	return cmd
}

// newVSwitchCmd constructs the `lxm vswitch` command and its subcommands.
func newVSwitchCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, getSvc serviceGetter, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vswitch <command>",
		Short: "Manage virtual switches and network ACLs",
	}

	gcCmd := &cobra.Command{
		Use:     "gc [file|dir]",
		Aliases: []string{"prune"},
		Short:   "Garbage collect orphaned managed network ACLs",
		Long: "Scans for network ACLs carrying the user.lxm.managed marker\n" +
			"that do not reference any declared virtual switch in the loaded manifests.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "."
			if len(args) > 0 {
				target = args[0]
			}

			svc, err := getSvc()
			if err != nil {
				return err
			}

			// 1. Discover manifests to determine referenced vswitches.
			// Safety: Must fail closed if any manifest fails to load.
			configFiles, err := discoverYAMLFiles(target, opts.includeHidden, logger)
			if err != nil {
				return &exitError{code: 5, err: fmt.Errorf("discovering manifests in %q: %w", target, err)}
			}

			referencedACLs := make(map[string]bool)
			for _, f := range configFiles {
				conf, err := config.LoadConfig(f)
				if err != nil {
					return &exitError{code: 3, err: fmt.Errorf("loading manifest %q: %w", f, err)}
				}
				for _, vs := range conf.VSwitches {
					if vs.Status != "absent" {
						referencedACLs["lxm-"+vs.Name] = true
					}
				}
			}

			acls, err := svc.GetNetworkACLs(ctx)
			if err != nil {
				return &exitError{code: 4, err: fmt.Errorf("listing network ACLs: %w", err)}
			}

			type orphanACL struct {
				name        string
				description string
			}

			var orphans []orphanACL
			for _, a := range acls {
				if a.Config == nil || a.Config["user.lxm.managed"] != "true" {
					continue // foreign ACL: safe guard
				}
				if !referencedACLs[a.Name] {
					orphans = append(orphans, orphanACL{
						name:        a.Name,
						description: a.Description,
					})
				}
			}

			if len(orphans) == 0 {
				fmt.Fprintln(stdout, "No orphaned managed network ACLs found.")
				return nil
			}

			fmt.Fprintf(stdout, "ORPHANED MANAGED NETWORK ACLS (%d):\n", len(orphans))
			fmt.Fprintf(stdout, "%-24s %-40s %s\n", "ACL NAME", "DESCRIPTION", "STATUS")
			for _, o := range orphans {
				desc := o.description
				if len(desc) > 38 {
					desc = desc[:35] + "..."
				}
				fmt.Fprintf(stdout, "%-24s %-40s %s\n", o.name, desc, "Unreferenced")
			}

			if opts.dryRun {
				fmt.Fprintln(stdout, "\n[dry-run] No network ACLs were deleted.")
				return nil
			}

			if !opts.force {
				fmt.Fprintf(stdout, "\nAre you sure you want to permanently delete %d network ACL(s)? [y/N]: ", len(orphans))
				reader := bufio.NewReader(cmd.InOrStdin())
				input, _ := reader.ReadString('\n')
				input = strings.TrimSpace(strings.ToLower(input))
				if input != "y" && input != "yes" {
					fmt.Fprintln(stdout, "Operation aborted.")
					return nil
				}
			}

			deleted := 0
			for _, o := range orphans {
				err := svc.DeleteNetworkACL(ctx, o.name)
				if err != nil {
					fmt.Fprintf(stderr, "Error deleting ACL %s: %v\n", o.name, err)
				} else {
					deleted++
				}
			}

			fmt.Fprintf(stdout, "Successfully deleted %d network ACL(s).\n", deleted)
			return nil
		},
	}

	cmd.AddCommand(gcCmd)
	return cmd
}
