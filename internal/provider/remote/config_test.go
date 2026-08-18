package remote_test

import (
	"os"
	"testing"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/remote"
)

func TestRemoteConfig_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LXM_CONFIG_DIR", tmpDir)

	cfg, err := remote.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.DefaultRemote != "local" {
		t.Errorf("expected default remote 'local', got %q", cfg.DefaultRemote)
	}

	cfg.DefaultRemote = "lab-node1"
	cfg.Remotes["lab-node1"] = remote.RemoteEntry{
		Address:           "https://10.171.13.50:8443",
		Provider:          provider.ProviderTypeIncus,
		Project:           "default",
		ServerFingerprint: "abcd1234efgh5678",
	}

	if err := remote.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	loaded, err := remote.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig after save failed: %v", err)
	}

	if loaded.DefaultRemote != "lab-node1" {
		t.Errorf("expected default remote 'lab-node1', got %q", loaded.DefaultRemote)
	}
	entry, ok := loaded.Remotes["lab-node1"]
	if !ok {
		t.Fatalf("expected remote 'lab-node1' in config")
	}
	if entry.Address != "https://10.171.13.50:8443" || entry.Provider != provider.ProviderTypeIncus {
		t.Errorf("unexpected remote entry: %+v", entry)
	}
}

func TestEnsureClientCertificate(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LXM_CONFIG_DIR", tmpDir)

	certPath, keyPath, err := remote.EnsureClientCertificate()
	if err != nil {
		t.Fatalf("EnsureClientCertificate failed: %v", err)
	}

	if _, err := os.Stat(certPath); err != nil {
		t.Errorf("expected cert file to exist at %q: %v", certPath, err)
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Errorf("expected key file to exist at %q: %v", keyPath, err)
	} else if keyInfo.Mode().Perm() != 0600 {
		t.Errorf("expected key permissions 0600, got %o", keyInfo.Mode().Perm())
	}

	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("reading cert: %v", err)
	}

	fp, err := remote.FingerprintPEM(certBytes)
	if err != nil {
		t.Fatalf("FingerprintPEM failed: %v", err)
	}
	if len(fp) != 64 {
		t.Errorf("expected 64-char SHA256 hex string, got %q (len %d)", fp, len(fp))
	}

	// Calling again should be idempotent and return existing paths
	c2, k2, err := remote.EnsureClientCertificate()
	if err != nil || c2 != certPath || k2 != keyPath {
		t.Fatalf("idempotent EnsureClientCertificate returned unexpected: %q, %q, %v", c2, k2, err)
	}
}
