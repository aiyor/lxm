package config

import (
	"strings"
)

// ============================================================================
// Shared Reference Grammars
// ============================================================================

#Fingerprint: =~"^[0-9a-f]{12,64}$"
#LocalAlias:  =~"^[a-zA-Z0-9_\\.\\-]+$"
#RemoteAlias: =~"^[a-zA-Z0-9_\\.\\-]+:[a-zA-Z0-9_\\.\\-/]+$"

// LXD Image identity: hex fingerprint, local alias, or remote:alias
#ImageRef: #Fingerprint | #LocalAlias | #RemoteAlias

// POSIX Environment Variable identifier
#EnvKey: =~"^[a-zA-Z_][a-zA-Z0-9_]*$"

// Sub-resource existence status (disks, vswitches)
#DiskStatus:    "present" | "absent"
#VSwitchStatus: "present" | "absent"

// Simplestreams image remote name (the remote part of image: remote:alias)
#ImageRemoteName: =~"^[a-zA-Z0-9_\\.\\-]+$"

// Provider type: Incus, LXD, or auto-detection
#ProviderType: "incus" | "lxd" | "auto"

// Remote name charset
#RemoteName: =~"^[a-zA-Z0-9_\\.\\-]+$"
#RemoteNameInvalid: !~"^[a-zA-Z0-9_\\.\\-]+$"

// Project and Target charsets
#ProjectName: =~"^[a-zA-Z0-9_\\.\\-]+$"
#ClusterTarget: =~"^[a-zA-Z0-9_\\.\\-]+$"

#RemoteObjAuthoring: close({
	address:   string
	provider?: #ProviderType
	project?:  #ProjectName
	insecure?: bool
	protocol?: "https" | "unix"
})

// #ImageRemoteNameInvalid rejects keys that do not fully match #ImageRemoteName.
// It is needed because a map carrying only a positive key-pattern constraint is
// NOT enforced when the struct is wrapped in close() (a CUE quirk); pairing the
// positive pattern with a [!~...]: _|_ rejection makes the charset rule
// concrete, so a key like "bad name!" fails validation.
#ImageRemoteNameInvalid: !~"^[a-zA-Z0-9_\\.\\-]+$"

// Mount path restrictions: absolute, cleaned, non-root system path
#CleanMountPath: string & {
	strings.MinRunes(2)
	!~"^/(proc|sys|dev)(/|$)"
	!~"^/$"
}

// Compact string mount: "HostPath:ContainerPath[:ro|:rw]"
#MountStr: =~"^.+:.+(:(ro|rw))?$"

#InstanceType: "container" | "virtual-machine"
#InstanceTypeAuthoring: #InstanceType | "vm"

// Strict integer byte sizes aligned with LXD shared/units.ParseByteSizeString
#ByteSize: =~"^[0-9]+(B|kB|MB|GB|TB|PB|EB|KiB|MiB|GiB|TiB|PiB|EiB)?$"

#CPUCountAuthoring: (int & >0) | (=~"^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$" & !~"^0$")
#CPUCountResolved:  =~"^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$" & !~"^0$"

// Authoring mount object (allows raw tilde '~' and '{{ .Vars.* }}' template sources)
#MountObjAuthoring: close({
	source:     string
	path:       #CleanMountPath
	readonly?:  bool | *false
	recursive?: bool | *false
	shift?:     bool | *true
})

// Authoring mount object for the Style 2 map form: the map key supplies the
// container path when path is omitted (the loader fills it in).
#MountMapObjAuthoring: close({
	source:     string
	path?:      #CleanMountPath
	readonly?:  bool | *false
	recursive?: bool | *false
	shift?:     bool | *true
})

// Resolved mount object (strictly enforces absolute host source path)
#MountObjResolved: close({
	source:     string & =~"^/"
	path:       #CleanMountPath
	readonly?:  bool | *false
	recursive?: bool | *false
	shift?:     bool | *true
})

// Authoring disk object (VM-only data disk, STORAGE-SPEC §3). Two orthogonal
// axes: mode (filesystem vs block, by `path`) and ownership (managed vs
// external, by `source`). `path` and `source` are NOT mutually exclusive
// (external filesystem volumes carry both); `size` and `bus` are conditional.
// Forbidden-field guards use the established `!= _|_` pattern; the
// complementary "size REQUIRED when source is unset" guard runs Go-side in
// LoadConfig normalization (managed disks must declare a size).
#DiskObjAuthoring: close({
	name:      string & =~"^[a-z][a-z0-9-]{0,30}$" & != "root"
	status?:   #DiskStatus | *"present"
	attach?:   bool | *true
	size?:     #ByteSize
	pool?:     string | *"default"
	P1="path"?:     #CleanMountPath
	S1="source"?:   string
	readonly?: bool | *false
	B1="bus"?:      "virtio-scsi" | "virtio-blk" | "nvme"

	// size is FORBIDDEN when the disk is external (source set)
	if S1 != _|_ && size != _|_ {
		_|_
	}

	// bus is FORBIDDEN in filesystem mode (path set)
	if P1 != _|_ && B1 != _|_ {
		_|_
	}
})

