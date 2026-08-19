package lxm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/common"
	"github.com/aiyor/lxm/internal/provider/fake"
)

type mockDriver struct {
	*fake.FakeDriver
	getInstanceFunc         func(ctx context.Context, name string) (*provider.Instance, string, error)
	createInstanceFunc      func(ctx context.Context, req provider.InstanceCreateRequest) error
	updateInstanceFunc      func(ctx context.Context, name string, req provider.InstanceUpdateRequest, etag string) error
	deleteInstanceFunc      func(ctx context.Context, name string) error
	updateInstanceStateFunc func(ctx context.Context, name string, action string, force bool) error
	resolveUIDFunc          func(ctx context.Context, name string, username string) (uint32, error)
	resolveUserEnvFunc      func(ctx context.Context, name string, username string) (*provider.UserEnv, error)
	execInstanceFunc        func(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (int, string, error)
	interactiveExecFunc     func(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) error
	createInstanceFileFunc  func(ctx context.Context, name string, path string, content io.Reader, mode int, uid, gid int64) error
	deleteInstanceFileFunc  func(ctx context.Context, name string, path string) error
	getIPFunc               func(ctx context.Context, name string) (string, error)
}

func newMockDriver() *mockDriver {
	return &mockDriver{
		FakeDriver: fake.New(),
	}
}

func (m *mockDriver) GetInstance(ctx context.Context, name string) (*provider.Instance, string, error) {
	if m.getInstanceFunc != nil {
		return m.getInstanceFunc(ctx, name)
	}
	return nil, "", errors.New("not found")
}

func (m *mockDriver) CreateInstance(ctx context.Context, req provider.InstanceCreateRequest) error {
	if m.createInstanceFunc != nil {
		return m.createInstanceFunc(ctx, req)
	}
	return nil
}

func (m *mockDriver) UpdateInstance(ctx context.Context, name string, req provider.InstanceUpdateRequest, etag string) error {
	if m.updateInstanceFunc != nil {
		return m.updateInstanceFunc(ctx, name, req, etag)
	}
	return nil
}

func (m *mockDriver) DeleteInstance(ctx context.Context, name string) error {
	if m.deleteInstanceFunc != nil {
		return m.deleteInstanceFunc(ctx, name)
	}
	return nil
}

func (m *mockDriver) UpdateInstanceState(ctx context.Context, name string, action string, force bool) error {
	if m.updateInstanceStateFunc != nil {
		return m.updateInstanceStateFunc(ctx, name, action, force)
	}
	return nil
}

func (m *mockDriver) ClassifyError(err error, intent string) (int, bool) {
	if err == nil {
		return 0, false
	}
	if strings.Contains(err.Error(), "not found") {
		if intent == "lookup" {
			return 5, false
		}
		return 0, false
	}
	return 4, false
}

func (m *mockDriver) ResolveUID(ctx context.Context, name string, username string) (uint32, error) {
	if m.resolveUIDFunc != nil {
		return m.resolveUIDFunc(ctx, name, username)
	}
	return 0, nil
}

func (m *mockDriver) ResolveUserEnv(ctx context.Context, name string, username string) (*provider.UserEnv, error) {
	if m.resolveUserEnvFunc != nil {
		return m.resolveUserEnvFunc(ctx, name, username)
	}
	return &provider.UserEnv{UID: 1000, GID: 1000, Home: "/home/" + username, Shell: "/bin/bash", User: username}, nil
}

func (m *mockDriver) ExecInstance(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
	if m.execInstanceFunc != nil {
		code, out, err := m.execInstanceFunc(ctx, name, cmd, uid, env)
		return provider.ExecResult{ExitCode: code, Stdout: out}, err
	}
	return provider.ExecResult{ExitCode: 0, Stdout: ""}, nil
}

func (m *mockDriver) InteractiveExecInstance(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) error {
	if m.interactiveExecFunc != nil {
		return m.interactiveExecFunc(ctx, name, cmd, uid, env)
	}
	return nil
}

func (m *mockDriver) CreateInstanceFile(ctx context.Context, name string, path string, content io.Reader, mode int, uid, gid int64) error {
	if m.createInstanceFileFunc != nil {
		return m.createInstanceFileFunc(ctx, name, path, content, mode, uid, gid)
	}
	return nil
}

func (m *mockDriver) DeleteInstanceFile(ctx context.Context, name string, path string) error {
	if m.deleteInstanceFileFunc != nil {
		return m.deleteInstanceFileFunc(ctx, name, path)
	}
	return nil
}

func (m *mockDriver) GetIP(ctx context.Context, name string) (string, error) {
	if m.getIPFunc != nil {
		return m.getIPFunc(ctx, name)
	}
	return "10.0.0.1", nil
}

func newTestManager(mock *mockDriver) *Manager {
	return NewManager(
		mock,
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})),
		false, false, false,
	)
}

