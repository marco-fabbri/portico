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
	Tenants []store.Tenant
	Polling bool
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
	d := listData{Tenants: ts}
	for _, t := range ts {
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

type newData struct{ Slug, DisplayName, Contacts string }

func (h *Handler) tenantNew(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "tenant_new", "New tenant", newData{}, "")
}

func (h *Handler) tenantCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	d := newData{Slug: strings.TrimSpace(r.PostFormValue("slug")), DisplayName: strings.TrimSpace(r.PostFormValue("display_name")), Contacts: strings.TrimSpace(r.PostFormValue("contacts"))}
	if !tenant.ValidSlug(d.Slug) {
		h.render(w, r, http.StatusBadRequest, "tenant_new", "New tenant", d, tenant.ErrInvalidSlug.Error())
		return
	}
	t, err := h.svc.Create(r.Context(), tenant.CreateRequest{Slug: d.Slug, DisplayName: d.DisplayName, Contacts: d.Contacts})
	if err != nil {
		h.render(w, r, statusOf(err), "tenant_new", "New tenant", d, err.Error())
		return
	}
	http.Redirect(w, r, "/tenants/"+url.PathEscape(t.ID), http.StatusSeeOther)
}

type detailData struct {
	Tenant     *store.Tenant
	Steps      []store.Step
	Visibility *tenant.Visibility
	VisError   string
	Polling    bool
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
	steps, _ := h.st.ListSteps(r.Context(), t.ID)
	d := detailData{Tenant: t, Steps: steps, Polling: inProgress(t.Status)}
	// The visibility check calls Prism Central: only when the objects exist.
	if t.Status == store.StatusActive || t.Status == store.StatusDisabled {
		v, err := h.svc.Visibility(r.Context(), t.ID)
		if err != nil {
			d.VisError = err.Error()
		} else {
			d.Visibility = v
		}
	}
	h.render(w, r, http.StatusOK, "tenant", t.DisplayNameOr(), d, "")
}

// tenantSteps is the steps fragment, polled every 5 s while in progress.
func (h *Handler) tenantSteps(w http.ResponseWriter, r *http.Request) {
	t, ok := h.loadTenant(w, r)
	if !ok {
		return
	}
	steps, _ := h.st.ListSteps(r.Context(), t.ID)
	d := detailData{Tenant: t, Steps: steps, Polling: inProgress(t.Status)}
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
	steps, _ := h.st.ListSteps(r.Context(), t.ID)
	h.render(w, r, status, "tenant", t.DisplayNameOr(), detailData{Tenant: t, Steps: steps, Polling: inProgress(t.Status)}, msg)
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
	case errors.Is(err, store.ErrExists), errors.Is(err, tenant.ErrExists), errors.Is(err, tenant.ErrBusy), errors.Is(err, tenant.ErrState):
		return http.StatusConflict
	case errors.Is(err, tenant.ErrInvalidSlug):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
