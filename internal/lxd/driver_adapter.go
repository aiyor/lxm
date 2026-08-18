package lxd

import (
	"context"

	lxd_client "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"

	"github.com/aiyor/lxm/internal/provider"
)

type driverAdapter struct {
	driver provider.Driver
}

// NewServiceFromDriver wraps a provider.Driver to satisfy the InstanceService,
// NetworkService, StorageService, and ImageService interfaces.
func NewServiceFromDriver(d provider.Driver) InstanceService {
	return &driverAdapter{driver: d}
}

// Driver returns the underlying provider.Driver.
func (a *driverAdapter) Driver() provider.Driver {
	return a.driver
}

func (a *driverAdapter) GetInstance(name string) (*api.Instance, string, error) {
	inst, etag, err := a.driver.GetInstance(name)
	if err != nil {
		return nil, "", err
	}
	if inst == nil {
		return nil, "", nil
	}
	return toAPIInstance(inst), etag, nil
}

func (a *driverAdapter) ListInstances() ([]api.InstanceFull, error) {
	instances, err := a.driver.ListInstances()
	if err != nil {
		return nil, err
	}
	res := make([]api.InstanceFull, len(instances))
	for i, inst := range instances {
		res[i] = toAPIInstanceFull(&inst)
	}
	return res, nil
}

func (a *driverAdapter) CreateInstance(req api.InstancesPost) error {
	return a.CreateInstanceContext(context.Background(), req)
}

func (a *driverAdapter) CreateInstanceContext(ctx context.Context, req api.InstancesPost) error {
	provReq := provider.InstanceCreateRequest{
		Name:      req.Name,
		Type:      provider.InstanceType(req.Type),
		Ephemeral: req.Ephemeral,
		Profiles:  req.Profiles,
		Config:    req.Config,
		Devices:   req.Devices,
		Source: provider.InstanceSource{
			Type:        string(req.Source.Type),
			Alias:       req.Source.Alias,
			Fingerprint: req.Source.Fingerprint,
			Server:      req.Source.Server,
			Protocol:    req.Source.Protocol,
			Secret:      req.Source.Secret,
		},
	}
	return a.driver.CreateInstanceContext(ctx, provReq)
}

func (a *driverAdapter) UpdateInstance(name string, put api.InstancePut, etag string) error {
	return a.UpdateInstanceContext(context.Background(), name, put, etag)
}

func (a *driverAdapter) UpdateInstanceContext(ctx context.Context, name string, put api.InstancePut, etag string) error {
	provReq := provider.InstanceUpdateRequest{
		Config:      put.Config,
		Devices:     put.Devices,
		Profiles:    put.Profiles,
		Description: put.Description,
	}
	return a.driver.UpdateInstanceContext(ctx, name, provReq, etag)
}

func (a *driverAdapter) DeleteInstance(name string) error {
	return a.DeleteInstanceContext(context.Background(), name)
}

func (a *driverAdapter) DeleteInstanceContext(ctx context.Context, name string) error {
	return a.driver.DeleteInstanceContext(ctx, name)
}

func (a *driverAdapter) UpdateInstanceState(name string, action string, force bool) error {
	return a.UpdateInstanceStateContext(context.Background(), name, action, force)
}

func (a *driverAdapter) UpdateInstanceStateContext(ctx context.Context, name string, action string, force bool) error {
	return a.driver.UpdateInstanceStateContext(ctx, name, action, force)
}

func (a *driverAdapter) RebuildInstance(name string, req api.InstanceRebuildPost) error {
	return a.RebuildInstanceContext(context.Background(), name, req)
}

func (a *driverAdapter) RebuildInstanceContext(ctx context.Context, name string, req api.InstanceRebuildPost) error {
	provReq := provider.InstanceRebuildRequest{
		Source: provider.InstanceSource{
			Type:        string(req.Source.Type),
			Alias:       req.Source.Alias,
			Fingerprint: req.Source.Fingerprint,
			Server:      req.Source.Server,
			Protocol:    req.Source.Protocol,
			Secret:      req.Source.Secret,
		},
	}
	return a.driver.RebuildInstanceContext(ctx, name, provReq)
}

func (a *driverAdapter) HasExtension(name string) bool {
	return a.driver.HasExtension(name)
}

func (a *driverAdapter) ResolveUID(name string, username string) (uint32, error) {
	return a.driver.ResolveUID(name, username)
}

func (a *driverAdapter) ResolveUserEnv(name string, username string) (*UserEnv, error) {
	u, err := a.driver.ResolveUserEnv(name, username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, nil
	}
	return &UserEnv{
		UID:   u.UID,
		GID:   u.GID,
		Home:  u.Home,
		Shell: u.Shell,
		User:  u.User,
	}, nil
}

