package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/marco-fabbri/portico/internal/settings"
)

// SettingsOptions is what the settings routes and the setup pages need.
// The same struct feeds the API and the UI so both see one document.
type SettingsOptions struct {
	Service *settings.Service
	// File holds the YAML's runtime sections with the secrets read from
	// its files, when the process runs from the file; nil otherwise.
	File        *settings.Settings
	Checker     *settings.Checker
	KeycloakURL string // backchannel URL for the Keycloak and OIDC checks
	Restart     func() // asks serve to restart; nil when not available

	restarting atomic.Bool
}

// Current returns the document the process would run from: the stored
// one when there is one, else the file's. Source is "store", "file" or
// "none".
func (o *SettingsOptions) Current(ctx context.Context) (settings.Settings, string, error) {
	if o.Service != nil && o.Service.Stored(ctx) {
		s, err := o.Service.Get(ctx)
		return s, "store", err
	}
	if o.File != nil {
		return *o.File, "file", nil
	}
	return settings.Settings{}, "none", nil
}

// Apply triggers the restart once settings are stored.
func (o *SettingsOptions) Apply(ctx context.Context) error {
	if o.Service == nil || !o.Service.Stored(ctx) {
		return settings.ErrNotStored
	}
	if o.Restart == nil {
		return errRestartUnavailable
	}
	o.restarting.Store(true)
	o.Restart()
	return nil
}

// Restarting reports whether a restart was requested from this process.
func (o *SettingsOptions) Restarting() bool { return o.restarting.Load() }

var errRestartUnavailable = errors.New("restart is not available in this process")

// Check runs the named check against the candidate document.
func (o *SettingsOptions) Check(ctx context.Context, target string, s settings.Settings) (settings.CheckResult, bool) {
	switch target {
	case "prism_central":
		return o.Checker.PrismCentral(ctx, s, string(s.PrismCentral.APIKey)), true
	case "keycloak":
		return o.Checker.Keycloak(ctx, o.KeycloakURL, s, string(s.Keycloak.AdminClientSecret)), true
	case "oidc":
		return o.Checker.OIDC(ctx, o.KeycloakURL, s), true
	}
	return settings.CheckResult{}, false
}

// settingsJSON is the wire shape of the document. Secrets are accepted
// on input and never written on output; the *_set booleans say whether
// one is held.
type settingsJSON struct {
	Stored       bool   `json:"stored"`
	Source       string `json:"source"`
	PrismHost    string `json:"prism_host"`
	IDHost       string `json:"id_host"`
	PrismCentral struct {
		URL         string `json:"url"`
		TLSVerify   bool   `json:"tls_verify"`
		CAPEM       string `json:"ca_pem"`
		APIKey      string `json:"api_key,omitempty"`
		APIKeySet   bool   `json:"api_key_set"`
		SAMLIdPName string `json:"saml_idp_name"`
	} `json:"prism_central"`
	Keycloak struct {
		Realm                string `json:"realm"`
		AdminClientID        string `json:"admin_client_id"`
		AdminClientSecret    string `json:"admin_client_secret,omitempty"`
		AdminClientSecretSet bool   `json:"admin_client_secret_set"`
	} `json:"keycloak"`
	OIDC struct {
		Issuer          string `json:"issuer"`
		ClientID        string `json:"client_id"`
		ClientSecret    string `json:"client_secret,omitempty"`
		ClientSecretSet bool   `json:"client_secret_set"`
	} `json:"oidc"`
	Tenants struct {
		ProjectPrefix         string `json:"project_prefix"`
		GroupPrefix           string `json:"group_prefix"`
		Role                  string `json:"role"`
		ClusterExtID          string `json:"cluster_ext_id"`
		StorageContainerExtID string `json:"storage_container_ext_id"`
		OperationTimeout      string `json:"operation_timeout"`
	} `json:"tenants"`
}

func toSettingsJSON(s settings.Settings, source string) settingsJSON {
	var j settingsJSON
	j.Stored, j.Source = source == "store", source
	j.PrismHost, j.IDHost = s.PrismHost, s.IDHost
	j.PrismCentral.URL, j.PrismCentral.TLSVerify, j.PrismCentral.CAPEM = s.PrismCentral.URL, s.PrismCentral.TLSVerify, s.PrismCentral.CAPEM
	j.PrismCentral.APIKeySet, j.PrismCentral.SAMLIdPName = s.PrismCentral.APIKey != "", s.PrismCentral.SAMLIdPName
	j.Keycloak.Realm, j.Keycloak.AdminClientID, j.Keycloak.AdminClientSecretSet = s.Keycloak.Realm, s.Keycloak.AdminClientID, s.Keycloak.AdminClientSecret != ""
	j.OIDC.Issuer, j.OIDC.ClientID, j.OIDC.ClientSecretSet = s.OIDC.Issuer, s.OIDC.ClientID, s.OIDC.ClientSecret != ""
	j.Tenants.ProjectPrefix, j.Tenants.GroupPrefix, j.Tenants.Role = s.Tenants.ProjectPrefix, s.Tenants.GroupPrefix, s.Tenants.Role
	j.Tenants.ClusterExtID, j.Tenants.StorageContainerExtID = s.Tenants.ClusterExtID, s.Tenants.StorageContainerExtID
	j.Tenants.OperationTimeout = s.Tenants.OperationTimeout.String()
	return j
}