func TestApplyConfig_Absent_DeletesContainer(t *testing.T) {
	deleted := false
	stopped := false
	mock := newMockDriver()
	mock.getInstanceFunc = func(ctx context.Context, name string) (*provider.Instance, string, error) {
		return &provider.Instance{
			StatusCode: 103,
		}, "etag", nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		if action == "stop" {
			stopped = true
		}
		return nil
	}
	mock.deleteInstanceFunc = func(ctx context.Context, name string) error {
		deleted = true
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Status: "absent"}
	if err := m.ApplyConfig(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stopped {
		t.Error("expected container to be stopped")
	}
	if !deleted {
		t.Error("expected container to be deleted")
	}
}

func TestApplyConfig_NotFound_CreatesContainer(t *testing.T) {
	created := false
	started := false
	mock := newMockDriver()
	mock.getInstanceFunc = func(ctx context.Context, name string) (*provider.Instance, string, error) {
		return nil, "", errors.New("not found")
	}
	mock.createInstanceFunc = func(ctx context.Context, req provider.InstanceCreateRequest) error {
		created = true
		if req.Name != "test" {
			t.Errorf("expected name 'test', got %q", req.Name)
		}
		if req.Source.Alias != "ubuntu:22.04" {
			t.Errorf("expected alias 'ubuntu:22.04', got %q", req.Source.Alias)
		}
		return nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		if action == "start" {
			started = true
		}
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Status: "present", Image: "ubuntu:22.04"}
	if err := m.ApplyConfig(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected container to be created")
	}
	if !started {
		t.Error("expected container to be started")
	}
}

func TestApplyConfig_Exists_NoChanges(t *testing.T) {
	updated := false
	conf := &config.Config{Name: "test", Status: "present", Image: "ubuntu:22.04", User: "ubuntu"}
	userData, _ := conf.ResolveCloudInit("")
	mock := newMockDriver()
	mock.getInstanceFunc = func(ctx context.Context, name string) (*provider.Instance, string, error) {
		return &provider.Instance{
			Config:  map[string]string{"user.lxm.user": "ubuntu", "user.lxm.managed": "true", "user.user-data": userData},
			Devices: map[string]map[string]string{},
		}, "etag", nil
	}
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		return nil
	}
	m := newTestManager(mock)
	if err := m.ApplyConfig(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated {
		t.Error("expected no update when container is already up to date")
	}
}

func TestApplyConfig_Exists_NeedsUpdate(t *testing.T) {
	updated := false
	mock := newMockDriver()
	mock.getInstanceFunc = func(ctx context.Context, name string) (*provider.Instance, string, error) {
		return &provider.Instance{
			Config:  map[string]string{},
			Devices: map[string]map[string]string{},
		}, "etag", nil
	}
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		if put.Config["user.lxm.user"] != "ubuntu" {
			t.Error("expected user.lxm.user to be set in update")
		}
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Status: "present", Image: "ubuntu:22.04", User: "ubuntu"}
	if err := m.ApplyConfig(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !updated {
		t.Error("expected update when user metadata differs")
	}
}

func TestDeleteContainer_NotExists(t *testing.T) {
	mock := newMockDriver()
	mock.getInstanceFunc = func(ctx context.Context, name string) (*provider.Instance, string, error) {
		return nil, "", errors.New("not found")
	}
	m := newTestManager(mock)

	if err := m.DeleteContainer("test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteContainer_Running(t *testing.T) {
	stopped := false
	deleted := false
	mock := newMockDriver()
	mock.getInstanceFunc = func(ctx context.Context, name string) (*provider.Instance, string, error) {
		return &provider.Instance{StatusCode: 103}, "etag", nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		if action == "stop" {
			stopped = true
		}
		return nil
	}
	mock.deleteInstanceFunc = func(ctx context.Context, name string) error {
		deleted = true
		return nil
	}
	m := newTestManager(mock)

	if err := m.DeleteContainer("test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stopped {
		t.Error("expected container to be stopped before deletion")
	}
	if !deleted {
		t.Error("expected container to be deleted")
	}
}

func TestDeleteContainer_Stopped(t *testing.T) {
	stopped := false
	deleted := false
	mock := newMockDriver()
	mock.getInstanceFunc = func(ctx context.Context, name string) (*provider.Instance, string, error) {
		return &provider.Instance{StatusCode: 102}, "etag", nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		stopped = true
		return nil
	}
	mock.deleteInstanceFunc = func(ctx context.Context, name string) error {
		deleted = true
		return nil
	}
	m := newTestManager(mock)

	if err := m.DeleteContainer("test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stopped {
		t.Error("stopped container should not be stopped again")
	}
	if !deleted {
		t.Error("expected container to be deleted")
	}
}

func TestCreateContainer_ImageAlias(t *testing.T) {
	var capturedReq provider.InstanceCreateRequest
	mock := newMockDriver()
	mock.createInstanceFunc = func(ctx context.Context, req provider.InstanceCreateRequest) error {
		capturedReq = req
		return nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Image: "ubuntu:22.04", Status: "present"}
	if err := m.CreateContainer(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedReq.Source.Alias != "ubuntu:22.04" {
		t.Errorf("expected alias 'ubuntu:22.04', got %q", capturedReq.Source.Alias)
	}
	if capturedReq.Source.Fingerprint != "" {
		t.Errorf("expected empty fingerprint for alias image, got %q", capturedReq.Source.Fingerprint)
	}
}

func TestCreateContainer_ImageFingerprint(t *testing.T) {
	var capturedReq provider.InstanceCreateRequest
	mock := newMockDriver()
	mock.createInstanceFunc = func(ctx context.Context, req provider.InstanceCreateRequest) error {
		capturedReq = req
		return nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Image: "a1b2c3d", Status: "present"}
	if err := m.CreateContainer(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedReq.Source.Fingerprint != "a1b2c3d" {
		t.Errorf("expected fingerprint 'a1b2c3d', got %q", capturedReq.Source.Fingerprint)
	}
	if capturedReq.Source.Alias != "" {
		t.Errorf("expected empty alias for fingerprint image, got %q", capturedReq.Source.Alias)
	}
}

func TestCreateContainer_WithMounts(t *testing.T) {
	dir := t.TempDir()
	var capturedReq provider.InstanceCreateRequest
	mock := newMockDriver()
	mock.createInstanceFunc = func(ctx context.Context, req provider.InstanceCreateRequest) error {
		capturedReq = req
		return nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{
		Name:   "test",
		Image:  "ubuntu:22.04",
		Status: "present",
		Mounts: []config.Mount{
			{Source: dir, Path: "/mnt/data"},
			{Source: dir, Path: "/mnt/recursive", Recursive: true},
		},
	}
	if err := m.CreateContainer(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	devName := common.DeviceName("/mnt/data")
	dev, ok := capturedReq.Devices[devName]
	if !ok {
		t.Fatalf("expected device %q", devName)
	}
	if dev["source"] != dir {
		t.Errorf("expected source %q, got %q", dir, dev["source"])
	}
	if dev["path"] != "/mnt/data" {
		t.Errorf("expected path /mnt/data, got %q", dev["path"])
	}
	if dev["shift"] != "true" {
		t.Error("expected shift=true")
	}
	if dev["type"] != "disk" {
		t.Errorf("expected type disk, got %q", dev["type"])
	}
	if _, exists := dev["recursive"]; exists {
		t.Error("expected recursive property to not be set for non-recursive mount")
	}

	recDevName := common.DeviceName("/mnt/recursive")
	recDev, ok := capturedReq.Devices[recDevName]
	if !ok {
		t.Fatalf("expected device %q", recDevName)
	}
	if recDev["source"] != dir {
		t.Errorf("expected source %q, got %q", dir, recDev["source"])
	}
	if recDev["path"] != "/mnt/recursive" {
		t.Errorf("expected path /mnt/recursive, got %q", recDev["path"])
	}
	if recDev["shift"] != "true" {
		t.Error("expected shift=true")
	}
	if recDev["type"] != "disk" {
		t.Errorf("expected type disk, got %q", recDev["type"])
	}
	if recDev["recursive"] != "true" {
		t.Errorf("expected recursive=true, got %q", recDev["recursive"])
	}
}

func TestCreateContainer_WithNetwork(t *testing.T) {
	var capturedReq provider.InstanceCreateRequest
	mock := newMockDriver()
	mock.createInstanceFunc = func(ctx context.Context, req provider.InstanceCreateRequest) error {
		capturedReq = req
		return nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{
		Name:     "test",
		Image:    "ubuntu:22.04",
		Status:   "present",
		Networks: []config.NetworkConfig{{Name: "eth0", IPv4: "10.0.0.10", Parent: "lxdbr0"}},
	}
	if err := m.CreateContainer(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dev, ok := capturedReq.Devices["eth0"]
	if !ok {
		t.Fatal("expected eth0 device")
	}
	if dev["type"] != "nic" {
		t.Errorf("expected type nic, got %q", dev["type"])
	}
	if dev["ipv4.address"] != "10.0.0.10" {
		t.Errorf("expected ipv4.address 10.0.0.10, got %q", dev["ipv4.address"])
	}
	if dev["parent"] != "lxdbr0" {
		t.Errorf("expected parent lxdbr0, got %q", dev["parent"])
	}
	if dev["user.lxm.managed"] != "true" {
		t.Error("expected user.lxm.managed=true")
	}
}

func TestCreateContainer_DryRun(t *testing.T) {
	created := false
	started := false
	mock := newMockDriver()
	mock.createInstanceFunc = func(ctx context.Context, req provider.InstanceCreateRequest) error {
		created = true
		return nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		started = true
		return nil
	}
	m := NewManager(
		mock,
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})),
		true, false, false,
	)

	conf := &config.Config{Name: "test", Image: "ubuntu:22.04", Status: "present"}
	if err := m.CreateContainer(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created {
		t.Error("dry-run should not create container")
	}
	if started {
		t.Error("dry-run should not start container")
	}
}

func TestCreateContainer_CloudInitAndUser(t *testing.T) {
	var capturedReq provider.InstanceCreateRequest
	mock := newMockDriver()
	mock.createInstanceFunc = func(ctx context.Context, req provider.InstanceCreateRequest) error {
		capturedReq = req
		return nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{
		Name:      "test",
		Image:     "ubuntu:22.04",
		Status:    "present",
		User:      "deploy",
		CloudInit: "package_update: true\n",
	}
	if err := m.CreateContainer(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedReq.Config["user.lxm.user"] != "deploy" {
		t.Errorf("expected user.lxm.user 'deploy', got %q", capturedReq.Config["user.lxm.user"])
	}
	if capturedReq.Config["user.user-data"] == "" {
		t.Error("expected user.user-data to be set")
	}
}

func TestUpdateContainer_AddMount(t *testing.T) {
	dir := t.TempDir()
	updated := false
	var capturedPut provider.InstanceUpdateRequest

	instance := &provider.Instance{
		Config:  map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{},
	}
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		capturedPut = put
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{
		Name:   "test",
		Image:  "ubuntu:22.04",
		User:   "ubuntu",
		Mounts: []config.Mount{{Source: dir, Path: "/mnt/data"}},
	}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true")
	}
	if !updated {
		t.Error("expected update to be called")
	}

	devName := common.DeviceName("/mnt/data")
	dev, ok := capturedPut.Devices[devName]
	if !ok {
		t.Fatalf("expected device %q", devName)
	}
	if dev["source"] != dir {
		t.Errorf("expected source %q, got %q", dir, dev["source"])
	}
}

func TestUpdateContainer_ModifyMountRecursive(t *testing.T) {
	dir := t.TempDir()
	updated := false
	var capturedPut provider.InstanceUpdateRequest

	instance := &provider.Instance{
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"mount--mnt-data": {
				"type":   "disk",
				"source": dir,
				"path":   "/mnt/data",
				"shift":  "true",
			},
		},
	}
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		capturedPut = put
		return nil
	}
	m := newTestManager(mock)

	// Update 1: Set recursive: true. Should trigger update and add the recursive key.
	conf := &config.Config{
		Name:   "test",
		Image:  "ubuntu:22.04",
		User:   "ubuntu",
		Mounts: []config.Mount{{Source: dir, Path: "/mnt/data", Recursive: true}},
	}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when toggling recursive to true")
	}
	if !updated {
		t.Error("expected update to be called")
	}
	dev := capturedPut.Devices["mount--mnt-data"]
	if dev["recursive"] != "true" {
		t.Errorf("expected recursive=true, got %q", dev["recursive"])
	}

	// Update 2: Update same container from recursive to non-recursive.
	instance.Devices["mount--mnt-data"]["recursive"] = "true"
	updated = false
	conf2 := &config.Config{
		Name:   "test",
		Image:  "ubuntu:22.04",
		User:   "ubuntu",
		Mounts: []config.Mount{{Source: dir, Path: "/mnt/data", Recursive: false}},
	}
	changed2, err2 := m.UpdateContainer(instance, "etag", conf2, "")
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if !changed2 {
		t.Error("expected changed=true when toggling recursive to false")
	}
	if !updated {
		t.Error("expected update to be called")
	}
	dev2 := capturedPut.Devices["mount--mnt-data"]
	if _, exists := dev2["recursive"]; exists {
		t.Errorf("expected recursive to be removed/empty, got %q", dev2["recursive"])
	}
}

func TestUpdateContainer_RemoveOrphanedMount(t *testing.T) {
	updated := false
	instance := &provider.Instance{
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"mount-old": {"type": "disk", "source": "/old", "path": "/old", "shift": "true"},
		},
	}
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		if _, exists := put.Devices["mount-old"]; exists {
			t.Error("orphaned mount should have been removed")
		}
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Image: "ubuntu:22.04", User: "ubuntu"}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when removing orphaned mount")
	}
	if !updated {
		t.Error("expected update to be called")
	}
}

func TestUpdateContainer_AddNetwork(t *testing.T) {
	updated := false
	var capturedPut provider.InstanceUpdateRequest

	instance := &provider.Instance{
		Config:  map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{},
	}
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		capturedPut = put
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{
		Name:     "test",
		Image:    "ubuntu:22.04",
		User:     "ubuntu",
		Networks: []config.NetworkConfig{{Name: "eth0", IPv4: "10.0.0.10", Parent: "lxdbr0"}},
	}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true")
	}
	if !updated {
		t.Error("expected update to be called")
	}

	dev, ok := capturedPut.Devices["eth0"]
	if !ok {
		t.Fatal("expected eth0 device")
	}
	if dev["ipv4.address"] != "10.0.0.10" {
		t.Errorf("expected ipv4.address 10.0.0.10, got %q", dev["ipv4.address"])
	}
}

