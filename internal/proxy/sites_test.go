package proxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/branding"
	"github.com/marco-fabbri/portico/internal/config"
	"github.com/marco-fabbri/portico/internal/sites"
)

// sitesHarness is the public handler over two sites, A and B, each a
// recording Prism Central.
type sitesHarness struct {
	handler    http.Handler
	a, b       *[]recorded
	store      *auth.MemoryStore
	mu         sync.Mutex
	placements map[string][]string // tenant → active sites
	status     map[string]sites.Status
	log        *bytes.Buffer
	routing    *SiteRouting
	auth       *auth.Authenticator
	keycloak   http.Handler
}

func newSitesHarness(t *testing.T) *sitesHarness {
	t.Helper()
	upA, gotA := recordingUpstream(t, false, nil)
	upB, gotB := recordingUpstream(t, false, nil)
	kcUp, _ := recordingUpstream(t, false, nil)
	h := &sitesHarness{a: gotA, b: gotB, store: auth.NewMemoryStore(8*time.Hour, 15*time.Minute), log: &bytes.Buffer{},
		placements: map[string][]string{"acme": {"site-a", "site-b"}, "solo": {"site-a"}},
		status:     map[string]sites.Status{"site-a": sites.Available, "site-b": sites.Available}}
	log := slog.New(slog.NewJSONHandler(h.log, nil))
	a := auth.NewAuthenticator(auth.OIDCConfig{
		Issuer: "https://id.test/realms/x", DiscoveryURL: "https://127.0.0.1:1/realms/x",
		ClientID: "portico", ClientSecret: "s", RedirectURL: "https://prism.test/.portico/callback",
	}, h.store, nil, log)
	pages := branding.NewPages(func(context.Context) branding.Branding { return branding.Default() })
	routing := &SiteRouting{
		Backends: map[string]*SiteBackend{
			"site-a": {ID: "site-a", Code: "AZ-IT-MI1", Name: "Milan", Order: 1, Upstream: upstreamFor(t, upA.URL, "prism.test")},
			"site-b": {ID: "site-b", Code: "AZ-US-NYC1", Name: "New York", Order: 2, Upstream: upstreamFor(t, upB.URL, "prism.test")},
		},
		Placements: func(ctx context.Context, tenant string) ([]string, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.placements[tenant], nil
		},
		Status: func(id string) sites.Status {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.status[id]
		},
		SetSite:   h.store.SetSite,
		LeaveSite: h.store.LeaveSite,
		Pages:     pages,
	}
	h.routing, h.auth, h.keycloak = routing, a, upstreamFor(t, kcUp.URL, "id.test")
	h.handler = NewPublic(PublicOptions{
		PrismHost: "prism.test", IDHost: "id.test",
		Sites: routing, Keycloak: h.keycloak, Auth: a,
		Policy: PrismPolicy{AllowPrefixes: config.DefaultAllowPrefixes}, Log: log,
	})
	return h
}

// withLanding rebuilds the handler with the landing page on, which is
// what intercepts the console's Sign Out.
func (h *sitesHarness) withLanding() *sitesHarness {
	log := slog.New(slog.NewJSONHandler(h.log, nil))
	h.handler = NewPublic(PublicOptions{
		PrismHost: "prism.test", IDHost: "id.test",
		Sites: h.routing, Keycloak: h.keycloak, Auth: h.auth,
		Policy: PrismPolicy{AllowPrefixes: config.DefaultAllowPrefixes}, Log: log,
		Landing: &LandingOptions{Pages: h.routing.Pages, Logo: func(context.Context) ([]byte, string, bool) { return nil, "", false }, PrismHost: "prism.test"},
	})
	return h
}

// session creates a tenant session and returns its id.
func (h *sitesHarness) session(id, tenant, site string) {
	now := time.Now()
	h.store.Create(auth.Session{ID: id, Subject: "u-" + id, Email: id + "@acme.example", Tenant: tenant, Site: site, CreatedAt: now, LastSeen: now})
}

func (h *sitesHarness) do(method, path, sid string, hdr map[string]string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, "https://prism.test"+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if sid != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sid})
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r)
	return rec
}

var nav = map[string]string{"Accept": "text/html"}

func (h *sitesHarness) site(sid string) string {
	s, _ := h.store.Get(sid, time.Now())
	return s.Site
}

