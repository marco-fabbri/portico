package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type ctxKey int

const sessionKey ctxKey = 0

// loginState travels in the OIDC cookie between /.portico/login and
// /.portico/callback.
type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Return   string `json:"rd"`
}

const oidcCookieMaxAge = 10 * time.Minute

// restartMarker ends the OIDC state of a login that a stateless
// callback restarted: a second stateless callback carrying it answers
// the plain error instead of restarting again. The marker lives in the
// state the identity provider echoes, not in a cookie, so a browser that
// blocks cookies on this host cannot loop through Keycloak.
const restartMarker = ".r"

// SessionFromContext returns the session Require stored for the request.
func SessionFromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey).(Session)
	return s, ok
}

// SafeReturnPath keeps only an absolute path on the same host: it must
// start with a single "/", carry no scheme, host or backslash, and never
// point into "/.portico/". Anything else becomes "/".
func SafeReturnPath(rd string) string {
	if rd == "" || !strings.HasPrefix(rd, "/") || strings.HasPrefix(rd, "//") || strings.ContainsAny(rd, "\\\r\n") {
		return "/"
	}
	u, err := url.Parse(rd)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	if strings.HasPrefix(strings.ToLower(u.Path), "/.portico/") {
		return "/"
	}
	return u.RequestURI()
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// unavailable answers "identity provider down": the branded page for a
// browser navigation when one is configured, plain text otherwise.
func (a *Authenticator) unavailable(w http.ResponseWriter, r *http.Request, retry string) {
	if a.cfg.ErrorPage != nil && strings.Contains(r.Header.Get("Accept"), "text/html") {
		a.cfg.ErrorPage(w, r, http.StatusServiceUnavailable, "Identity provider unavailable",
			"The sign-in service is not answering right now. Your session and data are safe; please try again in a minute.", retry)
		return
	}
	writeTextError(w, http.StatusServiceUnavailable, "identity provider unavailable, try again later")
}

func writeTextError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintln(w, msg)
}

func setOIDCCookie(w http.ResponseWriter, ls loginState) error {
	b, err := json.Marshal(ls)
	if err != nil {
		return err
	}
	http.SetCookie(w, cookie(OIDCCookie, base64.RawURLEncoding.EncodeToString(b), int(oidcCookieMaxAge.Seconds())))
	return nil
}

func clearOIDCCookie(w http.ResponseWriter) {
	http.SetCookie(w, cookie(OIDCCookie, "", -1))
}

func readOIDCCookie(r *http.Request) (loginState, bool) {
	c, err := r.Cookie(OIDCCookie)
	if err != nil || c.Value == "" {
		return loginState{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return loginState{}, false
	}
	var ls loginState
	if err := json.Unmarshal(b, &ls); err != nil || ls.State == "" || ls.Nonce == "" || ls.Verifier == "" {
		return loginState{}, false
	}
	return ls, true
}

// Login starts the authorization code flow: GET /.portico/login?rd=<path>.
func (a *Authenticator) Login() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeTextError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		p, err := a.Provider(a.ctxWithClient(r.Context()))
		if err != nil {
			a.log.Error("login: identity provider unavailable", "err", err.Error())
			a.unavailable(w, r, r.URL.RequestURI())
			return
		}
		ls := loginState{
			State:    NewSessionID(),
			Nonce:    NewSessionID(),
			Verifier: oauth2.GenerateVerifier(),
			Return:   SafeReturnPath(r.URL.Query().Get("rd")),
		}
		if r.URL.Query().Get("restart") == "1" {
			ls.State += restartMarker
		}
		if err := setOIDCCookie(w, ls); err != nil {
			writeTextError(w, http.StatusInternalServerError, "login failed")
			return
		}
		u := a.oauth2Config(p).AuthCodeURL(ls.State, oidc.Nonce(ls.Nonce), oauth2.S256ChallengeOption(ls.Verifier))
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, u, http.StatusFound)
	})
}