func TestUpdateContainer_RemoveOrphanedNetwork(t *testing.T) {
	updated := false
	instance := &provider.Instance{
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"eth0": {"type": "nic", "parent": "lxdbr0", "user.lxm.managed": "true"},
		},
	}
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		if _, exists := put.Devices["eth0"]; exists {
			t.Error("orphaned managed network should have been removed")
		}
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Image: "ubuntu:22.04", User: "ubuntu"}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when removing orphaned network")
	}
	if !updated {
		t.Error("expected update to be called")
	}
}

func TestUpdateContainer_CloudInitChange(t *testing.T) {
	updated := false
	instance := &provider.Instance{
		Config:  map[string]string{"user.user-data": "old-data", "user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{},
	}
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		if put.Config["user.user-data"] == "old-data" {
			t.Error("expected cloud-init data to be updated")
		}
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{
		Name:      "test",
		Image:     "ubuntu:22.04",
		User:      "ubuntu",
		CloudInit: "package_update: true\n",
	}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when cloud-init differs")
	}
	if !updated {
		t.Error("expected update to be called")
	}
}

func TestUpdateContainer_DryRun(t *testing.T) {
	updated := false
	instance := &provider.Instance{
		Config:  map[string]string{},
		Devices: map[string]map[string]string{},
	}
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		return nil
	}
	m := NewManager(
		mock,
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})),
		true, false, false,
	)

	conf := &config.Config{Name: "test", Image: "ubuntu:22.04", User: "ubuntu"}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true (config differs)")
	}
	if updated {
		t.Error("dry-run should not call UpdateInstance")
	}
}

