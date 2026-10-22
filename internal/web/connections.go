package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
)

// connectionRoutes registers the pages that connect a tenant to a site,
// suspend, resume, disconnect and retry it, and delete a tenant (design
// sites usability 4.4 to 4.6). Every state change is a POST; the GET of
// the same path is its confirmation page.
func (h *Handler) connectionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /targets", h.running(h.targets))
	mux.HandleFunc("POST /setup/sites/{id}/tenants", h.running(h.siteConnect))
	mux.HandleFunc("GET /tenants/{id}/sites/{site}/suspend", h.running(h.suspendConfirm))
	mux.HandleFunc("POST /tenants/{id}/sites/{site}/suspend", h.running(h.connectionAction("suspend")))
	mux.HandleFunc("POST /tenants/{id}/sites/{site}/resume", h.running(h.connectionAction("resume")))
	mux.HandleFunc("GET /tenants/{id}/sites/{site}/disconnect", h.running(h.disconnectConfirm))
	mux.HandleFunc("POST /tenants/{id}/sites/{site}/disconnect", h.running(h.connectionAction("disconnect")))
	mux.HandleFunc("POST /tenants/{id}/sites/{site}/retry", h.running(h.connectionAction("retry")))
	mux.HandleFunc("GET /tenants/{id}/delete", h.running(h.deleteConfirm))
}

// connectionRow is a tenant on one site as the tenant and site pages list
// it. From is "site" on the site page, so an action returns there.
type connectionRow struct {
	store.Placement
	Tenant, Code, SiteName string
	Users                  int
	UsersKnown             bool
	CSRF, From             string
}

func (c connectionRow) CanSuspend() bool { return c.Status == store.PlacementActive }
func (c connectionRow) CanResume() bool  { return c.Status == store.PlacementSuspended }
func (c connectionRow) CanRetry() bool {
	return c.Status == store.PlacementFailed || c.Status == store.PlacementRemoveFailed
}
func (c connectionRow) CanDisconnect() bool { return !placementInProgress(c.Status) }

// siteOf returns the tenant service's site.
func (h *Handler) siteOf(id string) (tenant.Site, bool) {
	for _, s := range h.svc.Sites() {
		if s.ID == id {
			return s, true
		}
	}
	return tenant.Site{}, false
}

// usersOn counts the tenant's sessions on the site now; known is false
// without a runtime (setup mode, or a test without one).
func (h *Handler) usersOn(siteID, tenantID string) (n int, known bool) {
	if h.setup == nil || h.setup.Runtime == nil {
		return 0, false
	}
	for _, s := range h.setup.Runtime.Sessions(siteID, time.Now()) {
		if s.Tenant == tenantID {
			n++
		}
	}
	return n, true
}

// connectionRows builds the rows of the placements; name gives a tenant's
// display name.
func (h *Handler) connectionRows(r *http.Request, ps []store.Placement, from string, name func(id string) string) []connectionRow {
	csrf := ""
	if s, ok := h.session(r); ok {
		csrf = s.CSRF
	}
	out := make([]connectionRow, 0, len(ps))
	for _, p := range ps {
		row := connectionRow{Placement: p, Tenant: name(p.TenantID), Code: h.svc.SiteCode(p.SiteID), CSRF: csrf, From: from}
		if s, ok := h.siteOf(p.SiteID); ok {
			row.SiteName = s.Name
		}
		row.Users, row.UsersKnown = h.usersOn(p.SiteID, p.TenantID)
		out = append(out, row)
	}
	return out
}

// back is where an action on a connection returns: the site page when it
// was started there, otherwise the tenant page.
func back(r *http.Request, tenantID, siteID string) string {
	if r.FormValue("from") == "site" {
		return "/setup/sites/" + url.PathEscape(siteID)
	}
	return "/tenants/" + url.PathEscape(tenantID)
}

// confirmData is a confirmation page: a question, what happens, and one
// button; without Action it only explains why nothing can be done.
type confirmData struct {
	Title, Text    string
	List           []string
	Note           string
	Link, LinkText string
	Action, Button string
	Danger         bool
	From, Back     string
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request, d confirmData) {
	h.render(w, r, http.StatusOK, "confirm", d.Title, d, "")
}

