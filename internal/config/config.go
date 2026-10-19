// Package config loads Portico's settings from a YAML file.
//
// The file holds static settings only (listeners, host names, upstreams,
// the OIDC client). Secrets are referenced by path and read from files
// with mode 0600; nothing secret lives in the YAML itself.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// TLS names a certificate and key pair on disk. Both empty means plain HTTP.
type TLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// Public configures the tenant-facing listener.
type Public struct {
	Listen    string `yaml:"listen"`
	TLS       TLS    `yaml:"tls"`
	PrismHost string `yaml:"prism_host"`
	IDHost    string `yaml:"id_host"`
}

// Internal configures the administrator-facing listeners.
type Internal struct {
	AdminListen       string `yaml:"admin_listen"`
	PassthroughListen string `yaml:"passthrough_listen"`
	TLS               TLS    `yaml:"tls"`
}

// PrismCentral is the upstream Prism Central and Portico's service
// account on it (design §4.5): an API key in a file with mode 0600.
type PrismCentral struct {
	URL         string `yaml:"url"`
	TLSVerify   bool   `yaml:"tls_verify"`
	CAFile      string `yaml:"ca_file"`
	APIKeyFile  string `yaml:"api_key_file"`
	SAMLIdPName string `yaml:"saml_idp_name"`
}

// Keycloak is the backchannel address of the identity provider and the
// confidential client Portico uses on its admin API.
type Keycloak struct {
	URL                   string `yaml:"url"`
	Realm                 string `yaml:"realm"`
	AdminClientID         string `yaml:"admin_client_id"`
	AdminClientSecretFile string `yaml:"admin_client_secret_file"`
	// ThemeDir is where Portico writes the login theme generated from the
	// branding; Keycloak mounts it as /opt/keycloak/themes/<ThemeName>.
	ThemeDir  string `yaml:"theme_dir"`
	ThemeName string `yaml:"theme_name"`
	// HostnameFile, when set, is written on Apply with KC_HOSTNAME for the
	// identity host (the appliance's Keycloak loads it, design 4.9).
	HostnameFile string `yaml:"hostname_file"`
}

// Tenants holds naming and placement for tenant provisioning (design §4.4).
type Tenants struct {
	ProjectPrefix         string        `yaml:"project_prefix"`
	GroupPrefix           string        `yaml:"group_prefix"`
	Role                  string        `yaml:"role"`
	ClusterExtID          string        `yaml:"cluster_ext_id"`
	StorageContainerExtID string        `yaml:"storage_container_ext_id"`
	OperationTimeout      time.Duration `yaml:"operation_timeout"`
}

// OIDC is Portico's own client registration in Keycloak.
type OIDC struct {
	Issuer           string `yaml:"issuer"`
	ClientID         string `yaml:"client_id"`
	ClientSecretFile string `yaml:"client_secret_file"`
}

// Session holds the Portico session lifetimes.
type Session struct {
	MaxAge time.Duration `yaml:"max_age"`
	Idle   time.Duration `yaml:"idle"`
}

// Policy holds the path allow-list for the Prism Central host.
type Policy struct {
	AllowPrefixes []string `yaml:"allow_prefixes"`
}

// Log holds logging settings.
type Log struct {
	Level string `yaml:"level"`
}

// Store locates Portico's own database and the master key that seals
// the secrets in it (design §4.6).
type Store struct {
	Path          string `yaml:"path"`
	MasterKeyFile string `yaml:"master_key_file"`
}

// Config is the whole settings file.
type Config struct {
	Public       Public       `yaml:"public"`
	Internal     Internal     `yaml:"internal"`
	PrismCentral PrismCentral `yaml:"prism_central"`
	Keycloak     Keycloak     `yaml:"keycloak"`
	OIDC         OIDC         `yaml:"oidc"`
	Session      Session      `yaml:"session"`
	Policy       Policy       `yaml:"policy"`
	Log          Log          `yaml:"log"`
	Store        Store        `yaml:"store"`
	Tenants      Tenants      `yaml:"tenants"`
}

