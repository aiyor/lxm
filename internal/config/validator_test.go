package config

import (
	"fmt"
	"testing"
)

func TestCUEValidator_Conformance(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	t.Run("F1: status: absent manifest without image passes #LXM_RESOLVED and #LXM_AUTHORING", func(t *testing.T) {
		absentYAML := []byte(`
schema: lxm/config/v2
name: dev-station
user: ubuntu
status: absent
wait:
  required: false
mounts: []
networks: []
recipes: []
`)
		if err := v.ValidateResolved(absentYAML); err != nil {
			t.Errorf("expected absent manifest without image to pass #LXM_RESOLVED, got: %v", err)
		}
		if err := v.ValidateAuthoring(absentYAML); err != nil {
			t.Errorf("expected absent manifest without image to pass #LXM_AUTHORING, got: %v", err)
		}
	})

	t.Run("status: present manifest with valid image passes #LXM_RESOLVED", func(t *testing.T) {
		presentYAML := []byte(`
schema: lxm/config/v2
name: dev-station
image: ubuntu-24.04
user: ubuntu
status: present
wait:
  cloud_init: 10m
  network: 60s
  poll: 5s
  required: true
mounts:
  - source: /home/user/workspace
    path: /mnt/workspace
networks: []
recipes: []
`)
		if err := v.ValidateResolved(presentYAML); err != nil {
			t.Errorf("expected present manifest to pass #LXM_RESOLVED, got: %v", err)
		}
	})

	t.Run("Authoring styles 1-4 pass #LXM_AUTHORING", func(t *testing.T) {
		stylesYAML := []byte(`
schema: lxm/config/v2
name: authoring-test
image: ubuntu-24.04
status: present
vars:
  PROJECT_ROOT: ~/devel/project
wait: false
mounts:
  - /home/user/src:/mnt/src:ro
  - source: ~/devel/project
    path: /mnt/project
recipes:
  - recipes/install.sh
  - root:
      - recipes/setup.sh
  - run_as: superuser
    scripts:
      - recipes/install-tools.sh
`)
		if err := v.ValidateAuthoring(stylesYAML); err != nil {
			t.Errorf("expected authoring styles to pass #LXM_AUTHORING, got: %v", err)
		}
	})

	t.Run("OVN vswitches pass #LXM_AUTHORING", func(t *testing.T) {
		ovnYAML := []byte(`
schema: lxm/config/v2
base: true
image: images:debian/12/cloud
user: debian
wait:
  agent: 3m
vswitches:
  - name: ovn-webbr0
    type: ovn
    parent: lxdbr0
    ipv4: 10.70.0.1/24
    nat: false
    group: web
  - name: ovn-dbbr0
    type: ovn
    parent: lxdbr0
    ipv4: 10.75.0.1/24
    nat: false
    group: db
network_policy:
  allow:
    - from: web
      to: db
      direction: egress
`)
		if err := v.ValidateAuthoring(ovnYAML); err != nil {
			t.Errorf("expected OVN vswitches to pass #LXM_AUTHORING, got: %v", err)
		}
	})

	t.Run("F2: scriptless recipe group rejected by #LXM_AUTHORING", func(t *testing.T) {
		scriptlessYAML := []byte(`
schema: lxm/config/v2
name: glm
image: ubuntu-24.04
recipes:
  - run_as: root
`)
		if err := v.ValidateAuthoring(scriptlessYAML); err == nil {
			t.Errorf("expected scriptless recipe group to be rejected by #LXM_AUTHORING, but it passed")
		}
	})

	t.Run("Unknown top-level key rejected by closed struct #LXM_RESOLVED", func(t *testing.T) {
		unknownKeyYAML := []byte(`
schema: lxm/config/v2
name: dev-station
image: ubuntu-24.04
user: ubuntu
status: present
unknown_field: true
wait:
  required: true
mounts: []
networks: []
recipes: []
`)
		if err := v.ValidateResolved(unknownKeyYAML); err == nil {
			t.Errorf("expected unknown top-level key to be rejected by #LXM_RESOLVED, but it passed")
		}
	})

	t.Run("Mount path /proc rejected by #CleanMountPath", func(t *testing.T) {
		procMountYAML := []byte(`
schema: lxm/config/v2
name: dev-station
image: ubuntu-24.04
user: ubuntu
status: present
wait:
  required: true
mounts:
  - source: /home/user/proc
    path: /proc
networks: []
recipes: []
`)
		if err := v.ValidateResolved(procMountYAML); err == nil {
			t.Errorf("expected /proc mount path to be rejected by #CleanMountPath, but it passed")
		}
	})

	t.Run("Relative mount source path rejected by #MountObjResolved", func(t *testing.T) {
		relativeSourceYAML := []byte(`
schema: lxm/config/v2
name: dev-station
image: ubuntu-24.04
user: ubuntu
status: present
wait:
  required: true
mounts:
  - source: ./relative/path
    path: /mnt/workspace
networks: []
recipes: []
`)
		if err := v.ValidateResolved(relativeSourceYAML); err == nil {
			t.Errorf("expected relative mount source to be rejected by #MountObjResolved, but it passed")
		}
	})

	t.Run("Directive include rejected by #LXM_RESOLVED", func(t *testing.T) {
		directiveYAML := []byte(`
schema: lxm/config/v2
name: dev-station
image: ubuntu-24.04
user: ubuntu
status: present
include: [_base.yaml]
wait:
  required: true
mounts: []
networks: []
recipes: []
`)
		if err := v.ValidateResolved(directiveYAML); err == nil {
			t.Errorf("expected include directive to be rejected by #LXM_RESOLVED, but it passed")
		}
	})

	t.Run("F5: Manifest with both cloud-init and cloud-init-file rejected by #LXM_RESOLVED", func(t *testing.T) {
		bothCloudInitYAML := []byte(`
schema: lxm/config/v2
name: dev-station
image: ubuntu-24.04
user: ubuntu
status: present
cloud-init: "#cloud-config\npackages: [curl]"
cloud-init-file: cloud-init.yaml
wait:
  required: true
mounts: []
networks: []
recipes: []
`)
		if err := v.ValidateResolved(bothCloudInitYAML); err == nil {
			t.Errorf("expected manifest with both cloud-init and cloud-init-file to be rejected by #LXM_RESOLVED, but it passed")
		}
	})
	t.Run("F2: state running/stopped accepted by #LXM_RESOLVED and #LXM_AUTHORING", func(t *testing.T) {
		for _, sv := range []string{"running", "stopped"} {
			stateYAML := fmt.Appendf(nil, `
schema: lxm/config/v2
name: dev-station
image: ubuntu-24.04
user: ubuntu
status: present
state: %s
wait:
  required: true
mounts: []
networks: []
recipes: []
`, sv)
			if err := v.ValidateResolved(stateYAML); err != nil {
				t.Errorf("expected state: %s to pass #LXM_RESOLVED, got: %v", sv, err)
			}
			if err := v.ValidateAuthoring(stateYAML); err != nil {
				t.Errorf("expected state: %s to pass #LXM_AUTHORING, got: %v", sv, err)
			}
		}
	})

	t.Run("Invalid YAML input returns error in ValidateAuthoring and ValidateResolved", func(t *testing.T) {
		badYAML := []byte("[invalid_yaml_unclosed")
		if err := v.ValidateAuthoring(badYAML); err == nil {
			t.Errorf("expected error for invalid YAML in ValidateAuthoring")
		}
		if err := v.ValidateResolved(badYAML); err == nil {
			t.Errorf("expected error for invalid YAML in ValidateResolved")
		}
	})
}

