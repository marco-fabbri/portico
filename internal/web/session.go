package web

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/auth"
)

// SessionCookie carries the admin session id (auth.AdminCookie). No
// __Host- prefix: the internal listener may be plain HTTP on the
// loopback, where a Secure cookie would never come back.
const SessionCookie = auth.AdminCookie

const sessionMaxAge = 8 * time.Hour

// Identity implements api.SessionChecker: a valid session cookie is the
// break-glass administrator.
func (h *Handler) Identity(r *http.Request) (api.Identity, bool) {
	if _, ok := h.session(r); ok {
		return api.Identity{Name: "admin", Via: "session"}, true
	}
	return api.Identity{}, false
}

// PollHeader marks a request an element makes on a timer (hx-trigger
// "every"): it checks the session without counting as activity.
const PollHeader = "X-Portico-Poll"

// session returns the request's live session, touching it unless the
// request is a poll.
func (h *Handler) session(r *http.Request) (auth.Session, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return auth.Session{}, false
	}
	if r.Header.Get(PollHeader) == "1" {
		return h.sessions.Peek(c.Value, time.Now())
	}
	return h.sessions.Get(c.Value, time.Now())
}

func (h *Handler) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   h.secure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// newSession creates a session with a fresh CSRF token and sets the cookie.
func (h *Handler) newSession(w http.ResponseWriter) {
	now := time.Now()
	s := auth.Session{ID: auth.NewSessionID(), Subject: "admin", CSRF: auth.NewSessionID(), CreatedAt: now, LastSeen: now}
	h.sessions.Create(s)
	h.setCookie(w, s.ID, int(sessionMaxAge.Seconds()))
}

// csrfOK reports whether the form's _csrf matches the session's token.
// Without a live session there is nothing to match.
func (h *Handler) csrfOK(r *http.Request) bool {
	s, ok := h.session(r)
	if !ok {
		return false
	}
	got := r.FormValue("_csrf")
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.CSRF)) == 1
}

// requireCSRF answers 403 when the token is missing or wrong and reports
// whether the handler may continue.
func (h *Handler) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	if h.csrfOK(r) {
		return true
	}
	if _, ok := h.session(r); !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return false
	}
	h.render(w, r, http.StatusForbidden, "notfound", "Forbidden", nil, "invalid CSRF token: reload the page and try again")
	return false
}
