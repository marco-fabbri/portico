package api

import (
	"bytes"
	"context"
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

func TestRequireAdminDelaysAfterFailures(t *testing.T) {
	h, _, _ := requireEnv(t, true)
	var slept []time.Duration
	old := sleep
	sleep = func(d time.Duration) { slept = append(slept, d) }
	defer func() { sleep = old }()
	for i := 0; i < 4; i++ {
		call(h, "/api/v1/tenants", "admin", "wrong", "", "10.0.0.1:5000")
	}
	if len(slept) != 4 || slept[3] != time.Second || slept[2] != 0 {
		t.Fatalf("sleeps = %v, want [0 0 0 1s]", slept)
	}
	call(h, "/api/v1/tenants", "admin", "wrong", "", "10.0.0.2:5000")
	if slept[len(slept)-1] != 0 {
		t.Fatal("another source was delayed")
	}
	rec := call(h, "/api/v1/tenants", "admin", adminPassword, "", "10.0.0.1:5000")
	if rec.Code != http.StatusTeapot {
		t.Fatalf("right password after failures: %d", rec.Code)
	}
	call(h, "/api/v1/tenants", "admin", "wrong", "", "10.0.0.1:5000")
	if slept[len(slept)-1] != 0 {
		t.Fatal("the success did not reset the delay")
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
