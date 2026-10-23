package api

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/branding"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
	"github.com/marco-fabbri/portico/internal/tenant/tenanttest"
	"golang.org/x/crypto/bcrypt"
)

// newEnv2 is the tenant API over two sites.
func newEnv2(t *testing.T) (*env, *tenanttest.PC) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "portico.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, _ := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.MinCost)
	st.SetAdminPassword(context.Background(), "admin", string(hash))
	pcA, pcB := tenanttest.NewPC("portico-idp"), tenanttest.NewPC("portico-idp")
	kc := tenanttest.NewKC()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	sites := []tenant.Site{
		{ID: "site-a", Code: "AZ-IT-MI1", PC: pcA, SAMLIdPName: "portico-idp", ClusterExtID: "cl-a", StorageContainerExtID: "sc-a"},
		{ID: "site-b", Code: "AZ-US-NYC1", PC: pcB, SAMLIdPName: "portico-idp", ClusterExtID: "cl-b", StorageContainerExtID: "sc-b"},
	}
	svc := tenant.NewService(st, sites, kc, tenant.Config{OperationTimeout: 5 * time.Second}, log)
	brand := branding.NewService(st, log)
	return &env{h: BasicAuth(st, "portico", log)(New(svc, st, brand, nil, log)), svc: svc, st: st, pc: pcA, kc: kc, brand: brand, log: &buf}, pcB
}

func TestTenantAPIPlacementsShape(t *testing.T) {
	e, _ := newEnv2(t)
	rec, out := e.do("POST", "/api/v1/tenants", `{"slug":"bare","display_name":"Bare"}`, true)
	if rec.Code != 202 || len(out["placements"].([]any)) != 0 {
		t.Fatalf("no sites with two configured: %d %v", rec.Code, out)
	}
	rec, out = e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME","sites":[{"site":"site-a"},{"site":"site-b","cluster_ext_id":"cl-own"}]}`, true)
	if rec.Code != 202 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	e.svc.Wait()
	rec, out = e.do("GET", "/api/v1/tenants/acme", "", true)
	ps := out["placements"].([]any)
	if rec.Code != 200 || len(ps) != 2 {
		t.Fatalf("get: %d %v", rec.Code, out)
	}
	a, b := ps[0].(map[string]any), ps[1].(map[string]any)
	if a["site"] != "site-a" || a["code"] != "AZ-IT-MI1" || a["status"] != "active" || a["project_id"] != "portico-acme" || a["cluster_ext_id"] != "cl-a" || b["cluster_ext_id"] != "cl-own" || b["storage_container_ext_id"] != "sc-b" {
		t.Fatalf("placements = %v", ps)
	}
	if _, has := out["project_ext_id"]; has {
		t.Fatalf("the tenant still carries project fields: %v", out)
	}
	rec, _ = e.do("GET", "/api/v1/tenants/acme/steps", "", true)
	if !strings.Contains(rec.Body.String(), `"site":"site-b"`) || !strings.Contains(rec.Body.String(), `"site":""`) {
		t.Fatalf("steps lack the site: %s", rec.Body.String())
	}
	_, out = e.do("GET", "/api/v1/tenants/acme/visibility", "", true)
	if sites := out["sites"].(map[string]any); len(sites) != 2 || sites["site-b"].(map[string]any)["ok"] != true {
		t.Fatalf("visibility = %v", out)
	}
	if rec, out := e.do("POST", "/api/v1/tenants", `{"slug":"beta","display_name":"B","sites":[{"site":"nope"}]}`, true); rec.Code != 400 || !strings.Contains(out["error"].(string), "site") {
		t.Fatalf("unknown site: %d %v", rec.Code, out)
	}
}

func TestTenantAPIAddRemoveSite(t *testing.T) {
	e, pcB := newEnv2(t)
	e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME","sites":[{"site":"site-a"}]}`, true)
	e.svc.Wait()
	rec, out := e.do("POST", "/api/v1/tenants/acme/sites", `{"site":"site-b"}`, true)
	if rec.Code != 202 {
		t.Fatalf("add site: %d %v", rec.Code, out)
	}
	e.svc.Wait()
	if rec, out := e.do("POST", "/api/v1/tenants/acme/sites", `{"site":"site-b"}`, true); rec.Code != 409 {
		t.Fatalf("add the same site again: %d %v", rec.Code, out)
	}
	if len(pcB.Memberships) != 1 {
		t.Fatalf("site-b not provisioned: %+v", pcB.Memberships)
	}
	rec, out = e.do("DELETE", "/api/v1/tenants/acme/sites/site-b", "", true)
	if rec.Code != 202 {
		t.Fatalf("disconnect: %d %v", rec.Code, out)
	}
	e.svc.Wait()
	if len(pcB.Memberships) != 0 || len(pcB.Projects) != 1 {
		t.Fatalf("site-b left: %+v %+v", pcB.Memberships, pcB.Projects)
	}
	if rec, _ := e.do("DELETE", "/api/v1/tenants/acme/sites/site-b", "", true); rec.Code != 404 {
		t.Fatalf("remove a site the tenant is not on: %d", rec.Code)
	}
}

