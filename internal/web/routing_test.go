package web

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/marco-fabbri/portico/internal/peering"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
)

// fakePeerer records the operations the card asks for and answers with
// the peering as the real service would leave it.
type fakePeerer struct {
	mu       sync.Mutex
	calls    []string
	failMove error // a move that fails before the remote gateway changes
}

func (f *fakePeerer) rec(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakePeerer) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func up(p settings.SitePeering) settings.SitePeering {
	p.State, p.Error = settings.PeeringUp, ""
	if p.LocalGatewayExtID == "" {
		p.LocalGatewayExtID, p.RemoteGatewayExtID, p.SessionExtID = "gw-1", "gw-r", "s-1"
	}
	if p.KindOrStatic() == settings.PeerEdge && p.EdgeVMExtID == "" {
		p.EdgeVMExtID = "vm-e"
	}
	return p
}

func (f *fakePeerer) Build(_ context.Context, _ settings.Site, p settings.SitePeering, _ peering.EdgeBuilder) (settings.SitePeering, error) {
	f.rec("Build " + p.KindOrStatic())
	return up(p), nil
}
func (f *fakePeerer) Refresh(_ context.Context, p settings.SitePeering) (settings.SitePeering, error) {
	f.rec("Refresh")
	return p, nil
}
func (f *fakePeerer) ReplaceEdge(_ context.Context, _ settings.Site, p settings.SitePeering, _ peering.EdgeBuilder) (settings.SitePeering, error) {
	f.rec("ReplaceEdge " + p.EdgeAddr.String())
	p.EdgeVMExtID = "vm-e2"
	return up(p), nil
}
func (f *fakePeerer) MoveToRouter(_ context.Context, _ settings.Site, p settings.SitePeering) (settings.SitePeering, error) {
	f.rec("MoveToRouter " + p.RouterAddr.String())
	if f.failMove != nil {
		p.State, p.Error = settings.PeeringFailed, f.failMove.Error()
		return p, f.failMove
	}
	p.Kind, p.EdgeKept = settings.PeerRouter, p.EdgeVMExtID != ""
	return up(p), nil
}
func (f *fakePeerer) MoveToEdge(_ context.Context, _ settings.Site, p settings.SitePeering, _ peering.EdgeBuilder) (settings.SitePeering, error) {
	f.rec("MoveToEdge " + p.EdgeAddr.String())
	p.Kind, p.EdgeKept = settings.PeerEdge, false
	return up(p), nil
}
func (f *fakePeerer) BackToEdge(_ context.Context, _ settings.Site, p settings.SitePeering) (settings.SitePeering, error) {
	f.rec("BackToEdge")
	p.Kind, p.EdgeKept = settings.PeerEdge, false
	return up(p), nil
}
func (f *fakePeerer) DeleteEdge(_ context.Context, p settings.SitePeering) (settings.SitePeering, error) {
	f.rec("DeleteEdge")
	p.EdgeVMExtID, p.EdgeKept = "", false
	return p, nil
}
func (f *fakePeerer) TearDown(_ context.Context, _ settings.Site, p settings.SitePeering) (settings.SitePeering, error) {
	f.rec("TearDown")
	p.LocalGatewayExtID, p.RemoteGatewayExtID, p.SessionExtID, p.EdgeVMExtID, p.VLANSubnetExtID = "", "", "", "", ""
	p.State, p.Error = settings.PeeringNone, ""
	return p, nil
}

// withRouting is withNetworking with the fake peering service wired.
func withRouting(t *testing.T, r *netReader) (*env, *settings.Service, *fakePeerer) {
	t.Helper()
	fp := &fakePeerer{}
	e := newEnv(t, true, func(o *Options) {
		o.Peering = func(settings.Site, func(context.Context, settings.SitePeering) error, *slog.Logger) (Peerer, peering.EdgeBuilder, error) {
			return fp, nil, nil
		}
	})
	svc := withNetworking(t, e, r)
	e.login(t)
	return e, svc, fp
}

func routerPeering() settings.SitePeering {
	return settings.SitePeering{Kind: settings.PeerRouter, GatewayAddr: netip.MustParsePrefix("10.0.210.195/24"), GatewayGW: netip.MustParseAddr("10.0.210.1"), GatewayASN: 65010,
		RouterAddr: netip.MustParseAddr("10.0.210.1"), RouterASN: 65000, LocalGatewayExtID: "gw-1", RemoteGatewayExtID: "gw-r", SessionExtID: "s-1", State: settings.PeeringUp}
}

func edgePeering() settings.SitePeering {
	return settings.SitePeering{Kind: settings.PeerEdge, GatewayAddr: netip.MustParsePrefix("10.0.210.195/24"), GatewayGW: netip.MustParseAddr("10.0.210.1"), GatewayASN: 65010,
		EdgeAddr: netip.MustParseAddr("10.0.210.194"), EdgeASN: 65020, LocalGatewayExtID: "gw-1", RemoteGatewayExtID: "gw-r", SessionExtID: "s-1", EdgeVMExtID: "vm-e", State: settings.PeeringUp}
}

