package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/branding"
	"github.com/marco-fabbri/portico/internal/config"
	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/proxy"
	"github.com/marco-fabbri/portico/internal/secrets"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
	"github.com/marco-fabbri/portico/internal/web"
)

// serverTLS returns the server-side TLS configuration for a listener, or
// nil when no certificate is configured (plain HTTP, tunnel mode).
func serverTLS(t config.TLS) (*tls.Config, error) {
	if t.CertFile == "" && t.KeyFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}

// ErrRestart is what runServe returns when an administrator applied the
// stored settings: main exits 0 and the unit's Restart=always brings the
// service back with the new document.
var ErrRestart = errors.New("restart requested")

type listener struct {
	name string
	srv  *http.Server
	tls  *tls.Config
}

// runServe opens the store and the settings, then runs Portico in one
// of two modes until ctx is done: running (the three listeners) when
// the settings are complete, setup (the admin listener only, design
// §4.7) when the stored document is a draft or nothing is stored and
// the file does not carry a complete runtime configuration. Apply
// returns ErrRestart in both modes.
func runServe(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	st, err := store.OpenSQLite(cfg.Store.Path)
	if err != nil {
		return err
	}
	defer st.Close()
	// Runtime settings: the store wins when it holds a document (design
	// §4.7); otherwise the YAML's runtime sections and secret files apply.
	masterKey, err := secrets.LoadOrCreateKey(cfg.Store.MasterKeyFile)
	if err != nil {
		return err
	}
	sealer, err := secrets.NewSealer(masterKey)
	if err != nil {
		return err
	}
	settingsSvc := settings.NewService(st, sealer)
	stored := settingsSvc.Stored(ctx)
	var missing []string
	setupMode := false
	var secret, apiKey, kcSecret string
	var caPEM []byte
	if stored {
		doc, err := settingsSvc.Get(ctx)
		if err != nil {
			return err
		}
		var inc *settings.IncompleteError
		if err := doc.Validate(); errors.As(err, &inc) {
			setupMode, missing = true, inc.Missing
		} else if err != nil {
			return err
		}
		if !setupMode {
			rt, err := doc.Overlay(cfg)
			if err != nil {
				return err
			}
			secret, apiKey, kcSecret, caPEM = rt.ClientSecret, rt.APIKey, rt.AdminSecret, []byte(rt.CAPEM)
		}
		log.Info("settings source", "source", "store")
	} else if err := cfg.ValidateRuntime(); err != nil {
		setupMode, missing = true, settings.Defaults().Missing()
		log.Info("settings source", "source", "none", "file", err.Error())
	} else {
		log.Info("settings source", "source", "file")
	}
	// Branding: read by every tenant-facing page and error page.
	brand := branding.NewService(st, log)
	// Settings: the API and the setup page edit the stored document.
	restart := make(chan struct{})
	var restartOnce sync.Once
	// The appliance's Keycloak learns its public name from the hosts card
	// (the SAML bootstrap needs it before Apply, measured on NTNX-01).
	if hf := cfg.Keycloak.HostnameFile; hf != "" {
		settingsSvc.OnChange(func(ctx context.Context, s settings.Settings) error {
			return settings.WriteKeycloakHostname(hf, s.IDHost)
		})
	}
	setup := &api.SettingsOptions{Service: settingsSvc, Checker: settings.NewChecker(log), Bootstrapper: settings.NewBootstrapper(log, settingsSvc), KeycloakURL: cfg.Keycloak.URL, HostnameFile: cfg.Keycloak.HostnameFile,
		Restart: func() { restartOnce.Do(func() { close(restart) }) }}
	if setupMode {
		setup.Mode = "setup"
		log.Warn("setup mode: the public and pass-through listeners are off", "missing", missing)
		brand.OnChange(func(ctx context.Context, b branding.Branding) error {
			if err := branding.WriteTheme(cfg.Keycloak.ThemeDir, b); err != nil {
				return fmt.Errorf("theme files: %w", err)
			}
			return nil
		})
		admin, limiter, adminSessions, err := adminListener(cfg, st, brand, nil, setup, log)
		if err != nil {
			return err
		}
		return serveListeners(ctx, []listener{admin}, restart, func(now time.Time) {
			adminSessions.Sweep(now)
			limiter.Sweep(now)
		}, func() {}, log)
	}
	setup.Mode = "running"
	if !stored {
		if secret, err = cfg.ClientSecret(); err != nil {
			return err
		}
		if apiKey, err = cfg.PrismAPIKey(); err != nil {
			return err
		}
		if kcSecret, err = cfg.KeycloakAdminSecret(); err != nil {
			return err
		}
		if cfg.PrismCentral.CAFile != "" && cfg.PrismCentral.TLSVerify {
			if caPEM, err = os.ReadFile(cfg.PrismCentral.CAFile); err != nil {
				return fmt.Errorf("prism_central.ca_file: %w", err)
			}
		}
		// When the process runs from the file, the setup page prefills
		// from it (with the secrets the process already read) so the
		// first save adopts it.
		fileDoc := settings.FromConfig(cfg, apiKey, kcSecret, secret, string(caPEM))
		setup.File = &fileDoc
	}
	pages := branding.NewPages(brand.Get)
	pcURL, err := url.Parse(cfg.PrismCentral.URL)
	if err != nil {
		return fmt.Errorf("prism_central.url: %w", err)
	}
	kcURL, err := url.Parse(cfg.Keycloak.URL)
	if err != nil {
		return fmt.Errorf("keycloak.url: %w", err)
	}
	issuer, err := url.Parse(cfg.OIDC.Issuer)
	if err != nil {
		return fmt.Errorf("oidc.issuer: %w", err)
	}
	var pcTLS *tls.Config
	if pcURL.Scheme == "https" {
		if !cfg.PrismCentral.TLSVerify {
			log.Warn("prism_central.tls_verify is false: the certificate of Prism Central is not checked")
		}
		pcTLS, err = proxy.PrismTLSPEM(pcURL.Hostname(), caPEM, cfg.PrismCentral.TLSVerify)
		if err != nil {
			return fmt.Errorf("prism_central ca: %w", err)
		}
	}

	sessions := auth.NewMemoryStore(cfg.Session.MaxAge, cfg.Session.Idle)
	authn := auth.NewAuthenticator(auth.OIDCConfig{
		Issuer: cfg.OIDC.Issuer,
		// The issuer's path is the realm path; the loopback base plus that
		// path is the same realm on the backchannel.
		DiscoveryURL: strings.TrimSuffix(cfg.Keycloak.URL, "/") + issuer.Path,
		ClientID:     cfg.OIDC.ClientID,
		ClientSecret: secret,
		RedirectURL:  "https://" + cfg.Public.PrismHost + "/.portico/callback",
		CookieMaxAge: cfg.Session.MaxAge,
		ErrorPage:    pages.Error,
	}, sessions, &http.Client{Timeout: 15 * time.Second}, log)

	stripped := auth.PorticoCookies()
	prismUp := proxy.NewUpstream(proxy.UpstreamOptions{Target: pcURL, HostHeader: cfg.Public.PrismHost, TLS: pcTLS, StripCookies: stripped, Log: log, ErrorPage: pages.Error})
	kcUp := proxy.NewUpstream(proxy.UpstreamOptions{Target: kcURL, HostHeader: cfg.Public.IDHost, StripCookies: stripped, Log: log, ErrorPage: pages.Error})
	// The pass-through keeps the caller's Host so Prism Central builds its
	// redirects for the address the administrator used.
	passthrough := proxy.NewPassthrough(proxy.NewUpstream(proxy.UpstreamOptions{Target: pcURL, HostHeader: "", TLS: pcTLS, Log: log}), log)

	// Tenant provisioning: Prism Central through the service account,
	// Keycloak through the admin client on the backchannel.
	pcClient, err := nutanix.New(cfg.PrismCentral.URL, apiKey, pcTLS, log)
	if err != nil {
		return err
	}
	kcClient := keycloak.New(cfg.Keycloak.URL, cfg.Keycloak.Realm, cfg.Keycloak.AdminClientID, kcSecret, &http.Client{Timeout: 15 * time.Second}, log)
	tenants := tenant.NewService(st, pcClient, kcClient, tenant.Config{
		ProjectPrefix: cfg.Tenants.ProjectPrefix, GroupPrefix: cfg.Tenants.GroupPrefix, RoleName: cfg.Tenants.Role,
		SAMLIdPName: cfg.PrismCentral.SAMLIdPName, ClusterExtID: cfg.Tenants.ClusterExtID,
		StorageContainerExtID: cfg.Tenants.StorageContainerExtID, OperationTimeout: cfg.Tenants.OperationTimeout,
	}, log)
	if n, err := tenants.RecoverInterrupted(ctx); err != nil {
		return fmt.Errorf("tenant recovery: %w", err)
	} else if n > 0 {
		log.Warn("tenants interrupted by the previous run marked failed", "count", n)
	}
	setup.Busy = tenants.Running

	publicTLS, err := serverTLS(cfg.Public.TLS)
	if err != nil {
		return fmt.Errorf("public: %w", err)
	}
	internalTLS, err := serverTLS(cfg.Internal.TLS)
	if err != nil {
		return fmt.Errorf("internal: %w", err)
	}
	// Saving the branding regenerates the Keycloak login theme and sets it
	// on the realm; a failure is reported to the admin, the save stands.
	brand.OnChange(func(ctx context.Context, b branding.Branding) error {
		if err := branding.WriteTheme(cfg.Keycloak.ThemeDir, b); err != nil {
			return fmt.Errorf("theme files: %w", err)
		}
		if err := kcClient.SetRealmLoginTheme(ctx, cfg.Keycloak.ThemeName); err != nil {
			return fmt.Errorf("keycloak realm: %w", err)
		}
		return nil
	})
	if brand.Stored(ctx) {
		if err := brand.Apply(ctx, brand.Hook(), brand.Get(ctx)); err != nil {
			log.Warn("branding: theme not applied at start", "err", err.Error())
		}
	}
	admin, limiter, adminSessions, err := adminListener(cfg, st, brand, tenants, setup, log)
	if err != nil {
		return err
	}
	// Tenant-facing pages: the landing page replaces Prism Central's
	// provider page and starts the SAML login with the browser's own
	// login-request cookie (write-ups 12 and 13).
	landing := &proxy.LandingOptions{
		Pages: pages,
		Logo: func(ctx context.Context) ([]byte, string, bool) {
			b := brand.Get(ctx)
			return b.LogoData, b.LogoType, b.HasLogo()
		},
		PrismHTTP: &http.Client{
			Transport:     &http.Transport{TLSClientConfig: pcTLS, ForceAttemptHTTP2: false},
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		PrismBase: &url.URL{Scheme: pcURL.Scheme, Host: pcURL.Host},
		PrismHost: cfg.Public.PrismHost,
		IdPExtID:  cachedIdPLookup(pcClient, cfg.PrismCentral.SAMLIdPName, 10*time.Minute),
		Log:       log,
	}
	public := proxy.NewPublic(proxy.PublicOptions{
		PrismHost: cfg.Public.PrismHost, IDHost: cfg.Public.IDHost,
		Prism: prismUp, Keycloak: kcUp, Auth: authn,
		Policy: proxy.PrismPolicy{AllowPrefixes: cfg.Policy.AllowPrefixes}, Log: log,
		Landing: landing,
	})
	listeners := []listener{
		{"public", newServer(cfg.Public.Listen, public, log), publicTLS},
		{"passthrough", newServer(cfg.Internal.PassthroughListen, passthrough, log), internalTLS},
		admin,
	}
	return serveListeners(ctx, listeners, restart, func(now time.Time) {
		if n := sessions.Sweep(now); n > 0 {
			log.Debug("sessions swept", "count", n)
		}
		adminSessions.Sweep(now)
		limiter.Sweep(now)
	}, tenants.Wait, log)
}

func newServer(addr string, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// adminListener builds the admin listener shared by both modes: the
// REST API and the UI behind the break-glass account, one failure
// limiter and one authentication middleware. tenants is nil in setup
// mode, which makes the tenant routes answer 503.
func adminListener(cfg *config.Config, st store.Store, brand *branding.Service, tenants *tenant.Service, setup *api.SettingsOptions, log *slog.Logger) (listener, *api.Limiter, *auth.MemoryStore, error) {
	internalTLS, err := serverTLS(cfg.Internal.TLS)
	if err != nil {
		return listener{}, nil, nil, fmt.Errorf("internal: %w", err)
	}
	limiter := api.NewLimiter()
	adminSessions := auth.NewMemoryStore(8*time.Hour, 15*time.Minute)
	ui := web.New(web.Options{Store: st, Service: tenants, Branding: brand, Sessions: adminSessions, Limiter: limiter, Setup: setup, Secure: internalTLS != nil, Version: version, Log: log})
	guard := api.RequireAdmin(st, ui, limiter, "portico", log)
	adminAPI := guard(api.New(tenants, st, brand, setup, log))
	adminUI := guard(ui)
	return listener{"admin", newServer(cfg.Internal.AdminListen, proxy.NewAdmin(log, adminAPI, adminUI), log), internalTLS}, limiter, adminSessions, nil
}

// serveListeners binds and serves the listeners until ctx is done, one
// of them fails, or a restart is requested (ErrRestart). sweep runs
// every minute; wait runs after the shutdown, before returning, so a
// tenant operation in flight finishes its step before the store closes.
func serveListeners(ctx context.Context, listeners []listener, restart <-chan struct{}, sweep func(time.Time), wait func(), log *slog.Logger) error {
	errs := make(chan error, len(listeners))
	for _, l := range listeners {
		ln, err := net.Listen("tcp", l.srv.Addr)
		if err != nil {
			return fmt.Errorf("%s listener: %w", l.name, err)
		}
		log.Info("listening", "listener", l.name, "addr", ln.Addr().String(), "tls", l.tls != nil)
		go func(l listener, ln net.Listener) {
			var err error
			if l.tls != nil {
				l.srv.TLSConfig = l.tls
				err = l.srv.ServeTLS(ln, "", "")
			} else {
				err = l.srv.Serve(ln)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s listener: %w", l.name, err)
			}
		}(l, ln)
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			sweep(now)
		case err := <-errs:
			shutdown(listeners, log)
			wait()
			return err
		case <-restart:
			log.Info("restarting on request")
			shutdown(listeners, log)
			wait()
			return ErrRestart
		case <-ctx.Done():
			shutdown(listeners, log)
			// Let a running tenant operation finish its current step set
			// before the store closes; the operation timeout bounds it.
			wait()
			return nil
		}
	}
}

// cachedIdPLookup resolves the SAML identity provider's extId by name
// through the service account, caching a hit for ttl; errors are not
// cached, so an IdP created later is found at the next call.
func cachedIdPLookup(pc *nutanix.Client, name string, ttl time.Duration) func(ctx context.Context) (string, error) {
	var mu sync.Mutex
	var extID string
	var until time.Time
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if extID != "" && time.Now().Before(until) {
			return extID, nil
		}
		idps, err := pc.ListSAMLIdPs(ctx)
		if err != nil {
			return "", err
		}
		for _, i := range idps {
			if i.Name == name {
				extID, until = i.ExtID, time.Now().Add(ttl)
				return extID, nil
			}
		}
		return "", fmt.Errorf("SAML identity provider %q not found", name)
	}
}

func shutdown(listeners []listener, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, l := range listeners {
		if err := l.srv.Shutdown(ctx); err != nil {
			log.Warn("shutdown", "listener", l.name, "err", err.Error())
		}
	}
}
