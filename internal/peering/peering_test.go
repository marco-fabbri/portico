package peering

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/settings"
)

func fast(t *testing.T) {
	t.Helper()
	oldPoll, oldGW, oldSess := Poll, GatewayBound, SessionBound
	Poll, GatewayBound, SessionBound = time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { Poll, GatewayBound, SessionBound = oldPoll, oldGW, oldSess })
}

type saves struct{ last []settings.SitePeering }

func (s *saves) save(_ context.Context, p settings.SitePeering) error {
	s.last = append(s.last, p)
	return nil
}

func newService(t *testing.T, f *fakePC) (*Service, *saves) {
	t.Helper()
	fast(t)
	sv := &saves{}
	return New(f, slog.New(slog.DiscardHandler), sv.save), sv
}

func TestBuildRouterCreatesThreeObjectsAndWaitsForUp(t *testing.T) {
	f := newFakePC()
	f.gatewayStates = []string{"DOWN", "DOWN", "UP"}
	s, sv := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), routerPeering(), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.State != settings.PeeringUp || p.Error != "" || p.LocalGatewayExtID == "" || p.RemoteGatewayExtID == "" || p.SessionExtID == "" || p.VLANSubnetExtID != mgmtSubnet {
		t.Fatalf("peering = %+v", p)
	}
	f.before(t, "CreateLocalBGPGateway portico-s1-bgp project="+systemProject+" cluster=cl-1 subnet="+mgmtSubnet+" addr=10.0.210.195/24 gw=10.0.210.254 asn=65010 vpc=vpc-1", "CreateRemoteBGPGateway portico-s1-peer project="+systemProject+" addr=10.0.210.254 asn=65000")
	f.before(t, "CreateRemoteBGPGateway", "CreateBGPSession portico-s1-peering project="+systemProject+" local="+p.LocalGatewayExtID+" remote="+p.RemoteGatewayExtID+" addr=10.0.210.195")
	if n := f.count("GetGateway " + p.LocalGatewayExtID); n < 3 {
		t.Fatalf("the gateway was read %d times before the session", n)
	}
	// The session came after the third read (UP).
	calls := f.calls
	third, sess := 0, f.index("CreateBGPSession")
	for i, c := range calls {
		if strings.HasPrefix(c, "GetGateway "+p.LocalGatewayExtID) {
			third++
			if third == 3 && i > sess {
				t.Fatalf("the session was created before the gateway was UP:\n  %s", strings.Join(calls, "\n  "))
			}
		}
	}
	f.never(t, "CreateVLANSubnet")
	if len(sv.last) == 0 || sv.last[len(sv.last)-1] != p {
		t.Fatalf("the last save is not the result: %+v", sv.last)
	}
	if sv.last[0].State != settings.PeeringBuilding {
		t.Fatalf("the first save is not building: %+v", sv.last[0])
	}
}

func TestBuildCreatesTheVLANSubnetWhenNoneFits(t *testing.T) {
	f := newFakePC()
	delete(f.subnets, mgmtSubnet)
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), routerPeering(), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	f.before(t, "CreateVLANSubnet portico-s1-vlan cluster=cl-1 vlan=210 vswitch=vs-1", "CreateLocalBGPGateway")
	if p.VLANSubnetExtID == "" || !strings.Contains(f.calls[f.index("CreateLocalBGPGateway")], "subnet="+p.VLANSubnetExtID) {
		t.Fatalf("peering = %+v", p)
	}
}

func TestBuildStopsAtGatewayNotUpWithinBound(t *testing.T) {
	f := newFakePC()
	f.gatewayStates = []string{"DOWN"}
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), routerPeering(), nil)
	if err == nil || p.State != settings.PeeringFailed || !strings.HasPrefix(p.Error, "gateway-local: ") || !strings.Contains(p.Error, "Service nginx is down") {
		t.Fatalf("Build = %+v, %v", p, err)
	}
	if p.LocalGatewayExtID == "" {
		t.Fatal("the gateway's extId was dropped on failure")
	}
	f.never(t, "CreateBGPSession")
	f.never(t, "CreateRemoteBGPGateway")
}

