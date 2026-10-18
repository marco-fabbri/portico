// Package settings is the runtime settings document Portico keeps in its
// store (design §4.7): public names, Prism Central connection and
// service-account key, Keycloak realm and admin client, OIDC client,
// tenant naming and placement. Secrets are sealed with the master key
// (design §4.6). The document overlays a loaded config.Config, which
// keeps the process settings (listeners, TLS, paths, log).
package settings

import (
	"errors"
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

// Validate overlays the document on a default config and runs the
// runtime checks; the three secrets must be present.
func (s Settings) Validate() error {
	cfg := config.Default()
	if _, err := s.Overlay(&cfg); err != nil {
		return err
	}
	errs := []error{cfg.ValidateRuntime()}
	for _, r := range []struct {
		key   string
		value Secret
	}{{"prism_central.api_key", s.PrismCentral.APIKey}, {"keycloak.admin_client_secret", s.Keycloak.AdminClientSecret}, {"oidc.client_secret", s.OIDC.ClientSecret}} {
		if r.value == "" {
			errs = append(errs, errors.New("settings: "+r.key+" is required"))
		}
	}
	return errors.Join(errs...)
}
