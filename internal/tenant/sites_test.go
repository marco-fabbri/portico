package tenant

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant/tenanttest"
)

// env2 is a service over two sites, each with its own fake Prism Central.
type env2 struct {
	svc      *Service
	st       *store.SQLite
	pcA, pcB *tenanttest.PC
	kc       *tenanttest.KC
	log      *bytes.Buffer
	changes  []string
}

func newEnv2(t *testing.T) *env2 {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "portico.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env2{st: st, pcA: tenanttest.NewPC("portico-idp"), pcB: tenanttest.NewPC("portico-idp"), kc: tenanttest.NewKC(), log: &bytes.Buffer{}}
	log := slog.New(slog.NewJSONHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sites := []Site{
		{ID: "site-a", Code: "SITE-A", URL: "https://pc-a.test:9440", PC: e.pcA, SAMLIdPName: "portico-idp", ClusterExtID: "cl-a", StorageContainerExtID: "sc-a"},
		{ID: "site-b", Code: "SITE-B", PC: e.pcB, SAMLIdPName: "portico-idp", ClusterExtID: "cl-b", StorageContainerExtID: "sc-b"},
	}
	cfg := Config{ProjectPrefix: "portico", GroupPrefix: "tenant-", RoleName: "Project Admin", OperationTimeout: 5 * time.Second}
	e.svc = NewService(st, sites, e.kc, cfg, log)
	e.svc.OnPlacementChange(func(id string) { e.changes = append(e.changes, id) })
	return e
}

func (e *env2) createOn(t *testing.T, slug string, sites ...string) {
	t.Helper()
	req := CreateRequest{Slug: slug, DisplayName: strings.ToUpper(slug)}
	for _, s := range sites {
		req.Sites = append(req.Sites, PlacementRequest{Site: s})
	}
	if _, err := e.svc.Create(context.Background(), req); err != nil {
		t.Fatalf("Create %s on %v: %v", slug, sites, err)
	}
	e.svc.Wait()
}

func (e *env2) placement(t *testing.T, tenant, site string) *store.Placement {
	t.Helper()
	p, err := e.st.GetPlacement(context.Background(), tenant, site)
	if err != nil {
		t.Fatalf("placement %s/%s: %v", tenant, site, err)
	}
	return p
}

func (e *env2) tenant(t *testing.T, id string) *store.Tenant {
	t.Helper()
	tn, err := e.st.GetTenant(context.Background(), id)
	if err != nil {
		t.Fatalf("tenant %s: %v", id, err)
	}
	return tn
}

func TestCreateOnTwoSites(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a", "site-b")
	if tn := e.tenant(t, "acme"); tn.Status != store.StatusActive {
		t.Fatalf("tenant = %q (%s)", tn.Status, tn.LastError)
	}
	for _, c := range []struct {
		site string
		pc   *tenanttest.PC
		cl   string
	}{{"site-a", e.pcA, "cl-a"}, {"site-b", e.pcB, "cl-b"}} {
		p := e.placement(t, "acme", c.site)
		if p.Status != store.PlacementActive || p.ProjectID != "portico-acme" || p.ClusterExtID != c.cl || p.ProjectExtID == "" || p.SAMLGroupExtID == "" || p.RoleMembershipExtID == "" || !p.IdPShared {
			t.Fatalf("placement %s = %+v", c.site, p)
		}
		if c.pc.Creates["CreateProject"] != 1 || c.pc.RGCluster[c.pc.RGs[0].ExtID] != c.cl {
			t.Fatalf("%s: creates %v rgs %+v", c.site, c.pc.Creates, c.pc.RGs)
		}
	}
	if len(e.kc.Groups) != 1 {
		t.Fatalf("keycloak groups = %+v (one per tenant)", e.kc.Groups)
	}
	steps, _ := e.st.ListSteps(context.Background(), "acme")
	if len(steps) != 13 || steps[0].Name != "keycloak-group" || steps[0].SiteID != "" || steps[1].SiteID != "site-a" || steps[12].SiteID != "site-b" || steps[12].Name != "visibility" {
		t.Fatalf("steps = %+v", steps)
	}
}

func TestPartialFailureAndRetry(t *testing.T) {
	e := newEnv2(t)
	e.pcB.FailOnce["CreateRoleMembership"] = &nutanix.APIError{Status: 500, Message: "membership exploded"}
	e.createOn(t, "acme", "site-a", "site-b")
	tn := e.tenant(t, "acme")
	if tn.Status != store.StatusFailed || !strings.Contains(tn.LastError, "site SITE-B") || !strings.Contains(tn.LastError, "role-membership") {
		t.Fatalf("tenant = %q %q", tn.Status, tn.LastError)
	}
	if a := e.placement(t, "acme", "site-a"); a.Status != store.PlacementActive {
		t.Fatalf("site-a = %+v", a)
	}
	if b := e.placement(t, "acme", "site-b"); b.Status != store.PlacementFailed || !strings.Contains(b.LastError, "membership exploded") {
		t.Fatalf("site-b = %+v", b)
	}
	if err := e.svc.Retry(context.Background(), "acme"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.svc.Wait()
	if tn := e.tenant(t, "acme"); tn.Status != store.StatusActive || tn.LastError != "" {
		t.Fatalf("after retry = %q %q", tn.Status, tn.LastError)
	}
	if b := e.placement(t, "acme", "site-b"); b.Status != store.PlacementActive || b.LastError != "" {
		t.Fatalf("site-b after retry = %+v", b)
	}
	steps, _ := e.st.ListSteps(context.Background(), "acme")
	for _, s := range steps[13:] {
		if s.SiteID == "site-a" {
			t.Fatalf("the retry ran a step on the active site: %+v", s)
		}
	}
}

func TestCreateDefaultsToOnlySite(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	p, err := e.st.GetPlacement(context.Background(), "acme", "site-a")
	if err != nil || p.Status != store.PlacementActive || p.ClusterExtID != "cl-1" || p.StorageContainerExtID != "sc-1" {
		t.Fatalf("placement = %+v %v", p, err)
	}
}

func TestCreateRequestErrors(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		req  CreateRequest
		want error
	}{
		{"unknown site", CreateRequest{Slug: "a2", DisplayName: "x", Sites: []PlacementRequest{{Site: "nope"}}}, ErrUnknownSite},
		{"site twice", CreateRequest{Slug: "a3", DisplayName: "x", Sites: []PlacementRequest{{Site: "site-a"}, {Site: "site-a"}}}, ErrPlacementExists},
	} {
		if _, err := e.svc.Create(ctx, c.req); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if _, err := e.st.GetTenant(ctx, c.req.Slug); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: a row was stored", c.name)
		}
	}
	e.svc.sites["site-a"] = Site{ID: "site-a", Code: "SITE-A", PC: e.pcA, SAMLIdPName: "portico-idp"}
	if _, err := e.svc.Create(ctx, CreateRequest{Slug: "a4", DisplayName: "x", Sites: []PlacementRequest{{Site: "site-a"}}}); !errors.Is(err, ErrNoPlacementTarget) {
		t.Errorf("no cluster: err = %v", err)
	}
	e.svc.Wait()
	if e.pcA.Calls != 0 || e.pcB.Calls != 0 || e.kc.Calls != 0 {
		t.Fatal("a refused request reached a backend")
	}
}

