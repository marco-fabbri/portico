// Package settings is the runtime settings document Portico keeps in its
// store (design §4.7): public names, Prism Central connection and
// service-account key, Keycloak realm and admin client, OIDC client,
// tenant naming and placement. Secrets are sealed with the master key
// (design §4.6). The document overlays a loaded config.Config, which
// keeps the process settings (listeners, TLS, paths, log).
package settings

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/config"
)

// SettingKey is the store key of the JSON document.
const SettingKey = "settings"

// Secret is a value sealed in the store and never returned by the API.
type Secret string

// Settings is the runtime settings document.
type Settings struct {
	PrismHost string `json:"prism_host"`
	IDHost    string `json:"id_host"`

	PrismCentral struct {
		URL         string `json:"url"`
		TLSVerify   bool   `json:"tls_verify"`
		CAPEM       string `json:"ca_pem"`
		APIKey      Secret `json:"api_key"`
		SAMLIdPName string `json:"saml_idp_name"`
	} `json:"prism_central"`

	Keycloak struct {
		Realm             string `json:"realm"`
		AdminClientID     string `json:"admin_client_id"`
		AdminClientSecret Secret `json:"admin_client_secret"`
	} `json:"keycloak"`

	OIDC struct {
		Issuer       string `json:"issuer"`
		ClientID     string `json:"client_id"`
		ClientSecret Secret `json:"client_secret"`
	} `json:"oidc"`

	Tenants struct {
		ProjectPrefix         string        `json:"project_prefix"`
		GroupPrefix           string        `json:"group_prefix"`
		Role                  string        `json:"role"`
		ClusterExtID          string        `json:"cluster_ext_id"`
		StorageContainerExtID string        `json:"storage_container_ext_id"`
		OperationTimeout      time.Duration `json:"operation_timeout"`
	} `json:"tenants"`
}

// Runtime carries what the overlay cannot put in the config: the secret
// values and the CA certificate.
type Runtime struct {
	APIKey, AdminSecret, ClientSecret, CAPEM string
}

// FromStore is the sentinel the overlay writes in the config's secret
// file fields, so the runtime validation knows they are provided.
const FromStore = "<store>"

// FromConfig builds the document from a loaded YAML config and the
// contents of its secret files.
func FromConfig(cfg *config.Config, apiKey, adminSecret, clientSecret, caPEM string) Settings {
	var s Settings
	s.PrismHost, s.IDHost = cfg.Public.PrismHost, cfg.Public.IDHost
	s.PrismCentral.URL = cfg.PrismCentral.URL
	s.PrismCentral.TLSVerify = cfg.PrismCentral.TLSVerify
	s.PrismCentral.CAPEM = caPEM
	s.PrismCentral.APIKey = Secret(apiKey)
	s.PrismCentral.SAMLIdPName = cfg.PrismCentral.SAMLIdPName
	s.Keycloak.Realm = cfg.Keycloak.Realm
	s.Keycloak.AdminClientID = cfg.Keycloak.AdminClientID
	s.Keycloak.AdminClientSecret = Secret(adminSecret)
	s.OIDC.Issuer, s.OIDC.ClientID = cfg.OIDC.Issuer, cfg.OIDC.ClientID
	s.OIDC.ClientSecret = Secret(clientSecret)
	s.Tenants.ProjectPrefix = cfg.Tenants.ProjectPrefix
	s.Tenants.GroupPrefix = cfg.Tenants.GroupPrefix
	s.Tenants.Role = cfg.Tenants.Role
	s.Tenants.ClusterExtID = cfg.Tenants.ClusterExtID
	s.Tenants.StorageContainerExtID = cfg.Tenants.StorageContainerExtID
	s.Tenants.OperationTimeout = cfg.Tenants.OperationTimeout
	return s
}

// Overlay writes the document onto cfg's runtime sections and returns
// the secrets. The secret file fields are set to FromStore and the CA
// file is cleared: the caller uses Runtime instead of files.
func (s Settings) Overlay(cfg *config.Config) (Runtime, error) {
	cfg.Public.PrismHost, cfg.Public.IDHost = s.PrismHost, s.IDHost
	cfg.PrismCentral.URL = s.PrismCentral.URL
	cfg.PrismCentral.TLSVerify = s.PrismCentral.TLSVerify
	cfg.PrismCentral.CAFile = ""
	cfg.PrismCentral.APIKeyFile = FromStore
	cfg.PrismCentral.SAMLIdPName = s.PrismCentral.SAMLIdPName
	cfg.Keycloak.Realm = s.Keycloak.Realm
	cfg.Keycloak.AdminClientID = s.Keycloak.AdminClientID
	cfg.Keycloak.AdminClientSecretFile = FromStore
	cfg.OIDC.Issuer, cfg.OIDC.ClientID = s.OIDC.Issuer, s.OIDC.ClientID
	cfg.OIDC.ClientSecretFile = FromStore
	cfg.Tenants.ProjectPrefix = s.Tenants.ProjectPrefix
	cfg.Tenants.GroupPrefix = s.Tenants.GroupPrefix
	cfg.Tenants.Role = s.Tenants.Role
	cfg.Tenants.ClusterExtID = s.Tenants.ClusterExtID
	cfg.Tenants.StorageContainerExtID = s.Tenants.StorageContainerExtID
	cfg.Tenants.OperationTimeout = s.Tenants.OperationTimeout
	return Runtime{APIKey: string(s.PrismCentral.APIKey), AdminSecret: string(s.Keycloak.AdminClientSecret), ClientSecret: string(s.OIDC.ClientSecret), CAPEM: s.PrismCentral.CAPEM}, nil
}

