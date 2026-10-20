package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/sites"
)

// SitesRuntime is the live state of the sites (design sites §4.1): the
// status, the maintenance flag, the tenant sessions on each site. It
// exists in running mode only.
type SitesRuntime interface {
	Status(siteID string) sites.Status
	Maintenance(siteID string) bool
	SetMaintenance(ctx context.Context, siteID string, on bool) error
	Sessions(siteID string, now time.Time) []auth.Session
	Disconnect(siteID string) int
}

// Runtime is the SitesRuntime of a running Portico: the site monitor and
// the tenant session store.
type Runtime struct {
	Monitor *sites.Monitor
	Store   *auth.MemoryStore // the tenant sessions
}

func (r *Runtime) Status(id string) sites.Status { return r.Monitor.Status(id) }
func (r *Runtime) Maintenance(id string) bool    { return r.Monitor.Maintenance(id) }
func (r *Runtime) SetMaintenance(ctx context.Context, id string, on bool) error {
	return r.Monitor.SetMaintenance(ctx, id, on)
}

// Sessions lists the live tenant sessions on the site, most recent
// activity first.
func (r *Runtime) Sessions(id string, now time.Time) []auth.Session {
	var out []auth.Session
	for _, s := range r.Store.List(now) {
		if s.Site == id {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// Disconnect takes every session off the site.
func (r *Runtime) Disconnect(id string) int { return r.Store.ClearSite(id) }

// SessionCounts splits the site's sessions into active (a request within
// auth.ActiveWindow) and idle.
func SessionCounts(ss []auth.Session, now time.Time) (active, idle int) {
	for _, s := range ss {
		if now.Sub(s.LastSeen) <= auth.ActiveWindow {
			active++
		} else {
			idle++
		}
	}
	return active, idle
}

// sessionJSON is a tenant session on a site, as the MSP sees it: never
// the session id.
type sessionJSON struct {
	Tenant       string    `json:"tenant"`
	Email        string    `json:"email"`
	Since        time.Time `json:"since"`
	LastActivity time.Time `json:"last_activity"`
	State        string    `json:"state"`
}

func (h *handler) runtimeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT "+Prefix+"sites/{id}/maintenance", h.withRuntime(h.setMaintenance))
	mux.HandleFunc("GET "+Prefix+"sites/{id}/sessions", h.withRuntime(h.siteSessions))
	mux.HandleFunc("POST "+Prefix+"sites/{id}/sessions/disconnect", h.withRuntime(h.disconnectSite))
}

// withRuntime answers 503 in setup mode and 404 for an unknown site.
func (h *handler) withRuntime(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.so == nil || h.so.Runtime == nil {
			writeError(w, http.StatusServiceUnavailable, SetupModeMessage)
			return
		}
		cur, _, err := h.so.Current(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, ok := cur.Site(r.PathValue("id")); !ok {
			writeError(w, http.StatusNotFound, ErrUnknownSite.Error())
			return
		}
		next(w, r)
	}
}

func (h *handler) setMaintenance(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Maintenance bool `json:"maintenance"`
	}
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	id := r.PathValue("id")
	if err := h.so.Runtime.SetMaintenance(r.Context(), id, in.Maintenance); err != nil {
		h.log.Error("maintenance not stored", "site", id, "err", err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.log.Warn("site maintenance set", "site", id, "maintenance", in.Maintenance)
	writeJSON(w, http.StatusOK, map[string]any{"site": id, "maintenance": in.Maintenance, "status": h.so.Runtime.Status(id)})
}

func (h *handler) siteSessions(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	ss := h.so.Runtime.Sessions(r.PathValue("id"), now)
	active, idle := SessionCounts(ss, now)
	out := make([]sessionJSON, 0, len(ss))
	for _, s := range ss {
		state := "idle"
		if now.Sub(s.LastSeen) <= auth.ActiveWindow {
			state = "active"
		}
		out = append(out, sessionJSON{Tenant: s.Tenant, Email: s.Email, Since: s.SiteSince, LastActivity: s.LastSeen, State: state})
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active, "idle": idle, "sessions": out})
}

func (h *handler) disconnectSite(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n := h.so.Runtime.Disconnect(id)
	h.log.Warn("site sessions disconnected", "site", id, "disconnected", n)
	writeJSON(w, http.StatusOK, map[string]int{"disconnected": n})
}
