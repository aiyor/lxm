package lxm

import (
	"context"
	"fmt"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/provider/common"
)

func buildMountDevice(mount config.Mount) map[string]string {
	dev := map[string]string{
		"type":   "disk",
		"source": mount.Source,
		"path":   mount.Path,
		"shift":  "true",
	}
	if mount.Recursive {
		dev["recursive"] = "true"
	}
	return dev
}

type networkDevice struct {
	devName string
	device  map[string]string
}

func buildNetworkDevice(n config.NetworkConfig) networkDevice {
	devName := n.Name
	if devName == "" {
		devName = "eth0"
	}
	parent := n.Parent
	if parent == "" {
		parent = "lxdbr0"
	}
	dev := map[string]string{
		"type":             "nic",
		"nictype":          "bridged",
		"parent":           parent,
		"name":             devName,
		"user.lxm.managed": "true",
	}
	if n.IPv4 != "" {
		dev["ipv4.address"] = n.IPv4
	}
	return networkDevice{devName: devName, device: dev}
}

func (m *Manager) AttachMounts(name string, mounts []config.Mount) error {
	ctx := context.Background()
	instance, etag, err := m.client.GetInstance(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get container %q: %w", name, err)
	}

	if instance.Devices == nil {
		instance.Devices = make(map[string]map[string]string)
	}

	for _, mount := range mounts {
		devName := common.DeviceName(mount.Path)
		instance.Devices[devName] = buildMountDevice(mount)
		m.logger.Info("Adding mount", "device", devName, "source", mount.Source, "path", mount.Path)
	}

	if m.dryRun {
		m.logger.Info("Dry-run: would update container devices", "name", name)
		return nil
	}

	return m.client.UpdateInstance(ctx, name, instance.Writable(), etag)
}
