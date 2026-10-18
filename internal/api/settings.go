package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	KeycloakURL string     // backchannel URL for the Keycloak and OIDC checks
	Restart     func()     // asks serve to restart; nil when not available
	Busy        func() int // tenant operations in progress; apply is refused while any runs
	// Mode is "setup" when the process serves the admin listener only
	// (design §4.7) and "running" otherwise.
	Mode string
	// Bootstrapper creates the realm, the service account and the SAML
	// trust from one-time credentials (design §4.4, §4.5).
	Bootstrapper *settings.Bootstrapper

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
	return settings.Defaults(), "none", nil
}

// BusyError says how many tenant operations stop an apply.
type BusyError struct{ N int }

func (e *BusyError) Error() string {
	return fmt.Sprintf("%d tenant operation(s) running: retry when they finish", e.N)
}

// Apply triggers the restart once settings are stored and no tenant
// operation is running: the restart closes the listeners first, so an
// operation in flight would leave the service dark until it ends.
func (o *SettingsOptions) Apply(ctx context.Context) error {
	if o.Service == nil || !o.Service.Stored(ctx) {
		return settings.ErrNotStored
	}
	cur, err := o.Service.Get(ctx)
	if err != nil {
		return err
	}
	if err := cur.Validate(); err != nil {
		return err
	}
	if o.Restart == nil {
		return errRestartUnavailable
	}
	if o.Busy != nil {
		if n := o.Busy(); n > 0 {
			return &BusyError{N: n}
		}
	}
	o.restarting.Store(true)
	o.Restart()
	return nil
}

// Restarting reports whether a restart was requested from this process.
func (o *SettingsOptions) Restarting() bool { return o.restarting.Load() }

var errRestartUnavailable = errors.New("restart is not available in this process")

// Check runs the named check with the candidate document. The stored
// Prism Central key is used only against the stored host: a check that
// names another host must bring its own key, or the stored
// service-account key would be sent wherever the caller points.
func (o *SettingsOptions) Check(ctx context.Context, target string, cur, s settings.Settings, keyGiven bool) (settings.CheckResult, bool) {
	switch target {
	case "prism_central":
		if !keyGiven && !sameHost(cur.PrismCentral.URL, s.PrismCentral.URL) {
			return settings.CheckResult{Message: "the API key must be given when the Prism Central URL changes: the stored key is only sent to the stored host"}, true
		}
		return o.Checker.PrismCentral(ctx, s, string(s.PrismCentral.APIKey)), true
	case "keycloak":
		return o.Checker.Keycloak(ctx, o.KeycloakURL, s, string(s.Keycloak.AdminClientSecret)), true
	case "oidc":
		return o.Checker.OIDC(ctx, o.KeycloakURL, s), true
	}
	return settings.CheckResult{}, false
}

// sameHost reports whether two URLs name the same host and port.
func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Host != "" && strings.EqualFold(ua.Host, ub.Host)
}