func TestBuildAdoptsExistingByNameInSystemProject(t *testing.T) {
	f := newFakePC()
	f.gateways["gw-old"] = nutanix.Gateway{ExtID: "gw-old", Name: "portico-s1-bgp", ProjectExtID: systemProject, Local: true, Address: netip.MustParseAddr("10.0.210.195"), ASN: 65010}
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), routerPeering(), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.LocalGatewayExtID != "gw-old" {
		t.Fatalf("LocalGatewayExtID = %q", p.LocalGatewayExtID)
	}
	f.never(t, "CreateLocalBGPGateway")

	f2 := newFakePC()
	f2.gateways["gw-t"] = nutanix.Gateway{ExtID: "gw-t", Name: "portico-s1-bgp", ProjectExtID: "p-tenant", Local: true}
	s2, _ := newService(t, f2)
	p, err = s2.Build(context.Background(), testSite(), routerPeering(), nil)
	if err == nil || !strings.Contains(err.Error(), "belongs to another project") || p.State != settings.PeeringFailed {
		t.Fatalf("Build with a tenant's gateway = %+v, %v", p, err)
	}
	f2.never(t, "CreateLocalBGPGateway")
}

func TestBuildEdgeBuildsTheEdgeAndMovesTheSubnetGateway(t *testing.T) {
	f := newFakePC()
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), edgePeering(), &fakeEdge{f: f})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.State != settings.PeeringUp || p.EdgeVMExtID == "" || p.EdgeKept {
		t.Fatalf("peering = %+v", p)
	}
	f.before(t, "EdgeBuild S1 addr=10.0.210.194", "CreateRemoteBGPGateway portico-s1-peer project="+systemProject+" addr=10.0.210.194 asn=65020")
	f.before(t, "CreateBGPSession", "UpdateSubnetGateway ext-1 gw=10.0.210.194")
	f.before(t, "GetBGPSession", "UpdateSubnetGateway")
}

func TestBuildEdgeLeavesTheSubnetGatewayWhenTheSessionStaysDown(t *testing.T) {
	f := newFakePC()
	f.sessionStates, f.sessionMsg = []string{"DOWN"}, "Current BGP state: Active"
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), edgePeering(), &fakeEdge{f: f})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.State != settings.PeeringDown || !strings.Contains(p.Error, "Current BGP state: Active") {
		t.Fatalf("peering = %+v", p)
	}
	f.never(t, "UpdateSubnetGateway")
}

func TestBuildReusesTheEdgeVMItKnows(t *testing.T) {
	f := newFakePC()
	f.vms["edge-E"] = nutanix.VM{ExtID: "edge-E", Name: "portico-s1-edge", PowerOn: false}
	in := edgePeering()
	in.EdgeVMExtID = "edge-E"
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), in, &fakeEdge{f: f})
	if err != nil || p.EdgeVMExtID != "edge-E" {
		t.Fatalf("Build = %+v, %v", p, err)
	}
	f.never(t, "EdgeBuild")
	if f.index("PowerVM edge-E on=true") < 0 {
		t.Fatal("the edge was not powered on")
	}
}

func TestMoveToRouterUpdatesRemoteWhenAccepted(t *testing.T) {
	f := newFakePC()
	p := built(f)
	p.RouterAddr, p.RouterASN = netip.MustParseAddr("10.0.210.254"), 65000
	s, _ := newService(t, f)
	out, err := s.MoveToRouter(context.Background(), testSite(), p)
	if err != nil {
		t.Fatalf("MoveToRouter: %v", err)
	}
	if out.Kind != settings.PeerRouter || out.State != settings.PeeringUp || out.RouteAdded || out.RemoteGatewayExtID != "rgw-R" || out.SessionExtID != "s-S" {
		t.Fatalf("peering = %+v", out)
	}
	if f.index("UpdateRemoteGateway rgw-R addr=10.0.210.254 asn=65000") < 0 {
		t.Fatalf("no update of the remote gateway:\n  %s", strings.Join(f.calls, "\n  "))
	}
	f.never(t, "CreateRemoteBGPGateway")
	f.never(t, "DeleteBGPSession")
	f.before(t, "GetBGPSession", "UpdateSubnetGateway ext-1 gw=10.0.210.254")
}

