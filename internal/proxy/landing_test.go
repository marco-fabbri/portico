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

func TestProvidersPostAndMarkerAreProxied(t *testing.T) {
	lh := newLandingHarness(t)
	rec := lh.do("POST", "https://prism.test/ui/iam/providers", nil, true)
	if rec.Code != 200 || rec.Body.String() != "pc" {
		t.Fatalf("POST providers: %d %q", rec.Code, rec.Body.String())
	}
	rec = lh.do("GET", "https://prism.test/ui/iam/providers?portico=manual", nil, true)
	if rec.Code != 200 || rec.Body.String() != "pc" {
		t.Fatalf("GET providers with the marker: %d %q", rec.Code, rec.Body.String())
	}
	if len(*lh.pc) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(*lh.pc))
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

func TestSAMLStartFallsBackOnce(t *testing.T) {
	cases := []struct {
		name string
		prep func(lh *landingHarness)
		hdr  map[string]string
	}{
		{"put fails", func(lh *landingHarness) { lh.putFail = true }, withIAMCookie(nil)},
		{"get fails", func(lh *landingHarness) { lh.getFail = true }, withIAMCookie(nil)},
		{"no cookie", func(lh *landingHarness) {}, nil},
		{"idp lookup", func(lh *landingHarness) { lh.idpErr = errors.New("idp not found") }, withIAMCookie(nil)},
	}
	for _, c := range cases {
		lh := newLandingHarness(t)
		c.prep(lh)
		rec := lh.do("GET", "https://prism.test/.portico/saml-start", c.hdr, true)
		if rec.Code != 302 || rec.Header().Get("Location") != "/ui/iam/providers?portico=manual" {
			t.Fatalf("%s: %d %q", c.name, rec.Code, rec.Header().Get("Location"))
		}
		rec = lh.do("GET", "https://prism.test"+rec.Header().Get("Location"), nil, true)
		if rec.Code != 200 || rec.Body.String() != "pc" {
			t.Fatalf("%s: the fallback was not proxied: %d %q", c.name, rec.Code, rec.Body.String())
		}
		if strings.Contains(lh.logBuf.String(), "abc\"") && c.hdr != nil {
			// the cookie value must not be logged
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
