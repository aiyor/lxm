package fake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/common"
)

// FakeDriver is an in-memory implementation of provider.Driver for unit and integration testing.
type FakeDriver struct {
	mu *sync.Mutex

	ProviderTypeVal provider.ProviderType
	Project         string
	Target          string

	Instances   map[string]*provider.Instance
	ETags       map[string]string
	Files       map[string]map[string][]byte // instanceName -> path -> content
	IPs         map[string]string
	Extensions  map[string]bool
	RebuiltLogs []string
	Snapshots   map[string]map[string]*provider.Snapshot // instanceName -> snapName -> snapshot

	// Networks & ACLs
	Networks    map[string]*provider.Network
	NetworkACLs map[string]*provider.NetworkACL

	// Storage Volumes
	StoragePools map[string]bool
	Volumes      map[string]map[string]*provider.StorageVolume // pool -> name -> volume

	// Images
	Images  map[string]*provider.Image
	Aliases map[string]*provider.ImageAlias
	Fetches []ImageFetchRecord

	// Cluster
	Clustered bool
	Members   map[string]*provider.ClusterMember

	// Projects
	Projects map[string]string // name -> description

	// Custom hook overrides
	GetInstanceFunc              func(name string) (*provider.Instance, string, error)
	CreateInstanceFunc           func(req provider.InstanceCreateRequest) error
	UpdateInstanceFunc           func(name string, req provider.InstanceUpdateRequest, etag string) error
	DeleteInstanceFunc           func(name string) error
	UpdateInstanceStateFunc      func(name, action string, force bool) error
	RebuildInstanceFunc          func(name string, req provider.InstanceRebuildRequest) error
	ExecInstanceFunc             func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error)
	CreateNetworkFunc            func(req provider.NetworkCreateRequest) error
	UpdateNetworkFunc            func(name string, req provider.NetworkUpdateRequest, etag string) error
	DeleteNetworkFunc            func(name string) error
	CreateNetworkACLFunc         func(req provider.NetworkACLCreateRequest) error
	UpdateNetworkACLFunc         func(name string, req provider.NetworkACLUpdateRequest, etag string) error
	DeleteNetworkACLFunc         func(name string) error
	DeleteStoragePoolVolumeFunc  func(pool, volType, name string) error
	GetNetworksFunc              func() ([]provider.Network, error)
	GetNetworkACLsFunc           func() ([]provider.NetworkACL, error)
	GetImageAliasesFunc          func() ([]provider.ImageAlias, error)
	CopyRemoteImageFunc          func(ctx context.Context, remoteURL, alias, imageType, localAlias string) error
}

var _ provider.Driver = (*FakeDriver)(nil)

// New creates a new initialized FakeDriver.
func New() *FakeDriver {
	f := &FakeDriver{
		mu:              &sync.Mutex{},
		ProviderTypeVal: provider.ProviderTypeLXD,
		Project:         "default",
		Instances:       make(map[string]*provider.Instance),
		ETags:           make(map[string]string),
		Files:           make(map[string]map[string][]byte),
		IPs:             make(map[string]string),
		Extensions:      map[string]bool{"instances_rebuild": true, "custom_block_volumes": true},
		Snapshots:       make(map[string]map[string]*provider.Snapshot),
		Networks:        make(map[string]*provider.Network),
		NetworkACLs:     make(map[string]*provider.NetworkACL),
		StoragePools:    map[string]bool{"default": true, "local": true},
		Volumes:         make(map[string]map[string]*provider.StorageVolume),
		Images:          make(map[string]*provider.Image),
		Aliases:         make(map[string]*provider.ImageAlias),
		Members:         make(map[string]*provider.ClusterMember),
		Projects:        map[string]string{"default": "Default project"},
	}
	f.addDefaultNetworks()
	return f
}

func (f *FakeDriver) addDefaultNetworks() {
	f.Networks["lxdbr0"] = &provider.Network{
		Name:    "lxdbr0",
		Type:    "bridge",
		Managed: true,
		Config:  map[string]string{"ipv4.address": "10.0.0.1/24"},
	}
	f.Networks["incusbr0"] = &provider.Network{
		Name:    "incusbr0",
		Type:    "bridge",
		Managed: true,
		Config:  map[string]string{"ipv4.address": "10.200.0.1/24"},
	}
}