func TestUpdateContainer_NilConfigAndDevices(t *testing.T) {
	updated := false
	instance := &provider.Instance{}
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Image: "ubuntu:22.04", User: "ubuntu"}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true with nil config/devices")
	}
	if !updated {
		t.Error("expected update to be called")
	}
}

func TestCreateContainer_SetsManagedFlag(t *testing.T) {
	var capturedReq provider.InstanceCreateRequest
	mock := newMockDriver()
	mock.createInstanceFunc = func(ctx context.Context, req provider.InstanceCreateRequest) error {
		capturedReq = req
		return nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Image: "ubuntu:22.04", Status: "present"}
	if err := m.CreateContainer(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedReq.Config["user.lxm.managed"] != "true" {
		t.Errorf("expected user.lxm.managed=true, got %q", capturedReq.Config["user.lxm.managed"])
	}
}

func TestCreateContainer_SetsGroups(t *testing.T) {
	var capturedReq provider.InstanceCreateRequest
	mock := newMockDriver()
	mock.createInstanceFunc = func(ctx context.Context, req provider.InstanceCreateRequest) error {
		capturedReq = req
		return nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{
		Name:   "test",
		Image:  "ubuntu:22.04",
		Status: "present",
		Groups: []string{"dev", "gpu"},
	}
	if err := m.CreateContainer(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedReq.Config["user.lxm.groups"] != "dev,gpu" {
		t.Errorf("expected groups 'dev,gpu', got %q", capturedReq.Config["user.lxm.groups"])
	}
}

func TestCreateContainer_NoGroups_NoGroupsKey(t *testing.T) {
	var capturedReq provider.InstanceCreateRequest
	mock := newMockDriver()
	mock.createInstanceFunc = func(ctx context.Context, req provider.InstanceCreateRequest) error {
		capturedReq = req
		return nil
	}
	mock.updateInstanceStateFunc = func(ctx context.Context, name string, action string, force bool) error {
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Image: "ubuntu:22.04", Status: "present"}
	if err := m.CreateContainer(conf, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := capturedReq.Config["user.lxm.groups"]; exists {
		t.Error("expected no user.lxm.groups key when groups is empty")
	}
}

func TestUpdateContainer_BackfillsManagedFlag(t *testing.T) {
	updated := false
	instance := &provider.Instance{
		Config:  map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{},
	}
	var capturedPut provider.InstanceUpdateRequest
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		capturedPut = put
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Image: "ubuntu:22.04", User: "ubuntu"}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when backfilling managed flag")
	}
	if capturedPut.Config["user.lxm.managed"] != "true" {
		t.Errorf("expected user.lxm.managed=true in update, got %q", capturedPut.Config["user.lxm.managed"])
	}
	if !updated {
		t.Error("expected updateInstance to be called")
	}
}

func TestUpdateContainer_SyncsGroups_Add(t *testing.T) {
	updated := false
	instance := &provider.Instance{
		Config:  map[string]string{"user.lxm.user": "ubuntu", "user.lxm.managed": "true"},
		Devices: map[string]map[string]string{},
	}
	var capturedPut provider.InstanceUpdateRequest
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		capturedPut = put
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{
		Name:   "test",
		Image:  "ubuntu:22.04",
		User:   "ubuntu",
		Groups: []string{"dev"},
	}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when adding groups")
	}
	if capturedPut.Config["user.lxm.groups"] != "dev" {
		t.Errorf("expected groups 'dev', got %q", capturedPut.Config["user.lxm.groups"])
	}
	if !updated {
		t.Error("expected updateInstance to be called")
	}
}

func TestUpdateContainer_SyncsGroups_Update(t *testing.T) {
	updated := false
	instance := &provider.Instance{
		Config:  map[string]string{"user.lxm.user": "ubuntu", "user.lxm.managed": "true", "user.lxm.groups": "dev"},
		Devices: map[string]map[string]string{},
	}
	var capturedPut provider.InstanceUpdateRequest
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		capturedPut = put
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{
		Name:   "test",
		Image:  "ubuntu:22.04",
		User:   "ubuntu",
		Groups: []string{"dev", "staging"},
	}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when updating groups")
	}
	if capturedPut.Config["user.lxm.groups"] != "dev,staging" {
		t.Errorf("expected groups 'dev,staging', got %q", capturedPut.Config["user.lxm.groups"])
	}
	if !updated {
		t.Error("expected updateInstance to be called")
	}
}