func TestPlacementDefaultsFromSite(t *testing.T) {
	e := newEnv2(t)
	if _, err := e.svc.Create(context.Background(), CreateRequest{Slug: "acme", DisplayName: "x", Sites: []PlacementRequest{{Site: "site-a", ClusterExtID: "cl-own"}, {Site: "site-b"}}}); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if a := e.placement(t, "acme", "site-a"); a.ClusterExtID != "cl-own" || a.StorageContainerExtID != "sc-a" {
		t.Fatalf("site-a = %+v", a)
	}
	if b := e.placement(t, "acme", "site-b"); b.ClusterExtID != "cl-b" || b.StorageContainerExtID != "sc-b" {
		t.Fatalf("site-b = %+v", b)
	}
}

func TestAddSiteLater(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	e.createOn(t, "acme", "site-a")
	before := e.pcA.Creates["CreateProject"]
	e.pcB.FailOnce["ShareIdP"] = &nutanix.APIError{Status: 500, Message: "share failed"}
	if err := e.svc.AddSite(ctx, "acme", PlacementRequest{Site: "site-b"}); err != nil {
		t.Fatalf("AddSite: %v", err)
	}
	e.svc.Wait()
	if b := e.placement(t, "acme", "site-b"); b.Status != store.PlacementFailed {
		t.Fatalf("site-b after a failure = %+v", b)
	}
	if tn := e.tenant(t, "acme"); tn.Status != store.StatusActive {
		t.Fatalf("a failed new site changed the tenant: %q", tn.Status)
	}
	if err := e.svc.Retry(ctx, "acme"); err != nil {
		t.Fatalf("Retry of the new site: %v", err)
	}
	e.svc.Wait()
	if b := e.placement(t, "acme", "site-b"); b.Status != store.PlacementActive {
		t.Fatalf("site-b after retry = %+v", b)
	}
	if e.pcA.Creates["CreateProject"] != before {
		t.Fatal("adding a site touched the other one")
	}
	if err := e.svc.AddSite(ctx, "acme", PlacementRequest{Site: "site-b"}); !errors.Is(err, ErrPlacementExists) {
		t.Fatalf("AddSite twice = %v", err)
	}
	if err := e.svc.AddSite(ctx, "acme", PlacementRequest{Site: "nope"}); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("AddSite unknown = %v", err)
	}
}