func TestMoveToRouterRecreatesSessionOnlyAfterNewRemoteExists(t *testing.T) {
	f := newFakePC()
	f.refusePut = true
	p := built(f)
	p.RouterAddr, p.RouterASN = netip.MustParseAddr("10.0.210.254"), 65000
	s, _ := newService(t, f)
	out, err := s.MoveToRouter(context.Background(), testSite(), p)
	if err != nil {
		t.Fatalf("MoveToRouter: %v", err)
	}
	if out.RemoteGatewayExtID == "rgw-R" || out.SessionExtID == "s-S" || out.State != settings.PeeringUp {
		t.Fatalf("peering = %+v", out)
	}
	f.before(t, "CreateRemoteBGPGateway", "DeleteBGPSession s-S")
	f.before(t, "DeleteBGPSession s-S", "CreateBGPSession")
	f.before(t, "CreateBGPSession", "DeleteGateway rgw-R")
	if _, ok := f.gateways["rgw-R"]; ok {
		t.Fatal("the old remote gateway was kept")
	}
}

func TestMoveToRouterKeepsEdgeOff(t *testing.T) {
	f := newFakePC()
	p := built(f)
	p.RouterAddr, p.RouterASN = netip.MustParseAddr("10.0.210.254"), 65000
	s, _ := newService(t, f)
	out, err := s.MoveToRouter(context.Background(), testSite(), p)
	if err != nil {
		t.Fatalf("MoveToRouter: %v", err)
	}
	if !out.EdgeKept || out.EdgeVMExtID != "edge-E" || f.vms["edge-E"].PowerOn {
		t.Fatalf("peering = %+v, vm on = %v", out, f.vms["edge-E"].PowerOn)
	}
	f.never(t, "DeleteVM")
	f.before(t, "GetBGPSession", "PowerVM edge-E on=false")
}

func TestMoveToRouterWithTheSessionDownKeepsTheEdgeRunning(t *testing.T) {
	f := newFakePC()
	f.sessionStates, f.sessionMsg = []string{"DOWN"}, "BGP not configured on Peer"
	p := built(f)
	p.RouterAddr, p.RouterASN = netip.MustParseAddr("10.0.210.254"), 65000
	s, _ := newService(t, f)
	out, err := s.MoveToRouter(context.Background(), testSite(), p)
	if err != nil {
		t.Fatalf("MoveToRouter: %v", err)
	}
	if out.Kind != settings.PeerRouter || out.State != settings.PeeringDown || !strings.Contains(out.Error, "BGP not configured on Peer") || !out.EdgeKept {
		t.Fatalf("peering = %+v", out)
	}
	f.never(t, "PowerVM")
	f.never(t, "UpdateSubnetGateway")
}

func TestReplaceEdgeIsBreakBeforeMake(t *testing.T) {
	f := newFakePC()
	p := built(f)
	s, _ := newService(t, f)
	out, err := s.ReplaceEdge(context.Background(), testSite(), p, &fakeEdge{f: f})
	if err != nil {
		t.Fatalf("ReplaceEdge: %v", err)
	}
	if out.EdgeVMExtID == "edge-E" || out.EdgeVMExtID == "" || out.State != settings.PeeringUp {
		t.Fatalf("peering = %+v", out)
	}
	f.before(t, "PowerVM edge-E on=false", "DeleteVM edge-E")
	f.before(t, "DeleteVM edge-E", "EdgeBuild")
	f.before(t, "EdgeBuild", "GetBGPSession")
	f.never(t, "UpdateRemoteGateway")
}