// settingsJSON is the wire shape of the document. Secrets are accepted
// on input and never written on output; the *_set booleans say whether
// one is held.
type settingsJSON struct {
	Stored       bool     `json:"stored"`
	Source       string   `json:"source"`
	Mode         string   `json:"mode,omitempty"`
	Complete     bool     `json:"complete"`
	Missing      []string `json:"missing"`
	PrismHost    string   `json:"prism_host"`
	IDHost       string   `json:"id_host"`
	PrismCentral struct {
		URL         string `json:"url"`
		TLSVerify   *bool  `json:"tls_verify"` // absent keeps the current value
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

func toSettingsJSON(s settings.Settings, source, mode string) settingsJSON {
	var j settingsJSON
	j.Stored, j.Source, j.Mode = source == "store", source, mode
	j.Missing = s.Missing()
	if j.Missing == nil {
		j.Missing = []string{}
	}
	j.Complete = len(j.Missing) == 0
	j.PrismHost, j.IDHost = s.PrismHost, s.IDHost
	tlsVerify := s.PrismCentral.TLSVerify
	j.PrismCentral.URL, j.PrismCentral.TLSVerify, j.PrismCentral.CAPEM = s.PrismCentral.URL, &tlsVerify, s.PrismCentral.CAPEM
	j.PrismCentral.APIKeySet, j.PrismCentral.SAMLIdPName = s.PrismCentral.APIKey != "", s.PrismCentral.SAMLIdPName
	j.Keycloak.Realm, j.Keycloak.AdminClientID, j.Keycloak.AdminClientSecretSet = s.Keycloak.Realm, s.Keycloak.AdminClientID, s.Keycloak.AdminClientSecret != ""
	j.OIDC.Issuer, j.OIDC.ClientID, j.OIDC.ClientSecretSet = s.OIDC.Issuer, s.OIDC.ClientID, s.OIDC.ClientSecret != ""
	j.Tenants.ProjectPrefix, j.Tenants.GroupPrefix, j.Tenants.Role = s.Tenants.ProjectPrefix, s.Tenants.GroupPrefix, s.Tenants.Role
	j.Tenants.ClusterExtID, j.Tenants.StorageContainerExtID = s.Tenants.ClusterExtID, s.Tenants.StorageContainerExtID
	j.Tenants.OperationTimeout = s.Tenants.OperationTimeout.String()
	return j
}

// fromSettingsJSON builds the document from the body on top of base: an
// empty secret and an absent tls_verify keep base's value (the stored
// document, or the file's when nothing is stored yet), so a body
// without secrets never erases them and a file-based installation is
// adopted by its first PUT.
func fromSettingsJSON(j settingsJSON, base settings.Settings) (settings.Settings, error) {
	var s settings.Settings
	s.PrismHost, s.IDHost = j.PrismHost, j.IDHost
	s.PrismCentral.URL, s.PrismCentral.TLSVerify, s.PrismCentral.CAPEM = j.PrismCentral.URL, base.PrismCentral.TLSVerify, j.PrismCentral.CAPEM
	if j.PrismCentral.TLSVerify != nil {
		s.PrismCentral.TLSVerify = *j.PrismCentral.TLSVerify
	}
	s.PrismCentral.APIKey, s.PrismCentral.SAMLIdPName = settings.Secret(j.PrismCentral.APIKey), j.PrismCentral.SAMLIdPName
	s.Keycloak.Realm, s.Keycloak.AdminClientID, s.Keycloak.AdminClientSecret = j.Keycloak.Realm, j.Keycloak.AdminClientID, settings.Secret(j.Keycloak.AdminClientSecret)
	s.OIDC.Issuer, s.OIDC.ClientID, s.OIDC.ClientSecret = j.OIDC.Issuer, j.OIDC.ClientID, settings.Secret(j.OIDC.ClientSecret)
	if s.PrismCentral.APIKey == "" {
		s.PrismCentral.APIKey = base.PrismCentral.APIKey
	}
	if s.Keycloak.AdminClientSecret == "" {
		s.Keycloak.AdminClientSecret = base.Keycloak.AdminClientSecret
	}
	if s.OIDC.ClientSecret == "" {
		s.OIDC.ClientSecret = base.OIDC.ClientSecret
	}
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

// fromBody is the document as the body gave it, without any base.
func fromBody(j settingsJSON) settings.Settings {
	s, _ := fromSettingsJSON(j, settings.Settings{})
	return s
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
	writeJSON(w, http.StatusOK, toSettingsJSON(s, source, h.so.Mode))
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
	base, _, err := h.so.Current(r.Context())
	if err != nil {
		h.log.Error("settings unreadable", "err", err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s, err := fromSettingsJSON(in, base)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.so.Service.Update(r.Context(), s); err != nil {
		// A wrong document is the caller's (400); anything else (the
		// store, the master key) is the appliance's (500).
		var ve *settings.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.log.Error("settings not stored", "err", err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.log.Info("settings saved", "via", "api", "secrets_given", secretsIn(fromBody(in)))
	cur, source, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toSettingsJSON(cur, source, h.so.Mode))
}

// checkJSON is the check request: the target plus candidate settings.
type checkJSON struct {
	Target string `json:"target"`
	settingsJSON
}

// candidate overlays the target section of the request on the current
// document: an empty field means the current value, so a body with the
// target alone checks what is stored. in comes from fromSettingsJSON
// with cur as base, so its secrets and TLS flag already fall back.
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
			out.PrismCentral.URL, out.PrismCentral.CAPEM = in.PrismCentral.URL, in.PrismCentral.CAPEM
		}
		out.PrismCentral.TLSVerify = in.PrismCentral.TLSVerify
		pick(&out.PrismCentral.SAMLIdPName, in.PrismCentral.SAMLIdPName)
		out.PrismCentral.APIKey = in.PrismCentral.APIKey
	case "keycloak":
		pick(&out.Keycloak.Realm, in.Keycloak.Realm)
		pick(&out.Keycloak.AdminClientID, in.Keycloak.AdminClientID)
		out.Keycloak.AdminClientSecret = in.Keycloak.AdminClientSecret
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
	cur, _, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cand, err := fromSettingsJSON(in.settingsJSON, cur)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, ok := h.so.Check(r.Context(), in.Target, cur, candidate(cur, in.Target, cand), in.PrismCentral.APIKey != "")
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
	var busy *BusyError
	var inc *settings.IncompleteError
	switch err := h.so.Apply(r.Context()); {
	case errors.Is(err, settings.ErrNotStored):
		writeError(w, http.StatusConflict, "no settings are stored: save them first")
	case errors.As(err, &inc):
		writeError(w, http.StatusConflict, err.Error())
	case errors.As(err, &busy):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusNotImplemented, err.Error())
	default:
		h.log.Info("settings apply: restart requested", "via", "api")
		writeJSON(w, http.StatusAccepted, map[string]bool{"restarting": true})
	}
}

// bootstrapKeycloakJSON is the Keycloak bootstrap request body.
type bootstrapKeycloakJSON struct {
	AdminUsername string `json:"admin_username"`
	AdminPassword string `json:"admin_password"`
	Realm         string `json:"realm"`
	DisplayName   string `json:"display_name"`
}

// bootstrapAnswer writes a bootstrap outcome: 409 for a precondition,
// 500 for an internal error, otherwise 200 with the result; its "stored"
// field says whether the bootstrapper saved what it set.
func (h *handler) bootstrapAnswer(w http.ResponseWriter, r *http.Request, res any, err error) {
	var se *settings.StateError
	switch {
	case errors.As(err, &se):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		h.log.Error("bootstrap failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *handler) bootstrapKeycloak(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in bootstrapKeycloakJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	cur, _, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, _, err := h.so.Bootstrapper.Keycloak(r.Context(), h.so.KeycloakURL, cur, settings.KeycloakInput{AdminUsername: in.AdminUsername, AdminPassword: in.AdminPassword, Realm: in.Realm, DisplayName: in.DisplayName})
	if err == nil {
		h.log.Info("bootstrap keycloak", "realm", in.Realm, "user", in.AdminUsername, "ok", res.OK, "created", res.Created)
	}
	h.bootstrapAnswer(w, r, res, err)
}

// bootstrapPrismJSON is the Prism Central bootstrap request body.
type bootstrapPrismJSON struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	URL            string `json:"url"`
	TLSVerify      *bool  `json:"tls_verify"`
	CAPEM          string `json:"ca_pem"`
	ServiceAccount string `json:"service_account"`
}

func (h *handler) bootstrapPrismCentral(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in bootstrapPrismJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	cur, _, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pin := settings.PrismInput{Username: in.Username, Password: in.Password, URL: in.URL, TLSVerify: true, CAPEM: in.CAPEM, ServiceAccount: in.ServiceAccount}
	if in.TLSVerify != nil {
		pin.TLSVerify = *in.TLSVerify
	}
	res, _, err := h.so.Bootstrapper.PrismCentral(r.Context(), cur, pin)
	if err == nil {
		attrs := []any{"url", in.URL, "user", in.Username, "service_account", res.ServiceAccount, "ok", res.OK, "created", res.Created}
		if res.OK {
			attrs = append(attrs, "key_expires", res.KeyExpires)
		}
		h.log.Info("bootstrap prism central", attrs...)
	}
	h.bootstrapAnswer(w, r, res, err)
}

// bootstrapSAMLJSON is the SAML trust bootstrap request body.
type bootstrapSAMLJSON struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *handler) bootstrapSAML(w http.ResponseWriter, r *http.Request) {
	if h.settingsUnavailable(w) {
		return
	}
	var in bootstrapSAMLJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	cur, _, err := h.so.Current(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, _, err := h.so.Bootstrapper.SAML(r.Context(), h.so.KeycloakURL, cur, settings.SAMLInput{Name: in.Name, Username: in.Username, Password: in.Password})
	if err == nil {
		h.log.Info("bootstrap saml", "name", res.Name, "ok", res.OK, "used_credentials", res.UsedCredentials, "created", res.Created)
	}
	h.bootstrapAnswer(w, r, res, err)
}
