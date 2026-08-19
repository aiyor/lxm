package provider

import (
	"strings"
	"time"
)

// ProviderType identifies the backend daemon engine.
type ProviderType string

const (
	ProviderTypeAuto  ProviderType = "auto"
	ProviderTypeLXD   ProviderType = "lxd"
	ProviderTypeIncus ProviderType = "incus"
)

// InstanceType defines container vs virtual machine.
type InstanceType string

const (
	InstanceTypeContainer      InstanceType = "container"
	InstanceTypeVirtualMachine InstanceType = "virtual-machine"
	InstanceTypeVM             InstanceType = "virtual-machine"
	InstanceTypeAny            InstanceType = "any"
)

// Instance represents a live container or VM across LXD and Incus.
type Instance struct {
	Name            string                       `json:"name"`
	Type            InstanceType                 `json:"type"`
	Status          string                       `json:"status"`
	StatusCode      int                          `json:"status_code"`
	Architecture    string                       `json:"architecture"`
	Location        string                       `json:"location,omitempty"` // Cluster member node name
	Description     string                       `json:"description,omitempty"`
	Config          map[string]string            `json:"config"`
	ExpandedConfig  map[string]string            `json:"expanded_config,omitempty"`
	Devices         map[string]map[string]string `json:"devices"`
	ExpandedDevices map[string]map[string]string `json:"expanded_devices,omitempty"`
	Profiles        []string                     `json:"profiles"`
	Ephemeral       bool                         `json:"ephemeral"`
	ETag            string                       `json:"etag"`
	HasSnapshots    bool                         `json:"has_snapshots"`
	CreatedAt       time.Time                    `json:"created_at"`
	LastUsedAt      time.Time                    `json:"last_used_at"`

	State     *InstanceState `json:"state,omitempty"`
	Snapshots []Snapshot     `json:"snapshots,omitempty"`
}

// Writable converts an Instance into an InstanceUpdateRequest payload.
func (i *Instance) Writable() InstanceUpdateRequest {
	return InstanceUpdateRequest{
		Type:        i.Type,
		Config:      i.Config,
		Devices:     i.Devices,
		Profiles:    i.Profiles,
		Description: i.Description,
	}
}

// InstanceState represents the live runtime state of an instance.
type InstanceState struct {
	Status     string                          `json:"status"`
	StatusCode int                             `json:"status_code"`
	Disk       map[string]InstanceStateDisk    `json:"disk,omitempty"`
	Memory     InstanceStateMemory             `json:"memory,omitempty"`
	Network    map[string]InstanceStateNetwork `json:"network,omitempty"`
	Pid        int64                           `json:"pid,omitempty"`
	Processes  int64                           `json:"processes,omitempty"`
	CPU        InstanceStateCPU                `json:"cpu,omitempty"`
}

type InstanceStateDisk struct {
	Usage int64 `json:"usage"`
	Total int64 `json:"total"`
}

type InstanceStateMemory struct {
	Usage         int64 `json:"usage"`
	UsagePeak     int64 `json:"usage_peak"`
	Total         int64 `json:"total"`
	SwapUsage     int64 `json:"swap_usage"`
	SwapUsagePeak int64 `json:"swap_usage_peak"`
}

type InstanceStateCPU struct {
	Usage int64 `json:"usage"`
}

type InstanceStateNetwork struct {
	Addresses []InstanceStateNetworkAddress `json:"addresses"`
	Counters  InstanceStateNetworkCounters  `json:"counters"`
	Hwaddr    string                        `json:"hwaddr"`
	Mtu       int                           `json:"mtu"`
	State     string                        `json:"state"`
	Type      string                        `json:"type"`
	HostName  string                        `json:"host_name"`
}

type InstanceStateNetworkAddress struct {
	Family  string `json:"family"`
	Address string `json:"address"`
	Netmask string `json:"netmask"`
	Scope   string `json:"scope"`
}

type InstanceStateNetworkCounters struct {
	BytesReceived   uint64 `json:"bytes_received"`
	BytesSent       uint64 `json:"bytes_sent"`
	PacketsReceived uint64 `json:"packets_received"`
	PacketsSent     uint64 `json:"packets_sent"`
}

// ClusterMemberStatus represents node states in an Incus/LXD cluster.
type ClusterMemberStatus string

const (
	ClusterMemberStatusOnline     ClusterMemberStatus = "Online"
	ClusterMemberStatusEvacuated  ClusterMemberStatus = "Evacuated"
	ClusterMemberStatusEvacuating ClusterMemberStatus = "Evacuating"
	ClusterMemberStatusRestoring  ClusterMemberStatus = "Restoring"
	ClusterMemberStatusBlocked    ClusterMemberStatus = "Blocked"
	ClusterMemberStatusOffline    ClusterMemberStatus = "Offline"
)

func (s ClusterMemberStatus) IsReady() bool {
	return strings.EqualFold(string(s), "online")
}

