package netready

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/settings"
)

type fakeReader struct {
	controllers []nutanix.Controller
	gw          netip.Addr
	pcNet       netip.Prefix
	clusters    []nutanix.Cluster
	vpcs        []nutanix.VPC
	subnets     map[string]*nutanix.Subnet
	routes      map[string][]nutanix.Route
	missing     []string
	roles       []string
	gateways    map[string]*nutanix.Gateway
	sessions    map[string]*nutanix.BGPSession
	bgpRoutes   []nutanix.BGPRoute
	vms         map[string]*nutanix.VM
}

func (f *fakeReader) GetGateway(_ context.Context, id string) (*nutanix.Gateway, error) {
	if g, ok := f.gateways[id]; ok {
		return g, nil
	}
	return nil, &nutanix.APIError{Status: 404, Message: "gateway " + id + " not found"}
}
func (f *fakeReader) GetBGPSession(_ context.Context, id string) (*nutanix.BGPSession, error) {
	if s, ok := f.sessions[id]; ok {
		return s, nil
	}
	return nil, &nutanix.APIError{Status: 404, Message: "session " + id + " not found"}
}
func (f *fakeReader) BGPRoutes(context.Context, string) ([]nutanix.BGPRoute, error) {
	return f.bgpRoutes, nil
}
func (f *fakeReader) GetVM(_ context.Context, id string) (*nutanix.VM, error) {
	if v, ok := f.vms[id]; ok {
		return v, nil
	}
	return nil, &nutanix.APIError{Status: 404, Message: "vm " + id + " not found"}
}