func TestDisconnectKeepsOtherSite(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	e.createOn(t, "acme", "site-a", "site-b")
	e.changes = nil
	if err := e.svc.Disconnect(ctx, "acme", "site-b"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	e.svc.Wait()
	if _, err := e.st.GetPlacement(ctx, "acme", "site-b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("site-b placement after removal: %v", err)
	}
	if len(e.pcB.Memberships) != 0 || len(e.pcB.Groups) != 0 || len(e.pcB.RGs) != 0 || len(e.pcB.Projects) != 1 {
		t.Fatalf("site-b objects left: %+v %+v %+v %+v", e.pcB.Memberships, e.pcB.Groups, e.pcB.RGs, e.pcB.Projects)
	}
	if a := e.placement(t, "acme", "site-a"); a.Status != store.PlacementActive || len(e.pcA.Memberships) != 1 {
		t.Fatalf("site-a after removing site-b = %+v", a)
	}
	if tn := e.tenant(t, "acme"); tn.Status != store.StatusActive || len(e.kc.Groups) != 1 {
		t.Fatalf("tenant after removing a site = %+v", tn)
	}
	if len(e.changes) == 0 || e.changes[0] != "acme" {
		t.Fatalf("placement changes = %v", e.changes)
	}
	if err := e.svc.Disconnect(ctx, "acme", "site-b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Disconnect twice = %v", err)
	}
}

func TestDisconnectLastSiteAllowed(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	if err := e.svc.Disconnect(context.Background(), "acme", "site-a"); err != nil {
		t.Fatalf("Disconnect of the last site = %v", err)
	}
	e.svc.Wait()
	ps, _ := e.st.ListPlacements(context.Background(), "acme")
	if len(ps) != 0 || e.tenant(t, "acme").Status != store.StatusActive {
		t.Fatalf("after disconnecting the last site: %d placements, tenant %s", len(ps), e.tenant(t, "acme").Status)
	}
}

