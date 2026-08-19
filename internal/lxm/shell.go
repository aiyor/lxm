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
		instance, _, err := m.client.GetInstance(ctx, containerName)
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

	// If the official CLI is available, we can delegate or use driver InteractiveExecInstance
	if lxcPath, err := exec.LookPath("lxc"); err == nil {
		m.logger.Debug("Delegating to native CLI for optimal TUI experience")
		cmd := exec.CommandContext(ctx, lxcPath, "exec", containerName, "--", "su", "-l", user)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	uenv, err := m.client.ResolveUserEnv(ctx, containerName, user)
	if err != nil {
		return fmt.Errorf("resolving user %q environment: %w", user, err)
	}

	env := uenv.DefaultEnv()

	cmd := []string{uenv.Shell, "-l"}
	return m.client.InteractiveExecInstance(ctx, containerName, cmd, uenv.UID, env)
}
