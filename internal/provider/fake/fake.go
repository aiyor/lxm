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

	// Cluster
	Clustered bool
	Members   map[string]*provider.ClusterMember

	// Projects
	Projects map[string]string // name -> description

	// Custom hook overrides
	GetInstanceFunc     func(name string) (*provider.Instance, string, error)
	CreateInstanceFunc  func(req provider.InstanceCreateRequest) error
	UpdateInstanceFunc  func(name string, req provider.InstanceUpdateRequest, etag string) error
	DeleteInstanceFunc  func(name string) error
	ExecInstanceFunc    func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error)
	CopyRemoteImageFunc func(ctx context.Context, remoteURL, alias, imageType, localAlias string) error
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
	f.addDefaultImages()
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

func (f *FakeDriver) addDefaultImages() {
	fp := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	f.Images[fp] = &provider.Image{
		Fingerprint: fp,
		Type:        provider.InstanceTypeContainer,
		Aliases: []provider.ImageAlias{
			{Name: "ubuntu/24.04", Target: fp},
		},
	}
	f.Aliases["ubuntu/24.04"] = &provider.ImageAlias{
		Name:   "ubuntu/24.04",
		Target: fp,
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

func (f *FakeDriver) IsClustered() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Clustered
}

func (f *FakeDriver) GetClusterMembers() ([]provider.ClusterMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]provider.ClusterMember, 0, len(f.Members))
	for _, m := range f.Members {
		result = append(result, *m)
	}
	return result, nil
}

func (f *FakeDriver) GetClusterMember(name string) (*provider.ClusterMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.Members[name]
	if !ok {
		return nil, fmt.Errorf("cluster member %q not found", name)
	}
	return m, nil
}

func (f *FakeDriver) GetProjects() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]string, 0, len(f.Projects))
	for p := range f.Projects {
		result = append(result, p)
	}
	return result, nil
}

func (f *FakeDriver) ProjectExists(name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.Projects[name]
	return ok, nil
}

func (f *FakeDriver) CreateProject(name string, description string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Projects[name]; ok {
		return fmt.Errorf("project %q already exists", name)
	}
	f.Projects[name] = description
	return nil
}

func (f *FakeDriver) GetInstance(name string) (*provider.Instance, string, error) {
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
	return inst, etag, nil
}

func (f *FakeDriver) ListInstances() ([]provider.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make([]provider.Instance, 0, len(f.Instances))
	for _, inst := range f.Instances {
		result = append(result, *inst)
	}
	return result, nil
}

func (f *FakeDriver) CreateInstance(req provider.InstanceCreateRequest) error {
	return f.CreateInstanceContext(context.Background(), req)
}

