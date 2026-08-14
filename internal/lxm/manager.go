package lxm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/lxd"
)

var ErrWaitTimeout = errors.New("cloud-init wait timed out")

type Manager struct {
	client  lxd.InstanceService
	logger  *slog.Logger
	dryRun  bool
	debug   bool
	force   bool
	noStart bool
}

func NewManager(client lxd.InstanceService, logger *slog.Logger, dryRun, debug, force bool) *Manager {
	return NewManagerWithOptions(client, logger, dryRun, debug, force, false)
}

func NewManagerWithOptions(client lxd.InstanceService, logger *slog.Logger, dryRun, debug, force, noStart bool) *Manager {
	return &Manager{
		client:  client,
		logger:  logger,
		dryRun:  dryRun,
		debug:   debug,
		force:   force,
		noStart: noStart,
	}
}

func (m *Manager) ApplyConfig(conf *config.Config, configBaseDir string) error {
	if conf.Status == "absent" {
		return m.DeleteContainer(conf.Name)
	}

	instance, etag, err := m.client.GetInstance(conf.Name)
	if err != nil {
		return m.CreateContainer(conf, configBaseDir)
	}

	updated, err := m.UpdateContainer(instance, etag, conf, configBaseDir)
	if err != nil {
		return err
	}

	hasCloudInit := instance.Config["user.user-data"] != ""
	shouldWait := (updated || conf.WaitPolicy.Required) && hasCloudInit
	return m.runPostCreate(conf, configBaseDir, shouldWait)
}

func (m *Manager) runPostCreate(conf *config.Config, configBaseDir string, shouldWait bool) error {
	if len(conf.Recipes) == 0 && !conf.WaitPolicy.Required {
		return nil
	}
	if m.dryRun {
		m.logger.Info("Dry-run: would wait for cloud-init and execute recipes", "name", conf.Name)
		return nil
	}
	if shouldWait {
		if err := m.WaitForCloudInit(context.Background(), conf.Name, 10*time.Minute); err != nil {
			m.logger.Warn("Cloud-init wait failed", "error", err)
			return fmt.Errorf("%w: %w", ErrWaitTimeout, err)
		}
	}
	if err := m.ExecuteRecipes(conf, configBaseDir); err != nil {
		return fmt.Errorf("executing recipes: %w", err)
	}
	return nil
}
