package tenant

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

var netSteps = []string{"net-external-subnet", "net-share", "net-vpc", "net-default-route", "net-subnet", "net-floating-ips", "net-read-back"}

// newNetEnv is newEnv with the site's ready network on: a routed transit
// VPC whose externally routable prefix is 10.23.211.0/24, as on NTNX-01.
func newNetEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.pc.VPCs = append(e.pc.VPCs, nutanix.VPC{ExtID: "vpc-transit", Name: "portico-transit", Type: "TRANSIT",
		RoutablePrefixes: []netip.Prefix{netip.MustParsePrefix("10.23.211.0/24")}})
	sites := e.svc.Sites()
	sites[0].Network = SiteNetwork{Enabled: true, TransitVPCExtID: "vpc-transit", DNS: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	e.svc.SetSites(sites)
	return e
}

// netStepsOf returns the tenant's recorded net- steps, in order.
func netStepsOf(t *testing.T, e *env, id string) []store.Step {
	t.Helper()
	steps, err := e.st.ListSteps(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Step
	for _, s := range steps {
		if strings.HasPrefix(s.Name, "net-") {
			out = append(out, s)
		}
	}
	return out
}

func TestConnectBuildsNetwork(t *testing.T) {
	e := newNetEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	steps := netStepsOf(t, e, "acme")
	if len(steps) != len(netSteps) {
		t.Fatalf("net steps = %+v", steps)
	}
	for i, s := range steps {
		if s.Name != netSteps[i] || !s.OK {
			t.Fatalf("step %d = %+v, want %s ok", i, s, netSteps[i])
		}
	}
	pl, tn := e.place(t, "acme"), e.get(t, "acme")
	if pl.Status != store.PlacementActive || pl.NetState != store.NetReady || pl.NetError != "" || pl.TransitPrefix != "10.23.211.0/28" || tn.Subnet != "10.100.0.0/24" {
		t.Fatalf("placement = %+v, tenant subnet %q", pl, tn.Subnet)
	}
	ext, net := e.pc.SubnetNamed("portico-acme-ext"), e.pc.SubnetNamed("portico-acme-net")
	vpc := e.pc.VPCNamed("portico-acme-vpc")
	if ext == nil || !ext.External || ext.VPCExtID != "vpc-transit" || ext.Prefix != netip.MustParsePrefix("10.23.211.0/28") || !slices.Contains(ext.SharedWith, pl.ProjectExtID) {
		t.Fatalf("external subnet = %+v", ext)
	}
	if vpc == nil || vpc.ProjectExtID != pl.ProjectExtID || vpc.External[0].SubnetExtID != ext.ExtID {
		t.Fatalf("vpc = %+v", vpc)
	}
	if net == nil || net.ProjectExtID != pl.ProjectExtID || net.VPCExtID != vpc.ExtID || net.Prefix != netip.MustParsePrefix("10.100.0.0/24") {
		t.Fatalf("subnet = %+v", net)
	}
	if r := e.pc.RoutesOf(vpc.ExtID); len(r) != 1 || r[0].Destination != netip.MustParsePrefix("0.0.0.0/0") || r[0].NextHopExtID != ext.ExtID {
		t.Fatalf("routes = %+v", r)
	}
	fip := e.pc.FloatingIPNamed("portico-acme-fip-1")
	if fip == nil || len(pl.FloatingIPs) != 1 || pl.FloatingIPs[0].ExtID != fip.ExtID || pl.FloatingIPs[0].IP != fip.IP.String() {
		t.Fatalf("floating ip = %+v, stored %+v", fip, pl.FloatingIPs)
	}
	if pl.ExtSubnetExtID != ext.ExtID || pl.VPCExtID != vpc.ExtID || pl.SubnetExtID != net.ExtID || pl.SNATIP != vpc.External[0].IPs[0].String() {
		t.Fatalf("stored ids = %+v", pl)
	}
}

func TestConnectWithoutNetwork(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	if steps := netStepsOf(t, e, "acme"); len(steps) != 0 {
		t.Fatalf("net steps = %+v", steps)
	}
	if pl := e.place(t, "acme"); pl.NetState != store.NetNone || pl.Status != store.PlacementActive {
		t.Fatalf("placement = %+v", pl)
	}
}

func TestNetworkFailureKeepsConnectionActive(t *testing.T) {
	e := newNetEnv(t)
	e.pc.FailOnce["CreateVPC"] = errors.New("vpc refused")
	e.create(t, "acme")
	e.svc.Wait()
	pl, tn := e.place(t, "acme"), e.get(t, "acme")
	if pl.Status != store.PlacementActive || pl.NetState != store.NetFailed || !strings.Contains(pl.NetError, "vpc refused") || tn.Status != store.StatusActive {
		t.Fatalf("placement = %+v, tenant %s", pl, tn.Status)
	}
}

func TestTwoTenantsTwoSlices(t *testing.T) {
	e := newNetEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	e.create(t, "beta")
	e.svc.Wait()
	if a, b := e.place(t, "acme"), e.place(t, "beta"); a.TransitPrefix != "10.23.211.0/28" || b.TransitPrefix != "10.23.211.16/28" || b.NetState != store.NetReady {
		t.Fatalf("slices = %q %q, beta %+v", a.TransitPrefix, b.TransitPrefix, b)
	}
}

func TestFailedHalfwayKeepsSlice(t *testing.T) {
	e := newNetEnv(t)
	e.pc.FailOnce["CreateVPC"] = errors.New("vpc refused")
	e.create(t, "acme")
	e.svc.Wait()
	e.create(t, "beta")
	e.svc.Wait()
	if a, b := e.place(t, "acme"), e.place(t, "beta"); a.TransitPrefix != "10.23.211.0/28" || b.TransitPrefix != "10.23.211.16/28" {
		t.Fatalf("slices = %q %q", a.TransitPrefix, b.TransitPrefix)
	}
}

// Prism Central once kept a subnet without the project the request
// carried (write-up 34): Portico deletes it and creates it once more.
func TestSubnetWithoutProjectRecreated(t *testing.T) {
	e := newNetEnv(t)
	e.pc.DropProject = map[string]int{"portico-acme-net": 1}
	e.create(t, "acme")
	e.svc.Wait()
	pl := e.place(t, "acme")
	if pl.NetState != store.NetReady || e.pc.Creates["CreateOverlaySubnet"] != 2 || e.pc.CallCount("DeleteSubnet") != 1 {
		t.Fatalf("placement = %+v, creates %d, deletes %d", pl, e.pc.Creates["CreateOverlaySubnet"], e.pc.CallCount("DeleteSubnet"))
	}
	if net := e.pc.SubnetNamed("portico-acme-net"); net == nil || net.ProjectExtID != pl.ProjectExtID || pl.SubnetExtID != net.ExtID {
		t.Fatalf("subnet = %+v", net)
	}
}

func TestSubnetWithoutProjectTwiceFails(t *testing.T) {
	e := newNetEnv(t)
	e.pc.DropProject = map[string]int{"portico-acme-net": 2}
	e.create(t, "acme")
	e.svc.Wait()
	if pl := e.place(t, "acme"); pl.NetState != store.NetFailed || !strings.Contains(pl.NetError, "Prism Central created portico-acme-net without its project") {
		t.Fatalf("placement = %+v", pl)
	}
}

// Tenants from before 0.2.0 get their /24 at the upgrade, in creation
// order; an exception occupies its slot.
func TestAssignSubnetsCreationOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, tn := range []*store.Tenant{
		{ID: "zeta", DisplayName: "Z", Status: store.StatusActive, Subnet: "10.100.0.0/24", SubnetException: true},
		{ID: "omega", DisplayName: "O", Status: store.StatusActive},
		{ID: "alpha", DisplayName: "A", Status: store.StatusActive},
	} {
		if err := e.st.CreateTenant(ctx, tn); err != nil {
			t.Fatal(err)
		}
	}
	n, err := e.svc.AssignSubnets(ctx)
	if err != nil || n != 2 {
		t.Fatalf("AssignSubnets = %d, %v", n, err)
	}
	if o, a := e.get(t, "omega"), e.get(t, "alpha"); o.Subnet != "10.100.1.0/24" || a.Subnet != "10.100.2.0/24" || o.SubnetException {
		t.Fatalf("omega %q, alpha %q", o.Subnet, a.Subnet)
	}
	if n, err := e.svc.AssignSubnets(ctx); err != nil || n != 0 {
		t.Fatalf("second AssignSubnets = %d, %v", n, err)
	}
}

