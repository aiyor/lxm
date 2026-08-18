package provider

import (
	"context"
	"io"
)

// InstanceService handles container and VM lifecycle operations.
type InstanceService interface {
	GetInstance(name string) (*Instance, string, error)
	CreateInstance(req InstanceCreateRequest) error
	CreateInstanceContext(ctx context.Context, req InstanceCreateRequest) error
	UpdateInstance(name string, req InstanceUpdateRequest, etag string) error
	UpdateInstanceContext(ctx context.Context, name string, req InstanceUpdateRequest, etag string) error
	DeleteInstance(name string) error
	DeleteInstanceContext(ctx context.Context, name string) error
	UpdateInstanceState(name string, action string, force bool) error
	UpdateInstanceStateContext(ctx context.Context, name string, action string, force bool) error
	RebuildInstance(name string, req InstanceRebuildRequest) error
	RebuildInstanceContext(ctx context.Context, name string, req InstanceRebuildRequest) error
	ListInstances() ([]Instance, error)

	// Exec & Guest Operations
	ResolveUID(name string, username string) (uint32, error)
	ResolveUserEnv(name string, username string) (*UserEnv, error)
	ExecInstance(name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error)
	ExecInstanceContext(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error)
	InteractiveExecInstance(name string, cmd []string, uid uint32, env map[string]string) error
	CreateInstanceFile(name string, path string, content io.Reader, mode int, uid, gid int64) error
	DeleteInstanceFile(name string, path string) error
	GetIP(name string) (string, error)

	// Snapshot Operations
	CreateInstanceSnapshot(name string, snapName string, stateful bool) error
	CreateInstanceSnapshotContext(ctx context.Context, name string, snapName string, stateful bool) error
	DeleteInstanceSnapshot(name string, snapName string) error
	DeleteInstanceSnapshotContext(ctx context.Context, name string, snapName string) error
	GetInstanceSnapshots(name string) ([]Snapshot, error)
	RestoreInstanceSnapshot(name string, snapName string) error
	RestoreInstanceSnapshotContext(ctx context.Context, name string, snapName string) error

	// Provider Metadata & Error Classification
	ProviderType() ProviderType
	HasExtension(name string) bool
	ClassifyError(err error, intent string) (int, bool)
}

// NetworkService handles bridges, OVN networks, and Network ACLs.
type NetworkService interface {
	GetNetworks() ([]Network, error)
	GetNetwork(name string) (*Network, string, error)
	CreateNetwork(net NetworkCreateRequest) error
	UpdateNetwork(name string, net NetworkUpdateRequest, etag string) error
	DeleteNetwork(name string) error
	GetNetworkACLs() ([]NetworkACL, error)
	GetNetworkACL(name string) (*NetworkACL, string, error)
	CreateNetworkACL(acl NetworkACLCreateRequest) error
	UpdateNetworkACL(name string, acl NetworkACLUpdateRequest, etag string) error
	DeleteNetworkACL(name string) error
}

// StorageService handles custom storage pools and volumes.
type StorageService interface {
	GetStoragePoolNames() ([]string, error)
	GetStoragePoolVolume(pool, volType, name string) (*StorageVolume, string, error)
	GetStoragePoolVolumes(pool string) ([]StorageVolume, error)
	CreateStoragePoolVolume(pool string, vol StorageVolumeCreateRequest) error
	UpdateStoragePoolVolume(pool, volType, name string, vol StorageVolumeUpdateRequest, etag string) error
	DeleteStoragePoolVolume(pool, volType, name string) error
}

// ImageService handles local image aliases and remote simplestreams fetches.
type ImageService interface {
	GetImages() ([]Image, error)
	GetImageAliases() ([]ImageAlias, error)
	CopyRemoteImage(ctx context.Context, remoteURL, alias, imageType, localAlias string) error
}

// ClusterService defines operations for cluster discovery and member management.
type ClusterService interface {
	IsClustered() bool
	GetClusterMembers() ([]ClusterMember, error)
	GetClusterMember(name string) (*ClusterMember, error)
}

// ProjectService defines operations for multi-tenant project management.
type ProjectService interface {
	GetProjects() ([]string, error)
	ProjectExists(name string) (bool, error)
	CreateProject(name string, description string) error
}

// Driver combines all operational services under a unified provider handle.
type Driver interface {
	InstanceService
	NetworkService
	StorageService
	ImageService
	ClusterService
	ProjectService

	// Scope and Targeting
	UseProject(project string) Driver
	UseTarget(targetNode string) Driver
}
