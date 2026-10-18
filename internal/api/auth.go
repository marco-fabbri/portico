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
			next.ServeHTTP(w, r)
		})
	}
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
