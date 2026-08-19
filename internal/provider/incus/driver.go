package incus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"
	incus_client "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/common"
)

type Driver struct {
	client  incus_client.InstanceServer
	project string
	target  string
}

var _ provider.Driver = (*Driver)(nil)

// NewDriver wraps an existing Incus InstanceServer as a provider.Driver.
func NewDriver(client incus_client.InstanceServer) *Driver {
	if client != nil {
		client = client.UseProject("default")
	}
	return &Driver{
		client:  client,
		project: "default",
	}
}

// NewUnixDriver connects to a local Incus UNIX socket following SDK precedence:
// 1. Explicit socket parameter
// 2. $INCUS_SOCKET or $INCUS_DIR
// 3. /run/incus/unix.socket
// 4. /var/lib/incus/unix.socket
func NewUnixDriver(socket string) (*Driver, error) {
	if socket == "" {
		socket = os.Getenv("INCUS_SOCKET")
	}
	if socket == "" {
		if incusDir := os.Getenv("INCUS_DIR"); incusDir != "" {
			cand := filepath.Clean(filepath.Join(incusDir, "unix.socket"))
			if _, err := os.Stat(cand); err == nil {
				socket = cand
			}
		}
	}
	if socket == "" {
		candidates := []string{
			"/run/incus/unix.socket",
			"/var/lib/incus/unix.socket",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				socket = c
				break
			}
		}
	}

	client, err := incus_client.ConnectIncusUnix(socket, nil)
	if err != nil {
		return nil, err
	}
	return NewDriver(client), nil
}

// NewRemoteDriver connects to a remote Incus HTTPS endpoint.
func NewRemoteDriver(url string, args *incus_client.ConnectionArgs) (*Driver, error) {
	client, err := incus_client.ConnectIncus(url, args)
	if err != nil {
		return nil, err
	}
	return NewDriver(client), nil
}

func (d *Driver) ProviderType() provider.ProviderType {
	return provider.ProviderTypeIncus
}

func (d *Driver) UseProject(project string) provider.Driver {
	if project == "" {
		project = "default"
	}
	return &Driver{
		client:  d.client.UseProject(project),
		project: project,
		target:  d.target,
	}
}

func (d *Driver) UseTarget(targetNode string) provider.Driver {
	return &Driver{
		client:  d.client.UseTarget(targetNode),
		project: d.project,
		target:  targetNode,
	}
}

func (d *Driver) clusterClient() incus_client.InstanceServer {
	if d.target != "" {
		return d.client.UseTarget("")
	}
	return d.client
}

func (d *Driver) GetInstance(ctx context.Context, name string) (*provider.Instance, string, error) {
	inst, etag, err := d.client.GetInstance(name)
	if err != nil {
		return nil, "", err
	}
	return toProviderInstance(inst, etag), etag, nil
}

func (d *Driver) ListInstances(ctx context.Context) ([]provider.Instance, error) {
	fullInstances, err := d.client.GetInstancesFull(api.InstanceTypeAny)
	if err != nil {
		return nil, err
	}
	result := make([]provider.Instance, len(fullInstances))
	for i, inst := range fullInstances {
		result[i] = *toProviderInstanceFull(&inst)
	}
	return result, nil
}

func (d *Driver) CreateInstance(ctx context.Context, req provider.InstanceCreateRequest) error {
	incusReq := toIncusInstancePost(req)
	op, err := d.client.CreateInstance(incusReq)
	if err != nil {
		return err
	}
	return common.WaitOpContext(ctx, op)
}

func (d *Driver) UpdateInstance(ctx context.Context, name string, req provider.InstanceUpdateRequest, etag string) error {
	incusPut := api.InstancePut{
		Config:      common.TranslateBootModeToDaemon(req.Type, req.Config),
		Devices:     req.Devices,
		Profiles:    req.Profiles,
		Description: req.Description,
	}
	op, err := d.client.UpdateInstance(name, incusPut, etag)
	if err != nil {
		return err
	}
	return common.WaitOpContext(ctx, op)
}

func (d *Driver) DeleteInstance(ctx context.Context, name string) error {
	op, err := d.client.DeleteInstance(name)
	if err != nil {
		return err
	}
	return common.WaitOpContext(ctx, op)
}

