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
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/config"
	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/proxy"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
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

type listener struct {
	name string
	srv  *http.Server
	tls  *tls.Config
}

// runServe builds the three listeners from cfg and runs them until ctx is
// done, then shuts them down with a 10 s grace period.
func runServe(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	secret, err := cfg.ClientSecret()
	if err != nil {
		return err
	}
	apiKey, err := cfg.PrismAPIKey()
	if err != nil {
		return err
	}
	kcSecret, err := cfg.KeycloakAdminSecret()
	if err != nil {
		return err
	}
	st, err := store.OpenSQLite(cfg.Store.Path)
	if err != nil {
		return err
	}
	defer st.Close()
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
		pcTLS, err = proxy.PrismTLS(pcURL.Hostname(), cfg.PrismCentral.CAFile, cfg.PrismCentral.TLSVerify)
		if err != nil {
			return err
		}
	}

	store := auth.NewMemoryStore(cfg.Session.MaxAge, cfg.Session.Idle)
	authn := auth.NewAuthenticator(auth.OIDCConfig{
		Issuer: cfg.OIDC.Issuer,
		// The issuer's path is the realm path; the loopback base plus that
		// path is the same realm on the backchannel.
		DiscoveryURL: strings.TrimSuffix(cfg.Keycloak.URL, "/") + issuer.Path,
		ClientID:     cfg.OIDC.ClientID,
		ClientSecret: secret,
		RedirectURL:  "https://" + cfg.Public.PrismHost + "/.portico/callback",
		CookieMaxAge: cfg.Session.MaxAge,
	}, store, &http.Client{Timeout: 15 * time.Second}, log)

	stripped := []string{auth.SessionCookie, auth.OIDCCookie}
	prismUp := proxy.NewUpstream(proxy.UpstreamOptions{Target: pcURL, HostHeader: cfg.Public.PrismHost, TLS: pcTLS, StripCookies: stripped, Log: log})
	kcUp := proxy.NewUpstream(proxy.UpstreamOptions{Target: kcURL, HostHeader: cfg.Public.IDHost, StripCookies: stripped, Log: log})
	public := proxy.NewPublic(proxy.PublicOptions{
		PrismHost: cfg.Public.PrismHost, IDHost: cfg.Public.IDHost,
		Prism: prismUp, Keycloak: kcUp, Auth: authn,
		Policy: proxy.PrismPolicy{AllowPrefixes: cfg.Policy.AllowPrefixes}, Log: log,
	})
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
	adminAPI := api.BasicAuth(st, "portico", log)(api.New(tenants, st, log))

	publicTLS, err := serverTLS(cfg.Public.TLS)
	if err != nil {
		return fmt.Errorf("public: %w", err)
	}
	internalTLS, err := serverTLS(cfg.Internal.TLS)
	if err != nil {
		return fmt.Errorf("internal: %w", err)
	}
	newServer := func(addr string, h http.Handler) *http.Server {
		return &http.Server{
			Addr:              addr,
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		}
	}
	listeners := []listener{
		{"public", newServer(cfg.Public.Listen, public), publicTLS},
		{"passthrough", newServer(cfg.Internal.PassthroughListen, passthrough), internalTLS},
		{"admin", newServer(cfg.Internal.AdminListen, proxy.NewAdmin(log, adminAPI)), internalTLS},
	}

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

	sweep := time.NewTicker(time.Minute)
	defer sweep.Stop()
	for {
		select {
		case <-sweep.C:
			if n := store.Sweep(time.Now()); n > 0 {
				log.Debug("sessions swept", "count", n)
			}
		case err := <-errs:
			shutdown(listeners, log)
			tenants.Wait()
			return err
		case <-ctx.Done():
			shutdown(listeners, log)
			// Let a running tenant operation finish its current step set
			// before the store closes; the operation timeout bounds it.
			tenants.Wait()
			return nil
		}
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
