package lxm

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/common"
)

func (m *Manager) CreateContainer(conf *config.Config, configBaseDir string) error {
	ctx := context.Background()
	m.logger.Info("Creating container", "name", conf.Name, "image", conf.Image)

	source := provider.InstanceSource{Type: "image"}
	if common.IsHex(conf.Image) && len(conf.Image) >= 7 {
		source.Fingerprint = conf.Image
	} else {
		source.Alias = conf.Image
	}

	req := provider.InstanceCreateRequest{
		Name:    conf.Name,
		Type:    provider.InstanceTypeContainer,
		Source:  source,
		Config:  make(map[string]string),
		Devices: make(map[string]map[string]string),
	}

	cloudInitData, err := conf.ResolveCloudInit(configBaseDir)
	if err != nil {
		return err
	}
	if m.debug && cloudInitData != "" {
		m.logger.Debug("Resolved cloud-init", "name", conf.Name, "data", cloudInitData)
	}
	if cloudInitData != "" {
		req.Config["user.user-data"] = cloudInitData
	}

	if conf.NetworkConfig != "" {
		req.Config["user.network-config"] = conf.NetworkConfig
	}

	if conf.User != "" {
		req.Config["user.lxm.user"] = conf.User
	}

	req.Config["user.lxm.managed"] = "true"

	if len(conf.Groups) > 0 {
		req.Config["user.lxm.groups"] = strings.Join(conf.Groups, ",")
	}

	for _, mount := range conf.Mounts {
		devName := common.DeviceName(mount.Path)
		req.Devices[devName] = buildMountDevice(mount)
		m.logger.Info("Adding mount", "source", mount.Source, "path", mount.Path)
	}

	for _, n := range conf.Networks {
		result := buildNetworkDevice(n)
		req.Devices[result.devName] = result.device
		m.logger.Info("Adding network interface", "device", result.devName, "ipv4", n.IPv4, "parent", result.device["parent"])
	}

	if m.dryRun {
		m.logger.Info("Dry-run: would create container", "name", conf.Name)
		return nil
	}

	if err := m.client.CreateInstance(ctx, req); err != nil {
		return fmt.Errorf("creating container: %w", err)
	}
	m.logger.Info("Container created", "name", conf.Name)

	if !m.noStart {
		if err := m.client.UpdateInstanceState(ctx, conf.Name, "start", false); err != nil {
			return fmt.Errorf("starting container: %w", err)
		}
		m.logger.Info("Container started", "name", conf.Name)
	} else {
		m.logger.Info("Skipping container start and post-create recipes (--no-start specified)", "name", conf.Name)
		return nil
	}

	return m.runPostCreate(conf, configBaseDir, cloudInitData != "")
}

func (m *Manager) UpdateContainer(instance *provider.Instance, etag string, conf *config.Config, configBaseDir string) (bool, error) {
	ctx := context.Background()
	m.logger.Info("Checking container state", "name", conf.Name)

	configChanged, err := m.updateContainerConfig(instance, conf, configBaseDir)
	if err != nil {
		return false, err
	}

	if instance.Devices == nil {
		instance.Devices = make(map[string]map[string]string)
	}

	mountsChanged := m.updateContainerMounts(instance, conf)
	networksChanged := m.updateContainerNetworks(instance, conf)

	changed := configChanged || mountsChanged || networksChanged
	if !changed {
		m.logger.Info("Container is already up to date", "name", conf.Name)
		return false, nil
	}

	if m.dryRun {
		m.logger.Info("Dry-run: would update container configuration", "name", conf.Name)
		return true, nil
	}

	if err := m.client.UpdateInstance(ctx, conf.Name, instance.Writable(), etag); err != nil {
		return true, fmt.Errorf("updating container: %w", err)
	}
	m.logger.Info("Container configuration updated", "name", conf.Name)
	return true, nil
}

func (m *Manager) updateContainerConfig(instance *provider.Instance, conf *config.Config, configBaseDir string) (bool, error) {
	changed := false
	if instance.Config == nil {
		instance.Config = make(map[string]string)
	}

	cloudInitData, err := conf.ResolveCloudInit(configBaseDir)
	if err != nil {
		return false, err
	}
	if cloudInitData != "" && instance.Config["user.user-data"] != cloudInitData {
		instance.Config["user.user-data"] = cloudInitData
		changed = true
		m.logger.Info("Updating cloud-init configuration")
	}

	if conf.NetworkConfig != "" && instance.Config["user.network-config"] != conf.NetworkConfig {
		instance.Config["user.network-config"] = conf.NetworkConfig
		changed = true
		m.logger.Info("Updating network configuration")
	}

	if conf.User != "" && instance.Config["user.lxm.user"] != conf.User {
		instance.Config["user.lxm.user"] = conf.User
		changed = true
		m.logger.Info("Updating user metadata", "user", conf.User)
	}

	// Backfill managed flag on existing containers (OQ5)
	if instance.Config["user.lxm.managed"] != "true" {
		instance.Config["user.lxm.managed"] = "true"
		changed = true
		m.logger.Info("Setting managed flag on existing container", "name", conf.Name)
	}

	// Full reconciliation: create, update, delete for groups (R5)
	groupsValue := strings.Join(conf.Groups, ",")
	existingGroups := instance.Config["user.lxm.groups"]
	if len(conf.Groups) == 0 {
		if existingGroups != "" {
			delete(instance.Config, "user.lxm.groups")
			changed = true
			m.logger.Info("Removing groups metadata", "name", conf.Name)
		}
	} else if groupsValue != existingGroups {
		instance.Config["user.lxm.groups"] = groupsValue
		changed = true
		m.logger.Info("Updating groups metadata", "groups", groupsValue)
	}

	return changed, nil
}

