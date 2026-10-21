package branding

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
)

//go:embed templates/*.html
var pagesFS embed.FS

// Pages renders Portico's tenant-facing pages with the current branding:
// the landing page shown while the SAML login runs and the error pages a
// browser gets when the identity provider or Prism Central is down. No
// JavaScript: the landing page continues with a meta refresh.
type Pages struct {
	get  func(ctx context.Context) Branding
	tmpl *template.Template
	log  *slog.Logger
}

// NewPages returns a renderer reading the branding through get.
func NewPages(get func(ctx context.Context) Branding) *Pages {
	return &Pages{get: get, tmpl: template.Must(template.ParseFS(pagesFS, "templates/*.html")), log: slog.Default()}
}

type pageData struct {
	B Branding
	// Header is the top of the page, as on the login page (theme.go).
	Header      Header
	Mark        template.HTML // Portico's mark in the text colour, inline (CSP allows no other source)
	IconVersion string        // the icons' links carry it (Branding.IconVersion)
	Delay       int           // seconds of the meta refresh
	Next        string        // landing: where the refresh goes
	Status      int
	Title       string
	Text        string
	Retry       string
	SwitchURL   string     // landing: a "switch site" link when set
	Tiles       []SiteTile // site page
	Email       string     // site page: who is signed in
}

// SiteTile is one site on the site page (design sites §2, variant B).
type SiteTile struct {
	ID, Code, Name, Status string
	Selectable, Current    bool
}

func (p *Pages) write(w http.ResponseWriter, status int, name string, d pageData) {
	d.Header = d.B.Header()
	d.Mark = template.HTML(PorticoMark(d.B.TextColor)) // #nosec: a validated #rrggbb in a constant of this package
	d.IconVersion = d.B.IconVersion()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.WriteHeader(status)
	if err := p.tmpl.ExecuteTemplate(w, name, d); err != nil {
		p.log.Error("branding page", "page", name, "err", err.Error())
	}
}

// Landing renders the waiting page that sends the browser to next after
// the configured minimum display time (rounded down to whole seconds).
func (p *Pages) Landing(w http.ResponseWriter, r *http.Request, next string) {
	p.LandingSwitch(w, r, next, "")
}

// LandingSwitch is Landing with a "switch site" link to switchURL, for a
// tenant with several sites.
func (p *Pages) LandingSwitch(w http.ResponseWriter, r *http.Request, next, switchURL string) {
	b := p.get(r.Context())
	p.write(w, http.StatusOK, "landing.html", pageData{B: b, Delay: b.MinDisplayMs / 1000, Next: next, SwitchURL: switchURL})
}

// Sites renders the site page: one tile per site, the user's email and a
// sign-out link.
func (p *Pages) Sites(w http.ResponseWriter, r *http.Request, tiles []SiteTile, email string) {
	b := p.get(r.Context())
	p.write(w, http.StatusOK, "sites.html", pageData{B: b, Title: "Choose a site", Tiles: tiles, Email: email})
}

// Error renders a branded error page; retry, when not empty, is a link.
func (p *Pages) Error(w http.ResponseWriter, r *http.Request, status int, title, text, retry string) {
	b := p.get(r.Context())
	p.write(w, status, "error.html", pageData{B: b, Status: status, Title: title, Text: text, Retry: retry})
}
