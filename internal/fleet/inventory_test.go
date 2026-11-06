package fleet

import (
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

func TestGetInventory_Basic(t *testing.T) {
	ctx := t.Context()
	driver := fake.New()
	driver.Instances["c1"] = &provider.Instance{
		Name:         "c1",
		Status:       "Running",
		StatusCode:   103,
		Architecture: "x86_64",
		Config: map[string]string{
			"user.lxm.managed":           "true",
			"user.lxm.groups":            "dev,web",
			"image.os":                   "ubuntu",
			"user.lxm.recipe.setup.hash": "abc123hash",
		},
	}
	driver.Instances["c2"] = &provider.Instance{
		Name:         "c2",
		Status:       "Stopped",
		StatusCode:   102,
		Architecture: "x86_64",
		Config: map[string]string{
			"user.lxm.managed": "false",
		},
	}

	inv, err := GetInventory(ctx, driver)
	if err != nil {
		t.Fatalf("GetInventory failed: %v", err)
	}

	if len(inv.Instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(inv.Instances))
	}

	var c1 *InstanceStatus
	for i := range inv.Instances {
		if inv.Instances[i].Name == "c1" {
			c1 = &inv.Instances[i]
		}
	}
	if c1 == nil {
		t.Fatalf("c1 not found in inventory")
	}

	if !c1.Managed {
		t.Errorf("expected c1 to be managed")
	}
	if len(c1.Groups) != 2 || c1.Groups[0] != "dev" || c1.Groups[1] != "web" {
		t.Errorf("unexpected groups for c1: %v", c1.Groups)
	}
	if c1.Image != "ubuntu" {
		t.Errorf("expected image ubuntu, got %s", c1.Image)
	}
	if c1.RecipeHashes["setup"] != "abc123hash" {
		t.Errorf("expected recipe hash abc123hash, got %s", c1.RecipeHashes["setup"])
	}
}

func TestFindOrphans_ScopedToSelectorAndTarget(t *testing.T) {
	instances := []InstanceStatus{
		{Name: "agent-1", Managed: true, Groups: []string{"agent"}},
		{Name: "agent-2", Managed: true, Groups: []string{"agent"}},
		{Name: "db-1", Managed: true, Groups: []string{"db"}},
		{Name: "unmanaged-1", Managed: false, Groups: []string{"agent"}},
	}

	// Active manifests in target directory: only agent-1 exists
	targetConfigs := []*config.Config{
		{Name: "agent-1", Groups: []string{"agent"}},
	}

	// Selector: only --group agent
	sel, err := NewSelector(SelectorOpts{Groups: []string{"agent"}})
	if err != nil {
		t.Fatalf("NewSelector failed: %v", err)
	}

	orphans := FindOrphans(instances, targetConfigs, sel)
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(orphans))
	}
	if orphans[0].Name != "agent-2" {
		t.Errorf("expected orphan agent-2, got %s", orphans[0].Name)
	}
}

func TestGetInventory_FallbacksAndIPs(t *testing.T) {
	ctx := t.Context()
	driver := fake.New()
	driver.Instances["c3"] = &provider.Instance{
		Name:         "c3",
		Status:       "Running",
		StatusCode:   103,
		Architecture: "x86_64",
		Config: map[string]string{
			"volatile.base_image": "alpine:3.18",
		},
		State: &provider.InstanceState{
			Status:     "Running",
			StatusCode: 103,
			Network: map[string]provider.InstanceStateNetwork{
				"eth0": {
					Addresses: []provider.InstanceStateNetworkAddress{
						{Family: "inet", Address: "10.0.0.15", Scope: "global"},
					},
				},
			},
		},
		Snapshots: []provider.Snapshot{
			{Name: "c3/snap1"},
		},
	}

	driver.Instances["c4"] = &provider.Instance{
		Name:         "c4",
		Status:       "Stopped",
		StatusCode:   102,
		Architecture: "x86_64",
	}

	inv, err := GetInventory(ctx, driver)
	if err != nil {
		t.Fatalf("GetInventory failed: %v", err)
	}

	var c3, c4 *InstanceStatus
	for i := range inv.Instances {
		if inv.Instances[i].Name == "c3" {
			c3 = &inv.Instances[i]
		}
		if inv.Instances[i].Name == "c4" {
			c4 = &inv.Instances[i]
		}
	}

	if c3 == nil || c4 == nil {
		t.Fatalf("expected c3 and c4 in inventory")
	}

	if c3.Image != "alpine:3.18" {
		t.Errorf("expected c3 image alpine:3.18, got %q", c3.Image)
	}
	if len(c3.IPs) != 1 || c3.IPs[0] != "10.0.0.15" {
		t.Errorf("expected c3 IP [10.0.0.15], got %v", c3.IPs)
	}
	if len(c3.Snapshots) != 1 || c3.Snapshots[0] != "c3/snap1" {
		t.Errorf("expected c3 snapshots [c3/snap1], got %v", c3.Snapshots)
	}
	if c4.Image != "unknown" {
		t.Errorf("expected c4 image unknown, got %q", c4.Image)
	}
}

func TestGetInventory_PublishedPorts(t *testing.T) {
	ctx := t.Context()
	driver := fake.New()
	driver.Instances["web"] = &provider.Instance{
		Name:   "web",
		Status: "Running",
		Config: map[string]string{"user.lxm.managed": "true"},
		Devices: map[string]map[string]string{
			"proxy-web": {
				"type":             "proxy",
				"listen":           "tcp:127.0.0.1:8080",
				"connect":          "tcp:127.0.0.1:80",
				"user.lxm.managed": "true",
			},
			"outside": {
				"type":    "proxy",
				"listen":  "tcp:127.0.0.1:9090",
				"connect": "tcp:127.0.0.1:90",
			},
		},
	}

	inv, err := GetInventory(ctx, driver)
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	if len(inv.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(inv.Instances))
	}
	ports := inv.Instances[0].PublishedPorts
	if len(ports) != 2 {
		t.Fatalf("expected 2 published ports, got %+v", ports)
	}
	if ports[0].Device != "outside" || ports[0].Managed || ports[0].Name != "" {
		t.Fatalf("unexpected foreign entry: %+v", ports[0])
	}
	if ports[1].Device != "proxy-web" || !ports[1].Managed || ports[1].Name != "web" {
		t.Fatalf("unexpected managed entry: %+v", ports[1])
	}
}

func TestGetInventory_ProfilePublishedPorts(t *testing.T) {
	ctx := t.Context()
	driver := fake.New()
	driver.Instances["web"] = &provider.Instance{
		Name:    "web",
		Status:  "Running",
		Config:  map[string]string{"user.lxm.managed": "true"},
		Devices: map[string]map[string]string{},
		ExpandedDevices: map[string]map[string]string{
			"profile-proxy": {
				"type":    "proxy",
				"listen":  "tcp:127.0.0.1:9090",
				"connect": "tcp:127.0.0.1:90",
			},
		},
	}
	inv, err := GetInventory(ctx, driver)
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	ports := inv.Instances[0].PublishedPorts
	if len(ports) != 1 || ports[0].Device != "profile-proxy" || ports[0].Managed || ports[0].Name != "" {
		t.Fatalf("expected profile-provided foreign proxy in published ports, got %+v", ports)
	}
}