func TestReplaceEdgeWithANewAddressMovesTheRemoteAndTheSubnet(t *testing.T) {
	f := newFakePC()
	p := built(f)
	p.RouteAdded = true
	p.EdgeAddr = netip.MustParseAddr("10.0.210.193")
	s, _ := newService(t, f)
	out, err := s.ReplaceEdge(context.Background(), testSite(), p, &fakeEdge{f: f})
	if err != nil {
		t.Fatalf("ReplaceEdge: %v", err)
	}
	if out.RouteAdded {
		t.Fatal("the route tick survived an address change")
	}
	f.before(t, "EdgeBuild S1 addr=10.0.210.193", "UpdateRemoteGateway rgw-R addr=10.0.210.193 asn=65020")
	f.before(t, "GetBGPSession", "UpdateSubnetGateway ext-1 gw=10.0.210.193")
}

func TestMoveToEdgeBuildsThenSwitches(t *testing.T) {
	f := newFakePC()
	p := built(f)
	// A router site: the remote is the router, no edge.
	p.Kind, p.RouterAddr, p.RouterASN, p.EdgeVMExtID = settings.PeerRouter, netip.MustParseAddr("10.0.210.254"), 65000, ""
	delete(f.vms, "edge-E")
	routerSubnet(f)
	s, _ := newService(t, f)
	out, err := s.MoveToEdge(context.Background(), testSite(), p, &fakeEdge{f: f})
	if err != nil {
		t.Fatalf("MoveToEdge: %v", err)
	}
	if out.Kind != settings.PeerEdge || out.EdgeVMExtID == "" || out.State != settings.PeeringUp || out.EdgeKept || out.RouteAdded {
		t.Fatalf("peering = %+v", out)
	}
	f.before(t, "EdgeBuild", "UpdateRemoteGateway rgw-R addr=10.0.210.194 asn=65020")
	f.before(t, "GetBGPSession", "UpdateSubnetGateway ext-1 gw=10.0.210.194")
}

func TestBackToEdgePowersOnAndMovesBack(t *testing.T) {
	f := newFakePC()
	p := built(f)
	p.Kind, p.RouterAddr, p.RouterASN, p.EdgeKept = settings.PeerRouter, netip.MustParseAddr("10.0.210.254"), 65000, true
	f.vms["edge-E"] = nutanix.VM{ExtID: "edge-E", Name: "portico-s1-edge", PowerOn: false}
	routerSubnet(f)
	s, _ := newService(t, f)
	out, err := s.BackToEdge(context.Background(), testSite(), p)
	if err != nil {
		t.Fatalf("BackToEdge: %v", err)
	}
	if out.Kind != settings.PeerEdge || out.EdgeKept || out.State != settings.PeeringUp {
		t.Fatalf("peering = %+v", out)
	}
	f.before(t, "PowerVM edge-E on=true", "UpdateRemoteGateway rgw-R addr=10.0.210.194 asn=65020")
	f.before(t, "GetBGPSession", "UpdateSubnetGateway ext-1 gw=10.0.210.194")
}

func TestDeleteEdgePowersOffFirst(t *testing.T) {
	f := newFakePC()
	p := built(f)
	p.Kind, p.EdgeKept = settings.PeerRouter, true
	s, _ := newService(t, f)
	out, err := s.DeleteEdge(context.Background(), testSite(), p)
	if err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	if out.EdgeVMExtID != "" || out.EdgeKept {
		t.Fatalf("peering = %+v", out)
	}
	f.before(t, "PowerVM edge-E on=false", "DeleteVM edge-E")
}

