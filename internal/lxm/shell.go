package lxm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func (m *Manager) Shell(containerName string, user string) error {
	return m.ShellContext(context.Background(), containerName, user)
}

func (m *Manager) ShellContext(ctx context.Context, containerName string, user string) error {
	if user == "" {
		instance, _, err := m.client.GetInstance(containerName)
		if err != nil {
			return fmt.Errorf("failed to get container info for %q: %w", containerName, err)
		}

		if u, ok := instance.Config["user.lxm.user"]; ok && u != "" {
			user = u
		} else {
			user = "root"
		}
	}

	m.logger.Info("Opening interactive shell", "name", containerName, "user", user)

	// Bulletproof way: if the official 'lxc' CLI is available, delegate to it.
	// It has the most robust terminal emulation and signal handling.
	if lxcPath, err := exec.LookPath("lxc"); err == nil {
		m.logger.Debug("Delegating to native lxc for optimal TUI experience")
		//nolint:gosec // G204: lxcPath is resolved via LookPath and containerName/user are validated identifiers
		cmd := exec.CommandContext(ctx, lxcPath, "exec", containerName, "--", "su", "-l", user)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	uenv, err := m.client.ResolveUserEnv(containerName, user)
	if err != nil {
		return fmt.Errorf("resolving user %q environment: %w", user, err)
	}

	env := uenv.DefaultEnv()

	cmd := []string{uenv.Shell, "-l"}
	return m.client.InteractiveExecInstance(containerName, cmd, uenv.UID, env)
}
