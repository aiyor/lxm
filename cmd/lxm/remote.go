package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/remote"
)

type remoteAddFlags struct {
	token    string
	provider string
	project  string
	insecure bool
}

func newRemoteCmd(opts *cmdOptions, ctx context.Context, stdout, stderr io.Writer, logger *slog.Logger) *cobra.Command {
	remoteCmd := &cobra.Command{
		Use:   "remote",
		Short: "Manage Incus and LXD remote endpoints and trust authentication",
		Long:  "Commands to add, list, remove, and configure remote Incus/LXD endpoints in ~/.config/lxm/remotes.yaml.",
	}

	remoteCmd.AddCommand(newRemoteListCmd(opts, stdout, stderr, logger))
	remoteCmd.AddCommand(newRemoteAddCmd(opts, stdout, stderr, logger))
	remoteCmd.AddCommand(newRemoteRemoveCmd(opts, stdout, stderr, logger))
	remoteCmd.AddCommand(newRemoteSetDefaultCmd(opts, stdout, stderr, logger))
	remoteCmd.AddCommand(newRemoteSetProjectCmd(opts, stdout, stderr, logger))

	return remoteCmd
}

func newRemoteListCmd(opts *cmdOptions, stdout, stderr io.Writer, logger *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List configured remote endpoints",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := remote.LoadConfig()
			if err != nil {
				return &exitError{code: 1, err: fmt.Errorf("loading remotes: %w", err)}
			}

			if opts.format == "json" {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(cfg)
			}

			w := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tADDRESS\tPROVIDER\tPROJECT\tDEFAULT")

			for name, r := range cfg.Remotes {
				isDefault := "NO"
				if name == cfg.DefaultRemote {
					isDefault = "YES"
				}
				prov := string(r.Provider)
				if prov == "" {
					prov = "auto"
				}
				proj := r.Project
				if proj == "" {
					proj = "default"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, r.Address, prov, proj, isDefault)
			}
			return w.Flush()
		},
	}
}

func newRemoteAddCmd(opts *cmdOptions, stdout, stderr io.Writer, logger *slog.Logger) *cobra.Command {
	var addFlags remoteAddFlags

	cmd := &cobra.Command{
		Use:   "add <name> <address>",
		Short: "Add a new remote server and enroll client trust certificate",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			addr := args[1]

			if !strings.HasPrefix(addr, "https://") && !strings.HasPrefix(addr, "unix://") {
				addr = "https://" + addr
			}

			provType := provider.ProviderType(addFlags.provider)
			if provType == "" {
				provType = provider.ProviderTypeIncus
			}
			if provType != provider.ProviderTypeIncus && provType != provider.ProviderTypeLXD && provType != provider.ProviderTypeAuto {
				return &exitError{code: 2, err: fmt.Errorf("invalid provider %q: must be 'incus' or 'lxd'", provType)}
			}

			cfg, err := remote.LoadConfig()
			if err != nil {
				return &exitError{code: 1, err: fmt.Errorf("loading remotes: %w", err)}
			}

			if _, exists := cfg.Remotes[name]; exists {
				return &exitError{code: 1, err: fmt.Errorf("remote %q already exists", name)}
			}

			var serverCert string
			var serverFp string

			if strings.HasPrefix(addr, "https://") {
				info, err := remote.FetchServerCertificate(addr)
				if err != nil {
					return &exitError{code: 4, err: fmt.Errorf("discovering server at %s: %w", addr, err)}
				}
				serverCert = info.CertPEM
				serverFp = info.Fingerprint
				logger.Info("Discovered server certificate", "fingerprint", serverFp)

				certPath, keyPath, err := remote.EnsureClientCertificate()
				if err != nil {
					return &exitError{code: 1, err: fmt.Errorf("ensuring client mTLS keypair: %w", err)}
				}

				if addFlags.token != "" {
					logger.Info("Enrolling client certificate with trust token...")
					if provType == provider.ProviderTypeLXD {
						if err := remote.EnrollTrustTokenLXD(addr, addFlags.token, certPath, keyPath, serverCert); err != nil {
							return &exitError{code: 4, err: fmt.Errorf("trust enrollment failed with LXD: %w", err)}
						}
					} else {
						if err := remote.EnrollTrustTokenIncus(addr, addFlags.token, certPath, keyPath, serverCert); err != nil {
							return &exitError{code: 4, err: fmt.Errorf("trust enrollment failed with Incus: %w", err)}
						}
					}
					logger.Info("Client certificate successfully trusted by server.")
				}
			}

			proj := addFlags.project
			if proj == "" {
				proj = "default"
			}

			cfg.Remotes[name] = remote.RemoteEntry{
				Address:           addr,
				Provider:          provType,
				Project:           proj,
				ServerCertificate: serverCert,
				ServerFingerprint: serverFp,
				Insecure:          addFlags.insecure,
			}

			if err := remote.SaveConfig(cfg); err != nil {
				return &exitError{code: 1, err: fmt.Errorf("saving remotes: %w", err)}
			}

			fmt.Fprintf(stdout, "Remote %q added successfully (%s).\n", name, addr)
			return nil
		},
	}

	cmd.Flags().StringVar(&addFlags.token, "token", "", "Trust token for remote server authentication")
	cmd.Flags().StringVar(&addFlags.provider, "provider", "incus", "Server provider type (incus, lxd)")
	cmd.Flags().StringVar(&addFlags.project, "project", "default", "Default project for this remote")
	cmd.Flags().BoolVar(&addFlags.insecure, "insecure", false, "Disable TLS certificate verification")

	return cmd
}