func TestTearDownOrder(t *testing.T) {
	f := newFakePC()
	p := built(f)
	p.VLANSubnetExtID = "vl-9"
	f.subnets["vl-9"] = nutanix.Subnet{ExtID: "vl-9", Name: "portico-s1-vlan", Type: "VLAN", ClusterExtID: "cl-1", VLANID: 210}
	s, _ := newService(t, f)
	out, err := s.TearDown(context.Background(), testSite(), p)
	if err != nil {
		t.Fatalf("TearDown: %v", err)
	}
	if out.LocalGatewayExtID != "" || out.RemoteGatewayExtID != "" || out.SessionExtID != "" || out.EdgeVMExtID != "" || out.VLANSubnetExtID != "" || out.State != settings.PeeringNone || out.Kind != settings.PeerEdge {
		t.Fatalf("peering = %+v", out)
	}
	f.before(t, "DeleteBGPSession s-S", "DeleteGateway gw-L")
	f.before(t, "DeleteGateway gw-L", "DeleteGateway rgw-R")
	// The external subnet is read before anything is deleted and its
	// gateway moved back before the edge, its current gateway, goes.
	f.before(t, "GetVPC", "DeleteBGPSession s-S")
	f.before(t, "DeleteGateway rgw-R", "UpdateSubnetGateway ext-1 gw=10.0.210.254")
	f.before(t, "UpdateSubnetGateway ext-1 gw=10.0.210.254", "PowerVM edge-E on=false")
	f.before(t, "PowerVM edge-E on=false", "DeleteVM edge-E")
	f.before(t, "DeleteVM edge-E", "DeleteSubnet vl-9")
}

// An edge whose power-on fails after the create is still the site's
// edge: its extId is stored, so Repair turns it on instead of building
// a second VM (deferred minor of write-up 39).
func TestBuildStoresTheEdgeWhosePowerOnFailed(t *testing.T) {
	f := newFakePC()
	s, svc := newService(t, f)
	p := edgeInput()
	out, err := s.Build(context.Background(), testSite(), p, &fakeEdge{f: f, failPower: &nutanix.APIError{Status: 503, Message: "busy"}})
	if err == nil || out.State != settings.PeeringFailed || out.EdgeVMExtID == "" {
		t.Fatalf("Build = %+v, %v", out, err)
	}
	if last := svc.last[len(svc.last)-1]; last.EdgeVMExtID != out.EdgeVMExtID {
		t.Fatalf("stored = %+v", last)
	}
	f.calls = nil
	out, err = s.Build(context.Background(), testSite(), out, &fakeEdge{f: f})
	if err != nil || f.index("EdgeBuild") >= 0 || f.index("PowerVM "+out.EdgeVMExtID+" on=true") < 0 {
		t.Fatalf("Repair: %v; calls:\n  %s", err, strings.Join(f.calls, "\n  "))
	}
}

func TestTearDownKeepsAVLANSubnetItDidNotCreate(t *testing.T) {
	f := newFakePC()
	p := built(f)
	s, _ := newService(t, f)
	if _, err := s.TearDown(context.Background(), testSite(), p); err != nil {
		t.Fatalf("TearDown: %v", err)
	}
	f.never(t, "DeleteSubnet")
	if _, ok := f.subnets[mgmtSubnet]; !ok {
		t.Fatal("MGMT was deleted")
	}
}

func TestRefreshReadsDownMessage(t *testing.T) {
	f := newFakePC()
	p := built(f)
	f.sessionStates, f.sessionMsg = []string{"DOWN"}, "Password/ASN Mismatch or BGP not configured on Peer. Current BGP state: Active"
	s, _ := newService(t, f)
	out, err := s.Refresh(context.Background(), p)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if out.State != settings.PeeringDown || !strings.Contains(out.Error, "Current BGP state: Active") || out.StateAt.IsZero() {
		t.Fatalf("peering = %+v", out)
	}
	f.gatewayStates = []string{"DOWN"}
	out, err = s.Refresh(context.Background(), p)
	if err != nil || out.State != settings.PeeringDown || !strings.Contains(out.Error, "gateway") || !strings.Contains(out.Error, "Service nginx is down") {
		t.Fatalf("Refresh with the gateway down = %+v, %v", out, err)
	}
	f.gatewayStates, f.sessionStates = []string{"UP"}, []string{"UP"}
	out, _ = s.Refresh(context.Background(), p)
	if out.State != settings.PeeringUp || out.Error != "" {
		t.Fatalf("Refresh up = %+v", out)
	}
	none, _ := s.Refresh(context.Background(), settings.SitePeering{Kind: settings.PeerRouter})
	if none.State != settings.PeeringNone {
		t.Fatalf("Refresh of nothing built = %+v", none)
	}
}

