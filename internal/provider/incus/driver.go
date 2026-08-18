package incus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	incus_client "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/gorilla/websocket"
	"golang.org/x/term"

	"github.com/aiyor/lxm/internal/provider"
)

type Driver struct {
	client  incus_client.InstanceServer
	project string
	target  string
}

var _ provider.Driver = (*Driver)(nil)

// NewDriver wraps an existing Incus InstanceServer as a provider.Driver.
func NewDriver(client incus_client.InstanceServer) *Driver {
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
			cand := incusDir + "/unix.socket"
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

// NewRemoteDriver connects to a remote Incus HTTPS endpoint using mTLS.
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

func waitOpContext(ctx context.Context, op incus_client.Operation) error {
	if op == nil {
		return nil
	}
	if ctx == nil {
		return op.Wait()
	}
	done := make(chan error, 1)
	go func() {
		done <- op.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = op.Cancel()
		return ctx.Err()
	}
}

func (d *Driver) GetInstance(name string) (*provider.Instance, string, error) {
	inst, etag, err := d.client.GetInstance(name)
	if err != nil {
		return nil, "", err
	}
	return toProviderInstance(inst, etag), etag, nil
}

func (d *Driver) ListInstances() ([]provider.Instance, error) {
	fullInstances, err := d.client.GetInstancesFull(api.InstanceTypeAny)
	if err != nil {
		return nil, err
	}
	result := make([]provider.Instance, len(fullInstances))
	for i, inst := range fullInstances {
		result[i] = *toProviderInstance(&inst.Instance, "")
	}
	return result, nil
}

func (d *Driver) CreateInstance(req provider.InstanceCreateRequest) error {
	return d.CreateInstanceContext(context.Background(), req)
}

func (d *Driver) CreateInstanceContext(ctx context.Context, req provider.InstanceCreateRequest) error {
	incusReq := toIncusInstancePost(req)
	op, err := d.client.CreateInstance(incusReq)
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (d *Driver) UpdateInstance(name string, req provider.InstanceUpdateRequest, etag string) error {
	return d.UpdateInstanceContext(context.Background(), name, req, etag)
}

func (d *Driver) UpdateInstanceContext(ctx context.Context, name string, req provider.InstanceUpdateRequest, etag string) error {
	incusPut := api.InstancePut{
		Config:      req.Config,
		Devices:     req.Devices,
		Profiles:    req.Profiles,
		Description: req.Description,
	}
	op, err := d.client.UpdateInstance(name, incusPut, etag)
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (d *Driver) DeleteInstance(name string) error {
	return d.DeleteInstanceContext(context.Background(), name)
}

func (d *Driver) DeleteInstanceContext(ctx context.Context, name string) error {
	op, err := d.client.DeleteInstance(name)
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (d *Driver) UpdateInstanceState(name string, action string, force bool) error {
	return d.UpdateInstanceStateContext(context.Background(), name, action, force)
}

func (d *Driver) UpdateInstanceStateContext(ctx context.Context, name string, action string, force bool) error {
	op, err := d.client.UpdateInstanceState(name, api.InstanceStatePut{Action: action, Force: force, Timeout: -1}, "")
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (d *Driver) RebuildInstance(name string, req provider.InstanceRebuildRequest) error {
	return d.RebuildInstanceContext(context.Background(), name, req)
}

func (d *Driver) RebuildInstanceContext(ctx context.Context, name string, req provider.InstanceRebuildRequest) error {
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
	return waitOpContext(ctx, op)
}

func (d *Driver) CreateInstanceSnapshot(name string, snapName string, stateful bool) error {
	return d.CreateInstanceSnapshotContext(context.Background(), name, snapName, stateful)
}

func (d *Driver) CreateInstanceSnapshotContext(ctx context.Context, name string, snapName string, stateful bool) error {
	op, err := d.client.CreateInstanceSnapshot(name, api.InstanceSnapshotsPost{
		Name:     snapName,
		Stateful: stateful,
	})
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (d *Driver) DeleteInstanceSnapshot(name string, snapName string) error {
	return d.DeleteInstanceSnapshotContext(context.Background(), name, snapName)
}

func (d *Driver) DeleteInstanceSnapshotContext(ctx context.Context, name string, snapName string) error {
	op, err := d.client.DeleteInstanceSnapshot(name, snapName)
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (d *Driver) GetInstanceSnapshots(name string) ([]provider.Snapshot, error) {
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

func (d *Driver) RestoreInstanceSnapshot(name string, snapName string) error {
	return d.RestoreInstanceSnapshotContext(context.Background(), name, snapName)
}

func (d *Driver) RestoreInstanceSnapshotContext(ctx context.Context, name string, snapName string) error {
	op, err := d.client.UpdateInstance(name, api.InstancePut{Restore: snapName}, "")
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (d *Driver) GetNetworks() ([]provider.Network, error) {
	nets, err := d.client.GetNetworks()
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

func (d *Driver) GetNetwork(name string) (*provider.Network, string, error) {
	n, etag, err := d.client.GetNetwork(name)
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
		ETag:        etag,
	}, etag, nil
}

func (d *Driver) CreateNetwork(net provider.NetworkCreateRequest) error {
	return d.client.CreateNetwork(api.NetworksPost{
		NetworkPut: api.NetworkPut{
			Description: net.Description,
			Config:      net.Config,
		},
		Name: net.Name,
		Type: net.Type,
	})
}

func (d *Driver) UpdateNetwork(name string, net provider.NetworkUpdateRequest, etag string) error {
	return d.client.UpdateNetwork(name, api.NetworkPut{
		Description: net.Description,
		Config:      net.Config,
	}, etag)
}

func (d *Driver) DeleteNetwork(name string) error {
	return d.client.DeleteNetwork(name)
}

func (d *Driver) GetNetworkACLs() ([]provider.NetworkACL, error) {
	acls, err := d.client.GetNetworkACLs()
	if err != nil {
		return nil, err
	}
	result := make([]provider.NetworkACL, len(acls))
	for i, a := range acls {
		result[i] = toProviderNetworkACL(&a, "")
	}
	return result, nil
}

func (d *Driver) GetNetworkACL(name string) (*provider.NetworkACL, string, error) {
	acl, etag, err := d.client.GetNetworkACL(name)
	if err != nil {
		return nil, "", err
	}
	res := toProviderNetworkACL(acl, etag)
	return &res, etag, nil
}

func (d *Driver) CreateNetworkACL(acl provider.NetworkACLCreateRequest) error {
	return d.client.CreateNetworkACL(api.NetworkACLsPost{
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

func (d *Driver) UpdateNetworkACL(name string, acl provider.NetworkACLUpdateRequest, etag string) error {
	return d.client.UpdateNetworkACL(name, api.NetworkACLPut{
		Description: acl.Description,
		Egress:      toIncusRules(acl.Egress),
		Ingress:     toIncusRules(acl.Ingress),
		Config:      acl.Config,
	}, etag)
}

func (d *Driver) DeleteNetworkACL(name string) error {
	return d.client.DeleteNetworkACL(name)
}

func (d *Driver) GetStoragePoolNames() ([]string, error) {
	return d.client.GetStoragePoolNames()
}

func (d *Driver) GetStoragePoolVolume(pool, volType, name string) (*provider.StorageVolume, string, error) {
	v, etag, err := d.client.GetStoragePoolVolume(pool, volType, name)
	if err != nil {
		return nil, "", err
	}
	return &provider.StorageVolume{
		Name:        v.Name,
		Type:        v.Type,
		ContentType: v.ContentType,
		Description: v.Description,
		Config:      v.Config,
		Location:    v.Location,
		ETag:        etag,
	}, etag, nil
}

func (d *Driver) GetStoragePoolVolumes(pool string) ([]provider.StorageVolume, error) {
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
			Config:      v.Config,
			Location:    v.Location,
		}
	}
	return result, nil
}

func (d *Driver) CreateStoragePoolVolume(pool string, vol provider.StorageVolumeCreateRequest) error {
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

func (d *Driver) UpdateStoragePoolVolume(pool, volType, name string, vol provider.StorageVolumeUpdateRequest, etag string) error {
	return d.client.UpdateStoragePoolVolume(pool, volType, name, api.StorageVolumePut{
		Description: vol.Description,
		Config:      vol.Config,
	}, etag)
}

func (d *Driver) DeleteStoragePoolVolume(pool, volType, name string) error {
	return d.client.DeleteStoragePoolVolume(pool, volType, name)
}

func (d *Driver) GetImages() ([]provider.Image, error) {
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

func (d *Driver) GetImageAliases() ([]provider.ImageAlias, error) {
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
	return waitOpContext(ctx, op)
}

func (d *Driver) IsClustered() bool {
	server, _, err := d.client.GetServer()
	if err != nil {
		return false
	}
	return server.Environment.ServerClustered
}

func (d *Driver) GetClusterMembers() ([]provider.ClusterMember, error) {
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

func (d *Driver) GetClusterMember(name string) (*provider.ClusterMember, error) {
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

func (d *Driver) GetProjects() ([]string, error) {
	return d.client.GetProjectNames()
}

func (d *Driver) ProjectExists(name string) (bool, error) {
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

func (d *Driver) CreateProject(name string, description string) error {
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
	if err == nil {
		return 0, false
	}

	errStr := err.Error()
	var apiErr api.StatusError
	if errors.As(err, &apiErr) {
		code := apiErr.Status()
		if code == 404 {
			if intent == "lookup" {
				return 5, false // TARGET_NOT_FOUND
			}
			return 0, false // existence check -> create signal
		}
		if isETagConflictMessage(errStr) || code == 412 {
			return 4, true // PROVIDER_ERROR, ETag mismatch retryable
		}
		return 4, false
	}

	if strings.Contains(errStr, "not found") {
		if intent == "lookup" {
			return 5, false
		}
		return 0, false
	}

	if isETagConflictMessage(errStr) || strings.Contains(errStr, "412") {
		return 4, true
	}

	return 4, false
}

func isETagConflictMessage(errStr string) bool {
	lower := strings.ToLower(errStr)
	return strings.Contains(lower, "etag mismatch") ||
		strings.Contains(lower, "etag does not match") ||
		strings.Contains(lower, "configuration has been modified since this change began")
}

func (d *Driver) ResolveUID(name string, username string) (uint32, error) {
	if username == "root" {
		return 0, nil
	}

	res, err := d.ExecInstance(name, []string{"id", "-u", username}, 0, nil)
	if err != nil {
		return 0, fmt.Errorf("resolving UID for %q: %w", username, err)
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("resolving UID for %q: id exited with code %d", username, res.ExitCode)
	}

	lines := strings.Split(strings.TrimSpace(res.Combined()), "\n")
	uidStr := strings.TrimSpace(lines[0])
	uid, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parsing UID %q for %q: %w", uidStr, username, err)
	}

	return uint32(uid), nil
}

func (d *Driver) ResolveUserEnv(name string, username string) (*provider.UserEnv, error) {
	res, err := d.ExecInstance(name, []string{"getent", "passwd", username}, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("getting passwd entry for %q: %w", username, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("user %q not found in container (getent exited with code %d)", username, res.ExitCode)
	}

	lines := strings.Split(strings.TrimSpace(res.Combined()), "\n")
	firstLine := strings.TrimSpace(lines[0])
	parts := strings.Split(firstLine, ":")
	if len(parts) < 7 {
		return nil, fmt.Errorf("malformed passwd entry for %q: %s", username, res.Combined())
	}

	uid, _ := strconv.ParseUint(parts[2], 10, 32)
	gid, _ := strconv.ParseUint(parts[3], 10, 32)

	return &provider.UserEnv{
		UID:   uint32(uid),
		GID:   uint32(gid),
		Home:  parts[5],
		Shell: parts[6],
		User:  username,
	}, nil
}

func (d *Driver) ExecInstance(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
	return d.ExecInstanceContext(context.Background(), name, cmd, uid, env)
}

func (d *Driver) ExecInstanceContext(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
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
	waitErr := waitOpContext(ctx, op)

	meta := op.Get()
	var exitCode int = -1
	if returnVal, ok := meta.Metadata["return"]; ok {
		if codeFloat, ok := returnVal.(float64); ok {
			exitCode = int(codeFloat)
		}
	}

	res := provider.ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}

	if waitErr != nil {
		return res, waitErr
	}
	return res, nil
}

func (d *Driver) InteractiveExecInstance(name string, cmd []string, uid uint32, env map[string]string) error {
	stdinFd := int(os.Stdin.Fd())
	var oldState *term.State
	var isTerminal bool

	if term.IsTerminal(stdinFd) {
		isTerminal = true
		state, err := term.MakeRaw(stdinFd)
		if err != nil {
			return fmt.Errorf("setting terminal to raw mode: %w", err)
		}
		oldState = state
		defer func() {
			_ = term.Restore(stdinFd, oldState)
		}()
	}

	width, height := 80, 24
	if isTerminal {
		if w, h, err := term.GetSize(stdinFd); err == nil {
			width, height = w, h
		}
	}

	execReq := api.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: true,
		Width:       width,
		Height:      height,
		User:        uid,
		Environment: env,
	}

	controlChan := make(chan *websocket.Conn, 1)

	execArgs := &incus_client.InstanceExecArgs{
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Control:  func(conn *websocket.Conn) { controlChan <- conn },
		DataDone: make(chan bool),
	}

	op, err := d.client.ExecInstance(name, execReq, execArgs)
	if err != nil {
		return fmt.Errorf("starting interactive exec: %w", err)
	}

	if isTerminal {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGWINCH)
		var wg sync.WaitGroup
		wg.Add(1)

		go func() {
			defer wg.Done()
			var controlConn *websocket.Conn
			select {
			case controlConn = <-controlChan:
			case <-execArgs.DataDone:
				return
			}

			for {
				select {
				case <-sigChan:
					w, h, err := term.GetSize(stdinFd)
					if err == nil && controlConn != nil {
						msg := api.InstanceExecControl{
							Command: "window-resize",
							Args: map[string]string{
								"width":  strconv.Itoa(w),
								"height": strconv.Itoa(h),
							},
						}
						_ = controlConn.WriteJSON(msg)
					}
				case <-execArgs.DataDone:
					return
				}
			}
		}()
		defer func() {
			signal.Stop(sigChan)
			close(sigChan)
			wg.Wait()
		}()
	}

	return op.Wait()
}

func (d *Driver) CreateInstanceFile(name string, path string, content io.Reader, mode int, uid, gid int64) error {
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

func (d *Driver) DeleteInstanceFile(name string, path string) error {
	return d.client.DeleteInstanceFile(name, path)
}

func (d *Driver) GetIP(name string) (string, error) {
	state, _, err := d.client.GetInstanceState(name)
	if err != nil {
		return "", err
	}

	for _, network := range state.Network {
		for _, addr := range network.Addresses {
			if addr.Family == "inet" && addr.Scope == "global" {
				return addr.Address, nil
			}
		}
	}

	return "", fmt.Errorf("no global IPv4 address found for instance %q", name)
}

// ============================================================================
// Helpers & Mapping Functions
// ============================================================================

func toProviderInstance(inst *api.Instance, etag string) *provider.Instance {
	if inst == nil {
		return nil
	}
	return &provider.Instance{
		Name:            inst.Name,
		Type:            provider.InstanceType(inst.Type),
		Status:          inst.Status,
		StatusCode:      int(inst.StatusCode),
		Architecture:    inst.Architecture,
		Location:        inst.Location,
		Config:          inst.Config,
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
			Config:    req.Config,
			Devices:   req.Devices,
			Profiles:  req.Profiles,
			Ephemeral: req.Ephemeral,
		},
	}
}
