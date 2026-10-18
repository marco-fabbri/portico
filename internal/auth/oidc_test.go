package auth

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func insecureClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func testConfig(idp *fakeIDP) OIDCConfig {
	return OIDCConfig{
		Issuer:       idp.issuer(),
		DiscoveryURL: idp.URL(),
		ClientID:     idp.clientID,
		ClientSecret: idp.clientSecret,
		RedirectURL:  "https://prism.test/.portico/callback",
		CookieMaxAge: 8 * time.Hour,
	}
}

func newTestAuth(t *testing.T, idp *fakeIDP) (*Authenticator, *MemoryStore) {
	t.Helper()
	st := NewMemoryStore(8*time.Hour, 15*time.Minute)
	a := NewAuthenticator(testConfig(idp), st, insecureClient(), slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	return a, st
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func TestDiscoverRejectsIssuerMismatch(t *testing.T) {
	idp := newFakeIDP(t)
	idp.issuerOverride = "https://other.test/realms/x"
	cfg := testConfig(idp)
	cfg.Issuer = idp.URL()
	_, err := Discover(context.Background(), insecureClient(), cfg)
	if err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("want an issuer error, got %v", err)
	}
}

func TestDiscoverUsesDocumentEndpoints(t *testing.T) {
	idp := newFakeIDP(t)
	p, err := Discover(context.Background(), insecureClient(), testConfig(idp))
	if err != nil {
		t.Fatal(err)
	}
	if p.Endpoint().AuthURL != idp.URL()+"/auth" || p.EndSessionURL != idp.URL()+"/logout" {
		t.Errorf("endpoints = %+v, end_session %q", p.Endpoint(), p.EndSessionURL)
	}
}

func TestLoginRedirectsToAuthorizationEndpoint(t *testing.T) {
	idp := newFakeIDP(t)
	a, _ := newTestAuth(t, idp)
	rec := httptest.NewRecorder()
	a.Login().ServeHTTP(rec, httptest.NewRequest("GET", "https://prism.test/.portico/login?rd=/console/", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || "https://"+loc.Host != idp.URL() {
		t.Fatalf("Location %q, want host of %s", rec.Header().Get("Location"), idp.URL())
	}
	q := loc.Query()
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "openid email" || q.Get("client_id") != "portico" {
		t.Errorf("query = %v", q)
	}
	var oc *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == OIDCCookie {
			oc = c
		}
	}
	if oc == nil || !oc.Secure || !oc.HttpOnly || oc.SameSite != http.SameSiteLaxMode || oc.Path != "/" {
		t.Errorf("OIDC cookie = %+v", oc)
	}
}

func TestLoginUnavailableIdP(t *testing.T) {
	idp := newFakeIDP(t)
	idp.down.Store(true)
	a, _ := newTestAuth(t, idp)
	rec := httptest.NewRecorder()
	a.Login().ServeHTTP(rec, httptest.NewRequest("GET", "https://prism.test/.portico/login", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "identity provider unavailable") {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
}

// porticoServer serves the auth handlers plus a protected /console/ at
// https://prism.test through a client transport that dials the test server
// for that host name.
func porticoServer(t *testing.T, a *Authenticator) (*http.Client, *[]string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/.portico/login", a.Login())
	mux.Handle("/.portico/callback", a.Callback())
	mux.Handle("/.portico/logout", a.Logout())
	mux.Handle("/", a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, _ := SessionFromContext(r.Context())
		w.Write([]byte("console for " + s.Email))
	})))
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	visited := &[]string{}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == "prism.test:443" {
				addr = srv.Listener.Addr().String()
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	client := &http.Client{Transport: tr, Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		*visited = append(*visited, req.URL.String())
		return nil
	}}
	return client, visited
}

func callbackURL(visited []string) string {
	for _, u := range visited {
		if strings.Contains(u, "/.portico/callback?") {
			return u
		}
	}
	return ""
}

func TestCallbackRoundTripCreatesSession(t *testing.T) {
	idp := newFakeIDP(t)
	a, st := newTestAuth(t, idp)
	client, visited := porticoServer(t, a)
	resp, err := client.Get("https://prism.test/.portico/login?rd=/console/")
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 100)
	n, _ := resp.Body.Read(body)
	if resp.StatusCode != 200 || resp.Request.URL.Path != "/console/" || string(body[:n]) != "console for alice@lab.portico.test" {
		t.Fatalf("final: %d %s %q (visited %v)", resp.StatusCode, resp.Request.URL, body[:n], *visited)
	}
	u, _ := url.Parse("https://prism.test/")
	var sid string
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == SessionCookie {
			sid = c.Value
		}
	}
	if sid == "" {
		t.Fatal("no session cookie in the jar")
	}
	s, ok := st.Get(sid, time.Now())
	if !ok || s.Subject != "alice" || s.Email != "alice@lab.portico.test" {
		t.Errorf("session = %+v ok=%v", s, ok)
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	idp := newFakeIDP(t)
	a, st := newTestAuth(t, idp)
	client, visited := porticoServer(t, a)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		*visited = append(*visited, req.URL.String())
		if strings.Contains(req.URL.Path, "/.portico/callback") {
			return http.ErrUseLastResponse
		}
		return nil
	}
	resp, _ := client.Get("https://prism.test/.portico/login?rd=/console/")
	resp.Body.Close()
	cb, _ := url.Parse(callbackURL(*visited))
	q := cb.Query()
	q.Set("state", "forged")
	cb.RawQuery = q.Encode()
	resp, err := client.Get(cb.String())
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	if n := st.Sweep(time.Now()); len(st.sessions) != 0 || n != 0 {
		t.Error("a session was created")
	}
}

