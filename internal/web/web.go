// Package web is Portico's administration UI on the internal listener:
// server-rendered pages with HTMX, embedded in the binary, calling the
// same service layer as the REST API (ADR 0003). Authentication is the
// break-glass account with a session cookie; every state-changing form
// carries a per-session CSRF token.
package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/auth"
	"github.com/marco-fabbri/portico/internal/branding"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Options configures the UI handler.
type Options struct {
	Store    store.Store
	Service  *tenant.Service
	Branding *branding.Service
	Sessions *auth.MemoryStore    // admin sessions: 8 h absolute, 15 min idle
	Limiter  *api.Limiter         // shared with the API's Basic check
	Setup    *api.SettingsOptions // settings document, checks and restart; nil disables /setup
	Exposure ExposureOptions      // the exposure requests; zero hides the form
	Secure   bool                 // the listener has TLS: Secure cookie
	Version  string
	Log      *slog.Logger
}

// Handler serves the UI. It implements api.SessionChecker so the same
// middleware accepts its session on every path.
type Handler struct {
	st       store.Store
	svc      *tenant.Service
	brand    *branding.Service
	sessions *auth.MemoryStore
	limiter  *api.Limiter
	setup    *api.SettingsOptions
	exposure ExposureOptions
	secure   bool
	version  string
	log      *slog.Logger
	mux      *http.ServeMux
	pages    map[string]*template.Template
	icons    branding.Icons // the admin UI's tab icons, drawn once
}

// New builds the handler and parses the templates once.
func New(o Options) *Handler {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Sessions == nil {
		o.Sessions = auth.NewMemoryStore(8*time.Hour, 15*time.Minute)
	}
	if o.Limiter == nil {
		o.Limiter = api.NewLimiter()
	}
	if o.Branding == nil {
		o.Branding = branding.NewService(o.Store, o.Log)
	}
	h := &Handler{st: o.Store, svc: o.Service, brand: o.Branding, sessions: o.Sessions, limiter: o.Limiter, setup: o.Setup, exposure: o.Exposure, secure: o.Secure, version: o.Version, log: o.Log}
	h.pages = parsePages()
	h.icons = branding.AdminIcons()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", h.loginPage)
	mux.HandleFunc("POST /login", h.loginSubmit)
	mux.HandleFunc("POST /logout", h.logout)
	mux.HandleFunc("GET /password", h.passwordPage)
	mux.HandleFunc("POST /password", h.passwordSubmit)
	mux.Handle("GET /static/", http.StripPrefix("/static/", h.static()))
	mux.HandleFunc("GET /static/favicon.ico", h.icon)
	mux.HandleFunc("GET /static/apple-touch-icon.png", h.icon)
	mux.HandleFunc("GET /{$}", h.home)
	h.routes(mux)
	h.connectionRoutes(mux)
	h.setupRoutes(mux)
	h.exposureRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h.render(w, r, http.StatusNotFound, "notfound", "Not found", nil, "")
	})
	h.mux = mux
	return h
}

// ServeHTTP dispatches to the pages.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'")
	// The factory password opens one page only: the one that replaces it.
	if r.URL.Path != "/password" && r.URL.Path != "/logout" && !strings.HasPrefix(r.URL.Path, "/static/") {
		if _, ok := h.session(r); ok && api.PasswordChangeRequired(r.Context(), h.st) {
			http.Redirect(w, r, "/password", http.StatusSeeOther)
			return
		}
	}
	h.mux.ServeHTTP(w, r)
}