// Helper Seeders for Unit and Integration Tests

func (f *FakeDriver) AddVolume(pool, name, contentType string, config map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.StoragePools[pool] = true
	if f.Volumes[pool] == nil {
		f.Volumes[pool] = make(map[string]*provider.StorageVolume)
	}
	f.Volumes[pool][name] = &provider.StorageVolume{
		Name:        name,
		Type:        "custom",
		ContentType: contentType,
		Config:      config,
		ETag:        "fake-vol-etag",
	}
}

func (f *FakeDriver) AddNetwork(name, netType string, config map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Networks[name] = &provider.Network{
		Name:    name,
		Type:    netType,
		Managed: true,
		Config:  config,
		Status:  "Created",
		ETag:    "fake-net-etag",
	}
}

func (f *FakeDriver) AddNetworkACL(name string, ingress, egress []provider.NetworkACLRule, config map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.NetworkACLs[name] = &provider.NetworkACL{
		Name:    name,
		Ingress: ingress,
		Egress:  egress,
		Config:  config,
		ETag:    "fake-acl-etag",
	}
}

func (f *FakeDriver) AddImage(alias, fingerprint string, instType provider.InstanceType) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Images[fingerprint] = &provider.Image{
		Fingerprint: fingerprint,
		Type:        instType,
		Aliases:     []provider.ImageAlias{{Name: alias, Target: fingerprint}},
	}
	f.Aliases[alias] = &provider.ImageAlias{
		Name:   alias,
		Target: fingerprint,
	}
}

func (f *FakeDriver) ProviderType() provider.ProviderType {
	return f.ProviderTypeVal
}

func (f *FakeDriver) UseProject(project string) provider.Driver {
	if project == "" {
		project = "default"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	clone := *f
	clone.Project = project
	return &clone
}

func (f *FakeDriver) UseTarget(targetNode string) provider.Driver {
	f.mu.Lock()
	defer f.mu.Unlock()
	clone := *f
	clone.Target = targetNode
	return &clone
}

func (f *FakeDriver) HasExtension(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Extensions[name]
}

func (f *FakeDriver) SetClustered(clustered bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Clustered = clustered
}

func (f *FakeDriver) SetClusterMembers(members []provider.ClusterMember) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Members = make(map[string]*provider.ClusterMember)
	for _, m := range members {
		f.Members[m.ServerName] = &m
	}
}

func (f *FakeDriver) IsClustered(ctx context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Clustered, nil
}

func (f *FakeDriver) GetClusterMembers(ctx context.Context) ([]provider.ClusterMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]provider.ClusterMember, 0, len(f.Members))
	for _, m := range f.Members {
		result = append(result, *m)
	}
	return result, nil
}

func (f *FakeDriver) GetClusterMember(ctx context.Context, name string) (*provider.ClusterMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.Members[name]
	if !ok {
		return nil, fmt.Errorf("cluster member %q not found", name)
	}
	return m, nil
}

func (f *FakeDriver) GetProjects(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]string, 0, len(f.Projects))
	for p := range f.Projects {
		result = append(result, p)
	}
	return result, nil
}

func (f *FakeDriver) ProjectExists(ctx context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.Projects[name]
	return ok, nil
}

func (f *FakeDriver) CreateProject(ctx context.Context, name string, description string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Projects[name]; ok {
		return fmt.Errorf("project %q already exists", name)
	}
	f.Projects[name] = description
	return nil
}

func (f *FakeDriver) GetInstance(ctx context.Context, name string) (*provider.Instance, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.GetInstanceFunc != nil {
		return f.GetInstanceFunc(name)
	}

	inst, ok := f.Instances[name]
	if !ok {
		return nil, "", errors.New("instance not found")
	}
	etag := f.ETags[name]
	if etag == "" {
		etag = "fake-etag-1"
	}
	copyInst := *inst
	f.enrichInstanceLocked(&copyInst)
	return &copyInst, etag, nil
}

