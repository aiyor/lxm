package lxd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	lxd_client "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"
)

// FakeInstanceServer is an in-memory implementation of InstanceService for unit and integration testing.
type FakeInstanceServer struct {
	mu sync.Mutex

	Instances   map[string]*api.Instance
	ETags       map[string]string
	Files       map[string]map[string][]byte // containerName -> path -> content
	IPs         map[string]string
	Extensions  map[string]bool
	RebuiltLogs []string
	Snapshots   map[string]map[string]*api.InstanceSnapshot // containerName -> snapName -> snapshot
	Nets        *Networks                                   // network + ACL backing state
	Vols        *VolumeStore                                // custom storage volume backing state

	// Optional custom hook functions
	GetInstanceFunc         func(name string) (*api.Instance, string, error)
	CreateInstanceFunc      func(req api.InstancesPost) error
	UpdateInstanceFunc      func(name string, put api.InstancePut, etag string) error
	DeleteInstanceFunc      func(name string) error
	UpdateInstanceStateFunc func(name string, action string, force bool) error
	RebuildInstanceFunc     func(name string, req api.InstanceRebuildPost) error
	ExecInstanceFunc        func(name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error)
	CreateNetworkFunc       func(req api.NetworksPost) error
	CreateNetworkACLFunc    func(acl api.NetworkACLsPost) error
	GetNetworksFunc         func() ([]api.Network, error)
	GetNetworkACLsFunc      func() ([]api.NetworkACL, error)
}

// NewFakeInstanceServer creates a new initialized FakeInstanceServer.
func NewFakeInstanceServer() *FakeInstanceServer {
	f := &FakeInstanceServer{
		Instances:  make(map[string]*api.Instance),
		ETags:      make(map[string]string),
		Files:      make(map[string]map[string][]byte),
		IPs:        make(map[string]string),
		Extensions: map[string]bool{"instances_rebuild": true, "custom_block_volumes": true},
		Snapshots:  make(map[string]map[string]*api.InstanceSnapshot),
	}
	f.AddNetworks()
	f.AddStorage()
	return f
}

var _ InstanceService = (*FakeInstanceServer)(nil)

