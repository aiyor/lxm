package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aiyor/lxm/internal/config"
)

func TestProviderConfig_AuthoringAndResolved(t *testing.T) {
	manifest := `
schema: lxm/config/v2
name: test-incus-vm
type: vm
image: images:ubuntu/24.04
provider: incus
remote: lab-node1
target: incus-node2
project: staging
remotes:
  lab-node1:
    address: https://10.171.13.50:8443
    provider: incus
    project: staging
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0644); err != nil {
		t.Fatalf("writing temp manifest: %v", err)
	}

	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if conf.Provider != "incus" {
		t.Errorf("expected provider 'incus', got %q", conf.Provider)
	}
	if conf.Remote != "lab-node1" {
		t.Errorf("expected remote 'lab-node1', got %q", conf.Remote)
	}
	if conf.Target != "incus-node2" {
		t.Errorf("expected target 'incus-node2', got %q", conf.Target)
	}
	if conf.Project != "staging" {
		t.Errorf("expected project 'staging', got %q", conf.Project)
	}
	if len(conf.Remotes) != 1 {
		t.Fatalf("expected 1 remote, got %d", len(conf.Remotes))
	}
	rem, ok := conf.Remotes["lab-node1"]
	if !ok || rem.Address != "https://10.171.13.50:8443" || rem.Provider != "incus" {
		t.Errorf("unexpected remotes entry: %+v", rem)
	}
}

func TestProviderConfig_InheritanceAndMerge(t *testing.T) {
	baseManifest := `
schema: lxm/config/v2
base: true
provider: incus
remote: default-remote
project: dev
remotes:
  default-remote:
    address: https://10.171.13.50:8443
`
	overlayManifest := `
schema: lxm/config/v2
name: child-node
type: container
image: images:ubuntu/24.04
include:
  - ./base.yaml
target: incus-node2
project: prod
remotes:
  extra-remote:
    address: https://10.171.13.51:8443
`

	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "base.yaml")
	overlayPath := filepath.Join(tmpDir, "overlay.yaml")

	if err := os.WriteFile(basePath, []byte(baseManifest), 0644); err != nil {
		t.Fatalf("writing base: %v", err)
	}
	if err := os.WriteFile(overlayPath, []byte(overlayManifest), 0644); err != nil {
		t.Fatalf("writing overlay: %v", err)
	}

	conf, err := config.LoadConfig(overlayPath)
	if err != nil {
		t.Fatalf("LoadConfig overlay failed: %v", err)
	}

	if conf.Provider != "incus" {
		t.Errorf("expected inherited provider 'incus', got %q", conf.Provider)
	}
	if conf.Remote != "default-remote" {
		t.Errorf("expected inherited remote 'default-remote', got %q", conf.Remote)
	}
	if conf.Target != "incus-node2" {
		t.Errorf("expected overlay target 'incus-node2', got %q", conf.Target)
	}
	if conf.Project != "prod" {
		t.Errorf("expected overlay project 'prod', got %q", conf.Project)
	}
	if len(conf.Remotes) != 2 {
		t.Errorf("expected merged 2 remotes, got %d", len(conf.Remotes))
	}
}