func (f *FakeDriver) ListInstances(ctx context.Context) ([]provider.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make([]provider.Instance, 0, len(f.Instances))
	for _, inst := range f.Instances {
		copyInst := *inst
		f.enrichInstanceLocked(&copyInst)
		result = append(result, copyInst)
	}
	return result, nil
}

func (f *FakeDriver) enrichInstanceLocked(inst *provider.Instance) {
	if inst.State == nil {
		netMap := make(map[string]provider.InstanceStateNetwork)
		ip := f.IPs[inst.Name]
		if ip == "" {
			ip = "10.10.10.100"
		}
		netMap["eth0"] = provider.InstanceStateNetwork{
			Addresses: []provider.InstanceStateNetworkAddress{
				{Family: "inet", Address: ip, Scope: "global"},
			},
			State: "up",
			Type:  "broadcast",
		}
		inst.State = &provider.InstanceState{
			Status:     inst.Status,
			StatusCode: inst.StatusCode,
			Network:    netMap,
		}
	}
	if snaps, ok := f.Snapshots[inst.Name]; ok && len(snaps) > 0 {
		inst.HasSnapshots = true
		snapList := make([]provider.Snapshot, 0, len(snaps))
		for _, s := range snaps {
			snapList = append(snapList, *s)
		}
		inst.Snapshots = snapList
	}
}

func (f *FakeDriver) CreateInstance(ctx context.Context, req provider.InstanceCreateRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.CreateInstanceFunc != nil {
		return f.CreateInstanceFunc(req)
	}

	if _, exists := f.Instances[req.Name]; exists {
		return fmt.Errorf("instance %q already exists", req.Name)
	}

	cfg := req.Config
	if cfg == nil {
		cfg = make(map[string]string)
	}

	location := f.Target
	if location == "" {
		location = "incus-node1"
	}

	inst := &provider.Instance{
		Name:         req.Name,
		Type:         req.Type,
		Status:       "Stopped",
		StatusCode:   102,
		Location:     location,
		Config:       cfg,
		Devices:      req.Devices,
		Ephemeral:    req.Ephemeral,
		Profiles:     req.Profiles,
		CreatedAt:    time.Now(),
		LastUsedAt:   time.Now(),
	}

	f.Instances[req.Name] = inst
	f.ETags[req.Name] = "fake-etag-created"
	return nil
}

func (f *FakeDriver) UpdateInstance(ctx context.Context, name string, req provider.InstanceUpdateRequest, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.UpdateInstanceFunc != nil {
		return f.UpdateInstanceFunc(name, req, etag)
	}

	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	currentETag := f.ETags[name]
	if etag != "" && currentETag != "" && etag != currentETag {
		return fmt.Errorf("ETag does not match: %s vs %s. The configuration has been modified since this change began", etag, currentETag)
	}

	if req.Config != nil {
		inst.Config = req.Config
	}
	if req.Devices != nil {
		inst.Devices = req.Devices
	}
	if req.Profiles != nil {
		inst.Profiles = req.Profiles
	}

	f.ETags[name] = "fake-etag-updated"
	return nil
}

func (f *FakeDriver) DeleteInstance(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.DeleteInstanceFunc != nil {
		return f.DeleteInstanceFunc(name)
	}

	if _, ok := f.Instances[name]; !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	delete(f.Instances, name)
	delete(f.ETags, name)
	delete(f.Files, name)
	delete(f.IPs, name)
	delete(f.Snapshots, name)
	return nil
}

func (f *FakeDriver) UpdateInstanceState(ctx context.Context, name string, action string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.UpdateInstanceStateFunc != nil {
		return f.UpdateInstanceStateFunc(name, action, force)
	}

	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	switch strings.ToLower(action) {
	case "start":
		inst.Status = "Running"
		inst.StatusCode = 103
	case "stop":
		inst.Status = "Stopped"
		inst.StatusCode = 102
	case "restart":
		inst.Status = "Running"
		inst.StatusCode = 103
	}
	return nil
}