func TestCreateStoresException(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Create(context.Background(), CreateRequest{Slug: "acme", DisplayName: "ACME", Subnet: "192.168.5.0/24"}); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	e.create(t, "beta")
	e.svc.Wait()
	if a, b := e.get(t, "acme"), e.get(t, "beta"); a.Subnet != "192.168.5.0/24" || !a.SubnetException || b.Subnet != "10.100.0.0/24" {
		t.Fatalf("acme %q %v, beta %q", a.Subnet, a.SubnetException, b.Subnet)
	}
	_, err := e.svc.Create(context.Background(), CreateRequest{Slug: "gamma", DisplayName: "G", Subnet: "192.168.5.0/24"})
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("an overlapping exception: %v", err)
	}
}

func TestBuildIsIdempotent(t *testing.T) {
	e := newNetEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	before := map[string]int{}
	for k, v := range e.pc.Creates {
		before[k] = v
	}
	site, _ := e.svc.site("site-a")
	if err := e.svc.buildNetwork(context.Background(), e.get(t, "acme"), e.place(t, "acme"), site); err != nil {
		t.Fatalf("buildNetwork: %v", err)
	}
	for k, v := range e.pc.Creates {
		if before[k] != v {
			t.Fatalf("%s: %d creates after %d", k, v, before[k])
		}
	}
	if e.pc.CallCount("ShareSubnet") != 1 {
		t.Fatalf("shared %d times", e.pc.CallCount("ShareSubnet"))
	}
	if pl := e.place(t, "acme"); pl.NetState != store.NetReady {
		t.Fatalf("placement = %+v", pl)
	}
}