func (d *Driver) UpdateInstanceState(ctx context.Context, name string, action string, force bool) error {
	op, err := d.client.UpdateInstanceState(name, api.InstanceStatePut{Action: action, Force: force, Timeout: -1}, "")
	if err != nil {
		return err
	}
	return common.WaitOpContext(ctx, op)
}

func (d *Driver) RebuildInstance(ctx context.Context, name string, req provider.InstanceRebuildRequest) error {
	op, err := d.client.RebuildInstance(name, api.InstanceRebuildPost{
		Source: api.InstanceSource{
			Type:        req.Source.Type,
			Alias:       req.Source.Alias,
			Fingerprint: req.Source.Fingerprint,
			Server:      req.Source.Server,
			Protocol:    req.Source.Protocol,
			Secret:      req.Source.Secret,
		},
	})
	if err != nil {
		return err
	}
	return common.WaitOpContext(ctx, op)
}

func (d *Driver) CreateInstanceSnapshot(ctx context.Context, name string, snapName string, stateful bool) error {
	op, err := d.client.CreateInstanceSnapshot(name, api.InstanceSnapshotsPost{
		Name:     snapName,
		Stateful: stateful,
	})
	if err != nil {
		return err
	}
	return common.WaitOpContext(ctx, op)
}

func (d *Driver) DeleteInstanceSnapshot(ctx context.Context, name string, snapName string) error {
	op, err := d.client.DeleteInstanceSnapshot(name, snapName)
	if err != nil {
		return err
	}
	return common.WaitOpContext(ctx, op)
}

func (d *Driver) GetInstanceSnapshots(ctx context.Context, name string) ([]provider.Snapshot, error) {
	snaps, err := d.client.GetInstanceSnapshots(name)
	if err != nil {
		return nil, err
	}
	result := make([]provider.Snapshot, len(snaps))
	for i, s := range snaps {
		result[i] = provider.Snapshot{
			Name:      s.Name,
			CreatedAt: s.CreatedAt,
			Stateful:  s.Stateful,
		}
	}
	return result, nil
}

func (d *Driver) RestoreInstanceSnapshot(ctx context.Context, name string, snapName string) error {
	op, err := d.client.UpdateInstance(name, api.InstancePut{Restore: snapName}, "")
	if err != nil {
		return err
	}
	return common.WaitOpContext(ctx, op)
}

func (d *Driver) GetNetworks(ctx context.Context) ([]provider.Network, error) {
	nets, err := d.clusterClient().GetNetworks()
	if err != nil {
		return nil, err
	}
	result := make([]provider.Network, len(nets))
	for i, n := range nets {
		result[i] = provider.Network{
			Name:        n.Name,
			Type:        n.Type,
			Description: n.Description,
			Config:      n.Config,
			Managed:     n.Managed,
			Status:      n.Status,
			Locations:   n.Locations,
		}
	}
	return result, nil
}

func (d *Driver) GetNetwork(ctx context.Context, name string) (*provider.Network, string, error) {
	n, etag, err := d.clusterClient().GetNetwork(name)
	if err != nil {
		return nil, "", err
	}
	return &provider.Network{
		Name:        n.Name,
		Type:        n.Type,
		Description: n.Description,
		Config:      n.Config,
		Managed:     n.Managed,
		Status:      n.Status,
		Locations:   n.Locations,
		UsedBy:      n.UsedBy,
		ETag:        etag,
	}, etag, nil
}

