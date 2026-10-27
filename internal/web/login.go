package web

import (
	"errors"
	"github.com/marco-fabbri/portico/internal/api"
	"net"
	"net/http"
	"strings"

	"github.com/marco-fabbri/portico/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type loginData struct{ Next string }

// passwordSet reports whether the break-glass hash exists.
func (h *Handler) passwordSet(r *http.Request) bool {
	_, err := h.st.AdminPasswordHash(r.Context(), "admin")
	return err == nil
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if !h.passwordSet(r) {
		h.render(w, r, http.StatusServiceUnavailable, "unset", "Password not set", nil, "")
		return
	}
	if _, ok := h.session(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.render(w, r, http.StatusOK, "login", "Sign in", loginData{Next: safeNext(r.URL.Query().Get("next"))}, "")
}

func (h *Handler) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !h.passwordSet(r) {
		h.render(w, r, http.StatusServiceUnavailable, "unset", "Password not set", nil, "")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, "login", "Sign in", loginData{}, "invalid form")
		return
	}
	user, pass := r.PostFormValue("user"), r.PostFormValue("password")
	next := safeNext(r.PostFormValue("next"))
	source := sourceOf(r)
	if wait := h.limiter.Wait(source); wait > 0 {
		w.Header().Set("Retry-After", api.RetryAfter(wait))
		h.render(w, r, http.StatusTooManyRequests, "login", "Sign in", loginData{Next: next}, "Too many failed sign-ins from this address: wait "+api.RetryAfter(wait)+" seconds and try again")
		return
	}
	hash, err := h.st.AdminPasswordHash(r.Context(), "admin")
	ok := err == nil && user == "admin" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) == nil
	if !ok {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			h.log.Warn("admin password lookup failed", "err", err.Error())
		}
		h.limiter.Failed(source)
		h.log.Warn("admin login failed", "source", source)
		h.render(w, r, http.StatusOK, "login", "Sign in", loginData{Next: next}, "Wrong user or password")
		return
	}
	h.limiter.Reset(source)
	// A sign-in replaces the session this browser held.
	if c, err := r.Cookie(SessionCookie); err == nil {
		h.sessions.Delete(c.Value)
	}
	h.newSession(w)
	h.log.Info("admin login", "source", source)
	if api.PasswordChangeRequired(r.Context(), h.st) {
		next = "/password"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		h.sessions.Delete(c.Value)
	}
	h.setCookie(w, "", -1)
	h.log.Info("admin logout")
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext keeps a return path on this host: a path starting with one
// slash, never a URL or a protocol-relative reference.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	return next
}

func sourceOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
