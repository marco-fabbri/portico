package branding

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
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
	// The lockup of the login page (theme.go): the name in two parts, the
	// last word bold with no space before it (PORTICO**MSP**).
	NameHead, NameTail string
	Mark               template.HTML // Portico's mark in white, inline (CSP allows no other source)
	Delay              int           // seconds of the meta refresh
	Next               string        // landing: where the refresh goes
	Status             int
	Title              string
	Text               string
	Retry              string
	HasLogo            bool
}

// lockup splits the MSP name as the login page does.
func lockup(d *pageData) {
	name := strings.TrimSpace(d.B.MSPName)
	d.NameHead = name
	if i := strings.LastIndex(name, " "); i > 0 {
		d.NameHead, d.NameTail = name[:i], strings.TrimSpace(name[i+1:])
	}
	d.Mark = template.HTML(PorticoMark("#ffffff")) // #nosec: a constant of this package
}

func (p *Pages) write(w http.ResponseWriter, status int, name string, d pageData) {
	lockup(&d)
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
	b := p.get(r.Context())
	p.write(w, http.StatusOK, "landing.html", pageData{B: b, Delay: b.MinDisplayMs / 1000, Next: next, HasLogo: b.HasLogo()})
}

// Error renders a branded error page; retry, when not empty, is a link.
func (p *Pages) Error(w http.ResponseWriter, r *http.Request, status int, title, text, retry string) {
	b := p.get(r.Context())
	p.write(w, status, "error.html", pageData{B: b, Status: status, Title: title, Text: text, Retry: retry, HasLogo: b.HasLogo()})
}
