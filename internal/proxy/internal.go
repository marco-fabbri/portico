package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// NewPassthrough forwards every request to Prism Central unchanged: no
// session, no policy, only the access log. It is bound to the internal
// listener, where local Prism Central accounts are allowed.
func NewPassthrough(prism http.Handler, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return AccessLog(log, prism)
}

// NewAdmin serves the internal admin listener: GET /healthz, open; the
// REST API under /api/v1/ when api is not nil; the admin UI on every
// other path when ui is not nil. The caller wraps both with the
// authentication middleware.
func NewAdmin(log *slog.Logger, api, ui http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	if api != nil {
		mux.Handle("/api/v1/", api)
	}
	if ui != nil {
		mux.Handle("/", ui)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		})
	}
	return AccessLog(log, mux)
}
