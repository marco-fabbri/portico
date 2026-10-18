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

type env struct {
	svc *Service
	st  *store.SQLite
	pc  *tenanttest.PC
	kc  *tenanttest.KC
	log *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "portico.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pc := tenanttest.NewPC("lab-idp")
	kc := tenanttest.NewKC()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := Config{ProjectPrefix: "portico", GroupPrefix: "tenant-", RoleName: "Project Admin", SAMLIdPName: "lab-idp",
		ClusterExtID: "cl-1", StorageContainerExtID: "sc-1", OperationTimeout: 5 * time.Second}
	return &env{svc: NewService(st, pc, kc, cfg, log), st: st, pc: pc, kc: kc, log: &buf}
}

func (e *env) create(t *testing.T, slug string) *store.Tenant {
	t.Helper()
	tn, err := e.svc.Create(context.Background(), CreateRequest{Slug: slug, DisplayName: strings.ToUpper(slug)})
	if err != nil {
		t.Fatalf("Create %s: %v", slug, err)
	}
	return tn
}

func (e *env) get(t *testing.T, id string) *store.Tenant {
	t.Helper()
	tn, err := e.st.GetTenant(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTenant %s: %v", id, err)
	}
	return tn
}

func TestSlugValidation(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{"Acme", "a b", "-a", "a-", "", strings.Repeat("a", 33), "a_b", "a.b"} {
		_, err := e.svc.Create(context.Background(), CreateRequest{Slug: bad, DisplayName: "x"})
		if !errors.Is(err, ErrInvalidSlug) {
			t.Errorf("slug %q: err = %v, want ErrInvalidSlug", bad, err)
		}
	}
	e.svc.Wait()
	if e.pc.Calls != 0 || e.kc.Calls != 0 {
		t.Fatalf("backend calls on invalid slugs: pc=%d kc=%d", e.pc.Calls, e.kc.Calls)
	}
	if _, err := e.svc.Create(context.Background(), CreateRequest{Slug: "acme", DisplayName: "ACME"}); err != nil {
		t.Fatalf("valid slug refused: %v", err)
	}
	e.svc.Wait()
}

func TestProvisionHappyPath(t *testing.T) {
	e := newEnv(t)
	tn := e.create(t, "acme")
	if tn.Status != store.StatusCreating {
		t.Fatalf("status after Create = %q", tn.Status)
	}
	e.svc.Wait()
	tn = e.get(t, "acme")
	if tn.Status != store.StatusActive {
		t.Fatalf("status = %q, last error %q", tn.Status, tn.LastError)
	}
	if len(e.kc.Groups) != 1 || e.kc.Groups[0].Name != "tenant-acme" || tn.KeycloakGroupID != e.kc.Groups[0].ID {
		t.Fatalf("keycloak groups = %+v, tenant %+v", e.kc.Groups, tn)
	}
	if len(e.pc.Projects) != 2 || e.pc.Projects[1].Name != "portico-acme" || e.pc.Projects[1].ID != "portico-acme" || tn.ProjectExtID != e.pc.Projects[1].ExtID || tn.ProjectID != "portico-acme" {
		t.Fatalf("projects = %+v, tenant %+v", e.pc.Projects, tn)
	}
	if len(e.pc.RGs) != 1 || e.pc.RGs[0].ProjectExtID != tn.ProjectExtID || tn.ResourceGroupExtID != e.pc.RGs[0].ExtID {
		t.Fatalf("resource groups = %+v", e.pc.RGs)
	}
	if len(e.pc.Groups) != 1 || e.pc.Groups[0].Name != "tenant-acme" || tn.SAMLGroupExtID != e.pc.Groups[0].ExtID {
		t.Fatalf("saml groups = %+v", e.pc.Groups)
	}
	if !tn.IdPShared || len(e.pc.IdPs[0].SharedWithProjects) != 1 || e.pc.IdPs[0].SharedWithProjects[0] != tn.ProjectExtID {
		t.Fatalf("idp sharing = %+v, tenant %+v", e.pc.IdPs[0], tn)
	}
	if len(e.pc.Memberships) != 1 || e.pc.Memberships[0].ProjectExtID != tn.ProjectExtID || e.pc.Memberships[0].IdentityExtID != tn.SAMLGroupExtID || e.pc.Memberships[0].RoleExtID != "role-pa" || tn.RoleMembershipExtID != e.pc.Memberships[0].ExtID {
		t.Fatalf("memberships = %+v", e.pc.Memberships)
	}
	steps, _ := e.st.ListSteps(context.Background(), "acme")
	want := []string{"keycloak-group", "project", "resource-group", "saml-group", "idp-share", "role-membership", "visibility"}
	if len(steps) != len(want) {
		t.Fatalf("steps = %+v", steps)
	}
	for i, s := range steps {
		if s.Name != want[i] || !s.OK {
			t.Fatalf("step %d = %+v, want %s ok", i, s, want[i])
		}
	}
}