func TestSitePageListsActivePlacementsOnly(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "acme", "")
	h.status["site-b"] = sites.Maintenance
	rec := h.do("GET", "/.portico/sites", "s1", nav, nil)
	body := rec.Body.String()
	for _, want := range []string{"AZ-IT-MI1", "Milan", "AZ-US-NYC1", "maintenance", `name="site" value="site-a"`, "s1@acme.example", `href="/.portico/logout"`} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("site page lacks %q: %d\n%s", want, rec.Code, body)
		}
	}
	if strings.Contains(body, `value="site-b"`) && !strings.Contains(body, `value="site-b" disabled`) {
		t.Fatal("a site in maintenance is selectable")
	}
	if strings.Index(body, "AZ-IT-MI1") > strings.Index(body, "AZ-US-NYC1") {
		t.Fatal("tiles not in site order")
	}
	h.placements["acme"] = []string{"site-a"}
	if rec := h.do("GET", "/.portico/sites", "s1", nav, nil); rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Fatalf("one placement: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(*h.a)+len(*h.b) != 0 {
		t.Fatal("the site page reached a Prism Central")
	}
}

// Review Focus 1: a tenant can never choose a site where it has no
// active placement, nor one that is not available.
func TestSelectRefusesForeignSite(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "solo", "")
	for _, c := range []struct{ name, site string }{{"no placement", "site-b"}, {"unknown", "site-z"}, {"empty", ""}} {
		rec := h.do("POST", "/.portico/sites/select", "s1", nil, url.Values{"site": {c.site}})
		if rec.Code != 403 || h.site("s1") != "" {
			t.Fatalf("%s: %d site=%q", c.name, rec.Code, h.site("s1"))
		}
	}
	h.session("s2", "acme", "")
	h.status["site-b"] = sites.Unreachable
	// A site of the tenant that is not available is refused with its
	// unavailable page (the tile is greyed out; this is a crafted POST).
	if rec := h.do("POST", "/.portico/sites/select", "s2", nil, url.Values{"site": {"site-b"}}); rec.Code != 503 || h.site("s2") != "" {
		t.Fatalf("unreachable site selected: %d", rec.Code)
	}
	if len(*h.a)+len(*h.b) != 0 {
		t.Fatal("a refused selection reached a Prism Central")
	}
}

