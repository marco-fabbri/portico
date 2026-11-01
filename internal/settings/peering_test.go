package settings

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestPeeringKindDefaultsToStatic(t *testing.T) {
	// A 0.2.2 document: the network section has no peering at all.
	var n SiteNetwork
	if err := json.Unmarshal([]byte(`{"transit_vpc_ext_id":"vpc-1","enabled":true}`), &n); err != nil {
		t.Fatal(err)
	}
	if n.Peering.KindOrStatic() != PeerStatic || n.Peering.Kind != "" {
		t.Fatalf("peering = %+v", n.Peering)
	}
	if ok, why := n.SwitchAllowed(); !ok || why != "" {
		t.Fatalf("SwitchAllowed = %v, %q", ok, why)
	}
}

func TestPeeringValidateRefusesPoolAndEqualASN(t *testing.T) {
	external := netip.MustParsePrefix("10.23.210.0/24")
	pool := [2]netip.Addr{netip.MustParseAddr("10.23.210.200"), netip.MustParseAddr("10.23.210.220")}
	good := SitePeering{Kind: PeerRouter, GatewayAddr: netip.MustParsePrefix("10.23.210.195/24"), GatewayGW: netip.MustParseAddr("10.23.210.254"), GatewayASN: 65010,
		RouterAddr: netip.MustParseAddr("10.23.210.254"), RouterASN: 65000}
	if err := good.Validate(external, pool); err != nil {
		t.Fatalf("a good router peering refused: %v", err)
	}
	cases := []struct {
		name string
		mut  func(p *SitePeering)
		want string
	}{
		{"gateway in the pool", func(p *SitePeering) { p.GatewayAddr = netip.MustParsePrefix("10.23.210.201/24") }, "pool"},
		{"gateway off the VLAN", func(p *SitePeering) { p.GatewayAddr = netip.MustParsePrefix("10.23.211.5/24") }, "10.23.210.0/24"},
		{"gateway prefix length", func(p *SitePeering) { p.GatewayAddr = netip.MustParsePrefix("10.23.210.195/25") }, "prefix length"},
		{"router off the VLAN", func(p *SitePeering) { p.RouterAddr = netip.MustParseAddr("10.23.211.1") }, "10.23.210.0/24"},
		{"same address", func(p *SitePeering) { p.RouterAddr = netip.MustParseAddr("10.23.210.195") }, "distinct"},
		{"equal ASN", func(p *SitePeering) { p.RouterASN = 65010 }, "distinct"},
		{"gateway ASN public", func(p *SitePeering) { p.GatewayASN = 64000 }, "private"},
		{"gateway ASN missing", func(p *SitePeering) { p.GatewayASN = 0 }, "ASN"},
		{"router ASN missing", func(p *SitePeering) { p.RouterASN = 0 }, "ASN"},
		{"no gateway", func(p *SitePeering) { p.GatewayAddr = netip.Prefix{} }, "address"},
		{"unknown kind", func(p *SitePeering) { p.Kind = "ospf" }, "kind"},
	}
	for _, c := range cases {
		p := good
		c.mut(&p)
		err := p.Validate(external, pool)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
	// The edge needs its own address and ASN, also private and distinct.
	edge := SitePeering{Kind: PeerEdge, GatewayAddr: good.GatewayAddr, GatewayGW: good.GatewayGW, GatewayASN: 65010,
		EdgeAddr: netip.MustParseAddr("10.23.210.194"), EdgeASN: 65020}
	if err := edge.Validate(external, pool); err != nil {
		t.Fatalf("a good edge peering refused: %v", err)
	}
	edge.EdgeASN = 4294967295
	if err := edge.Validate(external, pool); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("edge ASN 4294967295: %v", err)
	}
	edge.EdgeASN = 4200000000
	edge.EdgeAddr = netip.MustParseAddr("10.23.210.210")
	if err := edge.Validate(external, pool); err == nil || !strings.Contains(err.Error(), "pool") {
		t.Fatalf("edge in the pool: %v", err)
	}
	// Static imposes nothing.
	if err := (SitePeering{}).Validate(external, pool); err != nil {
		t.Fatalf("static refused: %v", err)
	}
	if err := (SitePeering{Kind: PeerStatic, GatewayASN: 1}).Validate(external, pool); err != nil {
		t.Fatalf("static with leftovers refused: %v", err)
	}
}