// DefaultAllowPrefixes are the path families the Prism Central console uses
// for a tenant user, measured on pc.7.6.0.6 (docs/lab write-ups 06 and 08).
// An entry ending in "/" matches that path and everything below it; any
// other entry matches exactly.
var DefaultAllowPrefixes = []string{
	"/", "/console/", "/static/", "/ui/iam/", "/api/", "/dm/",
	"/PrismGateway/services/rest/v1/", "/vnc/", "/auth_code", "/favicon.ico",
	// Prism Central's own Sign Out ends on /redirect?logout=<url> (measured
	// 2026-10-18, write-up 12); exact match, the query is not part of policy.
	"/redirect",
}

// Default returns the settings used when the file leaves a key out.
func Default() Config {
	return Config{
		Public:       Public{Listen: ":443"},
		Internal:     Internal{AdminListen: "127.0.0.1:8443", PassthroughListen: "127.0.0.1:9440"},
		PrismCentral: PrismCentral{TLSVerify: true},
		Keycloak:     Keycloak{URL: "http://127.0.0.1:8080", AdminClientID: "portico-admin", ThemeDir: "/var/lib/portico/keycloak-theme", ThemeName: "portico"},
		Session:      Session{MaxAge: 8 * time.Hour, Idle: 15 * time.Minute},
		Policy:       Policy{AllowPrefixes: append([]string(nil), DefaultAllowPrefixes...)},
		Log:          Log{Level: "info"},
		Store:        Store{Path: "/var/lib/portico/portico.db", MasterKeyFile: "/var/lib/portico/master.key"},
		Tenants:      Tenants{ProjectPrefix: "portico", GroupPrefix: "tenant-", Role: "Project Admin", OperationTimeout: 10 * time.Minute},
	}
}