// loadConnection returns the tenant, its placement on the path's site
// and that site, or renders the not-found page.
func (h *Handler) loadConnection(w http.ResponseWriter, r *http.Request) (*store.Tenant, *store.Placement, tenant.Site, bool) {
	t, ok := h.loadTenant(w, r)
	if !ok {
		return nil, nil, tenant.Site{}, false
	}
	p, err := h.st.GetPlacement(r.Context(), t.ID, r.PathValue("site"))
	site, known := h.siteOf(r.PathValue("site"))
	if err != nil || !known {
		h.render(w, r, http.StatusNotFound, "notfound", "Not found", nil, "the tenant is not connected to that site")
		return nil, nil, tenant.Site{}, false
	}
	return t, p, site, true
}

func (h *Handler) suspendConfirm(w http.ResponseWriter, r *http.Request) {
	t, p, site, ok := h.loadConnection(w, r)
	if !ok {
		return
	}
	d := confirmData{Title: fmt.Sprintf("Suspend %s on %s?", t.DisplayNameOr(), site.Code), From: r.FormValue("from"), Back: back(r, t.ID, site.ID)}
	if p.Status != store.PlacementActive {
		d.Text = fmt.Sprintf("The connection is %s: only an active one can be suspended.", p.Status)
		h.confirm(w, r, d)
		return
	}
	d.Text = "Their open VM consoles finish; nothing else opens until you resume. VMs keep running."
	if n, known := h.usersOn(site.ID, t.ID); known {
		d.Text = fmt.Sprintf("%d of its users are on this site now. ", n) + d.Text
	}
	d.Action, d.Button = r.URL.Path, "Suspend"
	h.confirm(w, r, d)
}

func (h *Handler) disconnectConfirm(w http.ResponseWriter, r *http.Request) {
	t, p, site, ok := h.loadConnection(w, r)
	if !ok {
		return
	}
	d := confirmData{Title: fmt.Sprintf("Disconnect %s from %s?", t.DisplayNameOr(), site.Code), From: r.FormValue("from"), Back: back(r, t.ID, site.ID)}
	if placementInProgress(p.Status) {
		d.Text = fmt.Sprintf("The connection is %s: wait until it ends.", p.Status)
		h.confirm(w, r, d)
		return
	}
	c, err := h.svc.Contents(r.Context(), t.ID, site.ID)
	switch {
	case err != nil:
		d.Text = "Portico could not count what the project holds, so it does not disconnect now: " + err.Error() + ". Try again in a moment."
	case !c.Empty():
		d.Title = fmt.Sprintf("%s cannot be disconnected from %s yet", t.DisplayNameOr(), site.Code)
		d.Text = fmt.Sprintf("The project %s still holds:", p.ProjectID)
		d.List = c.Lines()
		d.Note = "Portico never deletes what the tenant made: empty the project in Prism Central, then disconnect again."
		d.Link, d.LinkText = site.URL, "Open the Prism Central of "+site.Code
	default:
		d.Text = fmt.Sprintf("Portico deletes the project %s, its resource group, the SAML group and the role membership in that Prism Central.", p.ProjectID)
		if u := c.UncountedNames(); len(u) > 0 {
			d.Note = "Not counted, as Prism Central does not list them for Portico's account: " + strings.Join(u, ", ") +
				". If the project still holds any, Prism Central refuses to delete it and the connection ends remove-failed."
		}
		d.Action, d.Button, d.Danger = r.URL.Path, "Disconnect", true
	}
	h.confirm(w, r, d)
}

