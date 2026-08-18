package lxd_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	lxd_client "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"

	"github.com/aiyor/lxm/internal/provider/lxd"
)

func TestClassifyLXDError(t *testing.T) {
	d := lxd.NewDriver(nil)

	tests := []struct {
		name         string
		err          error
		intent       string
		wantExitCode int
		wantRetry    bool
	}{
		{
			name:         "nil error",
			err:          nil,
			intent:       "lookup",
			wantExitCode: 0,
			wantRetry:    false,
		},
		{
			name:         "not found with lookup intent",
			err:          errors.New("instance not found"),
			intent:       "lookup",
			wantExitCode: 5,
			wantRetry:    false,
		},
		{
			name:         "not found with existence check intent",
			err:          errors.New("instance not found"),
			intent:       "check",
			wantExitCode: 0,
			wantRetry:    false,
		},
		{
			name:         "404 StatusError lookup",
			err:          api.StatusErrorf(404, "not found"),
			intent:       "lookup",
			wantExitCode: 5,
			wantRetry:    false,
		},
		{
			name:         "412 StatusError ETag conflict",
			err:          api.StatusErrorf(412, "ETag does not match: abc vs def"),
			intent:       "mutate",
			wantExitCode: 4,
			wantRetry:    true,
		},
		{
			name:         "Generic ETag conflict message",
			err:          fmt.Errorf("ETag does not match: 123 vs 456. The configuration has been modified since this change began."),
			intent:       "mutate",
			wantExitCode: 4,
			wantRetry:    true,
		},
		{
			name:         "Generic error",
			err:          errors.New("connection refused"),
			intent:       "mutate",
			wantExitCode: 4,
			wantRetry:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, retry := d.ClassifyError(tt.err, tt.intent)
			if code != tt.wantExitCode || retry != tt.wantRetry {
				t.Errorf("ClassifyError(%v, %q) = (%d, %v), want (%d, %v)", tt.err, tt.intent, code, retry, tt.wantExitCode, tt.wantRetry)
			}
		})
	}
}

func TestLXDDriverMockServer(t *testing.T) {
	mux := http.NewServeMux()

	// Server info /1.0
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1.0" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"auth":"trusted","api_extensions":["instances_rebuild","custom_block_volumes","clustering","container_full","instance_full","network","network_acl","storage","storage_api_custom_volume_handling","projects"],"environment":{"server_name":"mock-lxd","server_clustered":true}}}`)
	})

	// Instances
	mux.HandleFunc("/1.0/instances", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":[{"name":"c1","status":"Running","status_code":103,"type":"container","config":{"image.os":"ubuntu"},"state":{"status":"Running","status_code":103,"network":{"eth0":{"addresses":[{"family":"inet","address":"10.0.0.5","scope":"global"}]}}}}]}`)
		} else {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200}`)
		}
	})

	mux.HandleFunc("/1.0/instances/c1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "etag-c1")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"name":"c1","status":"Running","status_code":103,"type":"container","config":{"image.os":"ubuntu"}}}`)
		} else {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200}`)
		}
	})

	mux.HandleFunc("/1.0/instances/c1/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"status":"Running","status_code":103,"network":{"eth0":{"addresses":[{"family":"inet","address":"10.0.0.5","scope":"global"}]}}}}`)
		} else {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200}`)
		}
	})

	mux.HandleFunc("/1.0/instances/c1/snapshots", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":[{"name":"c1/snap0","stateful":false}]}`)
		} else {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200}`)
		}
	})

	mux.HandleFunc("/1.0/networks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":[{"name":"lxdbr0","type":"bridge","config":{}}]}`)
		} else {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200}`)
		}
	})

	mux.HandleFunc("/1.0/networks/lxdbr0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"name":"lxdbr0","type":"bridge","config":{}}}`)
		} else {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200}`)
		}
	})

	mux.HandleFunc("/1.0/network-acls", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":[{"name":"acl1","egress":[],"ingress":[]}]}`)
		} else {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200}`)
		}
	})

	mux.HandleFunc("/1.0/storage-pools/default/volumes/custom", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":[{"name":"vol1","type":"custom","content_type":"block"}]}`)
		} else {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200}`)
		}
	})

	mux.HandleFunc("/1.0/cluster/members", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":[{"server_name":"node1","status":"Online"}]}`)
	})

	mux.HandleFunc("/1.0/projects", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":["/1.0/projects/default"]}`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	d, err := lxd.NewRemoteDriver(srv.URL, &lxd_client.ConnectionArgs{
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("NewRemoteDriver failed: %v", err)
	}

	if d.ProviderType() != "lxd" {
		t.Errorf("expected provider type lxd, got: %s", d.ProviderType())
	}
	if !d.HasExtension("instances_rebuild") {
		t.Errorf("expected instances_rebuild extension")
	}
	if !d.IsClustered() {
		t.Errorf("expected clustered")
	}

	members, err := d.GetClusterMembers()
	if err != nil || len(members) != 1 || members[0].ServerName != "node1" {
		t.Errorf("unexpected cluster members: %+v (err=%v)", members, err)
	}

	instances, err := d.ListInstances()
	if err != nil || len(instances) != 1 || instances[0].Name != "c1" {
		t.Fatalf("unexpected instances: %+v (err=%v)", instances, err)
	}
	if instances[0].State == nil || len(instances[0].State.Network) == 0 {
		t.Errorf("expected state and network on instance: %+v", instances[0])
	}

	ip, err := d.GetIP("c1")
	if err != nil || ip != "10.0.0.5" {
		t.Errorf("unexpected IP for c1: %s (err=%v)", ip, err)
	}

	networks, err := d.GetNetworks()
	if err != nil || len(networks) != 1 {
		t.Errorf("unexpected networks: %+v (err=%v)", networks, err)
	}

	acls, err := d.GetNetworkACLs()
	if err != nil || len(acls) != 1 {
		t.Errorf("unexpected network acls: %+v (err=%v)", acls, err)
	}

	projects, err := d.GetProjects()
	if err != nil || len(projects) != 1 {
		t.Errorf("unexpected projects: %+v (err=%v)", projects, err)
	}
}