// Callback finishes the flow: GET /.portico/callback?code=...&state=...
func (a *Authenticator) Callback() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clearOIDCCookie(w)
		ls, ok := readOIDCCookie(r)
		if !ok {
			// The state cookie expired (the login page sat open) or never
			// arrived: restart the login once. A state that already
			// carries the restart marker means the restarted login lost
			// its cookie too (cookies blocked): the plain error, no loop.
			if strings.HasSuffix(r.URL.Query().Get("state"), restartMarker) {
				writeTextError(w, http.StatusBadRequest, "login state missing or expired, start again")
				return
			}
			a.log.Info("callback: login state missing, restarting the login")
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/.portico/login?rd=%2F&restart=1", http.StatusSeeOther)
			return
		}
		q := r.URL.Query()
		if q.Get("state") == "" || q.Get("state") != ls.State {
			a.log.Warn("callback: state mismatch")
			writeTextError(w, http.StatusBadRequest, "login state mismatch, start again")
			return
		}
		if e := q.Get("error"); e != "" {
			a.log.Warn("callback: provider error", "error", e)
			writeTextError(w, http.StatusBadRequest, "login refused by the identity provider")
			return
		}
		ctx := a.ctxWithClient(r.Context())
		p, err := a.Provider(ctx)
		if err != nil {
			a.unavailable(w, r, "/.portico/login")
			return
		}
		tok, err := a.oauth2Config(p).Exchange(ctx, q.Get("code"), oauth2.VerifierOption(ls.Verifier))
		if err != nil {
			a.log.Warn("callback: code exchange failed", "err", err.Error())
			writeTextError(w, http.StatusBadRequest, "login could not be completed, start again")
			return
		}
		raw, _ := tok.Extra("id_token").(string)
		if raw == "" {
			writeTextError(w, http.StatusBadRequest, "login could not be completed, start again")
			return
		}
		idt, err := p.Verifier(&oidc.Config{ClientID: a.cfg.ClientID}).Verify(ctx, raw)
		if err != nil {
			a.log.Warn("callback: id token rejected", "err", err.Error())
			writeTextError(w, http.StatusBadRequest, "login could not be completed, start again")
			return
		}
		if idt.Nonce != ls.Nonce {
			a.log.Warn("callback: nonce mismatch")
			writeTextError(w, http.StatusBadRequest, "login could not be completed, start again")
			return
		}
		var claims struct {
			Email string `json:"email"`
		}
		_ = idt.Claims(&claims)
		now := time.Now()
		s := Session{ID: NewSessionID(), Subject: idt.Subject, Email: claims.Email, CreatedAt: now, LastSeen: now}
		a.store.Create(s)
		SetSessionCookie(w, s.ID, a.cfg.CookieMaxAge)
		a.log.Info("login", "subject", s.Subject, "email", s.Email)
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, SafeReturnPath(ls.Return), http.StatusFound)
	})
}

// Logout ends the Portico session and sends the browser to the provider's
// end-session endpoint, so the Keycloak session ends too.
func (a *Authenticator) Logout() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			a.store.Delete(c.Value)
		}
		ClearSessionCookie(w)
		// Expire every other cookie the browser holds for this host too, so
		// the upstream session (Prism Central's cookies, Path=/) does not
		// outlive the Portico one on a shared workstation.
		for _, c := range r.Cookies() {
			if c.Name == SessionCookie || c.Name == OIDCCookie {
				continue
			}
			http.SetCookie(w, &http.Cookie{Name: c.Name, Value: "", Path: "/", MaxAge: -1, Secure: true})
		}
		w.Header().Set("Cache-Control", "no-store")
		home := "/"
		if ru, err := url.Parse(a.cfg.RedirectURL); err == nil {
			home = ru.Scheme + "://" + ru.Host + "/"
		}
		p, err := a.Provider(a.ctxWithClient(r.Context()))
		if err != nil || p.EndSessionURL == "" {
			http.Redirect(w, r, home, http.StatusFound)
			return
		}
		v := url.Values{"post_logout_redirect_uri": {home}, "client_id": {a.cfg.ClientID}}
		http.Redirect(w, r, p.EndSessionURL+"?"+v.Encode(), http.StatusFound)
	})
}

// Require lets the request through with its session in the context, or
// sends a browser navigation to the login and refuses anything else with
// 401.
func (a *Authenticator) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s, ok := SessionFromRequest(a.store, r, time.Now()); ok {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, s)))
			return
		}
		if isNavigation(r) {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/.portico/login?rd="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		writeJSONError(w, http.StatusUnauthorized, "portico session required")
	})
}

func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}