func TestCUEValidator_AuthoringRecipeShorthands(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	t.Run("scripts-only group passes #LXM_AUTHORING (D13 v1 compat)", func(t *testing.T) {
		scriptsOnlyYAML := []byte(`
schema: lxm/config/v2
name: scripts-only
image: ubuntu-24.04
recipes:
  - scripts:
      - recipes/legacy.sh
`)
		if err := v.ValidateAuthoring(scriptsOnlyYAML); err != nil {
			t.Errorf("expected scripts-only group to pass #LXM_AUTHORING, got: %v", err)
		}
	})

	t.Run("run_as object and shorthands still pass #LXM_AUTHORING", func(t *testing.T) {
		stylesYAML := []byte(`
schema: lxm/config/v2
name: authoring-test
image: ubuntu-24.04
recipes:
  - recipes/install.sh
  - root:
      - recipes/setup.sh
  - run_as: superuser
    scripts:
      - recipes/install-tools.sh
`)
		if err := v.ValidateAuthoring(stylesYAML); err != nil {
			t.Errorf("expected recipe shorthands to pass #LXM_AUTHORING, got: %v", err)
		}
	})

	t.Run("empty/comment-only script entries rejected by #LXM_AUTHORING", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			yaml string
		}{
			{"empty scalar", "recipes:\n  - \"\"\n"},
			{"comment scalar", "recipes:\n  - \"# comment\"\n"},
			{"comment-only scripts", "recipes:\n  - run_as: root\n    scripts:\n      - \"# comment\"\n"},
			{"comment-only root", "recipes:\n  - root:\n      - \"# comment\"\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				badYAML := []byte("schema: lxm/config/v2\nname: t\nimage: ubuntu-24.04\n" + tc.yaml)
				if err := v.ValidateAuthoring(badYAML); err == nil {
					t.Errorf("expected %s to be rejected by #LXM_AUTHORING", tc.name)
				}
			})
		}
	})
}

