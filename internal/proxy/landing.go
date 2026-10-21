package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"

	"github.com/marco-fabbri/portico/internal/branding"
)

// LandingOptions enables the branded landing page on the Prism Central
// host (design §4.3 step 5, §4.10): Portico answers the provider page and
// starts the SAML login itself, replaying the console's own
// provider-selection PUT with the browser's login-request cookie
// (docs/lab write-ups 12 and 13). Prism Central keeps deciding who logs
// in; Portico only saves the click.
type LandingOptions struct {
	Pages *branding.Pages
	Logo  func(ctx context.Context) (data []byte, ctype string, ok bool)
	// Icons are the tenant tab icons (branding.Service.Icons).
	Icons func(ctx context.Context) branding.Icons
	// PrismHTTP, PrismBase and IdPExtID are the request's site's: forSite
	// fills them from the SiteBackend.
	PrismHTTP *http.Client // to Prism Central, same TLS as the upstream, no redirects
	PrismBase *url.URL     // https://<pc>:9440
	PrismHost string       // public host name, sent as Host
	IdPExtID  func(ctx context.Context) (string, error)
	Log       *slog.Logger
	// SwitchURL, when set, puts a "switch site" link on the landing page.
	SwitchURL string
}

// forSite returns the landing options for one site's Prism Central; nil
// when the landing page is off.
func (l *LandingOptions) forSite(b *SiteBackend) *LandingOptions {
	if l == nil {
		return nil
	}
	c := *l
	c.PrismHTTP, c.PrismBase, c.IdPExtID = b.PrismHTTP, b.PrismBase, b.IdPExtID
	return &c
}

const (
	providersPath    = "/ui/iam/providers"
	manualMarker     = "manual"
	samlStartPath    = "/.portico/saml-start"
	logoPath         = "/.portico/logo"
	faviconPath      = "/.portico/favicon.ico"
	touchIconPath    = "/.portico/apple-touch-icon.png"
	initiateLogin    = "/api/iam/authn/initiate-login"
	loginRequestPath = "/api/iam/v4.1.b3/authn/login-requests/"
	iamCookie        = "iam-sessionid"
	// signOutPath is where the console's own Sign Out ends (write-up 12):
	// with the landing page that would sign the tenant straight back in.
	signOutPath   = "/redirect"
	porticoLogout = "/.portico/logout"
)

var reLoginRequestID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// interceptsProviders reports whether this request gets the landing page
// instead of Prism Central's provider page: a GET without the one-shot
// marker that the fallback sets.
func (l *LandingOptions) interceptsProviders(r *http.Request) bool {
	return l != nil && r.Method == http.MethodGet && NormalizePath(r.URL.Path) == providersPath && r.URL.Query().Get("portico") != manualMarker
}

// interceptsSignOut reports whether this is the tail of the console's
// Sign Out, which Portico turns into its own logout (Prism Central
// cookies expired, Keycloak session ended).
func (l *LandingOptions) interceptsSignOut(r *http.Request) bool {
	return l != nil && r.Method == http.MethodGet && NormalizePath(r.URL.Path) == signOutPath && r.URL.Query().Has("logout")
}

func (l *LandingOptions) signOut(w http.ResponseWriter, r *http.Request) {
	setRule(r, "sign-out")
	http.Redirect(w, r, porticoLogout, http.StatusFound)
}

func (l *LandingOptions) landing(w http.ResponseWriter, r *http.Request) {
	setRule(r, "landing")
	l.Pages.LandingSwitch(w, r, samlStartPath, l.SwitchURL)
}

// samlStart selects the SAML identity provider on the browser's login
// request and sends the browser to initiate-login. Any failure falls
// back once to the original provider page with the marker.
func (l *LandingOptions) samlStart(w http.ResponseWriter, r *http.Request) {
	setRule(r, "saml-start")
	fallback := func(step string, status int, err error) {
		attrs := []any{"step", step, "status", status}
		if err != nil {
			attrs = append(attrs, "err", err.Error())
		}
		l.Log.Warn("saml-start: falling back to the provider page", attrs...)
		http.Redirect(w, r, providersPath+"?portico="+manualMarker, http.StatusFound)
	}
	c, err := r.Cookie(iamCookie)
	if err != nil || !reLoginRequestID.MatchString(c.Value) {
		fallback("cookie", 0, nil)
		return
	}
	idp, err := l.IdPExtID(r.Context())
	if err != nil {
		fallback("idp", 0, err)
		return
	}
	var current struct {
		Data map[string]any `json:"data"`
	}
	status, err := l.loginRequest(r.Context(), http.MethodGet, c.Value, nil, &current)
	if err != nil || status != http.StatusOK || current.Data == nil {
		fallback("get", status, err)
		return
	}
	// The console's own body: nothing else, nothing read-only.
	body := map[string]any{
		"clientId":        current.Data["clientId"],
		"responseType":    current.Data["responseType"],
		"scope":           current.Data["scope"],
		"redirectUri":     current.Data["redirectUri"],
		"loginProviderId": idp,
	}
	status, err = l.loginRequest(r.Context(), http.MethodPut, c.Value, body, nil)
	if err != nil || status < 200 || status > 299 {
		fallback("put", status, err)
		return
	}
	http.Redirect(w, r, initiateLogin, http.StatusFound)
}

// loginRequest calls Prism Central's login-request endpoint with only the
// browser's iam-sessionid cookie: no Portico cookie, no API key.
func (l *LandingOptions) loginRequest(ctx context.Context, method, id string, body any, out any) (int, error) {
	u := *l.PrismBase
	u.Path = loginRequestPath + url.PathEscape(id)
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, err
	}
	req.Host = l.PrismHost
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", iamCookie+"="+id)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := l.PrismHTTP.Do(req)
	if err != nil {
		// A *url.Error embeds the request URL, which carries the
		// login-request id: report the class of the failure only.
		return 0, fmt.Errorf("prism central %s login-request: %s", method, errorClass(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// icon serves one of the tenant tab icons. The pages link them with the
// icons' version, so "no-cache" only costs a small download.
func (l *LandingOptions) icon(w http.ResponseWriter, r *http.Request) {
	setRule(r, "icon")
	if l.Icons == nil {
		Refuse(w, http.StatusNotFound, "icon", "no icons")
		return
	}
	ic := l.Icons(r.Context())
	data, ctype := ic.ICO, "image/x-icon"
	if NormalizePath(r.URL.Path) == touchIconPath {
		data, ctype = ic.Touch, "image/png"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// isIcon reports a GET of one of the tab icons.
func isIcon(r *http.Request) bool {
	p := NormalizePath(r.URL.Path)
	return r.Method == http.MethodGet && (p == faviconPath || p == touchIconPath)
}

// logo serves the branding logo to the landing and error pages with the
// headers that keep an SVG inert.
func (l *LandingOptions) logo(w http.ResponseWriter, r *http.Request) {
	setRule(r, "logo")
	data, ctype, ok := l.Logo(r.Context())
	if !ok {
		Refuse(w, http.StatusNotFound, "logo", "no logo")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