func (d *Driver) CreateNetwork(ctx context.Context, net provider.NetworkCreateRequest) error {
	clustered, _ := d.IsClustered(ctx)
	if clustered {
		members, err := d.GetClusterMembers(ctx)
		if err == nil && len(members) > 0 {
			for _, m := range members {
				_ = d.client.UseTarget(m.ServerName).CreateNetwork(api.NetworksPost{
					Name: net.Name,
					Type: net.Type,
				})
			}
		}
	}
	err := d.clusterClient().CreateNetwork(api.NetworksPost{
		NetworkPut: api.NetworkPut{
			Description: net.Description,
			Config:      net.Config,
		},
		Name: net.Name,
		Type: net.Type,
	})
	if err != nil {
		return err
	}

	// Poll until network transitions from Pending to Created/active state
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, _, getErr := d.clusterClient().GetNetwork(net.Name)
		if getErr == nil && n != nil && n.Status != "Pending" && n.Status != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func (d *Driver) UpdateNetwork(ctx context.Context, name string, net provider.NetworkUpdateRequest, etag string) error {
	return d.clusterClient().UpdateNetwork(name, api.NetworkPut{
		Description: net.Description,
		Config:      net.Config,
	}, etag)
}

func (d *Driver) DeleteNetwork(ctx context.Context, name string) error {
	return d.clusterClient().DeleteNetwork(name)
}

func (d *Driver) GetNetworkACLs(ctx context.Context) ([]provider.NetworkACL, error) {
	acls, err := d.clusterClient().GetNetworkACLs()
	if err != nil {
		return nil, err
	}
	result := make([]provider.NetworkACL, len(acls))
	for i, a := range acls {
		result[i] = toProviderNetworkACL(&a, "")
	}
	return result, nil
}

func (d *Driver) GetNetworkACL(ctx context.Context, name string) (*provider.NetworkACL, string, error) {
	acl, etag, err := d.clusterClient().GetNetworkACL(name)
	if err != nil {
		return nil, "", err
	}
	res := toProviderNetworkACL(acl, etag)
	return &res, etag, nil
}

func (d *Driver) CreateNetworkACL(ctx context.Context, acl provider.NetworkACLCreateRequest) error {
	return d.clusterClient().CreateNetworkACL(api.NetworkACLsPost{
		NetworkACLPost: api.NetworkACLPost{
			Name: acl.Name,
		},
		NetworkACLPut: api.NetworkACLPut{
			Description: acl.Description,
			Egress:      toIncusRules(acl.Egress),
			Ingress:     toIncusRules(acl.Ingress),
			Config:      acl.Config,
		},
	})
}

func (d *Driver) UpdateNetworkACL(ctx context.Context, name string, acl provider.NetworkACLUpdateRequest, etag string) error {
	return d.clusterClient().UpdateNetworkACL(name, api.NetworkACLPut{
		Description: acl.Description,
		Egress:      toIncusRules(acl.Egress),
		Ingress:     toIncusRules(acl.Ingress),
		Config:      acl.Config,
	}, etag)
}

func (d *Driver) DeleteNetworkACL(ctx context.Context, name string) error {
	return d.clusterClient().DeleteNetworkACL(name)
}

func (d *Driver) GetStoragePoolNames(ctx context.Context) ([]string, error) {
	return d.client.GetStoragePoolNames()
}

func (d *Driver) GetStoragePoolVolume(ctx context.Context, pool, volType, name string) (*provider.StorageVolume, string, error) {
	v, etag, err := d.client.GetStoragePoolVolume(pool, volType, name)
	if err != nil {
		return nil, "", err
	}
	return &provider.StorageVolume{
		Name:        v.Name,
		Type:        v.Type,
		ContentType: v.ContentType,
		Description: v.Description,
		Pool:        pool,
		Config:      v.Config,
		Location:    v.Location,
		UsedBy:      v.UsedBy,
		ETag:        etag,
	}, etag, nil
}

func (d *Driver) GetStoragePoolVolumes(ctx context.Context, pool string) ([]provider.StorageVolume, error) {
	vols, err := d.client.GetStoragePoolVolumes(pool)
	if err != nil {
		return nil, err
	}
	result := make([]provider.StorageVolume, len(vols))
	for i, v := range vols {
		result[i] = provider.StorageVolume{
			Name:        v.Name,
			Type:        v.Type,
			ContentType: v.ContentType,
			Description: v.Description,
			Pool:        pool,
			Config:      v.Config,
			Location:    v.Location,
			UsedBy:      v.UsedBy,
		}
	}
	return result, nil
}

func (d *Driver) CreateStoragePoolVolume(ctx context.Context, pool string, vol provider.StorageVolumeCreateRequest) error {
	return d.client.CreateStoragePoolVolume(pool, api.StorageVolumesPost{
		Name:        vol.Name,
		Type:        vol.Type,
		ContentType: vol.ContentType,
		StorageVolumePut: api.StorageVolumePut{
			Description: vol.Description,
			Config:      vol.Config,
		},
	})
}

func (d *Driver) UpdateStoragePoolVolume(ctx context.Context, pool, volType, name string, vol provider.StorageVolumeUpdateRequest, etag string) error {
	return d.client.UpdateStoragePoolVolume(pool, volType, name, api.StorageVolumePut{
		Description: vol.Description,
		Config:      vol.Config,
	}, etag)
}

func (d *Driver) DeleteStoragePoolVolume(ctx context.Context, pool, volType, name string) error {
	return d.client.DeleteStoragePoolVolume(pool, volType, name)
}

func (d *Driver) GetImages(ctx context.Context) ([]provider.Image, error) {
	images, err := d.client.GetImages()
	if err != nil {
		return nil, err
	}
	result := make([]provider.Image, len(images))
	for i, img := range images {
		aliases := make([]provider.ImageAlias, len(img.Aliases))
		for j, a := range img.Aliases {
			aliases[j] = provider.ImageAlias{
				Name:        a.Name,
				Description: a.Description,
				Target:      img.Fingerprint,
			}
		}
		result[i] = provider.Image{
			Fingerprint:  img.Fingerprint,
			Type:         provider.InstanceType(img.Type),
			Architecture: img.Architecture,
			Properties:   img.Properties,
			Aliases:      aliases,
		}
	}
	return result, nil
}

func (d *Driver) GetImageAliases(ctx context.Context) ([]provider.ImageAlias, error) {
	aliases, err := d.client.GetImageAliases()
	if err != nil {
		return nil, err
	}
	result := make([]provider.ImageAlias, len(aliases))
	for i, a := range aliases {
		result[i] = provider.ImageAlias{
			Name:        a.Name,
			Description: a.Description,
			Target:      a.Target,
		}
	}
	return result, nil
}

func (d *Driver) CopyRemoteImage(ctx context.Context, remoteURL, alias, imageType, localAlias string) error {
	op, err := d.client.CreateImage(api.ImagesPost{
		Source: &api.ImagesPostSource{
			ImageSource: api.ImageSource{
				Protocol:  "simplestreams",
				Server:    remoteURL,
				Alias:     alias,
				ImageType: imageType,
			},
			Mode: "pull",
			Type: "image",
		},
		Aliases: []api.ImageAlias{{Name: localAlias}},
	}, nil)
	if err != nil {
		return err
	}
	return common.WaitOpContext(ctx, op)
}

func (d *Driver) IsClustered(ctx context.Context) (bool, error) {
	server, _, err := d.client.GetServer()
	if err != nil {
		return false, err
	}
	return server.Environment.ServerClustered, nil
}

func (d *Driver) GetClusterMembers(ctx context.Context) ([]provider.ClusterMember, error) {
	members, err := d.client.GetClusterMembers()
	if err != nil {
		return nil, err
	}
	result := make([]provider.ClusterMember, len(members))
	for i, m := range members {
		result[i] = provider.ClusterMember{
			ServerName:   m.ServerName,
			URL:          m.URL,
			Database:     m.Database,
			Status:       provider.ClusterMemberStatus(m.Status),
			Message:      m.Message,
			Architecture: m.Architecture,
			Roles:        m.Roles,
		}
	}
	return result, nil
}

func (d *Driver) GetClusterMember(ctx context.Context, name string) (*provider.ClusterMember, error) {
	m, _, err := d.client.GetClusterMember(name)
	if err != nil {
		return nil, err
	}
	return &provider.ClusterMember{
		ServerName:   m.ServerName,
		URL:          m.URL,
		Database:     m.Database,
		Status:       provider.ClusterMemberStatus(m.Status),
		Message:      m.Message,
		Architecture: m.Architecture,
		Roles:        m.Roles,
	}, nil
}

func (d *Driver) GetProjects(ctx context.Context) ([]string, error) {
	return d.client.GetProjectNames()
}

func (d *Driver) ProjectExists(ctx context.Context, name string) (bool, error) {
	projects, err := d.client.GetProjectNames()
	if err != nil {
		return false, err
	}
	for _, p := range projects {
		if p == name {
			return true, nil
		}
	}
	return false, nil
}

func (d *Driver) CreateProject(ctx context.Context, name string, description string) error {
	return d.client.CreateProject(api.ProjectsPost{
		Name: name,
		ProjectPut: api.ProjectPut{
			Description: description,
		},
	})
}

func (d *Driver) HasExtension(name string) bool {
	if d.client == nil {
		return false
	}
	return d.client.HasExtension(name)
}

func (d *Driver) ClassifyError(err error, intent string) (int, bool) {
	return common.ClassifyError(err, intent)
}

func (d *Driver) ResolveUID(ctx context.Context, name string, username string) (uint32, error) {
	return common.ResolveUID(ctx, d.ExecInstance, name, username)
}

func (d *Driver) ResolveUserEnv(ctx context.Context, name string, username string) (*provider.UserEnv, error) {
	return common.ResolveUserEnv(ctx, d.ExecInstance, name, username)
}

func (d *Driver) ExecInstance(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
	var stdout, stderr bytes.Buffer

	execReq := api.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: false,
		User:        uid,
		Environment: env,
	}

	execArgs := &incus_client.InstanceExecArgs{
		Stdin:  io.NopCloser(bytes.NewReader(nil)),
		Stdout: &stdout,
		Stderr: &stderr,
	}

	op, err := d.client.ExecInstance(name, execReq, execArgs)
	if err != nil {
		return provider.ExecResult{ExitCode: -1}, err
	}
	waitErr := common.WaitOpContext(ctx, op)
	var metadata map[string]interface{}
	if op != nil {
		metadata = op.Get().Metadata
	}
	exitCode, finalErr := common.ExtractExecExitCode(metadata, waitErr)
	return provider.ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, finalErr
}

