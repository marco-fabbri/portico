package proxy

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/branding"
	"github.com/marco-fabbri/portico/internal/sites"
)

// SiteBackend is one site's Prism Central behind the public name (design
// sites §4.3): its upstream and what the landing page needs to start the
// SAML login on that site.
type SiteBackend struct {
	ID, Code, Name string
	Order          int
	Upstream       http.Handler
	PrismHTTP      *http.Client // to the site's Prism Central, no redirects
	PrismBase      *url.URL     // https://<pc>:9440
	IdPExtID       func(ctx context.Context) (string, error)
}

// SiteRouting decides, per request, which site's Prism Central a tenant
// session reaches: the session's chosen site, checked against the
// tenant's active placements every time (design sites §4.4).
type SiteRouting struct {
	Backends map[string]*SiteBackend
	// Placements returns the ids of the sites where the tenant has an
	// active placement.
	Placements func(ctx context.Context, tenantID string) ([]string, error)
	Status     func(siteID string) sites.Status
	SetSite    func(sessionID, siteID string, now time.Time) bool
	// LeaveSite takes a session off its site, keeping the Portico session
	// (the console's Sign Out with several sites).
	LeaveSite func(sessionID string, now time.Time) bool
	// Switch runs in the response that moves a session from one site to
	// another; NewPublic sets the cookie stash when it is nil.
	Switch func(w http.ResponseWriter, r *http.Request, from, to string)
	// StashMaxAge keeps a site's stashed cookies; the session's lifetime,
	// 8 h when zero.
	StashMaxAge time.Duration
	// SignOutAfter sends a site page left open to the full sign-out
	// (SitePageSignOut); none when zero.
	SignOutAfter time.Duration
	Pages        *branding.Pages
}

// SitePageSignOut is how long a site page stays before it signs out of
// everything: a minute less than the Portico session's idle limit, so the
// session (and its id token, which spares Keycloak's "Do you want to log
// out?") is still there; half the limit when that is two minutes or less.
// Without it a tenant with several sites whose console signed out for
// inactivity would keep its Portico and Keycloak sessions on a page that
// sends no requests (write-up 21: with one site that sign-out ends both).
func SitePageSignOut(idle time.Duration) time.Duration {
	if idle <= 2*time.Minute {
		return idle / 2
	}
	return idle - time.Minute
}

const (
	sitesPath  = "/.portico/sites"
	selectPath = "/.portico/sites/select"
)

// allowed returns the backends of the tenant's active placements in site
// order.
func (s *SiteRouting) allowed(ctx context.Context, tenant string) ([]*SiteBackend, error) {
	if tenant == "" {
		return nil, nil
	}
	ids, err := s.Placements(ctx, tenant)
	if err != nil {
		return nil, err
	}
	var out []*SiteBackend
	for _, id := range ids {
		if b, ok := s.Backends[id]; ok {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Code < out[j].Code
	})
	return out, nil
}

func find(bs []*SiteBackend, id string) *SiteBackend {
	for _, b := range bs {
		if b.ID == id {
			return b
		}
	}
	return nil
}

// page answers a browser navigation with a branded page, anything else
// with the JSON refusal.
func (s *SiteRouting) page(w http.ResponseWriter, r *http.Request, status int, rule, title, text, retry string) {
	setRule(r, rule)
	if s.Pages != nil && isNavigation(r) {
		w.Header().Set("X-Portico-Rule", rule)
		s.Pages.Error(w, r, status, title, text, retry)
		return
	}
	Refuse(w, status, rule, strings.ToLower(title[:1])+title[1:]+": "+text)
}

// isNavigation: a page load or a form submit of a browser (the site
// page's choice is a POST), told apart from scripts by the Accept header.
func isNavigation(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost:
		return strings.Contains(r.Header.Get("Accept"), "text/html")
	}
	return false
}

func (s *SiteRouting) noSite(w http.ResponseWriter, r *http.Request) {
	s.page(w, r, http.StatusForbidden, "no-site", "No site available", "No site is available for your account. Contact your provider.", "")
}

func (s *SiteRouting) unavailable(w http.ResponseWriter, r *http.Request, b *SiteBackend, st sites.Status) {
	text := b.Code + " is unreachable right now. Please try again in a few minutes."
	title := "Site unreachable"
	if st == sites.Maintenance {
		title, text = "Site in maintenance", b.Code+" is in maintenance. Please try again later."
	}
	s.page(w, r, http.StatusServiceUnavailable, "site-"+string(st), title, text, "/")
}