func (f *FakeDriver) CreateInstanceContext(ctx context.Context, req provider.InstanceCreateRequest) error {
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

func (f *FakeDriver) UpdateInstance(name string, req provider.InstanceUpdateRequest, etag string) error {
	return f.UpdateInstanceContext(context.Background(), name, req, etag)
}

func (f *FakeDriver) UpdateInstanceContext(ctx context.Context, name string, req provider.InstanceUpdateRequest, etag string) error {
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
		return fmt.Errorf("ETag does not match: %s vs %s. The configuration has been modified since this change began.", etag, currentETag)
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

func (f *FakeDriver) DeleteInstance(name string) error {
	return f.DeleteInstanceContext(context.Background(), name)
}

func (f *FakeDriver) DeleteInstanceContext(ctx context.Context, name string) error {
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

func (f *FakeDriver) UpdateInstanceState(name string, action string, force bool) error {
	return f.UpdateInstanceStateContext(context.Background(), name, action, force)
}

func (f *FakeDriver) UpdateInstanceStateContext(ctx context.Context, name string, action string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

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

func (f *FakeDriver) RebuildInstance(name string, req provider.InstanceRebuildRequest) error {
	return f.RebuildInstanceContext(context.Background(), name, req)
}

func (f *FakeDriver) RebuildInstanceContext(ctx context.Context, name string, req provider.InstanceRebuildRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	f.RebuiltLogs = append(f.RebuiltLogs, fmt.Sprintf("%s:%s", name, req.Source.Alias))
	f.ETags[name] = "fake-etag-rebuilt"
	inst.LastUsedAt = time.Now()
	return nil
}

func (f *FakeDriver) CreateInstanceSnapshot(name string, snapName string, stateful bool) error {
	return f.CreateInstanceSnapshotContext(context.Background(), name, snapName, stateful)
}

func (f *FakeDriver) CreateInstanceSnapshotContext(ctx context.Context, name string, snapName string, stateful bool) error {
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

func (f *FakeDriver) DeleteInstanceSnapshot(name string, snapName string) error {
	return f.DeleteInstanceSnapshotContext(context.Background(), name, snapName)
}

func (f *FakeDriver) DeleteInstanceSnapshotContext(ctx context.Context, name string, snapName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Snapshots[name] != nil {
		delete(f.Snapshots[name], snapName)
	}
	return nil
}

func (f *FakeDriver) GetInstanceSnapshots(name string) ([]provider.Snapshot, error) {
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

func (f *FakeDriver) RestoreInstanceSnapshot(name string, snapName string) error {
	return f.RestoreInstanceSnapshotContext(context.Background(), name, snapName)
}

func (f *FakeDriver) RestoreInstanceSnapshotContext(ctx context.Context, name string, snapName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.Instances[name]; !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	f.ETags[name] = "fake-etag-restored"
	return nil
}

func (f *FakeDriver) GetNetworks() ([]provider.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make([]provider.Network, 0, len(f.Networks))
	for _, n := range f.Networks {
		result = append(result, *n)
	}
	return result, nil
}

func (f *FakeDriver) GetNetwork(name string) (*provider.Network, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	n, ok := f.Networks[name]
	if !ok {
		return nil, "", fmt.Errorf("network %q not found", name)
	}
	return n, "fake-net-etag", nil
}

func (f *FakeDriver) CreateNetwork(net provider.NetworkCreateRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

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

func (f *FakeDriver) UpdateNetwork(name string, net provider.NetworkUpdateRequest, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	n, ok := f.Networks[name]
	if !ok {
		return fmt.Errorf("network %q not found", name)
	}
	n.Description = net.Description
	n.Config = net.Config
	return nil
}

func (f *FakeDriver) DeleteNetwork(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.Networks[name]; !ok {
		return fmt.Errorf("network %q not found", name)
	}
	delete(f.Networks, name)
	return nil
}

func (f *FakeDriver) GetNetworkACLs() ([]provider.NetworkACL, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make([]provider.NetworkACL, 0, len(f.NetworkACLs))
	for _, a := range f.NetworkACLs {
		result = append(result, *a)
	}
	return result, nil
}

func (f *FakeDriver) GetNetworkACL(name string) (*provider.NetworkACL, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	a, ok := f.NetworkACLs[name]
	if !ok {
		return nil, "", fmt.Errorf("network ACL %q not found", name)
	}
	return a, "fake-acl-etag", nil
}

func (f *FakeDriver) CreateNetworkACL(acl provider.NetworkACLCreateRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

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

func (f *FakeDriver) UpdateNetworkACL(name string, acl provider.NetworkACLUpdateRequest, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

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

func (f *FakeDriver) DeleteNetworkACL(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.NetworkACLs[name]; !ok {
		return fmt.Errorf("network ACL %q not found", name)
	}
	delete(f.NetworkACLs, name)
	return nil
}

func (f *FakeDriver) GetStoragePoolNames() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make([]string, 0, len(f.StoragePools))
	for p := range f.StoragePools {
		result = append(result, p)
	}
	return result, nil
}

func (f *FakeDriver) GetStoragePoolVolume(pool, volType, name string) (*provider.StorageVolume, string, error) {
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

func (f *FakeDriver) GetStoragePoolVolumes(pool string) ([]provider.StorageVolume, error) {
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

func (f *FakeDriver) CreateStoragePoolVolume(pool string, vol provider.StorageVolumeCreateRequest) error {
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

func (f *FakeDriver) UpdateStoragePoolVolume(pool, volType, name string, vol provider.StorageVolumeUpdateRequest, etag string) error {
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

func (f *FakeDriver) DeleteStoragePoolVolume(pool, volType, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Volumes[pool] != nil {
		delete(f.Volumes[pool], name)
	}
	return nil
}

func (f *FakeDriver) GetImages() ([]provider.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make([]provider.Image, 0, len(f.Images))
	for _, img := range f.Images {
		result = append(result, *img)
	}
	return result, nil
}

func (f *FakeDriver) GetImageAliases() ([]provider.ImageAlias, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make([]provider.ImageAlias, 0, len(f.Aliases))
	for _, a := range f.Aliases {
		result = append(result, *a)
	}
	return result, nil
}

func (f *FakeDriver) CopyRemoteImage(ctx context.Context, remoteURL, alias, imageType, localAlias string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.CopyRemoteImageFunc != nil {
		return f.CopyRemoteImageFunc(ctx, remoteURL, alias, imageType, localAlias)
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

func (f *FakeDriver) ResolveUID(name string, username string) (uint32, error) {
	if username == "root" {
		return 0, nil
	}
	return 1000, nil
}

func (f *FakeDriver) ResolveUserEnv(name string, username string) (*provider.UserEnv, error) {
	return &provider.UserEnv{
		UID:   1000,
		GID:   1000,
		Home:  "/home/" + username,
		Shell: "/bin/bash",
		User:  username,
	}, nil
}

func (f *FakeDriver) ExecInstance(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
	return f.ExecInstanceContext(context.Background(), name, cmd, uid, env)
}

func (f *FakeDriver) ExecInstanceContext(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.ExecInstanceFunc != nil {
		return f.ExecInstanceFunc(name, cmd, uid, env)
	}
	return provider.ExecResult{ExitCode: 0, Stdout: "fake output", Stderr: ""}, nil
}

func (f *FakeDriver) InteractiveExecInstance(name string, cmd []string, uid uint32, env map[string]string) error {
	return nil
}

func (f *FakeDriver) CreateInstanceFile(name string, path string, content io.Reader, mode int, uid, gid int64) error {
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

func (f *FakeDriver) DeleteInstanceFile(name string, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Files[name] != nil {
		delete(f.Files[name], path)
	}
	return nil
}

func (f *FakeDriver) GetIP(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if ip, ok := f.IPs[name]; ok {
		return ip, nil
	}
	return "10.200.0.50", nil
}

func (f *FakeDriver) ClassifyError(err error, intent string) (int, bool) {
	if err == nil {
		return 0, false
	}
	errStr := err.Error()
	if strings.Contains(errStr, "not found") {
		if intent == "lookup" {
			return 5, false
		}
		return 0, false
	}
	if strings.Contains(strings.ToLower(errStr), "etag") || strings.Contains(errStr, "412") {
		return 4, true
	}
	return 4, false
}
