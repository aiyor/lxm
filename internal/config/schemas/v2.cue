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

// Mount path restrictions: absolute, cleaned, non-root system path
#CleanMountPath: string & {
	strings.MinRunes(2)
	!~"^/(proc|sys|dev)(/|$)"
	!~"^/$"
}

// Compact string mount: "HostPath:ContainerPath[:ro|:rw]"
#MountStr: =~"^.+:.+(:(ro|rw))?$"

// Authoring mount object (allows raw tilde '~' and '{{ .Vars.* }}' template sources)
#MountObjAuthoring: close({
	source:    string
	path:      #CleanMountPath
	readonly?: bool | *false
	recursive?: bool | *false
})

// Authoring mount object for the Style 2 map form: the map key supplies the
// container path when path is omitted (the loader fills it in).
#MountMapObjAuthoring: close({
	source:     string
	path?:      #CleanMountPath
	readonly?:  bool | *false
	recursive?: bool | *false
})

// Resolved mount object (strictly enforces absolute host source path)
#MountObjResolved: close({
	source:    string & =~"^/"
	path:      #CleanMountPath
	readonly?: bool | *false
	recursive?: bool | *false
})

// Wait policy struct with recursive presence tracking
#WaitConfig: close({
	cloud_init?: string | *"10m"
	network?:    string | *"60s"
	poll?:       string | *"5s"
	required?:   bool   | *true
})

// ============================================================================
// 1. #LXM_AUTHORING — Human Authoring Schema (Used for IDE JSON Schema Export)
// ============================================================================

#LXM_AUTHORING: close({
	schema?: "lxm/config/v2"
	base?:   bool | *false
	name?:   string
	image?:  #ImageRef
	user?:   string | *"ubuntu"
	status?: "present" | "absent" | *"present"
	state?:  "running" | "stopped" // explicit power state; overrides status-derived default (F2)

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

	// Wait policy: accepts scalar bool (shorthand) or struct
	wait?: bool | #WaitConfig

	// Recipes: accepts script paths, root shorthand, run_as objects, and
	// legacy scripts-only groups (run_as defaults to root, matching the
	// loader normalization and the recipe metadata schema). Empty or
	// comment-only script entries are rejected loudly (mirroring the
	// migrator's Transform 8 emptiness check).
	recipes?: [...((string & != "" & !~"^\\s*#") | close({root: [...(string & != "" & !~"^\\s*#")] & [_, ...]}) | close({
		run_as?:  string | *"root"
		scripts:  [...(string & != "" & !~"^\\s*#")] & [_, ...]
	}))]

	// Cloud-Init inclusion & configuration (Hyphenated v1 keys preserved)
	"cloud-init-include"?: [...string]
	X1="cloud-init"?:      string
	X2="cloud-init-file"?: string
	"network-config"?:  string

	if X1 != _|_ && X2 != _|_ {
		_|_
	}

	// Inheritance & List Modification Directives
	include?: [...string]
	remove?: close({
		mounts?:   [...string]
		networks?: [...string]
		recipes?:  [...string]
	})
	replace?: close({
		mounts?:   [...(#MountStr | #MountObjAuthoring)]
		networks?: [...]
		recipes?:  [...]
	})

	groups?: [...string]

	// Security Posture Fields (D9)
	sudo?:              bool
	"inject_ssh_keys"?: bool
	"ssh_keys"?:       [...string]
})

// ============================================================================
// 2. #LXM_RESOLVED — Strict Canonical Manifest Schema (Consumed by Reconciler)
// ============================================================================

#LXM_RESOLVED: close({
	schema: "lxm/config/v2"
	name:   string & strings.MinRunes(1)
	image?: #ImageRef  // Optional for status: absent; status: present => image required is a Go invariant (F1)
	user:   string & strings.MinRunes(1)
	status: "present" | "absent"
	state?: "running" | "stopped" // Go normalizer default "running"; state with status: absent is a Go ValidatePostMerge error (F2)

	// Normalized mount objects ONLY (no strings, no maps)
	// Authoritative Security Gate: source and path MUST be absolute
	mounts: [...#MountObjResolved]

	networks: [...close({
		name:   string
		ipv4?:  string
		parent: string
	})]

	wait: #WaitConfig

	recipes: [...close({
		run_as:  string
		scripts: [...string] & [_, ...]
	})]

	"cloud-init-include"?: [...string]
	Y1="cloud-init"?:      string
	Y2="cloud-init-file"?: string
	"network-config"?:  string

	if Y1 != _|_ && Y2 != _|_ {
		_|_
	}

	groups?: [...string]

	// Security Posture Fields (D9)
	sudo?:              bool
	"inject_ssh_keys"?: bool
	"ssh_keys"?:       [...string]
})
