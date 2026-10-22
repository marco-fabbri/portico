package auth

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func newSession(id string) Session {
	return Session{ID: id, Subject: "sub-" + id, Email: id + "@example.test", CreatedAt: t0, LastSeen: t0}
}

func TestMemoryStoreIdleExpiry(t *testing.T) {
	st := NewMemoryStore(8*time.Hour, 15*time.Minute)
	st.Create(newSession("a"))
	s, ok := st.Get("a", t0.Add(14*time.Minute))
	if !ok || !s.LastSeen.Equal(t0.Add(14*time.Minute)) {
		t.Fatalf("Get at +14m: ok=%v LastSeen=%v", ok, s.LastSeen)
	}
	if _, ok := st.Get("a", t0.Add(29*time.Minute)); !ok {
		t.Fatal("Get at +29m after a touch at +14m should succeed")
	}
	if _, ok := st.Get("a", t0.Add(45*time.Minute)); ok {
		t.Fatal("Get at +45m with the last touch at +29m should fail (idle 15m)")
	}
}

func TestMemoryStoreMaxAge(t *testing.T) {
	st := NewMemoryStore(8*time.Hour, 15*time.Minute)
	st.Create(newSession("a"))
	for m := 10; m <= 8*60; m += 10 {
		if _, ok := st.Get("a", t0.Add(time.Duration(m)*time.Minute)); !ok {
			t.Fatalf("Get at +%dm failed while touching every 10m", m)
		}
	}
	if _, ok := st.Get("a", t0.Add(8*time.Hour+time.Second)); ok {
		t.Fatal("Get past max age should fail")
	}
}

func TestMemoryStoreSweep(t *testing.T) {
	st := NewMemoryStore(8*time.Hour, 15*time.Minute)
	st.Create(newSession("live"))
	st.Create(newSession("idle"))
	st.Create(newSession("old"))
	st.Get("live", t0.Add(10*time.Minute)) // touch inside the idle limit
	st.sessions["old"] = Session{ID: "old", CreatedAt: t0.Add(-9 * time.Hour), LastSeen: t0.Add(19 * time.Minute)}
	if n := st.Sweep(t0.Add(21 * time.Minute)); n != 2 {
		t.Fatalf("Sweep removed %d, want 2", n)
	}
	if _, ok := st.Get("live", t0.Add(22*time.Minute)); !ok {
		t.Fatal("live session lost by Sweep")
	}
}

func TestSessionIDIsRandomAndLong(t *testing.T) {
	a, b := NewSessionID(), NewSessionID()
	if a == b {
		t.Fatal("two session IDs are equal")
	}
	if len(a) < 43 {
		t.Fatalf("len = %d, want >= 43", len(a))
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(a) {
		t.Fatalf("id %q is not base64url", a)
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "abc", 8*time.Hour)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies", len(cookies))
	}
	c := cookies[0]
	if c.Name != SessionCookie || c.Value != "abc" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || c.MaxAge != int((8*time.Hour).Seconds()) {
		t.Errorf("cookie attributes wrong: %+v", c)
	}
}

func TestClearSessionCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	ClearSessionCookie(rec)
	c := rec.Result().Cookies()[0]
	if c.Name != SessionCookie || c.Value != "" || c.MaxAge != -1 {
		t.Errorf("clear cookie wrong: %+v", c)
	}
}

func TestSessionFromRequest(t *testing.T) {
	st := NewMemoryStore(8*time.Hour, 15*time.Minute)
	st.Create(newSession("known"))
	r := httptest.NewRequest("GET", "https://prism.test/", nil)
	if _, ok := SessionFromRequest(st, r, t0); ok {
		t.Error("no cookie should give no session")
	}
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "unknown"})
	if _, ok := SessionFromRequest(st, r, t0); ok {
		t.Error("unknown id should give no session")
	}
	r = httptest.NewRequest("GET", "https://prism.test/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "known"})
	s, ok := SessionFromRequest(st, r, t0.Add(time.Minute))
	if !ok || s.Email != "known@example.test" {
		t.Errorf("known id: ok=%v s=%+v", ok, s)
	}
}

func TestSetSiteAndClearSite(t *testing.T) {
	st := NewMemoryStore(8*time.Hour, 15*time.Minute)
	st.Create(newSession("a"))
	st.Create(newSession("b"))
	if !st.SetSite("a", "site-1", t0.Add(time.Minute)) || !st.SetSite("b", "site-1", t0.Add(time.Minute)) {
		t.Fatal("SetSite on live sessions")
	}
	s, _ := st.Get("a", t0.Add(2*time.Minute))
	if s.Site != "site-1" || !s.SiteSince.Equal(t0.Add(time.Minute)) {
		t.Fatalf("session a = %+v", s)
	}
	// Choosing the same site again keeps the time it was first chosen.
	st.SetSite("a", "site-1", t0.Add(3*time.Minute))
	if s, _ := st.Get("a", t0.Add(3*time.Minute)); !s.SiteSince.Equal(t0.Add(time.Minute)) {
		t.Fatalf("SiteSince moved on a repeat: %v", s.SiteSince)
	}
	st.SetSite("b", "site-2", t0.Add(4*time.Minute))
	if n := st.ClearSite("site-1"); n != 1 {
		t.Fatalf("ClearSite = %d, want 1", n)
	}
	if s, _ := st.Get("a", t0.Add(5*time.Minute)); s.Site != "" || !s.SiteSince.IsZero() {
		t.Fatalf("session a after ClearSite = %+v", s)
	}
	if s, _ := st.Get("b", t0.Add(5*time.Minute)); s.Site != "site-2" {
		t.Fatalf("session b lost its site: %+v", s)
	}
	if st.SetSite("gone", "site-1", t0) {
		t.Fatal("SetSite on a missing session")
	}
}

func TestListDropsExpired(t *testing.T) {
	st := NewMemoryStore(8*time.Hour, 15*time.Minute)
	st.Create(newSession("a"))
	st.Create(newSession("b"))
	st.Get("a", t0.Add(10*time.Minute))
	got := st.List(t0.Add(20 * time.Minute))
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("List = %+v", got)
	}
	if _, ok := st.sessions["b"]; ok {
		t.Fatal("List kept an expired session")
	}
	if ActiveWindow != 2*time.Minute {
		t.Fatalf("ActiveWindow = %v", ActiveWindow)
	}
}

func TestLeaveSiteKeepsCookieSite(t *testing.T) {
	st := NewMemoryStore(8*time.Hour, 15*time.Minute)
	t0 := time.Now()
	st.Create(Session{ID: "a", CreatedAt: t0, LastSeen: t0})
	st.SetSite("a", "site-1", t0)
	if !st.LeaveSite("a", t0.Add(time.Minute)) {
		t.Fatal("LeaveSite on a live session")
	}
	s, ok := st.Get("a", t0.Add(time.Minute))
	if !ok || s.Site != "" || !s.SiteSince.IsZero() || s.CookieSite != "site-1" {
		t.Fatalf("after LeaveSite: ok=%v site=%q since=%v cookieSite=%q", ok, s.Site, s.SiteSince, s.CookieSite)
	}
	if st.LeaveSite("gone", t0) {
		t.Fatal("LeaveSite on a missing session")
	}
}
