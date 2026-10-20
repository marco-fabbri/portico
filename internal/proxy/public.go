package proxy

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/auth"
)

const sessionCookieName = auth.SessionCookie

// PublicOptions wires the public listener.
type PublicOptions struct {
	PrismHost string
	IDHost    string
	// Sites routes every tenant request on the Prism Central host to the
	// Prism Central of the session's site (design sites §4.3).
	Sites    *SiteRouting
	Keycloak http.Handler // upstream to Keycloak
	Auth     *auth.Authenticator
	Policy   PrismPolicy
	Log      *slog.Logger
	// Landing enables the branded landing page and the SAML start on the
	// Prism Central host; nil keeps the Milestone 1 behaviour. Its
	// Prism Central fields come from the request's site.
	Landing *LandingOptions
}

// Refuse writes a JSON error with the rule name in X-Portico-Rule so the
// probe script and the access log can show why.
func Refuse(w http.ResponseWriter, status int, rule, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Portico-Rule", rule)
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// NewPublic routes by Host: the Prism Central host runs deny list →
// session → allow-list → proxy; the identity host runs its allow-list and
// proxies without a session; any other host gets 421.
func NewPublic(o PublicOptions) http.Handler {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	prismHost := strings.ToLower(o.PrismHost)
	idHost := strings.ToLower(o.IDHost)

	portico := http.NewServeMux()
	portico.Handle("GET /.portico/login", o.Auth.Login())
	portico.Handle("GET /.portico/callback", o.Auth.Callback())
	portico.Handle("GET /.portico/logout", o.Auth.Logout())
	if o.Sites.Switch == nil {
		maxAge := o.Sites.StashMaxAge
		if maxAge <= 0 {
			maxAge = 8 * time.Hour
		}
		o.Sites.Switch = func(w http.ResponseWriter, r *http.Request, from, to string) {
			switchSiteCookies(w, r, from, to, maxAge)
		}
	}
	portico.Handle("GET "+sitesPath, o.Auth.Require(http.HandlerFunc(o.Sites.sitesPage)))
	portico.Handle("POST "+selectPath, o.Auth.Require(http.HandlerFunc(o.Sites.selectSite)))
	if o.Landing != nil {
		if o.Landing.Log == nil {
			o.Landing.Log = log
		}
		portico.Handle("GET "+samlStartPath, o.Auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess, _ := auth.SessionFromContext(r.Context())
			b, ok := o.Sites.route(w, r, sess)
			if !ok {
				return
			}
			o.Landing.forSite(b).samlStart(w, r)
		})))
		// The logo is a public asset (it is on the Keycloak login page too):
		// the error pages need it on both hosts and before any session.
		portico.Handle("GET "+logoPath, http.HandlerFunc(o.Landing.logo))
	}
	portico.HandleFunc("/.portico/", func(w http.ResponseWriter, r *http.Request) {
		setRule(r, "portico-unknown")
		Refuse(w, http.StatusNotFound, "portico-unknown", "not found")
	})

	// After the session check: the session's site (design sites §4.3),
	// the landing page in place of that Prism Central's provider page
	// (Milestone 2c), then allow-list, then proxy.
	prismAllowed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, _ := auth.SessionFromContext(r.Context())
		b, ok := o.Sites.route(w, r, sess)
		if !ok {
			return
		}
		l := o.Landing.forSite(b)
		if l.interceptsProviders(r) {
			if o.Sites.multiple(r.Context(), sess.Tenant) {
				l.SwitchURL = sitesPath
			}
			l.landing(w, r)
			return
		}
		if l.interceptsSignOut(r) {
			l.signOut(w, r)
			return
		}
		if rule, ok := o.Policy.Allowed(r); !ok {
			setRule(r, rule)
			Refuse(w, http.StatusForbidden, rule, "path not allowed on the public listener")
			return
		}
		b.Upstream.ServeHTTP(w, r)
	})
	prismWithSession := o.Auth.Require(prismAllowed)

	prism := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(strings.ToLower(r.URL.Path), "/.portico/") {
			portico.ServeHTTP(w, r)
			return
		}
		if rule, denied := o.Policy.Deny(r); denied {
			setRule(r, rule)
			Refuse(w, http.StatusForbidden, rule, "refused on the public listener")
			return
		}
		prismWithSession.ServeHTTP(w, r)
	})

	id := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.Landing != nil && r.Method == http.MethodGet && NormalizePath(r.URL.Path) == logoPath {
			o.Landing.logo(w, r)
			return
		}
		if rule, ok := (IDPolicy{}).Check(r); !ok {
			setRule(r, rule)
			Refuse(w, http.StatusForbidden, rule, "path not allowed on the public listener")
			return
		}
		o.Keycloak.ServeHTTP(w, r)
	})

	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch hostOnly(r.Host) {
		case prismHost:
			prism.ServeHTTP(w, r)
		case idHost:
			id.ServeHTTP(w, r)
		default:
			setRule(r, "unknown-host")
			Refuse(w, http.StatusMisdirectedRequest, "unknown-host", "unknown host")
		}
	})
	return AccessLog(log, router)
}

// hostOnly lower-cases the host and drops a port.
func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}