// Resolved disk object (defaults materialized by the Go normalizer).
#DiskObjResolved: close({
	name:      string & =~"^[a-z][a-z0-9-]{0,30}$" & != "root"
	status:    #DiskStatus
	attach?:   bool
	size?:     #ByteSize
	pool:      string
	path?:     #CleanMountPath
	source?:   string
	readonly?: bool | *false
	bus?:      "virtio-scsi" | "virtio-blk" | "nvme"
})

#LimitsAuthoring: close({
	cpu?:    #CPUCountAuthoring
	memory?: #ByteSize
	disk?:   #ByteSize
})

#LimitsResolved: close({
	cpu?:    #CPUCountResolved
	memory?: #ByteSize
	disk?:   #ByteSize
})

#VMConfigAuthoring: close({
	Z1="secureboot"?: bool
	Z2="boot_mode"?:  "uefi-secureboot" | "uefi-nosecureboot" | "bios"
	hugepages?:       bool | *false
	raw_qemu?:        string

	if Z1 != _|_ && Z2 != _|_ {
		_|_
	}
})

#VMConfigResolved: close({
	boot_mode?: "uefi-secureboot" | "uefi-nosecureboot" | "bios"
	hugepages?: bool | *false
	raw_qemu?:  string
})

// Wait policy struct with recursive presence tracking
#WaitConfig: close({
	agent?:      string
	cloud_init?: string | *"10m"
	network?:    string | *"60s"
	poll?:       string | *"5s"
	required?:   bool   | *true
})

// Managed virtual switch (v1: only "bridge" networks with an explicit IPv4
// subnet). The ipv4 regex is a coarse authoring gate; numeric checks
// (first usable host, /8–/29 mask bounds) run in Go after merge.
#VSwitchObjAuthoring: close({
	name:      string & =~"^[a-z][a-z0-9-]{0,30}$"
	status?:   #VSwitchStatus | *"present"
	type?:     "bridge"                    // v1 lock; "ovn" added later (additive relaxation)
	driver?:   "native" | "openvswitch" | *"native"
	ipv4?:     string & =~"^([0-9]{1,3}\\.[0-9]{1,3}\\.[0-9]{1,3}\\.[0-9]{1,3})/[0-9]{1,2}$"
	ipv6?:     "none"                      // v1 lock; IPv6 policy compilation deferred
	nat?:      bool | *true
	group?:    string
	internet?: bool | *true
})

// One inter-group allowance in network_policy.
#PolicyRuleAuthoring: close({
	from:      string
	to:        string
	direction?: "both" | "egress" | *"both"
})

// ============================================================================
// 1. #LXM_AUTHORING — Human Authoring Schema (Used for IDE JSON Schema Export)
// ============================================================================

