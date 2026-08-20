package incus_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	incus_client "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/incus"
)

func TestClassifyIncusError(t *testing.T) {
	d := incus.NewDriver(nil)

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

func TestIncusDriverMockServer(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()

	// Server info /1.0
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1.0" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"auth":"trusted","api_extensions":["instances_rebuild","custom_block_volumes","clustering","container_full","instance_full","network","network_acl","storage","storage_api_custom_volume_handling","projects"],"environment":{"server_name":"mock-incus","server_clustered":true}}}`)
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
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":[{"name":"incusbr0","type":"bridge","config":{}}]}`)
		} else {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200}`)
		}
	})

	mux.HandleFunc("/1.0/networks/incusbr0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"name":"incusbr0","type":"bridge","config":{}}}`)
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

	d, err := incus.NewRemoteDriver(srv.URL, &incus_client.ConnectionArgs{
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("NewRemoteDriver failed: %v", err)
	}

	if d.ProviderType() != "incus" {
		t.Errorf("expected provider type incus, got: %s", d.ProviderType())
	}
	if !d.HasExtension("instances_rebuild") {
		t.Errorf("expected instances_rebuild extension")
	}
	clustered, err := d.IsClustered(ctx)
	if err != nil || !clustered {
		t.Errorf("expected clustered, got %v, err=%v", clustered, err)
	}

	members, err := d.GetClusterMembers(ctx)
	if err != nil || len(members) != 1 || members[0].ServerName != "node1" {
		t.Errorf("unexpected cluster members: %+v (err=%v)", members, err)
	}

	instances, err := d.ListInstances(ctx)
	if err != nil || len(instances) != 1 || instances[0].Name != "c1" {
		t.Fatalf("unexpected instances: %+v (err=%v)", instances, err)
	}
	if instances[0].State == nil || len(instances[0].State.Network) == 0 {
		t.Errorf("expected state and network on instance: %+v", instances[0])
	}

	ip, err := d.GetIP(ctx, "c1")
	if err != nil || ip != "10.0.0.5" {
		t.Errorf("unexpected IP for c1: %s (err=%v)", ip, err)
	}

	networks, err := d.GetNetworks(ctx)
	if err != nil || len(networks) != 1 {
		t.Errorf("unexpected networks: %+v (err=%v)", networks, err)
	}

	acls, err := d.GetNetworkACLs(ctx)
	if err != nil || len(acls) != 1 {
		t.Errorf("unexpected network acls: %+v (err=%v)", acls, err)
	}

	projects, err := d.GetProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Errorf("unexpected projects: %+v (err=%v)", projects, err)
	}
}

// clusterNetworkMock is a minimal in-memory Incus daemon for exercising the
// CreateNetwork cluster-member staging path without a live cluster.
type clusterNetworkMock struct {
	clustered   bool
	failMember  string
	memberHas   map[string]bool
	globalHas   bool
	staged      []api.NetworksPost
	globalPosts []api.NetworksPost
}

func newClusterNetworkMock(t *testing.T, clustered bool, failMember string) (*clusterNetworkMock, *httptest.Server) {
	t.Helper()
	m := &clusterNetworkMock{
		clustered:  clustered,
		failMember: failMember,
		memberHas:  map[string]bool{},
	}
	mux := http.NewServeMux()

	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		clusteredJSON := "false"
		if m.clustered {
			clusteredJSON = "true"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"auth":"trusted","api_extensions":["clustering","network"],"environment":{"server_name":"mock","server_clustered":%s}}}`, clusteredJSON)
	})

	mux.HandleFunc("/1.0/cluster/members", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":[{"server_name":"node1","status":"Online"},{"server_name":"node2","status":"Online"}]}`)
	})

	mux.HandleFunc("/1.0/networks", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":[]}`)
		case http.MethodPost:
			var post api.NetworksPost
			if err := json.NewDecoder(r.Body).Decode(&post); err != nil {
				t.Errorf("decode networks post: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			target := r.URL.Query().Get("target")
			if target != "" {
				if target == m.failMember {
					writeClusterMockError(w, http.StatusInternalServerError, "failed to stage network")
					return
				}
				m.staged = append(m.staged, post)
				m.memberHas[target] = true
			} else {
				m.globalPosts = append(m.globalPosts, post)
				m.globalHas = true
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200}`)
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/1.0/networks/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/1.0/networks/")
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		has := m.globalHas
		if target := r.URL.Query().Get("target"); target != "" {
			has = m.memberHas[target]
		}
		if !has {
			writeClusterMockError(w, http.StatusNotFound, "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"name":%q,"type":"bridge","status":"Created","managed":true,"config":{}}}`, name)
	})

	srv := httptest.NewServer(mux)
	return m, srv
}