func TestCUEValidator_RemotesAuthoring(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	// Positive controls: legitimate configurations MUST pass
	t.Run("insecure alone passes #LXM_AUTHORING", func(t *testing.T) {
		validYAML := []byte(`
schema: lxm/config/v2
name: t
image: ubuntu-24.04
remotes:
  lab:
    address: https://10.0.0.1:8443
    insecure: true
`)
		if err := v.ValidateAuthoring(validYAML); err != nil {
			t.Fatalf("expected insecure: true alone to pass #LXM_AUTHORING, got: %v", err)
		}
	})

	t.Run("insecure false with server_certificate passes #LXM_AUTHORING", func(t *testing.T) {
		validYAML := []byte(`
schema: lxm/config/v2
name: t
image: ubuntu-24.04
remotes:
  lab:
    address: https://10.0.0.1:8443
    insecure: false
    server_certificate: "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----"
`)
		if err := v.ValidateAuthoring(validYAML); err != nil {
			t.Fatalf("expected insecure: false + server_certificate to pass #LXM_AUTHORING, got: %v", err)
		}
	})

	t.Run("secure with certificate and fingerprint passes #LXM_AUTHORING", func(t *testing.T) {
		validYAML := []byte(`
schema: lxm/config/v2
name: t
image: ubuntu-24.04
remotes:
  lab:
    address: https://10.0.0.1:8443
    server_certificate: "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----"
    server_fingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
`)
		if err := v.ValidateAuthoring(validYAML); err != nil {
			t.Fatalf("expected secure + cert + fingerprint to pass #LXM_AUTHORING, got: %v", err)
		}
	})

	t.Run("unix endpoint passes #LXM_AUTHORING", func(t *testing.T) {
		validYAML := []byte(`
schema: lxm/config/v2
name: t
image: ubuntu-24.04
remotes:
  lab-unix:
    address: unix:///run/incus/unix.socket
`)
		if err := v.ValidateAuthoring(validYAML); err != nil {
			t.Fatalf("expected unix remote to pass #LXM_AUTHORING, got: %v", err)
		}
	})

	// Negative controls: contradictory configurations MUST fail
	t.Run("insecure true with server_certificate rejected by #LXM_AUTHORING", func(t *testing.T) {
		badCertYAML := []byte(`
schema: lxm/config/v2
name: t
image: ubuntu-24.04
remotes:
  lab:
    address: https://10.0.0.1:8443
    insecure: true
    server_certificate: "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----"
`)
		err := v.ValidateAuthoring(badCertYAML)
		if err == nil {
			t.Fatalf("expected insecure: true + server_certificate to be rejected by #LXM_AUTHORING")
		}
	})

	t.Run("insecure true with server_fingerprint rejected by #LXM_AUTHORING", func(t *testing.T) {
		badFpYAML := []byte(`
schema: lxm/config/v2
name: t
image: ubuntu-24.04
remotes:
  lab:
    address: https://10.0.0.1:8443
    insecure: true
    server_fingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
`)
		err := v.ValidateAuthoring(badFpYAML)
		if err == nil {
			t.Fatalf("expected insecure: true + server_fingerprint to be rejected by #LXM_AUTHORING")
		}
	})
}
