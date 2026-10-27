package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type fakeSessions struct{ id Identity }

func (f fakeSessions) Identity(r *http.Request) (Identity, bool) {
	if c, err := r.Cookie("portico_admin"); err == nil && c.Value == "good" {
		return f.id, true
	}
	return Identity{}, false
}

func requireEnv(t *testing.T, withPassword bool) (http.Handler, *Limiter, *bytes.Buffer) {
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
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	lim := NewLimiter()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := IdentityFromContext(r.Context())
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte(id.Name + "/" + id.Via))
	})
	return RequireAdmin(st, fakeSessions{Identity{Name: "admin", Via: "session"}}, lim, "portico", log)(inner), lim, &buf
}

func call(h http.Handler, path, user, pass, cookie, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://127.0.0.1:8443"+path, nil)
	if user != "" {
		r.SetBasicAuth(user, pass)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "portico_admin", Value: cookie})
	}
	if remote != "" {
		r.RemoteAddr = remote
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestRequireAdminBasicStillWorks(t *testing.T) {
	h, _, _ := requireEnv(t, true)
	rec := call(h, "/api/v1/tenants", "admin", adminPassword, "", "")
	if rec.Code != http.StatusTeapot || rec.Body.String() != "admin/basic" {
		t.Fatalf("valid Basic: %d %q", rec.Code, rec.Body.String())
	}
	rec = call(h, "/api/v1/tenants", "admin", "wrong", "", "")
	if rec.Code != 401 || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), `Basic realm="portico"`) {
		t.Fatalf("wrong Basic: %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestRequireAdminAcceptsSession(t *testing.T) {
	h, _, _ := requireEnv(t, true)
	rec := call(h, "/tenants", "", "", "good", "")
	if rec.Code != http.StatusTeapot || rec.Body.String() != "admin/session" {
		t.Fatalf("session: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("a Basic challenge was issued to a session request")
	}
}

func TestRequireAdminRedirectsUIPaths(t *testing.T) {
	h, _, _ := requireEnv(t, true)
	rec := call(h, "/tenants", "", "", "", "")
	if rec.Code != 303 || rec.Header().Get("Location") != "/login?next=%2Ftenants" {
		t.Fatalf("UI path without credentials: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = call(h, "/api/v1/tenants", "", "", "", "")
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("API path without credentials: %d %q", rec.Code, rec.Body.String())
	}
	rec = call(h, "/tenants", "", "", "stale", "")
	if rec.Code != 303 {
		t.Fatalf("UI path with a stale cookie: %d", rec.Code)
	}
}

// Review before v0.1.0: a delay on each answer did not stop parallel
// guesses, so a source past three failures is locked out for the delay:
// refused with 429 before any password is checked, the right one too.
func TestRequireAdminLocksOutAfterFailures(t *testing.T) {
	h, lim, _ := requireEnv(t, true)
	now := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	lim.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		if rec := call(h, "/api/v1/tenants", "admin", "wrong", "", "10.0.0.1:5000"); rec.Code != 401 {
			t.Fatalf("failure %d: %d", i+1, rec.Code)
		}
	}
	rec := call(h, "/api/v1/tenants", "admin", adminPassword, "", "10.0.0.1:5000")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("locked source: %d Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := call(h, "/api/v1/tenants", "admin", adminPassword, "", "10.0.0.2:5000"); rec.Code != http.StatusTeapot {
		t.Fatalf("another source was locked: %d", rec.Code)
	}
	now = now.Add(time.Second)
	if rec := call(h, "/api/v1/tenants", "admin", adminPassword, "", "10.0.0.1:5000"); rec.Code != http.StatusTeapot {
		t.Fatalf("right password after the lock: %d", rec.Code)
	}
	if rec := call(h, "/api/v1/tenants", "admin", "wrong", "", "10.0.0.1:5000"); rec.Code != 401 {
		t.Fatalf("the success did not reset the count: %d", rec.Code)
	}
}

func TestRequireAdminNoPasswordSet(t *testing.T) {
	h, _, buf := requireEnv(t, false)
	rec := call(h, "/api/v1/tenants", "admin", "anything", "", "")
	if rec.Code != 401 {
		t.Fatalf("API with no password set: %d", rec.Code)
	}
	rec = call(h, "/tenants", "", "", "", "")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "portico admin set-password") {
		t.Fatalf("UI with no password set: %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(buf.String(), "anything") {
		t.Fatal("the password reached the log")
	}
}

// Review fix 2: the UI session (a cookie, no CSRF token on the API) is
// not accepted on API paths; scripts use Basic.
func TestRequireAdminSessionIgnoredOnAPIPaths(t *testing.T) {
	h, _, _ := requireEnv(t, true)
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "https://127.0.0.1:8443/api/v1/tenants/x/disable", nil)
		r.AddCookie(&http.Cookie{Name: "portico_admin", Value: "good"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 401 {
			t.Fatalf("%s API path with a UI session: %d, want 401", method, rec.Code)
		}
	}
}

// The guard serve really uses (RequireAdmin, not BasicAuth) refuses the
// factory password on the API until it is changed (measured on the
// appliance: the API answered 200 with portico/4u).
func TestRequireAdminRefusesTheFactoryPasswordOnTheAPI(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	BcryptCost = bcrypt.MinCost
	if _, err := EnsureFactoryPassword(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := RequireAdmin(st, fakeSessions{Identity{Name: "admin", Via: "session"}}, NewLimiter(), "portico", slog.New(slog.NewJSONHandler(io.Discard, nil)))(inner)
	rec := call(h, "/api/v1/tenants", "admin", FactoryPassword, "", "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "password change required") {
		t.Fatalf("factory password on the API: %d %s", rec.Code, rec.Body.String())
	}
	if err := SetAdminPassword(context.Background(), st, "Correct-Horse-9"); err != nil {
		t.Fatal(err)
	}
	if rec := call(h, "/api/v1/tenants", "admin", "Correct-Horse-9", "", ""); rec.Code != http.StatusTeapot {
		t.Fatalf("new password on the API: %d", rec.Code)
	}
}

// The restart pages poll across a restart, which ends the session: they
// pass without one; the exposure state itself does not.
func TestRequireAdminLetsTheRestartPollsThrough(t *testing.T) {
	h, _, _ := requireEnv(t, true)
	for _, p := range []string{"/setup/ready", "/exposure/ready"} {
		if rec := call(h, p, "", "", "", ""); rec.Code != http.StatusTeapot {
			t.Fatalf("%s without a session: %d", p, rec.Code)
		}
	}
	if rec := call(h, "/exposure/status", "", "", "", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("/exposure/status without a session: %d", rec.Code)
	}
}

// Review before v0.1.0: a browser that once asked for /api/v1/ keeps the
// Basic credentials and attaches them to a form another site submits.
// A browser marks such a request with Sec-Fetch-Site; scripts send none.
func TestRequireAdminRefusesCrossSiteAPIChanges(t *testing.T) {
	h, _, _ := requireEnv(t, true)
	send := func(method, site string) int {
		r := httptest.NewRequest(method, "https://127.0.0.1:8443/api/v1/tenants/acme/disable", nil)
		r.SetBasicAuth("admin", adminPassword)
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	for _, site := range []string{"cross-site", "same-site"} {
		if c := send("POST", site); c != http.StatusForbidden {
			t.Errorf("POST from %s: %d", site, c)
		}
	}
	for _, c := range []struct {
		method, site string
	}{{"POST", ""}, {"POST", "none"}, {"POST", "same-origin"}, {"GET", "cross-site"}} {
		if got := send(c.method, c.site); got != http.StatusTeapot {
			t.Errorf("%s with Sec-Fetch-Site %q: %d", c.method, c.site, got)
		}
	}
}
