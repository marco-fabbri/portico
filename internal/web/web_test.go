package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/branding"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
	"github.com/marco-fabbri/portico/internal/tenant/tenanttest"
	"golang.org/x/crypto/bcrypt"
)

const adminPassword = "correct-horse-7"

type env struct {
	h      http.Handler // RequireAdmin(web)
	web    *Handler
	st     *store.SQLite
	svc    *tenant.Service
	pc     *tenanttest.PC
	kc     *tenanttest.KC
	brand  *branding.Service
	log    *bytes.Buffer
	cookie *http.Cookie
	csrf   string
}

type envOpt func(*Options)

func newEnv(t *testing.T, withPassword bool, opts ...envOpt) *env {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if withPassword {
		hash, _ := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.MinCost)
		st.SetAdminPassword(context.Background(), "admin", string(hash))
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pc := tenanttest.NewPC("portico-idp")
	kc := tenanttest.NewKC()
	sites := []tenant.Site{{ID: "site-a", Code: "SITE-A", PC: pc, SAMLIdPName: "portico-idp", ClusterExtID: "cl", StorageContainerExtID: "sc"}}
	svc := tenant.NewService(st, sites, kc, tenant.Config{OperationTimeout: 5 * time.Second}, log)
	brand := branding.NewService(st, log)
	o := Options{Store: st, Service: svc, Branding: brand, Sessions: auth.NewMemoryStore(8*time.Hour, 15*time.Minute), Limiter: api.NewLimiter(), Secure: true, Version: "test", Log: log}
	for _, f := range opts {
		f(&o)
	}
	h := New(o)
	return &env{h: api.RequireAdmin(st, h, o.Limiter, "portico", log)(h), web: h, st: st, svc: svc, pc: pc, kc: kc, brand: brand, log: &buf}
}

func (e *env) do(method, path string, form url.Values, auth bool) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, "https://127.0.0.1:8443"+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if auth && e.cookie != nil {
		r.AddCookie(e.cookie)
	}
	r.RemoteAddr = "10.0.0.9:4000"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

var reCSRF = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// login signs in and keeps the cookie and the CSRF token for later calls.
func (e *env) login(t *testing.T) {
	t.Helper()
	rec := e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {adminPassword}}, false)
	if rec.Code != 303 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			e.cookie = c
		}
	}
	if e.cookie == nil {
		t.Fatal("no session cookie after login")
	}
	rec = e.do("GET", "/", nil, true)
	if rec.Code == 303 {
		// Setup mode sends the home page to /setup.
		rec = e.do("GET", rec.Header().Get("Location"), nil, true)
	}
	m := reCSRF.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no CSRF token in the page: %d %s", rec.Code, rec.Body.String())
	}
	e.csrf = m[1]
}

