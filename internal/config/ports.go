package config

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/aiyor/lxm/internal/ports"
)

// validatePorts enforces per-instance published-port rules (issue #11). It is
// invoked from validateCommon (root-only, after type normalization). Cross-field
// rules are skipped for base fragments: they are validated on the resolved leaf
// manifest, where the instance type and networks are known.
func (conf *Config) validatePorts() error {
	if len(conf.Ports) == 0 {
		return nil
	}
	if conf.Base {
		return nil
	}

	instance := conf.Name
	if instance == "" {
		instance = "?"
	}

	nicNames := make(map[string]bool, len(conf.Networks))
	staticIPv4 := make([]netip.Addr, 0, len(conf.Networks))
	for i := range conf.Networks {
		name := conf.Networks[i].Name
		if name == "" {
			name = "eth0"
		}
		nicNames[name] = true

		raw := strings.TrimSpace(conf.Networks[i].IPv4)
		if raw == "" {
			continue
		}
		// net.ParseIP (used by the pre-existing network validation) accepts
		// IPv6 literals in the IPv4 field; NAT validation must not.
		addr, err := netip.ParseAddr(raw)
		if err != nil || !addr.Is4() {
			continue
		}
		staticIPv4 = append(staticIPv4, addr)
	}

	isVM := conf.Type == "virtual-machine" || conf.Type == "vm"

	type declaredListener struct {
		name     string
		listener ports.Listener
	}
	listeners := make([]declaredListener, 0, len(conf.Ports))

	for i := range conf.Ports {
		p := &conf.Ports[i]

		listen, err := ports.ParseEndpoint(p.Proto, p.Listen)
		if err != nil {
			return fmt.Errorf("port %q of instance %q: invalid listen %q: %w", p.Name, instance, p.Listen, err)
		}
		connect, err := ports.ParseEndpoint(p.Proto, p.Connect)
		if err != nil {
			return fmt.Errorf("port %q of instance %q: invalid connect %q: %w", p.Name, instance, p.Connect, err)
		}

		// A NIC named like a port device would be silently overwritten by the
		// port in the instance PUT device map (step 9 after step 7).
		devName := ports.DeviceName(p.Name)
		if nicNames[devName] {
			return fmt.Errorf("port %q of instance %q: NIC %q would collide with device %q", p.Name, instance, devName, devName)
		}

		if isVM && !p.NAT {
			return fmt.Errorf("port %q of instance %q: virtual machines require nat: true (daemon limitation)", p.Name, instance)
		}

		if connect.IsWildcard() {
			return fmt.Errorf("port %q of instance %q: wildcard connect is not supported; use an explicit instance address", p.Name, instance)
		}

		if p.NAT {
			if listen.IsIPv6() || connect.IsIPv6() {
				return fmt.Errorf("port %q of instance %q: nat: true currently supports IPv4 only", p.Name, instance)
			}
			if listen.IsWildcard() {
				return fmt.Errorf("port %q of instance %q: nat: true requires an explicit, non-wildcard listen address", p.Name, instance)
			}
			if listen.IsLoopback() {
				// Daemons accept this configuration but NAT is DNAT-only with
				// no route_localnet handling, so host-local traffic to a
				// loopback listen is dropped before it reaches the instance.
				return fmt.Errorf("port %q of instance %q: nat: true cannot listen on a loopback address (unusable on both daemons)", p.Name, instance)
			}
			if len(staticIPv4) == 0 {
				return fmt.Errorf("port %q of instance %q: nat: true requires a static ipv4 in networks (add networks[].ipv4)", p.Name, instance)
			}
			matched := false
			for _, addr := range staticIPv4 {
				if connect.Addr == addr {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("port %q of instance %q: nat: true connect must be an explicit declared static ipv4 (got %q)", p.Name, instance, connect.HostPort())
			}
		}

		// Same-instance duplicates: pair-wise so dual-stack wildcard binds
		// (0.0.0.0 / ::) are caught against specific addresses too.
		current := ports.NewListener(listen, p.NAT)
		for _, prev := range listeners {
			if ports.ListenersConflict(prev.listener, current) {
				return fmt.Errorf("port %q of instance %q: listen %s %s duplicates port %q", p.Name, instance, listen.Proto, listen.HostPort(), prev.name)
			}
		}
		listeners = append(listeners, declaredListener{name: p.Name, listener: current})
	}

	return nil
}

// ValidateFleetPorts validates cross-manifest listener uniqueness across the
// full loaded manifest set. Same-instance duplicates and NIC/port device-name
// collisions are already rejected by validatePorts. Errors are exit-3 config
// errors; the warnings return is reserved for future cluster-scope notices.
func ValidateFleetPorts(configs []*Config) (warnings []string, err error) {
	type entry struct {
		instance string
		file     string
		port     string
		listener ports.Listener
	}

	sorted := make([]*Config, 0, len(configs))
	for _, conf := range configs {
		if conf == nil || conf.Base || conf.Name == "" || conf.Status == "absent" {
			continue
		}
		sorted = append(sorted, conf)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ConfigFile < sorted[j].ConfigFile
	})

	entries := make([]entry, 0)
	for _, conf := range sorted {
		file := conf.ConfigFile
		if file == "" {
			file = conf.ConfigBaseDir
		}
		for i := range conf.Ports {
			p := &conf.Ports[i]
			listen, parseErr := ports.ParseEndpoint(p.Proto, p.Listen)
			if parseErr != nil {
				return nil, fmt.Errorf("port %q of instance %q: invalid listen %q: %w", p.Name, conf.Name, p.Listen, parseErr)
			}
			current := ports.NewListener(listen, p.NAT)

			for _, prev := range entries {
				if prev.instance == conf.Name && prev.file == file {
					continue // same-instance duplicates within one file are a validatePorts error
				}
				if !ports.ListenersConflict(prev.listener, current) {
					continue
				}
				if prev.listener.Addr.IsUnspecified() || current.Addr.IsUnspecified() {
					return nil, fmt.Errorf("port %s %s of instance %q overlaps listener %s %s of instance %q (dual-stack wildcard binds conflict with both address families) (%s and %s)",
						listen.Proto, listen.HostPort(), conf.Name,
						prev.listener.Proto, netip.AddrPortFrom(prev.listener.Addr, prev.listener.Port).String(), prev.instance,
						prev.file, file)
				}
				return nil, fmt.Errorf("port %s %s is published by both instance %q (%s) and instance %q (%s)",
					listen.Proto, listen.HostPort(), prev.instance, prev.file, conf.Name, file)
			}
			entries = append(entries, entry{instance: conf.Name, file: file, port: p.Name, listener: current})
		}
	}

	return nil, nil
}
