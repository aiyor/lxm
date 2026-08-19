package provider

import (
	"context"
	"io"
)

// InstanceService handles container and VM lifecycle operations.
type InstanceService interface {
	GetInstance(ctx context.Context, name string) (*Instance, string, error)
	ListInstances(ctx context.Context) ([]Instance, error)
	CreateInstance(ctx context.Context, req InstanceCreateRequest) error
	UpdateInstance(ctx context.Context, name string, req InstanceUpdateRequest, etag string) error
	DeleteInstance(ctx context.Context, name string) error
	UpdateInstanceState(ctx context.Context, name string, action string, force bool) error
	RebuildInstance(ctx context.Context, name string, req InstanceRebuildRequest) error

	// Exec & Guest Operations
	ResolveUID(ctx context.Context, name string, username string) (uint32, error)
	ResolveUserEnv(ctx context.Context, name string, username string) (*UserEnv, error)
	ExecInstance(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) (ExecResult, error)
	InteractiveExecInstance(ctx context.Context, name string, cmd []string, uid uint32, env map[string]string) error
	CreateInstanceFile(ctx context.Context, name string, path string, content io.Reader, mode int, uid, gid int64) error
	DeleteInstanceFile(ctx context.Context, name string, path string) error
	GetIP(ctx context.Context, name string) (string, error)

	// Snapshot Operations
	CreateInstanceSnapshot(ctx context.Context, name string, snapName string, stateful bool) error
	DeleteInstanceSnapshot(ctx context.Context, name string, snapName string) error
	GetInstanceSnapshots(ctx context.Context, name string) ([]Snapshot, error)
	RestoreInstanceSnapshot(ctx context.Context, name string, snapName string) error

	// Provider Metadata & Error Classification (In-Memory / Context-Free)
	ProviderType() ProviderType
	HasExtension(name string) bool
	ClassifyError(err error, intent string) (int, bool)
}

// NetworkService handles bridges, OVN networks, and Network ACLs.
type NetworkService interface {
	GetNetworks(ctx context.Context) ([]Network, error)
	GetNetwork(ctx context.Context, name string) (*Network, string, error)
	CreateNetwork(ctx context.Context, net NetworkCreateRequest) error
	UpdateNetwork(ctx context.Context, name string, net NetworkUpdateRequest, etag string) error
	DeleteNetwork(ctx context.Context, name string) error
	GetNetworkACLs(ctx context.Context) ([]NetworkACL, error)
	GetNetworkACL(ctx context.Context, name string) (*NetworkACL, string, error)
	CreateNetworkACL(ctx context.Context, acl NetworkACLCreateRequest) error
	UpdateNetworkACL(ctx context.Context, name string, acl NetworkACLUpdateRequest, etag string) error
	DeleteNetworkACL(ctx context.Context, name string) error
}

// StorageService handles custom storage pools and volumes.
type StorageService interface {
	GetStoragePoolNames(ctx context.Context) ([]string, error)
	GetStoragePoolVolume(ctx context.Context, pool, volType, name string) (*StorageVolume, string, error)
	GetStoragePoolVolumes(ctx context.Context, pool string) ([]StorageVolume, error)
	CreateStoragePoolVolume(ctx context.Context, pool string, vol StorageVolumeCreateRequest) error
	UpdateStoragePoolVolume(ctx context.Context, pool, volType, name string, vol StorageVolumeUpdateRequest, etag string) error
	DeleteStoragePoolVolume(ctx context.Context, pool, volType, name string) error
}

// ImageService handles local image aliases and remote simplestreams fetches.
type ImageService interface {
	GetImages(ctx context.Context) ([]Image, error)
	GetImageAliases(ctx context.Context) ([]ImageAlias, error)
	CopyRemoteImage(ctx context.Context, remoteURL, alias, imageType, localAlias string) error
}

// ClusterService defines operations for cluster discovery and member management.
type ClusterService interface {
	IsClustered(ctx context.Context) (bool, error)
	GetClusterMembers(ctx context.Context) ([]ClusterMember, error)
	GetClusterMember(ctx context.Context, name string) (*ClusterMember, error)
}

// ProjectService defines operations for multi-tenant project management.
type ProjectService interface {
	GetProjects(ctx context.Context) ([]string, error)
	ProjectExists(ctx context.Context, name string) (bool, error)
	CreateProject(ctx context.Context, name string, description string) error
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
