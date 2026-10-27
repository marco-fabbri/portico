package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig is Portico's client registration and where to find the
// identity provider.
type OIDCConfig struct {
	// Issuer is the public issuer URL, exactly as it appears in ID tokens.
	Issuer string
	// DiscoveryURL is the base URL Portico uses to reach the provider
	// (the loopback backchannel on the appliance). The discovery document
	// is fetched at DiscoveryURL + "/.well-known/openid-configuration".
	DiscoveryURL string
	ClientID     string
	ClientSecret string
	// RedirectURL is https://<prism host>/.portico/callback.
	RedirectURL string
	// Scopes defaults to openid and email.
	Scopes []string
	// ErrorPage, when set, renders a branded page for browser navigations
	// (Accept text/html) instead of the plain text answer.
	ErrorPage func(w http.ResponseWriter, r *http.Request, status int, title, text, retry string)
	// CookieMaxAge is the lifetime of the session cookie, matching the
	// store's absolute lifetime.
	CookieMaxAge time.Duration
	// ResolveTenant, when set, names the tenant of the user who just
	// logged in (by OIDC subject); the session keeps it. An error leaves
	// the session without a tenant: the routing then shows the "no site"
	// page instead of reaching any Prism Central.
	ResolveTenant func(ctx context.Context, subject string) (string, error)
}

// Provider is a discovered identity provider.
type Provider struct {
	*oidc.Provider
	EndSessionURL string
}

// Discover fetches the discovery document from cfg.DiscoveryURL, refuses a
// document whose issuer differs from cfg.Issuer, and builds the provider
// from the endpoints the document lists. With Keycloak's dynamic
// backchannel the authorization endpoint is public (for the browser) while
// token and JWKS endpoints are on the loopback (for Portico).
func Discover(ctx context.Context, client *http.Client, cfg OIDCConfig) (*Provider, error) {
	u := strings.TrimSuffix(cfg.DiscoveryURL, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc discovery: %s returned %d", u, resp.StatusCode)
	}
	var doc struct {
		oidc.ProviderConfig
		EndSession string `json:"end_session_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("oidc discovery: decode: %w", err)
	}
	if strings.TrimSuffix(doc.IssuerURL, "/") != strings.TrimSuffix(cfg.Issuer, "/") {
		return nil, fmt.Errorf("oidc discovery: document issuer %q does not match configured issuer %q", doc.IssuerURL, cfg.Issuer)
	}
	if doc.AuthURL == "" || doc.TokenURL == "" || doc.JWKSURL == "" {
		return nil, fmt.Errorf("oidc discovery: document lacks authorization, token or jwks endpoint")
	}
	p := doc.ProviderConfig.NewProvider(oidc.ClientContext(ctx, client))
	return &Provider{Provider: p, EndSessionURL: doc.EndSession}, nil
}

// Authenticator runs the OIDC authorization code flow and owns the
// resulting Portico sessions.
type Authenticator struct {
	cfg    OIDCConfig
	store  Store
	client *http.Client
	log    *slog.Logger

	mu       sync.Mutex
	provider *Provider
}

// NewAuthenticator returns an Authenticator; discovery happens on first use.
func NewAuthenticator(cfg OIDCConfig, st Store, client *http.Client, log *slog.Logger) *Authenticator {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "email"}
	}
	if cfg.CookieMaxAge <= 0 {
		cfg.CookieMaxAge = 8 * time.Hour
	}
	if client == nil {
		client = http.DefaultClient
	}
	if log == nil {
		log = slog.Default()
	}
	return &Authenticator{cfg: cfg, store: st, client: client, log: log}
}

// Provider returns the discovered provider, running discovery on the first
// call and caching success. A failure is returned and retried next time, so
// Portico keeps running when Keycloak starts later or goes down. Discovery
// runs outside the lock: while Keycloak hangs, concurrent logins wait for
// the timeout side by side, not one after the other.
func (a *Authenticator) Provider(ctx context.Context) (*Provider, error) {
	a.mu.Lock()
	p := a.provider
	a.mu.Unlock()
	if p != nil {
		return p, nil
	}
	p, err := Discover(ctx, a.client, a.cfg)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.provider == nil {
		a.provider = p
		a.log.Info("oidc discovery done", "issuer", a.cfg.Issuer)
	}
	return a.provider, nil
}

func (a *Authenticator) oauth2Config(p *Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     a.cfg.ClientID,
		ClientSecret: a.cfg.ClientSecret,
		RedirectURL:  a.cfg.RedirectURL,
		Endpoint:     p.Endpoint(),
		Scopes:       a.cfg.Scopes,
	}
}

// ctxWithClient makes oauth2 and go-oidc use Portico's HTTP client.
func (a *Authenticator) ctxWithClient(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, a.client)
}