func (e *env) form(kv ...string) url.Values {
	v := url.Values{"_csrf": {e.csrf}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func TestLoginPageAndSuccess(t *testing.T) {
	e := newEnv(t, true)
	rec := e.do("GET", "/login", nil, false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="password"`) || !strings.Contains(rec.Body.String(), `name="user"`) {
		t.Fatalf("login page: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {adminPassword}}, false)
	if rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Fatalf("login: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	var c *http.Cookie
	for _, x := range rec.Result().Cookies() {
		if x.Name == SessionCookie {
			c = x
		}
	}
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie = %+v", c)
	}
	r := httptest.NewRequest("GET", "https://127.0.0.1:8443/", nil)
	r.AddCookie(c)
	id, ok := e.web.Identity(r)
	if !ok || id.Name != "admin" || id.Via != "session" {
		t.Fatalf("Identity = %+v, %v", id, ok)
	}
}

func TestLoginCookieNotSecureWithoutTLS(t *testing.T) {
	e := newEnv(t, true, func(o *Options) { o.Secure = false })
	rec := e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {adminPassword}}, false)
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie && c.Secure {
			t.Fatal("Secure cookie on a plain listener would never be sent back")
		}
	}
}

func TestLoginWrongPasswordShowsErrorAndFails(t *testing.T) {
	e := newEnv(t, true)
	rec := e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {"nope"}}, false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Wrong user or password") {
		t.Fatalf("wrong password: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			t.Fatal("a session cookie was set on a failed login")
		}
	}
	if e.web.limiter.Delay("10.0.0.9") != 0 {
		t.Fatal("one failure should not delay yet")
	}
	for i := 0; i < 3; i++ {
		e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {"nope"}}, false)
	}
	if e.web.limiter.Delay("10.0.0.9") == 0 {
		t.Fatal("four failures should delay")
	}
}

func TestLoginNextIsPathOnly(t *testing.T) {
	e := newEnv(t, true)
	rec := e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {adminPassword}, "next": {"https://evil.test/x"}}, false)
	if rec.Header().Get("Location") != "/" {
		t.Fatalf("absolute next: %q", rec.Header().Get("Location"))
	}
	rec = e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {adminPassword}, "next": {"//evil.test/x"}}, false)
	if rec.Header().Get("Location") != "/" {
		t.Fatalf("protocol-relative next: %q", rec.Header().Get("Location"))
	}
	rec = e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {adminPassword}, "next": {"/tenants/x"}}, false)
	if rec.Header().Get("Location") != "/tenants/x" {
		t.Fatalf("path next: %q", rec.Header().Get("Location"))
	}
}

func TestCSRFRequiredOnPost(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.do("POST", "/logout", url.Values{}, true)
	if rec.Code != 403 {
		t.Fatalf("logout without token: %d", rec.Code)
	}
	rec = e.do("POST", "/logout", url.Values{"_csrf": {"someone-elses-token"}}, true)
	if rec.Code != 403 {
		t.Fatalf("logout with a foreign token: %d", rec.Code)
	}
	if rec := e.do("GET", "/", nil, true); rec.Code != 200 {
		t.Fatalf("session should still be alive: %d", rec.Code)
	}
	rec = e.do("POST", "/logout", e.form(), true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/login" {
		t.Fatalf("logout: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.do("GET", "/", nil, true); rec.Code != 303 {
		t.Fatalf("after logout GET / = %d, want redirect to login", rec.Code)
	}
}

func TestSessionIdleExpires(t *testing.T) {
	e := newEnv(t, true, func(o *Options) { o.Sessions = auth.NewMemoryStore(8*time.Hour, time.Millisecond) })
	e.login(t)
	time.Sleep(5 * time.Millisecond)
	rec := e.do("GET", "/", nil, true)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("idle session: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = e.do("POST", "/logout", e.form(), true)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("form after idle: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestUnsetPasswordPage(t *testing.T) {
	e := newEnv(t, false)
	rec := e.do("GET", "/login", nil, false)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "portico admin set-password") || strings.Contains(rec.Body.String(), `name="password"`) {
		t.Fatalf("unset page: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {"x"}}, false)
	if rec.Code != 503 {
		t.Fatalf("login POST with no password set: %d", rec.Code)
	}
}

func TestStaticServed(t *testing.T) {
	e := newEnv(t, true)
	rec := e.do("GET", "/static/htmx.min.js", nil, false)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") || !strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
		t.Fatalf("htmx: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	rec = e.do("GET", "/static/portico.css", nil, false)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = e.do("GET", "/static/../web.go", nil, false)
	if rec.Code == 200 {
		t.Fatal("path traversal served a file")
	}
}

func TestNoSecretInLog(t *testing.T) {
	e := newEnv(t, true)
	e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {"wrong-one-xyz"}}, false)
	e.login(t)
	out := e.log.String()
	for _, secret := range []string{"wrong-one-xyz", adminPassword, e.cookie.Value, e.csrf} {
		if strings.Contains(out, secret) {
			t.Fatalf("log contains a secret: %s", out)
		}
	}
	if !strings.Contains(out, "admin login") {
		t.Fatalf("expected a login event in the log: %s", out)
	}
}

// Every page carries Portico's own mark: the favicon and the header
// brand (black mark on the dark header is served white).
func TestLayoutCarriesThePorticoMark(t *testing.T) {
	e := newEnv(t, true)
	page := e.do("GET", "/login", nil, false).Body.String()
	// Static URLs carry the binary's version: the assets are cached for
	// a day, and a new binary must not run on the old stylesheet.
	for _, want := range []string{`<link rel="icon" type="image/svg+xml" href="/static/portico-mark.svg?v=`, `<link rel="stylesheet" href="/static/portico.css?v=`, `<img class="mark-large" src="/static/portico-mark.svg?v=`} {
		if !strings.Contains(page, want) {
			t.Fatalf("layout lacks %q:\n%s", want, page)
		}
	}
	for _, f := range []string{"/static/portico-mark.svg"} {
		rec := e.do("GET", f, nil, false)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<svg") {
			t.Fatalf("%s: %d", f, rec.Code)
		}
	}
}
