package fleet

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// KnownHostsManager manages tool-managed known_hosts file under advisory file lock serialization.
type KnownHostsManager struct {
	KnownHostsFile string
	LockFile       string
}

// NewKnownHostsManager creates a KnownHostsManager instance.
// If customPath is empty, defaults to ~/.config/lxm/known_hosts (or LXM_KNOWN_HOSTS_FILE env if set).
func NewKnownHostsManager(customPath string) *KnownHostsManager {
	if customPath == "" {
		if envPath := os.Getenv("LXM_KNOWN_HOSTS_FILE"); envPath != "" {
			customPath = envPath
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				home = "/tmp"
			}
			customPath = filepath.Join(home, ".config", "lxm", "known_hosts")
		}
	}
	return &KnownHostsManager{
		KnownHostsFile: customPath,
		LockFile:       customPath + ".lock",
	}
}

// DefaultKnownHostsManager returns a KnownHostsManager using the default file location.
func DefaultKnownHostsManager() *KnownHostsManager {
	return NewKnownHostsManager("")
}

// withLock executes fn while holding an exclusive advisory file lock on LockFile.
func (m *KnownHostsManager) withLock(fn func() error) error {
	dir := filepath.Dir(m.KnownHostsFile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating known_hosts dir: %w", err)
	}

	lockFile, err := os.OpenFile(m.LockFile, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	return fn()
}

// PurgeContainerKeyContext removes host key entries for containerName using ssh-keygen -R under lock.
func (m *KnownHostsManager) PurgeContainerKeyContext(ctx context.Context, containerName string) error {
	return m.withLock(func() error {
		if _, err := os.Stat(m.KnownHostsFile); os.IsNotExist(err) {
			return nil
		}

		cmd := exec.CommandContext(ctx, "ssh-keygen", "-R", containerName, "-f", m.KnownHostsFile)
		out, err := cmd.CombinedOutput()
		if err != nil {
			outStr := string(out)
			if strings.Contains(outStr, "not found") || strings.Contains(outStr, "No such file") {
				return nil
			}
			return fmt.Errorf("ssh-keygen -R failed: %s: %w", outStr, err)
		}

		// Remove backup file created by ssh-keygen (known_hosts.old)
		oldFile := m.KnownHostsFile + ".old"
		_ = os.Remove(oldFile)

		return nil
	})
}

// PurgeContainerKey removes host key entries for containerName using ssh-keygen -R under lock.
func (m *KnownHostsManager) PurgeContainerKey(containerName string) error {
	return m.PurgeContainerKeyContext(context.Background(), containerName)
}

// IsHostRegisteredContext checks if containerName is present in known_hosts file using ssh-keygen -F.
func (m *KnownHostsManager) IsHostRegisteredContext(ctx context.Context, containerName string) (bool, error) {
	var registered bool
	err := m.withLock(func() error {
		if _, err := os.Stat(m.KnownHostsFile); os.IsNotExist(err) {
			registered = false
			return nil
		}

		cmd := exec.CommandContext(ctx, "ssh-keygen", "-F", containerName, "-f", m.KnownHostsFile)
		err := cmd.Run()
		registered = (err == nil)
		return nil
	})
	return registered, err
}

// IsHostRegistered checks if containerName is present in known_hosts file using ssh-keygen -F.
func (m *KnownHostsManager) IsHostRegistered(containerName string) (bool, error) {
	return m.IsHostRegisteredContext(context.Background(), containerName)
}

// EnsureHostKeyRegisteredContext verifies if containerName is in known_hosts; if absent, runs ssh-keyscan and appends under lock.
func (m *KnownHostsManager) EnsureHostKeyRegisteredContext(ctx context.Context, containerName string, ip string, port int) error {
	if port <= 0 {
		port = 22
	}

	return m.withLock(func() error {
		// Check if already registered
		if _, err := os.Stat(m.KnownHostsFile); err == nil {
			cmd := exec.CommandContext(ctx, "ssh-keygen", "-F", containerName, "-f", m.KnownHostsFile)
			if err := cmd.Run(); err == nil {
				// Host is already registered
				return nil
			}
		}

		// Run ssh-keyscan with 5-second timeout (-T 5)
		scanCmd := exec.CommandContext(ctx, "ssh-keyscan", "-T", "5", "-p", fmt.Sprintf("%d", port), ip)
		var outBuf, errBuf bytes.Buffer
		scanCmd.Stdout = &outBuf
		scanCmd.Stderr = &errBuf

		if err := scanCmd.Run(); err != nil {
			return fmt.Errorf("ssh-keyscan on %s (%s) failed: %s: %w", containerName, ip, errBuf.String(), err)
		}

		lines := strings.Split(outBuf.String(), "\n")
		var entries []string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				entry := fmt.Sprintf("%s %s %s", containerName, fields[1], fields[2])
				entries = append(entries, entry)
			}
		}

		if len(entries) == 0 {
			return fmt.Errorf("ssh-keyscan returned no keys for %s (%s)", containerName, ip)
		}

		f, err := os.OpenFile(m.KnownHostsFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return fmt.Errorf("opening known_hosts file: %w", err)
		}
		defer f.Close()

		for _, entry := range entries {
			if _, err := f.WriteString(entry + "\n"); err != nil {
				return fmt.Errorf("writing to known_hosts: %w", err)
			}
		}

		return nil
	})
}

// EnsureHostKeyRegistered verifies if containerName is in known_hosts; if absent, runs ssh-keyscan and appends under lock.
func (m *KnownHostsManager) EnsureHostKeyRegistered(containerName string, ip string, port int) error {
	return m.EnsureHostKeyRegisteredContext(context.Background(), containerName, ip, port)
}
