package lxd

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

	lxd_client "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"
	"github.com/gorilla/websocket"
	"golang.org/x/term"

	"github.com/aiyor/lxm/internal/provider"
)

// UserEnv contains environment information for a user inside a container.
type UserEnv struct {
	UID   uint32
	GID   uint32
	Home  string
	Shell string
	User  string
}

// DefaultEnv returns a map of common environment variables mapped correctly for this user.
func (u *UserEnv) DefaultEnv() map[string]string {
	return map[string]string{
		"HOME":  u.Home,
		"USER":  u.User,
		"SHELL": u.Shell,
		"PATH":  "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin",
	}
}

// ExecResult contains structured execution results from running a command inside a container.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// Combined returns stdout and stderr formatted into a single string.
func (e ExecResult) Combined() string {
	var buf strings.Builder
	buf.WriteString(e.Stdout)
	if len(e.Stderr) > 0 {
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(e.Stderr)
	}
	return buf.String()
}

// InstanceService defines the interface for interacting with container instances.
type InstanceService interface {
	GetInstance(name string) (*api.Instance, string, error)
	CreateInstance(req api.InstancesPost) error
	CreateInstanceContext(ctx context.Context, req api.InstancesPost) error
	UpdateInstance(name string, put api.InstancePut, etag string) error
	UpdateInstanceContext(ctx context.Context, name string, put api.InstancePut, etag string) error
	DeleteInstance(name string) error
	DeleteInstanceContext(ctx context.Context, name string) error
	UpdateInstanceState(name string, action string, force bool) error
	UpdateInstanceStateContext(ctx context.Context, name string, action string, force bool) error
	RebuildInstance(name string, req api.InstanceRebuildPost) error
	RebuildInstanceContext(ctx context.Context, name string, req api.InstanceRebuildPost) error
	HasExtension(name string) bool
	ResolveUID(name string, username string) (uint32, error)
	ResolveUserEnv(name string, username string) (*UserEnv, error)
	ExecInstance(name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error)
	ExecInstanceContext(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error)
	InteractiveExecInstance(name string, cmd []string, uid uint32, env map[string]string) error
	CreateInstanceFile(name string, path string, args lxd_client.InstanceFileArgs) error
	DeleteInstanceFile(name string, path string) error
	GetIP(name string) (string, error)
	ListInstances() ([]api.InstanceFull, error)
	ClassifyLXDError(err error, intent string) (int, bool)
	CreateInstanceSnapshot(name string, req api.InstanceSnapshotsPost) error
	CreateInstanceSnapshotContext(ctx context.Context, name string, req api.InstanceSnapshotsPost) error
	DeleteInstanceSnapshot(name string, snapshotName string) error
	DeleteInstanceSnapshotContext(ctx context.Context, name string, snapshotName string) error
	GetInstanceSnapshots(name string) ([]api.InstanceSnapshot, error)
	RestoreInstanceSnapshot(name string, snapshotName string) error
	RestoreInstanceSnapshotContext(ctx context.Context, name string, snapshotName string) error
	ProviderType() provider.ProviderType
}

// NetworkService defines the interface for interacting with LXD networks and
// network ACLs (the vswitches:/network_policy: feature surface).
type NetworkService interface {
	GetNetworks() ([]api.Network, error)
	GetNetwork(name string) (*api.Network, string, error)
	CreateNetwork(network api.NetworksPost) error
	UpdateNetwork(name string, network api.NetworkPut, etag string) error
	DeleteNetwork(name string) error
	GetNetworkACLs() ([]api.NetworkACL, error)
	GetNetworkACL(name string) (*api.NetworkACL, string, error)
	CreateNetworkACL(acl api.NetworkACLsPost) error
	UpdateNetworkACL(name string, acl api.NetworkACLPut, etag string) error
	DeleteNetworkACL(name string) error
}

// StorageService defines the interface for interacting with LXD storage pools
// and custom volumes (the disks: feature surface, STORAGE-SPEC §9). Volume
// mutations return asynchronous LXD Operations that must be awaited.
type StorageService interface {
	GetStoragePoolNames() ([]string, error)
	GetStoragePoolVolume(pool, volType, name string) (*api.StorageVolume, string, error)
	GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error)
	CreateStoragePoolVolume(pool string, vol api.StorageVolumesPost) error
	UpdateStoragePoolVolume(pool, volType, name string, vol api.StorageVolumePut, etag string) error
	// DeleteStoragePoolVolume is reserved for a future `lxm disk gc`; the disks
	// feature never deletes volumes (STORAGE-SPEC §7.5).
	DeleteStoragePoolVolume(pool, volType, name string) error
}