func writeClusterMockError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"type":"error","error":%q,"error_code":%d}`, msg, code)
}

func newIncusClusterTestDriver(t *testing.T, srv *httptest.Server) *incus.Driver {
	t.Helper()
	d, err := incus.NewRemoteDriver(srv.URL, &incus_client.ConnectionArgs{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("NewRemoteDriver: %v", err)
	}
	return d
}

func TestIncusDriver_CreateNetwork_ClusterStaging(t *testing.T) {
	ctx := context.Background()
	req := provider.NetworkCreateRequest{
		Name: "testbr0",
		Type: "bridge",
		Config: map[string]string{
			"ipv4.address":     "10.30.0.1/24",
			"user.lxm.managed": "true",
		},
	}
	ovnReq := provider.NetworkCreateRequest{
		Name:   "ovn0",
		Type:   "ovn",
		Config: map[string]string{"network": "incusbr0", "ipv4.address": "10.60.0.1/24"},
	}

	t.Run("stages every member with the full payload then creates globally", func(t *testing.T) {
		m, srv := newClusterNetworkMock(t, true, "")
		defer srv.Close()
		d := newIncusClusterTestDriver(t, srv)
		if err := d.CreateNetwork(ctx, req); err != nil {
			t.Fatalf("CreateNetwork: %v", err)
		}
		if len(m.staged) != 2 {
			t.Fatalf("expected 2 member staging creates, got %d", len(m.staged))
		}
		for _, s := range m.staged {
			if s.Type != "bridge" || s.Config["ipv4.address"] != "10.30.0.1/24" || s.Config["user.lxm.managed"] != "true" {
				t.Errorf("member staging payload incomplete: %+v", s)
			}
		}
		if len(m.globalPosts) != 1 {
			t.Fatalf("expected 1 global create, got %d", len(m.globalPosts))
		}
		if got := m.globalPosts[0].Config["ipv4.address"]; got != "10.30.0.1/24" {
			t.Errorf("global create payload lost config: %+v", m.globalPosts[0])
		}
	})

	t.Run("skips members that already host the network", func(t *testing.T) {
		m, srv := newClusterNetworkMock(t, true, "")
		m.memberHas["node1"] = true
		defer srv.Close()
		d := newIncusClusterTestDriver(t, srv)
		if err := d.CreateNetwork(ctx, req); err != nil {
			t.Fatalf("CreateNetwork: %v", err)
		}
		if len(m.staged) != 1 {
			t.Fatalf("expected only node2 to be staged, got %d", len(m.staged))
		}
	})

	t.Run("propagates member staging errors and aborts before the global create", func(t *testing.T) {
		m, srv := newClusterNetworkMock(t, true, "node1")
		defer srv.Close()
		d := newIncusClusterTestDriver(t, srv)
		err := d.CreateNetwork(ctx, req)
		if err == nil || !strings.Contains(err.Error(), "node1") {
			t.Fatalf("expected staging error mentioning node1, got: %v", err)
		}
		if len(m.globalPosts) != 0 {
			t.Errorf("global create must not run after a staging failure, got %d", len(m.globalPosts))
		}
	})

	t.Run("does not stage members for OVN networks", func(t *testing.T) {
		m, srv := newClusterNetworkMock(t, true, "")
		defer srv.Close()
		d := newIncusClusterTestDriver(t, srv)
		if err := d.CreateNetwork(ctx, ovnReq); err != nil {
			t.Fatalf("CreateNetwork: %v", err)
		}
		if len(m.staged) != 0 {
			t.Errorf("OVN networks must not be staged per-member, got %d", len(m.staged))
		}
		if len(m.globalPosts) != 1 {
			t.Errorf("expected 1 global create, got %d", len(m.globalPosts))
		}
	})

	t.Run("does not stage on a non-clustered server", func(t *testing.T) {
		m, srv := newClusterNetworkMock(t, false, "")
		defer srv.Close()
		d := newIncusClusterTestDriver(t, srv)
		if err := d.CreateNetwork(ctx, req); err != nil {
			t.Fatalf("CreateNetwork: %v", err)
		}
		if len(m.staged) != 0 {
			t.Errorf("single-node server must not stage members, got %d", len(m.staged))
		}
		if len(m.globalPosts) != 1 {
			t.Errorf("expected 1 global create, got %d", len(m.globalPosts))
		}
	})
}