// A site that becomes unreachable between the site page and the click:
// the browser's form POST gets the branded page, not the JSON refusal
// (write-up 23); a script's POST still gets JSON.
func TestSelectUnreachableSiteAnswersTheBrowserWithAPage(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "acme", "")
	h.status["site-b"] = sites.Unreachable
	rec := h.do("POST", "/.portico/sites/select", "s1", nav, url.Values{"site": {"site-b"}})
	if rec.Code != 503 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "is unreachable right now") {
		t.Fatalf("browser POST: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	rec = h.do("POST", "/.portico/sites/select", "s1", map[string]string{"Accept": "application/json"}, url.Values{"site": {"site-b"}})
	if rec.Code != 503 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("script POST: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestSelectRefusesCrossSite(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "acme", "")
	rec := h.do("POST", "/.portico/sites/select", "s1", map[string]string{"Sec-Fetch-Site": "cross-site"}, url.Values{"site": {"site-a"}})
	if rec.Code != 403 || h.site("s1") != "" {
		t.Fatalf("cross-site select: %d", rec.Code)
	}
	rec = h.do("POST", "/.portico/sites/select", "s1", map[string]string{"Sec-Fetch-Site": "same-origin"}, url.Values{"site": {"site-a"}})
	if rec.Code != 303 || h.site("s1") != "site-a" {
		t.Fatalf("same-origin select: %d", rec.Code)
	}
}

func TestSelectThenProxyGoesToChosenSite(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "acme", "")
	if rec := h.do("GET", "/console/", "s1", nav, nil); rec.Code != 303 || rec.Header().Get("Location") != "/.portico/sites" {
		t.Fatalf("no site yet: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := h.do("GET", "/api/nutanix/v3/vms", "s1", nil, nil); rec.Code != 403 || rec.Header().Get("X-Portico-Rule") != "site-required" {
		t.Fatalf("API call without a site: %d %q", rec.Code, rec.Header().Get("X-Portico-Rule"))
	}
	rec := h.do("POST", "/.portico/sites/select", "s1", nil, url.Values{"site": {"site-b"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Fatalf("select: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := h.do("GET", "/console/", "s1", nav, nil); rec.Code != 200 || len(*h.b) != 1 || len(*h.a) != 0 {
		t.Fatalf("after select: %d a=%d b=%d", rec.Code, len(*h.a), len(*h.b))
	}
	h.do("POST", "/.portico/sites/select", "s1", nil, url.Values{"site": {"site-a"}})
	h.do("GET", "/console/", "s1", nav, nil)
	if len(*h.a) != 1 || len(*h.b) != 1 {
		t.Fatalf("after switching back: a=%d b=%d", len(*h.a), len(*h.b))
	}
}

func TestSingleSiteAutoSelected(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "solo", "")
	if rec := h.do("GET", "/console/", "s1", nav, nil); rec.Code != 200 || len(*h.a) != 1 || h.site("s1") != "site-a" {
		t.Fatalf("one site: %d a=%d site=%q", rec.Code, len(*h.a), h.site("s1"))
	}
}

func TestSingleSiteInMaintenanceShowsPage(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "solo", "")
	h.status["site-a"] = sites.Maintenance
	rec := h.do("GET", "/console/", "s1", nav, nil)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "AZ-IT-MI1 is in maintenance") || len(*h.a) != 0 {
		t.Fatalf("maintenance page: %d %s", rec.Code, rec.Body.String())
	}
	h.status["site-a"] = sites.Unreachable
	if rec := h.do("GET", "/api/nutanix/v3/vms", "s1", nil, nil); rec.Code != 503 || !strings.Contains(rec.Body.String(), "unreachable") {
		t.Fatalf("unreachable JSON: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionOnSiteInMaintenanceKeepsWorking(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "acme", "site-a")
	h.status["site-a"] = sites.Maintenance
	if rec := h.do("GET", "/console/", "s1", nav, nil); rec.Code != 200 || len(*h.a) != 1 {
		t.Fatalf("session already on the site: %d a=%d", rec.Code, len(*h.a))
	}
}

// Review Focus 2: a site the tenant lost (placement removed, site
// forgotten, session disconnected) is not proxied any more.
func TestRemovedPlacementStopsRouting(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "acme", "site-b")
	h.placements["acme"] = []string{"site-a"}
	if rec := h.do("GET", "/api/nutanix/v3/vms", "s1", nil, nil); rec.Code != 200 || len(*h.b) != 0 || len(*h.a) != 1 {
		t.Fatalf("lost site with one left: %d a=%d b=%d", rec.Code, len(*h.a), len(*h.b))
	}
	// A forgotten site (no backend any more) with two sites left.
	h.placements["acme"] = []string{"site-a", "site-b"}
	h.session("s2", "acme", "site-x")
	if rec := h.do("GET", "/console/", "s2", nav, nil); rec.Code != 303 || rec.Header().Get("Location") != "/.portico/sites" || len(*h.b) != 0 || len(*h.a) != 1 {
		t.Fatalf("lost site with several left: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	h.placements["acme"] = []string{"site-a", "site-b"}
	h.session("s3", "acme", "site-b")
	h.store.ClearSite("site-b")
	if rec := h.do("GET", "/api/nutanix/v3/vms", "s3", nil, nil); rec.Code != 403 || len(*h.b) != 0 {
		t.Fatalf("disconnected session: %d b=%d", rec.Code, len(*h.b))
	}
}

func TestNoTenantShowsNoSitePage(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "", "")
	h.session("s2", "nobody", "")
	for _, sid := range []string{"s1", "s2"} {
		rec := h.do("GET", "/console/", sid, nav, nil)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "No site is available for your account") {
			t.Fatalf("%s: %d %s", sid, rec.Code, rec.Body.String())
		}
		if rec := h.do("GET", "/.portico/sites", sid, nav, nil); rec.Code != 403 {
			t.Fatalf("%s site page: %d", sid, rec.Code)
		}
	}
	if len(*h.a)+len(*h.b) != 0 {
		t.Fatal("a session without a tenant reached a Prism Central")
	}
}

func TestDenyListBeforeSiteCheck(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "acme", "site-a")
	rec := h.do("GET", "/api/nutanix/v3/vms", "s1", map[string]string{"Authorization": "Basic YWRtaW46cHc="}, nil)
	if rec.Code != 403 || rec.Header().Get("X-Portico-Rule") != "deny-basic-auth" || len(*h.a) != 0 {
		t.Fatalf("basic auth on a site: %d %q", rec.Code, rec.Header().Get("X-Portico-Rule"))
	}
}

// The landing page and the SAML start use the session's site: its Prism
// Central for the login request, its identity provider.
func TestLandingUsesSiteIdP(t *testing.T) {
	type pc struct {
		srv  *httptest.Server
		puts []string
	}
	mk := func(idp string) *pc {
		p := &pc{}
		p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/api/iam/v4.1.b3/authn/login-requests/") && r.Method == "GET":
				w.Write([]byte(`{"data":{"clientId":"c","responseType":"CODE","scope":["OPENID"],"redirectUri":"https://prism.test/auth_code"}}`))
			case strings.HasPrefix(r.URL.Path, "/api/iam/v4.1.b3/authn/login-requests/") && r.Method == "PUT":
				p.puts = append(p.puts, idp)
				w.Write([]byte(`{"data":{}}`))
			default:
				w.Write([]byte("pc " + idp))
			}
		}))
		t.Cleanup(p.srv.Close)
		return p
	}
	pa, pb := mk("idp-a"), mk("idp-b")
	h := newSitesHarness(t)
	routing := &SiteRouting{Backends: map[string]*SiteBackend{}, SetSite: h.store.SetSite,
		Placements: func(context.Context, string) ([]string, error) { return []string{"site-a", "site-b"}, nil },
		Status:     func(string) sites.Status { return sites.Available }}
	for id, p := range map[string]*pc{"site-a": pa, "site-b": pb} {
		base, _ := url.Parse(p.srv.URL)
		idp := map[string]string{"site-a": "idp-a", "site-b": "idp-b"}[id]
		routing.Backends[id] = &SiteBackend{ID: id, Code: strings.ToUpper(id), Upstream: upstreamFor(t, p.srv.URL, "prism.test"),
			PrismHTTP: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
			PrismBase: base, IdPExtID: func(context.Context) (string, error) { return idp, nil }}
	}
	pages := branding.NewPages(func(context.Context) branding.Branding { return branding.Default() })
	routing.Pages = pages
	a := auth.NewAuthenticator(auth.OIDCConfig{Issuer: "https://id.test/realms/x", DiscoveryURL: "https://127.0.0.1:1/realms/x", ClientID: "portico", ClientSecret: "s", RedirectURL: "https://prism.test/.portico/callback"}, h.store, nil, nil)
	h.handler = NewPublic(PublicOptions{PrismHost: "prism.test", IDHost: "id.test", Sites: routing, Keycloak: http.NotFoundHandler(), Auth: a,
		Policy:  PrismPolicy{AllowPrefixes: config.DefaultAllowPrefixes},
		Landing: &LandingOptions{Pages: pages, Logo: func(context.Context) ([]byte, string, bool) { return nil, "", false }, PrismHost: "prism.test"}})
	h.session("s1", "acme", "site-b")
	rec := h.do("GET", "/ui/iam/providers", "s1", nav, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `href="/.portico/sites">Switch site`) {
		t.Fatalf("landing on site-b: %d %s", rec.Code, rec.Body.String())
	}
	rec = h.do("GET", "/.portico/saml-start", "s1", map[string]string{"Cookie": auth.SessionCookie + "=s1; iam-sessionid=abc"}, nil)
	if rec.Code != 302 || rec.Header().Get("Location") != "/api/iam/authn/initiate-login" || len(pb.puts) != 1 || pb.puts[0] != "idp-b" || len(pa.puts) != 0 {
		t.Fatalf("saml-start on site-b: %d %q a=%v b=%v", rec.Code, rec.Header().Get("Location"), pa.puts, pb.puts)
	}
}