// ImageService exposes the local image store for cache probing and remote
// simplestreams fetch (the image: remote:alias feature surface, IMAGE-SPEC §8).
type ImageService interface {
	GetImages() ([]api.Image, error)
	GetImageAliases() ([]api.ImageAliasesEntry, error)
	// CopyRemoteImage pulls a remote simplestreams image into the local store
	// and creates the canonical local alias. The async operation is awaited.
	CopyRemoteImage(ctx context.Context, remoteURL, alias, imageType, localAlias string) error
}

type lxdService struct {
	client lxd_client.InstanceServer
}

func NewService() (InstanceService, error) {
	socket := os.Getenv("LXD_SOCKET")
	if socket == "" {
		// Common LXD socket locations
		candidates := []string{
			"/var/snap/lxd/common/lxd/unix.socket",
			"/var/lib/lxd/unix.socket",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				socket = c
				break
			}
		}
	}

	client, err := lxd_client.ConnectLXDUnix(socket, nil)
	if err != nil {
		return nil, err
	}
	return &lxdService{client: client}, nil
}

func (s *lxdService) GetInstance(name string) (*api.Instance, string, error) {
	return s.client.GetInstance(name)
}

func (s *lxdService) ListInstances() ([]api.InstanceFull, error) {
	return s.client.GetInstancesFull(lxd_client.GetInstancesFullArgs{InstanceType: api.InstanceTypeAny})
}