// A replayed callback (its state cookie is gone after the first use)
// never exchanges the code again or creates a session: it restarts from
// /, where the existing session answers.
func TestCallbackRejectsReplay(t *testing.T) {
	idp := newFakeIDP(t)
	a, st := newTestAuth(t, idp)
	client, visited := porticoServer(t, a)
	resp, err := client.Get("https://prism.test/.portico/login?rd=/console/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("first login: %v %v", err, resp)
	}
	if len(st.sessions) != 1 {
		t.Fatalf("%d sessions after the login, want 1", len(st.sessions))
	}
	replay := callbackURL(*visited)
	*visited = (*visited)[:0]
	resp, err = client.Get(replay)
	if err != nil {
		t.Fatal(err)
	}
	// The restart ran a fresh login (a new code from the provider); the
	// replayed code was never exchanged again (the fake provider would
	// have refused it: codes are single-use).
	if fresh := callbackURL(*visited); resp.StatusCode != 200 || fresh == "" || fresh == replay {
		t.Fatalf("replayed callback: status %d, visited %v", resp.StatusCode, *visited)
	}
}

func TestSafeReturnPath(t *testing.T) {
	cases := map[string]string{
		"/console/":           "/console/",
		"/console/?x=1":       "/console/?x=1",
		"https://evil.test/x": "/",
		"//evil.test":         "/",
		"console":             "/",
		"/.portico/callback":  "/",
		"":                    "/",
		"/x\\evil":            "/",
	}
	for in, want := range cases {
		if got := SafeReturnPath(in); got != want {
			t.Errorf("SafeReturnPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequireRedirectsNavigations(t *testing.T) {
	idp := newFakeIDP(t)
	a, _ := newTestAuth(t, idp)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://prism.test/console/", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	a.Require(http.NotFoundHandler()).ServeHTTP(rec, r)
	if rec.Code != 302 || rec.Header().Get("Location") != "/.portico/login?rd=%2Fconsole%2F" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRequireRejectsAPICalls(t *testing.T) {
	idp := newFakeIDP(t)
	a, _ := newTestAuth(t, idp)
	for _, c := range []struct{ method, accept string }{{"POST", ""}, {"GET", "application/json"}} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(c.method, "https://prism.test/api/x", nil)
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		a.Require(http.NotFoundHandler()).ServeHTTP(rec, r)
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), `"error"`) || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s %q: got %d %q", c.method, c.accept, rec.Code, rec.Body.String())
		}
	}
}

func TestRequirePassesWithSession(t *testing.T) {
	idp := newFakeIDP(t)
	a, st := newTestAuth(t, idp)
	st.Create(Session{ID: "sid", Subject: "alice", Email: "alice@lab.portico.test", CreatedAt: time.Now(), LastSeen: time.Now()})
	called := false
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://prism.test/console/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "sid"})
	a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if s, ok := SessionFromContext(r.Context()); !ok || s.Email != "alice@lab.portico.test" {
			t.Errorf("session in context = %+v ok=%v", s, ok)
		}
	})).ServeHTTP(rec, r)
	if !called {
		t.Fatal("next handler not called")
	}
}

func TestLogoutClearsSessionAndRedirects(t *testing.T) {
	idp := newFakeIDP(t)
	a, st := newTestAuth(t, idp)
	st.Create(Session{ID: "sid", CreatedAt: time.Now(), LastSeen: time.Now()})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://prism.test/.portico/logout", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "sid"})
	a.Logout().ServeHTTP(rec, r)
	if _, ok := st.Get("sid", time.Now()); ok {
		t.Error("session still in the store")
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie && c.MaxAge == -1 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("session cookie not cleared")
	}
	loc := rec.Header().Get("Location")
	if rec.Code != 302 || !strings.HasPrefix(loc, idp.URL()+"/logout?") || !strings.Contains(loc, "post_logout_redirect_uri=https%3A%2F%2Fprism.test%2F") {
		t.Errorf("got %d %q", rec.Code, loc)
	}
}