func TestProvisionIsIdempotentAfterFailure(t *testing.T) {
	e := newEnv(t)
	e.pc.FailOnce["CreateRoleMembership"] = &nutanix.APIError{Status: 500, Message: "membership exploded"}
	e.create(t, "acme")
	e.svc.Wait()
	tn := e.get(t, "acme")
	if tn.Status != store.StatusFailed || !strings.Contains(tn.LastError, "membership exploded") {
		t.Fatalf("after failure: status %q, last error %q", tn.Status, tn.LastError)
	}
	if err := e.svc.Retry(context.Background(), "acme"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	e.svc.Wait()
	tn = e.get(t, "acme")
	if tn.Status != store.StatusActive {
		t.Fatalf("after retry: status %q, last error %q", tn.Status, tn.LastError)
	}
	for method, want := range map[string]int{"CreateProject": 1, "CreateResourceGroup": 1, "CreateUserGroup": 1, "ShareIdP": 1, "CreateRoleMembership": 1} {
		if e.pc.Creates[method] != want {
			t.Errorf("%s called %d times, want %d", method, e.pc.Creates[method], want)
		}
	}
	if e.kc.Creates["CreateGroup"] != 1 {
		t.Errorf("CreateGroup called %d times, want 1", e.kc.Creates["CreateGroup"])
	}
	if tn.LastError != "" {
		t.Errorf("LastError not cleared: %q", tn.LastError)
	}
}

func TestProvisionRefusesDefaultProject(t *testing.T) {
	e := newEnv(t)
	e.pc.Projects = append(e.pc.Projects, nutanix.Project{ExtID: "p-def", ID: "default2", Name: "portico-acme", IsDefault: true})
	e.create(t, "acme")
	e.svc.Wait()
	tn := e.get(t, "acme")
	if tn.Status != store.StatusFailed || !strings.Contains(tn.LastError, "default") {
		t.Fatalf("status %q, last error %q", tn.Status, tn.LastError)
	}
	if e.pc.Creates["CreateResourceGroup"] != 0 {
		t.Fatal("a resource group was created on the default project")
	}
}

func TestProvisionRequiresExactlyOneIdP(t *testing.T) {
	for _, idps := range [][]nutanix.SAMLIdP{nil, {{ExtID: "a", Name: "lab-idp"}, {ExtID: "b", Name: "lab-idp"}}} {
		e := newEnv(t)
		e.pc.IdPs = idps
		e.create(t, "acme")
		e.svc.Wait()
		tn := e.get(t, "acme")
		if tn.Status != store.StatusFailed || !strings.Contains(tn.LastError, "identity provider") {
			t.Fatalf("idps=%d: status %q, last error %q", len(idps), tn.Status, tn.LastError)
		}
		if e.pc.Creates["CreateProject"] != 0 || e.kc.Creates["CreateGroup"] != 0 {
			t.Fatalf("idps=%d: writes happened: %v %v", len(idps), e.pc.Creates, e.kc.Creates)
		}
	}
}

func TestCreateDuplicateSlug(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	_, err := e.svc.Create(context.Background(), CreateRequest{Slug: "acme", DisplayName: "again"})
	if !errors.Is(err, store.ErrExists) {
		t.Fatalf("second Create = %v, want store.ErrExists", err)
	}
}

func TestBusyWhileRunning(t *testing.T) {
	e := newEnv(t)
	e.pc.GateCreate = tenanttest.NewGate()
	e.create(t, "acme")
	select {
	case <-e.pc.GateCreate.Started:
	case <-time.After(2 * time.Second):
		t.Fatal("provisioning did not reach CreateProject")
	}
	if err := e.svc.Delete(context.Background(), "acme", false); !errors.Is(err, ErrBusy) {
		t.Fatalf("Delete while provisioning = %v, want ErrBusy", err)
	}
	if err := e.svc.Retry(context.Background(), "acme"); !errors.Is(err, ErrBusy) && !errors.Is(err, ErrState) {
		t.Fatalf("Retry while provisioning = %v, want ErrBusy or ErrState", err)
	}
	close(e.pc.GateCreate.Release)
	e.svc.Wait()
	if tn := e.get(t, "acme"); tn.Status != store.StatusActive {
		t.Fatalf("status after release = %q (%s)", tn.Status, tn.LastError)
	}
}

func TestDeleteReverseOrderAndTolerates404(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	tn := e.get(t, "acme")
	// Someone removed the membership and the SAML group by hand.
	e.pc.Memberships = nil
	e.pc.Groups = nil
	if err := e.svc.Delete(context.Background(), "acme", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	e.svc.Wait()
	if _, err := e.st.GetTenant(context.Background(), "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row after delete: %v", err)
	}
	if len(e.pc.RGs) != 0 {
		t.Fatalf("resource groups left: %+v", e.pc.RGs)
	}
	for _, p := range e.pc.Projects {
		if p.ExtID == tn.ProjectExtID {
			t.Fatal("project left behind")
		}
	}
	if len(e.pc.IdPs[0].SharedWithProjects) != 0 {
		t.Fatalf("idp still shared: %v", e.pc.IdPs[0].SharedWithProjects)
	}
	if len(e.kc.Groups) != 0 {
		t.Fatalf("keycloak group left: %+v", e.kc.Groups)
	}
	if !strings.Contains(e.log.String(), "already absent") {
		t.Fatal("the 404s were not recorded as 'already absent' in the log")
	}
}

func TestDeleteKeepsProjectByDefault(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	tn := e.get(t, "acme")
	if err := e.svc.Delete(context.Background(), "acme", false); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if _, err := e.st.GetTenant(context.Background(), "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row after delete: %v", err)
	}
	found := false
	for _, p := range e.pc.Projects {
		if p.ExtID == tn.ProjectExtID {
			found = true
		}
	}
	if !found {
		t.Fatal("project was deleted although deleteProject was false")
	}
	if len(e.pc.Memberships) != 0 || len(e.pc.Groups) != 0 || len(e.pc.IdPs[0].SharedWithProjects) != 0 {
		t.Fatalf("access objects left: %+v %+v %+v", e.pc.Memberships, e.pc.Groups, e.pc.IdPs[0].SharedWithProjects)
	}
}