func waitOpContext(ctx context.Context, op lxd_client.Operation) error {
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

func (s *lxdService) ProviderType() provider.ProviderType {
	return provider.ProviderTypeLXD
}

func (s *lxdService) CreateInstance(req api.InstancesPost) error {
	return s.CreateInstanceContext(context.Background(), req)
}

func (s *lxdService) CreateInstanceContext(ctx context.Context, req api.InstancesPost) error {
	op, err := s.client.CreateInstance(req)
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (s *lxdService) UpdateInstance(name string, put api.InstancePut, etag string) error {
	return s.UpdateInstanceContext(context.Background(), name, put, etag)
}

func (s *lxdService) UpdateInstanceContext(ctx context.Context, name string, put api.InstancePut, etag string) error {
	op, err := s.client.UpdateInstance(name, put, etag)
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (s *lxdService) DeleteInstance(name string) error {
	return s.DeleteInstanceContext(context.Background(), name)
}

func (s *lxdService) DeleteInstanceContext(ctx context.Context, name string) error {
	op, err := s.client.DeleteInstance(name, false)
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (s *lxdService) UpdateInstanceState(name string, action string, force bool) error {
	return s.UpdateInstanceStateContext(context.Background(), name, action, force)
}

func (s *lxdService) UpdateInstanceStateContext(ctx context.Context, name string, action string, force bool) error {
	op, err := s.client.UpdateInstanceState(name, api.InstanceStatePut{Action: action, Force: force, Timeout: -1}, "")
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (s *lxdService) RebuildInstance(name string, req api.InstanceRebuildPost) error {
	return s.RebuildInstanceContext(context.Background(), name, req)
}

func (s *lxdService) RebuildInstanceContext(ctx context.Context, name string, req api.InstanceRebuildPost) error {
	op, err := s.client.RebuildInstance(name, req)
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (s *lxdService) CreateInstanceSnapshot(name string, req api.InstanceSnapshotsPost) error {
	return s.CreateInstanceSnapshotContext(context.Background(), name, req)
}

func (s *lxdService) CreateInstanceSnapshotContext(ctx context.Context, name string, req api.InstanceSnapshotsPost) error {
	op, err := s.client.CreateInstanceSnapshot(name, req)
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (s *lxdService) DeleteInstanceSnapshot(name string, snapshotName string) error {
	return s.DeleteInstanceSnapshotContext(context.Background(), name, snapshotName)
}

func (s *lxdService) DeleteInstanceSnapshotContext(ctx context.Context, name string, snapshotName string) error {
	op, err := s.client.DeleteInstanceSnapshot(name, snapshotName, "")
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (s *lxdService) GetInstanceSnapshots(name string) ([]api.InstanceSnapshot, error) {
	return s.client.GetInstanceSnapshots(name)
}

func (s *lxdService) RestoreInstanceSnapshot(name string, snapshotName string) error {
	return s.RestoreInstanceSnapshotContext(context.Background(), name, snapshotName)
}

func (s *lxdService) RestoreInstanceSnapshotContext(ctx context.Context, name string, snapshotName string) error {
	op, err := s.client.UpdateInstance(name, api.InstancePut{Restore: snapshotName}, "")
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (s *lxdService) GetNetworks() ([]api.Network, error) {
	return s.client.GetNetworks()
}

func (s *lxdService) GetNetwork(name string) (*api.Network, string, error) {
	return s.client.GetNetwork(name)
}

func (s *lxdService) CreateNetwork(network api.NetworksPost) error {
	return s.client.CreateNetwork(network)
}

func (s *lxdService) UpdateNetwork(name string, network api.NetworkPut, etag string) error {
	return s.client.UpdateNetwork(name, network, etag)
}

func (s *lxdService) DeleteNetwork(name string) error {
	return s.client.DeleteNetwork(name)
}

func (s *lxdService) GetNetworkACLs() ([]api.NetworkACL, error) {
	return s.client.GetNetworkACLs()
}

func (s *lxdService) GetNetworkACL(name string) (*api.NetworkACL, string, error) {
	return s.client.GetNetworkACL(name)
}

func (s *lxdService) CreateNetworkACL(acl api.NetworkACLsPost) error {
	return s.client.CreateNetworkACL(acl)
}

func (s *lxdService) UpdateNetworkACL(name string, acl api.NetworkACLPut, etag string) error {
	return s.client.UpdateNetworkACL(name, acl, etag)
}

func (s *lxdService) DeleteNetworkACL(name string) error {
	return s.client.DeleteNetworkACL(name)
}

func (s *lxdService) GetStoragePoolNames() ([]string, error) {
	return s.client.GetStoragePoolNames()
}

func (s *lxdService) GetStoragePoolVolume(pool, volType, name string) (*api.StorageVolume, string, error) {
	return s.client.GetStoragePoolVolume(pool, volType, name)
}

func (s *lxdService) GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error) {
	return s.client.GetStoragePoolVolumes(pool)
}

func (s *lxdService) CreateStoragePoolVolume(pool string, vol api.StorageVolumesPost) error {
	op, err := s.client.CreateStoragePoolVolume(pool, vol)
	if err != nil {
		return err
	}
	return waitOpContext(context.Background(), op)
}

func (s *lxdService) UpdateStoragePoolVolume(pool, volType, name string, vol api.StorageVolumePut, etag string) error {
	op, err := s.client.UpdateStoragePoolVolume(pool, volType, name, vol, etag)
	if err != nil {
		return err
	}
	return waitOpContext(context.Background(), op)
}

func (s *lxdService) DeleteStoragePoolVolume(pool, volType, name string) error {
	op, err := s.client.DeleteStoragePoolVolume(pool, volType, name)
	if err != nil {
		return err
	}
	return waitOpContext(context.Background(), op)
}

func (s *lxdService) GetImages() ([]api.Image, error) {
	return s.client.GetImages()
}

func (s *lxdService) GetImageAliases() ([]api.ImageAliasesEntry, error) {
	return s.client.GetImageAliases()
}

// CopyRemoteImage pulls a remote simplestreams image into the local store and
// tags it with the canonical local alias (IMAGE-SPEC §8). The LXD daemon
// performs the download and arch/variant selection — the same mechanism as
// `lxc image copy` — and the async operation is awaited. A concurrent fetch of
// the same alias surfaces LXD's "Alias already exists", which the executor
// treats as a no-op (§7.7).
func (s *lxdService) CopyRemoteImage(ctx context.Context, remoteURL, alias, imageType, localAlias string) error {
	op, err := s.client.CreateImage(api.ImagesPost{
		Source: &api.ImagesPostSource{
			ImageSource: api.ImageSource{
				Protocol:  "simplestreams",
				Server:    remoteURL,
				Alias:     alias,
				ImageType: imageType,
			},
			Mode: "pull",
			Type: api.SourceTypeImage,
		},
		Aliases: []api.ImageAlias{{Name: localAlias}},
	}, nil)
	if err != nil {
		return err
	}
	return waitOpContext(ctx, op)
}

func (s *lxdService) HasExtension(name string) bool {
	if s.client == nil {
		return false
	}
	return s.client.HasExtension(name)
}

func (s *lxdService) ClassifyLXDError(err error, intent string) (int, bool) {
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
		// The daemon's ETag-conflict text is authoritative regardless of the
		// HTTP code it arrives under (LXD 6.x uses 412; a non-standard code
		// carrying the same message is still the same retryable drift).
		if isETagConflictMessage(errStr) {
			return 4, true
		}
		if code == 412 {
			return 4, true // LXD_ERROR, ETag mismatch retryable
		}
		return 4, false
	}

	if strings.Contains(errStr, "not found") {
		if intent == "lookup" {
			return 5, false
		}
		return 0, false
	}

	// Generic (non-StatusError) errors: the "412" substring and the message
	// markers both indicate an ETag conflict. The substring check stays here
	// (not on the StatusError path, where code == 412 already covers it and a
	// coincidental "412" in another error's message must not broaden
	// retryable).
	if isETagConflictMessage(errStr) || strings.Contains(errStr, "412") {
		return 4, true
	}

	return 4, false
}

// ETagConflictPrefix is the leading text of the LXD daemon's 412 response on
// an optimistic-concurrency conflict (real LXD 6.x message). The classifier
// keys on it so drift errors surface as retryable.
const ETagConflictPrefix = "ETag does not match"

// isETagConflictMessage reports whether an error string is an
// optimistic-concurrency (ETag) conflict by its message text. It matches both
// the synthetic "etag mismatch" text used by the fake server and the real LXD
// daemon's 412 message ("ETag does not match: <old> vs <new>. The
// configuration has been modified since this change began. ..."), which is
// what a live host actually returns.
func isETagConflictMessage(errStr string) bool {
	lower := strings.ToLower(errStr)
	return strings.Contains(lower, "etag mismatch") ||
		strings.Contains(lower, "etag does not match") ||
		strings.Contains(lower, "configuration has been modified since this change began")
}

// ResolveUID resolves a username to a UID inside the container by executing
// `id -u <username>` as root.
func (s *lxdService) ResolveUID(name string, username string) (uint32, error) {
	if username == "root" {
		return 0, nil
	}

	res, err := s.ExecInstance(name, []string{"id", "-u", username}, 0, nil)
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

// ResolveUserEnv resolves a username to its environment (UID, GID, Home, Shell)
// inside the container using `getent passwd`.
func (s *lxdService) ResolveUserEnv(name string, username string) (*UserEnv, error) {
	res, err := s.ExecInstance(name, []string{"getent", "passwd", username}, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("getting passwd entry for %q: %w", username, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("user %q not found in container (getent exited with code %d)", username, res.ExitCode)
	}

	// format: name:password:uid:gid:gecos:home:shell
	lines := strings.Split(strings.TrimSpace(res.Combined()), "\n")
	firstLine := strings.TrimSpace(lines[0])
	parts := strings.Split(firstLine, ":")
	if len(parts) < 7 {
		return nil, fmt.Errorf("malformed passwd entry for %q: %s", username, res.Combined())
	}

	uid, _ := strconv.ParseUint(parts[2], 10, 32)
	gid, _ := strconv.ParseUint(parts[3], 10, 32)

	return &UserEnv{
		UID:   uint32(uid),
		GID:   uint32(gid),
		Home:  parts[5],
		Shell: parts[6],
		User:  username,
	}, nil
}

func (s *lxdService) ExecInstance(name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error) {
	return s.ExecInstanceContext(context.Background(), name, cmd, uid, env)
}

func (s *lxdService) ExecInstanceContext(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error) {
	var stdout, stderr bytes.Buffer

	execReq := api.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: false,
		User:        uid,
		Environment: env,
	}

	execArgs := &lxd_client.InstanceExecArgs{
		Stdin:  io.NopCloser(bytes.NewReader(nil)),
		Stdout: &stdout,
		Stderr: &stderr,
	}

	op, err := s.client.ExecInstance(name, execReq, execArgs)
	if err != nil {
		return ExecResult{ExitCode: -1}, err
	}
	waitErr := waitOpContext(ctx, op)

	opMeta := op.Get()
	exitCode := -1
	if returnVal, ok := opMeta.Metadata["return"]; ok {
		if code, ok := returnVal.(float64); ok {
			exitCode = int(code)
		}
	}

	if waitErr != nil && exitCode == -1 {
		return ExecResult{ExitCode: -1}, waitErr
	}

	return ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}

func (s *lxdService) InteractiveExecInstance(name string, cmd []string, uid uint32, env map[string]string) error {
	fd := int(os.Stdin.Fd())

	if env == nil {
		env = make(map[string]string)
	}

	// Pass crucial terminal environment from host to ensure rich TUI support
	hostVars := []string{"TERM", "LANG", "COLORTERM", "LANGUAGE"}
	for _, v := range hostVars {
		if _, ok := env[v]; !ok {
			if val := os.Getenv(v); val != "" {
				env[v] = val
			}
		}
	}
	// Also pass all LC_* variables
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "LC_") {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				if _, ok := env[parts[0]]; !ok {
					env[parts[0]] = parts[1]
				}
			}
		}
	}

	// If it's not a terminal, just run a basic interactive session
	if !term.IsTerminal(fd) {
		execReq := api.InstanceExecPost{
			Command:     cmd,
			WaitForWS:   true,
			Interactive: true,
			User:        uid,
			Environment: env,
		}
		execArgs := &lxd_client.InstanceExecArgs{
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		}
		op, err := s.client.ExecInstance(name, execReq, execArgs)
		if err != nil {
			return err
		}
		return op.Wait()
	}

	// Put the terminal in raw mode
	state, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(fd, state) }()

	// Get initial size
	width, height, err := term.GetSize(fd)
	if err != nil {
		width, height = 80, 24
	}

	execReq := api.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: true,
		User:        uid,
		Environment: env,
		Width:       width,
		Height:      height,
	}

	controlChan := make(chan api.InstanceExecControl)
	done := make(chan struct{})
	var wg sync.WaitGroup

	defer func() {
		close(done)
		wg.Wait()
		close(controlChan)
	}()

	execArgs := &lxd_client.InstanceExecArgs{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Control: func(conn *websocket.Conn) {
			for control := range controlChan {
				_ = conn.WriteJSON(control)
			}
		},
	}

	defer func() {
		if r := recover(); r != nil {
			_ = term.Restore(fd, state)
			panic(r)
		}
	}()

	// Handle window resizing & terminal restoration on signals (D2 / 4.3)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigChan)

	opts := interactiveSignalOpts{
		fd:    fd,
		state: state,
		reRaiseSignal: func(sig os.Signal) {
			signal.Stop(sigChan)
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				_ = p.Signal(sig)
			}
		},
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case sig := <-sigChan:
				if shouldExit := handleInteractiveSignal(sig, opts, controlChan, done); shouldExit {
					signal.Stop(sigChan)
					return
				}
			case <-done:
				return
			}
		}
	}()

	op, err := s.client.ExecInstance(name, execReq, execArgs)
	if err != nil {
		return err
	}
	return op.Wait()
}