// signOut is the console's Sign Out tail with live Prism Central cookies of
// the session's site and a stash of site-b.
func (h *sitesHarness) signOut(sid string) *httptest.ResponseRecorder {
	return h.do("GET", "/redirect?logout=https%3A%2F%2Fprism.test%2F", sid, map[string]string{
		"Accept": "text/html",
		"Cookie": "NTNX_SESSION_ID=x; " + StashName("site-b", "NTNX_SESSION_ID") + "=y",
	}, nil)
}

// setCookie returns the Set-Cookie header for name, "" when none.
func setCookie(rec *httptest.ResponseRecorder, name string) string {
	for _, c := range rec.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(c, name+"=") {
			return c
		}
	}
	return ""
}

func TestSignOutWithSeveralSitesReturnsToSitePage(t *testing.T) {
	h := newSitesHarness(t).withLanding()
	h.session("s1", "acme", "site-a")
	h.store.SetSite("s1", "site-a", time.Now())
	rec := h.signOut("s1")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/.portico/sites" {
		t.Fatalf("sign-out with two sites: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := setCookie(rec, "NTNX_SESSION_ID"); !strings.Contains(c, "Max-Age=0") {
		t.Errorf("the site's live cookie is not expired: %q", c)
	}
	if c := setCookie(rec, StashName("site-b", "NTNX_SESSION_ID")); c != "" {
		t.Errorf("another site's stash was touched: %q", c)
	}
	if c := setCookie(rec, auth.SessionCookie); c != "" {
		t.Errorf("the Portico session cookie was touched: %q", c)
	}
	s, ok := h.store.Get("s1", time.Now())
	if !ok || s.Site != "" || s.CookieSite != "site-a" {
		t.Fatalf("session after a site sign-out: ok=%v site=%q cookieSite=%q", ok, s.Site, s.CookieSite)
	}
	if !strings.Contains(h.log.String(), `"sign-out-site"`) {
		t.Errorf("access log lacks the rule: %s", h.log.String())
	}
	if len(*h.a)+len(*h.b) != 0 {
		t.Error("the sign-out reached a Prism Central")
	}
}

