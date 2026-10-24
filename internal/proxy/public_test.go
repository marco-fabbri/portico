package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/config"
	"github.com/marco-fabbri/portico/internal/sites"
)

type publicHarness struct {
	handler  http.Handler
	prism    *[]recorded
	keycloak *[]recorded
	store    *auth.MemoryStore
	logBuf   *bytes.Buffer
}

func newPublicHarness(t *testing.T) *publicHarness {
	t.Helper()
	return newPublicHarnessWith(t, "", nil)
}

// newPublicHarnessWith builds the harness; prismURL, when not empty,
// replaces the recording Prism Central (its requests then go unrecorded
// here), and mutate edits the options before NewPublic.
func newPublicHarnessWith(t *testing.T, prismURL string, mutate func(*PublicOptions)) *publicHarness {
	t.Helper()
	prismUp, prismGot := recordingUpstream(t, false, nil)
	if prismURL == "" {
		prismURL = prismUp.URL
	}
	kcUp, kcGot := recordingUpstream(t, false, nil)
	logBuf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logBuf, nil))
	store := auth.NewMemoryStore(8*time.Hour, 15*time.Minute)
	a := auth.NewAuthenticator(auth.OIDCConfig{
		Issuer: "https://id.test/realms/x", DiscoveryURL: "https://127.0.0.1:1/realms/x",
		ClientID: "portico", ClientSecret: "s", RedirectURL: "https://prism.test/.portico/callback",
	}, store, nil, log)
	// One site, site-a, with tenant acme placed on it: every request of
	// the harness session reaches that site's Prism Central.
	site := &SiteBackend{ID: "site-a", Code: "SITE-A", Upstream: upstreamFor(t, prismURL, "prism.test")}
	opts := PublicOptions{
		PortalHost: "prism.test",
		IDHost:     "id.test",
		Sites: &SiteRouting{
			Backends: NewBackendSet(map[string]*SiteBackend{"site-a": site}),
			Connections: func(context.Context, string) ([]sites.Connection, error) {
				return []sites.Connection{{SiteID: "site-a"}}, nil
			},
			Status:  func(string) sites.Status { return sites.Available },
			SetSite: store.SetSite,
		},
		Keycloak: upstreamFor(t, kcUp.URL, "id.test"),
		Auth:     a,
		Policy:   PrismPolicy{AllowPrefixes: config.DefaultAllowPrefixes},
		Log:      log,
	}
	if mutate != nil {
		mutate(&opts)
	}
	// The landing tests give the site's Prism Central fields on the
	// landing options, as before sites; they belong to the site now.
	if l := opts.Landing; l != nil {
		site.PrismHTTP, site.PrismBase, site.IdPExtID = l.PrismHTTP, l.PrismBase, l.IdPExtID
	}
	h := NewPublic(opts)
	return &publicHarness{handler: h, prism: prismGot, keycloak: kcGot, store: store, logBuf: logBuf}
}

