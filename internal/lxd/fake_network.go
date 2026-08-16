package lxd

import (
	"fmt"

	"github.com/canonical/lxd/shared/api"
)

var _ NetworkService = (*FakeInstanceServer)(nil)

// Networks holds the in-memory network and ACL state backing the fake
// NetworkService implementation.
type Networks struct {
	Networks     map[string]*api.Network
	NetworkETags map[string]string
	ACLs         map[string]*api.NetworkACL
	ACLETags     map[string]string
}

// AddNetworks initializes the network backing stores (called by
// NewFakeInstanceServer).
func (f *FakeInstanceServer) AddNetworks() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Nets == nil {
		f.Nets = &Networks{
			Networks:     make(map[string]*api.Network),
			NetworkETags: make(map[string]string),
			ACLs:         make(map[string]*api.NetworkACL),
			ACLETags:     make(map[string]string),
		}
	}
}

func (f *FakeInstanceServer) GetNetworks() ([]api.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetNetworksFunc != nil {
		return f.GetNetworksFunc()
	}
	if f.Nets == nil {
		return nil, nil
	}
	var out []api.Network
	for _, n := range f.Nets.Networks {
		out = append(out, *n)
	}
	return out, nil
}

func (f *FakeInstanceServer) GetNetwork(name string) (*api.Network, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Nets == nil {
		return nil, "", fmt.Errorf("network %q not found", name)
	}
	n, ok := f.Nets.Networks[name]
	if !ok {
		return nil, "", fmt.Errorf("network %q not found", name)
	}
	etag := f.Nets.NetworkETags[name]
	if etag == "" {
		etag = "net-etag-1"
	}
	return n, etag, nil
}

func (f *FakeInstanceServer) CreateNetwork(req api.NetworksPost) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateNetworkFunc != nil {
		return f.CreateNetworkFunc(req)
	}
	f.AddNetworksLocked()
	if _, ok := f.Nets.Networks[req.Name]; ok {
		return fmt.Errorf("network %q already exists", req.Name)
	}
	cfg := req.Config
	if cfg == nil {
		cfg = make(map[string]string)
	}
	f.Nets.Networks[req.Name] = &api.Network{
		Name:        req.Name,
		Type:        req.Type,
		Description: req.Description,
		Config:      cfg,
		Managed:     true,
	}
	f.Nets.NetworkETags[req.Name] = "net-etag-created"
	return nil
}

func (f *FakeInstanceServer) UpdateNetwork(name string, put api.NetworkPut, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Nets == nil {
		return fmt.Errorf("network %q not found", name)
	}
	n, ok := f.Nets.Networks[name]
	if !ok {
		return fmt.Errorf("network %q not found", name)
	}
	currentETag := f.Nets.NetworkETags[name]
	if etag != "" && currentETag != "" && etag != currentETag {
		return fmt.Errorf("%s: %s vs %s. The configuration has been modified since this change began. Please retrieve the updated configuration before proceeding.", ETagConflictPrefix, etag, currentETag)
	}
	n.Config = put.Config
	n.Description = put.Description
	f.Nets.NetworkETags[name] = "net-etag-updated"
	return nil
}

func (f *FakeInstanceServer) DeleteNetwork(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeleteNetworkFunc != nil {
		return f.DeleteNetworkFunc(name)
	}
	if f.Nets == nil {
		return fmt.Errorf("network %q not found", name)
	}
	if _, ok := f.Nets.Networks[name]; !ok {
		return fmt.Errorf("network %q not found", name)
	}
	delete(f.Nets.Networks, name)
	delete(f.Nets.NetworkETags, name)
	return nil
}

func (f *FakeInstanceServer) GetNetworkACLs() ([]api.NetworkACL, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Nets == nil {
		return nil, nil
	}
	var out []api.NetworkACL
	for _, a := range f.Nets.ACLs {
		out = append(out, *a)
	}
	return out, nil
}

func (f *FakeInstanceServer) GetNetworkACL(name string) (*api.NetworkACL, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Nets == nil {
		return nil, "", fmt.Errorf("network ACL %q not found", name)
	}
	a, ok := f.Nets.ACLs[name]
	if !ok {
		return nil, "", fmt.Errorf("network ACL %q not found", name)
	}
	etag := f.Nets.ACLETags[name]
	if etag == "" {
		etag = "acl-etag-1"
	}
	return a, etag, nil
}

func (f *FakeInstanceServer) CreateNetworkACL(acl api.NetworkACLsPost) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateNetworkACLFunc != nil {
		return f.CreateNetworkACLFunc(acl)
	}
	f.AddNetworksLocked()
	if _, ok := f.Nets.ACLs[acl.Name]; ok {
		return fmt.Errorf("network ACL %q already exists", acl.Name)
	}
	cfg := acl.Config
	if cfg == nil {
		cfg = make(map[string]string)
	}
	f.Nets.ACLs[acl.Name] = &api.NetworkACL{
		Name:        acl.Name,
		Description: acl.Description,
		Ingress:     acl.Ingress,
		Egress:      acl.Egress,
		Config:      cfg,
	}
	f.Nets.ACLETags[acl.Name] = "acl-etag-created"
	return nil
}

func (f *FakeInstanceServer) UpdateNetworkACL(name string, put api.NetworkACLPut, etag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Nets == nil {
		return fmt.Errorf("network ACL %q not found", name)
	}
	a, ok := f.Nets.ACLs[name]
	if !ok {
		return fmt.Errorf("network ACL %q not found", name)
	}
	currentETag := f.Nets.ACLETags[name]
	if etag != "" && currentETag != "" && etag != currentETag {
		return fmt.Errorf("%s: %s vs %s. The configuration has been modified since this change began. Please retrieve the updated configuration before proceeding.", ETagConflictPrefix, etag, currentETag)
	}
	a.Config = put.Config
	a.Description = put.Description
	a.Ingress = put.Ingress
	a.Egress = put.Egress
	f.Nets.ACLETags[name] = "acl-etag-updated"
	return nil
}

func (f *FakeInstanceServer) DeleteNetworkACL(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DeleteNetworkACLFunc != nil {
		return f.DeleteNetworkACLFunc(name)
	}
	if f.Nets == nil {
		return fmt.Errorf("network ACL %q not found", name)
	}
	if _, ok := f.Nets.ACLs[name]; !ok {
		return fmt.Errorf("network ACL %q not found", name)
	}
	delete(f.Nets.ACLs, name)
	delete(f.Nets.ACLETags, name)
	return nil
}

// AddNetworksLocked initializes the network stores; callers must hold f.mu.
func (f *FakeInstanceServer) AddNetworksLocked() {
	if f.Nets == nil {
		f.Nets = &Networks{
			Networks:     make(map[string]*api.Network),
			NetworkETags: make(map[string]string),
			ACLs:         make(map[string]*api.NetworkACL),
			ACLETags:     make(map[string]string),
		}
	}
}
