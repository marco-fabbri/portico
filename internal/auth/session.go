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
	CreatedAt time.Time
	LastSeen  time.Time
}

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

// Delete implements Store.
func (m *MemoryStore) Delete(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
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