// ClusterMember represents a node in an Incus or LXD cluster.
type ClusterMember struct {
	ServerName   string              `json:"server_name"`
	URL          string              `json:"url"`
	Database     bool                `json:"database"`
	Status       ClusterMemberStatus `json:"status"`
	Message      string              `json:"message"`
	Architecture string              `json:"architecture"`
	Roles        []string            `json:"roles"`
}

// UserEnv contains environment information for a user inside a container or VM.
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

// ExecResult contains structured execution results from running a command.
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

// Snapshot represents an instance snapshot.
type Snapshot struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Stateful  bool      `json:"stateful"`
}

// Network represents a managed bridge or OVN network.
type Network struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Config      map[string]string `json:"config"`
	Managed     bool              `json:"managed"`
	Status      string            `json:"status"`
	Locations   []string          `json:"locations,omitempty"`
	UsedBy      []string          `json:"used_by,omitempty"`
	ETag        string            `json:"etag"`
}

// NetworkACL represents a network access control list.
type NetworkACL struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Egress      []NetworkACLRule  `json:"egress"`
	Ingress     []NetworkACLRule  `json:"ingress"`
	Config      map[string]string `json:"config"`
	ETag        string            `json:"etag"`
}

// NetworkACLRule defines a single ACL rule entry.
type NetworkACLRule struct {
	Action          string `json:"action"` // "allow" | "reject" | "drop"
	Source          string `json:"source,omitempty"`
	Destination     string `json:"destination,omitempty"`
	Protocol        string `json:"protocol,omitempty"`
	SourcePort      string `json:"source_port,omitempty"`
	DestinationPort string `json:"destination_port,omitempty"`
	ICMPType        string `json:"icmp_type,omitempty"`
	ICMPCode        string `json:"icmp_code,omitempty"`
	State           string `json:"state,omitempty"`
	Description     string `json:"description,omitempty"`
}

// StorageVolume represents a custom storage volume.
type StorageVolume struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`         // "custom"
	ContentType string            `json:"content_type"` // "filesystem" | "block"
	Description string            `json:"description"`
	Pool        string            `json:"pool,omitempty"`
	Config      map[string]string `json:"config"`
	Location    string            `json:"location,omitempty"`
	UsedBy      []string          `json:"used_by,omitempty"`
	ETag        string            `json:"etag"`
}

// Image represents a local cached image.
type Image struct {
	Fingerprint  string            `json:"fingerprint"`
	Type         InstanceType      `json:"type"`
	Architecture string            `json:"architecture"`
	Properties   map[string]string `json:"properties"`
	Aliases      []ImageAlias      `json:"aliases"`
}

// ImageAlias represents an alias pointing to an image fingerprint.
type ImageAlias struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Target      string `json:"target"`
}

// ============================================================================
// Request Payloads
// ============================================================================

type InstanceSource struct {
	Type        string `json:"type"` // "image"
	Alias       string `json:"alias,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Server      string `json:"server,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Secret      string `json:"secret,omitempty"`
}

type InstanceCreateRequest struct {
	Name        string                       `json:"name"`
	Type        InstanceType                 `json:"type"`
	Source      InstanceSource               `json:"source"`
	Description string                       `json:"description,omitempty"`
	Config      map[string]string            `json:"config"`
	Devices     map[string]map[string]string `json:"devices"`
	Profiles    []string                     `json:"profiles,omitempty"`
	Ephemeral   bool                         `json:"ephemeral,omitempty"`
}

type InstanceUpdateRequest struct {
	Type        InstanceType                 `json:"type,omitempty"`
	Config      map[string]string            `json:"config"`
	Devices     map[string]map[string]string `json:"devices"`
	Profiles    []string                     `json:"profiles,omitempty"`
	Description string                       `json:"description,omitempty"`
}

type InstanceRebuildRequest struct {
	Source InstanceSource `json:"source"`
}

type NetworkCreateRequest struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Config      map[string]string `json:"config"`
}

type NetworkUpdateRequest struct {
	Description string            `json:"description,omitempty"`
	Config      map[string]string `json:"config"`
}

type NetworkACLCreateRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Egress      []NetworkACLRule  `json:"egress"`
	Ingress     []NetworkACLRule  `json:"ingress"`
	Config      map[string]string `json:"config,omitempty"`
}

type NetworkACLUpdateRequest struct {
	Description string            `json:"description,omitempty"`
	Egress      []NetworkACLRule  `json:"egress"`
	Ingress     []NetworkACLRule  `json:"ingress"`
	Config      map[string]string `json:"config,omitempty"`
}

type StorageVolumeCreateRequest struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`         // "custom"
	ContentType string            `json:"content_type"` // "filesystem" | "block"
	Description string            `json:"description,omitempty"`
	Config      map[string]string `json:"config,omitempty"`
}

type StorageVolumeUpdateRequest struct {
	Description string            `json:"description,omitempty"`
	Config      map[string]string `json:"config,omitempty"`
}
