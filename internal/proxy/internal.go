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

// NewAdmin serves the internal admin listener: GET /healthz now, the admin
// UI in Milestone 2.
func NewAdmin(log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	return AccessLog(log, mux)
}
