package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/branding"
)

// landingHarness extends the public harness with a fake Prism Central
// that answers the login-request GET/PUT of write-up 13.
type landingHarness struct {
	*publicHarness
	pc       *[]recorded
	idpErr   error
	putFail  bool
	getFail  bool
	logoData []byte
}

func newLandingHarness(t *testing.T) *landingHarness {
	t.Helper()
	lh := &landingHarness{}
	pcUp, pcGot := recordingUpstream(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/iam/v4.1.b3/authn/login-requests/") && r.Method == "GET":
			if lh.getFail {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"$objectType": "iam.v4.authn.LoginRequest", "$reserved": map[string]any{"$fv": "v4.r1.b3"},
				"clientId": "client-1", "extId": "lr-1", "isUserLoggedIn": false, "loginProviderId": "",
				"redirectUri": "https://prism.test/auth_code?original_request=https://prism.test/", "responseType": "CODE",
				"scope": []string{"OFFLINE_ACCESS", "OPENID", "PROFILE", "EMAIL", "GROUPS"},
			}})
		case strings.HasPrefix(r.URL.Path, "/api/iam/v4.1.b3/authn/login-requests/") && r.Method == "PUT":
			if lh.putFail {
				w.WriteHeader(400)
				w.Write([]byte(`{"data":{"error":"bad"}}`))
				return
			}
			w.Write([]byte(`{"data":{"loginProviderId":"idp-1"}}`))
		default:
			w.Write([]byte("pc"))
		}
	})
	lh.pc = pcGot
	h := newPublicHarnessWith(t, pcUp.URL, func(o *PublicOptions) {
		base, _ := url.Parse(pcUp.URL)
		b := branding.Default()
		b.MSPName = "ACME"
		o.Landing = &LandingOptions{
			Pages: branding.NewPages(func(ctx context.Context) branding.Branding {
				if lh.logoData != nil {
					b.LogoType, b.LogoData = "image/svg+xml", lh.logoData
				}
				return b
			}),
			Logo: func(ctx context.Context) ([]byte, string, bool) {
				return lh.logoData, "image/svg+xml", lh.logoData != nil
			},
			Icons: func(ctx context.Context) branding.Icons {
				return branding.Icons{ICO: []byte("ico-bytes"), Touch: []byte("touch-bytes")}
			},
			PrismHTTP: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
			PrismBase: base,
			PrismHost: "prism.test",
			IdPExtID:  func(ctx context.Context) (string, error) { return "idp-1", lh.idpErr },
			Log:       o.Log,
		}
	})
	lh.publicHarness = h
	return lh
}

func (lh *landingHarness) loginRequests(method string) []recorded {
	var out []recorded
	for _, r := range *lh.pc {
		if r.Method == method && strings.HasPrefix(r.Path, "/api/iam/v4.1.b3/authn/login-requests/") {
			out = append(out, r)
		}
	}
	return out
}

func withIAMCookie(hdr map[string]string) map[string]string {
	if hdr == nil {
		hdr = map[string]string{}
	}
	hdr["Cookie"] = "iam-sessionid=abc; NTNX=x"
	return hdr
}

func TestProvidersPageServesLandingWithSession(t *testing.T) {
	lh := newLandingHarness(t)
	rec := lh.do("GET", "https://prism.test/ui/iam/providers", nil, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ACME") || !strings.Contains(rec.Body.String(), `url=/.portico/saml-start`) {
		t.Fatalf("landing: %d %s", rec.Code, rec.Body.String())
	}
	if len(*lh.pc) != 0 {
		t.Fatalf("the landing page reached the upstream: %+v", *lh.pc)
	}
	if !strings.Contains(lh.logBuf.String(), `"rule":"landing"`) {
		t.Errorf("access log rule: %s", lh.logBuf.String())
	}
}