func (d *Driver) InteractiveExecInstance(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) error {
	return common.RunInteractiveTerminal(func(width, height int, controlChan <-chan common.ControlMessage) error {
		execReq := api.InstanceExecPost{
			Command:     cmd,
			WaitForWS:   true,
			Interactive: true,
			Width:       width,
			Height:      height,
			User:        uid,
			Environment: env,
		}

		execArgs := &incus_client.InstanceExecArgs{
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
			Control: func(conn *websocket.Conn) {
				for msg := range controlChan {
					_ = conn.WriteJSON(api.InstanceExecControl{
						Command: msg.Command,
						Args:    msg.Args,
					})
				}
			},
		}

		op, err := d.client.ExecInstance(name, execReq, execArgs)
		if err != nil {
			return fmt.Errorf("starting interactive exec: %w", err)
		}

		return op.Wait()
	})
}

func (d *Driver) CreateInstanceFile(ctx context.Context, name string, path string, content io.Reader, mode int, uid, gid int64) error {
	var readSeeker io.ReadSeeker
	if rs, ok := content.(io.ReadSeeker); ok {
		readSeeker = rs
	} else if content != nil {
		b, err := io.ReadAll(content)
		if err != nil {
			return err
		}
		readSeeker = bytes.NewReader(b)
	}

	args := incus_client.InstanceFileArgs{
		Content:   readSeeker,
		UID:       uid,
		GID:       gid,
		Mode:      mode,
		Type:      "file",
		WriteMode: "overwrite",
	}
	return d.client.CreateInstanceFile(name, path, args)
}

