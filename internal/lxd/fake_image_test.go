package lxd

import (
	"context"
	"testing"
)

func TestFakeImageService_GetImageAliases_Empty(t *testing.T) {
	f := NewFakeInstanceServer()
	aliases, err := f.GetImageAliases()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(aliases) != 0 {
		t.Errorf("expected no aliases on a fresh fake, got %d", len(aliases))
	}
}

func TestFakeImageService_CopyRemoteImage_SeedsAlias(t *testing.T) {
	f := NewFakeInstanceServer()
	if err := f.CopyRemoteImage(context.Background(), "https://cloud-images.ubuntu.com/releases", "24.04", "container", "ubuntu/24.04"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	aliases, err := f.GetImageAliases()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(aliases) != 1 || aliases[0].Name != "ubuntu/24.04" {
		t.Errorf("expected canonical alias seeded, got %+v", aliases)
	}

	images, err := f.GetImages()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(images) != 1 {
		t.Errorf("expected 1 image seeded, got %d", len(images))
	}
	if len(f.Images.Fetches) != 1 {
		t.Errorf("expected 1 recorded fetch, got %d", len(f.Images.Fetches))
	}
}

func TestFakeImageService_CopyRemoteImage_DuplicateAlias(t *testing.T) {
	f := NewFakeInstanceServer()
	_ = f.CopyRemoteImage(context.Background(), "u", "a", "container", "x/y")
	if err := f.CopyRemoteImage(context.Background(), "u", "a", "container", "x/y"); err == nil {
		t.Fatal("expected Alias already exists error on duplicate alias")
	} else if err.Error() != "Alias already exists: x/y" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFakeImageService_ProbeRoundTrip(t *testing.T) {
	// The command layer's cache probe (fetchImageAliases) maps GetImageAliases
	// to a set of names; a seeded alias must round-trip so a second plan is a
	// no-op.
	f := NewFakeInstanceServer()
	if err := f.CopyRemoteImage(context.Background(), "https://cloud-images.ubuntu.com/releases", "24.04", "container", "ubuntu/24.04"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	aliases, err := f.GetImageAliases()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	probe := make(map[string]bool, len(aliases))
	for _, a := range aliases {
		probe[a.Name] = true
	}
	if !probe["ubuntu/24.04"] {
		t.Errorf("expected probe to see seeded alias, got %+v", probe)
	}
}