func newRemoteRemoveCmd(opts *cmdOptions, stdout, stderr io.Writer, logger *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a remote endpoint from config",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := remote.LoadConfig()
			if err != nil {
				return &exitError{code: 1, err: fmt.Errorf("loading remotes: %w", err)}
			}

			if _, exists := cfg.Remotes[name]; !exists {
				return &exitError{code: 5, err: fmt.Errorf("remote %q not found", name)}
			}

			delete(cfg.Remotes, name)
			if cfg.DefaultRemote == name {
				cfg.DefaultRemote = "local"
			}

			if err := remote.SaveConfig(cfg); err != nil {
				return &exitError{code: 1, err: fmt.Errorf("saving remotes: %w", err)}
			}

			fmt.Fprintf(stdout, "Remote %q removed.\n", name)
			return nil
		},
	}
}

func newRemoteSetDefaultCmd(opts *cmdOptions, stdout, stderr io.Writer, logger *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:     "set-default <name>",
		Aliases: []string{"switch"},
		Short:   "Set the default remote for CLI operations",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := remote.LoadConfig()
			if err != nil {
				return &exitError{code: 1, err: fmt.Errorf("loading remotes: %w", err)}
			}

			if _, exists := cfg.Remotes[name]; !exists {
				return &exitError{code: 5, err: fmt.Errorf("remote %q not found", name)}
			}

			cfg.DefaultRemote = name
			if err := remote.SaveConfig(cfg); err != nil {
				return &exitError{code: 1, err: fmt.Errorf("saving remotes: %w", err)}
			}

			fmt.Fprintf(stdout, "Default remote set to %q.\n", name)
			return nil
		},
	}
}

func newRemoteSetProjectCmd(opts *cmdOptions, stdout, stderr io.Writer, logger *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "set-project <remote> <project>",
		Short: "Set the default project for a remote endpoint",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			remName := args[0]
			proj := args[1]

			cfg, err := remote.LoadConfig()
			if err != nil {
				return &exitError{code: 1, err: fmt.Errorf("loading remotes: %w", err)}
			}

			entry, exists := cfg.Remotes[remName]
			if !exists {
				return &exitError{code: 5, err: fmt.Errorf("remote %q not found", remName)}
			}

			entry.Project = proj
			cfg.Remotes[remName] = entry

			if err := remote.SaveConfig(cfg); err != nil {
				return &exitError{code: 1, err: fmt.Errorf("saving remotes: %w", err)}
			}

			fmt.Fprintf(stdout, "Remote %q project set to %q.\n", remName, proj)
			return nil
		},
	}
}