// Load reads the YAML file at path on top of Default and validates it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	// Only the process checks here: the runtime sections may live in the
	// store (design §4.7); serve validates them after the overlay.
	if err := cfg.ValidateProcess(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate runs the process checks and the runtime checks.
func (c *Config) Validate() error {
	return errors.Join(c.ValidateProcess(), c.ValidateRuntime())
}

// ValidateProcess checks what the process needs before it can open its
// store: listeners and their TLS, session lifetimes, store and key
// paths, the Keycloak theme fields.
func (c *Config) ValidateProcess() error {
	var errs []error
	for _, r := range []struct{ key, value string }{
		{"store.path", c.Store.Path},
		{"store.master_key_file", c.Store.MasterKeyFile},
		{"keycloak.theme_dir", c.Keycloak.ThemeDir},
		{"keycloak.theme_name", c.Keycloak.ThemeName},
	} {
		if strings.TrimSpace(r.value) == "" {
			errs = append(errs, fmt.Errorf("config: %s is required", r.key))
		}
	}
	for _, t := range []struct {
		key string
		tls TLS
	}{{"public.tls", c.Public.TLS}, {"internal.tls", c.Internal.TLS}} {
		if (t.tls.CertFile == "") != (t.tls.KeyFile == "") {
			errs = append(errs, fmt.Errorf("config: %s needs both cert_file and key_file, or neither", t.key))
		}
	}
	if c.Session.MaxAge <= 0 || c.Session.Idle <= 0 {
		errs = append(errs, errors.New("config: session.max_age and session.idle must be positive"))
	}
	// The theme name is a directory name inside the container.
	if !rePrefix.MatchString(c.Keycloak.ThemeName) {
		errs = append(errs, fmt.Errorf("config: keycloak.theme_name %q must be lower-case letters, digits and dashes", c.Keycloak.ThemeName))
	}
	// The public listener must not take Keycloak's published loopback
	// port: in tunnel mode cloudflared would reach Keycloak instead.
	if u, err := url.Parse(c.Keycloak.URL); err == nil && u.Host != "" && sameListen(c.Public.Listen, u.Host) {
		errs = append(errs, fmt.Errorf("config: public.listen %q is keycloak.url's address; give the public listener another port", c.Public.Listen))
	}
	// The admin API authenticates with HTTP Basic (the break-glass
	// password) and the pass-through carries local Prism Central
	// credentials: off the loopback they travel only under TLS.
	if c.Internal.TLS.CertFile == "" {
		for _, l := range []struct{ key, addr string }{{"internal.admin_listen", c.Internal.AdminListen}, {"internal.passthrough_listen", c.Internal.PassthroughListen}} {
			if !isLoopbackListen(l.addr) {
				errs = append(errs, fmt.Errorf("config: %s %q is not a loopback address and internal.tls is empty; credentials would travel in clear", l.key, l.addr))
			}
		}
	}
	return errors.Join(errs...)
}

// ValidateRuntime checks the sections that may come from the YAML or
// from the store (design §4.7): public names, Prism Central, Keycloak
// realm and client, OIDC client, tenant naming and placement.
func (c *Config) ValidateRuntime() error {
	var errs []error
	required := []struct{ key, value string }{
		{"public.prism_host", c.Public.PrismHost},
		{"public.id_host", c.Public.IDHost},
		{"prism_central.url", c.PrismCentral.URL},
		{"oidc.issuer", c.OIDC.Issuer},
		{"oidc.client_id", c.OIDC.ClientID},
		{"oidc.client_secret_file", c.OIDC.ClientSecretFile},
		{"prism_central.api_key_file", c.PrismCentral.APIKeyFile},
		{"prism_central.saml_idp_name", c.PrismCentral.SAMLIdPName},
		{"keycloak.realm", c.Keycloak.Realm},
		{"keycloak.admin_client_id", c.Keycloak.AdminClientID},
		{"keycloak.admin_client_secret_file", c.Keycloak.AdminClientSecretFile},
		{"tenants.cluster_ext_id", c.Tenants.ClusterExtID},
		{"tenants.storage_container_ext_id", c.Tenants.StorageContainerExtID},
	}
	for _, r := range required {
		if strings.TrimSpace(r.value) == "" {
			errs = append(errs, fmt.Errorf("config: %s is required", r.key))
		}
	}
	// The prefix becomes part of a project id and a group name.
	if !rePrefix.MatchString(c.Tenants.ProjectPrefix) {
		errs = append(errs, fmt.Errorf("config: tenants.project_prefix %q must be lower-case letters, digits and dashes", c.Tenants.ProjectPrefix))
	}
	if !rePrefix.MatchString(strings.TrimSuffix(c.Tenants.GroupPrefix, "-")) {
		errs = append(errs, fmt.Errorf("config: tenants.group_prefix %q must be lower-case letters, digits and dashes", c.Tenants.GroupPrefix))
	}
	if c.Tenants.OperationTimeout <= 0 {
		errs = append(errs, errors.New("config: tenants.operation_timeout must be positive"))
	}
	return errors.Join(errs...)
}

// isLoopbackListen reports whether a listen address binds the loopback
// only ("127.0.0.1:8443", "[::1]:8443", "localhost:8443"). An empty host
// (":8443") binds every interface.
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var rePrefix = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// PrismAPIKey reads the service-account API key file (mode 0600).
func (c *Config) PrismAPIKey() (string, error) {
	return readSecretFile(c.PrismCentral.APIKeyFile)
}

// KeycloakAdminSecret reads the admin client secret file (mode 0600).
func (c *Config) KeycloakAdminSecret() (string, error) {
	return readSecretFile(c.Keycloak.AdminClientSecretFile)
}

// ClientSecret reads the OIDC client secret file. The file must not be
// readable by group or others; one trailing newline is removed.
func (c *Config) ClientSecret() (string, error) {
	return readSecretFile(c.OIDC.ClientSecretFile)
}

func readSecretFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("secret file: %w", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return "", fmt.Errorf("secret file %s has mode %04o; it must not be readable by group or others", path, mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("secret file: %w", err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	s = strings.TrimSuffix(s, "\r")
	if s == "" {
		return "", fmt.Errorf("secret file %s is empty", path)
	}
	return s, nil
}

// sameListen reports whether a listen address and a host:port name the
// same socket: equal ports and equal hosts, an empty or unspecified host
// matching every address.
func sameListen(listen, hostport string) bool {
	lh, lp, err1 := net.SplitHostPort(listen)
	hh, hp, err2 := net.SplitHostPort(hostport)
	if err1 != nil || err2 != nil || lp != hp {
		return false
	}
	if lh == "" || lh == "0.0.0.0" || lh == "::" {
		return true
	}
	return lh == hh || (lh == "127.0.0.1" && hh == "localhost") || (lh == "localhost" && hh == "127.0.0.1")
}