func upstreamFor(t *testing.T, target, host string) http.Handler {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return NewUpstream(UpstreamOptions{Target: u, HostHeader: host, StripCookies: auth.PorticoCookies(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func (h *publicHarness) do(method, rawURL string, hdr map[string]string, session bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, rawURL, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if session {
		// An established session: on its one site, whose cookies it holds.
		h.store.Create(auth.Session{ID: "sid", Subject: "alice", Email: "alice@example.test", Tenant: "acme", Site: "site-a", CookieSite: "site-a", CreatedAt: time.Now(), LastSeen: time.Now()})
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "sid"})
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r)
	return rec
}

func TestPublicBasicWithoutSessionIs403(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://prism.test/api/nutanix/v3/users/me", map[string]string{"Authorization": "Basic YWRtaW46cHc="}, false)
	if rec.Code != 403 || rec.Header().Get("X-Portico-Rule") != "deny-basic-auth" || len(*h.prism) != 0 {
		t.Errorf("got %d rule %q upstream requests %d", rec.Code, rec.Header().Get("X-Portico-Rule"), len(*h.prism))
	}
}

func TestPublicBasicWithSessionIs403(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://prism.test/api/nutanix/v3/users/me", map[string]string{"Authorization": "Basic YWRtaW46cHc="}, true)
	if rec.Code != 403 || len(*h.prism) != 0 {
		t.Errorf("got %d upstream requests %d", rec.Code, len(*h.prism))
	}
}

func TestPublicAPIKeyIs403(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://prism.test/api/multidomain/v4.4.b1/config/projects", map[string]string{"X-ntnx-api-key": "k"}, true)
	if rec.Code != 403 || rec.Header().Get("X-Portico-Rule") != "deny-api-key" {
		t.Errorf("got %d rule %q", rec.Code, rec.Header().Get("X-Portico-Rule"))
	}
}

func TestPublicValidateCredentialsIs403(t *testing.T) {
	h := newPublicHarness(t)
	p := "https://prism.test/api/iam/v4.1.b3/authn/login-requests/abc/$actions/validate-credentials"
	for _, session := range []bool{true, false} {
		rec := h.do("POST", p, map[string]string{"Content-Type": "application/json"}, session)
		if rec.Code != 403 || rec.Header().Get("X-Portico-Rule") != "deny-local-login" {
			t.Errorf("session=%v: got %d rule %q", session, rec.Code, rec.Header().Get("X-Portico-Rule"))
		}
	}
	if len(*h.prism) != 0 {
		t.Errorf("upstream got %d requests", len(*h.prism))
	}
}

func TestPublicLegacyLoginIs403(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("POST", "https://prism.test/PrismGateway/j_spring_security_check", nil, false)
	if rec.Code != 403 || rec.Header().Get("X-Portico-Rule") != "deny-legacy-login" {
		t.Errorf("got %d rule %q", rec.Code, rec.Header().Get("X-Portico-Rule"))
	}
}

func TestPublicNoSessionNavigationRedirects(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://prism.test/console/", map[string]string{"Accept": "text/html"}, false)
	if rec.Code != 302 || rec.Header().Get("Location") != "/.portico/login?rd=%2Fconsole%2F" || len(*h.prism) != 0 {
		t.Errorf("got %d %q upstream %d", rec.Code, rec.Header().Get("Location"), len(*h.prism))
	}
	if !strings.Contains(h.logBuf.String(), `"rule":"no-session"`) {
		t.Errorf("log should name the no-session rule: %s", h.logBuf.String())
	}
}

func TestPublicNoSessionAPIIs401(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("POST", "https://prism.test/api/nutanix/v3/groups", nil, false)
	if rec.Code != 401 || len(*h.prism) != 0 {
		t.Errorf("got %d upstream %d", rec.Code, len(*h.prism))
	}
}

func TestPublicWithSessionProxies(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://prism.test/console/", map[string]string{"Cookie": "NTNX=c"}, true)
	if rec.Code != 200 || rec.Body.String() != "upstream body" || len(*h.prism) != 1 {
		t.Fatalf("got %d %q upstream %d", rec.Code, rec.Body.String(), len(*h.prism))
	}
	r := (*h.prism)[0]
	if r.Host != "prism.test" || strings.Contains(r.Header.Get("Cookie"), "__Host-") {
		t.Errorf("upstream saw host %q cookie %q", r.Host, r.Header.Get("Cookie"))
	}
}

func TestPublicWebSocketRequiresSession(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://prism.test/vnc/vm/x/proxy?proxyClusterUuid=y", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}, false)
	if rec.Code != 401 || len(*h.prism) != 0 {
		t.Errorf("got %d upstream %d", rec.Code, len(*h.prism))
	}
}

func TestPublicNotAllowedPathIs403(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://prism.test/index.html", nil, true)
	if rec.Code != 403 || rec.Header().Get("X-Portico-Rule") != "not-allowed" || len(*h.prism) != 0 {
		t.Errorf("got %d rule %q upstream %d", rec.Code, rec.Header().Get("X-Portico-Rule"), len(*h.prism))
	}
}

func TestPublicPorticoPrefixNeverProxied(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://prism.test/.portico/unknown", nil, true)
	if rec.Code != 404 || len(*h.prism) != 0 {
		t.Errorf("got %d upstream %d", rec.Code, len(*h.prism))
	}
}

func TestPublicIDHostProxiesRealm(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://id.test/realms/portico/protocol/openid-connect/auth?client_id=portico", nil, false)
	if rec.Code != 200 || len(*h.keycloak) != 1 || (*h.keycloak)[0].Host != "id.test" || (*h.keycloak)[0].Query != "client_id=portico" {
		t.Errorf("got %d keycloak %+v", rec.Code, *h.keycloak)
	}
}

func TestPublicIDHostDeniesAdmin(t *testing.T) {
	h := newPublicHarness(t)
	for _, p := range []string{"/admin/master/console/", "/realms/master"} {
		rec := h.do("GET", "https://id.test"+p, nil, false)
		if rec.Code != 403 {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
	if len(*h.keycloak) != 0 {
		t.Errorf("keycloak got %d requests", len(*h.keycloak))
	}
}

func TestPublicUnknownHostIs421(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://other.test/console/", nil, false)
	if rec.Code != 421 {
		t.Errorf("got %d", rec.Code)
	}
}

func TestPublicHostMatchingIgnoresPortAndCase(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://PRISM.test:443/console/", nil, true)
	if rec.Code != 200 || len(*h.prism) != 1 {
		t.Errorf("got %d upstream %d", rec.Code, len(*h.prism))
	}
}

func TestAccessLogNeverContainsSecrets(t *testing.T) {
	h := newPublicHarness(t)
	h.do("GET", "https://prism.test/api/x?code=c0de", map[string]string{
		"Authorization":  "Basic YWRtaW46cHc=",
		"X-ntnx-api-key": "k3y",
		"Cookie":         "__Host-portico_session=sid",
	}, false)
	out := h.logBuf.String()
	if !strings.Contains(out, "deny-basic-auth") || !strings.Contains(out, "/api/x") {
		t.Errorf("log lacks rule or path: %s", out)
	}
	for _, secret := range []string{"YWRtaW46cHc=", "k3y", "sid", "c0de"} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains %q: %s", secret, out)
		}
	}
}

func TestAccessLogRecordsAuthSchemeOnly(t *testing.T) {
	cases := []struct{ header, want, secret string }{
		{"", "none", ""},
		{"Bearer abc.def", "bearer", "abc.def"},
		{"Basic YWJj", "basic", "YWJj"},
	}
	for _, c := range cases {
		h := newPublicHarness(t)
		hdr := map[string]string{}
		if c.header != "" {
			hdr["Authorization"] = c.header
		}
		h.do("GET", "https://prism.test/console/", hdr, false)
		out := h.logBuf.String()
		if !strings.Contains(out, `"auth_scheme":"`+c.want+`"`) {
			t.Errorf("header %q: log lacks auth_scheme %q: %s", c.header, c.want, out)
		}
		if c.secret != "" && strings.Contains(out, c.secret) {
			t.Errorf("header %q: log contains the value: %s", c.header, out)
		}
	}
}
