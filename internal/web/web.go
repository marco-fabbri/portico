// Package web is Portico's administration UI on the internal listener:
// server-rendered pages with HTMX, embedded in the binary, calling the
// same service layer as the REST API (ADR 0003). Authentication is the
// break-glass account with a session cookie; every state-changing form
// carries a per-session CSRF token.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/auth"
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
	Sessions *auth.MemoryStore // admin sessions: 8 h absolute, 15 min idle
	Limiter  *api.Limiter      // shared with the API's Basic check
	Secure   bool              // the listener has TLS: Secure cookie
	Version  string
	Log      *slog.Logger
}

// Handler serves the UI. It implements api.SessionChecker so the same
// middleware accepts its session on every path.
type Handler struct {
	st       store.Store
	svc      *tenant.Service
	sessions *auth.MemoryStore
	limiter  *api.Limiter
	secure   bool
	version  string
	log      *slog.Logger
	mux      *http.ServeMux
	pages    map[string]*template.Template
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
	h := &Handler{st: o.Store, svc: o.Service, sessions: o.Sessions, limiter: o.Limiter, secure: o.Secure, version: o.Version, log: o.Log}
	h.pages = parsePages()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", h.loginPage)
	mux.HandleFunc("POST /login", h.loginSubmit)
	mux.HandleFunc("POST /logout", h.logout)
	mux.Handle("GET /static/", http.StripPrefix("/static/", h.static()))
	mux.HandleFunc("GET /{$}", h.home)
	h.routes(mux)
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
	h.mux.ServeHTTP(w, r)
}

// parsePages builds one template set per page on top of the layout.
func parsePages() map[string]*template.Template {
	layout := template.Must(template.ParseFS(templatesFS, "templates/layout.html"))
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
	return pages
}

// page is what every template receives.
type page struct {
	Title   string
	Admin   string
	CSRF    string
	Version string
	Error   string
	Notice  string
	Data    any
}

// render writes a page. The admin name and CSRF token come from the
// session when there is one.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, name, title string, data any, errMsg string) {
	p := page{Title: title, Version: h.version, Error: errMsg, Data: data}
	if s, ok := h.session(r); ok {
		p.Admin = "admin"
		p.CSRF = s.CSRF
	}
	if n := r.URL.Query().Get("notice"); n != "" && status == http.StatusOK {
		p.Notice = n
	}
	t, ok := h.pages[name]
	if !ok {
		http.Error(w, "page not found: "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "layout.html", p); err != nil {
		h.log.Error("template", "page", name, "err", err.Error())
	}
}

// static serves the embedded assets with a day of caching; traversal
// cannot escape the embedded directory.
func (h *Handler) static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
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

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "home", "Tenants", nil, "")
}
