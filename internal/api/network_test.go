package api

import (
	"context"
	"testing"

	"github.com/marco-fabbri/portico/internal/store"
)

func TestNetworkRoutes(t *testing.T) {
	e, _ := newEnv2(t)
	if rec, out := e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME","sites":[{"site":"site-a"}]}`, true); rec.Code != 202 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	e.svc.Wait()
	// The site's ready network is off, and the connection has none.
	if rec, out := e.do("POST", "/api/v1/tenants/acme/sites/site-a/network", "", true); rec.Code != 409 {
		t.Fatalf("create network: %d %v", rec.Code, out)
	}
	if rec, out := e.do("POST", "/api/v1/tenants/acme/sites/site-a/network/retry", "", true); rec.Code != 409 {
		t.Fatalf("retry network: %d %v", rec.Code, out)
	}
	if rec, out := e.do("POST", "/api/v1/tenants/acme/sites/site-b/network/retry", "", true); rec.Code != 404 {
		t.Fatalf("retry on a site not connected: %d %v", rec.Code, out)
	}
	if rec, out := e.do("PUT", "/api/v1/tenants/acme/floating-ips", `{"allowance":13}`, true); rec.Code != 400 {
		t.Fatalf("allowance 13: %d %v", rec.Code, out)
	}
	if rec, out := e.do("PUT", "/api/v1/tenants/acme/floating-ips", `{}`, true); rec.Code != 400 {
		t.Fatalf("no allowance: %d %v", rec.Code, out)
	}
	rec, out := e.do("PUT", "/api/v1/tenants/acme/floating-ips", `{"allowance":3}`, true)
	if rec.Code != 200 || out["allowance"] != float64(3) || out["kept"] != float64(0) {
		t.Fatalf("allowance 3: %d %v", rec.Code, out)
	}
	if rec, _ := e.do("PUT", "/api/v1/tenants/acme/floating-ips", `{"allowance":3}`, false); rec.Code != 401 {
		t.Fatalf("without a session: %d", rec.Code)
	}
}

// The tenant's wire shape carries its address plan and, per site, the
// ready network's state and addresses.
func TestTenantJSONShowsNetwork(t *testing.T) {
	e, _ := newEnv2(t)
	if rec, out := e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME","subnet":"192.168.5.0/24","floating_ips":2,"sites":[{"site":"site-a"}]}`, true); rec.Code != 202 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	e.svc.Wait()
	p, err := e.st.GetPlacement(context.Background(), "acme", "site-a")
	if err != nil {
		t.Fatal(err)
	}
	p.NetState, p.TransitPrefix, p.SNATIP = store.NetReady, "10.23.211.16/28", "10.23.211.29"
	p.FloatingIPs = []store.FloatingIP{{ExtID: "fip-1", IP: "10.23.211.18"}}
	if err := e.st.UpdatePlacement(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	rec, out := e.do("GET", "/api/v1/tenants/acme", "", true)
	if rec.Code != 200 || out["subnet"] != "192.168.5.0/24" || out["subnet_exception"] != true || out["floating_ips"] != float64(2) {
		t.Fatalf("tenant: %d %v", rec.Code, out)
	}
	net := out["placements"].([]any)[0].(map[string]any)["network"].(map[string]any)
	fips := net["floating_ips"].([]any)
	if net["state"] != "ready" || net["transit_prefix"] != "10.23.211.16/28" || net["snat_ip"] != "10.23.211.29" || len(fips) != 1 || fips[0].(map[string]any)["ip"] != "10.23.211.18" {
		t.Fatalf("network: %v", net)
	}
}
