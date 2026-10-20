package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
)

// routes registers the tenant pages. Every page action has an API twin
// under /api/v1/ (ADR 0003); both call the same service.
func (h *Handler) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /tenants/new", h.running(h.tenantNew))
	mux.HandleFunc("POST /tenants", h.running(h.tenantCreate))
	mux.HandleFunc("GET /tenants/{id}", h.running(h.tenantDetail))
	mux.HandleFunc("GET /tenants/{id}/steps", h.running(h.tenantSteps))
	mux.HandleFunc("POST /tenants/{id}/users", h.running(h.tenantCreateUser))
	mux.HandleFunc("POST /tenants/{id}/disable", h.running(h.tenantAction("disable")))
	mux.HandleFunc("POST /tenants/{id}/enable", h.running(h.tenantAction("enable")))
	mux.HandleFunc("POST /tenants/{id}/provision", h.running(h.tenantAction("provision")))
	mux.HandleFunc("POST /tenants/{id}/delete", h.running(h.tenantDelete))
	mux.HandleFunc("POST /tenants/{id}/sites", h.running(h.tenantAddSite))
	mux.HandleFunc("POST /tenants/{id}/sites/{site}/remove", h.running(h.tenantRemoveSite))
	mux.HandleFunc("GET /settings/branding", h.brandingPage)
	mux.HandleFunc("POST /settings/branding", h.brandingSave)
	mux.HandleFunc("GET /settings/branding/logo", h.brandingLogo)
}

// running renders the setup-mode page with 503 while the tenant service
// is nil (design §4.7): the tenant pages have nothing to act on yet.
func (h *Handler) running(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.svc == nil {
			h.render(w, r, http.StatusServiceUnavailable, "setupmode", "Setup mode", nil, "")
			return
		}
		next(w, r)
	}
}

// inProgress reports whether a status still changes on its own.
func inProgress(s store.Status) bool {
	return s == store.StatusCreating || s == store.StatusDeleting
}

type listData struct {
	Tenants []tenantRow
	Polling bool
}

// tenantRow is a tenant in the list with the codes of its sites.
type tenantRow struct {
	store.Tenant
	Sites []string
}

// home is the tenant list; ?partial=rows (or an HTMX request) returns the
// table body only, polled every 10 s while a tenant is in progress.
func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	ts, err := h.st.ListTenants(r.Context())
	if err != nil {
		h.render(w, r, http.StatusInternalServerError, "tenants", "Tenants", listData{}, err.Error())
		return
	}
	d := listData{}
	for _, t := range ts {
		row := tenantRow{Tenant: t}
		ps, _ := h.st.ListPlacements(r.Context(), t.ID)
		for _, p := range ps {
			row.Sites = append(row.Sites, h.svc.SiteCode(p.SiteID))
			d.Polling = d.Polling || placementInProgress(p.Status)
		}
		d.Tenants = append(d.Tenants, row)
		if inProgress(t.Status) {
			d.Polling = true
		}
	}
	if r.URL.Query().Get("partial") == "rows" || r.Header.Get("HX-Request") == "true" {
		if !d.Polling {
			// Every run finished: reload the page once so the empty-list
			// text and the badges settle, and the polling stops.
			w.Header().Set("HX-Refresh", "true")
		}
		h.fragment(w, r, "tenants", "rows", d)
		return
	}
	h.render(w, r, http.StatusOK, "tenants", "Tenants", d, "")
}

// placementInProgress reports whether a placement still changes on its own.
func placementInProgress(s store.PlacementStatus) bool {
	return s == store.PlacementCreating || s == store.PlacementRemoving
}

type newData struct {
	Slug, DisplayName, Contacts string
	Sites                       []newSite
}

// newSite is one site row of the new-tenant form: whether it is chosen
// and the placement, prefilled with the site's defaults.
type newSite struct {
	ID, Code           string
	Checked            bool
	Cluster, Container string
}

// newSites builds the form's site rows; a single site is chosen already.
func (h *Handler) newSites(r *http.Request) []newSite {
	sites := h.svc.Sites()
	chosen := map[string]bool{}
	if r != nil && r.Method == http.MethodPost {
		for _, id := range r.PostForm["site"] {
			chosen[id] = true
		}
	}
	out := make([]newSite, 0, len(sites))
	for _, s := range sites {
		row := newSite{ID: s.ID, Code: s.Code, Cluster: s.ClusterExtID, Container: s.StorageContainerExtID, Checked: len(sites) == 1 || chosen[s.ID]}
		if r != nil && r.Method == http.MethodPost {
			row.Cluster, row.Container = strings.TrimSpace(r.PostFormValue("cluster_"+s.ID)), strings.TrimSpace(r.PostFormValue("container_"+s.ID))
		}
		out = append(out, row)
	}
	return out
}

func (h *Handler) tenantNew(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "tenant_new", "New tenant", newData{Sites: h.newSites(nil)}, "")
}

