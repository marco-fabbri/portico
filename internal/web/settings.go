package web

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/branding"
)

// maxBrandingForm bounds the multipart body: the logo limit plus the fields.
const maxBrandingForm = branding.MaxLogoBytes + 64<<10

type brandingData struct {
	B       branding.Branding
	HasLogo bool
}

func (h *Handler) renderBranding(w http.ResponseWriter, r *http.Request, status int, b branding.Branding, errMsg, warning string) {
	p := page{Title: "Branding", Version: h.version, Section: section(r.URL.Path), Error: errMsg, Warning: warning, Data: brandingData{B: b, HasLogo: b.HasLogo()}}
	if s, ok := h.session(r); ok {
		p.Admin, p.CSRF = "admin", s.CSRF
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.pages["settings"].ExecuteTemplate(w, "layout.html", p); err != nil {
		h.log.Error("template", "page", "settings", "err", err.Error())
	}
}

func (h *Handler) brandingPage(w http.ResponseWriter, r *http.Request) {
	h.renderBranding(w, r, http.StatusOK, h.brand.Get(r.Context()), "", "")
}

// brandingSave reads the multipart form: text fields, an optional logo
// file (replaces the current one), a "remove_logo" checkbox.
func (h *Handler) brandingSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBrandingForm)
	if err := r.ParseMultipartForm(maxBrandingForm); err != nil {
		h.renderBranding(w, r, http.StatusBadRequest, h.brand.Get(r.Context()), "the form could not be read (logo too large?)", "")
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	cur := h.brand.Get(r.Context())
	b := branding.Branding{
		MSPName:     strings.TrimSpace(r.FormValue("msp_name")),
		Tagline:     strings.TrimSpace(r.FormValue("tagline")),
		AccentColor: strings.TrimSpace(r.FormValue("accent_color")),
		LogoType:    cur.LogoType,
		LogoData:    cur.LogoData,
	}
	if v := strings.TrimSpace(r.FormValue("min_display_ms")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			h.renderBranding(w, r, http.StatusBadRequest, b, "min_display_ms must be a number", "")
			return
		}
		b.MinDisplayMs = n
	}
	if r.FormValue("remove_logo") != "" {
		b.LogoType, b.LogoData = "", nil
	}
	if f, hdr, err := r.FormFile("logo"); err == nil {
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, branding.MaxLogoBytes+1))
		if err != nil {
			h.renderBranding(w, r, http.StatusBadRequest, b, "logo could not be read", "")
			return
		}
		b.LogoType, b.LogoData = hdr.Header.Get("Content-Type"), data
	}
	err := h.brand.Set(r.Context(), b)
	var ae *branding.ApplyError
	switch {
	case errors.As(err, &ae):
		h.renderBranding(w, r, http.StatusOK, h.brand.Get(r.Context()), "", "Saved. Applying the branding to Keycloak failed: "+ae.Err.Error())
	case err != nil:
		h.renderBranding(w, r, http.StatusBadRequest, b, err.Error(), "")
	default:
		http.Redirect(w, r, "/settings/branding", http.StatusSeeOther)
	}
}

func (h *Handler) brandingLogo(w http.ResponseWriter, r *http.Request) {
	api.ServeLogo(w, h.brand.Get(r.Context()))
}
