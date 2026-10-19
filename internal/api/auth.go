package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/marco-fabbri/portico/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// adminAccount is the break-glass account name (design §4.2).
const adminAccount = "admin"

// BasicAuth protects a handler with HTTP Basic against the break-glass
// account stored as a bcrypt hash. A missing or wrong credential gets 401
// with a challenge; the password never reaches a log line.
func BasicAuth(st store.Store, realm string, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			if !ok || !checkPassword(r.Context(), st, user, pass, log) {
				w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`", charset="UTF-8"`)
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if PasswordChangeRequired(r.Context(), st) {
				writeError(w, http.StatusForbidden, "password change required: sign in to the admin UI once and set a new password")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// FactoryPassword is the break-glass password every appliance starts with
// (Nutanix's own convention, nutanix/4u): usable once, on the admin UI, to
// set a real one. The API refuses it (403) until then.
const FactoryPassword = "portico/4u"

// BcryptCost is the work factor of the stored hash; tests lower it.
var BcryptCost = 12

const changeRequiredKey = "admin.password_change_required"

// EnsureFactoryPassword stores the factory password when no admin
// password exists, flagged as "to be changed". It reports whether it did.
func EnsureFactoryPassword(ctx context.Context, st store.Store) (bool, error) {
	if _, err := st.AdminPasswordHash(ctx, adminAccount); err == nil {
		return false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(FactoryPassword), BcryptCost)
	if err != nil {
		return false, err
	}
	if err := st.SetAdminPassword(ctx, adminAccount, string(hash)); err != nil {
		return false, err
	}
	return true, st.SetSetting(ctx, changeRequiredKey, []byte("1"))
}

// PasswordChangeRequired reports whether the admin still has the factory
// password (or one an operator flagged for a change).
func PasswordChangeRequired(ctx context.Context, st store.Store) bool {
	v, err := st.GetSetting(ctx, changeRequiredKey)
	return err == nil && string(v) == "1"
}

// SetAdminPassword stores a new password and clears the change flag.
func SetAdminPassword(ctx context.Context, st store.Store, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return err
	}
	if err := st.SetAdminPassword(ctx, adminAccount, string(hash)); err != nil {
		return err
	}
	return st.SetSetting(ctx, changeRequiredKey, []byte("0"))
}

func checkPassword(ctx context.Context, st store.Store, user, pass string, log *slog.Logger) bool {
	hash, err := st.AdminPasswordHash(ctx, adminAccount)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Warn("admin password lookup failed", "err", err.Error())
		}
		// Keep the timing close to a real comparison.
		hash = "$2a$12$000000000000000000000uGQ9Q8WQfJQ9QfJQ9QfJQ9QfJQ9QfJQ9"
	}
	start := time.Now()
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(adminAccount)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) == nil
	if !userOK || !passOK || err != nil {
		log.Warn("admin authentication failed", "user_ok", userOK, "duration_ms", time.Since(start).Milliseconds())
		return false
	}
	return true
}