func (h *Handler) tenantCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	d := newData{Slug: strings.TrimSpace(r.PostFormValue("slug")), DisplayName: strings.TrimSpace(r.PostFormValue("display_name")), Contacts: strings.TrimSpace(r.PostFormValue("contacts")), Sites: h.newSites(r)}
	if !tenant.ValidSlug(d.Slug) {
		h.render(w, r, http.StatusBadRequest, "tenant_new", "New tenant", d, tenant.ErrInvalidSlug.Error())
		return
	}
	req := tenant.CreateRequest{Slug: d.Slug, DisplayName: d.DisplayName, Contacts: d.Contacts}
	for _, s := range d.Sites {
		if s.Checked {
			req.Sites = append(req.Sites, tenant.PlacementRequest{Site: s.ID, ClusterExtID: s.Cluster, StorageContainerExtID: s.Container})
		}
	}
	t, err := h.svc.Create(r.Context(), req)
	if err != nil {
		h.render(w, r, statusOf(err), "tenant_new", "New tenant", d, err.Error())
		return
	}
	http.Redirect(w, r, "/tenants/"+url.PathEscape(t.ID), http.StatusSeeOther)
}

type detailData struct {
	Tenant     *store.Tenant
	Steps      []stepRow
	Placements []placementView
	// Addable are the configured sites the tenant is not on.
	Addable  []tenant.Site
	VisError string
	Polling  bool
	// Retry offers the retry button: a failed tenant, or a failed site.
	Retry bool
}

// stepRow is a recorded step with its site's code ("" tenant-wide).
type stepRow struct {
	store.Step
	Site string
}

// placementView is a tenant on one site as the detail page shows it.
type placementView struct {
	store.Placement
	Code       string
	Visibility *tenant.Visibility
	Removable  bool
}

// detail gathers the detail page; withVisibility calls each site's Prism
// Central for the visibility check.
func (h *Handler) detail(r *http.Request, t *store.Tenant, withVisibility bool) detailData {
	d := detailData{Tenant: t, Polling: inProgress(t.Status), Retry: t.Status == store.StatusFailed && t.LastOp != store.OpDelete}
	steps, _ := h.st.ListSteps(r.Context(), t.ID)
	for _, s := range steps {
		code := ""
		if s.SiteID != "" {
			code = h.svc.SiteCode(s.SiteID)
		}
		d.Steps = append(d.Steps, stepRow{Step: s, Site: code})
	}
	ps, _ := h.st.ListPlacements(r.Context(), t.ID)
	on := map[string]bool{}
	var vis map[string]*tenant.Visibility
	if withVisibility && (t.Status == store.StatusActive || t.Status == store.StatusDisabled) {
		v, err := h.svc.Visibility(r.Context(), t.ID)
		if err != nil {
			d.VisError = err.Error()
		}
		vis = v
	}
	for _, p := range ps {
		on[p.SiteID] = true
		d.Polling = d.Polling || placementInProgress(p.Status)
		if p.Status == store.PlacementFailed && t.Status == store.StatusActive {
			d.Retry = true
		}
		pv := placementView{Placement: p, Code: h.svc.SiteCode(p.SiteID), Removable: len(ps) > 1 && !placementInProgress(p.Status)}
		if p.Status == store.PlacementActive {
			pv.Visibility = vis[p.SiteID]
		}
		d.Placements = append(d.Placements, pv)
	}
	for _, s := range h.svc.Sites() {
		if !on[s.ID] {
			d.Addable = append(d.Addable, s)
		}
	}
	return d
}

func (h *Handler) loadTenant(w http.ResponseWriter, r *http.Request) (*store.Tenant, bool) {
	t, err := h.st.GetTenant(r.Context(), r.PathValue("id"))
	if err != nil {
		h.render(w, r, statusOf(err), "notfound", "Not found", nil, "")
		return nil, false
	}
	return t, true
}

func (h *Handler) tenantDetail(w http.ResponseWriter, r *http.Request) {
	t, ok := h.loadTenant(w, r)
	if !ok {
		return
	}
	// The visibility check calls Prism Central: only when the objects exist.
	h.render(w, r, http.StatusOK, "tenant", t.DisplayNameOr(), h.detail(r, t, true), "")
}

// tenantSteps is the steps fragment, polled every 5 s while in progress.
func (h *Handler) tenantSteps(w http.ResponseWriter, r *http.Request) {
	t, ok := h.loadTenant(w, r)
	if !ok {
		return
	}
	d := h.detail(r, t, false)
	if r.Header.Get("HX-Request") == "true" && !d.Polling {
		// The run finished: tell HTMX to reload the whole page once.
		w.Header().Set("HX-Refresh", "true")
	}
	h.fragment(w, r, "tenant", "steps", d)
}

type userCreatedData struct {
	Tenant   *store.Tenant
	Email    string
	Password string
}

