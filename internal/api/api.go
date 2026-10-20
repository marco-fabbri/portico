// Package api is Portico's administration REST API on the internal
// listener. It is the boundary between any front end and the logic (ADR
// 0003): the admin UI, a script or Ansible all go through these calls.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/branding"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
)

// Prefix is where the API lives on the admin listener.
const Prefix = "/api/v1/"

type handler struct {
	svc   *tenant.Service
	st    store.Store
	brand *branding.Service
	so    *SettingsOptions
	log   *slog.Logger
}

// SetupModeMessage is the answer of every tenant route while Portico
// runs in setup mode (design §4.7): the service that would act on
// Prism Central and Keycloak does not exist yet.
const SetupModeMessage = "Portico is in setup mode: complete the settings and apply them"

// running refuses the route with 503 while the tenant service is nil.
func (h *handler) running(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.svc == nil {
			writeError(w, http.StatusServiceUnavailable, SetupModeMessage)
			return
		}
		next(w, r)
	}
}

// New returns the API handler. The caller wraps it with the
// authentication middleware. so may be nil: the settings routes then
// answer 501. svc may be nil (setup mode): the tenant routes then
// answer 503.
func New(svc *tenant.Service, st store.Store, brand *branding.Service, so *SettingsOptions, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	if brand == nil {
		brand = branding.NewService(st, log)
	}
	if so != nil && so.Checker == nil {
		so.Checker = settings.NewChecker(log)
	}
	if so != nil && so.Bootstrapper == nil {
		so.Bootstrapper = settings.NewBootstrapper(log, so.Service)
	}
	h := &handler{svc: svc, st: st, brand: brand, so: so, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"settings", h.getSettings)
	mux.HandleFunc("PUT "+Prefix+"settings", h.putSettings)
	mux.HandleFunc("POST "+Prefix+"settings/check", h.checkSettings)
	mux.HandleFunc("POST "+Prefix+"settings/apply", h.applySettings)
	mux.HandleFunc("POST "+Prefix+"settings/bootstrap/keycloak", h.bootstrapKeycloak)
	h.siteRoutes(mux)
	mux.HandleFunc("GET "+Prefix+"settings/branding", h.getBranding)
	mux.HandleFunc("PUT "+Prefix+"settings/branding", h.putBranding)
	mux.HandleFunc("GET "+Prefix+"settings/branding/logo", h.getLogo)
	mux.HandleFunc("GET "+Prefix+"tenants", h.running(h.listTenants))
	mux.HandleFunc("POST "+Prefix+"tenants", h.running(h.createTenant))
	mux.HandleFunc("GET "+Prefix+"tenants/{id}", h.running(h.getTenant))
	mux.HandleFunc("GET "+Prefix+"tenants/{id}/steps", h.running(h.listSteps))
	mux.HandleFunc("POST "+Prefix+"tenants/{id}/provision", h.running(h.retry))
	mux.HandleFunc("DELETE "+Prefix+"tenants/{id}", h.running(h.deleteTenant))
	mux.HandleFunc("POST "+Prefix+"tenants/{id}/disable", h.running(h.disable))
	mux.HandleFunc("POST "+Prefix+"tenants/{id}/enable", h.running(h.enable))
	mux.HandleFunc("POST "+Prefix+"tenants/{id}/users", h.running(h.createUser))
	mux.HandleFunc("GET "+Prefix+"tenants/{id}/visibility", h.running(h.visibility))
	mux.HandleFunc("POST "+Prefix+"tenants/{id}/sites", h.running(h.addSite))
	mux.HandleFunc("DELETE "+Prefix+"tenants/{id}/sites/{site}", h.running(h.removeSite))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return mux
}

