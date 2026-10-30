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
	if strings.Join(names, ",") != "flow,mode,gateway,transit,transit-valid,rights,dns,pool" {
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