func (f *FakeInstanceServer) GetInstance(name string) (*api.Instance, string, error) {
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

func (f *FakeInstanceServer) CreateInstance(req api.InstancesPost) error {
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

	inst := &api.Instance{
		Name:         req.Name,
		Type:         string(req.Type),
		Status:       "Stopped",
		StatusCode:   api.Stopped,
		Architecture: req.Architecture,
		Config:       cfg,
		Devices:      req.Devices,
		Ephemeral:    req.Ephemeral,
		Profiles:     req.Profiles,
		Description:  req.Description,
	}

	f.Instances[req.Name] = inst
	f.ETags[req.Name] = "fake-etag-created"
	return nil
}

func (f *FakeInstanceServer) UpdateInstance(name string, put api.InstancePut, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.UpdateInstanceFunc != nil {
		return f.UpdateInstanceFunc(name, put, etag)
	}

	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	currentETag := f.ETags[name]
	if etag != "" && currentETag != "" && etag != currentETag {
		// Faithful to the real LXD daemon's 412 response text (UG5 B1):
		// classification must be exercised against authentic input.
		return fmt.Errorf("%s: %s vs %s. The configuration has been modified since this change began. Please retrieve the updated configuration before proceeding.", ETagConflictPrefix, etag, currentETag)
	}

	// Match real LXD QEMU driver semantics (driver_qemu.go:6046):
	// non-live-updatable keys cannot be modified while VM is running.
	if inst.Type == "virtual-machine" && (inst.Status == "Running" || inst.StatusCode == api.Running) {
		if put.Config != nil {
			if put.Config["limits.memory.hugepages"] != inst.Config["limits.memory.hugepages"] {
				return fmt.Errorf("key %q cannot be updated when VM is running", "limits.memory.hugepages")
			}
			if put.Config["raw.qemu"] != inst.Config["raw.qemu"] {
				return fmt.Errorf("key %q cannot be updated when VM is running", "raw.qemu")
			}
		}
	}

	inst.Architecture = put.Architecture
	inst.Config = put.Config
	inst.Devices = put.Devices
	inst.Ephemeral = put.Ephemeral
	inst.Profiles = put.Profiles
	inst.Description = put.Description

	f.ETags[name] = "fake-etag-updated"
	return nil
}

func (f *FakeInstanceServer) DeleteInstance(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.DeleteInstanceFunc != nil {
		return f.DeleteInstanceFunc(name)
	}

	if _, ok := f.Instances[name]; !ok {
		return fmt.Errorf("instance %q not found", name)
	}
	// Match real LXD semantics for a non-forced delete: a running instance
	// cannot be deleted. The executor stops instances before deleting them,
	// so this catches regressions where a delete is attempted while running.
	// StatusCode is LXD's authoritative state field (IsRunning() is
	// code-based), so both this check and the stop check below key off it.
	if f.Instances[name].StatusCode == api.Running {
		return errors.New("Instance is running")
	}
	delete(f.Instances, name)
	delete(f.ETags, name)
	delete(f.Files, name)
	delete(f.IPs, name)
	return nil
}

func (f *FakeInstanceServer) UpdateInstanceState(name string, action string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.UpdateInstanceStateFunc != nil {
		return f.UpdateInstanceStateFunc(name, action, force)
	}

	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	switch action {
	case "start":
		inst.Status = "Running"
		inst.StatusCode = api.Running
	case "stop":
		// Match real LXD semantics: stopping an already-stopped instance is
		// an error ("The instance is already stopped"). The executor checks
		// the live state before stopping, so this catches regressions where
		// a stop is attempted on an already-stopped instance.
		if inst.StatusCode == api.Stopped {
			return errors.New("The instance is already stopped")
		}
		inst.Status = "Stopped"
		inst.StatusCode = api.Stopped
	case "restart":
		inst.Status = "Running"
		inst.StatusCode = api.Running
	}
	return nil
}

func (f *FakeInstanceServer) RebuildInstance(name string, req api.InstanceRebuildPost) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.RebuildInstanceFunc != nil {
		return f.RebuildInstanceFunc(name, req)
	}

	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	if inst.Config == nil {
		inst.Config = make(map[string]string)
	}
	if req.Source.Properties != nil {
		inst.Config["image.os"] = req.Source.Properties["os"]
		inst.Config["image.release"] = req.Source.Properties["release"]
	}
	f.RebuiltLogs = append(f.RebuiltLogs, name)
	f.ETags[name] = "fake-etag-rebuilt"
	return nil
}

func (f *FakeInstanceServer) HasExtension(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Extensions[name]
}

func (f *FakeInstanceServer) ClassifyLXDError(err error, intent string) (int, bool) {
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
	if isETagConflictMessage(errStr) || strings.Contains(errStr, "412") {
		return 4, true
	}
	return 4, false
}

func (f *FakeInstanceServer) ResolveUID(name string, username string) (uint32, error) {
	if username == "root" {
		return 0, nil
	}
	if username == "ubuntu" {
		return 1000, nil
	}
	return 1000, nil
}

func (f *FakeInstanceServer) ResolveUserEnv(name string, username string) (*UserEnv, error) {
	if username == "root" {
		return &UserEnv{
			UID:   0,
			GID:   0,
			Home:  "/root",
			Shell: "/bin/bash",
			User:  "root",
		}, nil
	}
	return &UserEnv{
		UID:   1000,
		GID:   1000,
		Home:  "/home/" + username,
		Shell: "/bin/bash",
		User:  username,
	}, nil
}

func (f *FakeInstanceServer) ExecInstance(name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error) {
	if f.ExecInstanceFunc != nil {
		return f.ExecInstanceFunc(name, cmd, uid, env)
	}
	return ExecResult{ExitCode: 0, Stdout: "fake output", Stderr: ""}, nil
}

func (f *FakeInstanceServer) InteractiveExecInstance(name string, cmd []string, uid uint32, env map[string]string) error {
	return nil
}

func (f *FakeInstanceServer) CreateInstanceFile(name string, path string, args lxd_client.InstanceFileArgs) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Files[name] == nil {
		f.Files[name] = make(map[string][]byte)
	}
	buf := new(bytes.Buffer)
	if args.Content != nil {
		if _, err := buf.ReadFrom(args.Content); err != nil {
			return err
		}
	}
	f.Files[name][path] = buf.Bytes()
	return nil
}

