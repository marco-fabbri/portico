package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/settings"
)

// setupData is what the setup page renders: the document with its
// secrets blanked, where it comes from, and which secrets are held.
type setupData struct {
	S                               settings.Settings
	Source                          string
	Stored                          bool
	AdminSecretSet, ClientSecretSet bool
	OperationTimeout                string
	// Sites are the document's sites in order, keys blanked, with the
	// tenants placed on each; Incomplete names the ones not served yet.
	Sites      []siteRow
	Incomplete []string
	Mode       string
	Missing    []string
	Complete   bool
	// OOB marks the inputs rendered by a bootstrap fragment, which
	// HTMX swaps into the cards by id.
	OOB bool
	// Form holds the non-secret bootstrap fields as typed, so a failed
	// attempt does not make the administrator retype them; passwords
	// are never echoed. Empty on the page itself.
	Form map[string]string
	// Done says, per card, whether its required fields and secrets are
	// all there: the card states it and folds its bootstrap form away.
	Done map[string]bool
}

func (h *Handler) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /setup", h.setupPage)
	mux.HandleFunc("POST /setup/save", h.setupSave)
	mux.HandleFunc("POST /setup/check", h.setupCheck)
	mux.HandleFunc("POST /setup/apply", h.setupApply)
	mux.HandleFunc("POST /setup/bootstrap/{target}", h.setupBootstrap)
	mux.HandleFunc("GET /setup/ready", h.setupReady)
	h.siteRoutes(mux)
}

// current returns the document the process would run from, or an error
// page when the stored one cannot be opened.
func (h *Handler) current(w http.ResponseWriter, r *http.Request) (settings.Settings, string, bool) {
	if h.setup == nil || h.setup.Service == nil {
		h.render(w, r, http.StatusServiceUnavailable, "notfound", "Setup", nil, "setup is not available in this process")
		return settings.Settings{}, "", false
	}
	s, source, err := h.setup.Current(r.Context())
	if err != nil {
		h.log.Error("settings unreadable", "err", err.Error())
		h.render(w, r, http.StatusInternalServerError, "notfound", "Setup", nil, err.Error())
		return settings.Settings{}, "", false
	}
	return s, source, true
}

func toSetupData(s settings.Settings, source, mode string) setupData {
	d := setupData{Source: source, Stored: source == "store", Mode: mode, Missing: s.Missing(),
		AdminSecretSet: s.Keycloak.AdminClientSecret != "", ClientSecretSet: s.OIDC.ClientSecret != ""}
	d.Complete = len(d.Missing) == 0
	d.Incomplete = s.IncompleteSites()
	sitesDone := len(s.Sites) > 0
	for _, site := range s.OrderedSites() {
		row := toSiteRow(site, nil)
		sitesDone = sitesDone && row.Done
		d.Sites = append(d.Sites, row)
	}
	d.Done = map[string]bool{
		"hosts":    s.PortalHost != "" && s.IDHost != "",
		"sites":    sitesDone,
		"keycloak": s.Keycloak.Realm != "" && s.Keycloak.AdminClientID != "" && d.AdminSecretSet && s.OIDC.Issuer != "" && s.OIDC.ClientID != "" && d.ClientSecretSet,
		"tenants":  s.Tenants.ProjectPrefix != "" && s.Tenants.GroupPrefix != "" && s.Tenants.Role != "",
	}
	s.Keycloak.AdminClientSecret, s.OIDC.ClientSecret = "", ""
	s.Sites = nil
	d.S = s
	if s.Tenants.OperationTimeout > 0 {
		d.OperationTimeout = s.Tenants.OperationTimeout.String()
	}
	return d
}

func (h *Handler) renderSetup(w http.ResponseWriter, r *http.Request, status int, s settings.Settings, source, errMsg string) {
	d := toSetupData(s, source, h.setup.Mode)
	for i := range d.Sites {
		d.Sites[i].Tenants = h.siteTenants(r, d.Sites[i].S.ID)
		if h.setup.Runtime != nil {
			d.Sites[i].Status = string(h.setup.Runtime.Status(d.Sites[i].S.ID))
		}
	}
	h.render(w, r, status, "setup", "Setup", d, errMsg)
}