func TestVisibilityPerSite(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	e.createOn(t, "acme", "site-a", "site-b")
	vs, err := e.svc.Visibility(ctx, "acme")
	if err != nil || len(vs) != 2 || !vs["site-a"].OK || !vs["site-b"].OK {
		t.Fatalf("visibility = %+v %v", vs, err)
	}
	b := e.placement(t, "acme", "site-b")
	e.pcB.Policies["Leaky_acp"] = []string{b.SAMLGroupExtID}
	vs, _ = e.svc.Visibility(ctx, "acme")
	if !vs["site-a"].OK || vs["site-b"].OK || !strings.Contains(strings.Join(vs["site-b"].Findings, " "), "Leaky_acp") {
		t.Fatalf("visibility with a finding on site-b = %+v", vs)
	}
}

func TestPlacementChangeHookFires(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	if len(e.changes) == 0 || e.changes[len(e.changes)-1] != "acme" {
		t.Fatalf("changes after create = %v", e.changes)
	}
	n := len(e.changes)
	if err := e.svc.Disconnect(context.Background(), "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if len(e.changes) == n {
		t.Fatal("no change reported for the disconnect")
	}
}

// Review I2: a site whose removal failed is never provisioned again by a
// retry; the removal is repeated instead.
func TestRetrySkipsAFailedRemoval(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	e.createOn(t, "acme", "site-a", "site-b")
	e.pcB.FailOnce["DeleteUserGroup"] = &nutanix.APIError{Status: 500, Message: "group busy"}
	if err := e.svc.Disconnect(ctx, "acme", "site-b"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	b := e.placement(t, "acme", "site-b")
	if b.Status != store.PlacementRemoveFailed || !strings.Contains(b.LastError, "group busy") {
		t.Fatalf("site-b after a failed removal = %+v", b)
	}
	before := e.pcB.Creates["CreateRoleMembership"]
	if err := e.svc.Retry(ctx, "acme"); !errors.Is(err, ErrState) {
		t.Fatalf("Retry after a failed removal = %v, want ErrState", err)
	}
	e.svc.Wait()
	if e.pcB.Creates["CreateRoleMembership"] != before {
		t.Fatal("a retry re-granted the access being removed")
	}
	if err := e.svc.Disconnect(ctx, "acme", "site-b"); err != nil {
		t.Fatalf("Disconnect again: %v", err)
	}
	e.svc.Wait()
	if _, err := e.st.GetPlacement(ctx, "acme", "site-b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("site-b after the second removal: %v", err)
	}
}

// A site added later that failed, next to a failed removal: the retry
// provisions only the first.
func TestRetryProvisionsOnlyFailedProvisioning(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	e.createOn(t, "acme", "site-a")
	e.pcB.FailOnce["ShareIdP"] = &nutanix.APIError{Status: 500, Message: "share failed"}
	e.svc.AddSite(ctx, "acme", PlacementRequest{Site: "site-b"})
	e.svc.Wait()
	a := e.placement(t, "acme", "site-a")
	a.Status = store.PlacementRemoveFailed
	e.st.UpdatePlacement(ctx, a)
	membershipsA := e.pcA.Creates["CreateRoleMembership"]
	if err := e.svc.Retry(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if e.placement(t, "acme", "site-b").Status != store.PlacementActive || e.placement(t, "acme", "site-a").Status != store.PlacementRemoveFailed || e.pcA.Creates["CreateRoleMembership"] != membershipsA {
		t.Fatalf("retry: a=%+v b=%+v", e.placement(t, "acme", "site-a"), e.placement(t, "acme", "site-b"))
	}
}

// An interrupted removal ends as a failed removal, not a failed
// provisioning.
func TestRecoverInterruptedRemoval(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	e.createOn(t, "acme", "site-a", "site-b")
	b := e.placement(t, "acme", "site-b")
	b.Status = store.PlacementRemoving
	e.st.UpdatePlacement(ctx, b)
	if _, err := e.svc.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.placement(t, "acme", "site-b").Status; got != store.PlacementRemoveFailed {
		t.Fatalf("interrupted removal = %q", got)
	}
}

func TestSuspendKeepsPrismCentralUntouched(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	calls, changes := e.pcA.Calls, len(e.changes)
	if err := e.svc.Suspend(context.Background(), "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	if p := e.placement(t, "acme", "site-a"); p.Status != store.PlacementSuspended {
		t.Fatalf("status = %s", p.Status)
	}
	if e.pcA.Calls != calls {
		t.Fatalf("Prism Central called %d times by Suspend", e.pcA.Calls-calls)
	}
	if len(e.changes) != changes+1 || e.changes[len(e.changes)-1] != "acme" {
		t.Fatalf("changes = %v", e.changes)
	}
}

func TestResumeRestoresSameProject(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	before := e.placement(t, "acme", "site-a")
	creates := e.pcA.Creates["CreateProject"]
	ctx := context.Background()
	if err := e.svc.Suspend(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Resume(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	after := e.placement(t, "acme", "site-a")
	if after.Status != store.PlacementActive || after.ProjectExtID != before.ProjectExtID || e.pcA.Creates["CreateProject"] != creates {
		t.Fatalf("after resume: %+v, creates %d→%d", after, creates, e.pcA.Creates["CreateProject"])
	}
}

func TestSuspendRefusedUnlessActive(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	ctx := context.Background()
	if err := e.svc.Resume(ctx, "acme", "site-a"); !errors.Is(err, ErrState) {
		t.Fatalf("Resume of an active placement: %v", err)
	}
	p := e.placement(t, "acme", "site-a")
	p.Status = store.PlacementRemoving
	e.st.UpdatePlacement(ctx, p)
	if err := e.svc.Suspend(ctx, "acme", "site-a"); !errors.Is(err, ErrState) {
		t.Fatalf("Suspend of a removing placement: %v", err)
	}
	if err := e.svc.Suspend(ctx, "acme", "site-z"); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("Suspend on an unknown site: %v", err)
	}
}

func TestProvisionSkipsSuspended(t *testing.T) {
	e := newEnv2(t)
	e.pcB.FailOnce["CreateRoleMembership"] = &nutanix.APIError{Status: 500, Message: "membership exploded"}
	e.createOn(t, "acme", "site-a", "site-b")
	ctx := context.Background()
	if err := e.svc.Suspend(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	if p := e.placement(t, "acme", "site-b"); p.Status != store.PlacementFailed {
		t.Fatalf("site-b = %s, want failed", p.Status)
	}
	if err := e.svc.Retry(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if a, b := e.placement(t, "acme", "site-a"), e.placement(t, "acme", "site-b"); a.Status != store.PlacementSuspended || b.Status != store.PlacementActive {
		t.Fatalf("after retry: site-a %s, site-b %s", a.Status, b.Status)
	}
}

func (e *env2) contents(t *testing.T, tenant, site string, c nutanix.Contents) {
	t.Helper()
	pc := map[string]*tenanttest.PC{"site-a": e.pcA, "site-b": e.pcB}[site]
	pc.Contents[e.placement(t, tenant, site).ProjectExtID] = c
}

func TestDisconnectRefusedWhenNotEmpty(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	e.contents(t, "acme", "site-a", nutanix.Contents{Counts: map[string]int{"vms": 1}})
	calls := e.pcA.Calls
	err := e.svc.Disconnect(context.Background(), "acme", "site-a")
	var ne *NotEmptyError
	if !errors.As(err, &ne) || ne.Site != "SITE-A" || ne.Project != "portico-acme" || ne.URL != "https://pc-a.test:9440" || ne.Contents.Counts["vms"] != 1 {
		t.Fatalf("Disconnect of a project with a VM = %v (%+v)", err, ne)
	}
	if !strings.Contains(err.Error(), "still holds VMs: 1") {
		t.Fatalf("message = %q", err.Error())
	}
	if p := e.placement(t, "acme", "site-a"); p.Status != store.PlacementActive {
		t.Fatalf("placement after a refusal = %s", p.Status)
	}
	if e.pcA.Calls != calls+1 {
		t.Fatalf("%d Prism Central calls, want the count only", e.pcA.Calls-calls)
	}
}

func TestDisconnectRefusedWhenCountFails(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	e.pcA.FailOnce["ProjectContents"] = &nutanix.APIError{Status: 504, Message: "gateway timeout"}
	if err := e.svc.Disconnect(context.Background(), "acme", "site-a"); !errors.Is(err, ErrCount) {
		t.Fatalf("Disconnect with a failed count = %v", err)
	}
	e.svc.Wait()
	if p := e.placement(t, "acme", "site-a"); p.Status != store.PlacementActive || len(e.pcA.Memberships) != 1 {
		t.Fatalf("after a failed count: %s, %d memberships", p.Status, len(e.pcA.Memberships))
	}
}

func TestDisconnectEmptyDeletesProject(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	ctx := context.Background()
	if err := e.svc.Disconnect(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if _, err := e.st.GetPlacement(ctx, "acme", "site-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("placement after disconnect: %v", err)
	}
	for _, pr := range e.pcA.Projects {
		if pr.Name == "portico-acme" {
			t.Fatal("project kept")
		}
	}
	if len(e.pcA.RGs) != 0 || len(e.pcA.Groups) != 0 || len(e.pcA.Memberships) != 0 || len(e.pcA.IdPs[0].SharedWithProjects) != 0 {
		t.Fatalf("left on site-a: rgs %d groups %d memberships %d shared %v", len(e.pcA.RGs), len(e.pcA.Groups), len(e.pcA.Memberships), e.pcA.IdPs[0].SharedWithProjects)
	}
	steps, _ := e.st.ListSteps(ctx, "acme")
	ok := map[string]bool{}
	for _, st := range steps {
		if st.SiteID == "site-a" && st.OK && strings.Contains(st.Detail, "deleted") || st.Name == "idp-unshare" && st.OK {
			ok[st.Name] = true
		}
	}
	for _, n := range []string{"role-membership", "idp-unshare", "saml-group", "resource-group", "project"} {
		if !ok[n] {
			t.Errorf("step %s not recorded as done", n)
		}
	}
}

func TestDisconnectSuspendedAllowed(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	ctx := context.Background()
	e.svc.Suspend(ctx, "acme", "site-a")
	if err := e.svc.Disconnect(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if _, err := e.st.GetPlacement(ctx, "acme", "site-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("suspended placement after disconnect: %v", err)
	}
}

func TestDisconnectProjectDeleteRefusedByPrismCentral(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	ctx := context.Background()
	e.pcA.FailOnce["DeleteProject"] = &nutanix.APIError{Status: 500, Message: "task t-1 FAILED: legacy error", Task: true}
	if err := e.svc.Disconnect(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	p := e.placement(t, "acme", "site-a")
	if p.Status != store.PlacementRemoveFailed || !strings.Contains(p.LastError, "Prism Central refused to delete the project: it still holds something") {
		t.Fatalf("after Prism Central's refusal: %s %q", p.Status, p.LastError)
	}
	if err := e.svc.RetrySite(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if _, err := e.st.GetPlacement(ctx, "acme", "site-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after the retry: %v", err)
	}
}

func TestDisconnectNeverDeletesForeignProject(t *testing.T) {
	e := newEnv2(t)
	e.createOn(t, "acme", "site-a")
	ctx := context.Background()
	p := e.placement(t, "acme", "site-a")
	p.ProjectID = "someone-else"
	e.st.UpdatePlacement(ctx, p)
	if err := e.svc.Disconnect(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	kept := false
	for _, pr := range e.pcA.Projects {
		kept = kept || pr.ExtID == p.ProjectExtID
	}
	if !kept {
		t.Fatal("a project outside Portico's naming was deleted")
	}
}

func TestRetrySiteProvisionsOnlyThatSite(t *testing.T) {
	e := newEnv2(t)
	e.pcA.FailOnce["CreateRoleMembership"] = &nutanix.APIError{Status: 500, Message: "a exploded"}
	e.pcB.FailOnce["CreateRoleMembership"] = &nutanix.APIError{Status: 500, Message: "b exploded"}
	e.createOn(t, "acme", "site-a", "site-b")
	ctx := context.Background()
	if a, b := e.placement(t, "acme", "site-a"), e.placement(t, "acme", "site-b"); a.Status != store.PlacementFailed || b.Status != store.PlacementFailed {
		t.Fatalf("setup: %s %s", a.Status, b.Status)
	}
	e.pcB.FailOnce["CreateRoleMembership"] = &nutanix.APIError{Status: 500, Message: "b would be retried"}
	if err := e.svc.RetrySite(ctx, "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	_, untouched := e.pcB.FailOnce["CreateRoleMembership"]
	if a, b := e.placement(t, "acme", "site-a"), e.placement(t, "acme", "site-b"); a.Status != store.PlacementActive || b.Status != store.PlacementFailed || !untouched {
		t.Fatalf("after RetrySite(site-a): a %s, b %s, site-b retried %v", a.Status, b.Status, !untouched)
	}
}

func TestCreateNilSitesWithSeveralConfiguredHasNone(t *testing.T) {
	e := newEnv2(t)
	ctx := context.Background()
	if _, err := e.svc.Create(ctx, CreateRequest{Slug: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if ps, _ := e.st.ListPlacements(ctx, "acme"); len(ps) != 0 || len(e.svc.DefaultSites()) != 0 {
		t.Fatalf("%d placements, defaults %v", len(ps), e.svc.DefaultSites())
	}
}

func TestTargetsMarkSiteDefaults(t *testing.T) {
	e := newEnv2(t)
	e.pcA.Clusters = []nutanix.Cluster{{ExtID: "pc", Name: "prism-central", IsPrismCentral: true}, {ExtID: "cl-a", Name: "NTNX-01", IsAOS: true}}
	e.pcA.Containers = []nutanix.StorageContainer{
		{ExtID: "sc-other", Name: "bulk", ClusterExtID: "cl-a"},
		{ExtID: "sys", Name: "NutanixManagementShare", ClusterExtID: "cl-a", IsSystem: true},
		{ExtID: "sc-a", Name: "default-container", ClusterExtID: "cl-a"},
	}
	got, err := e.svc.Targets(context.Background(), "site-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Clusters) != 1 || got.Clusters[0] != (Target{ExtID: "cl-a", Name: "NTNX-01", SiteDefault: true}) {
		t.Fatalf("clusters = %+v", got.Clusters)
	}
	if len(got.Containers) != 2 || got.Containers[0] != (Target{ExtID: "sc-a", Name: "default-container", ClusterName: "NTNX-01", SiteDefault: true}) ||
		got.Containers[1] != (Target{ExtID: "sc-other", Name: "bulk", ClusterName: "NTNX-01"}) {
		t.Fatalf("containers = %+v", got.Containers)
	}
}

func TestTargetsOnErrorOffersDefaults(t *testing.T) {
	e := newEnv2(t)
	e.pcA.FailOnce["ListClusters"] = errors.New("timeout")
	got, err := e.svc.Targets(context.Background(), "site-a")
	if err == nil {
		t.Fatal("no error")
	}
	if len(got.Clusters) != 1 || got.Clusters[0] != (Target{ExtID: "cl-a", SiteDefault: true}) ||
		len(got.Containers) != 1 || got.Containers[0] != (Target{ExtID: "sc-a", SiteDefault: true}) {
		t.Fatalf("targets = %+v", got)
	}
}