func TestSuspendResumeAPI(t *testing.T) {
	e, _ := newEnv2(t)
	e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME","sites":[{"site":"site-a"}]}`, true)
	e.svc.Wait()
	if rec, out := e.do("POST", "/api/v1/tenants/acme/sites/site-a/suspend", "", true); rec.Code != 200 || out["status"] != "suspended" {
		t.Fatalf("suspend: %d %v", rec.Code, out)
	}
	if rec, out := e.do("POST", "/api/v1/tenants/acme/sites/site-a/resume", "", true); rec.Code != 200 || out["status"] != "active" {
		t.Fatalf("resume: %d %v", rec.Code, out)
	}
	p, _ := e.st.GetPlacement(context.Background(), "acme", "site-a")
	p.Status = store.PlacementRemoving
	e.st.UpdatePlacement(context.Background(), p)
	if rec, _ := e.do("POST", "/api/v1/tenants/acme/sites/site-a/suspend", "", true); rec.Code != 409 {
		t.Fatalf("suspend of a removing placement: %d", rec.Code)
	}
}

func TestDisconnectAPI(t *testing.T) {
	e, pcB := newEnv2(t)
	e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME","sites":[{"site":"site-a"},{"site":"site-b"}]}`, true)
	e.svc.Wait()
	p, _ := e.st.GetPlacement(context.Background(), "acme", "site-b")
	pcB.Contents[p.ProjectExtID] = nutanix.Contents{Counts: map[string]int{"vms": 1}}
	rec, out := e.do("DELETE", "/api/v1/tenants/acme/sites/site-b", "", true)
	if counts, _ := out["counts"].(map[string]any); rec.Code != 409 || counts["vms"] != float64(1) {
		t.Fatalf("disconnect of a project with a VM: %d %v", rec.Code, out)
	}
	if rec, out := e.do("GET", "/api/v1/tenants/acme/sites/site-b/contents", "", true); rec.Code != 200 || out["empty"] != false {
		t.Fatalf("contents: %d %v", rec.Code, out)
	}
	pcB.Contents[p.ProjectExtID] = nutanix.Contents{Counts: map[string]int{}}
	if rec, out := e.do("GET", "/api/v1/tenants/acme/sites/site-b/contents", "", true); rec.Code != 200 || out["empty"] != true {
		t.Fatalf("contents when empty: %d %v", rec.Code, out)
	}
	if rec, out := e.do("DELETE", "/api/v1/tenants/acme/sites/site-b", "", true); rec.Code != 202 {
		t.Fatalf("disconnect of an empty project (old query ignored): %d %v", rec.Code, out)
	}
	e.svc.Wait()
	e.pc.FailOnce["ProjectContents"] = &nutanix.APIError{Status: 504, Message: "slow"}
	if rec, out := e.do("DELETE", "/api/v1/tenants/acme/sites/site-a", "", true); rec.Code != 503 {
		t.Fatalf("disconnect with a failed count: %d %v", rec.Code, out)
	}
	if rec, out := e.do("DELETE", "/api/v1/tenants/acme/sites/site-a", "", true); rec.Code != 202 {
		t.Fatalf("disconnect of the last site: %d %v", rec.Code, out)
	}
}

func TestTenantAPIIncompleteSite409(t *testing.T) {
	e, _ := newEnv2(t)
	e.svc.SetSiteGate(func(id string) error {
		if id == "site-b" {
			return fmt.Errorf("%w: finish the setup of AZ-US-NYC1 first", tenant.ErrSiteIncomplete)
		}
		return nil
	})
	rec, out := e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME","sites":[{"site":"site-b"}]}`, true)
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "finish the setup of AZ-US-NYC1 first") {
		t.Fatalf("create on an incomplete site: %d %v", rec.Code, out)
	}
	if rec, _ := e.do("POST", "/api/v1/tenants", `{"slug":"acme","display_name":"ACME","sites":[{"site":"site-a"}]}`, true); rec.Code != 202 {
		t.Fatalf("create on a complete site: %d", rec.Code)
	}
	e.svc.Wait()
	if rec, out := e.do("POST", "/api/v1/tenants/acme/sites", `{"site":"site-b"}`, true); rec.Code != 409 || !strings.Contains(out["error"].(string), "finish the setup") {
		t.Fatalf("connect to an incomplete site: %d %v", rec.Code, out)
	}
}