func (s *lxdService) CreateInstanceFile(name string, path string, args lxd_client.InstanceFileArgs) error {
	return s.client.CreateInstanceFile(name, path, args)
}

func (s *lxdService) DeleteInstanceFile(name string, path string) error {
	return s.client.DeleteInstanceFile(name, path)
}

func (s *lxdService) GetIP(name string) (string, error) {
	state, _, err := s.client.GetInstanceState(name)
	if err != nil {
		return "", err
	}

	// Try to find the first global IPv4 address
	for _, net := range state.Network {
		for _, addr := range net.Addresses {
			if addr.Family == "inet" && addr.Scope == "global" {
				return addr.Address, nil
			}
		}
	}

	// Fallback to any IPv4 if global not found (e.g. some bridge setups)
	for _, net := range state.Network {
		for _, addr := range net.Addresses {
			if addr.Family == "inet" {
				return addr.Address, nil
			}
		}
	}

	return "", fmt.Errorf("no IPv4 address found for %q", name)
}

// DeviceName generates a deterministic LXD device name from a container path.
func DeviceName(containerPath string) string {
	name := "mount-" + strings.ReplaceAll(containerPath, "/", "-")
	return strings.Trim(name, "-")
}

func IsHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

type interactiveSignalOpts struct {
	fd            int
	state         *term.State
	restoreTerm   func()
	reRaiseSignal func(sig os.Signal)
	getTermSize   func(fd int) (int, int, error)
}

