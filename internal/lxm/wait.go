package lxm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (m *Manager) WaitForCloudInit(ctx context.Context, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pollInterval := 5 * time.Second
	m.logger.Info("Waiting for cloud-init to finish", "name", name)

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for cloud-init on %q (%v)", name, timeout)
		default:
			res, err := m.client.ExecInstance(name, []string{"cloud-init", "status", "--long"}, 0, nil)
			if err != nil {
				time.Sleep(pollInterval)
				continue
			}

			exitCode, output := res.ExitCode, res.Combined()
			if strings.Contains(output, "status: error") {
				return fmt.Errorf("cloud-init failed on %q: %s", name, output)
			}

			if exitCode == 0 && strings.Contains(output, "status: done") {
				m.logger.Info("Cloud-init completed", "name", name)
				return m.WaitForNetwork(ctx, name, 60*time.Second)
			}
			time.Sleep(pollInterval)
		}
	}
}

// WaitForNetwork polls until the container has network connectivity.
func (m *Manager) WaitForNetwork(ctx context.Context, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pollInterval := 2 * time.Second
	m.logger.Info("Waiting for network readiness", "name", name)

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for network on %q (%v)", name, timeout)
		default:
			res, err := m.client.ExecInstance(name, []string{"ip", "-4", "route", "show", "default"}, 0, nil)
			if err == nil && res.ExitCode == 0 && strings.TrimSpace(res.Combined()) != "" {
				m.logger.Info("Network interface is ready", "name", name)
				return nil
			}
			time.Sleep(pollInterval)
		}
	}
}