func (h *Handler) deleteConfirm(w http.ResponseWriter, r *http.Request) {
	t, ok := h.loadTenant(w, r)
	if !ok {
		return
	}
	d := confirmData{Title: fmt.Sprintf("Delete %s?", t.DisplayNameOr()), Back: "/tenants/" + url.PathEscape(t.ID)}
	if ps, _ := h.st.ListPlacements(r.Context(), t.ID); len(ps) > 0 {
		d.Text = "Disconnect it from every site to delete it."
		h.confirm(w, r, d)
		return
	}
	n, err := h.svc.UserCount(r.Context(), t.ID)
	if err != nil {
		d.Text = "Its Keycloak group and its users are deleted (Keycloak did not say how many: " + err.Error() + ")."
	} else {
		d.Text = fmt.Sprintf("Its Keycloak group and its %d %s are deleted.", n, plural(n, "user", "users"))
	}
	d.Action, d.Button, d.Danger = r.URL.Path, "Delete the tenant", true
	h.confirm(w, r, d)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// connectionAction suspends, resumes, disconnects or retries one
// connection and returns to the page it was started from.
func (h *Handler) connectionAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.requireCSRF(w, r) {
			return
		}
		t, ok := h.loadTenant(w, r)
		if !ok {
			return
		}
		site := r.PathValue("site")
		var err error
		switch action {
		case "suspend":
			err = h.svc.Suspend(r.Context(), t.ID, site)
		case "resume":
			err = h.svc.Resume(r.Context(), t.ID, site)
		case "disconnect":
			err = h.svc.Disconnect(r.Context(), t.ID, site)
		case "retry":
			err = h.svc.RetrySite(r.Context(), t.ID, site)
		}
		if err != nil {
			h.detailWithError(w, r, t, statusOf(err), err.Error())
			return
		}
		h.log.Info("tenant connection "+action, "via", "ui", "tenant", t.ID, "site", site)
		http.Redirect(w, r, back(r, t.ID, site), http.StatusSeeOther)
	}
}

// siteConnect connects a tenant to the site from the site page.
func (h *Handler) siteConnect(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PostFormValue("tenant"))
	req := tenant.PlacementRequest{Site: s.ID, ClusterExtID: strings.TrimSpace(r.PostFormValue("cluster_ext_id")), StorageContainerExtID: strings.TrimSpace(r.PostFormValue("storage_container_ext_id"))}
	if err := h.svc.AddSite(r.Context(), id, req); err != nil {
		h.renderSite(w, r, statusOf(err), s, err.Error())
		return
	}
	h.log.Info("tenant site added", "via", "ui", "tenant", id, "site", s.ID)
	http.Redirect(w, r, "/setup/sites/"+url.PathEscape(s.ID), http.StatusSeeOther)
}

// targetOption is one menu entry of the targets fragment.
type targetOption struct {
	tenant.Target
	Selected bool
}

type targetsData struct {
	ClusterField, ContainerField string
	Clusters, Containers         []targetOption
	Failed                       bool
}

// targets is the fragment with the cluster and container menus of a
// site, for the connect forms (form=connect) and the new-tenant form
// (form=create, one pair per site). cluster= and container= preselect.
func (h *Handler) targets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	site := q.Get("site")
	if _, ok := h.siteOf(site); !ok {
		http.Error(w, "unknown site", http.StatusNotFound)
		return
	}
	d := targetsData{ClusterField: "cluster_ext_id", ContainerField: "storage_container_ext_id"}
	if q.Get("form") == "create" {
		d.ClusterField, d.ContainerField = "cluster_"+site, "container_"+site
	}
	ts, err := h.svc.Targets(r.Context(), site)
	if err != nil && !errors.Is(err, tenant.ErrUnknownSite) {
		h.log.Warn("targets unreadable", "site", site, "err", err.Error())
		d.Failed = true
	}
	d.Clusters = options(ts.Clusters, q.Get("cluster"))
	d.Containers = options(ts.Containers, q.Get("container"))
	h.fragment(w, r, "targets", "targets", d)
}

// options marks the wanted entry selected, or the site's default.
func options(ts []tenant.Target, want string) []targetOption {
	found := false
	for _, t := range ts {
		found = found || (want != "" && t.ExtID == want)
	}
	out := make([]targetOption, 0, len(ts))
	for _, t := range ts {
		out = append(out, targetOption{Target: t, Selected: (found && t.ExtID == want) || (!found && t.SiteDefault)})
	}
	return out
}