func (a *driverAdapter) ExecInstance(name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error) {
	return a.ExecInstanceContext(context.Background(), name, cmd, uid, env)
}

func (a *driverAdapter) ExecInstanceContext(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error) {
	res, err := a.driver.ExecInstanceContext(ctx, name, cmd, uid, env)
	return ExecResult{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
	}, err
}

func (a *driverAdapter) InteractiveExecInstance(name string, cmd []string, uid uint32, env map[string]string) error {
	return a.driver.InteractiveExecInstance(name, cmd, uid, env)
}

func (a *driverAdapter) CreateInstanceFile(name string, path string, args lxd_client.InstanceFileArgs) error {
	return a.driver.CreateInstanceFile(name, path, args.Content, args.Mode, args.UID, args.GID)
}

func (a *driverAdapter) DeleteInstanceFile(name string, path string) error {
	return a.driver.DeleteInstanceFile(name, path)
}

func (a *driverAdapter) GetIP(name string) (string, error) {
	return a.driver.GetIP(name)
}

func (a *driverAdapter) ClassifyLXDError(err error, intent string) (int, bool) {
	return a.driver.ClassifyError(err, intent)
}

func (a *driverAdapter) CreateInstanceSnapshot(name string, req api.InstanceSnapshotsPost) error {
	return a.CreateInstanceSnapshotContext(context.Background(), name, req)
}

func (a *driverAdapter) CreateInstanceSnapshotContext(ctx context.Context, name string, req api.InstanceSnapshotsPost) error {
	return a.driver.CreateInstanceSnapshotContext(ctx, name, req.Name, req.Stateful)
}

func (a *driverAdapter) DeleteInstanceSnapshot(name string, snapshotName string) error {
	return a.DeleteInstanceSnapshotContext(context.Background(), name, snapshotName)
}

func (a *driverAdapter) DeleteInstanceSnapshotContext(ctx context.Context, name string, snapshotName string) error {
	return a.driver.DeleteInstanceSnapshotContext(ctx, name, snapshotName)
}

func (a *driverAdapter) GetInstanceSnapshots(name string) ([]api.InstanceSnapshot, error) {
	snaps, err := a.driver.GetInstanceSnapshots(name)
	if err != nil {
		return nil, err
	}
	res := make([]api.InstanceSnapshot, len(snaps))
	for i, s := range snaps {
		res[i] = api.InstanceSnapshot{
			Name:      s.Name,
			CreatedAt: s.CreatedAt,
			Stateful:  s.Stateful,
		}
	}
	return res, nil
}

func (a *driverAdapter) RestoreInstanceSnapshot(name string, snapshotName string) error {
	return a.RestoreInstanceSnapshotContext(context.Background(), name, snapshotName)
}

func (a *driverAdapter) RestoreInstanceSnapshotContext(ctx context.Context, name string, snapshotName string) error {
	return a.driver.RestoreInstanceSnapshotContext(ctx, name, snapshotName)
}

// ----------------------------------------------------------------------------
// Networks & ACLs
// ----------------------------------------------------------------------------

func (a *driverAdapter) GetNetworks() ([]api.Network, error) {
	nets, err := a.driver.GetNetworks()
	if err != nil {
		return nil, err
	}
	res := make([]api.Network, len(nets))
	for i, n := range nets {
		res[i] = api.Network{
			Name:        n.Name,
			Type:        n.Type,
			Description: n.Description,
			Config:      n.Config,
			Managed:     n.Managed,
			Status:      n.Status,
			Locations:   n.Locations,
		}
	}
	return res, nil
}

func (a *driverAdapter) GetNetwork(name string) (*api.Network, string, error) {
	net, etag, err := a.driver.GetNetwork(name)
	if err != nil {
		return nil, "", err
	}
	if net == nil {
		return nil, "", nil
	}
	return &api.Network{
		Name:        net.Name,
		Type:        net.Type,
		Description: net.Description,
		Config:      net.Config,
		Managed:     net.Managed,
		Status:      net.Status,
		Locations:   net.Locations,
	}, etag, nil
}

func (a *driverAdapter) CreateNetwork(network api.NetworksPost) error {
	return a.driver.CreateNetwork(provider.NetworkCreateRequest{
		Name:        network.Name,
		Type:        network.Type,
		Description: network.Description,
		Config:      network.Config,
	})
}

func (a *driverAdapter) UpdateNetwork(name string, network api.NetworkPut, etag string) error {
	return a.driver.UpdateNetwork(name, provider.NetworkUpdateRequest{
		Description: network.Description,
		Config:      network.Config,
	}, etag)
}

func (a *driverAdapter) DeleteNetwork(name string) error {
	return a.driver.DeleteNetwork(name)
}

