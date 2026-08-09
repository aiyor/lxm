package fleet_test

import (
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/fleet"
)

func TestSelector_GroupOR(t *testing.T) {
	sel, err := fleet.NewSelector(fleet.SelectorOpts{
		Groups: []string{"dev,staging", "prod"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !sel.Matches("box1", []string{"dev"}) {
		t.Errorf("expected box1 with 'dev' group to match")
	}
	if !sel.Matches("box2", []string{"prod"}) {
		t.Errorf("expected box2 with 'prod' group to match")
	}
	if sel.Matches("box3", []string{"other"}) {
		t.Errorf("expected box3 with 'other' group NOT to match")
	}
}

func TestSelector_NameRegexp(t *testing.T) {
	sel, err := fleet.NewSelector(fleet.SelectorOpts{
		Name: "agent-.*",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !sel.Matches("agent-1", nil) {
		t.Errorf("expected agent-1 to match")
	}
	if sel.Matches("web-1", nil) {
		t.Errorf("expected web-1 NOT to match")
	}
}

func TestSelector_GroupAndName(t *testing.T) {
	sel, err := fleet.NewSelector(fleet.SelectorOpts{
		Groups: []string{"dev"},
		Name:   "agent-.*",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !sel.Matches("agent-1", []string{"dev"}) {
		t.Errorf("expected agent-1 in dev group to match")
	}
	if sel.Matches("agent-1", []string{"prod"}) {
		t.Errorf("expected agent-1 in prod group NOT to match")
	}
	if sel.Matches("web-1", []string{"dev"}) {
		t.Errorf("expected web-1 in dev group NOT to match")
	}
}

func TestSelector_EmptyMatch_ReturnsError(t *testing.T) {
	sel, err := fleet.NewSelector(fleet.SelectorOpts{
		Name: "nonexistent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	configs := []*config.Config{
		{Name: "box1"},
	}

	_, err = sel.FilterConfigs(configs)
	if err == nil {
		t.Fatalf("expected error on empty match")
	}
}

func TestSelector_ExcludeGroupsAndInvalidRegex(t *testing.T) {
	_, err := fleet.NewSelector(fleet.SelectorOpts{
		Name: "[invalid(regex",
	})
	if err == nil {
		t.Errorf("expected error for invalid regex pattern")
	}

	sel, err := fleet.NewSelector(fleet.SelectorOpts{
		ExcludeGroups: []string{"experimental,deprecated"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !sel.Matches("box1", []string{"dev"}) {
		t.Errorf("expected box1 in dev group to match")
	}
	if sel.Matches("box2", []string{"experimental"}) {
		t.Errorf("expected box2 in experimental group NOT to match")
	}
}