func TestSignOutWithoutChosenSiteStillGoesToSitePage(t *testing.T) {
	h := newSitesHarness(t).withLanding()
	h.session("s1", "acme", "")
	rec := h.signOut("s1")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/.portico/sites" {
		t.Fatalf("sign-out without a chosen site: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := setCookie(rec, "NTNX_SESSION_ID"); !strings.Contains(c, "Max-Age=0") {
		t.Errorf("the live Prism Central cookie is not expired: %q", c)
	}
	if _, ok := h.store.Get("s1", time.Now()); !ok {
		t.Fatal("the Portico session ended")
	}
}

func TestSignOutWithOneSiteSignsOutOfEverything(t *testing.T) {
	h := newSitesHarness(t).withLanding()
	h.session("s1", "solo", "site-a")
	rec := h.signOut("s1")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/.portico/logout" {
		t.Fatalf("sign-out with one site: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSignOutAfterOtherPlacementsRemoved(t *testing.T) {
	h := newSitesHarness(t).withLanding()
	h.session("s1", "acme", "site-a")
	h.mu.Lock()
	h.placements["acme"] = []string{"site-a"}
	h.mu.Unlock()
	rec := h.signOut("s1")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/.portico/logout" {
		t.Fatalf("sign-out with one site left: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// After a site sign-out the other site's stash still comes back on the
// next choice: no new password there (Review Focus 1).
func TestSignOutThenOtherSiteRestoresItsStash(t *testing.T) {
	h := newSitesHarness(t).withLanding()
	h.session("s1", "acme", "site-a")
	h.store.SetSite("s1", "site-a", time.Now())
	h.signOut("s1")
	rec := h.do("POST", "/.portico/sites/select", "s1", map[string]string{
		"Accept": "text/html", "Sec-Fetch-Site": "same-origin",
		"Cookie": StashName("site-b", "NTNX_SESSION_ID") + "=y",
	}, url.Values{"site": {"site-b"}})
	if rec.Code != http.StatusSeeOther || h.site("s1") != "site-b" {
		t.Fatalf("choosing site-b after the sign-out: %d site=%q", rec.Code, h.site("s1"))
	}
	if c := setCookie(rec, "NTNX_SESSION_ID"); !strings.HasPrefix(c, "NTNX_SESSION_ID=y") {
		t.Fatalf("site-b's cookie was not restored: %q", c)
	}
}

func TestSAMLStartFailureWithSeveralSitesLinksSitePage(t *testing.T) {
	h := newSitesHarness(t)
	h.routing.Backends["site-a"].PrismHTTP = &http.Client{}
	h.routing.Backends["site-a"].PrismBase, _ = url.Parse("https://127.0.0.1:1")
	h.routing.Backends["site-a"].IdPExtID = func(context.Context) (string, error) { return "", errors.New("slow") }
	h.withLanding()
	h.session("s1", "acme", "site-a")
	rec := h.do("GET", "/.portico/saml-start", "s1", map[string]string{"Accept": "text/html", "Cookie": "iam-sessionid=abc"}, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(body, `href="/.portico/sites"`) || !strings.Contains(body, "Choose another site") || !strings.Contains(body, "AZ-IT-MI1 is responding slowly.") {
		t.Fatalf("slow page with two sites: %d %s", rec.Code, body)
	}
}

// The console's idle sign-out with several sites lands on the site page;
// left open, the page signs out of everything before the Portico session
// would expire, as the idle sign-out does with one site (Keycloak too).
func TestSitePageSignsOutOfEverythingWhenIdle(t *testing.T) {
	h := newSitesHarness(t)
	h.routing.SignOutAfter = 14 * time.Minute
	h.session("s1", "acme", "")
	rec := h.do("GET", "/.portico/sites", "s1", nav, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `content="840;url=/.portico/logout"`) {
		t.Fatalf("site page without the idle sign-out: %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestSitePageSignOutDelay(t *testing.T) {
	for idle, want := range map[time.Duration]time.Duration{
		15 * time.Minute: 14 * time.Minute,
		2 * time.Minute:  time.Minute,
		90 * time.Second: 45 * time.Second,
	} {
		if got := SitePageSignOut(idle); got != want {
			t.Errorf("SitePageSignOut(%v) = %v, want %v", idle, got, want)
		}
	}
}