func (a *driverAdapter) GetNetworkACLs() ([]api.NetworkACL, error) {
	acls, err := a.driver.GetNetworkACLs()
	if err != nil {
		return nil, err
	}
	res := make([]api.NetworkACL, len(acls))
	for i, acl := range acls {
		res[i] = *toAPINetworkACL(&acl)
	}
	return res, nil
}

func (a *driverAdapter) GetNetworkACL(name string) (*api.NetworkACL, string, error) {
	acl, etag, err := a.driver.GetNetworkACL(name)
	if err != nil {
		return nil, "", err
	}
	if acl == nil {
		return nil, "", nil
	}
	return toAPINetworkACL(acl), etag, nil
}

func (a *driverAdapter) CreateNetworkACL(acl api.NetworkACLsPost) error {
	req := provider.NetworkACLCreateRequest{
		Name:        acl.Name,
		Description: acl.Description,
		Config:      acl.Config,
		Egress:      toProviderACLRules(acl.Egress),
		Ingress:     toProviderACLRules(acl.Ingress),
	}
	return a.driver.CreateNetworkACL(req)
}

func (a *driverAdapter) UpdateNetworkACL(name string, acl api.NetworkACLPut, etag string) error {
	req := provider.NetworkACLUpdateRequest{
		Description: acl.Description,
		Config:      acl.Config,
		Egress:      toProviderACLRules(acl.Egress),
		Ingress:     toProviderACLRules(acl.Ingress),
	}
	return a.driver.UpdateNetworkACL(name, req, etag)
}

func (a *driverAdapter) DeleteNetworkACL(name string) error {
	return a.driver.DeleteNetworkACL(name)
}

// ----------------------------------------------------------------------------
// Storage Volumes
// ----------------------------------------------------------------------------

func (a *driverAdapter) GetStoragePoolNames() ([]string, error) {
	return a.driver.GetStoragePoolNames()
}

func (a *driverAdapter) GetStoragePoolVolume(pool, volType, name string) (*api.StorageVolume, string, error) {
	vol, etag, err := a.driver.GetStoragePoolVolume(pool, volType, name)
	if err != nil {
		return nil, "", err
	}
	if vol == nil {
		return nil, "", nil
	}
	return &api.StorageVolume{
		Name:        vol.Name,
		Type:        vol.Type,
		Description: vol.Description,
		Config:      vol.Config,
		ContentType: vol.ContentType,
		Location:    vol.Location,
	}, etag, nil
}

func (a *driverAdapter) GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error) {
	vols, err := a.driver.GetStoragePoolVolumes(pool)
	if err != nil {
		return nil, err
	}
	res := make([]api.StorageVolume, len(vols))
	for i, v := range vols {
		res[i] = api.StorageVolume{
			Name:        v.Name,
			Type:        v.Type,
			Description: v.Description,
			Config:      v.Config,
			ContentType: v.ContentType,
			Location:    v.Location,
		}
	}
	return res, nil
}

func (a *driverAdapter) CreateStoragePoolVolume(pool string, vol api.StorageVolumesPost) error {
	return a.driver.CreateStoragePoolVolume(pool, provider.StorageVolumeCreateRequest{
		Name:        vol.Name,
		Type:        vol.Type,
		ContentType: vol.ContentType,
		Description: vol.Description,
		Config:      vol.Config,
	})
}

func (a *driverAdapter) UpdateStoragePoolVolume(pool, volType, name string, vol api.StorageVolumePut, etag string) error {
	return a.driver.UpdateStoragePoolVolume(pool, volType, name, provider.StorageVolumeUpdateRequest{
		Description: vol.Description,
		Config:      vol.Config,
	}, etag)
}

func (a *driverAdapter) DeleteStoragePoolVolume(pool, volType, name string) error {
	return a.driver.DeleteStoragePoolVolume(pool, volType, name)
}

// ----------------------------------------------------------------------------
// Images
// ----------------------------------------------------------------------------

func (a *driverAdapter) GetImages() ([]api.Image, error) {
	images, err := a.driver.GetImages()
	if err != nil {
		return nil, err
	}
	res := make([]api.Image, len(images))
	for i, img := range images {
		aliases := make([]api.ImageAlias, len(img.Aliases))
		for j, al := range img.Aliases {
			aliases[j] = api.ImageAlias{
				Name:        al.Name,
				Description: al.Description,
			}
		}
		res[i] = api.Image{
			Fingerprint: img.Fingerprint,
			Size:        0,
			Architecture: img.Architecture,
			Type:        string(img.Type),
			Public:      false,
			Aliases:     aliases,
			Properties:  img.Properties,
		}
	}
	return res, nil
}

func (a *driverAdapter) GetImageAliases() ([]api.ImageAliasesEntry, error) {
	aliases, err := a.driver.GetImageAliases()
	if err != nil {
		return nil, err
	}
	res := make([]api.ImageAliasesEntry, len(aliases))
	for i, al := range aliases {
		res[i] = api.ImageAliasesEntry{
			Name:        al.Name,
			Target:      al.Target,
			Description: al.Description,
		}
	}
	return res, nil
}

