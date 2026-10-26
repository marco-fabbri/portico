package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
)

// siteRow is a site as the setup pages show it: the key blanked, whether
// one is held, whether the site is complete, the tenants placed on it,
// and the monitor's status in running mode.
type siteRow struct {
	S       settings.Site
	KeySet  bool
	Done    bool
	Tenants []string
	Status  string
}

func toSiteRow(s settings.Site, tenants []string) siteRow {
	row := siteRow{KeySet: s.APIKey != "", Done: s.Complete(), Tenants: tenants}
	s.APIKey = ""
	row.S = s
	return row
}

// siteData is what the site page and its fragments render.
type siteData struct {
	siteRow
	Mode string
	// Live is the site's live state; nil in setup mode.
	Live *siteLive
	// Running is set when the tenant service runs: Connections are the
	// tenants on this site, Connectable the active tenants not on it.
	Running     bool
	Connections []connectionRow
	Connectable []store.Tenant
}

// siteFragment is a check or bootstrap answer on the site page.
type siteFragment struct {
	Status, Message      string
	OK                   bool
	CSRF                 string
	Data                 siteData
	Clusters, Containers []settings.Choice
}

func (h *Handler) siteRoutes(mux *http.ServeMux) {
	h.siteSetupRoutes(mux)
	mux.HandleFunc("GET /setup/sites/{id}", h.sitePage)
	mux.HandleFunc("POST /setup/sites/{id}/save", h.siteSave)
	mux.HandleFunc("POST /setup/sites/{id}/check", h.siteCheck)
	mux.HandleFunc("POST /setup/sites/{id}/delete", h.siteDelete)
	mux.HandleFunc("POST /setup/sites/{id}/forget", h.siteForget)
	mux.HandleFunc("POST /setup/sites/{id}/maintenance", h.siteMaintenance)
	mux.HandleFunc("GET /setup/sites/{id}/sessions", h.siteSessions)
	mux.HandleFunc("POST /setup/sites/{id}/disconnect", h.siteDisconnect)
}

// siteLive is the live state of a site on its page (design sites §4.1).
type siteLive struct {
	ID           string
	Status       string
	Maintenance  bool
	Active, Idle int
	Sessions     []sessionRow
}

// sessionRow is a tenant session on the site, never its id.
type sessionRow struct {
	Tenant, Email, Since, LastActivity string
	Active                             bool
}

// live gathers the site's live state, nil without a runtime.
func (h *Handler) live(id string) *siteLive {
	if h.setup == nil || h.setup.Runtime == nil {
		return nil
	}
	rt := h.setup.Runtime
	now := time.Now()
	ss := rt.Sessions(id, now)
	l := &siteLive{ID: id, Status: string(rt.Status(id)), Maintenance: rt.Maintenance(id)}
	l.Active, l.Idle = api.SessionCounts(ss, now)
	for _, s := range ss {
		since := ""
		if !s.SiteSince.IsZero() {
			since = s.SiteSince.Format("2006-01-02 15:04")
		}
		l.Sessions = append(l.Sessions, sessionRow{Tenant: s.Tenant, Email: s.Email, Since: since,
			LastActivity: s.LastSeen.Format("15:04:05"), Active: now.Sub(s.LastSeen) <= auth.ActiveWindow})
	}
	return l
}

// siteTenants lists the tenants placed on a site.
func (h *Handler) siteTenants(r *http.Request, id string) []string {
	ps, err := h.st.ListPlacementsBySite(r.Context(), id)
	if err != nil {
		h.log.Warn("placements unreadable", "site", id, "err", err.Error())
		return nil
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.TenantID)
	}
	return out
}