func TestUpdateContainer_SyncsGroups_Delete(t *testing.T) {
	updated := false
	instance := &provider.Instance{
		Config:  map[string]string{"user.lxm.user": "ubuntu", "user.lxm.managed": "true", "user.lxm.groups": "dev"},
		Devices: map[string]map[string]string{},
	}
	var capturedPut provider.InstanceUpdateRequest
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		capturedPut = put
		return nil
	}
	m := newTestManager(mock)

	conf := &config.Config{Name: "test", Image: "ubuntu:22.04", User: "ubuntu"}
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when deleting groups")
	}
	if _, exists := capturedPut.Config["user.lxm.groups"]; exists {
		t.Error("expected user.lxm.groups to be deleted")
	}
	if !updated {
		t.Error("expected updateInstance to be called")
	}
}

func TestUpdateContainer_SyncsGroups_Idempotent(t *testing.T) {
	updated := false
	conf := &config.Config{
		Name:   "test",
		Image:  "ubuntu:22.04",
		User:   "ubuntu",
		Groups: []string{"dev", "gpu"},
	}
	cloudData, _ := conf.ResolveCloudInit("")
	instance := &provider.Instance{
		Config:  map[string]string{"user.lxm.user": "ubuntu", "user.lxm.managed": "true", "user.lxm.groups": "dev,gpu", "user.user-data": cloudData},
		Devices: map[string]map[string]string{},
	}
	mock := newMockDriver()
	mock.updateInstanceFunc = func(ctx context.Context, name string, put provider.InstanceUpdateRequest, etag string) error {
		updated = true
		return nil
	}
	m := newTestManager(mock)
	changed, err := m.UpdateContainer(instance, "etag", conf, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected no change when groups already match")
	}
	if updated {
		t.Error("expected no update when state matches")
	}
}

func TestManager_Shell(t *testing.T) {
	interactiveCalled := false
	mock := newMockDriver()
	mock.getInstanceFunc = func(ctx context.Context, name string) (*provider.Instance, string, error) {
		return &provider.Instance{
			Name:       "shellbox",
			StatusCode: 103,
			Config: map[string]string{
				"user.lxm.user": "ubuntu",
			},
		}, "etag", nil
	}
	mock.interactiveExecFunc = func(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) error {
		interactiveCalled = true
		return nil
	}
	t.Setenv("PATH", "")
	mgr := newTestManager(mock)
	err := mgr.Shell("shellbox", "")
	if err != nil {
		t.Fatalf("unexpected error running shell: %v", err)
	}
	if !interactiveCalled {
		t.Errorf("expected InteractiveExecInstance to be called")
	}
}
