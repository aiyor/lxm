package fake_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

func TestFakeDriver_Operations(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()

	// 1. Instance Lifecycle
	err := driver.CreateInstance(ctx, provider.InstanceCreateRequest{
		Name: "test-box",
		Type: provider.InstanceTypeContainer,
		Config: map[string]string{
			"user.lxm": "true",
		},
		Devices: map[string]map[string]string{
			"root": {"type": "disk", "path": "/"},
		},
	})
	if err != nil {
		t.Fatalf("CreateInstance failed: %v", err)
	}

	inst, etag, err := driver.GetInstance(ctx, "test-box")
	if err != nil || inst.Name != "test-box" || etag == "" {
		t.Fatalf("GetInstance failed: %v, etag: %s", err, etag)
	}
	if inst.Config["user.lxm"] != "true" {
		t.Errorf("Config mismatch: %v", inst.Config)
	}
	if inst.StatusCode != 102 { // Stopped
		t.Errorf("expected StatusCode 102 (Stopped), got %d", inst.StatusCode)
	}

	// 2. State Transition (Start)
	if err := driver.UpdateInstanceState(ctx, "test-box", "start", false); err != nil {
		t.Fatalf("UpdateInstanceState failed: %v", err)
	}

	inst, _, err = driver.GetInstance(ctx, "test-box")
	if err != nil || inst.Status != "Running" || inst.StatusCode != 103 {
		t.Errorf("expected Running / 103, got status=%s, code=%d", inst.Status, inst.StatusCode)
	}

	// 3. Snapshot Lifecycle
	if err := driver.CreateInstanceSnapshot(ctx, "test-box", "snap1", false); err != nil {
		t.Fatalf("CreateInstanceSnapshot failed: %v", err)
	}
	snaps, err := driver.GetInstanceSnapshots(ctx, "test-box")
	if err != nil || len(snaps) != 1 || snaps[0].Name != "snap1" {
		t.Fatalf("GetInstanceSnapshots unexpected: %v, snaps=%+v", err, snaps)
	}
	if err := driver.RestoreInstanceSnapshot(ctx, "test-box", "snap1"); err != nil {
		t.Fatalf("RestoreInstanceSnapshot failed: %v", err)
	}
	if err := driver.DeleteInstanceSnapshot(ctx, "test-box", "snap1"); err != nil {
		t.Fatalf("DeleteInstanceSnapshot failed: %v", err)
	}

	// 4. File Creation & Reading
	fileContent := "hello fake driver"
	if err := driver.CreateInstanceFile(ctx, "test-box", "/etc/test.conf", strings.NewReader(fileContent), 0644, 0, 0); err != nil {
		t.Fatalf("CreateInstanceFile failed: %v", err)
	}
	if string(driver.Files["test-box"]["/etc/test.conf"]) != fileContent {
		t.Errorf("expected file content %q, got %q", fileContent, string(driver.Files["test-box"]["/etc/test.conf"]))
	}

	// 5. Volume Management
	driver.AddVolume("default", "vol1", "filesystem", map[string]string{"size": "10GiB"})
	vol, volEtag, err := driver.GetStoragePoolVolume(ctx, "default", "custom", "vol1")
	if err != nil || vol.Name != "vol1" || volEtag == "" {
		t.Fatalf("GetStoragePoolVolume failed: %v, etag: %s", err, volEtag)
	}

	// 6. Network Management
	driver.AddNetwork("testnet0", "bridge", map[string]string{"ipv4.address": "10.50.0.1/24"})
	net, netEtag, err := driver.GetNetwork(ctx, "testnet0")
	if err != nil || net.Name != "testnet0" || netEtag == "" {
		t.Fatalf("GetNetwork failed: %v, etag: %s", err, netEtag)
	}

	// 7. Network ACL Management
	driver.AddNetworkACL("testacl0", nil, nil, map[string]string{"user.lxm.managed": "true"})
	acl, aclEtag, err := driver.GetNetworkACL(ctx, "testacl0")
	if err != nil || acl.Name != "testacl0" || aclEtag == "" {
		t.Fatalf("GetNetworkACL failed: %v, etag: %s", err, aclEtag)
	}

	// 8. Delete Instance
	if err := driver.DeleteInstance(ctx, "test-box"); err != nil {
		t.Fatalf("DeleteInstance failed: %v", err)
	}
	if _, _, err := driver.GetInstance(ctx, "test-box"); err == nil {
		t.Errorf("expected test-box to be deleted")
	}
}

func TestFakeDriver_CreateNetwork_Types(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()

	// 1. Create bridge network
	err := driver.CreateNetwork(ctx, provider.NetworkCreateRequest{
		Name:        "testbridge0",
		Type:        "bridge",
		Description: "test bridge",
		Config: map[string]string{
			"ipv4.address": "10.100.0.1/24",
			"ipv4.nat":     "true",
		},
	})
	if err != nil {
		t.Fatalf("CreateNetwork bridge failed: %v", err)
	}

	// 2. Create OVN network
	err = driver.CreateNetwork(ctx, provider.NetworkCreateRequest{
		Name:        "testovn0",
		Type:        "ovn",
		Description: "test ovn overlay",
		Config: map[string]string{
			"network":      "testbridge0",
			"ipv4.address": "10.200.0.1/24",
			"ipv4.nat":     "true",
		},
	})
	if err != nil {
		t.Fatalf("CreateNetwork ovn failed: %v", err)
	}

	// 3. Verify GetNetworks returns both with distinct Types
	nets, err := driver.GetNetworks(ctx)
	if err != nil {
		t.Fatalf("GetNetworks failed: %v", err)
	}
	netMap := make(map[string]provider.Network)
	for _, n := range nets {
		netMap[n.Name] = n
	}

	bridge, ok := netMap["testbridge0"]
	if !ok || bridge.Type != "bridge" || bridge.Config["ipv4.address"] != "10.100.0.1/24" {
		t.Errorf("bridge network mismatch: %+v", bridge)
	}

	ovn, ok := netMap["testovn0"]
	if !ok || ovn.Type != "ovn" || ovn.Config["network"] != "testbridge0" {
		t.Errorf("ovn network mismatch: %+v", ovn)
	}

	// 4. Update OVN network
	err = driver.UpdateNetwork(ctx, "testovn0", provider.NetworkUpdateRequest{
		Description: "updated ovn",
		Config: map[string]string{
			"network":      "testbridge0",
			"ipv4.address": "10.200.0.1/24",
			"ipv4.nat":     "false",
		},
	}, ovn.ETag)
	if err != nil {
		t.Fatalf("UpdateNetwork failed: %v", err)
	}

	updated, _, err := driver.GetNetwork(ctx, "testovn0")
	if err != nil || updated.Config["ipv4.nat"] != "false" || updated.Description != "updated ovn" {
		t.Errorf("expected updated ovn config, got %+v, err: %v", updated, err)
	}

	// 5. Delete networks
	if err := driver.DeleteNetwork(ctx, "testovn0"); err != nil {
		t.Fatalf("DeleteNetwork ovn failed: %v", err)
	}
	if _, _, err := driver.GetNetwork(ctx, "testovn0"); err == nil {
		t.Errorf("expected testovn0 to be deleted")
	}
}
