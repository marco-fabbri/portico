package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/settings"
)

// setupData is what the setup page renders: the document with its
// secrets blanked, where it comes from, and which secrets are held.
type setupData struct {
	S                                          settings.Settings
	Source                                     string
	Stored                                     bool
	APIKeySet, AdminSecretSet, ClientSecretSet bool
	OperationTimeout                           string
}

func (h *Handler) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /setup", h.setupPage)
	mux.HandleFunc("POST /setup/save", h.setupSave)
	mux.HandleFunc("POST /setup/check", h.setupCheck)
	mux.HandleFunc("POST /setup/apply", h.setupApply)
	mux.HandleFunc("GET /setup/ready", h.setupReady)
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

func toSetupData(s settings.Settings, source string) setupData {
	d := setupData{Source: source, Stored: source == "store",
		APIKeySet: s.PrismCentral.APIKey != "", AdminSecretSet: s.Keycloak.AdminClientSecret != "", ClientSecretSet: s.OIDC.ClientSecret != ""}
	s.PrismCentral.APIKey, s.Keycloak.AdminClientSecret, s.OIDC.ClientSecret = "", "", ""
	d.S = s
	if s.Tenants.OperationTimeout > 0 {
		d.OperationTimeout = s.Tenants.OperationTimeout.String()
	}
	return d
}

func (h *Handler) renderSetup(w http.ResponseWriter, r *http.Request, status int, s settings.Settings, source, errMsg string) {
	h.render(w, r, status, "setup", "Setup", toSetupData(s, source), errMsg)
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
		out.PrismHost, out.IDHost = v("prism_host"), v("id_host")
	case "prism_central":
		out.PrismCentral.URL, out.PrismCentral.TLSVerify = v("url"), r.FormValue("tls_verify") != ""
		out.PrismCentral.CAPEM, out.PrismCentral.SAMLIdPName = strings.TrimSpace(r.FormValue("ca_pem")), v("saml_idp_name")
		if k := r.FormValue("api_key"); k != "" {
			out.PrismCentral.APIKey = settings.Secret(strings.TrimSpace(k))
		}
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
		out.Tenants.ClusterExtID, out.Tenants.StorageContainerExtID = v("cluster_ext_id"), v("storage_container_ext_id")
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
		h.renderSetup(w, r, http.StatusBadRequest, s, source, err.Error())
		return
	}
	h.log.Info("settings saved", "via", "ui", "section", section)
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
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
	res, ok := h.setup.Check(r.Context(), target, cand)
	if !ok {
		http.Error(w, "unknown target", http.StatusBadRequest)
		return
	}
	h.log.Info("settings check", "via", "ui", "target", target, "ok", res.OK)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d := checkData{Target: target, Res: res, ClusterExtID: cur.Tenants.ClusterExtID, StorageContainer: cur.Tenants.StorageContainerExtID}
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
	switch err := h.setup.Apply(r.Context()); {
	case errors.Is(err, settings.ErrNotStored):
		h.renderSetup(w, r, http.StatusConflict, cur, source, "no settings are stored yet: save them first")
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
