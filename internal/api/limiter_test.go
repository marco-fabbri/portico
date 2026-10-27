package api

import (
	"context"
	"github.com/marco-fabbri/portico/internal/store"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLimiterDelayGrowsAndResets(t *testing.T) {
	l := NewLimiter()
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	want := []time.Duration{0, 0, 0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for i, d := range want {
		l.Failed("10.0.0.1")
		if got := l.Delay("10.0.0.1"); got != d {
			t.Fatalf("after %d failures Delay = %v, want %v", i+1, got, d)
		}
	}
	l.Reset("10.0.0.1")
	if got := l.Delay("10.0.0.1"); got != 0 {
		t.Fatalf("after Reset Delay = %v, want 0", got)
	}
}

func TestLimiterIsPerSource(t *testing.T) {
	l := NewLimiter()
	for i := 0; i < 6; i++ {
		l.Failed("a")
	}
	if l.Delay("b") != 0 {
		t.Fatal("failures on a delayed b")
	}
	if l.Delay("a") == 0 {
		t.Fatal("a is not delayed")
	}
}

func TestLimiterSweepsIdleSources(t *testing.T) {
	l := NewLimiter()
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < 6; i++ {
		l.Failed("a")
	}
	now = now.Add(11 * time.Minute)
	if l.Delay("a") != 0 {
		t.Fatal("an idle source is still delayed")
	}
	l.Sweep(now)
	if n := l.len(); n != 0 {
		t.Fatalf("entries after Sweep = %d, want 0", n)
	}
}

// Factory password (Nutanix's convention, Marco 2026-10-19): the appliance
// starts with admin / portico/4u and refuses every API call until it is
// changed; the UI forces the change at the first login.
func TestBasicAuthRefusesTheFactoryPasswordUntilChanged(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	BcryptCost = bcrypt.MinCost
	ctx := context.Background()
	set, err := EnsureFactoryPassword(ctx, st)
	if err != nil || !set {
		t.Fatalf("EnsureFactoryPassword: set=%v err=%v", set, err)
	}
	if set, _ := EnsureFactoryPassword(ctx, st); set {
		t.Fatal("the factory password was set twice")
	}
	if !PasswordChangeRequired(ctx, st) {
		t.Fatal("no change required after the factory password")
	}
	h := BasicAuth(st, "portico", nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	call := func(pw string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/tenants", nil)
		req.SetBasicAuth("admin", pw)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := call(FactoryPassword); rec.Code != 403 || !strings.Contains(rec.Body.String(), "password change required") {
		t.Fatalf("factory password on the API: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("wrong"); rec.Code != 401 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	if err := SetAdminPassword(ctx, st, "Correct-Horse-9"); err != nil {
		t.Fatal(err)
	}
	if PasswordChangeRequired(ctx, st) {
		t.Fatal("change still required after SetAdminPassword")
	}
	if rec := call("Correct-Horse-9"); rec.Code != 200 {
		t.Fatalf("new password: %d", rec.Code)
	}
}

func TestLimiterWaitCountsDownFromTheLastFailure(t *testing.T) {
	l := NewLimiter()
	now := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		l.Failed("a")
	}
	if w := l.Wait("a"); w != 0 {
		t.Fatalf("three failures are free: Wait = %v", w)
	}
	l.Failed("a")
	now = now.Add(400 * time.Millisecond)
	if w := l.Wait("a"); w != 600*time.Millisecond {
		t.Fatalf("Wait = %v, want 600ms", w)
	}
	now = now.Add(600 * time.Millisecond)
	if w := l.Wait("a"); w != 0 {
		t.Fatalf("Wait after the delay = %v", w)
	}
}