func TestLogoutExpiresUpstreamCookies(t *testing.T) {
	idp := newFakeIDP(t)
	a, _ := newTestAuth(t, idp)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://prism.test/.portico/logout", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "sid"})
	r.AddCookie(&http.Cookie{Name: "NTNX_IGW_SESSION", Value: "x"})
	r.AddCookie(&http.Cookie{Name: "iam-sessionid", Value: "y"})
	a.Logout().ServeHTTP(rec, r)
	expired := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge == -1 && c.Path == "/" && c.Value == "" {
			expired[c.Name] = true
		}
	}
	for _, name := range []string{SessionCookie, "NTNX_IGW_SESSION", "iam-sessionid"} {
		if !expired[name] {
			t.Errorf("cookie %s not expired on logout: %v", name, rec.Result().Cookies())
		}
	}
}

// Milestone 2c: browsers get a branded page when the identity provider is
// down; API-style callers keep the text answer.
func TestLoginIdPDownRendersErrorPageForBrowsers(t *testing.T) {
	idp := newFakeIDP(t)
	idp.down.Store(true)
	cfg := testConfig(idp)
	cfg.ErrorPage = func(w http.ResponseWriter, r *http.Request, status int, title, text, retry string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		w.Write([]byte("<html>BRANDED " + title + " retry=" + retry + "</html>"))
	}
	a := NewAuthenticator(cfg, NewMemoryStore(8*time.Hour, 15*time.Minute), insecureClient(), slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	r := httptest.NewRequest("GET", "https://prism.test/.portico/login?rd=/console/", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	a.Login().ServeHTTP(rec, r)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "BRANDED") || !strings.Contains(rec.Body.String(), "retry=/.portico/login?rd=/console/") {
		t.Fatalf("browser: %d %q", rec.Code, rec.Body.String())
	}
	r = httptest.NewRequest("GET", "https://prism.test/.portico/login", nil)
	r.Header.Set("Accept", "application/json")
	rec = httptest.NewRecorder()
	a.Login().ServeHTTP(rec, r)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "BRANDED") || !strings.Contains(rec.Body.String(), "identity provider unavailable") {
		t.Fatalf("non-browser: %d %q", rec.Code, rec.Body.String())
	}
}

// A callback without its login state (the state cookie expired while
// the login page sat open) restarts the flow once from /; a second one
// in a row is the 400 of old, so blocked cookies cannot loop.
func TestCallbackWithoutStateRestartsOnce(t *testing.T) {
	idp := newFakeIDP(t)
	a, st := newTestAuth(t, idp)
	client, _ := porticoServer(t, a)
	stop := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	follow := client.CheckRedirect
	client.CheckRedirect = stop
	resp, err := client.Get("https://prism.test/.portico/callback?code=x&state=y")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != 303 || !strings.HasPrefix(loc, "/.portico/login?") {
		t.Fatalf("first: %d %q, want 303 to the login", resp.StatusCode, loc)
	}
	if len(st.sessions) != 0 {
		t.Error("a session was created by the stateless callback")
	}
	// With cookies working, the restarted login completes.
	client.CheckRedirect = follow
	resp, err = client.Get("https://prism.test" + loc)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "console for alice@lab.portico.test") || len(st.sessions) != 1 {
		t.Fatalf("restarted login: %d %q sessions %d", resp.StatusCode, body, len(st.sessions))
	}
}

// With cookies blocked on the Portico host no marker cookie can exist:
// the restart marker travels in the OIDC state instead, so the second
// stateless callback in a row is the plain error, never another trip
// through Keycloak.
func TestCallbackWithoutCookiesDoesNotLoop(t *testing.T) {
	idp := newFakeIDP(t)
	a, st := newTestAuth(t, idp)
	client, _ := porticoServer(t, a)
	client.Jar = nil // cookies blocked
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get("https://prism.test/.portico/callback?code=x&state=y")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != 303 || !strings.HasPrefix(loc, "/.portico/login?") || !strings.Contains(loc, "restart=1") {
		t.Fatalf("first: %d %q, want 303 to /.portico/login with restart=1", resp.StatusCode, loc)
	}
	// The restarted login carries the marker in its state.
	resp, err = client.Get("https://prism.test" + loc)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	authz, _ := url.Parse(resp.Header.Get("Location"))
	state := authz.Query().Get("state")
	if resp.StatusCode != 302 || !strings.HasSuffix(state, restartMarker) {
		t.Fatalf("restarted login: %d state %q", resp.StatusCode, state)
	}
	// Keycloak sends the browser back with that state and still no cookie.
	resp, err = client.Get("https://prism.test/.portico/callback?code=x&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("second stateless callback: %d, want 400", resp.StatusCode)
	}
	if len(st.sessions) != 0 {
		t.Error("a session was created")
	}
}