func TestDeleteRefusesForeignProjectName(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	tn := e.get(t, "acme")
	tn.ProjectID = "other-name"
	if err := e.st.UpdateTenant(context.Background(), tn); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(context.Background(), "acme", true); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	found := false
	for _, p := range e.pc.Projects {
		if p.ExtID == tn.ProjectExtID {
			found = true
		}
	}
	if !found {
		t.Fatal("a project outside Portico's naming was deleted")
	}
	if !strings.Contains(e.log.String(), "refusing") {
		t.Fatal("the refusal was not logged")
	}
}

func TestDisableEnable(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	ctx := context.Background()
	var ids []string
	for _, em := range []string{"a@acme.test", "b@acme.test"} {
		if _, err := e.svc.CreateUser(ctx, "acme", em, "A", "B"); err != nil {
			t.Fatalf("CreateUser %s: %v", em, err)
		}
	}
	for _, u := range e.kc.Users {
		ids = append(ids, u.ID)
	}
	if len(ids) != 2 {
		t.Fatalf("users = %+v", e.kc.Users)
	}
	if err := e.svc.Disable(ctx, "acme"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	for _, id := range ids {
		if e.kc.UserEnabled(id) {
			t.Fatalf("user %s still enabled", id)
		}
	}
	if e.get(t, "acme").Status != store.StatusDisabled {
		t.Fatal("status not disabled")
	}
	if err := e.svc.Disable(ctx, "acme"); !errors.Is(err, ErrState) {
		t.Fatalf("second Disable = %v, want ErrState", err)
	}
	if err := e.svc.Enable(ctx, "acme"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	for _, id := range ids {
		if !e.kc.UserEnabled(id) {
			t.Fatalf("user %s still disabled", id)
		}
	}
	if e.get(t, "acme").Status != store.StatusActive {
		t.Fatal("status not active")
	}
}

func TestCreateUserTemporaryPasswordShape(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	ctx := context.Background()
	pw, err := e.svc.CreateUser(ctx, "acme", "carol@acme.test", "Carol", "Lab")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if len(pw) != 20 || pw != e.kc.LastTemporaryPassword {
		t.Fatalf("password = %q (len %d), fake got %q", pw, len(pw), e.kc.LastTemporaryPassword)
	}
	if !strings.ContainsAny(pw, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || !strings.ContainsAny(pw, "abcdefghijklmnopqrstuvwxyz") || !strings.ContainsAny(pw, "0123456789") {
		t.Fatalf("password %q lacks a character class", pw)
	}
	tn := e.get(t, "acme")
	if members, _ := e.kc.GroupMembers(ctx, tn.KeycloakGroupID); len(members) != 1 || members[0].Email != "carol@acme.test" {
		t.Fatalf("group members = %+v", members)
	}
	if strings.Contains(e.log.String(), pw) {
		t.Fatal("the temporary password reached the log")
	}
	if _, err := e.svc.CreateUser(ctx, "acme", "carol@acme.test", "Carol", "Lab"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate user = %v, want ErrExists", err)
	}
	tn.Status = store.StatusFailed
	e.st.UpdateTenant(ctx, tn)
	if _, err := e.svc.CreateUser(ctx, "acme", "dave@acme.test", "D", "L"); !errors.Is(err, ErrState) {
		t.Fatalf("CreateUser on failed tenant = %v, want ErrState", err)
	}
}

func TestVisibilityFindings(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	ctx := context.Background()
	v, err := e.svc.Visibility(ctx, "acme")
	if err != nil || !v.OK || len(v.Findings) != 0 {
		t.Fatalf("clean visibility = %+v, %v", v, err)
	}
	tn := e.get(t, "acme")
	e.pc.Memberships = append(e.pc.Memberships, nutanix.RoleMembership{ExtID: "rm-x", IdentityExtID: tn.SAMLGroupExtID, IdentityType: "GROUP", ProjectExtID: "p-other", RoleExtID: "role-pa", ScopeTemplateName: nutanix.ScopeTemplate})
	e.pc.Policies["Leaky_acp"] = []string{tn.SAMLGroupExtID}
	v, err = e.svc.Visibility(ctx, "acme")
	if err != nil || v.OK || len(v.Findings) != 2 {
		t.Fatalf("visibility = %+v, %v; want 2 findings", v, err)
	}
	joined := strings.Join(v.Findings, "\n")
	if !strings.Contains(joined, "p-other") || !strings.Contains(joined, "Leaky_acp") {
		t.Fatalf("findings = %v", v.Findings)
	}
}