// Defaults is the document a fresh appliance starts from: the naming
// and timing conventions filled in, everything that names the MSP's
// systems left empty (design §4.7, setup mode).
func Defaults() Settings {
	var s Settings
	s.PrismCentral.TLSVerify = true
	s.Keycloak.AdminClientID = "portico-admin"
	s.OIDC.ClientID = "portico"
	s.Tenants.ProjectPrefix, s.Tenants.GroupPrefix, s.Tenants.Role = "portico", "tenant-", "Project Admin"
	s.Tenants.OperationTimeout = 10 * time.Minute
	return s
}

// Missing lists the required fields that are empty, in document order.
// A document with nothing missing is complete and can be applied.
func (s Settings) Missing() []string {
	var out []string
	for _, r := range []struct{ key, value string }{
		{"prism_host", s.PrismHost},
		{"id_host", s.IDHost},
		{"prism_central.url", s.PrismCentral.URL},
		{"prism_central.api_key", string(s.PrismCentral.APIKey)},
		{"prism_central.saml_idp_name", s.PrismCentral.SAMLIdPName},
		{"keycloak.realm", s.Keycloak.Realm},
		{"keycloak.admin_client_id", s.Keycloak.AdminClientID},
		{"keycloak.admin_client_secret", string(s.Keycloak.AdminClientSecret)},
		{"oidc.issuer", s.OIDC.Issuer},
		{"oidc.client_id", s.OIDC.ClientID},
		{"oidc.client_secret", string(s.OIDC.ClientSecret)},
		{"tenants.project_prefix", s.Tenants.ProjectPrefix},
		{"tenants.group_prefix", s.Tenants.GroupPrefix},
		{"tenants.role", s.Tenants.Role},
		{"tenants.cluster_ext_id", s.Tenants.ClusterExtID},
		{"tenants.storage_container_ext_id", s.Tenants.StorageContainerExtID},
	} {
		if strings.TrimSpace(r.value) == "" {
			out = append(out, r.key)
		}
	}
	if s.Tenants.OperationTimeout == 0 {
		out = append(out, "tenants.operation_timeout")
	}
	return out
}

var rePrefix = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateFormat checks the shape of the values that are set and says
// nothing about the empty ones, so a draft can be stored card by card.
func (s Settings) ValidateFormat() error {
	var errs []error
	for _, r := range []struct{ key, value string }{{"prism_central.url", s.PrismCentral.URL}, {"oidc.issuer", s.OIDC.Issuer}} {
		if r.value == "" {
			continue
		}
		if u, err := url.Parse(r.value); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			errs = append(errs, fmt.Errorf("settings: %s %q is not an http(s) URL", r.key, r.value))
		}
	}
	if p := s.Tenants.ProjectPrefix; p != "" && !rePrefix.MatchString(p) {
		errs = append(errs, fmt.Errorf("settings: tenants.project_prefix %q must be lower-case letters, digits and dashes", p))
	}
	if p := s.Tenants.GroupPrefix; p != "" && !rePrefix.MatchString(strings.TrimSuffix(p, "-")) {
		errs = append(errs, fmt.Errorf("settings: tenants.group_prefix %q must be lower-case letters, digits and dashes", p))
	}
	if s.Tenants.OperationTimeout < 0 {
		errs = append(errs, errors.New("settings: tenants.operation_timeout must be positive"))
	}
	return errors.Join(errs...)
}

// IncompleteError says which required fields a document still lacks.
type IncompleteError struct {
	Missing []string
}

func (e *IncompleteError) Error() string {
	return "settings incomplete: missing " + strings.Join(e.Missing, ", ")
}

// Validate is the check before the document runs Portico: the format
// of every value, then completeness (an *IncompleteError).
func (s Settings) Validate() error {
	if err := s.ValidateFormat(); err != nil {
		return err
	}
	if m := s.Missing(); len(m) > 0 {
		return &IncompleteError{Missing: m}
	}
	return nil
}