// siteFromForm reads the fields a posted site form carries: code, name,
// order, the address, the placement defaults and, under "Advanced", the
// CA PEM and the manual path's API key and SAML provider name. An empty
// key keeps the stored one (UpdateSite). The trust is never read from a
// form: a site is verified or pinned (sites usability design 4.3).
func siteFromForm(r *http.Request, s *settings.Site) error {
	v := func(name string) string { return strings.TrimSpace(r.FormValue(name)) }
	if r.Form == nil {
		r.ParseForm()
	}
	if r.Form.Has("code") {
		s.Code, s.Name = v("code"), v("name")
		if o := v("order"); o != "" {
			n, err := strconv.Atoi(o)
			if err != nil {
				return errors.New("order must be a whole number")
			}
			s.Order = n
		}
	}
	if r.Form.Has("url") {
		s.URL = v("url")
	}
	if r.Form.Has("ca_pem") {
		s.CAPEM = strings.TrimSpace(r.FormValue("ca_pem"))
	}
	if r.Form.Has("api_key") {
		s.APIKey = settings.Secret(v("api_key"))
	}
	if r.Form.Has("cluster_ext_id") {
		s.ClusterExtID = v("cluster_ext_id")
	}
	if r.Form.Has("storage_container_ext_id") {
		s.StorageContainerExtID = v("storage_container_ext_id")
	}
	if n := v("saml_idp_name"); n != "" {
		s.SAMLIdPName = n
	}
	return nil
}

// siteStatus answers a site error with the API's status.
func siteStatus(err error) int { return api.SiteStatus(err) }

// loadSite returns the stored site or renders the not-found page.
func (h *Handler) loadSite(w http.ResponseWriter, r *http.Request) (settings.Site, bool) {
	cur, _, ok := h.current(w, r)
	if !ok {
		return settings.Site{}, false
	}
	s, found := cur.Site(r.PathValue("id"))
	if !found {
		h.render(w, r, http.StatusNotFound, "notfound", "Not found", nil, "no such site")
		return settings.Site{}, false
	}
	return s, true
}

func (h *Handler) renderSite(w http.ResponseWriter, r *http.Request, status int, s settings.Site, errMsg string) {
	d := siteData{siteRow: toSiteRow(s, h.siteTenants(r, s.ID)), Mode: h.setup.Mode, Live: h.live(s.ID)}
	h.siteConnections(r, &d)
	h.render(w, r, status, "site", "Site "+s.Code, d, errMsg)
}

// siteConnections fills the tenant rows of the site page and the tenants
// that can still be connected to it.
func (h *Handler) siteConnections(r *http.Request, d *siteData) {
	if h.svc == nil {
		return
	}
	if _, ok := h.siteOf(d.S.ID); !ok {
		// Not served yet (incomplete): the tenant service has no such site.
		return
	}
	d.Running = true
	ps, err := h.st.ListPlacementsBySite(r.Context(), d.S.ID)
	if err != nil {
		h.log.Warn("placements unreadable", "site", d.S.ID, "err", err.Error())
	}
	ts, err := h.st.ListTenants(r.Context())
	if err != nil {
		h.log.Warn("tenants unreadable", "err", err.Error())
	}
	names, on := map[string]string{}, map[string]bool{}
	for _, t := range ts {
		names[t.ID] = t.DisplayNameOr()
	}
	for _, p := range ps {
		on[p.TenantID] = true
	}
	d.Connections = h.connectionRows(r, ps, "site", func(id string) string { return names[id] })
	for _, t := range ts {
		if t.Status == store.StatusActive && !on[t.ID] {
			d.Connectable = append(d.Connectable, t)
		}
	}
}

func (h *Handler) sitePage(w http.ResponseWriter, r *http.Request) {
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	h.renderSite(w, r, http.StatusOK, s, "")
}

func (h *Handler) siteSave(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	var ferr error
	out, err := h.setup.UpdateSite(r.Context(), s.ID, func(d *settings.Site) { ferr = siteFromForm(r, d) })
	if ferr != nil {
		err = ferr
	}
	if err != nil {
		h.renderSite(w, r, siteStatus(err), s, err.Error())
		return
	}
	h.log.Info("site updated", "via", "ui", "id", out.ID, "code", out.Code)
	h.notice(w, "Saved.")
	http.Redirect(w, r, "/setup/sites/"+url.PathEscape(out.ID), http.StatusSeeOther)
}

