package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/sites"
)

// withRuntime gives the settings env a site runtime: a monitor whose
// site-a probe succeeds and a tenant session store.
func withRuntime(t *testing.T, e *settingsEnv) (*sites.Monitor, *auth.MemoryStore) {
	t.Helper()
	mon := sites.NewMonitor(e.st, map[string]sites.Prober{"site-a": func(context.Context) error { return nil }}, nil)
	sess := auth.NewMemoryStore(8*time.Hour, 15*time.Minute)
	e.so.Runtime = &Runtime{Monitor: mon, Store: sess}
	return mon, sess
}

func TestMaintenanceAPIImmediate(t *testing.T) {
	e := newSettingsEnv(t, true)
	mon, _ := withRuntime(t, e)
	rec, out := e.do("PUT", "/api/v1/sites/site-a/maintenance", `{"maintenance":true}`)
	if rec.Code != 200 || out["status"] != "maintenance" || mon.Status("site-a") != sites.Maintenance || e.restarted != 0 {
		t.Fatalf("maintenance on: %d %v restarted=%d", rec.Code, out, e.restarted)
	}
	_, out = e.do("GET", "/api/v1/sites", "")
	if s := out["sites"].([]any)[0].(map[string]any); s["status"] != "maintenance" || s["maintenance"] != true {
		t.Fatalf("list = %v", s)
	}
	if rec, out := e.do("PUT", "/api/v1/sites/site-a/maintenance", `{"maintenance":false}`); rec.Code != 200 || out["status"] != "available" {
		t.Fatalf("maintenance off: %d %v", rec.Code, out)
	}
	if rec, _ := e.do("PUT", "/api/v1/sites/nope/maintenance", `{"maintenance":true}`); rec.Code != 404 {
		t.Fatalf("unknown site: %d", rec.Code)
	}
	if !strings.Contains(e.log.String(), "site maintenance") {
		t.Fatalf("log: %s", e.log.String())
	}
}

func TestSessionsAPIActiveIdle(t *testing.T) {
	e := newSettingsEnv(t, true)
	_, sess := withRuntime(t, e)
	now := time.Now()
	sess.Create(auth.Session{ID: "s1", Email: "carol@acme.example", Tenant: "acme", Site: "site-a", SiteSince: now.Add(-time.Hour), CreatedAt: now.Add(-time.Hour), LastSeen: now.Add(-30 * time.Second)})
	sess.Create(auth.Session{ID: "s2", Email: "dave@acme.example", Tenant: "acme", Site: "site-a", SiteSince: now.Add(-time.Hour), CreatedAt: now.Add(-time.Hour), LastSeen: now.Add(-10 * time.Minute)})
	sess.Create(auth.Session{ID: "s3", Email: "erin@beta.example", Tenant: "beta", Site: "site-b", CreatedAt: now, LastSeen: now})
	rec, out := e.do("GET", "/api/v1/sites/site-a/sessions", "")
	if rec.Code != 200 || out["active"] != float64(1) || out["idle"] != float64(1) {
		t.Fatalf("sessions: %d %v", rec.Code, out)
	}
	list := out["sessions"].([]any)
	first := list[0].(map[string]any)
	if len(list) != 2 || first["email"] != "carol@acme.example" || first["state"] != "active" || first["tenant"] != "acme" || first["since"] == nil || first["last_activity"] == nil {
		t.Fatalf("list = %v", list)
	}
	if strings.Contains(rec.Body.String(), "s1") && strings.Contains(rec.Body.String(), `"s1"`) {
		t.Fatal("a session id reached the answer")
	}
	_, out = e.do("GET", "/api/v1/sites", "")
	if s := out["sites"].([]any)[0].(map[string]any); s["active"] != float64(1) || s["idle"] != float64(1) {
		t.Fatalf("list counts = %v", s)
	}
}

func TestDisconnectAPIClearsSite(t *testing.T) {
	e := newSettingsEnv(t, true)
	_, sess := withRuntime(t, e)
	now := time.Now()
	sess.Create(auth.Session{ID: "s1", Tenant: "acme", Site: "site-a", CreatedAt: now, LastSeen: now})
	sess.Create(auth.Session{ID: "s2", Tenant: "beta", Site: "site-b", CreatedAt: now, LastSeen: now})
	rec, out := e.do("POST", "/api/v1/sites/site-a/sessions/disconnect", "")
	if rec.Code != 200 || out["disconnected"] != float64(1) {
		t.Fatalf("disconnect: %d %v", rec.Code, out)
	}
	if s, _ := sess.Get("s1", time.Now()); s.Site != "" {
		t.Fatalf("s1 still on the site: %+v", s)
	}
	if s, _ := sess.Get("s2", time.Now()); s.Site != "site-b" {
		t.Fatal("another site's session was touched")
	}
	if !strings.Contains(e.log.String(), "disconnected") {
		t.Fatalf("log: %s", e.log.String())
	}
}

func TestRuntimeRoutes503InSetupMode(t *testing.T) {
	e := newDraftEnv(t)
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/sites/site-a/maintenance", `{"maintenance":true}`},
		{"GET", "/api/v1/sites/site-a/sessions", ""},
		{"POST", "/api/v1/sites/site-a/sessions/disconnect", ""},
	} {
		rec, out := e.do(c.method, c.path, c.body)
		if rec.Code != 503 || out["error"] != SetupModeMessage {
			t.Fatalf("%s %s: %d %v", c.method, c.path, rec.Code, out)
		}
	}
}
