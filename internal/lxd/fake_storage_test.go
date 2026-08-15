package lxd

import (
	"strings"
	"testing"

	"github.com/canonical/lxd/shared/api"
)

func TestFakeStorage_CRUD(t *testing.T) {
	f := NewFakeInstanceServer()

	// Empty list.
	vols, err := f.GetStoragePoolVolumes("default")
	if err != nil || len(vols) != 0 {
		t.Fatalf("expected empty list, got %v, %v", vols, err)
	}

	// Create filesystem + block.
	if err := f.CreateStoragePoolVolume("default", api.StorageVolumesPost{
		Name: "vol-fs", Type: "custom", ContentType: "filesystem",
		StorageVolumePut: api.StorageVolumePut{Config: map[string]string{"size": "10GiB"}},
	}); err != nil {
		t.Fatalf("create fs volume: %v", err)
	}
	if err := f.CreateStoragePoolVolume("default", api.StorageVolumesPost{
		Name: "vol-blk", Type: "custom", ContentType: "block",
		StorageVolumePut: api.StorageVolumePut{Config: map[string]string{"size": "20GiB"}},
	}); err != nil {
		t.Fatalf("create block volume: %v", err)
	}

	// Duplicate create rejected.
	if err := f.CreateStoragePoolVolume("default", api.StorageVolumesPost{Name: "vol-fs", Type: "custom"}); err == nil {
		t.Fatal("expected duplicate create to fail")
	}

	// Get + list.
	vol, etag, err := f.GetStoragePoolVolume("default", "custom", "vol-fs")
	if err != nil || vol.ContentType != "filesystem" || etag == "" {
		t.Fatalf("unexpected get: %+v etag=%q err=%v", vol, etag, err)
	}
	vols, _ = f.GetStoragePoolVolumes("default")
	if len(vols) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(vols))
	}

	// Update (grow).
	put := api.StorageVolumePut{Config: map[string]string{"size": "15GiB"}}
	if err := f.UpdateStoragePoolVolume("default", "custom", "vol-fs", put, etag); err != nil {
		t.Fatalf("update volume: %v", err)
	}
	vol, _, _ = f.GetStoragePoolVolume("default", "custom", "vol-fs")
	if vol.Config["size"] != "15GiB" {
		t.Errorf("expected 15GiB, got %q", vol.Config["size"])
	}

	// ETag conflict.
	if err := f.UpdateStoragePoolVolume("default", "custom", "vol-fs", put, "stale-etag"); err == nil {
		t.Fatal("expected ETag conflict error")
	}

	// Delete + not-found.
	if err := f.DeleteStoragePoolVolume("default", "custom", "vol-blk"); err != nil {
		t.Fatalf("delete volume: %v", err)
	}
	if _, _, err := f.GetStoragePoolVolume("default", "custom", "vol-blk"); err == nil {
		t.Fatal("expected not-found after delete")
	}
}

func TestFakeStorage_BlockShrinkRejected(t *testing.T) {
	f := NewFakeInstanceServer()
	if err := f.CreateStoragePoolVolume("default", api.StorageVolumesPost{
		Name: "vol-blk", Type: "custom", ContentType: "block",
		StorageVolumePut: api.StorageVolumePut{Config: map[string]string{"size": "20GiB"}},
	}); err != nil {
		t.Fatalf("create block volume: %v", err)
	}
	_, etag, _ := f.GetStoragePoolVolume("default", "custom", "vol-blk")
	err := f.UpdateStoragePoolVolume("default", "custom", "vol-blk",
		api.StorageVolumePut{Config: map[string]string{"size": "10GiB"}}, etag)
	if err == nil {
		t.Fatal("expected block shrink to be rejected")
	}
	if !strings.Contains(err.Error(), "Block volumes cannot be shrunk") {
		t.Errorf("unexpected error: %v", err)
	}

	// Block grow is allowed.
	_, etag, _ = f.GetStoragePoolVolume("default", "custom", "vol-blk")
	if err := f.UpdateStoragePoolVolume("default", "custom", "vol-blk",
		api.StorageVolumePut{Config: map[string]string{"size": "30GiB"}}, etag); err != nil {
		t.Errorf("block grow should succeed: %v", err)
	}
}

func TestFakeStorage_ContentTypePersists(t *testing.T) {
	f := NewFakeInstanceServer()
	if err := f.CreateStoragePoolVolume("fast", api.StorageVolumesPost{
		Name: "web", Type: "custom", ContentType: "filesystem",
	}); err != nil {
		t.Fatalf("create volume: %v", err)
	}
	vol, _, err := f.GetStoragePoolVolume("fast", "custom", "web")
	if err != nil || vol.ContentType != "filesystem" {
		t.Fatalf("unexpected volume: %+v err=%v", vol, err)
	}
	// Pool-scoped listing.
	vols, _ := f.GetStoragePoolVolumes("other")
	if len(vols) != 0 {
		t.Errorf("expected no volumes in unrelated pool, got %d", len(vols))
	}
}