// tenantJSON is the wire shape of a tenant: the row and one entry per
// site it is placed on (design sites §4.2).
type tenantJSON struct {
	ID              string          `json:"id"`
	DisplayName     string          `json:"display_name"`
	Status          string          `json:"status"`
	Contacts        string          `json:"contacts"`
	KeycloakGroupID string          `json:"keycloak_group_id"`
	LastError       string          `json:"last_error"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Placements      []placementJSON `json:"placements"`
}

// placementJSON is a tenant on one site.
type placementJSON struct {
	Site                  string `json:"site"`
	Code                  string `json:"code"`
	Status                string `json:"status"`
	ClusterExtID          string `json:"cluster_ext_id"`
	StorageContainerExtID string `json:"storage_container_ext_id"`
	ProjectID             string `json:"project_id"`
	ProjectExtID          string `json:"project_ext_id"`
	ResourceGroupExtID    string `json:"resource_group_ext_id"`
	SAMLGroupExtID        string `json:"saml_group_ext_id"`
	RoleMembershipExtID   string `json:"role_membership_ext_id"`
	IdPShared             bool   `json:"idp_shared"`
	LastError             string `json:"last_error"`
}

// toJSON builds a tenant's wire shape with its placements.
func (h *handler) toJSON(ctx context.Context, t *store.Tenant) (tenantJSON, error) {
	ps, err := h.st.ListPlacements(ctx, t.ID)
	if err != nil {
		return tenantJSON{}, err
	}
	out := tenantJSON{
		ID: t.ID, DisplayName: t.DisplayName, Status: string(t.Status), Contacts: t.Contacts,
		KeycloakGroupID: t.KeycloakGroupID, LastError: t.LastError, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Placements: make([]placementJSON, 0, len(ps)),
	}
	for _, p := range ps {
		out.Placements = append(out.Placements, placementJSON{
			Site: p.SiteID, Code: h.svc.SiteCode(p.SiteID), Status: string(p.Status),
			ClusterExtID: p.ClusterExtID, StorageContainerExtID: p.StorageContainerExtID,
			ProjectID: p.ProjectID, ProjectExtID: p.ProjectExtID, ResourceGroupExtID: p.ResourceGroupExtID,
			SAMLGroupExtID: p.SAMLGroupExtID, RoleMembershipExtID: p.RoleMembershipExtID, IdPShared: p.IdPShared, LastError: p.LastError,
		})
	}
	return out, nil
}

// writeTenant answers a tenant with its placements.
func (h *handler) writeTenant(w http.ResponseWriter, r *http.Request, status int, t *store.Tenant) {
	j, err := h.toJSON(r.Context(), t)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, status, j)
}

type stepJSON struct {
	Site   string    `json:"site"`
	Name   string    `json:"name"`
	OK     bool      `json:"ok"`
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail maps service and store errors to statuses. Messages come from the
// errors themselves, which never carry secrets.
func (h *handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "tenant not found")
	case errors.Is(err, store.ErrExists), errors.Is(err, tenant.ErrExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, tenant.ErrBusy), errors.Is(err, tenant.ErrState), errors.Is(err, tenant.ErrPlacementExists), errors.Is(err, tenant.ErrLastSite):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, tenant.ErrInvalidSlug), errors.Is(err, tenant.ErrUnknownSite), errors.Is(err, tenant.ErrNoSites), errors.Is(err, tenant.ErrNoPlacementTarget):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		h.log.Error("api request failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func (h *handler) listTenants(w http.ResponseWriter, r *http.Request) {
	ts, err := h.st.ListTenants(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]tenantJSON, 0, len(ts))
	for i := range ts {
		j, err := h.toJSON(r.Context(), &ts[i])
		if err != nil {
			h.fail(w, err)
			return
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) createTenant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Slug        string                 `json:"slug"`
		DisplayName string                 `json:"display_name"`
		Contacts    string                 `json:"contacts"`
		Sites       []placementRequestJSON `json:"sites"`
	}
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if !tenant.ValidSlug(in.Slug) {
		writeError(w, http.StatusBadRequest, tenant.ErrInvalidSlug.Error())
		return
	}
	req := tenant.CreateRequest{Slug: in.Slug, DisplayName: strings.TrimSpace(in.DisplayName), Contacts: strings.TrimSpace(in.Contacts)}
	for _, p := range in.Sites {
		req.Sites = append(req.Sites, p.request())
	}
	t, err := h.svc.Create(r.Context(), req)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.writeTenant(w, r, http.StatusAccepted, t)
}

func (h *handler) getTenant(w http.ResponseWriter, r *http.Request) {
	t, err := h.st.GetTenant(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.writeTenant(w, r, http.StatusOK, t)
}

func (h *handler) listSteps(w http.ResponseWriter, r *http.Request) {
	if _, err := h.st.GetTenant(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	steps, err := h.st.ListSteps(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]stepJSON, 0, len(steps))
	for _, s := range steps {
		out = append(out, stepJSON{Site: s.SiteID, Name: s.Name, OK: s.OK, Detail: s.Detail, At: s.At})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) retry(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Retry(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	t, err := h.st.GetTenant(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.writeTenant(w, r, http.StatusAccepted, t)
}

func (h *handler) deleteTenant(w http.ResponseWriter, r *http.Request) {
	deleteProject := false
	if v := r.URL.Query().Get("delete_project"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "delete_project must be true or false")
			return
		}
		deleteProject = b
	}
	if err := h.svc.Delete(r.Context(), r.PathValue("id"), deleteProject); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": r.PathValue("id"), "status": string(store.StatusDeleting), "delete_project": deleteProject})
}

func (h *handler) disable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, false)
}

func (h *handler) enable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, true)
}

func (h *handler) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	var err error
	if enabled {
		err = h.svc.Enable(r.Context(), r.PathValue("id"))
	} else {
		err = h.svc.Disable(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	t, err := h.st.GetTenant(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.writeTenant(w, r, http.StatusOK, t)
}

func (h *handler) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email     string `json:"email"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if in.Email == "" || !strings.Contains(in.Email, "@") {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	pw, err := h.svc.CreateUser(r.Context(), r.PathValue("id"), in.Email, strings.TrimSpace(in.FirstName), strings.TrimSpace(in.LastName))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": r.PathValue("id"), "email": in.Email, "temporary_password": pw})
}

func (h *handler) visibility(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.Visibility(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": v})
}

// placementRequestJSON places a tenant on a site; empty cluster and
// container take the site's defaults.
type placementRequestJSON struct {
	Site                  string `json:"site"`
	ClusterExtID          string `json:"cluster_ext_id"`
	StorageContainerExtID string `json:"storage_container_ext_id"`
}

func (p placementRequestJSON) request() tenant.PlacementRequest {
	return tenant.PlacementRequest{Site: strings.TrimSpace(p.Site), ClusterExtID: strings.TrimSpace(p.ClusterExtID), StorageContainerExtID: strings.TrimSpace(p.StorageContainerExtID)}
}

func (h *handler) addSite(w http.ResponseWriter, r *http.Request) {
	var in placementRequestJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	id := r.PathValue("id")
	if err := h.svc.AddSite(r.Context(), id, in.request()); err != nil {
		h.fail(w, err)
		return
	}
	h.log.Info("tenant site added", "tenant", id, "site", in.Site)
	t, err := h.st.GetTenant(r.Context(), id)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.writeTenant(w, r, http.StatusAccepted, t)
}

func (h *handler) removeSite(w http.ResponseWriter, r *http.Request) {
	deleteProject := false
	if v := r.URL.Query().Get("delete_project"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "delete_project must be true or false")
			return
		}
		deleteProject = b
	}
	id, site := r.PathValue("id"), r.PathValue("site")
	if err := h.svc.RemoveSite(r.Context(), id, site, deleteProject); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "the tenant is not placed on site "+site)
			return
		}
		h.fail(w, err)
		return
	}
	h.log.Info("tenant site removal started", "tenant", id, "site", site, "delete_project", deleteProject)
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "site": site, "status": string(store.PlacementRemoving), "delete_project": deleteProject})
}
