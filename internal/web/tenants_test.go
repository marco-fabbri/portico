package web

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

func (e *env) createTenant(t *testing.T, slug string) {
	t.Helper()
	rec := e.do("POST", "/tenants", e.form("slug", slug, "display_name", strings.ToUpper(slug), "contacts", "ops@"+slug+".test"), true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/tenants/"+slug {
		t.Fatalf("create %s: %d %q %s", slug, rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	e.svc.Wait()
}

func TestTenantListShowsRowsAndPollsWhileCreating(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.createTenant(t, "acme")
	rec := e.do("GET", "/", nil, true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "acme") || !strings.Contains(body, `class="status status-active"`) || !strings.Contains(body, "SITE-A") {
		t.Fatalf("list: %d %s", rec.Code, body)
	}
	if strings.Contains(body, "hx-trigger") {
		t.Fatal("the list polls although nothing is in progress")
	}
	if err := e.st.CreateTenant(context.Background(), &store.Tenant{ID: "beta", Status: store.StatusCreating}); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/", nil, true)
	body = rec.Body.String()
	if !strings.Contains(body, `class="status status-creating"`) || !strings.Contains(body, `hx-trigger="every 10s"`) {
		t.Fatalf("list with a creating tenant: %s", body)
	}
	rec = e.do("GET", "/?partial=rows", nil, true)
	body = rec.Body.String()
	if rec.Code != 200 || strings.Contains(body, "<html") || !strings.Contains(body, "beta") {
		t.Fatalf("rows partial: %d %s", rec.Code, body)
	}
	// HTMX swaps the tbody with outerHTML: the fragment must bring its own
	// tbody with the polling attributes, or polling stops after one tick.
	if !strings.Contains(body, `<tbody id="rows"`) || !strings.Contains(body, `hx-trigger="every 10s"`) {
		t.Fatalf("rows partial lacks the polling tbody: %s", body)
	}
	if rec.Header().Get("HX-Refresh") != "" {
		t.Fatal("HX-Refresh set while a tenant is still in progress")
	}
	if err := e.st.DeleteTenant(context.Background(), "beta"); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", "/?partial=rows", nil, true)
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatal("HX-Refresh not set once nothing is in progress")
	}
}

func TestCreateTenantFormAndRedirect(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.do("GET", "/tenants/new", nil, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="slug"`) || !strings.Contains(rec.Body.String(), `name="_csrf"`) {
		t.Fatalf("new form: %d %s", rec.Code, rec.Body.String())
	}
	e.createTenant(t, "acme")
	rec = e.do("GET", "/tenants/acme", nil, true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "status-active") || strings.Count(body, `<td class="step-ok">`) != 7 {
		t.Fatalf("detail: %d (ok steps %d) %s", rec.Code, strings.Count(body, `<td class="step-ok">`), body)
	}
	if !strings.Contains(body, "Visibility") || !strings.Contains(body, `class="ok"`) {
		t.Fatalf("detail lacks the visibility result: %s", body)
	}
}

func TestCreateTenantInvalidSlugShowsForm(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.do("POST", "/tenants", e.form("slug", "Acme", "display_name", "x"), true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `name="slug"`) || !strings.Contains(rec.Body.String(), "invalid slug") {
		t.Fatalf("invalid slug: %d %s", rec.Code, rec.Body.String())
	}
	if list, _ := e.st.ListTenants(context.Background()); len(list) != 0 {
		t.Fatal("a row was created for an invalid slug")
	}
}

func TestDetailShowsVisibilityFindings(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.createTenant(t, "acme")
	tn, _ := e.st.GetPlacement(context.Background(), "acme", "site-a")
	e.pc.Memberships = append(e.pc.Memberships, nutanix.RoleMembership{ExtID: "rm-x", IdentityExtID: tn.SAMLGroupExtID, IdentityType: "GROUP", ProjectExtID: "p-other", RoleExtID: "role-pa", ScopeTemplateName: nutanix.ScopeTemplate})
	rec := e.do("GET", "/tenants/acme", nil, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "p-other") || !strings.Contains(rec.Body.String(), `class="bad"`) {
		t.Fatalf("detail with a finding: %d %s", rec.Code, rec.Body.String())
	}
}

var rePassword = regexp.MustCompile(`class="password-box">([^<]+)<`)

func TestUserCreatedPageShowsPasswordOnce(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.createTenant(t, "acme")
	rec := e.do("POST", "/tenants/acme/users", e.form("email", "carol@acme.test", "first_name", "Carol", "last_name", "Rossi"), true)
	m := rePassword.FindStringSubmatch(rec.Body.String())
	if rec.Code != 201 || m == nil || len(m[1]) != 20 {
		t.Fatalf("user created page: %d %s", rec.Code, rec.Body.String())
	}
	pw := m[1]
	for _, path := range []string{"/tenants/acme", "/", "/tenants/acme/steps"} {
		if r := e.do("GET", path, nil, true); strings.Contains(r.Body.String(), pw) {
			t.Fatalf("%s shows the temporary password again", path)
		}
	}
	if strings.Contains(e.log.String(), pw) {
		t.Fatal("the temporary password reached the log")
	}
	rec = e.do("POST", "/tenants/acme/users", e.form("email", "carol@acme.test"), true)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("duplicate user: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("POST", "/tenants/acme/users", e.form("email", "not-an-email"), true)
	if rec.Code != 400 {
		t.Fatalf("bad email: %d", rec.Code)
	}
}

func TestDisableEnableRetryDeleteForms(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.createTenant(t, "acme")
	rec := e.do("POST", "/tenants/acme/disable", e.form(), true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/tenants/acme" {
		t.Fatalf("disable: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if tn, _ := e.st.GetTenant(context.Background(), "acme"); tn.Status != store.StatusDisabled {
		t.Fatalf("status after disable = %q", tn.Status)
	}
	rec = e.do("POST", "/tenants/acme/enable", e.form(), true)
	if tn, _ := e.st.GetTenant(context.Background(), "acme"); rec.Code != 303 || tn.Status != store.StatusActive {
		t.Fatalf("enable: %d status %q", rec.Code, tn.Status)
	}
	rec = e.do("POST", "/tenants/acme/provision", e.form(), true)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "not allowed") {
		t.Fatalf("retry on an active tenant: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("POST", "/tenants/acme/delete", e.form(), true)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "still connected to a site") {
		t.Fatalf("delete with a site: %d %s", rec.Code, rec.Body.String())
	}
	if err := e.svc.Disconnect(context.Background(), "acme", e.svc.Sites()[0].ID); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	rec = e.do("POST", "/tenants/acme/delete", e.form(), true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Fatalf("delete: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	e.svc.Wait()
	if _, err := e.st.GetTenant(context.Background(), "acme"); err == nil {
		t.Fatal("row still present after delete")
	}
	for _, p := range e.pc.Projects {
		if p.Name == "portico-acme" {
			t.Fatal("project kept after the disconnect")
		}
	}
}

func TestAllPostsRequireCSRF(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.createTenant(t, "acme")
	before, _ := e.st.GetTenant(context.Background(), "acme")
	for _, path := range []string{"/tenants", "/tenants/acme/users", "/tenants/acme/disable", "/tenants/acme/enable", "/tenants/acme/provision", "/tenants/acme/delete"} {
		rec := e.do("POST", path, url.Values{"slug": {"other"}, "email": {"x@y.test"}}, true)
		if rec.Code != 403 {
			t.Errorf("%s without CSRF: %d", path, rec.Code)
		}
	}
	e.svc.Wait()
	after, _ := e.st.GetTenant(context.Background(), "acme")
	if after.Status != before.Status || e.kc.Creates["CreateUser"] != 0 {
		t.Fatal("a form without CSRF changed state")
	}
	if list, _ := e.st.ListTenants(context.Background()); len(list) != 1 {
		t.Fatal("a tenant was created without CSRF")
	}
}

func TestTenantPagesInSetupModePointToSetup(t *testing.T) {
	e := newEnv(t, true)
	e.web.svc = nil
	e.login(t)
	rec := e.do("GET", "/", nil, true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("home: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = e.do("GET", "/tenants/new", nil, true)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "setup mode") || !strings.Contains(rec.Body.String(), `href="/setup"`) {
		t.Fatalf("tenants/new: %d %s", rec.Code, rec.Body.String())
	}
}

// placementsDown fails the reads of a tenant's sites.
type placementsDown struct{ store.Store }

func (placementsDown) ListPlacements(context.Context, string) ([]store.Placement, error) {
	return nil, errors.New("database is locked")
}

// A store that cannot list a tenant's sites is an error on the page, not
// a tenant on no site with every site offered to add again.
func TestUnreadableSitesAreAnErrorNotNoSites(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.createTenant(t, "acme")
	e.web.st = placementsDown{e.st}
	rec := e.do("GET", "/tenants/acme", nil, true)
	if body := rec.Body.String(); rec.Code != 500 || !strings.Contains(body, "the tenant&#39;s sites could not be read") || strings.Contains(body, `action="/tenants/acme/sites"`) {
		t.Fatalf("detail: %d %s", rec.Code, body)
	}
	if rec := e.do("GET", "/", nil, true); rec.Code != 500 || !strings.Contains(rec.Body.String(), "the tenants&#39; sites could not be read") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
}