func (h *Handler) tenantCreateUser(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	t, ok := h.loadTenant(w, r)
	if !ok {
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	if email == "" || !strings.Contains(email, "@") {
		h.detailWithError(w, r, t, http.StatusBadRequest, "email is required")
		return
	}
	pw, err := h.svc.CreateUser(r.Context(), t.ID, email, strings.TrimSpace(r.PostFormValue("first_name")), strings.TrimSpace(r.PostFormValue("last_name")))
	if err != nil {
		h.detailWithError(w, r, t, statusOf(err), err.Error())
		return
	}
	// Shown once: the page is the only place the password ever appears.
	h.render(w, r, http.StatusCreated, "tenant_user_created", "User created", userCreatedData{Tenant: t, Email: email, Password: pw}, "")
}

// tenantAction runs disable, enable or provision (retry) and returns to
// the detail page; a refused action shows the reason on that page.
func (h *Handler) tenantAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.requireCSRF(w, r) {
			return
		}
		t, ok := h.loadTenant(w, r)
		if !ok {
			return
		}
		var err error
		switch action {
		case "disable":
			err = h.svc.Disable(r.Context(), t.ID)
		case "enable":
			err = h.svc.Enable(r.Context(), t.ID)
		case "provision":
			err = h.svc.Retry(r.Context(), t.ID)
		}
		if err != nil {
			h.detailWithError(w, r, t, statusOf(err), err.Error())
			return
		}
		http.Redirect(w, r, "/tenants/"+url.PathEscape(t.ID), http.StatusSeeOther)
	}
}

func (h *Handler) tenantDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	t, ok := h.loadTenant(w, r)
	if !ok {
		return
	}
	deleteProject := r.PostFormValue("delete_project") != ""
	if err := h.svc.Delete(r.Context(), t.ID, deleteProject); err != nil {
		h.detailWithError(w, r, t, statusOf(err), err.Error())
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// detailWithError re-renders the detail page with a banner.
func (h *Handler) detailWithError(w http.ResponseWriter, r *http.Request, t *store.Tenant, status int, msg string) {
	h.render(w, r, status, "tenant", t.DisplayNameOr(), h.detail(r, t, false), msg)
}

// tenantAddSite places the tenant on one more site (design sites §4.2).
func (h *Handler) tenantAddSite(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	t, ok := h.loadTenant(w, r)
	if !ok {
		return
	}
	req := tenant.PlacementRequest{Site: strings.TrimSpace(r.PostFormValue("site")), ClusterExtID: strings.TrimSpace(r.PostFormValue("cluster_ext_id")), StorageContainerExtID: strings.TrimSpace(r.PostFormValue("storage_container_ext_id"))}
	if err := h.svc.AddSite(r.Context(), t.ID, req); err != nil {
		h.detailWithError(w, r, t, statusOf(err), err.Error())
		return
	}
	h.log.Info("tenant site added", "via", "ui", "tenant", t.ID, "site", req.Site)
	http.Redirect(w, r, "/tenants/"+url.PathEscape(t.ID), http.StatusSeeOther)
}

// tenantRemoveSite takes the tenant off one site.
func (h *Handler) tenantRemoveSite(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	t, ok := h.loadTenant(w, r)
	if !ok {
		return
	}
	site := r.PathValue("site")
	deleteProject := r.PostFormValue("delete_project") != ""
	if err := h.svc.RemoveSite(r.Context(), t.ID, site, deleteProject); err != nil {
		h.detailWithError(w, r, t, statusOf(err), err.Error())
		return
	}
	h.log.Info("tenant site removal started", "via", "ui", "tenant", t.ID, "site", site, "delete_project", deleteProject)
	http.Redirect(w, r, "/tenants/"+url.PathEscape(t.ID), http.StatusSeeOther)
}

// fragment renders one named block of a page template without the layout.
func (h *Handler) fragment(w http.ResponseWriter, r *http.Request, pageName, block string, data any) {
	p := page{Version: h.version, Data: data}
	if s, ok := h.session(r); ok {
		p.Admin, p.CSRF = "admin", s.CSRF
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.pages[pageName].ExecuteTemplate(w, block, p); err != nil {
		h.log.Error("template", "page", pageName, "block", block, "err", err.Error())
	}
}

// statusOf maps service and store errors to HTTP statuses like the API.
func statusOf(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrExists), errors.Is(err, tenant.ErrExists), errors.Is(err, tenant.ErrBusy), errors.Is(err, tenant.ErrState),
		errors.Is(err, tenant.ErrPlacementExists), errors.Is(err, tenant.ErrLastSite):
		return http.StatusConflict
	case errors.Is(err, tenant.ErrInvalidSlug), errors.Is(err, tenant.ErrUnknownSite), errors.Is(err, tenant.ErrNoSites), errors.Is(err, tenant.ErrNoPlacementTarget):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