// routerSubnet puts the external subnet's gateway on the VLAN's router,
// as a router site has it.
func routerSubnet(f *fakePC) {
	s := f.subnets[extSubnet]
	s.Gateway = netip.MustParseAddr("10.0.210.254")
	f.subnets[extSubnet] = s
}

func TestBuildRouterPutsTheSubnetGatewayOnTheVLANRouter(t *testing.T) {
	f := newFakePC()
	// The external subnet still points at a previous edge router.
	ext := f.subnets[extSubnet]
	ext.Gateway = netip.MustParseAddr("10.0.210.194")
	f.subnets[extSubnet] = ext
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), routerPeering(), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.State != settings.PeeringUp {
		t.Fatalf("peering = %+v", p)
	}
	f.before(t, "GetBGPSession", "UpdateSubnetGateway ext-1 gw=10.0.210.254")
	if f.subnets[extSubnet].Gateway != netip.MustParseAddr("10.0.210.254") {
		t.Fatalf("the external subnet's gateway is %s", f.subnets[extSubnet].Gateway)
	}
}

func TestBuildRouterLeavesTheSubnetGatewayWhenTheSessionStaysDown(t *testing.T) {
	f := newFakePC()
	ext := f.subnets[extSubnet]
	ext.Gateway = netip.MustParseAddr("10.0.210.194")
	f.subnets[extSubnet] = ext
	f.sessionStates, f.sessionMsg = []string{"DOWN"}, "Current BGP state: Active"
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), routerPeering(), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.State != settings.PeeringDown {
		t.Fatalf("peering = %+v", p)
	}
	f.never(t, "UpdateSubnetGateway")
}

func TestMoveToRouterThatFailsBeforeTheRemoteMovesKeepsTheEdgeAsPeer(t *testing.T) {
	f := newFakePC()
	p := built(f)
	p.RouterAddr, p.RouterASN = netip.MustParseAddr("10.0.210.254"), 65000
	delete(f.subnets, extSubnet) // the external subnet cannot be read: the move fails at its first step
	s, _ := newService(t, f)
	out, err := s.MoveToRouter(context.Background(), testSite(), p)
	if err == nil {
		t.Fatalf("MoveToRouter did not fail")
	}
	if out.Kind != settings.PeerEdge || out.State != settings.PeeringFailed || out.EdgeKept {
		t.Fatalf("peering = %+v", out)
	}
	f.never(t, "UpdateRemoteGateway")
	if _, err := s.DeleteEdge(context.Background(), testSite(), out); err == nil {
		t.Fatalf("DeleteEdge deleted the edge that is still the peer")
	}
}

func TestMoveToEdgeThatFailsToBuildTheEdgeKeepsTheRouterAsPeer(t *testing.T) {
	f := newFakePC()
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), routerPeering(), nil)
	if err != nil || p.State != settings.PeeringUp {
		t.Fatalf("Build: %v %+v", err, p)
	}
	p.EdgeAddr, p.EdgeASN = netip.MustParseAddr("10.0.210.194"), 65020
	out, err := s.MoveToEdge(context.Background(), testSite(), p, &fakeEdge{f: f, fail: errors.New("no host has enough resources")})
	if err == nil {
		t.Fatalf("MoveToEdge did not fail")
	}
	if out.Kind != settings.PeerRouter || out.State != settings.PeeringFailed {
		t.Fatalf("peering = %+v", out)
	}
	f.never(t, "UpdateRemoteGateway")
}