func TestSwitchWaitsForSessionUp(t *testing.T) {
	for _, kind := range []string{PeerRouter, PeerEdge} {
		for _, st := range []string{PeeringNone, PeeringBuilding, PeeringDown, PeeringFailed, ""} {
			n := SiteNetwork{Enabled: true, Peering: SitePeering{Kind: kind, State: st}}
			if ok, why := n.SwitchAllowed(); ok || why != "the BGP session is not UP yet" {
				t.Fatalf("%s/%s: SwitchAllowed = %v, %q", kind, st, ok, why)
			}
		}
		n := SiteNetwork{Peering: SitePeering{Kind: kind, State: PeeringUp}}
		if ok, why := n.SwitchAllowed(); !ok || why != "" {
			t.Fatalf("%s/up: SwitchAllowed = %v, %q", kind, ok, why)
		}
	}
}

func TestSettingsRoundTripKeepsPeering(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	in := sample()
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	in.Sites[0].Network = SiteNetwork{TransitVPCExtID: "vpc-1", Enabled: true, Peering: SitePeering{
		Kind: PeerEdge, GatewayAddr: netip.MustParsePrefix("10.23.210.195/24"), GatewayGW: netip.MustParseAddr("10.23.210.254"), GatewayASN: 65010,
		EdgeAddr: netip.MustParseAddr("10.23.210.194"), EdgeASN: 65020, EdgeKept: true, RouteAdded: true,
		LocalGatewayExtID: "gw-1", RemoteGatewayExtID: "gw-r", SessionExtID: "s-1", EdgeVMExtID: "vm-1", EdgeImageExtID: "img-1", VLANSubnetExtID: "sub-1",
		State: PeeringUp, Error: "", StateAt: at}}
	if err := svc.Set(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := got.Sites[0].Network.Peering
	if p != in.Sites[0].Network.Peering {
		t.Fatalf("peering after round trip = %+v", p)
	}
	// Zero addresses survive the round trip as zero (a static site).
	in.Sites[0].Network.Peering = SitePeering{}
	if err := svc.Set(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Get(ctx)
	if p := got.Sites[0].Network.Peering; p.GatewayAddr.IsValid() || p.EdgeAddr.IsValid() || p.KindOrStatic() != PeerStatic {
		t.Fatalf("static peering after round trip = %+v", p)
	}
}

// Every peer field that is filled in is checked, whatever the kind: the
// edge address saved beside a router peer is what a later move builds.
func TestPeeringValidateChecksTheOtherPeerToo(t *testing.T) {
	ext, pool := netip.MustParsePrefix("10.0.210.0/24"), [2]netip.Addr{netip.MustParseAddr("10.0.210.30"), netip.MustParseAddr("10.0.210.40")}
	p := SitePeering{Kind: PeerRouter, GatewayAddr: netip.MustParsePrefix("10.0.210.195/24"), GatewayGW: netip.MustParseAddr("10.0.210.1"), GatewayASN: 65010,
		RouterAddr: netip.MustParseAddr("10.0.210.1"), RouterASN: 65000, EdgeAddr: netip.MustParseAddr("10.0.210.35"), EdgeASN: 65020}
	if err := p.Validate(ext, pool); err == nil || !strings.Contains(err.Error(), "edge router 10.0.210.35 is in the external subnet's pool") {
		t.Fatalf("err = %v", err)
	}
	p.EdgeAddr, p.EdgeASN = netip.MustParseAddr("10.0.210.196"), 0
	if err := p.Validate(ext, pool); err == nil || !strings.Contains(err.Error(), "the edge router needs an ASN") {
		t.Fatalf("err = %v", err)
	}
}

// Neither the gateway nor the edge may take the VLAN router's address:
// that is the MSP's own router on the VLAN.
func TestPeeringValidateRefusesTheVLANRoutersAddress(t *testing.T) {
	ext, pool := netip.MustParsePrefix("10.0.210.0/24"), [2]netip.Addr{netip.MustParseAddr("10.0.210.30"), netip.MustParseAddr("10.0.210.40")}
	p := SitePeering{Kind: PeerEdge, GatewayAddr: netip.MustParsePrefix("10.0.210.195/24"), GatewayGW: netip.MustParseAddr("10.0.210.1"), GatewayASN: 65010,
		EdgeAddr: netip.MustParseAddr("10.0.210.1"), EdgeASN: 65020}
	if err := p.Validate(ext, pool); err == nil || !strings.Contains(err.Error(), "the edge router 10.0.210.1 is the VLAN router's address") {
		t.Fatalf("err = %v", err)
	}
	p.EdgeAddr, p.GatewayAddr = netip.MustParseAddr("10.0.210.196"), netip.MustParsePrefix("10.0.210.1/24")
	if err := p.Validate(ext, pool); err == nil || !strings.Contains(err.Error(), "the BGP gateway 10.0.210.1 is the VLAN router's address") {
		t.Fatalf("err = %v", err)
	}
}
