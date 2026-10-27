// Package auth holds Portico's own sessions and the OIDC login flow
// against Keycloak.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

// SessionCookie carries the Portico session id. The __Host- prefix makes
// browsers require Secure, Path=/ and no Domain.
const SessionCookie = "__Host-portico_session"

// OIDCCookie carries the state of a login in progress.
const OIDCCookie = "__Host-portico_oidc"

// AdminCookie is the admin UI's session on the internal listener. It has
// no __Host- prefix (the listener may be plain HTTP on the loopback) and
// cookies are port-blind, so a browser holding an admin session sends it
// to the public listener on the same host name too: the proxy strips it.
const AdminCookie = "portico_admin"

// PorticoCookies lists every cookie Portico sets; none of them may reach
// an upstream.
func PorticoCookies() []string {
	return []string{SessionCookie, OIDCCookie, AdminCookie}
}

// Session is one authenticated browser.
type Session struct {
	ID        string
	Subject   string // OIDC "sub"
	Email     string
	CSRF      string // per-session token of the admin UI forms (unused for tenant sessions)
	IDToken   string // raw ID token of the login, sent as id_token_hint at logout; never logged
	CreatedAt time.Time
	LastSeen  time.Time
	// Tenant is the tenant the login resolved from the user's Keycloak
	// group; "" when the user belongs to none (design sites §4.3).
	Tenant string
	// Site is the site the tenant chose and SiteSince when; "" until one
	// is chosen. The routing checks it against the tenant's placements on
	// every request (design sites §4.4).
	Site      string
	SiteSince time.Time
	// CookieSite is the site whose Prism Central cookies the browser
	// holds live: the last site chosen. "Disconnect" clears Site, not
	// CookieSite, so the next choice still stashes the right cookies.
	CookieSite string
}

// ActiveWindow is how recent a session's last request must be for the
// session to count as active on its site (design sites §4.1): an open
// console tab polls more often than this (write-up 21: at least once a
// minute from a hidden tab).
const ActiveWindow = 2 * time.Minute

// Store keeps sessions. The memory implementation is used now; Milestone 2
// adds SQLite behind the same interface.
type Store interface {
	Create(s Session)
	// Get returns the session when it exists and is neither past the
	// absolute lifetime nor past the idle limit; on success LastSeen is
	// moved to now.
	Get(id string, now time.Time) (Session, bool)
	Delete(id string)
}

// MemoryStore is a mutex-guarded map of sessions.
type MemoryStore struct {
	maxAge, idle time.Duration
	mu           sync.Mutex
	sessions     map[string]Session
}

// NewMemoryStore returns an empty store with the given lifetimes.
func NewMemoryStore(maxAge, idle time.Duration) *MemoryStore {
	return &MemoryStore{maxAge: maxAge, idle: idle, sessions: make(map[string]Session)}
}

func (m *MemoryStore) expired(s Session, now time.Time) bool {
	return now.Sub(s.CreatedAt) > m.maxAge || now.Sub(s.LastSeen) > m.idle
}

// Create stores s, replacing any session with the same id.
func (m *MemoryStore) Create(s Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s
}

// Get implements Store.
func (m *MemoryStore) Get(id string, now time.Time) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, false
	}
	if m.expired(s, now) {
		delete(m.sessions, id)
		return Session{}, false
	}
	s.LastSeen = now
	m.sessions[id] = s
	return s, true
}

// Peek is Get without touching the session: a request that is not the
// user at the keyboard (a page polling on a timer) does not move its
// idle clock.
func (m *MemoryStore) Peek(id string, now time.Time) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, false
	}
	if m.expired(s, now) {
		delete(m.sessions, id)
		return Session{}, false
	}
	return s, true
}

// Delete implements Store.
func (m *MemoryStore) Delete(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}

// DeleteSubject ends every session of subject and reports how many.
func (m *MemoryStore) DeleteSubject(subject string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, s := range m.sessions {
		if s.Subject == subject {
			delete(m.sessions, id)
			n++
		}
	}
	return n
}

// SetSite records the session's chosen site; choosing the same site again
// keeps the time it was first chosen. False when the session is gone.
func (m *MemoryStore) SetSite(id, site string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || m.expired(s, now) {
		return false
	}
	if s.Site != site {
		s.Site, s.SiteSince = site, now
	}
	s.CookieSite = site
	m.sessions[id] = s
	return true
}

// LeaveSite takes one session off its site (the console's Sign Out with
// several sites, design sites usability 4.7). CookieSite stays: the
// browser's cookies of that site are expired by the caller, and the next
// choice must still restore the target's stash without wiping the
// others. False when the session is gone.
func (m *MemoryStore) LeaveSite(id string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || m.expired(s, now) {
		return false
	}
	s.Site, s.SiteSince = "", time.Time{}
	m.sessions[id] = s
	return true
}

// ClearSite takes every session off the site ("Disconnect remaining
// users", design sites §4.1) and returns how many there were; their next
// request goes back to the site page.
func (m *MemoryStore) ClearSite(site string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, s := range m.sessions {
		if s.Site == site {
			s.Site, s.SiteSince = "", time.Time{}
			m.sessions[id] = s
			n++
		}
	}
	return n
}

// List returns the live sessions and drops the expired ones.
func (m *MemoryStore) List(now time.Time) []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		if m.expired(s, now) {
			delete(m.sessions, id)
			continue
		}
		out = append(out, s)
	}
	return out
}

// Sweep drops expired sessions and returns how many it removed.
func (m *MemoryStore) Sweep(now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, s := range m.sessions {
		if m.expired(s, now) {
			delete(m.sessions, id)
			n++
		}
	}
	return n
}

// NewSessionID returns 32 random bytes as unpadded base64url.
func NewSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// SetSessionCookie sends the session cookie.
func SetSessionCookie(w http.ResponseWriter, id string, maxAge time.Duration) {
	http.SetCookie(w, cookie(SessionCookie, id, int(maxAge.Seconds())))
}

// ClearSessionCookie tells the browser to drop the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, cookie(SessionCookie, "", -1))
}

// SessionFromRequest looks the request's session cookie up in st.
func SessionFromRequest(st Store, r *http.Request, now time.Time) (Session, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return Session{}, false
	}
	return st.Get(c.Value, now)
}