func (h *Handler) writeSiteFragment(w http.ResponseWriter, r *http.Request, status int, name string, f siteFragment) {
	if sess, ok := h.session(r); ok {
		f.CSRF = sess.CSRF
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.pages["site"].ExecuteTemplate(w, name, f); err != nil {
		h.log.Error("template", "page", "site/"+name, "err", err.Error())
	}
}

func (h *Handler) siteCheck(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	cand := settings.Site{ID: s.ID, TLSVerify: s.TLSVerify, TLSPin: s.TLSPin, CAPEM: s.CAPEM}
	if err := siteFromForm(r, &cand); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := h.setup.CheckSite(r.Context(), s.ID, cand, cand.APIKey != "")
	if err != nil {
		http.Error(w, err.Error(), siteStatus(err))
		return
	}
	h.log.Info("site check", "via", "ui", "id", s.ID, "ok", res.OK, "version", res.Version)
	f := siteFragment{OK: res.OK, Message: res.Message, Status: "error", Clusters: res.Clusters, Containers: res.Containers,
		Data: siteData{siteRow: toSiteRow(s, nil)}}
	if res.OK {
		f.Status = "notice"
	}
	h.writeSiteFragment(w, r, http.StatusOK, "site_check", f)
}

func (h *Handler) siteDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	var admin *settings.SetupInput
	if user, pw := strings.TrimSpace(r.FormValue("username")), r.FormValue("password"); user != "" || pw != "" {
		admin = &settings.SetupInput{Username: user, Password: pw}
	}
	res, err := h.setup.RemoveSite(r.Context(), h.st, s.ID, admin)
	if err != nil {
		h.renderSite(w, r, siteStatus(err), s, err.Error())
		return
	}
	h.log.Warn("site removed", "via", "ui", "id", s.ID, "with_credentials", admin != nil, "deleted", res.Deleted, "left_in_prism_central", res.LeftInPrismCentral)
	h.render(w, r, http.StatusOK, "site_removed", "Site removed", removedData{Code: s.Code, Res: res}, "")
}

// removedData is the page after a site was removed.
type removedData struct {
	Code string
	Res  api.RemoveResult
}

// forgetData is the page after a site was forgotten.
type forgetData struct {
	Res api.ForgetResult
}

func (h *Handler) siteForget(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	res, err := h.setup.ForgetSite(r.Context(), h.st, s.ID, r.FormValue("confirm"))
	if err != nil {
		h.renderSite(w, r, siteStatus(err), s, err.Error())
		return
	}
	h.log.Warn("site forgotten: its Prism Central was not contacted", "via", "ui", "site", res.Forgotten, "left_in_prism_central", res.LeftInPrismCentral)
	h.render(w, r, http.StatusOK, "site_forgotten", "Site forgotten", forgetData{Res: res}, "")
}

// siteMaintenance switches the site's maintenance flag; it takes effect
// at once, without Apply (design sites §4.1).
func (h *Handler) siteMaintenance(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	if h.setup.Runtime == nil {
		h.renderSite(w, r, http.StatusServiceUnavailable, s, api.SetupModeMessage)
		return
	}
	on := r.PostFormValue("maintenance") == "on"
	if err := h.setup.Runtime.SetMaintenance(r.Context(), s.ID, on); err != nil {
		h.renderSite(w, r, http.StatusInternalServerError, s, err.Error())
		return
	}
	h.log.Warn("site maintenance set", "via", "ui", "site", s.ID, "maintenance", on)
	http.Redirect(w, r, "/setup/sites/"+url.PathEscape(s.ID), http.StatusSeeOther)
}

// siteSessions is the connected-users fragment, polled every 10 s.
func (h *Handler) siteSessions(w http.ResponseWriter, r *http.Request) {
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	d := siteData{siteRow: toSiteRow(s, nil), Live: h.live(s.ID)}
	h.fragment(w, r, "site", "site_sessions", d)
}

// siteDisconnect ends the routing of every session on the site: their
// next request lands on the site page or the maintenance page.
func (h *Handler) siteDisconnect(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	s, ok := h.loadSite(w, r)
	if !ok {
		return
	}
	if h.setup.Runtime == nil {
		h.renderSite(w, r, http.StatusServiceUnavailable, s, api.SetupModeMessage)
		return
	}
	n := h.setup.Runtime.Disconnect(s.ID)
	h.log.Warn("site sessions disconnected", "via", "ui", "site", s.ID, "disconnected", n)
	h.notice(w, fmt.Sprintf("%d user(s) disconnected from %s.", n, s.Code))
	http.Redirect(w, r, "/setup/sites/"+url.PathEscape(s.ID), http.StatusSeeOther)
}
