package fleet

import (
	"fmt"
	"strings"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/lxd"
)

// InstanceStatus contains high-level information about a container instance.
type InstanceStatus struct {
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	StatusCode   int               `json:"status_code"`
	Architecture string            `json:"architecture"`
	Managed      bool              `json:"managed"`
	Groups       []string          `json:"groups,omitempty"`
	Image        string            `json:"image"`
	IPs          []string          `json:"ips,omitempty"`
	RecipeHashes map[string]string `json:"recipe_hashes,omitempty"`
	Snapshots    []string          `json:"snapshots,omitempty"`
	Config       map[string]string `json:"config,omitempty"`
}

// FleetInventory holds full inventory information about the fleet.
type FleetInventory struct {
	Instances []InstanceStatus `json:"instances"`
}

// GetInventory gathers full inventory from the LXD instance service in a single round-trip.
func GetInventory(svc lxd.InstanceService) (*FleetInventory, error) {
	instances, err := svc.ListInstances()
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}

	inv := &FleetInventory{
		Instances: make([]InstanceStatus, 0, len(instances)),
	}

	for _, full := range instances {
		status := InstanceStatus{
			Name:         full.Name,
			Status:       full.Status,
			StatusCode:   int(full.StatusCode),
			Architecture: full.Architecture,
			Config:       full.Config,
			RecipeHashes: make(map[string]string),
		}

		if status.Config == nil {
			status.Config = make(map[string]string)
		}

		// Managed marker
		status.Managed = (status.Config["user.lxm.managed"] == "true")

		// Groups
		if grpStr := status.Config["user.lxm.groups"]; grpStr != "" {
			for _, g := range strings.Split(grpStr, ",") {
				if clean := strings.TrimSpace(g); clean != "" {
					status.Groups = append(status.Groups, clean)
				}
			}
		}

		// Image
		status.Image = status.Config["image.os"]
		if status.Image == "" {
			status.Image = status.Config["volatile.base_image"]
		}
		if status.Image == "" {
			status.Image = "unknown"
		}

		// Recipe hashes
		for k, v := range status.Config {
			if strings.HasPrefix(k, "user.lxm.recipe.") && strings.HasSuffix(k, ".hash") {
				recipeName := strings.TrimPrefix(k, "user.lxm.recipe.")
				recipeName = strings.TrimSuffix(recipeName, ".hash")
				status.RecipeHashes[recipeName] = v
			}
		}

		// Extract IPs from full.State if available
		if full.State != nil && full.State.Network != nil {
			for _, net := range full.State.Network {
				for _, addr := range net.Addresses {
					if addr.Family == "inet" && addr.Address != "127.0.0.1" {
						status.IPs = append(status.IPs, addr.Address)
					}
				}
			}
		}

		// Snapshots
		for _, snap := range full.Snapshots {
			status.Snapshots = append(status.Snapshots, snap.Name)
		}

		inv.Instances = append(inv.Instances, status)
	}

	return inv, nil
}

// FindOrphans finds managed instances in LXD that match the active selector
// but have no matching manifest in the targeted directory.
func FindOrphans(instances []InstanceStatus, targetConfigs []*config.Config, sel *Selector) []InstanceStatus {
	manifestNames := make(map[string]bool)
	for _, c := range targetConfigs {
		if sel == nil || sel.Matches(c.Name, c.Groups) {
			manifestNames[c.Name] = true
		}
	}

	var orphans []InstanceStatus
	for _, inst := range instances {
		if !inst.Managed {
			continue
		}
		if sel != nil && !sel.Matches(inst.Name, inst.Groups) {
			continue
		}
		if !manifestNames[inst.Name] {
			orphans = append(orphans, inst)
		}
	}
	return orphans
}