func (f *FakeInstanceServer) DeleteInstanceFile(name string, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Files[name] != nil {
		delete(f.Files[name], path)
	}
	return nil
}

func (f *FakeInstanceServer) GetIP(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if ip, ok := f.IPs[name]; ok {
		return ip, nil
	}
	return "10.211.55.100", nil
}

func (f *FakeInstanceServer) ListInstances() ([]api.InstanceFull, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var result []api.InstanceFull
	for _, inst := range f.Instances {
		snaps := f.Snapshots[inst.Name]
		var snapList []api.InstanceSnapshot
		for _, s := range snaps {
			snapList = append(snapList, *s)
		}
		var state *api.InstanceState
		if ip, ok := f.IPs[inst.Name]; ok && ip != "" {
			state = &api.InstanceState{
				Network: map[string]api.InstanceStateNetwork{
					"eth0": {
						Addresses: []api.InstanceStateNetworkAddress{
							{Family: "inet", Address: ip},
						},
					},
				},
			}
		}
		result = append(result, api.InstanceFull{
			Instance:  *inst,
			Snapshots: snapList,
			State:     state,
		})
	}
	return result, nil
}

func (f *FakeInstanceServer) CreateInstanceSnapshot(name string, req api.InstanceSnapshotsPost) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.Instances[name]; !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	if f.Snapshots[name] == nil {
		f.Snapshots[name] = make(map[string]*api.InstanceSnapshot)
	}

	snapName := req.Name
	if snapName == "" {
		snapName = fmt.Sprintf("snap-%d", len(f.Snapshots[name])+1)
	}

	f.Snapshots[name][snapName] = &api.InstanceSnapshot{
		Name:      snapName,
		Stateful:  req.Stateful,
		Ephemeral: false,
	}
	return nil
}

func (f *FakeInstanceServer) DeleteInstanceSnapshot(name string, snapshotName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.Snapshots[name] != nil {
		delete(f.Snapshots[name], snapshotName)
	}
	return nil
}

func (f *FakeInstanceServer) GetInstanceSnapshots(name string) ([]api.InstanceSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.Instances[name]; !ok {
		return nil, fmt.Errorf("instance %q not found", name)
	}

	var result []api.InstanceSnapshot
	if snaps, ok := f.Snapshots[name]; ok {
		for _, s := range snaps {
			result = append(result, *s)
		}
	}
	return result, nil
}

func (f *FakeInstanceServer) RestoreInstanceSnapshot(name string, snapshotName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.Instances[name]; !ok {
		return fmt.Errorf("instance %q not found", name)
	}

	f.ETags[name] = "fake-etag-restored"
	return nil
}

func (f *FakeInstanceServer) CreateInstanceContext(ctx context.Context, req api.InstancesPost) error {
	return f.CreateInstance(req)
}

func (f *FakeInstanceServer) UpdateInstanceContext(ctx context.Context, name string, put api.InstancePut, etag string) error {
	return f.UpdateInstance(name, put, etag)
}

func (f *FakeInstanceServer) DeleteInstanceContext(ctx context.Context, name string) error {
	return f.DeleteInstance(name)
}

func (f *FakeInstanceServer) UpdateInstanceStateContext(ctx context.Context, name string, action string, force bool) error {
	return f.UpdateInstanceState(name, action, force)
}

func (f *FakeInstanceServer) RebuildInstanceContext(ctx context.Context, name string, req api.InstanceRebuildPost) error {
	return f.RebuildInstance(name, req)
}

func (f *FakeInstanceServer) ExecInstanceContext(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error) {
	return f.ExecInstance(name, cmd, uid, env)
}

func (f *FakeInstanceServer) CreateInstanceSnapshotContext(ctx context.Context, name string, req api.InstanceSnapshotsPost) error {
	return f.CreateInstanceSnapshot(name, req)
}

func (f *FakeInstanceServer) DeleteInstanceSnapshotContext(ctx context.Context, name string, snapshotName string) error {
	return f.DeleteInstanceSnapshot(name, snapshotName)
}

func (f *FakeInstanceServer) RestoreInstanceSnapshotContext(ctx context.Context, name string, snapshotName string) error {
	return f.RestoreInstanceSnapshot(name, snapshotName)
}
