package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/proxy"
)

// Checker runs connectivity checks with candidate settings, storing
// nothing. The setup pages call it before a save; the messages are for
// the administrator and never carry a secret.
type Checker struct {
	log *slog.Logger
}

// NewChecker returns a checker.
func NewChecker(log *slog.Logger) *Checker {
	if log == nil {
		log = slog.Default()
	}
	return &Checker{log: log}
}

// CheckResult is one check's outcome.
type CheckResult struct {
	OK         bool     `json:"ok"`
	Message    string   `json:"message"`
	Clusters   []Choice `json:"clusters,omitempty"`
	Containers []Choice `json:"containers,omitempty"`
}

// Choice is a selectable Prism Central object.
type Choice struct {
	ExtID string `json:"ext_id"`
	Name  string `json:"name"`
}

func fail(format string, args ...any) CheckResult {
	return CheckResult{OK: false, Message: fmt.Sprintf(format, args...)}
}

// PrismCentral checks the URL, the TLS settings and the API key by
// listing clusters and storage containers; the lists fill the pickers.
func (c *Checker) PrismCentral(ctx context.Context, s Settings, apiKey string) CheckResult {
	if apiKey == "" {
		return fail("the API key is not set")
	}
	u, err := url.Parse(s.PrismCentral.URL)
	if err != nil || u.Host == "" {
		return fail("the Prism Central URL %q is not valid", s.PrismCentral.URL)
	}
	if u.User != nil {
		// Not echoed: the userinfo is a password in clear.
		return fail("the Prism Central URL must not carry credentials")
	}
	tlsCfg, err := proxy.PrismTLSPEM(u.Hostname(), []byte(s.PrismCentral.CAPEM), s.PrismCentral.TLSVerify)
	if err != nil {
		return fail("the CA certificate is not usable: %v", err)
	}
	client, err := nutanix.New(s.PrismCentral.URL, apiKey, tlsCfg, c.log)
	if err != nil {
		return fail("%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	clusters, err := client.ListClusters(ctx)
	if err != nil {
		return fail("%s", prismError(s.PrismCentral.URL, err))
	}
	containers, err := client.ListStorageContainers(ctx)
	if err != nil {
		return fail("%s", prismError(s.PrismCentral.URL, err))
	}
	var res CheckResult
	for _, cl := range clusters {
		if cl.IsAOS {
			res.Clusters = append(res.Clusters, Choice{ExtID: cl.ExtID, Name: cl.Name})
		}
	}
	for _, sc := range containers {
		if sc.IsSystem {
			continue
		}
		res.Containers = append(res.Containers, Choice{ExtID: sc.ExtID, Name: sc.Name})
	}
	res.OK = true
	res.Message = fmt.Sprintf("Prism Central answered: %d cluster(s), %d storage container(s)", len(res.Clusters), len(res.Containers))
	return res
}

// prismError turns a client error into an administrator message without
// echoing credentials.
func prismError(target string, err error) string {
	var apiErr *nutanix.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "Prism Central refused the API key (" + fmt.Sprint(apiErr.Status) + "): check the service account and its key"
		default:
			return fmt.Sprintf("Prism Central answered %d: %s", apiErr.Status, apiErr.Message)
		}
	}
	return "Prism Central at " + target + " did not answer: " + errClass(err)
}

func errClass(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "certificate"):
		return "TLS certificate problem (check tls_verify or the CA)"
	case strings.Contains(msg, "refused"):
		return "connection refused"
	case strings.Contains(msg, "no such host"):
		return "name not found"
	case strings.Contains(msg, "deadline") || strings.Contains(msg, "timeout"):
		return "timeout"
	default:
		return "connection failed"
	}
}

// Keycloak checks the admin client credentials by fetching the realm.
func (c *Checker) Keycloak(ctx context.Context, keycloakURL string, s Settings, adminSecret string) CheckResult {
	if adminSecret == "" {
		return fail("the admin client secret is not set")
	}
	client := keycloak.New(keycloakURL, s.Keycloak.Realm, s.Keycloak.AdminClientID, adminSecret, &http.Client{Timeout: 15 * time.Second}, c.log)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rep, err := client.Realm(ctx)
	if err != nil {
		var se *keycloak.StatusError
		switch {
		case errors.As(err, &se) && se.Op == "token" && se.Status == http.StatusNotFound:
			return fail("realm %q not found in Keycloak", s.Keycloak.Realm)
		case errors.As(err, &se) && se.Op == "token" && se.Status == http.StatusUnauthorized:
			return fail("Keycloak refused the admin client secret for %q in realm %q", s.Keycloak.AdminClientID, s.Keycloak.Realm)
		case errors.As(err, &se) && se.Op == "token":
			return fail("Keycloak answered status %d at the token endpoint", se.Status)
		case errors.Is(err, keycloak.ErrNotFound):
			return fail("realm %q not found in Keycloak", s.Keycloak.Realm)
		case errors.As(err, &se):
			return fail("Keycloak answered an error: %s", strings.TrimPrefix(err.Error(), "keycloak: "))
		default:
			return fail("Keycloak did not answer: %s", errClass(err))
		}
	}
	name, _ := rep["realm"].(string)
	return CheckResult{OK: true, Message: fmt.Sprintf("Keycloak answered: realm %q reachable with the admin client", name)}
}

// OIDC fetches the discovery document on the backchannel and checks the
// issuer matches the configured one.
func (c *Checker) OIDC(ctx context.Context, keycloakURL string, s Settings) CheckResult {
	issuer, err := url.Parse(s.OIDC.Issuer)
	if err != nil || issuer.Host == "" {
		return fail("the issuer %q is not a valid URL", s.OIDC.Issuer)
	}
	disc := strings.TrimSuffix(keycloakURL, "/") + issuer.Path + "/.well-known/openid-configuration"
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, disc, nil)
	if err != nil {
		return fail("%v", err)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fail("the identity provider did not answer on the backchannel: %s", errClass(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail("discovery answered %d at %s", resp.StatusCode, disc)
	}
	var doc struct {
		Issuer string `json:"issuer"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return fail("discovery document unreadable: %v", err)
	}
	if doc.Issuer != s.OIDC.Issuer {
		return fail("the identity provider's issuer is %q, the settings say %q", doc.Issuer, s.OIDC.Issuer)
	}
	return CheckResult{OK: true, Message: "OIDC discovery answered with the expected issuer"}
}
