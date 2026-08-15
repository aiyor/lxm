package network

import (
	"net"
	"reflect"
	"sort"
	"testing"
)

func mustParse(t *testing.T, s string) *net.IPNet {
	t.Helper()
	n, err := ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	return n
}

func TestSubtractCIDRs_Golden_108Minus105024(t *testing.T) {
	super := mustParse(t, "10.0.0.0/8")
	carve := mustParse(t, "10.50.0.0/24")
	got := SubtractCIDRs(super, []*net.IPNet{carve})

	want := []string{
		"10.0.0.0/11",
		"10.32.0.0/12",
		"10.48.0.0/15",
		"10.50.1.0/24",
		"10.50.2.0/23",
		"10.50.4.0/22",
		"10.50.8.0/21",
		"10.50.16.0/20",
		"10.50.32.0/19",
		"10.50.64.0/18",
		"10.50.128.0/17",
		"10.51.0.0/16",
		"10.52.0.0/14",
		"10.56.0.0/13",
		"10.64.0.0/10",
		"10.128.0.0/9",
	}

	var gotStrs []string
	for _, p := range got {
		gotStrs = append(gotStrs, p.String())
	}
	sort.Strings(gotStrs)
	sort.Strings(want)
	if !reflect.DeepEqual(gotStrs, want) {
		t.Fatalf("decomposition mismatch:\ngot  %v\nwant %v", gotStrs, want)
	}

	// The carved-out subnet must be fully covered by the emitted prefixes plus
	// the carve itself: total host count check.
	var emittedHosts float64
	for _, p := range got {
		ones, bits := p.Mask.Size()
		hosts := 1 << uint(bits-ones)
		emittedHosts += float64(hosts)
	}
	wantHosts := float64(1 << 24) // /8
	carveHosts := float64(1 << 8) // /24
	if emittedHosts+carveHosts != wantHosts {
		t.Fatalf("host accounting mismatch: emitted=%v carve=%v total=%v want=%v", emittedHosts, carveHosts, emittedHosts+carveHosts, wantHosts)
	}
}

func TestSubtractCIDRs_Property_DisjointAndCoverage(t *testing.T) {
	super := mustParse(t, "10.0.0.0/8")
	carves := []string{"10.50.0.0/24", "10.0.0.0/16", "172.16.0.0/12"}
	var carveNets []*net.IPNet
	for _, c := range carves {
		if !super.Contains(mustParse(t, c).IP) {
			continue
		}
		carveNets = append(carveNets, mustParse(t, c))
	}

	got := SubtractCIDRs(super, carveNets)

	// 1. Every emitted prefix lies within the supernet and is disjoint from
	//    every carve-out.
	for _, p := range got {
		if !super.Contains(p.IP) {
			t.Fatalf("prefix %s outside supernet %s", p.String(), super.String())
		}
		for _, c := range carveNets {
			if overlaps(c, p) {
				t.Fatalf("prefix %s overlaps carve-out %s", p.String(), c.String())
			}
		}
	}

	// 2. Emitted prefixes are pairwise disjoint.
	for i := range got {
		for j := i + 1; j < len(got); j++ {
			if overlaps(got[i], got[j]) {
				t.Fatalf("prefixes overlap: %s and %s", got[i].String(), got[j].String())
			}
		}
	}

	// 3. Host accounting: emitted + carve-outs == supernet size.
	var emitted float64
	for _, p := range got {
		ones, bits := p.Mask.Size()
		hosts := 1 << uint(bits-ones)
		emitted += float64(hosts)
	}
	var carved float64
	for _, c := range carveNets {
		ones, bits := c.Mask.Size()
		hosts := 1 << uint(bits-ones)
		carved += float64(hosts)
	}
	superHosts := float64(1 << 24)
	if emitted+carved != superHosts {
		t.Fatalf("host accounting mismatch: emitted=%v carved=%v total=%v want=%v", emitted, carved, emitted+carved, superHosts)
	}
}

func TestSubtractCIDRs_NoCarve_ReturnsSupernet(t *testing.T) {
	super := mustParse(t, "10.0.0.0/8")
	got := SubtractCIDRs(super, nil)
	if len(got) != 1 || got[0].String() != super.String() {
		t.Fatalf("expected supernet returned unchanged, got %v", got)
	}
}

func TestCanonicalizeCIDRs_Subsumption(t *testing.T) {
	// 192.168.77.0/24 is subsumed by 192.168.0.0/16; 10.0.0.0/8 duplicate merges.
	got, err := CanonicalizeCIDRs([]string{
		"192.168.77.0/24",
		"192.168.0.0/16",
		"10.0.0.0/8",
		"10.0.0.0/8",
		"::1/128",
		"fc00::/7",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"10.0.0.0/8", "192.168.0.0/16", "::1/128", "fc00::/7"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical set mismatch:\ngot  %v\nwant %v", got, want)
	}
}

func TestParseSubnet_GatewayToNetwork(t *testing.T) {
	netStr, ipnet, err := ParseSubnet("10.50.0.1/24")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if netStr != "10.50.0.0/24" {
		t.Fatalf("expected network CIDR, got %q", netStr)
	}
	if !ipnet.Contains(net.ParseIP("10.50.0.1")) {
		t.Fatal("gateway should be within subnet")
	}
}
