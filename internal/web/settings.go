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

// maxBrandingForm bounds the multipart body: the logo and icon limits
// plus the fields.
const maxBrandingForm = 2*branding.MaxLogoBytes + 64<<10

type brandingData struct {
	B                branding.Branding
	HasLogo, HasIcon bool
	MinIcon, MaxIcon int
}

func (h *Handler) renderBranding(w http.ResponseWriter, r *http.Request, status int, b branding.Branding, errMsg, warning string) {
	p := page{Title: "Branding", Version: h.version, Section: section(r.URL.Path), Error: errMsg, Warning: warning, Data: brandingData{B: b, HasLogo: b.HasLogo(), HasIcon: b.HasIcon(), MinIcon: branding.MinIconSide, MaxIcon: branding.MaxIconSide}}
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
// and an optional tab icon (each replaces the current one), and the
// "remove_logo" and "remove_icon" checkboxes.
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
		IconData:    cur.IconData,
		// Bridge until the form carries them (plan Task 6).
		HeaderText:      cur.HeaderText,
		BackgroundColor: cur.BackgroundColor,
		TextColor:       cur.TextColor,
		PorticoLogo:     cur.PorticoLogo,
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
		b.LogoType, b.LogoData, b.PorticoLogo = "", nil, false
	}
	if f, hdr, err := r.FormFile("logo"); err == nil {
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, branding.MaxLogoBytes+1))
		if err != nil {
			h.renderBranding(w, r, http.StatusBadRequest, b, "logo could not be read", "")
			return
		}
		b.LogoType, b.LogoData, b.PorticoLogo = hdr.Header.Get("Content-Type"), data, false
	}
	if r.FormValue("remove_icon") != "" {
		b.IconData = nil
	}
	if f, _, err := r.FormFile("icon"); err == nil {
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, branding.MaxLogoBytes+1))
		if err != nil {
			h.renderBranding(w, r, http.StatusBadRequest, b, "icon could not be read", "")
			return
		}
		b.IconData = data
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

func (h *Handler) brandingIcon(w http.ResponseWriter, r *http.Request) {
	api.ServeIcon(w, h.brand.Get(r.Context()))
}