func handleInteractiveSignal(sig os.Signal, opts interactiveSignalOpts, controlChan chan<- api.InstanceExecControl, done <-chan struct{}) bool {
	switch sig {
	case syscall.SIGWINCH:
		getFS := opts.getTermSize
		if getFS == nil {
			getFS = term.GetSize
		}
		w, h, err := getFS(opts.fd)
		if err == nil {
			select {
			case controlChan <- api.InstanceExecControl{
				Command: "window-resize",
				Args: map[string]string{
					"width":  strconv.Itoa(w),
					"height": strconv.Itoa(h),
				},
			}:
			case <-done:
				return true
			}
		}
		return false
	case syscall.SIGINT:
		select {
		case controlChan <- api.InstanceExecControl{
			Command: "signal",
			Args: map[string]string{
				"signum": strconv.Itoa(int(syscall.SIGINT)),
			},
		}:
		case <-done:
			return true
		}
		return false
	default:
		// SIGTERM / SIGHUP / external interrupt: restore terminal state before re-raising signal
		if opts.restoreTerm != nil {
			opts.restoreTerm()
		} else if opts.state != nil {
			_ = term.Restore(opts.fd, opts.state)
		}
		if opts.reRaiseSignal != nil {
			opts.reRaiseSignal(sig)
		} else {
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				_ = p.Signal(sig)
			}
		}
		return true
	}
}