func (d *Driver) DeleteInstanceFile(ctx context.Context, name string, path string) error {
	return d.client.DeleteInstanceFile(name, path)
}

func (d *Driver) GetIP(ctx context.Context, name string) (string, error) {
	state, _, err := d.client.GetInstanceState(name)
	if err != nil {
		return "", err
	}
	pState := toProviderInstanceState(state)
	if pState != nil {
		return common.ExtractIPv4(pState.Network)
	}
	return "", fmt.Errorf("no state available for %q", name)
}

// ============================================================================
// Helpers & Mapping Functions
// ============================================================================

func toProviderInstance(inst *api.Instance, etag string) *provider.Instance {
	if inst == nil {
		return nil
	}
	cfg := inst.Config
	if inst.Type == string(api.InstanceTypeVM) {
		cfg = common.TranslateDaemonToBootMode(provider.InstanceTypeVM, cfg)
	}

	return &provider.Instance{
		Name:            inst.Name,
		Type:            provider.InstanceType(inst.Type),
		Status:          inst.Status,
		StatusCode:      int(inst.StatusCode),
		Architecture:    inst.Architecture,
		Location:        inst.Location,
		Description:     inst.Description,
		Config:          cfg,
		ExpandedConfig:  inst.ExpandedConfig,
		Devices:         inst.Devices,
		ExpandedDevices: inst.ExpandedDevices,
		Profiles:        inst.Profiles,
		Ephemeral:       inst.Ephemeral,
		ETag:            etag,
		CreatedAt:       inst.CreatedAt,
		LastUsedAt:      inst.LastUsedAt,
	}
}

