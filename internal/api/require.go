package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/marco-fabbri/portico/internal/store"
)

// Identity is who authenticated on the admin listener.
type Identity struct {
	Name string
	Via  string // "basic" or "session"
}

// SessionChecker lets the UI's session be accepted by the middleware.
type SessionChecker interface {
	Identity(r *http.Request) (Identity, bool)
}

type identityKey struct{}

// IdentityFromContext returns the identity RequireAdmin stored.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// publicUIPaths need no authentication: the login page and its assets,
// and the readiness fragment the restart page polls (sessions do not
// survive a restart).
func publicUIPath(p string) bool {
	return p == "/login" || p == "/setup/ready" || p == "/exposure/ready" || strings.HasPrefix(p, "/static/")
}

func isAPIPath(p string) bool {
	return strings.HasPrefix(p, Prefix) || p == strings.TrimSuffix(Prefix, "/")
}

// RequireAdmin accepts a UI session (through sessions, may be nil) on UI
// paths, and HTTP Basic against the stored break-glass hash, braked by
// lim, on every path. Without either: 401 with the Basic challenge on API
// paths, a redirect to /login on UI paths. When no password is set: 401 on API paths, 503 with the
// set-password instruction on UI paths. The login page and the static
// assets pass through.
func RequireAdmin(st store.Store, sessions SessionChecker, lim *Limiter, realm string, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	if lim == nil {
		lim = NewLimiter()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			api := isAPIPath(r.URL.Path)
			if !api && publicUIPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			// A browser keeps Basic credentials it was asked for once and
			// attaches them to a form another site submits; it also says
			// where the request comes from, which scripts do not.
			if api && crossSiteChange(r) {
				writeError(w, http.StatusForbidden, "the API does not accept changes sent by a browser from another site")
				return
			}
			// The UI session is a cookie with no CSRF token on the API, and
			// SameSite=Strict is port-blind: a page on the tenant site could
			// post to the API with it. API paths take Basic only.
			if sessions != nil && !api {
				if id, ok := sessions.Identity(r); ok {
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
					return
				}
			}
			if user, pass, ok := r.BasicAuth(); ok {
				source := sourceOf(r)
				if wait := lim.Wait(source); wait > 0 {
					w.Header().Set("Retry-After", RetryAfter(wait))
					writeError(w, http.StatusTooManyRequests, "too many failed sign-ins from this address: wait "+RetryAfter(wait)+" s")
					return
				}
				if checkPassword(r.Context(), st, user, pass, log) {
					lim.Reset(source)
					// The factory password opens the admin UI's password page
					// only; the API waits for a real one.
					if PasswordChangeRequired(r.Context(), st) {
						writeError(w, http.StatusForbidden, "password change required: sign in to the admin UI once and set a new password")
						return
					}
					id := Identity{Name: adminAccount, Via: "basic"}
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
					return
				}
				// The fourth wrong password in ten minutes locks the
				// source out for 1 s, the next ones 2, 4, 8 s.
				lim.Failed(source)
				challenge(w, realm)
				return
			}
			if api {
				challenge(w, realm)
				return
			}
			if !passwordSet(r.Context(), st) {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte("Portico's break-glass password is not set. On the appliance run:\n  printf '%s\\n' \"$PW\" | sudo -u portico portico admin set-password -config /etc/portico/portico.yaml\n"))
				return
			}
			// An HTMX request (a poll, a button) asks HTMX to move the
			// whole page: a redirect would swap the login page into the
			// fragment.
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusOK)
				return
			}
			// A return path only for a page load: a POST that met an
			// expired session would land on a 405 after the login.
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		})
	}
}

// crossSiteChange reports a state-changing request a browser sent from
// another origin.
func crossSiteChange(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	site := r.Header.Get("Sec-Fetch-Site")
	return site != "" && site != "none" && site != "same-origin"
}

func challenge(w http.ResponseWriter, realm string) {
	w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`", charset="UTF-8"`)
	writeError(w, http.StatusUnauthorized, "unauthorized")
}

func passwordSet(ctx context.Context, st store.Store) bool {
	_, err := st.AdminPasswordHash(ctx, adminAccount)
	return err == nil
}

// sourceOf returns the client address without the port.
func sourceOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
