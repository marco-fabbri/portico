package web

import (
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/netready"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
)

// netReader answers the Networking card's reads: a ready site unless a
// test changes it.
type netReader struct {
	gateway netip.Addr
}

func (f *netReader) Controllers(context.Context) ([]nutanix.Controller, error) {
	return []nutanix.Controller{{Status: "UP"}}, nil
}
func (f *netReader) PrismGateway(context.Context) (netip.Addr, netip.Prefix, error) {
	return f.gateway, netip.MustParsePrefix("10.23.210.175/24"), nil
}
func (f *netReader) ListClusters(context.Context) ([]nutanix.Cluster, error) {
	return []nutanix.Cluster{{ExtID: "cl-1", Name: "NTNX-01", Addresses: []netip.Addr{netip.MustParseAddr("10.23.210.173")}}}, nil
}
func (f *netReader) ListVPCs(context.Context) ([]nutanix.VPC, error) {
	return []nutanix.VPC{{ExtID: "vpc-transit", Name: "portico-transit", Type: "TRANSIT",
		External:         []nutanix.VPCExternal{{SubnetExtID: "ext-routed", IPs: []netip.Addr{netip.MustParseAddr("10.23.210.200")}}},
		RoutablePrefixes: []netip.Prefix{netip.MustParsePrefix("10.23.211.0/24")}}}, nil
}
func (f *netReader) GetSubnet(_ context.Context, id string) (*nutanix.Subnet, error) {
	return &nutanix.Subnet{ExtID: id, Name: "external-routed", Type: "VLAN", External: true}, nil
}
func (f *netReader) Routes(context.Context, string) (string, []nutanix.Route, error) {
	return "rt-1", []nutanix.Route{{Destination: netip.MustParsePrefix("0.0.0.0/0")}}, nil
}
func (f *netReader) MissingOperations(context.Context, string, []string) ([]string, error) {
	return nil, nil
}
func (f *netReader) FindRole(_ context.Context, name string) (*nutanix.Role, error) {
	return &nutanix.Role{ExtID: "role-pt", DisplayName: name}, nil
}

// withNetworking is withSetup with the site's service account stored and
// the Networking card reading r.
func withNetworking(t *testing.T, e *env, r *netReader) *settings.Service {
	t.Helper()
	svc, _, _ := withSetup(t, e, true)
	if _, err := e.web.setup.UpdateSite(context.Background(), "site-a", func(s *settings.Site) { s.ServiceAccountExtID = "sa-1" }); err != nil {
		t.Fatal(err)
	}
	e.web.netReader = func(settings.Site, *slog.Logger) (netready.Reader, error) { return r, nil }
	return svc
}

func storedNetwork(t *testing.T, svc *settings.Service) settings.SiteNetwork {
	t.Helper()
	d, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, _ := d.Site("site-a")
	return s.Network
}

func TestNetworkingCardChecks(t *testing.T) {
	e := newEnv(t, true)
	withNetworking(t, e, &netReader{gateway: netip.MustParseAddr("10.23.210.173")})
	e.login(t)
	rec := e.hx("GET", "/setup/sites/site-a/networking", nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "an address of the cluster NTNX-01") || !strings.Contains(body, "portico-transit") || !strings.Contains(body, "10.23.211.0/24") {
		t.Fatalf("card: %d\n%s", rec.Code, body)
	}
	if page := e.do("GET", "/setup/sites/site-a", nil, true).Body.String(); !strings.Contains(page, `hx-get="/setup/sites/site-a/networking"`) {
		t.Fatal("the site page does not load the Networking card")
	}
}

func TestNetworkingSwitchRefusedWhenNotReady(t *testing.T) {
	e := newEnv(t, true)
	svc := withNetworking(t, e, &netReader{gateway: netip.MustParseAddr("10.23.210.173")})
	e.login(t)
	rec := e.do("POST", "/setup/sites/site-a/networking", e.form("transit_vpc_ext_id", "vpc-transit", "dns", "1.1.1.1", "integrated", "on", "enabled", "on"), true)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "stays off") || !strings.Contains(rec.Body.String(), "gateway") {
		t.Fatalf("switch: %d\n%s", rec.Code, rec.Body.String())
	}
	if n := storedNetwork(t, svc); n.Enabled || n.TransitVPCExtID != "vpc-transit" {
		t.Fatalf("stored = %+v", n)
	}
}