func (h *Handler) setupPage(w http.ResponseWriter, r *http.Request) {
	s, source, ok := h.current(w, r)
	if !ok {
		return
	}
	h.renderSetup(w, r, http.StatusOK, s, source, "")
}

// formSettings overlays one card's fields on cur. An empty secret field
// keeps the current secret; the checkbox tls_verify is "on" or absent.
func formSettings(r *http.Request, section string, cur settings.Settings) (settings.Settings, error) {
	v := func(name string) string { return strings.TrimSpace(r.FormValue(name)) }
	out := cur
	switch section {
	case "hosts":
		out.PortalHost, out.IDHost = v("portal_host"), v("id_host")
	case "keycloak":
		out.Keycloak.Realm, out.Keycloak.AdminClientID = v("realm"), v("admin_client_id")
		if k := r.FormValue("admin_client_secret"); k != "" {
			out.Keycloak.AdminClientSecret = settings.Secret(strings.TrimSpace(k))
		}
		out.OIDC.Issuer, out.OIDC.ClientID = v("issuer"), v("client_id")
		if k := r.FormValue("client_secret"); k != "" {
			out.OIDC.ClientSecret = settings.Secret(strings.TrimSpace(k))
		}
	case "tenants":
		out.Tenants.ProjectPrefix, out.Tenants.GroupPrefix, out.Tenants.Role = v("project_prefix"), v("group_prefix"), v("role")
		if t := v("operation_timeout"); t != "" {
			d, err := time.ParseDuration(t)
			if err != nil {
				return out, errors.New("operation timeout must be a duration such as 10m")
			}
			out.Tenants.OperationTimeout = d
		}
	default:
		return out, errors.New("unknown section")
	}
	return out, nil
}

func (h *Handler) setupSave(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	cur, source, ok := h.current(w, r)
	if !ok {
		return
	}
	section := r.FormValue("section")
	s, err := formSettings(r, section, cur)
	if err != nil {
		h.renderSetup(w, r, http.StatusBadRequest, s, source, err.Error())
		return
	}
	if err := h.setup.Service.Update(r.Context(), s); err != nil {
		status := http.StatusBadRequest
		var ve *settings.ValidationError
		if !errors.As(err, &ve) {
			h.log.Error("settings not stored", "via", "ui", "err", err.Error())
			status = http.StatusInternalServerError
		}
		h.renderSetup(w, r, status, s, source, err.Error())
		return
	}
	h.log.Info("settings saved", "via", "ui", "section", section)
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

// pickersView feeds the "pickers" template: the choices and the current
// selection of the two tenants-card selects.
type pickersView struct {
	Clusters, Containers []settings.Choice
	Cluster, Container   string
}

func pickersData(clusters, containers []settings.Choice, cluster, container string) pickersView {
	return pickersView{Clusters: clusters, Containers: containers, Cluster: cluster, Container: container}
}

// checkData is the check fragment: the result plus the current pickers'
// values so a refilled picker keeps the selection.
type checkData struct {
	Target                         string
	Res                            settings.CheckResult
	ClusterExtID, StorageContainer string
}

// sectionOf maps a check target to the card whose fields it reads.
func sectionOf(target string) string {
	if target == "oidc" {
		return "keycloak"
	}
	return target
}

func (h *Handler) setupCheck(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	cur, _, ok := h.current(w, r)
	if !ok {
		return
	}
	target := r.FormValue("target")
	cand, err := formSettings(r, sectionOf(target), cur)
	if err != nil {
		http.Error(w, "unknown target", http.StatusBadRequest)
		return
	}
	res, ok := h.setup.Check(r.Context(), target, cur, cand, false)
	if !ok {
		http.Error(w, "unknown target", http.StatusBadRequest)
		return
	}
	h.log.Info("settings check", "via", "ui", "target", target, "ok", res.OK)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d := checkData{Target: target, Res: res}
	if err := h.pages["setup"].ExecuteTemplate(w, "check", d); err != nil {
		h.log.Error("template", "page", "setup/check", "err", err.Error())
	}
}

func (h *Handler) setupApply(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	cur, source, ok := h.current(w, r)
	if !ok {
		return
	}
	var busy *api.BusyError
	var inc *settings.IncompleteError
	switch err := h.setup.Apply(r.Context()); {
	case errors.Is(err, settings.ErrNotStored):
		h.renderSetup(w, r, http.StatusConflict, cur, source, "no settings are stored yet: save them first")
	case errors.As(err, &inc):
		h.renderSetup(w, r, http.StatusConflict, cur, source, err.Error())
	case errors.As(err, &busy):
		h.renderSetup(w, r, http.StatusConflict, cur, source, "Apply refused: "+err.Error())
	case err != nil:
		h.renderSetup(w, r, http.StatusNotImplemented, cur, source, err.Error())
	default:
		h.log.Info("settings apply: restart requested", "via", "ui")
		h.render(w, r, http.StatusOK, "restarting", "Restarting", nil, "")
	}
}

// setupReady is the fragment the restart page polls. It needs no
// session: the new process has none yet.
func (h *Handler) setupReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if h.setup != nil && h.setup.Restarting() {
		w.Write([]byte(`<p class="muted">Portico is restarting with the new settings…</p>`))
		return
	}
	w.Write([]byte(`<p class="banner notice">Portico is back. <a href="/login">Log in again</a> to continue.</p>`))
}