func TestProvidersPageWithoutSessionRedirectsToLogin(t *testing.T) {
	lh := newLandingHarness(t)
	rec := lh.do("GET", "https://prism.test/ui/iam/providers", map[string]string{"Accept": "text/html"}, false)
	if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/.portico/login") {
		t.Fatalf("without session: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestProvidersPostIsProxied(t *testing.T) {
	lh := newLandingHarness(t)
	rec := lh.do("POST", "https://prism.test/ui/iam/providers", nil, true)
	if rec.Code != 200 || rec.Body.String() != "pc" || len(*lh.pc) != 1 {
		t.Fatalf("POST providers: %d %q upstream %d", rec.Code, rec.Body.String(), len(*lh.pc))
	}
}

// The fallback marker is gone (design sites usability 4.8): a crafted
// marker gets the landing page, never Prism Central's provider page.
func TestProvidersMarkerNoLongerBypassesLanding(t *testing.T) {
	lh := newLandingHarness(t)
	rec := lh.do("GET", "https://prism.test/ui/iam/providers?portico=manual", nil, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/.portico/saml-start") || len(*lh.pc) != 0 {
		t.Fatalf("GET providers with the old marker: %d upstream %d %s", rec.Code, len(*lh.pc), rec.Body.String())
	}
}

func TestSAMLStartHappyPath(t *testing.T) {
	lh := newLandingHarness(t)
	rec := lh.do("GET", "https://prism.test/.portico/saml-start", withIAMCookie(nil), true)
	if rec.Code != 302 || rec.Header().Get("Location") != "/api/iam/authn/initiate-login" {
		t.Fatalf("saml-start: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	gets, puts := lh.loginRequests("GET"), lh.loginRequests("PUT")
	if len(gets) != 1 || len(puts) != 1 || !strings.HasSuffix(gets[0].Path, "/login-requests/abc") || !strings.HasSuffix(puts[0].Path, "/login-requests/abc") {
		t.Fatalf("login-request calls: GET %+v PUT %+v", gets, puts)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(puts[0].Body), &body); err != nil {
		t.Fatalf("PUT body: %v", err)
	}
	if body["loginProviderId"] != "idp-1" || body["clientId"] != "client-1" || body["responseType"] != "CODE" || body["redirectUri"] == "" {
		t.Fatalf("PUT body = %v", body)
	}
	if _, has := body["extId"]; has {
		t.Fatalf("PUT body carries fields the console does not send: %v", body)
	}
	for _, r := range append(gets, puts...) {
		if c := r.Header.Get("Cookie"); c != "iam-sessionid=abc" {
			t.Fatalf("upstream Cookie = %q, want only iam-sessionid", c)
		}
		if r.Header.Get("X-Ntnx-Api-Key") != "" || r.Header.Get("Authorization") != "" {
			t.Fatal("service-account credentials used for the login request")
		}
		if r.Host != "prism.test" {
			t.Fatalf("upstream Host = %q", r.Host)
		}
	}
	if !strings.Contains(lh.logBuf.String(), `"rule":"saml-start"`) {
		t.Errorf("access log rule: %s", lh.logBuf.String())
	}
}

// A failed SAML start shows the branded "responding slowly" page with
// Retry to "/", which restarts the flow (design sites usability 4.8).
func TestSAMLStartFailureShowsSlowPage(t *testing.T) {
	cases := []struct {
		name, step string
		prep       func(lh *landingHarness)
		hdr        map[string]string
	}{
		{"put fails", "put", func(lh *landingHarness) { lh.putFail = true }, withIAMCookie(nil)},
		{"get fails", "get", func(lh *landingHarness) { lh.getFail = true }, withIAMCookie(nil)},
		{"no cookie", "cookie", func(lh *landingHarness) {}, nil},
		{"idp lookup", "idp", func(lh *landingHarness) { lh.idpErr = errors.New("idp not found") }, withIAMCookie(nil)},
	}
	for _, c := range cases {
		lh := newLandingHarness(t)
		c.prep(lh)
		hdr := c.hdr
		if hdr == nil {
			hdr = map[string]string{}
		}
		hdr["Accept"] = "text/html"
		rec := lh.do("GET", "https://prism.test/.portico/saml-start", hdr, true)
		body := rec.Body.String()
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d %q", c.name, rec.Code, rec.Header().Get("Location"))
		}
		if rec.Header().Get("Content-Security-Policy") != "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'" {
			t.Errorf("%s: CSP %q", c.name, rec.Header().Get("Content-Security-Policy"))
		}
		for _, want := range []string{"Site responding slowly", "SITE-A is responding slowly. Please try again in a moment.", `href="/"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: page lacks %q", c.name, want)
			}
		}
		for _, not := range []string{"/ui/iam/providers", "/.portico/saml-start", "Choose another site"} {
			if strings.Contains(body, not) {
				t.Errorf("%s: page has %q", c.name, not)
			}
		}
		if !strings.Contains(lh.logBuf.String(), `"step":"`+c.step+`"`) {
			t.Errorf("%s: the log lacks the step: %s", c.name, lh.logBuf.String())
		}
		if strings.Contains(lh.logBuf.String(), "abc\"") && c.hdr != nil {
			t.Fatalf("%s: cookie value in the log: %s", c.name, lh.logBuf.String())
		}
	}
}

func TestSAMLStartRequiresSession(t *testing.T) {
	lh := newLandingHarness(t)
	rec := lh.do("GET", "https://prism.test/.portico/saml-start", withIAMCookie(map[string]string{"Accept": "text/html"}), false)
	if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/.portico/login") {
		t.Fatalf("without session: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(*lh.pc) != 0 {
		t.Fatal("the upstream was called without a session")
	}
}

func TestLogoServedWithCSP(t *testing.T) {
	lh := newLandingHarness(t)
	lh.logoData = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	rec := lh.do("GET", "https://prism.test/.portico/logo", nil, true)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" || rec.Header().Get("Content-Security-Policy") != "default-src 'none'" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("logo: %d %v", rec.Code, rec.Header())
	}
	rec = lh.do("GET", "https://prism.test/.portico/logo", map[string]string{"Accept": "text/html"}, false)
	if rec.Code != 200 {
		t.Fatalf("logo without session: %d, want 200 (public asset)", rec.Code)
	}
}

func TestLandingDisabledKeepsMilestone1Behaviour(t *testing.T) {
	h := newPublicHarness(t)
	rec := h.do("GET", "https://prism.test/ui/iam/providers", nil, true)
	if rec.Code != 200 || len(*h.prism) != 1 {
		t.Fatalf("without LandingOptions the provider page must be proxied: %d upstream %d", rec.Code, len(*h.prism))
	}
}

// The logo is a public asset: the error pages need it on the identity
// host (Keycloak down) and before any session exists.
func TestLogoIsPublicOnBothHosts(t *testing.T) {
	lh := newLandingHarness(t)
	lh.logoData = []byte("<svg/>")
	for _, u := range []string{"https://prism.test/.portico/logo", "https://id.test/.portico/logo"} {
		rec := lh.do("GET", u, map[string]string{"Accept": "text/html"}, false)
		if rec.Code != 200 || rec.Header().Get("Content-Security-Policy") != "default-src 'none'" {
			t.Fatalf("%s without session: %d %v", u, rec.Code, rec.Header())
		}
	}
	if len(*lh.keycloak) != 0 {
		t.Fatal("the logo request reached Keycloak")
	}
}

// The tab icons are public assets like the logo: the pages before any
// session and the Keycloak login page link them on both hosts.
func TestIconsArePublicOnBothHosts(t *testing.T) {
	lh := newLandingHarness(t)
	for _, host := range []string{"prism.test", "id.test"} {
		for path, want := range map[string][2]string{
			"/.portico/favicon.ico":          {"image/x-icon", "ico-bytes"},
			"/.portico/apple-touch-icon.png": {"image/png", "touch-bytes"},
		} {
			rec := lh.do("GET", "https://"+host+path+"?v=abc", map[string]string{"Accept": "image/*"}, false)
			if rec.Code != 200 || rec.Header().Get("Content-Type") != want[0] || rec.Body.String() != want[1] ||
				rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Content-Security-Policy") != "default-src 'none'" {
				t.Errorf("%s%s without session: %d %v %q", host, path, rec.Code, rec.Header(), rec.Body.String())
			}
		}
	}
	if len(*lh.keycloak) != 0 || len(*lh.pc) != 0 {
		t.Fatal("an icon request reached Keycloak or Prism Central")
	}
}

// Landing options without icons (a caller that does not wire them)
// answer 404, not a panic.
func TestIconsWithoutSourceAnswer404(t *testing.T) {
	lh := newLandingHarness(t)
	lh.publicHarness = newPublicHarnessWith(t, "http://127.0.0.1:1", func(o *PublicOptions) {
		o.Landing = &LandingOptions{Pages: branding.NewPages(func(context.Context) branding.Branding { return branding.Default() }), Log: o.Log}
	})
	rec := lh.do("GET", "https://prism.test/.portico/favicon.ico", nil, false)
	if rec.Code != 404 {
		t.Fatalf("favicon without an icon source: %d", rec.Code)
	}
}

// Review fix 1: a transport error embeds the request URL, which carries
// the login-request id (the cookie value); it must not reach the log.
func TestSAMLStartTransportErrorDoesNotLogCookie(t *testing.T) {
	lh := newLandingHarness(t)
	dead, _ := recordingUpstream(t, false, nil)
	dead.Close()
	base, _ := url.Parse(dead.URL)
	lh.publicHarness = newPublicHarnessWith(t, "", func(o *PublicOptions) {
		o.Landing = &LandingOptions{
			Pages:     branding.NewPages(func(ctx context.Context) branding.Branding { return branding.Default() }),
			Logo:      func(ctx context.Context) ([]byte, string, bool) { return nil, "", false },
			PrismHTTP: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
			PrismBase: base, PrismHost: "prism.test",
			IdPExtID: func(ctx context.Context) (string, error) { return "idp-1", nil },
			Log:      o.Log,
		}
	})
	rec := lh.do("GET", "https://prism.test/.portico/saml-start", map[string]string{"Cookie": "iam-sessionid=secret-cookie-id-9f3a"}, true)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" {
		t.Fatalf("transport error: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Contains(lh.logBuf.String(), "secret-cookie-id-9f3a") {
		t.Fatalf("the login-request id reached the log: %s", lh.logBuf.String())
	}
	if !strings.Contains(lh.logBuf.String(), "sign-in could not start") {
		t.Fatalf("no warning: %s", lh.logBuf.String())
	}
}

// Review fix 3: the console's Sign Out ends on /redirect?logout=…; with
// the landing page that would sign the tenant straight back in, so
// Portico turns it into its own logout, which ends the Keycloak session.
func TestConsoleSignOutGoesToPorticoLogout(t *testing.T) {
	lh := newLandingHarness(t)
	rec := lh.do("GET", "https://prism.test/redirect?logout=https%3A%2F%2Fprism.test%2F", nil, true)
	if rec.Code != 302 || rec.Header().Get("Location") != "/.portico/logout" {
		t.Fatalf("sign out with a session: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(*lh.pc) != 0 {
		t.Fatal("the sign-out redirect reached Prism Central")
	}
	rec = lh.do("GET", "https://prism.test/redirect?logout=x", map[string]string{"Accept": "text/html"}, false)
	if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/.portico/login") {
		t.Fatalf("sign out without a session: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	h := newPublicHarness(t)
	rec = h.do("GET", "https://prism.test/redirect?logout=x", nil, true)
	if rec.Code != 200 || len(*h.prism) != 1 {
		t.Fatalf("without LandingOptions /redirect must be proxied as before: %d upstream %d", rec.Code, len(*h.prism))
	}
}
