package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

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

// sleep is replaced by tests.
var sleep = time.Sleep

// publicUIPaths need no authentication: the login page and its assets,
// and the readiness fragment the restart page polls (sessions do not
// survive a restart).
func publicUIPath(p string) bool {
	return p == "/login" || p == "/setup/ready" || strings.HasPrefix(p, "/static/")
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
				// The failure is recorded first, then the answer waits:
				// the fourth wrong password in ten minutes costs 1 s, the
				// next ones 2, 4, 8 s.
				lim.Failed(source)
				sleep(lim.Delay(source))
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