func (f *FakeDriver) RebuildInstance(ctx context.Context, name string, req provider.InstanceRebuildRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.RebuildInstanceFunc != nil {
		return f.RebuildInstanceFunc(name, req)
	}

	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	f.RebuiltLogs = append(f.RebuiltLogs, fmt.Sprintf("%s:%s", name, req.Source.Alias))
	f.ETags[name] = "fake-etag-rebuilt"
	inst.LastUsedAt = time.Now()
	return nil
}

func (f *FakeDriver) CreateInstanceSnapshot(ctx context.Context, name string, snapName string, stateful bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.Instances[name]; !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	if f.Snapshots[name] == nil {
		f.Snapshots[name] = make(map[string]*provider.Snapshot)
	}

	if snapName == "" {
		snapName = fmt.Sprintf("snap-%d", len(f.Snapshots[name])+1)
	}

	f.Snapshots[name][snapName] = &provider.Snapshot{
		Name:      snapName,
		CreatedAt: time.Now(),
		Stateful:  stateful,
	}
	return nil
}

func (f *FakeDriver) DeleteInstanceSnapshot(ctx context.Context, name string, snapName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Snapshots[name] != nil {
		delete(f.Snapshots[name], snapName)
	}
	return nil
}

func (f *FakeDriver) GetInstanceSnapshots(ctx context.Context, name string) ([]provider.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.Instances[name]; !ok {
		return nil, fmt.Errorf("instance %q not found", name)
	}

	var result []provider.Snapshot
	if snaps, ok := f.Snapshots[name]; ok {
		for _, s := range snaps {
			result = append(result, *s)
		}
	}
	return result, nil
}

func (f *FakeDriver) RestoreInstanceSnapshot(ctx context.Context, name string, snapName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.Instances[name]; !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	f.ETags[name] = "fake-etag-restored"
	return nil
}

func (f *FakeDriver) GetNetworks(ctx context.Context) ([]provider.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.GetNetworksFunc != nil {
		return f.GetNetworksFunc()
	}

	result := make([]provider.Network, 0, len(f.Networks))
	for _, n := range f.Networks {
		result = append(result, *n)
	}
	return result, nil
}

func (f *FakeDriver) GetNetwork(ctx context.Context, name string) (*provider.Network, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	n, ok := f.Networks[name]
	if !ok {
		return nil, "", fmt.Errorf("network %q not found", name)
	}
	return n, "fake-net-etag", nil
}

func (f *FakeDriver) CreateNetwork(ctx context.Context, net provider.NetworkCreateRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.CreateNetworkFunc != nil {
		return f.CreateNetworkFunc(net)
	}

	if _, ok := f.Networks[net.Name]; ok {
		return fmt.Errorf("network %q already exists", net.Name)
	}

	f.Networks[net.Name] = &provider.Network{
		Name:        net.Name,
		Type:        net.Type,
		Description: net.Description,
		Config:      net.Config,
		Managed:     true,
		Status:      "Created",
		ETag:        "fake-net-etag",
	}
	return nil
}

func (f *FakeDriver) UpdateNetwork(ctx context.Context, name string, net provider.NetworkUpdateRequest, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.UpdateNetworkFunc != nil {
		return f.UpdateNetworkFunc(name, net, etag)
	}

	n, ok := f.Networks[name]
	if !ok {
		return fmt.Errorf("network %q not found", name)
	}
	n.Description = net.Description
	n.Config = net.Config
	return nil
}

func (f *FakeDriver) DeleteNetwork(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.DeleteNetworkFunc != nil {
		return f.DeleteNetworkFunc(name)
	}

	if _, ok := f.Networks[name]; !ok {
		return fmt.Errorf("network %q not found", name)
	}
	delete(f.Networks, name)
	return nil
}

func (f *FakeDriver) GetNetworkACLs(ctx context.Context) ([]provider.NetworkACL, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.GetNetworkACLsFunc != nil {
		return f.GetNetworkACLsFunc()
	}

	result := make([]provider.NetworkACL, 0, len(f.NetworkACLs))
	for _, a := range f.NetworkACLs {
		result = append(result, *a)
	}
	return result, nil
}