func (a *driverAdapter) CopyRemoteImage(ctx context.Context, remoteURL, alias, imageType, localAlias string) error {
	return a.driver.CopyRemoteImage(ctx, remoteURL, alias, imageType, localAlias)
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

func toAPIInstance(inst *provider.Instance) *api.Instance {
	if inst == nil {
		return nil
	}
	return &api.Instance{
		Name:            inst.Name,
		Type:            string(inst.Type),
		Status:          inst.Status,
		StatusCode:      api.StatusCode(inst.StatusCode),
		Architecture:    inst.Architecture,
		Location:        inst.Location,
		Config:          inst.Config,
		ExpandedConfig:  inst.ExpandedConfig,
		Devices:         inst.Devices,
		ExpandedDevices: inst.ExpandedDevices,
		Profiles:        inst.Profiles,
		Ephemeral:       inst.Ephemeral,
		CreatedAt:       inst.CreatedAt,
		LastUsedAt:      inst.LastUsedAt,
	}
}

func toAPIInstanceFull(inst *provider.Instance) api.InstanceFull {
	if inst == nil {
		return api.InstanceFull{}
	}
	apiInst := *toAPIInstance(inst)
	full := api.InstanceFull{
		Instance: apiInst,
	}
	if inst.State != nil {
		netMap := make(map[string]api.InstanceStateNetwork, len(inst.State.Network))
		for name, net := range inst.State.Network {
			addrs := make([]api.InstanceStateNetworkAddress, len(net.Addresses))
			for j, addr := range net.Addresses {
				addrs[j] = api.InstanceStateNetworkAddress{
					Family:  addr.Family,
					Address: addr.Address,
					Netmask: addr.Netmask,
					Scope:   addr.Scope,
				}
			}
			netMap[name] = api.InstanceStateNetwork{
				Addresses: addrs,
				Counters: api.InstanceStateNetworkCounters{
					BytesReceived:   uint64(net.Counters.BytesReceived),
					BytesSent:       uint64(net.Counters.BytesSent),
					PacketsReceived: uint64(net.Counters.PacketsReceived),
					PacketsSent:     uint64(net.Counters.PacketsSent),
				},
				Hwaddr:   net.Hwaddr,
				Mtu:      net.Mtu,
				State:    net.State,
				Type:     net.Type,
				HostName: net.HostName,
			}
		}
		full.State = &api.InstanceState{
			Status:     inst.State.Status,
			StatusCode: api.StatusCode(inst.State.StatusCode),
			Network:    netMap,
			Pid:        inst.State.Pid,
			Processes:  inst.State.Processes,
		}
	}
	if len(inst.Snapshots) > 0 {
		snaps := make([]api.InstanceSnapshot, len(inst.Snapshots))
		for j, s := range inst.Snapshots {
			snaps[j] = api.InstanceSnapshot{
				Name:      s.Name,
				CreatedAt: s.CreatedAt,
				Stateful:  s.Stateful,
			}
		}
		full.Snapshots = snaps
	}
	return full
}

func toAPINetworkACL(acl *provider.NetworkACL) *api.NetworkACL {
	if acl == nil {
		return nil
	}
	return &api.NetworkACL{
		Name:        acl.Name,
		Description: acl.Description,
		Config:      acl.Config,
		Egress:      toAPIACLRules(acl.Egress),
		Ingress:     toAPIACLRules(acl.Ingress),
	}
}

func toAPIACLRules(rules []provider.NetworkACLRule) []api.NetworkACLRule {
	res := make([]api.NetworkACLRule, len(rules))
	for i, r := range rules {
		res[i] = api.NetworkACLRule{
			Action:          r.Action,
			Source:          r.Source,
			Destination:     r.Destination,
			Protocol:        r.Protocol,
			SourcePort:      r.SourcePort,
			DestinationPort: r.DestinationPort,
			ICMPType:        r.ICMPType,
			ICMPCode:        r.ICMPCode,
			State:           r.State,
			Description:     r.Description,
		}
	}
	return res
}

func toProviderACLRules(rules []api.NetworkACLRule) []provider.NetworkACLRule {
	res := make([]provider.NetworkACLRule, len(rules))
	for i, r := range rules {
		res[i] = provider.NetworkACLRule{
			Action:          r.Action,
			Source:          r.Source,
			Destination:     r.Destination,
			Protocol:        r.Protocol,
			SourcePort:      r.SourcePort,
			DestinationPort: r.DestinationPort,
			ICMPType:        r.ICMPType,
			ICMPCode:        r.ICMPCode,
			State:           r.State,
			Description:     r.Description,
		}
	}
	return res
}
