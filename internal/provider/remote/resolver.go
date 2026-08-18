package remote

import (
	"fmt"
	"os"

	lxd_client "github.com/canonical/lxd/client"
	incus_client "github.com/lxc/incus/v7/client"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/incus"
	"github.com/aiyor/lxm/internal/provider/lxd"
)

// ResolveOptions encapsulates inputs for resolving a target provider Driver.
type ResolveOptions struct {
	RemoteName string
	Provider   provider.ProviderType
	Project    string
	TargetNode string
	SocketPath string
}

// DetectLocalProvider probes host sockets to determine if Incus or LXD is running locally.
func DetectLocalProvider() provider.ProviderType {
	if prov := os.Getenv("LXM_PROVIDER"); prov != "" {
		return provider.ProviderType(prov)
	}

	if socket := os.Getenv("INCUS_SOCKET"); socket != "" {
		if _, err := os.Stat(socket); err == nil {
			return provider.ProviderTypeIncus
		}
	}
	if dir := os.Getenv("INCUS_DIR"); dir != "" {
		if _, err := os.Stat(dir + "/unix.socket"); err == nil {
			return provider.ProviderTypeIncus
		}
	}
	incusCandidates := []string{
		"/run/incus/unix.socket",
		"/var/lib/incus/unix.socket",
	}
	for _, c := range incusCandidates {
		if _, err := os.Stat(c); err == nil {
			return provider.ProviderTypeIncus
		}
	}

	if socket := os.Getenv("LXD_SOCKET"); socket != "" {
		if _, err := os.Stat(socket); err == nil {
			return provider.ProviderTypeLXD
		}
	}
	lxdCandidates := []string{
		"/var/snap/lxd/common/lxd/unix.socket",
		"/var/lib/lxd/unix.socket",
	}
	for _, c := range lxdCandidates {
		if _, err := os.Stat(c); err == nil {
			return provider.ProviderTypeLXD
		}
	}

	return provider.ProviderTypeIncus // Default modern preference
}

// ResolveDriver resolves and connects to the appropriate provider Driver based on options and config.
func ResolveDriver(opts ResolveOptions) (provider.Driver, error) {
	remoteName := opts.RemoteName
	if remoteName == "" {
		remoteName = os.Getenv("LXM_REMOTE")
	}

	cfg, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading remotes config: %w", err)
	}

	if remoteName == "" {
		if cfg.DefaultRemote != "" {
			remoteName = cfg.DefaultRemote
		} else {
			remoteName = "local"
		}
	}

	var d provider.Driver
	if remoteName == "local" {
		prov := opts.Provider
		if prov == "" || prov == provider.ProviderTypeAuto {
			prov = DetectLocalProvider()
		}

		switch prov {
		case provider.ProviderTypeIncus:
			d, err = incus.NewUnixDriver(opts.SocketPath)
			if err != nil {
				return nil, fmt.Errorf("connecting to local Incus daemon: %w", err)
			}
		case provider.ProviderTypeLXD:
			d, err = lxd.NewUnixDriver(opts.SocketPath)
			if err != nil {
				return nil, fmt.Errorf("connecting to local LXD daemon: %w", err)
			}
		default:
			return nil, fmt.Errorf("unsupported provider type %q", prov)
		}
	} else {
		entry, ok := cfg.Remotes[remoteName]
		if !ok {
			return nil, fmt.Errorf("remote %q not found in remotes configuration", remoteName)
		}

		prov := opts.Provider
		if prov == "" || prov == provider.ProviderTypeAuto {
			prov = entry.Provider
		}
		if prov == "" || prov == provider.ProviderTypeAuto {
			prov = provider.ProviderTypeIncus // default to Incus for remotes
		}

		certPath, keyPath, err := EnsureClientCertificate()
		if err != nil {
			return nil, fmt.Errorf("ensuring client certificates: %w", err)
		}

		clientCertPEM, err := os.ReadFile(certPath)
		if err != nil {
			return nil, fmt.Errorf("reading client certificate: %w", err)
		}
		clientKeyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("reading client key: %w", err)
		}

		switch prov {
		case provider.ProviderTypeIncus:
			args := &incus_client.ConnectionArgs{
				TLSClientCert:      string(clientCertPEM),
				TLSClientKey:       string(clientKeyPEM),
				TLSServerCert:      entry.ServerCertificate,
				InsecureSkipVerify: entry.Insecure,
			}
			d, err = incus.NewRemoteDriver(entry.Address, args)
			if err != nil {
				return nil, fmt.Errorf("connecting to remote Incus at %s: %w", entry.Address, err)
			}
		case provider.ProviderTypeLXD:
			args := &lxd_client.ConnectionArgs{
				TLSClientCert:      string(clientCertPEM),
				TLSClientKey:       string(clientKeyPEM),
				TLSServerCert:      entry.ServerCertificate,
				InsecureSkipVerify: entry.Insecure,
			}
			d, err = lxd.NewRemoteDriver(entry.Address, args)
			if err != nil {
				return nil, fmt.Errorf("connecting to remote LXD at %s: %w", entry.Address, err)
			}
		default:
			return nil, fmt.Errorf("unsupported provider type %q for remote %q", prov, remoteName)
		}

		if opts.Project == "" && entry.Project != "" {
			opts.Project = entry.Project
		}
	}

	if opts.Project != "" && opts.Project != "default" {
		d = d.UseProject(opts.Project)
	}

	if opts.TargetNode != "" {
		d = d.UseTarget(opts.TargetNode)
	}

	return d, nil
}
