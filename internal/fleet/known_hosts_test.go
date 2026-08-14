package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "lxm_fleet_test_*")
	if err == nil {
		os.Setenv("LXM_KNOWN_HOSTS_FILE", filepath.Join(tmpDir, "known_hosts"))
		defer os.RemoveAll(tmpDir)
	}
	os.Exit(m.Run())
}

func TestKnownHostsManager_BasicOperations(t *testing.T) {
	tmpDir := t.TempDir()
	khFile := filepath.Join(tmpDir, "known_hosts")

	mgr := NewKnownHostsManager(khFile)

	// Initially host should not be registered
	reg, err := mgr.IsHostRegistered("test-container")
	if err != nil {
		t.Fatalf("IsHostRegistered failed: %v", err)
	}
	if reg {
		t.Errorf("expected host not registered initially")
	}

	// Purge on non-existent file should be a clean no-op
	if err := mgr.PurgeContainerKey("test-container"); err != nil {
		t.Fatalf("PurgeContainerKey on missing file failed: %v", err)
	}

	// Manually write an entry to test Purge and IsHostRegistered
	entry := "test-container ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI123456789012345678901234567890123456789012\n"
	if err := os.WriteFile(khFile, []byte(entry), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Host should now be registered
	reg, err = mgr.IsHostRegistered("test-container")
	if err != nil {
		t.Fatalf("IsHostRegistered failed: %v", err)
	}
	if !reg {
		t.Errorf("expected host to be registered")
	}

	// Purge host key using ssh-keygen -R
	if err := mgr.PurgeContainerKey("test-container"); err != nil {
		t.Fatalf("PurgeContainerKey failed: %v", err)
	}

	// Verify host is no longer registered
	reg, err = mgr.IsHostRegistered("test-container")
	if err != nil {
		t.Fatalf("IsHostRegistered after purge failed: %v", err)
	}
	if reg {
		t.Errorf("expected host to be purged")
	}
}

func TestKnownHostsManager_Concurrency(t *testing.T) {
	tmpDir := t.TempDir()
	khFile := filepath.Join(tmpDir, "known_hosts")

	mgr := NewKnownHostsManager(khFile)

	// Pre-populate shared file with host key entries
	var initial []string
	for i := 0; i < 10; i++ {
		initial = append(initial, fmt.Sprintf("container-%d ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI12345678901234567890123456789012345678901%d\n", i, i))
	}
	if err := os.WriteFile(khFile, []byte(strings.Join(initial, "")), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var wg sync.WaitGroup
	workers := 20

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("container-%d", idx%10)
			if idx%2 == 0 {
				_ = mgr.PurgeContainerKey(name)
			} else {
				_ = mgr.withLock(func() error {
					f, err := os.OpenFile(khFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
					if err != nil {
						return err
					}
					defer f.Close()
					_, err = fmt.Fprintf(f, "%s ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI12345678901234567890123456789012345678901%d\n", name, idx)
					return err
				})
			}
		}(i)
	}

	wg.Wait()

	// Assert file integrity and parseability
	content, err := os.ReadFile(khFile)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadFile after concurrent operations failed: %v", err)
	}

	if len(content) > 0 {
		lines := strings.Split(strings.TrimSpace(string(content)), "\n")
		for lineIdx, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 3 {
				t.Errorf("line %d is corrupted: %q", lineIdx, line)
			}
		}
	}
}

func TestKnownHostsManager_EnsureRegisteredWhenAlreadyPresent(t *testing.T) {
	tmpDir := t.TempDir()
	khFile := filepath.Join(tmpDir, "known_hosts")

	t.Setenv("LXM_KNOWN_HOSTS_FILE", khFile)
	mgr := DefaultKnownHostsManager()

	entry := "test-container ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI123456789012345678901234567890123456789012\n"
	if err := os.WriteFile(khFile, []byte(entry), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Should short-circuit since test-container is already in known_hosts
	if err := mgr.EnsureHostKeyRegistered("test-container", "127.0.0.1", 22); err != nil {
		t.Errorf("expected EnsureHostKeyRegistered to succeed for registered host, got %v", err)
	}
}

func TestKnownHostsManager_EnsureRegisteredFailsOnClosedPort(t *testing.T) {
	tmpDir := t.TempDir()
	khFile := filepath.Join(tmpDir, "known_hosts")

	mgr := NewKnownHostsManager(khFile)

	// keyscan to 127.0.0.1 on closed port should fail or return no keys
	err := mgr.EnsureHostKeyRegistered("new-container", "127.0.0.1", 65534)
	if err == nil {
		t.Errorf("expected EnsureHostKeyRegistered to fail on closed port")
	}
}