#LXM_AUTHORING: close({
	schema?: "lxm/config/v2"
	base?:   bool | *false
	name?:   string
	type?:   #InstanceTypeAuthoring | *"container"
	image?:  #ImageRef
	user?:   string | *"ubuntu"
	status?: "present" | "absent" | *"present"
	state?:  "running" | "stopped" // explicit power state; overrides status-derived default (F2)

	provider?: #ProviderType
	remote?:   #RemoteName
	target?:   #ClusterTarget
	project?:  #ProjectName
	remotes?: {
		[#RemoteName]: #RemoteObjAuthoring
		[#RemoteNameInvalid]: _|_
	}

	limits?: #LimitsAuthoring
	vm?:     #VMConfigAuthoring

	// Local template variables for host path reuse (file-local scope)
	vars?: {[#EnvKey]: string}

	// Mounts: accepts compact strings, closed map form, object form, or mixed list
	mounts?: [...(#MountStr | #MountObjAuthoring)] | close({[#CleanMountPath]: (string | #MountMapObjAuthoring)})

	// Networks: list of network interfaces
	networks?: [...close({
		name?:   string | *"eth0"
		ipv4?:   string
		parent?: string | *"lxdbr0"
	})]

	// Data disks (VM-only, STORAGE-SPEC §3)
	disks?: [...#DiskObjAuthoring]

	// Fleet-scoped managed virtual switches (unioned across loaded manifests)
	vswitches?: [...#VSwitchObjAuthoring]

	// Fleet-scoped group-based traffic policy (compiled to LXD network ACLs)
	network_policy?: close({
		internal_cidrs?: [...string]
		allow:           [...#PolicyRuleAuthoring]
	})

	// Fleet-scoped simplestreams image remotes (image: remote:alias). Rich
	// URL validation (scheme, host, loopback-http rule) and the remote-name
	// charset diagnostic run Go-side in ValidatePostMerge; the URL is a bare
	// string here and the charset is declared (enforced as the resolved-form
	// contract in #LXM_RESOLVED).
	image_remotes?: {[#ImageRemoteName]: string}

	// Wait policy: accepts scalar bool (shorthand) or struct
	wait?: bool | #WaitConfig

	// Recipes: accepts script paths, root shorthand, run_as objects, and
	// legacy scripts-only groups (run_as defaults to root, matching the
	// loader normalization and the recipe metadata schema). Empty or
	// comment-only script entries are rejected loudly (mirroring the
	// migrator's Transform 8 emptiness check).
	recipes?: [...((string & != "" & !~"^\\s*#") | close({root: [...(string & != "" & !~"^\\s*#")] & [_, ...]}) | close({
		run_as?: string | *"root"
		scripts: [...(string & != "" & !~"^\\s*#")] & [_, ...]
	}))]

	// Cloud-Init inclusion & configuration (Hyphenated v1 keys preserved)
	"cloud-init-include"?: [...string]
	X1="cloud-init"?:      string
	X2="cloud-init-file"?: string
	"network-config"?:     string

	if X1 != _|_ && X2 != _|_ {
		_|_
	}

	// Inheritance & List Modification Directives
	include?: [...string]
	remove?: close({
		mounts?:   [...string]
		networks?: [...string]
		recipes?:  [...string]
		disks?:    [...string]
	})
	replace?: close({
		mounts?:   [...(#MountStr | #MountObjAuthoring)]
		networks?: [...]
		recipes?:  [...]
		disks?:    [...#DiskObjAuthoring]
	})

	groups?: [...string]

	// Security Posture Fields (D9)
	sudo?:              bool
	"inject_ssh_keys"?: bool
	"ssh_keys"?:        [...string]
})

// ============================================================================
// 2. #LXM_RESOLVED — Strict Canonical Manifest Schema (Consumed by Reconciler)
// ============================================================================

#LXM_RESOLVED: close({
	schema: "lxm/config/v2"
	name:   string & strings.MinRunes(1)
	type:   #InstanceType | *"container"
	image?: #ImageRef // Optional for status: absent; status: present => image required is a Go invariant (F1)
	user:   string & strings.MinRunes(1)
	status: "present" | "absent"
	state?: "running" | "stopped" // Go normalizer default "running"; state with status: absent is a Go ValidatePostMerge error (F2)

	provider?: #ProviderType
	remote?:   #RemoteName
	target?:   #ClusterTarget
	project?:  #ProjectName
	remotes?: {
		[#RemoteName]: #RemoteObjAuthoring
		[#RemoteNameInvalid]: _|_
	}

	limits?: #LimitsResolved
	vm?:     #VMConfigResolved

	// Normalized mount objects ONLY (no strings, no maps)
	// Authoritative Security Gate: source and path MUST be absolute
	mounts: [...#MountObjResolved]

	networks: [...close({
		name:   string
		ipv4?:  string
		parent: string
	})]

	disks?: [...#DiskObjResolved]

	vswitches?: [...close({
		name:     string
		status:   #VSwitchStatus
		type:     "bridge"
		driver:   "native" | "openvswitch"
		ipv4?:    string
		ipv6:     "none"
		nat:      bool
		group?:   string
		internet: bool
	})]

	network_policy?: close({
		internal_cidrs?: [...string]
		allow: [...close({
			from:      string
			to:        string
			direction: "both" | "egress"
		})]
	})

	image_remotes?: {
		[#ImageRemoteName]: string
		[#ImageRemoteNameInvalid]: _|_
	}

	wait: #WaitConfig

	recipes: [...close({
		run_as:  string
		scripts: [...string] & [_, ...]
	})]

	"cloud-init-include"?: [...string]
	Y1="cloud-init"?:      string
	Y2="cloud-init-file"?: string
	"network-config"?:     string

	if Y1 != _|_ && Y2 != _|_ {
		_|_
	}

	groups?: [...string]

	// Security Posture Fields (D9)
	sudo?:              bool
	"inject_ssh_keys"?: bool
	"ssh_keys"?:        [...string]
})