// fromSettingsJSON builds the document; empty secrets stay empty so the
// service keeps the stored ones.
func fromSettingsJSON(j settingsJSON) (settings.Settings, error) {
	var s settings.Settings
	s.PrismHost, s.IDHost = j.PrismHost, j.IDHost
	s.PrismCentral.URL, s.PrismCentral.TLSVerify, s.PrismCentral.CAPEM = j.PrismCentral.URL, j.PrismCentral.TLSVerify, j.PrismCentral.CAPEM
	s.PrismCentral.APIKey, s.PrismCentral.SAMLIdPName = settings.Secret(j.PrismCentral.APIKey), j.PrismCentral.SAMLIdPName
	s.Keycloak.Realm, s.Keycloak.AdminClientID, s.Keycloak.AdminClientSecret = j.Keycloak.Realm, j.Keycloak.AdminClientID, settings.Secret(j.Keycloak.AdminClientSecret)
	s.OIDC.Issuer, s.OIDC.ClientID, s.OIDC.ClientSecret = j.OIDC.Issuer, j.OIDC.ClientID, settings.Secret(j.OIDC.ClientSecret)
	s.Tenants.ProjectPrefix, s.Tenants.GroupPrefix, s.Tenants.Role = j.Tenants.ProjectPrefix, j.Tenants.GroupPrefix, j.Tenants.Role
	s.Tenants.ClusterExtID, s.Tenants.StorageContainerExtID = j.Tenants.ClusterExtID, j.Tenants.StorageContainerExtID
	if v := strings.TrimSpace(j.Tenants.OperationTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return s, errors.New("tenants.operation_timeout must be a duration such as 10m")
		}
		s.Tenants.OperationTimeout = d
	}
	return s, nil
}

// secretsIn names the secret fields a request carries, for the log.
func secretsIn(s settings.Settings) []string {
	var names []string
	if s.PrismCentral.APIKey != "" {
		names = append(names, "prism_central.api_key")
	}
	if s.Keycloak.AdminClientSecret != "" {
		names = append(names, "keycloak.admin_client_secret")
	}
	if s.OIDC.ClientSecret != "" {
		names = append(names, "oidc.client_secret")
	}
	return names
}

func (h *handler) settingsUnavailable(w http.ResponseWriter) bool {
	if h.so == nil || h.so.Service == nil {
		writeError(w, http.StatusNotImplemented, "settings are not available in this process")
		return true
	}
	return false
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	s, source, err := h.so.Current(r.Context())
	if err != nil {
		h.log.Error("settings unreadable", "err", err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toSettingsJSON(s, source))
}

func (h *handler) putSettings(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in settingsJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	s, err := fromSettingsJSON(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.so.Service.Update(r.Context(), s); err != nil {
		var oe *settings.OpenError
		if errors.As(err, &oe) {
			h.log.Error("settings unreadable", "err", err.Error())
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.log.Info("settings saved", "via", "api", "secrets_updated", secretsIn(s))
	cur, source, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toSettingsJSON(cur, source))
}

// checkJSON is the check request: the target plus candidate settings.
type checkJSON struct {
	Target string `json:"target"`
	settingsJSON
}

// candidate overlays the target section of the request on the current
// document: an empty field (a secret above all) means the current value,
// so a body with the target alone checks what is stored. The TLS flag
// and the CA travel with the URL, since a boolean has no "absent".
func candidate(cur settings.Settings, target string, in settings.Settings) settings.Settings {
	out := cur
	pick := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	switch target {
	case "prism_central":
		if in.PrismCentral.URL != "" {
			out.PrismCentral.URL, out.PrismCentral.TLSVerify, out.PrismCentral.CAPEM = in.PrismCentral.URL, in.PrismCentral.TLSVerify, in.PrismCentral.CAPEM
		}
		pick(&out.PrismCentral.SAMLIdPName, in.PrismCentral.SAMLIdPName)
		if in.PrismCentral.APIKey != "" {
			out.PrismCentral.APIKey = in.PrismCentral.APIKey
		}
	case "keycloak":
		pick(&out.Keycloak.Realm, in.Keycloak.Realm)
		pick(&out.Keycloak.AdminClientID, in.Keycloak.AdminClientID)
		if in.Keycloak.AdminClientSecret != "" {
			out.Keycloak.AdminClientSecret = in.Keycloak.AdminClientSecret
		}
	case "oidc":
		pick(&out.OIDC.Issuer, in.OIDC.Issuer)
		pick(&out.OIDC.ClientID, in.OIDC.ClientID)
	}
	return out
}

func (h *handler) checkSettings(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in checkJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	cand, err := fromSettingsJSON(in.settingsJSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cur, _, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, ok := h.so.Check(r.Context(), in.Target, candidate(cur, in.Target, cand))
	if !ok {
		writeError(w, http.StatusBadRequest, "target must be prism_central, keycloak or oidc")
		return
	}
	h.log.Info("settings check", "target", in.Target, "ok", res.OK)
	writeJSON(w, http.StatusOK, res)
}

func (h *handler) applySettings(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	switch err := h.so.Apply(r.Context()); {
	case errors.Is(err, settings.ErrNotStored):
		writeError(w, http.StatusConflict, "no settings are stored: save them first")
	case err != nil:
		writeError(w, http.StatusNotImplemented, err.Error())
	default:
		h.log.Info("settings apply: restart requested", "via", "api")
		writeJSON(w, http.StatusAccepted, map[string]bool{"restarting": true})
	}
}