// bootData is what a bootstrap fragment renders: the outcome banner, a
// fresh copy of the bootstrap form (so the password fields come back
// empty) and, on success, the inputs the bootstrap filled, swapped
// out-of-band into their cards.
type bootData struct {
	Target, Status, Message string
	OK                      bool
	CSRF                    string
	Data                    setupData
	Clusters, Containers    []settings.Choice
}

// setupBootstrap runs one bootstrap from the card's form: the one-time
// credentials travel in this request only (design §4.5) and the result
// is stored through the settings service when the step succeeded.
func (h *Handler) setupBootstrap(w http.ResponseWriter, r *http.Request) {
	if !h.requireCSRF(w, r) {
		return
	}
	target := r.PathValue("target")
	if target != "keycloak" {
		http.NotFound(w, r)
		return
	}
	cur, source, ok := h.current(w, r)
	if !ok {
		return
	}
	v := func(name string) string { return strings.TrimSpace(r.FormValue(name)) }
	b := h.setup.Bootstrapper
	var (
		okRes   bool
		message string
		out     settings.Settings
		err     error
		d       = bootData{Target: target}
		form    = map[string]string{}
	)
	for _, name := range []string{"username", "service_account", "admin_username", "display_name"} {
		form[name] = v(name)
	}
	switch target {
	case "keycloak":
		var res settings.KeycloakResult
		res, out, err = b.Keycloak(r.Context(), h.setup.KeycloakURL, cur, settings.KeycloakInput{AdminUsername: v("admin_username"), AdminPassword: r.FormValue("admin_password"), Realm: v("realm"), DisplayName: v("display_name")})
		okRes, message = res.OK, res.Message
	}
	status := http.StatusOK
	var se *settings.StateError
	switch {
	case errors.As(err, &se):
		status, d.Status, d.Message = http.StatusConflict, "error", err.Error()
	case err != nil:
		h.log.Error("settings bootstrap failed", "via", "ui", "target", target, "err", err.Error())
		status, d.Status, d.Message = http.StatusInternalServerError, "error", err.Error()
	case !okRes:
		d.Status, d.Message = "error", message
	default:
		// The bootstrapper stored what it set; the page shows the
		// document as it is now, with any save made meanwhile.
		cur, source = out, "store"
		if now, err := h.setup.Service.Get(r.Context()); err == nil {
			cur = now
		}
		d.OK, d.Status, d.Message = true, "notice", message
	}
	h.log.Info("settings bootstrap", "via", "ui", "target", target, "ok", d.OK, "status", status)
	d.Data = toSetupData(cur, source, h.setup.Mode)
	d.Data.OOB, d.Data.Form = true, form
	if sess, ok := h.session(r); ok {
		d.CSRF = sess.CSRF
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.pages["setup"].ExecuteTemplate(w, "bootstrap_"+strings.ReplaceAll(target, "-", "_"), d); err != nil {
		h.log.Error("template", "page", "setup/bootstrap", "err", err.Error())
	}
}