func TestNetworkingSaveTransit(t *testing.T) {
	e := newEnv(t, true)
	svc := withNetworking(t, e, &netReader{gateway: netip.MustParseAddr("10.23.210.254")})
	e.login(t)
	rec := e.do("POST", "/setup/sites/site-a/networking", e.form("transit_vpc_ext_id", "vpc-transit", "dns", "1.1.1.1, 8.8.8.8", "integrated", "on", "enabled", "on"), true)
	if rec.Code != 200 {
		t.Fatalf("save: %d\n%s", rec.Code, rec.Body.String())
	}
	n := storedNetwork(t, svc)
	if !n.Enabled || n.TransitVPCExtID != "vpc-transit" || !n.IntegratedConfirmed || strings.Join(n.DNS, ",") != "1.1.1.1,8.8.8.8" {
		t.Fatalf("stored = %+v", n)
	}
	if rec := e.do("POST", "/setup/sites/site-a/networking", e.form("transit_vpc_ext_id", "vpc-transit", "dns", "not-an-address"), true); rec.Code != 400 {
		t.Fatalf("a bad DNS server: %d", rec.Code)
	}
}

// netPlacement stores a ready network on acme's connection.
func netPlacement(t *testing.T, e *env, state store.NetState, msg string) {
	t.Helper()
	p, err := e.st.GetPlacement(context.Background(), "acme", "site-a")
	if err != nil {
		t.Fatal(err)
	}
	p.NetState, p.NetError, p.TransitPrefix, p.SNATIP = state, msg, "10.23.211.16/28", "10.23.211.29"
	p.FloatingIPs = []store.FloatingIP{{ExtID: "fip-1", IP: "10.23.211.18"}}
	if err := e.st.UpdatePlacement(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func TestTenantPageShowsNetwork(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.createTenant(t, "acme")
	netPlacement(t, e, store.NetReady, "")
	body := e.do("GET", "/tenants/acme", nil, true).Body.String()
	for _, want := range []string{"10.23.211.18", "10.23.211.29", "10.23.211.16/28", "10.100.0.0/24", `status-ready`} {
		if !strings.Contains(body, want) {
			t.Fatalf("the page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "/network/retry") {
		t.Fatal("Retry network on a ready network")
	}
}

func TestRetryNetworkButton(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.createTenant(t, "acme")
	netPlacement(t, e, store.NetFailed, "vpc refused")
	body := e.do("GET", "/tenants/acme", nil, true).Body.String()
	if !strings.Contains(body, `action="/tenants/acme/sites/site-a/network/retry"`) || !strings.Contains(body, "vpc refused") {
		t.Fatalf("no Retry network:\n%s", body)
	}
	// Create ready network shows on a connection with none, on a site
	// whose ready network is on.
	netPlacement(t, e, store.NetNone, "")
	sites := e.svc.Sites()
	sites[0].Network = tenant.SiteNetwork{Enabled: true, TransitVPCExtID: "vpc-transit"}
	e.svc.SetSites(sites)
	if body := e.do("GET", "/tenants/acme", nil, true).Body.String(); !strings.Contains(body, `action="/tenants/acme/sites/site-a/network"`) {
		t.Fatalf("no Create ready network:\n%s", body)
	}
}

func TestPoolReadOnlyOnceAssigned(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	e.login(t)
	if body := e.do("GET", "/setup", nil, true).Body.String(); !strings.Contains(body, `name="address_pool"`) || strings.Contains(body, `name="address_pool" value="10.100.0.0/16" readonly`) {
		t.Fatalf("pool field before any tenant:\n%s", body)
	}
	e.createTenant(t, "acme")
	if body := e.do("GET", "/setup", nil, true).Body.String(); !strings.Contains(body, "readonly") {
		t.Fatalf("pool field not read-only:\n%s", body)
	}
	rec := e.do("POST", "/setup/save", e.form("section", "tenants", "project_prefix", "portico", "group_prefix", "tenant-", "role", "Portico Tenant", "address_pool", "10.200.0.0/16"), true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "address pool") {
		t.Fatalf("a pool change with a tenant holding a subnet: %d\n%s", rec.Code, rec.Body.String())
	}
	// The same pool is no change.
	if rec := e.do("POST", "/setup/save", e.form("section", "tenants", "project_prefix", "portico", "group_prefix", "tenant-", "role", "Portico Tenant", "address_pool", "10.100.0.0/16"), true); rec.Code != 303 {
		t.Fatalf("the same pool: %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestTenantNewException(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.createTenant(t, "acme")
	rec := e.do("POST", "/tenants", e.form("slug", "beta", "display_name", "Beta", "subnet", "10.100.0.0/24", "allowance", "2"), true)
	if rec.Code != 409 && rec.Code != 400 || !strings.Contains(rec.Body.String(), "acme") {
		t.Fatalf("an overlapping exception: %d\n%s", rec.Code, rec.Body.String())
	}
	rec = e.do("POST", "/tenants", e.form("slug", "gamma", "display_name", "Gamma", "subnet", "192.168.5.0/24", "allowance", "2"), true)
	if rec.Code != 303 {
		t.Fatalf("an exception: %d\n%s", rec.Code, rec.Body.String())
	}
	e.svc.Wait()
	if g, _ := e.st.GetTenant(context.Background(), "gamma"); g.Subnet != "192.168.5.0/24" || g.FIPAllowance != 2 {
		t.Fatalf("gamma = %+v", g)
	}
}
