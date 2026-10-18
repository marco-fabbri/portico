package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/marco-fabbri/portico/internal/config"
	"github.com/marco-fabbri/portico/internal/secrets"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
	"gopkg.in/yaml.v3"
)

// euid is overridable in tests.
var euid = os.Geteuid

// openSettings opens the store and the master key for the settings
// commands. create says whether a missing key may be created: only the
// import does, and only when not running as root, so the key belongs to
// the user who runs the service.
func openSettings(cfg *config.Config, create bool) (*store.SQLite, *settings.Service, error) {
	st, err := store.OpenSQLite(cfg.Store.Path)
	if err != nil {
		return nil, nil, err
	}
	load := secrets.LoadKey
	if create {
		load = secrets.LoadOrCreateKey
	}
	key, err := load(cfg.Store.MasterKeyFile)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	sealer, err := secrets.NewSealer(key)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, settings.NewService(st, sealer), nil
}

// settingsImport moves the YAML's runtime sections and the contents of
// its secret files into the store, sealed with the master key.
func settingsImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("settings import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "/etc/portico/portico.yaml", "path to the settings file")
	force := fs.Bool("force", false, "replace settings already stored")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	if err := cfg.ValidateRuntime(); err != nil {
		fmt.Fprintf(stderr, "portico: the file must hold the runtime sections to import: %v\n", err)
		return 1
	}
	apiKey, err := cfg.PrismAPIKey()
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	adminSecret, err := cfg.KeycloakAdminSecret()
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	clientSecret, err := cfg.ClientSecret()
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	var caPEM string
	if cfg.PrismCentral.CAFile != "" {
		b, err := os.ReadFile(cfg.PrismCentral.CAFile)
		if err != nil {
			fmt.Fprintf(stderr, "portico: prism_central.ca_file: %v\n", err)
			return 1
		}
		caPEM = string(b)
	}
	if euid() == 0 {
		fmt.Fprintln(stderr, "portico: settings import must run as the service user, not root: `sudo -u portico portico settings import ...`, so the master key it creates belongs to the user that runs `portico serve`")
		return 1
	}
	st, svc, err := openSettings(cfg, true)
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()
	if svc.Stored(ctx) && !*force {
		fmt.Fprintln(stderr, "portico: settings are already stored; use -force to replace them with the file's values")
		return 2
	}
	doc := settings.FromConfig(cfg, apiKey, adminSecret, clientSecret, caPEM)
	if err := svc.Set(ctx, doc); err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "settings imported into the store (sealed with the master key):")
	for _, f := range []string{"public.prism_host", "public.id_host", "prism_central.url", "prism_central.tls_verify", "prism_central.ca_pem", "prism_central.api_key", "prism_central.saml_idp_name", "keycloak.realm", "keycloak.admin_client_id", "keycloak.admin_client_secret", "oidc.issuer", "oidc.client_id", "oidc.client_secret", "tenants.*"} {
		fmt.Fprintln(stdout, "  "+f)
	}
	fmt.Fprintln(stdout, "the YAML's runtime sections and secret files can now be removed; the store wins from the next start")
	return 0
}

// settingsShow prints the stored settings with secrets redacted.
func settingsShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("settings show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "/etc/portico/portico.yaml", "path to the settings file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	// Nothing stored: say so before touching the key (show never creates one).
	st, err := store.OpenSQLite(cfg.Store.Path)
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	if _, err := st.GetSetting(context.Background(), settings.SettingKey); errors.Is(err, store.ErrNotFound) {
		st.Close()
		fmt.Fprintln(stderr, "portico: no settings stored (the runtime sections come from the file)")
		return 1
	}
	st.Close()
	st, svc, err := openSettings(cfg, false)
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	defer st.Close()
	doc, err := svc.Get(context.Background())
	if errors.Is(err, settings.ErrNotStored) {
		fmt.Fprintln(stderr, "portico: no settings stored (the runtime sections come from the file)")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	out, err := yaml.Marshal(redacted(doc))
	if err != nil {
		fmt.Fprintf(stderr, "portico: %v\n", err)
		return 1
	}
	stdout.Write(out)
	return 0
}

// redacted is the printable form of the document: secrets as <set>/<unset>.
func redacted(d settings.Settings) map[string]any {
	set := func(s settings.Secret) string {
		if s == "" {
			return "<unset>"
		}
		return "<set>"
	}
	return map[string]any{
		"public": map[string]any{"prism_host": d.PrismHost, "id_host": d.IDHost},
		"prism_central": map[string]any{
			"url": d.PrismCentral.URL, "tls_verify": d.PrismCentral.TLSVerify,
			"ca_pem": func() string {
				if d.PrismCentral.CAPEM == "" {
					return "<unset>"
				}
				return "<set>"
			}(),
			"api_key": set(d.PrismCentral.APIKey), "saml_idp_name": d.PrismCentral.SAMLIdPName,
		},
		"keycloak": map[string]any{"realm": d.Keycloak.Realm, "admin_client_id": d.Keycloak.AdminClientID, "admin_client_secret": set(d.Keycloak.AdminClientSecret)},
		"oidc":     map[string]any{"issuer": d.OIDC.Issuer, "client_id": d.OIDC.ClientID, "client_secret": set(d.OIDC.ClientSecret)},
		"tenants": map[string]any{
			"project_prefix": d.Tenants.ProjectPrefix, "group_prefix": d.Tenants.GroupPrefix, "role": d.Tenants.Role,
			"cluster_ext_id": d.Tenants.ClusterExtID, "storage_container_ext_id": d.Tenants.StorageContainerExtID,
			"operation_timeout": d.Tenants.OperationTimeout.String(),
		},
	}
}