// The web handler marks the peering building before DeleteEdge runs;
// DeleteEdge must end with the session's state, not that mark (write-up
// 39: a card stuck on "working" with nothing running).
func TestDeleteEdgeEndsWithTheSessionState(t *testing.T) {
	f := newFakePC()
	p := built(f)
	p.Kind, p.EdgeKept, p.State = settings.PeerRouter, true, settings.PeeringBuilding
	s, _ := newService(t, f)
	out, err := s.DeleteEdge(context.Background(), testSite(), p)
	if err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	if out.State != settings.PeeringUp || out.EdgeVMExtID != "" || out.EdgeKept {
		t.Fatalf("peering = %+v", out)
	}
}

// An unmanaged subnet with the same VLAN ID but on another virtual switch
// is another L2 network (spec section 3): not adopted.
func TestBuildCreatesTheVLANSubnetWhenTheVirtualSwitchDiffers(t *testing.T) {
	f := newFakePC()
	m := f.subnets[mgmtSubnet]
	m.VirtualSwitchExtID = "vs-2"
	f.subnets[mgmtSubnet] = m
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), routerPeering(), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.VLANSubnetExtID == mgmtSubnet || f.index("CreateVLANSubnet portico-s1-vlan") < 0 {
		t.Fatalf("peering = %+v\n  %s", p, strings.Join(f.calls, "\n  "))
	}
}

// One transient answer while the gateway or the session is waited for
// is not the outcome (Review Focus 2; write-up 39: 504 waves): the wait
// goes on and the build ends up.
func TestBuildWaitsThroughATransientAnswer(t *testing.T) {
	f := newFakePC()
	f.gatewayErrs = []error{&nutanix.APIError{Status: 504, Message: "upstream request timeout"}, &nutanix.APIError{Status: 500, Message: "Failed to store new cookie in IDF"}}
	f.gatewayStates = []string{"DOWN", "UP"}
	f.sessionErrs = []error{&nutanix.APIError{Status: 504, Message: "upstream request timeout"}}
	s, _ := newService(t, f)
	p, err := s.Build(context.Background(), testSite(), edgePeering(), &fakeEdge{f: f})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.State != settings.PeeringUp {
		t.Fatalf("peering = %+v", p)
	}
}

// A remote gateway update that fails with a transient answer is not a
// refusal: nothing is recreated and no session is deleted (Review Focus
// 3); the step fails and Repair runs it again.
func TestMoveToRouterDoesNotRecreateOnATransientUpdateError(t *testing.T) {
	f := newFakePC()
	p := built(f)
	p.RouterAddr, p.RouterASN = netip.MustParseAddr("10.0.210.254"), 65000
	f.updateRemoteErr = &nutanix.APIError{Status: 504, Message: "upstream request timeout"}
	s, _ := newService(t, f)
	out, err := s.MoveToRouter(context.Background(), testSite(), p)
	if err == nil || !strings.Contains(err.Error(), "504") {
		t.Fatalf("MoveToRouter: %v", err)
	}
	if out.Kind != settings.PeerEdge || out.SessionExtID != "s-S" || out.RemoteGatewayExtID != "rgw-R" {
		t.Fatalf("peering = %+v", out)
	}
	f.never(t, "CreateRemoteBGPGateway")
	f.never(t, "DeleteBGPSession")
}

// Delete the edge router puts the external subnet's gateway back on the
// VLAN router before the VM goes: a move to the router that ended with
// the session down left the gateway on the edge.
func TestDeleteEdgePutsTheSubnetGatewayBackFirst(t *testing.T) {
	f := newFakePC()
	p := built(f) // the subnet's gateway is the edge's address
	p.Kind, p.EdgeKept = settings.PeerRouter, true
	s, _ := newService(t, f)
	out, err := s.DeleteEdge(context.Background(), testSite(), p)
	if err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	if out.EdgeVMExtID != "" || f.subnets[extSubnet].Gateway != p.GatewayGW {
		t.Fatalf("peering = %+v, subnet gateway %s", out, f.subnets[extSubnet].Gateway)
	}
	f.before(t, "UpdateSubnetGateway ext-1 gw=10.0.210.254", "DeleteVM edge-E")
}
