// Package settings is the runtime settings document Portico keeps in its
// store (design §4.7): public names, the sites (one Prism Central each,
// with its connection and service-account key, design sites §4.1),
// Keycloak realm and admin client, OIDC client, tenant naming. Secrets are sealed with the master key
// (design §4.6). The document overlays a loaded config.Config, which
// keeps the process settings (listeners, TLS, paths, log).
package settings

import (
	"encoding/json"
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
	// PortalHost is the public name tenants use (the portal, every site
	// behind it); prism_host, its name before v0.1.0, is still read.
	PortalHost string `json:"portal_host"`
	IDHost     string `json:"id_host"`

	// Sites replaced the single prism_central block in Milestone 4; a
	// stored legacy document becomes site-1 when it is read.
	Sites []Site `json:"sites"`
	// PassthroughSite is the site the internal pass-through serves; empty
	// means the first by order.
	PassthroughSite string `json:"passthrough_site"`

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
		ProjectPrefix    string        `json:"project_prefix"`
		GroupPrefix      string        `json:"group_prefix"`
		Role             string        `json:"role"`
		OperationTimeout time.Duration `json:"operation_timeout"`
		// AddressPool is the private prefix the tenants' /24s come from
		// (tenant networking design 4.2).
		AddressPool string `json:"address_pool"`
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

// UnmarshalJSON reads the document, accepting prism_host for portal_host:
// a document stored before the rename loads unchanged and is written back
// with the new key.
func (s *Settings) UnmarshalJSON(data []byte) error {
	type plain Settings
	aux := struct {
		*plain
		PrismHost string `json:"prism_host"`
	}{plain: (*plain)(s)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.PrismHost != "" {
		if s.PortalHost != "" && s.PortalHost != aux.PrismHost {
			return errors.New("settings: prism_host is the old name of portal_host; keep one")
		}
		s.PortalHost = aux.PrismHost
	}
	return nil
}

// FromConfig builds the document from a loaded YAML config and the
// contents of its secret files. The file's one Prism Central is site-1.
func FromConfig(cfg *config.Config, apiKey, adminSecret, clientSecret, caPEM string) Settings {
	var s Settings
	s.PortalHost, s.IDHost = cfg.Public.PortalHost, cfg.Public.IDHost
	if cfg.PrismCentral.URL != "" {
		name := cfg.PrismCentral.URL
		if u, err := url.Parse(cfg.PrismCentral.URL); err == nil && u.Hostname() != "" {
			name = u.Hostname()
		}
		s.Sites = []Site{{
			ID: LegacySiteID, Code: LegacySiteCode, Name: name,
			URL: cfg.PrismCentral.URL, TLSVerify: cfg.PrismCentral.TLSVerify, CAPEM: caPEM,
			APIKey: Secret(apiKey), SAMLIdPName: cfg.PrismCentral.SAMLIdPName,
			ClusterExtID: cfg.Tenants.ClusterExtID, StorageContainerExtID: cfg.Tenants.StorageContainerExtID,
		}}
	}
	s.Keycloak.Realm = cfg.Keycloak.Realm
	s.Keycloak.AdminClientID = cfg.Keycloak.AdminClientID
	s.Keycloak.AdminClientSecret = Secret(adminSecret)
	s.OIDC.Issuer, s.OIDC.ClientID = cfg.OIDC.Issuer, cfg.OIDC.ClientID
	s.OIDC.ClientSecret = Secret(clientSecret)
	s.Tenants.ProjectPrefix = cfg.Tenants.ProjectPrefix
	s.Tenants.GroupPrefix = cfg.Tenants.GroupPrefix
	s.Tenants.Role = cfg.Tenants.Role
	s.Tenants.OperationTimeout = cfg.Tenants.OperationTimeout
	return s
}

// Overlay writes the document onto cfg's runtime sections and returns
// the secrets. The secret file fields are set to FromStore and the CA
// file is cleared: the caller uses Runtime instead of files. The config
// has one Prism Central: it gets the first site by order, and Runtime
// carries that site's key and CA; the sites themselves are read from
// the document.
func (s Settings) Overlay(cfg *config.Config) (Runtime, error) {
	var first Site
	if o := s.OrderedSites(); len(o) > 0 {
		first = o[0]
	}
	cfg.Public.PortalHost, cfg.Public.IDHost = s.PortalHost, s.IDHost
	cfg.PrismCentral.URL = first.URL
	cfg.PrismCentral.TLSVerify = first.TLSVerify
	cfg.PrismCentral.CAFile = ""
	cfg.PrismCentral.APIKeyFile = FromStore
	cfg.PrismCentral.SAMLIdPName = first.SAMLIdPName
	cfg.Keycloak.Realm = s.Keycloak.Realm
	cfg.Keycloak.AdminClientID = s.Keycloak.AdminClientID
	cfg.Keycloak.AdminClientSecretFile = FromStore
	cfg.OIDC.Issuer, cfg.OIDC.ClientID = s.OIDC.Issuer, s.OIDC.ClientID
	cfg.OIDC.ClientSecretFile = FromStore
	cfg.Tenants.ProjectPrefix = s.Tenants.ProjectPrefix
	cfg.Tenants.GroupPrefix = s.Tenants.GroupPrefix
	cfg.Tenants.Role = s.Tenants.Role
	cfg.Tenants.ClusterExtID = first.ClusterExtID
	cfg.Tenants.StorageContainerExtID = first.StorageContainerExtID
	cfg.Tenants.OperationTimeout = s.Tenants.OperationTimeout
	return Runtime{APIKey: string(first.APIKey), AdminSecret: string(s.Keycloak.AdminClientSecret), ClientSecret: string(s.OIDC.ClientSecret), CAPEM: first.CAPEM}, nil
}

// Defaults is the document a fresh appliance starts from: the naming
// and timing conventions filled in, everything that names the MSP's
// systems left empty (design §4.7, setup mode).
func Defaults() Settings {
	var s Settings
	s.Keycloak.AdminClientID = "portico-admin"
	s.OIDC.ClientID = "portico"
	s.Tenants.ProjectPrefix, s.Tenants.GroupPrefix, s.Tenants.Role = "portico", "tenant-", DefaultTenantRole
	s.Tenants.OperationTimeout = 10 * time.Minute
	s.Tenants.AddressPool = DefaultAddressPool
	return s
}

// DefaultTenantRole is the role tenant groups get in their project: Project
// Admin without the floating IP creation and deletion (tenant networking
// design 4.3), created on each site by its setup run.
const DefaultTenantRole = "Portico Tenant"

// DefaultAddressPool is the tenant address pool of a fresh document.
const DefaultAddressPool = "10.100.0.0/16"

// upgradeTenants fills what a document written before 0.2.0 lacks: the
// pool, and the tenant role. Project Admin was 0.1.0's default, not a
// choice, so it moves to Portico Tenant; another role is kept.
func upgradeTenants(s *Settings) {
	if s.Tenants.AddressPool == "" {
		s.Tenants.AddressPool = DefaultAddressPool
	}
	if s.Tenants.Role == "Project Admin" {
		s.Tenants.Role = DefaultTenantRole
	}
}

// Missing lists the required fields that are empty, in document order.
// A document with nothing missing is complete and can be applied. At
// least one complete site (URL, key and identity provider) is required;
// a site still being added is listed by IncompleteSites and not served,
// without making the document incomplete.
func (s Settings) Missing() []string {
	var out []string
	add := func(key, value string) {
		if strings.TrimSpace(value) == "" {
			out = append(out, key)
		}
	}
	add("portal_host", s.PortalHost)
	add("id_host", s.IDHost)
	if len(s.ServedSites()) == 0 {
		out = append(out, "sites")
	}
	for _, r := range []struct{ key, value string }{
		{"keycloak.realm", s.Keycloak.Realm},
		{"keycloak.admin_client_id", s.Keycloak.AdminClientID},
		{"keycloak.admin_client_secret", string(s.Keycloak.AdminClientSecret)},
		{"oidc.issuer", s.OIDC.Issuer},
		{"oidc.client_id", s.OIDC.ClientID},
		{"oidc.client_secret", string(s.OIDC.ClientSecret)},
		{"tenants.project_prefix", s.Tenants.ProjectPrefix},
		{"tenants.group_prefix", s.Tenants.GroupPrefix},
		{"tenants.role", s.Tenants.Role},
	} {
		add(r.key, r.value)
	}
	if s.Tenants.OperationTimeout == 0 {
		out = append(out, "tenants.operation_timeout")
	}
	return out
}

var rePrefix = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// reHost is a host name with an optional port: labels of letters, digits
// and dashes, not starting with a dash. The identity host ends up in
// Keycloak's environment file, so nothing else may pass.
var reHost = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*(:[0-9]{1,5})?$`)

// ValidHost reports whether h is a host name with an optional port.
func ValidHost(h string) bool { return reHost.MatchString(h) }

// ValidateFormat checks the shape of the values that are set and says
// nothing about the empty ones, so a draft can be stored card by card.
func (s Settings) ValidateFormat() error {
	var errs []error
	for _, r := range []struct{ key, value string }{{"portal_host", s.PortalHost}, {"id_host", s.IDHost}} {
		if r.value != "" && !ValidHost(r.value) {
			errs = append(errs, fmt.Errorf("settings: %s %q must be a host name (letters, digits, dots, dashes, optional :port)", r.key, r.value))
		}
	}
	if s.PortalHost != "" && strings.EqualFold(strings.TrimSpace(s.PortalHost), strings.TrimSpace(s.IDHost)) {
		errs = append(errs, errors.New("settings: id_host must differ from portal_host"))
	}
	errs = append(errs, s.validateSites()...)
	for _, r := range []struct{ key, value string }{{"oidc.issuer", s.OIDC.Issuer}} {
		if r.value == "" {
			continue
		}
		u, err := url.Parse(r.value)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
			errs = append(errs, fmt.Errorf("settings: %s %q is not an http(s) URL", r.key, r.value))
		case u.User != nil:
			// Not echoed: the userinfo is a password in clear.
			errs = append(errs, fmt.Errorf("settings: %s must not carry credentials", r.key))
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
