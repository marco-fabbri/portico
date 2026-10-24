package web

import (
	"net/http"

	"github.com/marco-fabbri/portico/internal/api"
	"golang.org/x/crypto/bcrypt"
)

type passwordData struct {
	Required bool
}

// passwordPage is where the factory password is replaced (forced after
// the first login) and where an administrator changes the password later.
func (h *Handler) passwordPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(r); !ok {
		http.Redirect(w, r, "/login?next=%2Fpassword", http.StatusSeeOther)
		return
	}
	h.render(w, r, http.StatusOK, "password", "Admin password", passwordData{Required: api.PasswordChangeRequired(r.Context(), h.st)}, "")
}

func (h *Handler) passwordSubmit(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	d := passwordData{Required: api.PasswordChangeRequired(r.Context(), h.st)}
	fail := func(msg string) { h.render(w, r, http.StatusOK, "password", "Admin password", d, msg) }
	current, next, confirm := r.PostFormValue("current"), r.PostFormValue("new_password"), r.PostFormValue("confirm")
	hash, err := h.st.AdminPasswordHash(r.Context(), "admin")
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		fail("the current password is wrong")
		return
	}
	switch {
	case len(next) < 12:
		fail("the new password needs at least 12 characters")
		return
	case next != confirm:
		fail("the two entries do not match")
		return
	case next == api.FactoryPassword:
		fail("the factory password cannot stay")
		return
	case next == current:
		fail("the new password must differ from the current one")
		return
	}
	if err := api.SetAdminPassword(r.Context(), h.st, next); err != nil {
		h.log.Error("admin password change", "err", err.Error())
		fail("the password could not be stored")
		return
	}
	// A password is usually changed because someone else may know it:
	// every admin session ends, and this browser gets a fresh one.
	n := h.sessions.DeleteSubject("admin")
	h.newSession(w)
	h.log.Info("admin password changed", "source", sourceOf(r), "sessions_ended", n)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