func toProviderInstanceFull(inst *api.InstanceFull) *provider.Instance {
	if inst == nil {
		return nil
	}
	pInst := toProviderInstance(&inst.Instance, "")
	if inst.State != nil {
		pInst.State = toProviderInstanceState(inst.State)
	}
	if len(inst.Snapshots) > 0 {
		pInst.HasSnapshots = true
		snaps := make([]provider.Snapshot, len(inst.Snapshots))
		for i, s := range inst.Snapshots {
			snaps[i] = provider.Snapshot{
				Name:      s.Name,
				CreatedAt: s.CreatedAt,
				Stateful:  s.Stateful,
			}
		}
		pInst.Snapshots = snaps
	}
	return pInst
}

func toProviderInstanceState(state *api.InstanceState) *provider.InstanceState {
	if state == nil {
		return nil
	}
	netMap := make(map[string]provider.InstanceStateNetwork, len(state.Network))
	for name, net := range state.Network {
		addrs := make([]provider.InstanceStateNetworkAddress, len(net.Addresses))
		for j, addr := range net.Addresses {
			addrs[j] = provider.InstanceStateNetworkAddress{
				Family:  addr.Family,
				Address: addr.Address,
				Netmask: addr.Netmask,
				Scope:   addr.Scope,
			}
		}
		netMap[name] = provider.InstanceStateNetwork{
			Addresses: addrs,
			Counters: provider.InstanceStateNetworkCounters{
				BytesReceived:   uint64(max(0, net.Counters.BytesReceived)),
				BytesSent:       uint64(max(0, net.Counters.BytesSent)),
				PacketsReceived: uint64(max(0, net.Counters.PacketsReceived)),
				PacketsSent:     uint64(max(0, net.Counters.PacketsSent)),
			},
			Hwaddr:   net.Hwaddr,
			Mtu:      net.Mtu,
			State:    net.State,
			Type:     net.Type,
			HostName: net.HostName,
		}
	}

	return &provider.InstanceState{
		Status:     state.Status,
		StatusCode: int(state.StatusCode),
		Pid:        state.Pid,
		Processes:  state.Processes,
		Network:    netMap,
	}
}

func toProviderNetworkACL(acl *api.NetworkACL, etag string) provider.NetworkACL {
	if acl == nil {
		return provider.NetworkACL{}
	}
	egress := make([]provider.NetworkACLRule, len(acl.Egress))
	for i, r := range acl.Egress {
		egress[i] = provider.NetworkACLRule{
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
	ingress := make([]provider.NetworkACLRule, len(acl.Ingress))
	for i, r := range acl.Ingress {
		ingress[i] = provider.NetworkACLRule{
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
	return provider.NetworkACL{
		Name:        acl.Name,
		Description: acl.Description,
		Egress:      egress,
		Ingress:     ingress,
		Config:      acl.Config,
		ETag:        etag,
	}
}

func toIncusRules(rules []provider.NetworkACLRule) []api.NetworkACLRule {
	result := make([]api.NetworkACLRule, len(rules))
	for i, r := range rules {
		result[i] = api.NetworkACLRule{
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
	return result
}

func toIncusInstancePost(req provider.InstanceCreateRequest) api.InstancesPost {
	cfg := common.TranslateBootModeToDaemon(req.Type, req.Config)

	return api.InstancesPost{
		Name: req.Name,
		Type: api.InstanceType(req.Type),
		Source: api.InstanceSource{
			Type:        req.Source.Type,
			Alias:       req.Source.Alias,
			Fingerprint: req.Source.Fingerprint,
			Server:      req.Source.Server,
			Protocol:    req.Source.Protocol,
			Secret:      req.Source.Secret,
		},
		InstancePut: api.InstancePut{
			Description: req.Description,
			Config:      cfg,
			Devices:     req.Devices,
			Profiles:    req.Profiles,
			Ephemeral:   req.Ephemeral,
		},
	}
}