func (f *fakeReader) Controllers(context.Context) ([]nutanix.Controller, error) {
	return f.controllers, nil
}
func (f *fakeReader) PrismGateway(context.Context) (netip.Addr, netip.Prefix, error) {
	return f.gw, f.pcNet, nil
}
func (f *fakeReader) ListClusters(context.Context) ([]nutanix.Cluster, error) { return f.clusters, nil }
func (f *fakeReader) ListVPCs(context.Context) ([]nutanix.VPC, error)         { return f.vpcs, nil }
func (f *fakeReader) GetSubnet(_ context.Context, id string) (*nutanix.Subnet, error) {
	if s, ok := f.subnets[id]; ok {
		return s, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeReader) Routes(_ context.Context, vpc string) (string, []nutanix.Route, error) {
	return "rt-" + vpc, f.routes[vpc], nil
}
func (f *fakeReader) MissingOperations(context.Context, string, []string) ([]string, error) {
	return f.missing, nil
}

func (f *fakeReader) FindRole(_ context.Context, name string) (*nutanix.Role, error) {
	for _, r := range f.roles {
		if r == name {
			return &nutanix.Role{ExtID: "role-" + r, DisplayName: r}, nil
		}
	}
	return nil, &nutanix.APIError{Status: 404, Message: "role " + name + " not found"}
}

func a(s string) netip.Addr   { return netip.MustParseAddr(s) }
func p(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// good is cluster-1 as measured in write-up 34.
func good() *fakeReader {
	return &fakeReader{
		controllers: []nutanix.Controller{{Status: "UP"}},
		gw:          a("10.0.210.1"), pcNet: p("10.0.210.10/24"),
		clusters: []nutanix.Cluster{{ExtID: "c-1", Name: "cluster-1", IsAOS: true,
			Addresses: []netip.Addr{a("10.0.210.40"), a("10.0.210.11"), a("10.0.210.42"), a("10.0.210.41")}}},
		vpcs: []nutanix.VPC{
			{ExtID: "vpc-t", Name: "portico-transit", Type: "TRANSIT", RoutablePrefixes: []netip.Prefix{p("198.51.100.0/24")},
				External: []nutanix.VPCExternal{{SubnetExtID: "ext-r", IPs: []netip.Addr{a("10.0.210.30")}}}},
			{ExtID: "vpc-x", Name: "other", Type: "REGULAR"},
		},
		subnets: map[string]*nutanix.Subnet{"ext-r": {ExtID: "ext-r", Name: "external-routed", Type: "VLAN", External: true, NAT: false, Prefix: p("10.0.210.0/24")}},
		routes:  map[string][]nutanix.Route{"vpc-t": {{Destination: p("0.0.0.0/0"), NextHopType: "EXTERNAL_SUBNET", NextHopExtID: "ext-r"}}},
	}
}

func picked() settings.SiteNetwork {
	return settings.SiteNetwork{TransitVPCExtID: "vpc-t", IntegratedConfirmed: true, DNS: []string{"1.1.1.1"}}
}

func check(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", name, checks)
	return Check{}
}

func TestAllGood(t *testing.T) {
	cs := Evaluate(context.Background(), good(), picked(), "sa-1", 0, "")
	if !Ready(cs) {
		t.Fatalf("checks = %+v", cs)
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "flow,mode,gateway,transit,transit-valid,rights,dns,pool,routing" {
		t.Fatalf("order = %v", names)
	}
}

func TestFlowDown(t *testing.T) {
	r := good()
	r.controllers = []nutanix.Controller{{Status: "DOWN"}}
	c := check(t, Evaluate(context.Background(), r, picked(), "sa-1", 0, ""), "flow")
	if c.Status != Failed || c.Message != "Enable Flow Virtual Networking in Prism Central (Integrated mode)" {
		t.Fatalf("flow = %+v", c)
	}
}

func TestModeNeedsConfirmation(t *testing.T) {
	n := picked()
	n.IntegratedConfirmed = false
	cs := Evaluate(context.Background(), good(), n, "sa-1", 0, "")
	if c := check(t, cs, "mode"); c.Status != Todo || Ready(cs) {
		t.Fatalf("mode = %+v", c)
	}
}

func TestGatewayIsClusterAddress(t *testing.T) {
	r := good()
	r.gw = a("10.0.210.11") // NYC1 had its cluster's Data Services IP
	c := check(t, Evaluate(context.Background(), r, picked(), "sa-1", 0, ""), "gateway")
	if c.Status != Failed || !strings.Contains(c.Message, "10.0.210.11") || !strings.Contains(c.Message, "cluster-1") {
		t.Fatalf("gateway = %+v", c)
	}
}

func TestGatewayOutsideSubnet(t *testing.T) {
	r := good()
	r.gw = a("10.0.220.254")
	if c := check(t, Evaluate(context.Background(), r, picked(), "sa-1", 0, ""), "gateway"); c.Status != Failed {
		t.Fatalf("gateway = %+v", c)
	}
}

func TestGatewayOK(t *testing.T) {
	if c := check(t, Evaluate(context.Background(), good(), picked(), "sa-1", 0, ""), "gateway"); c.Status != OK {
		t.Fatalf("gateway = %+v", c)
	}
}

func TestTransitNotPicked(t *testing.T) {
	c := check(t, Evaluate(context.Background(), good(), settings.SiteNetwork{IntegratedConfirmed: true}, "sa-1", 0, ""), "transit")
	if c.Status != Todo || c.Message != "Pick the transit VPC tenants use" {
		t.Fatalf("transit = %+v", c)
	}
}

func TestTransitInvalid(t *testing.T) {
	cases := map[string]func(r *fakeReader){
		"not a transit VPC":  func(r *fakeReader) { r.vpcs[0].Type = "REGULAR" },
		"NAT":                func(r *fakeReader) { r.subnets["ext-r"].NAT = true },
		"no default route":   func(r *fakeReader) { r.routes["vpc-t"] = nil },
		"no routable prefix": func(r *fakeReader) { r.vpcs[0].RoutablePrefixes = nil },
		"gone":               func(r *fakeReader) { r.vpcs = r.vpcs[1:] },
	}
	for name, mutate := range cases {
		r := good()
		mutate(r)
		c := check(t, Evaluate(context.Background(), r, picked(), "sa-1", 0, ""), "transit-valid")
		if c.Status != Failed || c.Message == "" {
			t.Fatalf("%s: transit-valid = %+v", name, c)
		}
	}
}

func TestPoolWarning(t *testing.T) {
	cs := Evaluate(context.Background(), good(), picked(), "sa-1", 13, "")
	if c := check(t, cs, "pool"); c.Status != Warning || !strings.Contains(c.Message, "13 of 16") || !Ready(cs) {
		t.Fatalf("pool = %+v", c)
	}
}

func TestRightsMissing(t *testing.T) {
	r := good()
	r.missing = []string{"Share_Subnet"}
	cs := Evaluate(context.Background(), r, picked(), "sa-1", 0, "")
	if c := check(t, cs, "rights"); c.Status != Failed || !strings.Contains(c.Message, "Repair") || !strings.Contains(c.Message, "Share_Subnet") {
		t.Fatalf("rights = %+v", c)
	}
}

func TestTransitChoices(t *testing.T) {
	ch, err := TransitChoices(context.Background(), good())
	if err != nil || len(ch) != 1 {
		t.Fatalf("choices = %+v, %v", ch, err)
	}
	c := ch[0]
	if c.VPCExtID != "vpc-t" || c.Name != "portico-transit" || c.Address != a("10.0.210.30") || c.RoutedPrefix != p("198.51.100.0/24") || c.Slices != 16 || c.ExternalSubnet != "external-routed" {
		t.Fatalf("choice = %+v", c)
	}
}

// Without DNS servers the tenants' VMs would resolve nothing (review).
func TestDNSNeeded(t *testing.T) {
	n := picked()
	n.DNS = nil
	cs := Evaluate(context.Background(), good(), n, "sa-1", 0, "")
	if c := check(t, cs, "dns"); c.Status != Todo || Ready(cs) {
		t.Fatalf("dns = %+v", c)
	}
	if c := check(t, Evaluate(context.Background(), good(), picked(), "sa-1", 0, ""), "dns"); c.Status != OK || !strings.Contains(c.Message, "1.1.1.1") {
		t.Fatalf("dns = %+v", c)
	}
}

// A site set up before 0.2.0 has no Portico Tenant role: its tenants
// would keep Project Admin, which creates floating IPs (review).
func TestTenantRoleNeeded(t *testing.T) {
	cs := Evaluate(context.Background(), good(), picked(), "sa-1", 0, "Portico Tenant")
	if c := check(t, cs, "tenant-role"); c.Status != Failed || !strings.Contains(c.Message, "Repair the site") || Ready(cs) {
		t.Fatalf("tenant-role = %+v", c)
	}
	r := good()
	r.roles = []string{"Portico Tenant"}
	if c := check(t, Evaluate(context.Background(), r, picked(), "sa-1", 0, "Portico Tenant"), "tenant-role"); c.Status != OK {
		t.Fatalf("tenant-role = %+v", c)
	}
	// Another role chosen by the MSP is not checked.
	for _, c := range Evaluate(context.Background(), good(), picked(), "sa-1", 0, "") {
		if c.Name == "tenant-role" {
			t.Fatalf("checked without a role: %+v", c)
		}
	}
}

// peered is good() with the peering's objects: the gateway and the
// session UP, the routed prefix advertised via the transit VPC's address,
// the edge VM on.
func peered() *fakeReader {
	f := good()
	f.gateways = map[string]*nutanix.Gateway{"gw-1": {ExtID: "gw-1", Name: "portico-site-a-bgp", Local: true, Address: a("10.0.210.195"), ASN: 65010, State: "UP", Version: "7.6.0.6"}}
	f.sessions = map[string]*nutanix.BGPSession{"s-1": {ExtID: "s-1", LocalExtID: "gw-1", RemoteExtID: "gw-r", State: "UP"}}
	f.bgpRoutes = []nutanix.BGPRoute{{Prefix: p("198.51.100.0/24"), NextHop: a("10.0.210.30")}}
	f.vms = map[string]*nutanix.VM{"vm-e": {ExtID: "vm-e", Name: "portico-site-a-edge", PowerOn: true, Categories: []string{"portico:edge"}}}
	return f
}

func routerNet() settings.SiteNetwork {
	n := picked()
	n.Peering = settings.SitePeering{Kind: settings.PeerRouter, GatewayAddr: p("10.0.210.195/24"), GatewayGW: a("10.0.210.1"), GatewayASN: 65010,
		RouterAddr: a("10.0.210.1"), RouterASN: 65000, LocalGatewayExtID: "gw-1", RemoteGatewayExtID: "gw-r", SessionExtID: "s-1", State: settings.PeeringUp}
	return n
}

func edgeNet() settings.SiteNetwork {
	n := picked()
	n.Peering = settings.SitePeering{Kind: settings.PeerEdge, GatewayAddr: p("10.0.210.195/24"), GatewayGW: a("10.0.210.1"), GatewayASN: 65010,
		EdgeAddr: a("10.0.210.194"), EdgeASN: 65020, LocalGatewayExtID: "gw-1", RemoteGatewayExtID: "gw-r", SessionExtID: "s-1", EdgeVMExtID: "vm-e", State: settings.PeeringUp}
	return n
}

func noCheck(t *testing.T, checks []Check, name string) {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			t.Fatalf("unexpected check %q: %+v", name, c)
		}
	}
}

func TestEvaluateStaticAddsOneUncheckedRow(t *testing.T) {
	cs := Evaluate(context.Background(), good(), picked(), "sa-1", 0, "")
	if c := check(t, cs, "routing"); c.Status != OK || !strings.Contains(c.Message, "not checked") || !strings.Contains(c.Message, "your router") {
		t.Fatalf("routing = %+v", c)
	}
	if !Ready(cs) || cs[len(cs)-1].Name != "routing" {
		t.Fatalf("checks = %+v", cs)
	}
	for _, name := range []string{"bgp gateway", "bgp session", "advertised", "edge router", "route on your core", "peer on your router"} {
		noCheck(t, cs, name)
	}
}

func TestEvaluateRouterNothingBuiltYet(t *testing.T) {
	n := routerNet()
	n.Peering.LocalGatewayExtID, n.Peering.RemoteGatewayExtID, n.Peering.SessionExtID, n.Peering.State = "", "", "", ""
	cs := Evaluate(context.Background(), peered(), n, "sa-1", 0, "")
	if c := check(t, cs, "bgp gateway"); c.Status != Todo || !strings.Contains(c.Message, "Save the Routing section") {
		t.Fatalf("bgp gateway = %+v", c)
	}
	if Ready(cs) {
		t.Fatal("ready without a peering")
	}
}

func TestEvaluateRouterAllUp(t *testing.T) {
	cs := Evaluate(context.Background(), peered(), routerNet(), "sa-1", 0, "")
	if !Ready(cs) {
		t.Fatalf("checks = %+v", cs)
	}
	if c := check(t, cs, "bgp gateway"); c.Status != OK || !strings.Contains(c.Message, "10.0.210.195") || !strings.Contains(c.Message, "7.6.0.6") {
		t.Fatalf("bgp gateway = %+v", c)
	}
	if c := check(t, cs, "bgp session"); c.Status != OK {
		t.Fatalf("bgp session = %+v", c)
	}
	if c := check(t, cs, "advertised"); c.Status != OK || !strings.Contains(c.Message, "198.51.100.0/24") || !strings.Contains(c.Message, "10.0.210.30") {
		t.Fatalf("advertised = %+v", c)
	}
	noCheck(t, cs, "edge router")
	noCheck(t, cs, "peer on your router")
	noCheck(t, cs, "routing")
}

func TestEvaluateRouterSessionDown(t *testing.T) {
	f := peered()
	f.sessions["s-1"].State, f.sessions["s-1"].Message = "DOWN", "Password/ASN Mismatch or BGP not configured on Peer. Current BGP state: Active"
	cs := Evaluate(context.Background(), f, routerNet(), "sa-1", 0, "")
	c := check(t, cs, "bgp session")
	if c.Status != Failed || !strings.Contains(c.Message, "Current BGP state: Active") || !strings.Contains(c.Message, "configure the session on your router") {
		t.Fatalf("bgp session = %+v", c)
	}
	w := check(t, cs, "peer on your router")
	for _, line := range []string{"router bgp 65000", "neighbor 10.0.210.195 remote-as 65010", "neighbor 10.0.210.195 activate"} {
		if !strings.Contains(w.Message, line) {
			t.Fatalf("peer on your router lacks %q: %+v", line, w)
		}
	}
	if w.Status != Warning || Ready(cs) {
		t.Fatalf("checks = %+v", cs)
	}
	if c := check(t, cs, "advertised"); c.Status != Failed {
		t.Fatalf("advertised with the session down = %+v", c)
	}
}

func TestEvaluateGatewayDown(t *testing.T) {
	f := peered()
	f.gateways["gw-1"].State, f.gateways["gw-1"].Message = "DOWN", "Service nginx is down"
	cs := Evaluate(context.Background(), f, routerNet(), "sa-1", 0, "")
	if c := check(t, cs, "bgp gateway"); c.Status != Failed || !strings.Contains(c.Message, "DOWN") || !strings.Contains(c.Message, "Service nginx is down") || !strings.Contains(c.Message, "Repair") {
		t.Fatalf("bgp gateway = %+v", c)
	}
	delete(f.gateways, "gw-1")
	cs = Evaluate(context.Background(), f, routerNet(), "sa-1", 0, "")
	if c := check(t, cs, "bgp gateway"); c.Status != Failed || !strings.Contains(c.Message, "gone") {
		t.Fatalf("bgp gateway gone = %+v", c)
	}
}

func TestEvaluateEdgeAdvertisedPrefixMissing(t *testing.T) {
	f := peered()
	f.bgpRoutes = []nutanix.BGPRoute{{Prefix: p("10.0.0.0/8"), NextHop: a("10.0.210.1")}}
	cs := Evaluate(context.Background(), f, edgeNet(), "sa-1", 0, "")
	if c := check(t, cs, "advertised"); c.Status != Failed || !strings.Contains(c.Message, "The routed prefix is not advertised: 10.0.0.0/8 via 10.0.210.1") {
		t.Fatalf("advertised = %+v", c)
	}
	f.bgpRoutes = nil
	cs = Evaluate(context.Background(), f, edgeNet(), "sa-1", 0, "")
	if c := check(t, cs, "advertised"); c.Status != Failed || !strings.Contains(c.Message, "no routes") {
		t.Fatalf("advertised without routes = %+v", c)
	}
	// The prefix via another next hop is not the transit VPC's.
	f.bgpRoutes = []nutanix.BGPRoute{{Prefix: p("198.51.100.0/24"), NextHop: a("10.0.210.99")}}
	cs = Evaluate(context.Background(), f, edgeNet(), "sa-1", 0, "")
	if c := check(t, cs, "advertised"); c.Status != Failed {
		t.Fatalf("advertised via another hop = %+v", c)
	}
}

func TestEvaluateEdgeRouteNotTicked(t *testing.T) {
	cs := Evaluate(context.Background(), peered(), edgeNet(), "sa-1", 0, "")
	w := check(t, cs, "route on your core")
	if w.Status != Warning || !strings.Contains(w.Message, "198.51.100.0/24 via 10.0.210.194") {
		t.Fatalf("route on your core = %+v", w)
	}
	if !Ready(cs) {
		t.Fatalf("a warning stops the switch: %+v", cs)
	}
	if c := check(t, cs, "edge router"); c.Status != OK {
		t.Fatalf("edge router = %+v", c)
	}
	n := edgeNet()
	n.Peering.RouteAdded = true
	noCheck(t, Evaluate(context.Background(), peered(), n, "sa-1", 0, ""), "route on your core")
}

func TestEvaluateEdgeRouterOffOrGone(t *testing.T) {
	f := peered()
	f.vms["vm-e"].PowerOn = false
	cs := Evaluate(context.Background(), f, edgeNet(), "sa-1", 0, "")
	if c := check(t, cs, "edge router"); c.Status != Failed || !strings.Contains(c.Message, "off") || !strings.Contains(c.Message, "Repair") {
		t.Fatalf("edge router off = %+v", c)
	}
	delete(f.vms, "vm-e")
	cs = Evaluate(context.Background(), f, edgeNet(), "sa-1", 0, "")
	if c := check(t, cs, "edge router"); c.Status != Failed || !strings.Contains(c.Message, "gone") {
		t.Fatalf("edge router gone = %+v", c)
	}
	n := edgeNet()
	n.Peering.EdgeVMExtID = ""
	cs = Evaluate(context.Background(), peered(), n, "sa-1", 0, "")
	if c := check(t, cs, "edge router"); c.Status != Failed {
		t.Fatalf("edge router unbuilt = %+v", c)
	}
}

// The core's static route is shown before the routed prefix is known
// (the transit check did not answer, write-up 39): the line names the
// prefix instead of printing an invalid one.
func TestCoreRouteWithoutThePrefix(t *testing.T) {
	p := settings.SitePeering{EdgeAddr: netip.MustParseAddr("10.23.210.196")}
	if got := CoreRoute(netip.Prefix{}, p); got != "<routed prefix> via 10.23.210.196" {
		t.Fatalf("CoreRoute = %q", got)
	}
	if got := CoreRoute(netip.MustParsePrefix("100.96.0.0/24"), p); got != "100.96.0.0/24 via 10.23.210.196" {
		t.Fatalf("CoreRoute = %q", got)
	}
}

// A site that keeps its static route (0.2.x, no peer) needs only the
// tenant networking operations: the peering ones are a warning until a
// peer is chosen, never a red card after the upgrade (Review Focus 1).
func TestRightsOfAStaticSiteWarnOnThePeeringOperations(t *testing.T) {
	r := good()
	r.missing = []string{"View_Network_Gateway", "Create_Bgp_Session"}
	cs := Evaluate(context.Background(), r, picked(), "sa-1", 0, "")
	if c := check(t, cs, "rights"); c.Status != Warning || !strings.Contains(c.Message, "Repair") || !strings.Contains(c.Message, "View_Network_Gateway") || !Ready(cs) {
		t.Fatalf("rights = %+v", c)
	}
	n := picked()
	n.Peering = settings.SitePeering{Kind: settings.PeerRouter}
	cs = Evaluate(context.Background(), r, n, "sa-1", 0, "")
	if c := check(t, cs, "rights"); c.Status != Failed {
		t.Fatalf("rights with a peer = %+v", c)
	}
}

// The rows promise what Repair does (write-up 39's deferred minors): a
// gateway that is DOWN is waited for, not recreated; an edge that is
// off is turned on; a session down in edge mode is fixed by Replace,
// not by Repair; the objects of a running build read "being built".
func TestPeeringRowsPromiseWhatRepairDoes(t *testing.T) {
	f := peered()
	f.gateways["gw-1"].State, f.gateways["gw-1"].Message = "DOWN", "Service nginx is down"
	cs := Evaluate(context.Background(), f, routerNet(), "sa-1", 0, "")
	if c := check(t, cs, "bgp gateway"); c.Status != Failed || !strings.Contains(c.Message, "Repair waits for it") || strings.Contains(c.Message, "recreates") {
		t.Fatalf("gateway DOWN = %+v", c)
	}
	f = peered()
	f.vms["vm-e"].PowerOn = false
	cs = Evaluate(context.Background(), f, edgeNet(), "sa-1", 0, "")
	if c := check(t, cs, "edge router"); c.Status != Failed || !strings.Contains(c.Message, "Repair turns it on") {
		t.Fatalf("edge off = %+v", c)
	}
	f = peered()
	f.sessions["s-1"].State, f.sessions["s-1"].Message = "DOWN", "Current BGP state: Active"
	cs = Evaluate(context.Background(), f, edgeNet(), "sa-1", 0, "")
	if c := check(t, cs, "bgp session"); c.Status != Failed || !strings.Contains(c.Message, "Replace the edge router") || strings.Contains(c.Message, "Repair replaces") {
		t.Fatalf("session down (edge) = %+v", c)
	}
	n := edgeNet()
	n.Peering.State, n.Peering.SessionExtID, n.Peering.EdgeVMExtID = settings.PeeringBuilding, "", ""
	cs = Evaluate(context.Background(), peered(), n, "sa-1", 0, "")
	for _, name := range []string{"bgp session", "edge router"} {
		if c := check(t, cs, name); c.Status != Todo || !strings.Contains(c.Message, "being built") {
			t.Fatalf("%s during a build = %+v", name, c)
		}
	}
	n.Peering.LocalGatewayExtID = ""
	cs = Evaluate(context.Background(), peered(), n, "sa-1", 0, "")
	if c := check(t, cs, "bgp gateway"); c.Status != Todo || !strings.Contains(c.Message, "being built") {
		t.Fatalf("gateway during a build = %+v", c)
	}
}