func (f *FakeDriver) GetNetworkACL(ctx context.Context, name string) (*provider.NetworkACL, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	a, ok := f.NetworkACLs[name]
	if !ok {
		return nil, "", fmt.Errorf("network ACL %q not found", name)
	}
	return a, "fake-acl-etag", nil
}

func (f *FakeDriver) CreateNetworkACL(ctx context.Context, acl provider.NetworkACLCreateRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.CreateNetworkACLFunc != nil {
		return f.CreateNetworkACLFunc(acl)
	}

	if _, ok := f.NetworkACLs[acl.Name]; ok {
		return fmt.Errorf("network ACL %q already exists", acl.Name)
	}

	f.NetworkACLs[acl.Name] = &provider.NetworkACL{
		Name:        acl.Name,
		Description: acl.Description,
		Egress:      acl.Egress,
		Ingress:     acl.Ingress,
		Config:      acl.Config,
		ETag:        "fake-acl-etag",
	}
	return nil
}

func (f *FakeDriver) UpdateNetworkACL(ctx context.Context, name string, acl provider.NetworkACLUpdateRequest, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.UpdateNetworkACLFunc != nil {
		return f.UpdateNetworkACLFunc(name, acl, etag)
	}

	a, ok := f.NetworkACLs[name]
	if !ok {
		return fmt.Errorf("network ACL %q not found", name)
	}
	a.Description = acl.Description
	a.Egress = acl.Egress
	a.Ingress = acl.Ingress
	a.Config = acl.Config
	return nil
}

func (f *FakeDriver) DeleteNetworkACL(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.DeleteNetworkACLFunc != nil {
		return f.DeleteNetworkACLFunc(name)
	}

	if _, ok := f.NetworkACLs[name]; !ok {
		return fmt.Errorf("network ACL %q not found", name)
	}
	delete(f.NetworkACLs, name)
	return nil
}

func (f *FakeDriver) GetStoragePoolNames(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make([]string, 0, len(f.StoragePools))
	for p := range f.StoragePools {
		result = append(result, p)
	}
	return result, nil
}

func (f *FakeDriver) GetStoragePoolVolume(ctx context.Context, pool, volType, name string) (*provider.StorageVolume, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	poolVols, ok := f.Volumes[pool]
	if !ok {
		return nil, "", fmt.Errorf("storage pool %q not found", pool)
	}
	v, ok := poolVols[name]
	if !ok {
		return nil, "", fmt.Errorf("storage volume %q not found in pool %q", name, pool)
	}
	return v, "fake-vol-etag", nil
}

func (f *FakeDriver) GetStoragePoolVolumes(ctx context.Context, pool string) ([]provider.StorageVolume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	poolVols, ok := f.Volumes[pool]
	if !ok {
		return []provider.StorageVolume{}, nil
	}
	result := make([]provider.StorageVolume, 0, len(poolVols))
	for _, v := range poolVols {
		result = append(result, *v)
	}
	return result, nil
}

func (f *FakeDriver) CreateStoragePoolVolume(ctx context.Context, pool string, vol provider.StorageVolumeCreateRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Volumes[pool] == nil {
		f.Volumes[pool] = make(map[string]*provider.StorageVolume)
	}
	if _, ok := f.Volumes[pool][vol.Name]; ok {
		return fmt.Errorf("storage volume %q already exists in pool %q", vol.Name, pool)
	}
	f.Volumes[pool][vol.Name] = &provider.StorageVolume{
		Name:        vol.Name,
		Type:        vol.Type,
		ContentType: vol.ContentType,
		Description: vol.Description,
		Config:      vol.Config,
		ETag:        "fake-vol-etag",
	}
	return nil
}

func (f *FakeDriver) UpdateStoragePoolVolume(ctx context.Context, pool, volType, name string, vol provider.StorageVolumeUpdateRequest, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Volumes[pool] == nil || f.Volumes[pool][name] == nil {
		return fmt.Errorf("storage volume %q not found in pool %q", name, pool)
	}
	v := f.Volumes[pool][name]
	v.Description = vol.Description
	v.Config = vol.Config
	return nil
}