func storePeering(t *testing.T, e *env, n settings.SiteNetwork) {
	t.Helper()
	if _, err := e.web.setup.UpdateSite(context.Background(), "site-a", func(s *settings.Site) { s.Network = n }); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingSaveRefusesAddressInPool(t *testing.T) {
	e, svc, fp := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1")})
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}})
	rec := e.do("POST", "/setup/sites/site-a/routing", e.form("peer", "router", "gateway_addr", "10.0.210.35", "gateway_gw", "10.0.210.1", "gateway_asn", "65010", "router_addr", "10.0.210.1", "router_asn", "65000"), true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "pool") {
		t.Fatalf("save: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	if fp.has("Build") {
		t.Fatalf("built with a refused address: %v", fp.calls)
	}
	if n := storedNetwork(t, svc); n.Peering.Kind != "" {
		t.Fatalf("stored = %+v", n.Peering)
	}
}

func TestRoutingSaveRouterBuilds(t *testing.T) {
	e, svc, fp := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1")})
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}})
	rec := e.hx("POST", "/setup/sites/site-a/routing", e.form("peer", "router", "gateway_addr", "10.0.210.195", "gateway_gw", "10.0.210.1", "gateway_asn", "65010", "router_addr", "10.0.210.1", "router_asn", "65000"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Working on the peering") {
		t.Fatalf("save: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	if !fp.has("Build router") {
		t.Fatalf("calls = %v", fp.calls)
	}
	n := storedNetwork(t, svc)
	if n.Peering.Kind != settings.PeerRouter || n.Peering.State != settings.PeeringUp || n.Peering.GatewayAddr.String() != "10.0.210.195/24" || n.Peering.RouterASN != 65000 || n.Peering.SessionExtID != "s-1" {
		t.Fatalf("stored = %+v", n.Peering)
	}
	// Saving the same section again rebuilds nothing new but runs Build (idempotent).
	rec = e.hx("POST", "/setup/sites/site-a/routing", e.form("peer", "router", "gateway_addr", "10.0.210.195", "gateway_gw", "10.0.210.1", "gateway_asn", "65010", "router_addr", "10.0.210.1", "router_asn", "65000"))
	if rec.Code != 200 {
		t.Fatalf("second save: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	// A switch to the edge from the form is a move.
	rec = e.hx("POST", "/setup/sites/site-a/routing", e.form("peer", "edge", "gateway_addr", "10.0.210.195", "gateway_gw", "10.0.210.1", "gateway_asn", "65010", "edge_addr", "10.0.210.194", "edge_asn", "65020"))
	if rec.Code != 200 {
		t.Fatalf("move save: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	if !fp.has("MoveToEdge 10.0.210.194") {
		t.Fatalf("calls = %v", fp.calls)
	}
	if n := storedNetwork(t, svc); n.Peering.Kind != settings.PeerEdge || n.Peering.EdgeVMExtID == "" {
		t.Fatalf("stored = %+v", n.Peering)
	}
}

func TestRoutingReplaceEdgeNeedsConfirm(t *testing.T) {
	e, svc, fp := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1")})
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}, Peering: edgePeering()})
	rec := e.do("POST", "/setup/sites/site-a/routing/replace-edge", e.form(), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "three minutes") || !strings.Contains(rec.Body.String(), `name="confirmed"`) {
		t.Fatalf("confirm: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	if fp.has("ReplaceEdge") {
		t.Fatal("replaced without confirmation")
	}
	rec = e.do("POST", "/setup/sites/site-a/routing/replace-edge", e.form("confirmed", "on"), true)
	if rec.Code != 303 {
		t.Fatalf("confirmed: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	if !fp.has("ReplaceEdge 10.0.210.194") {
		t.Fatalf("calls = %v", fp.calls)
	}
	if n := storedNetwork(t, svc); n.Peering.EdgeVMExtID != "vm-e2" || n.Peering.State != settings.PeeringUp {
		t.Fatalf("stored = %+v", n.Peering)
	}
}

func TestRoutingActionsOnTheEdge(t *testing.T) {
	e, svc, fp := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1")})
	p := edgePeering()
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}, Peering: p})
	if rec := e.hx("POST", "/setup/sites/site-a/routing/route-added", e.form()); rec.Code != 200 {
		t.Fatalf("route-added: %d\n%s", rec.Code, rec.Body.String())
	}
	if n := storedNetwork(t, svc); !n.Peering.RouteAdded {
		t.Fatalf("stored = %+v", n.Peering)
	}
	// Move to the router needs its address and ASN stored first.
	rec := e.do("POST", "/setup/sites/site-a/routing/move-router", e.form(), true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "router") {
		t.Fatalf("move-router without a router: %d\n%s", rec.Code, rec.Body.String())
	}
	p.RouterAddr, p.RouterASN = netip.MustParseAddr("10.0.210.1"), 65000
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}, Peering: p})
	if rec := e.hx("POST", "/setup/sites/site-a/routing/move-router", e.form()); rec.Code != 200 {
		t.Fatalf("move-router: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	if !fp.has("MoveToRouter 10.0.210.1") {
		t.Fatalf("calls = %v", fp.calls)
	}
	n := storedNetwork(t, svc)
	if n.Peering.Kind != settings.PeerRouter || !n.Peering.EdgeKept {
		t.Fatalf("stored = %+v", n.Peering)
	}
	body := e.hx("GET", "/setup/sites/site-a/networking", nil).Body.String()
	if !strings.Contains(body, "back-to-edge") || !strings.Contains(body, "delete-edge") || !strings.Contains(body, "static route") {
		t.Fatalf("card after the move:\n%s", body)
	}
	if rec := e.hx("POST", "/setup/sites/site-a/routing/back-to-edge", e.form()); rec.Code != 200 {
		t.Fatalf("back-to-edge: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	if !fp.has("BackToEdge") {
		t.Fatalf("calls = %v", fp.calls)
	}
	// Delete needs a confirmation and the router as the peer.
	if rec := e.do("POST", "/setup/sites/site-a/routing/delete-edge", e.form("confirmed", "on"), true); rec.Code != 409 {
		t.Fatalf("delete-edge while the edge is the peer: %d\n%s", rec.Code, rec.Body.String())
	}
	if rec := e.hx("POST", "/setup/sites/site-a/routing/move-router", e.form()); rec.Code != 200 {
		t.Fatalf("move-router again: %d", rec.Code)
	}
	e.web.WaitPeering()
	rec = e.do("POST", "/setup/sites/site-a/routing/delete-edge", e.form(), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="confirmed"`) {
		t.Fatalf("delete-edge confirm: %d\n%s", rec.Code, rec.Body.String())
	}
	if rec := e.do("POST", "/setup/sites/site-a/routing/delete-edge", e.form("confirmed", "on"), true); rec.Code != 303 {
		t.Fatalf("delete-edge: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	if !fp.has("DeleteEdge") {
		t.Fatalf("calls = %v", fp.calls)
	}
	if n := storedNetwork(t, svc); n.Peering.EdgeVMExtID != "" || n.Peering.EdgeKept {
		t.Fatalf("stored = %+v", n.Peering)
	}
}

func TestRoutingLinesForRouter(t *testing.T) {
	e, _, _ := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1"), session: "DOWN"})
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}, Peering: routerPeering()})
	body := e.hx("GET", "/setup/sites/site-a/networking", nil).Body.String()
	for _, want := range []string{"On your router", "router bgp 65000", "neighbor 10.0.210.195 remote-as 65010", `value="router" checked`, "repair"} {
		if !strings.Contains(body, want) {
			t.Fatalf("card lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "On your core") {
		t.Fatalf("a router site shows the core route:\n%s", body)
	}
}

func TestRoutingStaticShowsRecommendation(t *testing.T) {
	e, _, _ := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1")})
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}})
	body := e.hx("GET", "/setup/sites/site-a/networking", nil).Body.String()
	for _, want := range []string{"Routing", "requirement", `value="static" checked`, `value="router"`, `value="edge"`, "edge router", "/setup/sites/site-a/routing"} {
		if !strings.Contains(body, want) {
			t.Fatalf("card lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "On your router") || strings.Contains(body, "replace-edge") {
		t.Fatalf("a static site shows peering actions:\n%s", body)
	}
}

func TestSwitchOffKeepsPeeringWhileNetworksExist(t *testing.T) {
	e, svc, fp := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1")})
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}, Enabled: true, Peering: routerPeering()})
	ctx := context.Background()
	if err := e.st.CreateTenant(ctx, &store.Tenant{ID: "acme", Status: store.StatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreatePlacement(ctx, &store.Placement{TenantID: "acme", SiteID: "site-a", Status: store.PlacementActive, NetState: store.NetReady, TransitPrefix: "198.51.100.0/28"}); err != nil {
		t.Fatal(err)
	}
	off := e.form("transit_vpc_ext_id", "vpc-transit", "dns", "1.1.1.1", "integrated", "on")
	if rec := e.hx("POST", "/setup/sites/site-a/networking", off); rec.Code != 200 {
		t.Fatalf("switch off: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	if fp.has("TearDown") {
		t.Fatalf("torn down with a ready network on the site: %v", fp.calls)
	}
	if n := storedNetwork(t, svc); n.Enabled || n.Peering.SessionExtID != "s-1" {
		t.Fatalf("stored = %+v", n)
	}
	// The switch going off with no ready network on the site tears the
	// peering down (a disconnect of the last one goes through the tenant
	// service's hook).
	if err := e.st.DeletePlacement(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}, Enabled: true, Peering: routerPeering()})
	if rec := e.hx("POST", "/setup/sites/site-a/networking", off); rec.Code != 200 {
		t.Fatalf("switch off again: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	if !fp.has("TearDown") {
		t.Fatalf("not torn down: %v", fp.calls)
	}
	if n := storedNetwork(t, svc); n.Peering.SessionExtID != "" || n.Peering.State != settings.PeeringNone || n.Peering.Kind != settings.PeerRouter {
		t.Fatalf("stored = %+v", n.Peering)
	}
}

// Router mode with the edge kept off: the core's static route to the
// edge is dead now, the card says to remove it (write-up 39: a static
// route without a metric beat the BGP route and blackholed the prefix).
func TestRoutingLinesForRouterWithAKeptEdge(t *testing.T) {
	e, _, _ := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1"), session: "UP"})
	p := routerPeering()
	p.EdgeAddr, p.EdgeASN, p.EdgeVMExtID, p.EdgeKept, p.State = netip.MustParseAddr("10.0.210.196"), 65020, "edge-1", true, settings.PeeringUp
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}, Peering: p})
	body := e.hx("GET", "/setup/sites/site-a/networking", nil).Body.String()
	for _, want := range []string{"On your core", "Remove the static route", "via 10.0.210.196", "the edge router is off", "delete-edge"} {
		if !strings.Contains(body, want) {
			t.Fatalf("card lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "once the edge router is deleted") {
		t.Fatalf("the card defers the removal of a dead route:\n%s", body)
	}
}

// The VLAN router field names what the address is used for: the
// router that forwards the routed prefix (write-up 39: the lab's VLAN
// gateway was not that router, and the edge sent the prefix's traffic
// to it).
func TestRoutingVLANRouterFieldSaysWhatItIs(t *testing.T) {
	e, _, _ := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1")})
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}})
	body := e.hx("GET", "/setup/sites/site-a/networking", nil).Body.String()
	if !strings.Contains(body, "VLAN router (the router that forwards the routed prefix") {
		t.Fatalf("the field does not say what the VLAN router is:\n%s", body)
	}
}

// A Save that moves the peer and fails before the remote gateway changes
// must leave the stored kind as it was: the edge is still the peer, and
// Delete the edge router must refuse (write-up 39: the deleted live peer).
func TestRoutingSaveMoveThatFailsKeepsTheKind(t *testing.T) {
	e, svc, fp := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1")})
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}, Peering: edgePeering()})
	fp.failMove = errors.New("external: the transit VPC: prism central: 504 upstream request timeout")
	rec := e.hx("POST", "/setup/sites/site-a/routing", e.form("peer", "router", "gateway_addr", "10.0.210.195", "gateway_gw", "10.0.210.1", "gateway_asn", "65010", "router_addr", "10.0.210.1", "router_asn", "65000", "edge_addr", "10.0.210.194", "edge_asn", "65020"))
	if rec.Code != 200 {
		t.Fatalf("save: %d\n%s", rec.Code, rec.Body.String())
	}
	e.web.WaitPeering()
	n := storedNetwork(t, svc)
	if n.Peering.Kind != settings.PeerEdge || n.Peering.State != settings.PeeringFailed {
		t.Fatalf("stored = %+v", n.Peering)
	}
	if rec := e.do("POST", "/setup/sites/site-a/routing/delete-edge", e.form("confirmed", "on"), true); rec.Code == 303 || fp.has("DeleteEdge") {
		t.Fatalf("delete-edge ran on the live peer: %d calls=%v", rec.Code, fp.calls)
	}
}

// The route tick waits for the operation in progress: the service stores
// its own copy of the peering as it goes and would write the tick over.
func TestRoutingRouteAddedWaitsForTheOperation(t *testing.T) {
	e, svc, _ := withRouting(t, &netReader{gateway: netip.MustParseAddr("10.0.210.1")})
	p := edgePeering()
	p.State = settings.PeeringBuilding
	storePeering(t, e, settings.SiteNetwork{TransitVPCExtID: "vpc-transit", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}, Peering: p})
	rec := e.hx("POST", "/setup/sites/site-a/routing/route-added", e.form())
	if !strings.Contains(rec.Body.String(), "An operation on the peering is running") {
		t.Fatalf("route-added while building: %d\n%s", rec.Code, rec.Body.String())
	}
	if n := storedNetwork(t, svc); n.Peering.RouteAdded {
		t.Fatalf("stored = %+v", n.Peering)
	}
}
