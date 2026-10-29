package api

import (
	"testing"
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
