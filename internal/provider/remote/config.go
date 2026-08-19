package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"gopkg.in/yaml.v3"

	"github.com/aiyor/lxm/internal/provider"
)

// Config represents ~/.config/lxm/remotes.yaml.
type Config struct {
	DefaultRemote string                 `yaml:"default_remote,omitempty" json:"default_remote,omitempty"`
	Remotes       map[string]RemoteEntry `yaml:"remotes" json:"remotes"`
}

// RemoteEntry represents a single remote endpoint configuration.
type RemoteEntry struct {
	Address           string                `yaml:"address" json:"address"`
	Provider          provider.ProviderType `yaml:"provider,omitempty" json:"provider,omitempty"`
	Project           string                `yaml:"project,omitempty" json:"project,omitempty"`
	Protocol          string                `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	ServerCertificate string                `yaml:"server_certificate,omitempty" json:"server_certificate,omitempty"`
	ServerFingerprint string                `yaml:"server_fingerprint,omitempty" json:"server_fingerprint,omitempty"`
	ClientCertificate string                `yaml:"client_certificate,omitempty" json:"client_certificate,omitempty"`
	ClientKey         string                `yaml:"client_key,omitempty" json:"client_key,omitempty"`
	Insecure          bool                  `yaml:"insecure,omitempty" json:"insecure,omitempty"`
}

// DefaultConfigDir returns ~/.config/lxm or $LXM_CONFIG_DIR.
func DefaultConfigDir() (string, error) {
	if dir := os.Getenv("LXM_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving user home directory: %w", err)
	}
	return filepath.Join(home, ".config", "lxm"), nil
}

// DefaultRemotesFile returns ~/.config/lxm/remotes.yaml.
func DefaultRemotesFile() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "remotes.yaml"), nil
}

// LoadConfig reads the remotes configuration with advisory lock protection.
func LoadConfig() (*Config, error) {
	path, err := DefaultRemotesFile()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &Config{
			DefaultRemote: "local",
			Remotes: map[string]RemoteEntry{
				"local": {
					Address:  "unix:///var/lib/incus/unix.socket",
					Provider: provider.ProviderTypeAuto,
					Project:  "default",
					Protocol: "unix",
				},
			},
		}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading remotes config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing remotes config %q: %w", path, err)
	}
	if cfg.Remotes == nil {
		cfg.Remotes = make(map[string]RemoteEntry)
	}
	return &cfg, nil
}

// SaveConfig persists the remotes configuration with advisory locking.
func SaveConfig(cfg *Config) error {
	dir, err := DefaultConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory %q: %w", dir, err)
	}

	lockFile := filepath.Join(dir, "remotes.lock")
	lockFd, err := syscall.Open(lockFile, syscall.O_CREAT|syscall.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening lock file %q: %w", lockFile, err)
	}
	defer syscall.Close(lockFd)

	if err := syscall.Flock(lockFd, syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring lock on %q: %w", lockFile, err)
	}
	defer syscall.Flock(lockFd, syscall.LOCK_UN)

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling remotes config: %w", err)
	}

	path := filepath.Join(dir, "remotes.yaml")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("writing temp config %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("committing remotes config %q: %w", path, err)
	}

	return nil
}
