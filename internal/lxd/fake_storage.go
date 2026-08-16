package lxd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/units"
)

var _ StorageService = (*FakeInstanceServer)(nil)

// VolumeStore holds the in-memory custom-volume state backing the fake
// StorageService implementation.
type VolumeStore struct {
	Volumes     map[string]*api.StorageVolume // key: "<pool>/<name>"
	VolumeETags map[string]string
}

// AddStorage initializes the storage-volume backing stores (called by
// NewFakeInstanceServer).
func (f *FakeInstanceServer) AddStorage() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Vols == nil {
		f.Vols = &VolumeStore{
			Volumes:     make(map[string]*api.StorageVolume),
			VolumeETags: make(map[string]string),
		}
	}
}

// AddStorageLocked initializes the storage stores; callers must hold f.mu.
func (f *FakeInstanceServer) AddStorageLocked() {
	if f.Vols == nil {
		f.Vols = &VolumeStore{
			Volumes:     make(map[string]*api.StorageVolume),
			VolumeETags: make(map[string]string),
		}
	}
}

func storageKey(pool, name string) string {
	return pool + "/" + name
}

func (f *FakeInstanceServer) GetStoragePoolNames() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Vols == nil {
		return []string{"default"}, nil
	}
	poolsMap := make(map[string]bool)
	poolsMap["default"] = true
	for key := range f.Vols.Volumes {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) > 0 && parts[0] != "" {
			poolsMap[parts[0]] = true
		}
	}
	pools := make([]string, 0, len(poolsMap))
	for p := range poolsMap {
		pools = append(pools, p)
	}
	sort.Strings(pools)
	return pools, nil
}

func (f *FakeInstanceServer) GetStoragePoolVolume(pool, volType, name string) (*api.StorageVolume, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Vols == nil {
		return nil, "", fmt.Errorf("storage volume %q not found", name)
	}
	v, ok := f.Vols.Volumes[storageKey(pool, name)]
	if !ok {
		return nil, "", fmt.Errorf("storage volume %q not found", name)
	}
	etag := f.Vols.VolumeETags[storageKey(pool, name)]
	if etag == "" {
		etag = "vol-etag-1"
	}
	return v, etag, nil
}

func (f *FakeInstanceServer) GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Vols == nil {
		return nil, nil
	}
	var out []api.StorageVolume
	for k, v := range f.Vols.Volumes {
		if strings.HasPrefix(k, pool+"/") {
			out = append(out, *v)
		}
	}
	return out, nil
}

func (f *FakeInstanceServer) CreateStoragePoolVolume(pool string, vol api.StorageVolumesPost) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AddStorageLocked()
	if _, ok := f.Vols.Volumes[storageKey(pool, vol.Name)]; ok {
		return fmt.Errorf("storage volume %q already exists", vol.Name)
	}
	cfg := vol.Config
	if cfg == nil {
		cfg = make(map[string]string)
	}
	contentType := vol.ContentType
	if contentType == "" {
		contentType = "filesystem"
	}
	f.Vols.Volumes[storageKey(pool, vol.Name)] = &api.StorageVolume{
		Name:        vol.Name,
		Type:        "custom",
		Pool:        pool,
		ContentType: contentType,
		Config:      cfg,
	}
	f.Vols.VolumeETags[storageKey(pool, vol.Name)] = "vol-etag-created"
	return nil
}

func (f *FakeInstanceServer) UpdateStoragePoolVolume(pool, volType, name string, vol api.StorageVolumePut, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Vols == nil {
		return fmt.Errorf("storage volume %q not found", name)
	}
	key := storageKey(pool, name)
	v, ok := f.Vols.Volumes[key]
	if !ok {
		return fmt.Errorf("storage volume %q not found", name)
	}
	currentETag := f.Vols.VolumeETags[key]
	if etag != "" && currentETag != "" && etag != currentETag {
		return fmt.Errorf("%s: %s vs %s. The configuration has been modified since this change began. Please retrieve the updated configuration before proceeding.", ETagConflictPrefix, etag, currentETag)
	}
	// Faithful to real LXD (ZFS): block volumes cannot be shrunk.
	if v.ContentType == "block" && vol.Config != nil {
		if newSize, ok := vol.Config["size"]; ok && newSize != "" {
			if oldSize := v.Config["size"]; oldSize != "" && newSize != oldSize {
				if isSmallerSize(newSize, oldSize) {
					return fmt.Errorf("Block volumes cannot be shrunk: Cannot be shrunk")
				}
			}
		}
	}
	if v.Config == nil {
		v.Config = make(map[string]string)
	}
	for k, val := range vol.Config {
		v.Config[k] = val
	}
	f.Vols.VolumeETags[key] = "vol-etag-updated"
	return nil
}

func (f *FakeInstanceServer) DeleteStoragePoolVolume(pool, volType, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeleteStoragePoolVolumeFunc != nil {
		return f.DeleteStoragePoolVolumeFunc(pool, volType, name)
	}
	if f.Vols == nil {
		return fmt.Errorf("storage volume %q not found", name)
	}
	key := storageKey(pool, name)
	if _, ok := f.Vols.Volumes[key]; !ok {
		return fmt.Errorf("storage volume %q not found", name)
	}
	delete(f.Vols.Volumes, key)
	delete(f.Vols.VolumeETags, key)
	return nil
}

// isSmallerSize reports whether the first byte-size string is strictly smaller
// than the second (used by the fake's block-volume shrink guard).
func isSmallerSize(a, b string) bool {
	av, errA := units.ParseByteSizeString(a)
	bv, errB := units.ParseByteSizeString(b)
	if errA != nil || errB != nil {
		return false
	}
	return av < bv
}