// parsePages builds one template set per page on top of the layout.
func parsePages() map[string]*template.Template {
	layout := template.Must(template.New("layout.html").ParseFS(templatesFS, "templates/layout.html"))
	pages := map[string]*template.Template{}
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".html")
		if name == "layout" {
			continue
		}
		t := template.Must(layout.Clone())
		pages[name] = template.Must(t.ParseFS(templatesFS, "templates/"+e.Name()))
	}
	// A minimal not-found page shares the layout.
	nf := template.Must(layout.Clone())
	pages["notfound"] = template.Must(nf.Parse(`{{define "content"}}<section class="card"><h1>Not found</h1><p><a href="/">Back to the tenants</a></p></section>{{end}}`))
	sm := template.Must(layout.Clone())
	pages["setupmode"] = template.Must(sm.Parse(`{{define "content"}}<section class="card"><h1>Setup mode</h1><p>` + api.SetupModeMessage + `. <a href="/setup">Open the setup page</a></p></section>{{end}}`))
	return pages
}

// noticeCookie carries a notice ("Saved.", "Certificate pinned: …") from
// the handler that did the work to the page the browser is sent to; set
// by the server only and shown once. A notice is never read from the URL.
const noticeCookie = "portico_admin_notice"

// page is what every template receives.
type page struct {
	Title   string
	Admin   string
	CSRF    string
	Version string
	Section string // which header tab is current: tenants, branding, setup
	Error   string
	Warning string
	Notice  string
	Data    any
}

// render writes a page. The admin name and CSRF token come from the
// session when there is one.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, name, title string, data any, errMsg string) {
	p := page{Title: title, Version: h.version, Section: section(r.URL.Path), Error: errMsg, Data: data}
	if s, ok := h.session(r); ok {
		p.Admin = "admin"
		p.CSRF = s.CSRF
	}
	// Consumed by the first page rendered, whatever its status: a notice
	// left behind a 404 would otherwise reappear on the next page.
	if c, err := r.Cookie(noticeCookie); err == nil && c.Value != "" {
		if n, err := url.QueryUnescape(c.Value); err == nil {
			p.Notice = n
		}
		http.SetCookie(w, &http.Cookie{Name: noticeCookie, Value: "", Path: "/", MaxAge: -1, Secure: h.secure, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
	h.writeTemplate(w, status, name, "layout.html", p)
}

// writeTemplate writes one block of a page template, a whole page
// ("layout.html") or a fragment. Rendered into a buffer: a template that
// fails half-way would otherwise leave a 200 and a torn page.
func (h *Handler) writeTemplate(w http.ResponseWriter, status int, pageName, block string, data any) {
	t, ok := h.pages[pageName]
	if !ok {
		http.Error(w, "page not found: "+pageName, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		h.log.Error("template", "page", pageName, "block", block, "err", err.Error())
		http.Error(w, "the page could not be rendered", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// notice sets the one-shot notice the next page shows, then the caller
// redirects. 60 s is enough for the redirect and short enough to vanish
// if the browser never follows it.
func (h *Handler) notice(w http.ResponseWriter, text string) {
	http.SetCookie(w, &http.Cookie{Name: noticeCookie, Value: url.QueryEscape(text), Path: "/", MaxAge: 60, Secure: h.secure, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// section names the header tab a path belongs to.
func section(p string) string {
	switch {
	case p == "/" || strings.HasPrefix(p, "/tenants"):
		return "tenants"
	case strings.HasPrefix(p, "/settings"):
		return "branding"
	case strings.HasPrefix(p, "/setup"):
		return "setup"
	}
	return ""
}

// static serves the embedded assets with a day of caching; traversal
// cannot escape the embedded directory.
// icon serves the admin UI's tab icons; the layout links them with the
// binary's version, like the other static files.
func (h *Handler) icon(w http.ResponseWriter, r *http.Request) {
	data, ctype := h.icons.ICO, "image/x-icon"
	if strings.HasSuffix(r.URL.Path, ".png") {
		data, ctype = h.icons.Touch, "image/png"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

func (h *Handler) static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	// The font files: Go's built-in table has no woff2 and the appliance
	// may lack /etc/mime.types.
	mime.AddExtensionType(".woff2", "font/woff2")
	files := http.FS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean("/" + r.URL.Path)
		f, err := files.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeContent(w, r, name, info.ModTime(), f)
	})
}