func (f *FakeDriver) DeleteStoragePoolVolume(ctx context.Context, pool, volType, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.DeleteStoragePoolVolumeFunc != nil {
		return f.DeleteStoragePoolVolumeFunc(pool, volType, name)
	}

	if f.Volumes[pool] != nil {
		delete(f.Volumes[pool], name)
	}
	return nil
}

func (f *FakeDriver) GetImages(ctx context.Context) ([]provider.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make([]provider.Image, 0, len(f.Images))
	for _, img := range f.Images {
		result = append(result, *img)
	}
	return result, nil
}

func (f *FakeDriver) GetImageAliases(ctx context.Context) ([]provider.ImageAlias, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.GetImageAliasesFunc != nil {
		return f.GetImageAliasesFunc()
	}

	result := make([]provider.ImageAlias, 0, len(f.Aliases))
	for _, a := range f.Aliases {
		result = append(result, *a)
	}
	return result, nil
}

// ImageFetchRecord records one CopyRemoteImage invocation on the fake.
type ImageFetchRecord struct {
	RemoteURL  string
	Alias      string
	Type       string
	LocalAlias string
}

func (f *FakeDriver) CopyRemoteImage(ctx context.Context, remoteURL, alias, imageType, localAlias string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.Fetches = append(f.Fetches, ImageFetchRecord{
		RemoteURL:  remoteURL,
		Alias:      alias,
		Type:       imageType,
		LocalAlias: localAlias,
	})

	if f.CopyRemoteImageFunc != nil {
		return f.CopyRemoteImageFunc(ctx, remoteURL, alias, imageType, localAlias)
	}

	if _, ok := f.Aliases[localAlias]; ok {
		return fmt.Errorf("alias already exists")
	}

	fp := fmt.Sprintf("fake-fingerprint-%s", localAlias)
	f.Images[fp] = &provider.Image{
		Fingerprint: fp,
		Type:        provider.InstanceType(imageType),
		Aliases:     []provider.ImageAlias{{Name: localAlias, Target: fp}},
	}
	f.Aliases[localAlias] = &provider.ImageAlias{
		Name:   localAlias,
		Target: fp,
	}
	return nil
}

func (f *FakeDriver) ResolveUID(ctx context.Context, name string, username string) (uint32, error) {
	if username == "root" {
		return 0, nil
	}
	return 1000, nil
}

func (f *FakeDriver) ResolveUserEnv(ctx context.Context, name string, username string) (*provider.UserEnv, error) {
	return &provider.UserEnv{
		UID:   1000,
		GID:   1000,
		Home:  "/home/" + username,
		Shell: "/bin/bash",
		User:  username,
	}, nil
}

func (f *FakeDriver) ExecInstance(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.ExecInstanceFunc != nil {
		return f.ExecInstanceFunc(name, cmd, uid, env)
	}
	return provider.ExecResult{ExitCode: 0, Stdout: "fake output", Stderr: ""}, nil
}

func (f *FakeDriver) InteractiveExecInstance(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) error {
	return nil
}

func (f *FakeDriver) CreateInstanceFile(ctx context.Context, name string, path string, content io.Reader, mode int, uid, gid int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Files[name] == nil {
		f.Files[name] = make(map[string][]byte)
	}
	buf := new(bytes.Buffer)
	if content != nil {
		if _, err := buf.ReadFrom(content); err != nil {
			return err
		}
	}
	f.Files[name][path] = buf.Bytes()
	return nil
}

func (f *FakeDriver) DeleteInstanceFile(ctx context.Context, name string, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Files[name] != nil {
		delete(f.Files[name], path)
	}
	return nil
}

func (f *FakeDriver) GetIP(ctx context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if ip, ok := f.IPs[name]; ok {
		return ip, nil
	}
	return "10.200.0.50", nil
}

func (f *FakeDriver) ClassifyError(err error, intent string) (int, bool) {
	return common.ClassifyError(err, intent)
}