func (m *Manager) updateContainerMounts(instance *provider.Instance, conf *config.Config) bool {
	changed := false
	desiredDevices := make(map[string]bool)

	for _, mount := range conf.Mounts {
		devName := common.DeviceName(mount.Path)
		desiredDevices[devName] = true

		expectedRecursive := "false"
		if mount.Recursive {
			expectedRecursive = "true"
		}

		var currentRecursive string
		if dev, ok := instance.Devices[devName]; ok {
			currentRecursive = dev["recursive"]
		}
		if currentRecursive == "" {
			currentRecursive = "false"
		}

		if dev, ok := instance.Devices[devName]; !ok ||
			dev["source"] != mount.Source ||
			dev["path"] != mount.Path ||
			dev["shift"] != "true" ||
			currentRecursive != expectedRecursive {
			instance.Devices[devName] = buildMountDevice(mount)
			changed = true
			m.logger.Info("Adding/Updating mount", "source", mount.Source, "path", mount.Path)
		}
	}

	for devName, dev := range instance.Devices {
		if strings.HasPrefix(devName, "mount-") && dev["type"] == "disk" && !desiredDevices[devName] {
			delete(instance.Devices, devName)
			changed = true
			m.logger.Info("Removing mount", "device", devName)
		}
	}

	return changed
}

func (m *Manager) updateContainerNetworks(instance *provider.Instance, conf *config.Config) bool {
	changed := false
	desiredDevices := make(map[string]bool)

	for _, n := range conf.Networks {
		result := buildNetworkDevice(n)
		desiredDevices[result.devName] = true

		dev, ok := instance.Devices[result.devName]
		if !ok || dev["type"] != "nic" || dev["parent"] != result.device["parent"] || dev["ipv4.address"] != n.IPv4 || dev["user.lxm.managed"] != "true" {
			instance.Devices[result.devName] = result.device
			changed = true
			m.logger.Info("Adding/Updating network interface", "device", result.devName, "ipv4", n.IPv4, "parent", result.device["parent"])
		}
	}

	for devName, dev := range instance.Devices {
		if dev["user.lxm.managed"] == "true" && dev["type"] == "nic" && !desiredDevices[devName] {
			delete(instance.Devices, devName)
			changed = true
			m.logger.Info("Removing network interface", "device", devName)
		}
	}

	return changed
}

func (m *Manager) DeleteContainer(name string) error {
	ctx := context.Background()
	instance, _, err := m.client.GetInstance(ctx, name)
	if err != nil {
		if code, _ := m.client.ClassifyError(err, "lookup"); code == 5 {
			m.logger.Info("Container does not exist. Nothing to delete.", "name", name)
			return nil
		}
		return fmt.Errorf("getting container %q for deletion: %w", name, err)
	}

	if instance.StatusCode != 102 { // 102 is Stopped
		m.logger.Info("Stopping container", "name", name)
		if m.dryRun {
			m.logger.Info("Dry-run: would stop and delete container", "name", name)
			return nil
		}
		if err := m.client.UpdateInstanceState(ctx, name, "stop", true); err != nil {
			return fmt.Errorf("stopping container: %w", err)
		}
	}

	if m.dryRun {
		m.logger.Info("Dry-run: would delete container", "name", name)
		return nil
	}

	m.logger.Info("Deleting container", "name", name)
	if err := m.client.DeleteInstance(ctx, name); err != nil {
		return fmt.Errorf("deleting container: %w", err)
	}
	m.logger.Info("Container deleted", "name", name)
	return nil
}

func (m *Manager) UpdateCloudInit(name string, data []byte) error {
	ctx := context.Background()
	instance, etag, err := m.client.GetInstance(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get container %q: %w", name, err)
	}

	if instance.Config == nil {
		instance.Config = make(map[string]string)
	}
	instance.Config["user.user-data"] = string(data)

	if m.dryRun {
		m.logger.Info("Dry-run: would update cloud-init", "name", name)
		return nil
	}

	return m.client.UpdateInstance(ctx, name, instance.Writable(), etag)
}