// route returns the backend this request goes to, or answers it. The
// session's site must be among the tenant's active placements; without
// one, a single placement is chosen when it is available, several send a
// browser to the site page and refuse anything else.
func (s *SiteRouting) route(w http.ResponseWriter, r *http.Request, sess auth.Session) (*SiteBackend, bool) {
	bs, err := s.allowed(r.Context(), sess.Tenant)
	if err != nil {
		s.page(w, r, http.StatusServiceUnavailable, "site-lookup", "Service unavailable", "Your sites could not be read. Please try again in a moment.", "/")
		return nil, false
	}
	if len(bs) == 0 {
		s.noSite(w, r)
		return nil, false
	}
	if b := find(bs, sess.Site); b != nil {
		return b, true
	}
	if len(bs) == 1 {
		b := bs[0]
		if st := s.Status(b.ID); st != sites.Available {
			s.unavailable(w, r, b, st)
			return nil, false
		}
		if !s.SetSite(sess.ID, b.ID, time.Now()) {
			Refuse(w, http.StatusUnauthorized, "session-gone", "portico session required")
			return nil, false
		}
		if sess.CookieSite != b.ID && s.Switch != nil {
			s.Switch(w, r, sess.CookieSite, b.ID)
		}
		return b, true
	}
	if isNavigation(r) {
		setRule(r, "site-page")
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, sitesPath, http.StatusSeeOther)
		return nil, false
	}
	setRule(r, "site-required")
	Refuse(w, http.StatusForbidden, "site-required", "choose a site first")
	return nil, false
}

// sitesPage shows the tenant's sites as tiles (design sites §2, variant B).
func (s *SiteRouting) sitesPage(w http.ResponseWriter, r *http.Request) {
	setRule(r, "site-page")
	sess, _ := auth.SessionFromContext(r.Context())
	bs, err := s.allowed(r.Context(), sess.Tenant)
	if err != nil {
		s.page(w, r, http.StatusServiceUnavailable, "site-lookup", "Service unavailable", "Your sites could not be read. Please try again in a moment.", sitesPath)
		return
	}
	switch len(bs) {
	case 0:
		s.noSite(w, r)
		return
	case 1:
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	tiles := make([]branding.SiteTile, 0, len(bs))
	for _, b := range bs {
		st := s.Status(b.ID)
		tiles = append(tiles, branding.SiteTile{ID: b.ID, Code: b.Code, Name: b.Name, Status: string(st), Selectable: st == sites.Available, Current: b.ID == sess.Site})
	}
	s.Pages.Sites(w, r, tiles, sess.Email, s.SignOutAfter)
}

// selectSite records the tenant's choice: only a site among its active
// placements, available, from a same-origin form.
func (s *SiteRouting) selectSite(w http.ResponseWriter, r *http.Request) {
	setRule(r, "site-select")
	if f := r.Header.Get("Sec-Fetch-Site"); f != "" && f != "same-origin" {
		Refuse(w, http.StatusForbidden, "site-select-cross-site", "the site choice must come from this site's page")
		return
	}
	sess, _ := auth.SessionFromContext(r.Context())
	id := strings.TrimSpace(r.PostFormValue("site"))
	bs, err := s.allowed(r.Context(), sess.Tenant)
	if err != nil {
		Refuse(w, http.StatusServiceUnavailable, "site-lookup", "sites could not be read")
		return
	}
	b := find(bs, id)
	if b == nil {
		s.page(w, r, http.StatusForbidden, "site-not-allowed", "Site not available", "This site is not available for your account.", sitesPath)
		return
	}
	if st := s.Status(b.ID); st != sites.Available {
		s.unavailable(w, r, b, st)
		return
	}
	if !s.SetSite(sess.ID, b.ID, time.Now()) {
		Refuse(w, http.StatusUnauthorized, "session-gone", "portico session required")
		return
	}
	if sess.CookieSite != b.ID && s.Switch != nil {
		s.Switch(w, r, sess.CookieSite, b.ID)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// multiple reports whether the session's tenant has more than one site,
// for the landing page's "switch site" link.
func (s *SiteRouting) multiple(ctx context.Context, tenant string) bool {
	bs, err := s.allowed(ctx, tenant)
	return err == nil && len(bs) > 1
}

// leaveSite is the console's Sign Out of a tenant with several sites
// (design sites usability 4.7): it ends that site only. The live Prism
// Central cookies are expired, the other sites' stashes and Portico's
// cookies are left alone, the session leaves its site and the browser
// goes to the site page, where Sign out ends everything.
func (s *SiteRouting) leaveSite(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	setRule(r, "sign-out-site")
	for _, c := range r.Cookies() {
		if isPorticoCookie(c.Name) {
			continue
		}
		if _, _, ok := parseStash(c.Name); ok {
			continue
		}
		http.SetCookie(w, &http.Cookie{Name: c.Name, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}
	if s.LeaveSite != nil && !s.LeaveSite(sess.ID, time.Now()) {
		Refuse(w, http.StatusUnauthorized, "session-gone", "portico session required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, sitesPath, http.StatusSeeOther)
}
