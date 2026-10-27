package web

import (
	"errors"
	"html/template"
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
	// LogoSource is the logo radio's value: portico, file or none.
	LogoSource string
	// SavedLogo: an uploaded logo is saved, the preview may show it.
	SavedLogo bool
	Arch      template.HTML // Portico's arch in currentColor, for the preview
}

func logoSource(b branding.Branding) string {
	switch {
	case b.PorticoLogo:
		return "portico"
	case b.HasLogo():
		return "file"
	}
	return "none"
}

func (h *Handler) renderBranding(w http.ResponseWriter, r *http.Request, status int, b branding.Branding, errMsg, warning string) {
	data := brandingData{B: b, HasLogo: b.HasLogo(), HasIcon: b.HasIcon(), MinIcon: branding.MinIconSide, MaxIcon: branding.MaxIconSide,
		LogoSource: logoSource(b), SavedLogo: h.brand.Get(r.Context()).HasLogo(),
		Arch: template.HTML(branding.PorticoMark("currentColor"))} // #nosec: a constant of the branding package
	p := page{Title: "Branding", Version: h.version, Section: section(r.URL.Path), Error: errMsg, Warning: warning, Data: data}
	if s, ok := h.session(r); ok {
		p.Admin, p.CSRF = "admin", s.CSRF
	}
	h.writeTemplate(w, status, "settings", "layout.html", p)
}

func (h *Handler) brandingPage(w http.ResponseWriter, r *http.Request) {
	h.renderBranding(w, r, http.StatusOK, h.brand.Get(r.Context()), "", "")
}

// brandingSave reads the multipart form: text and colour fields, the
// logo source (Portico's arch, the uploaded file, none), an optional logo
// and an optional tab icon (each replaces the current one), and the
// "remove_icon" checkbox. A field missing from the form keeps its saved
// value. A save that stands with warnings (readability, Keycloak) shows
// them on the page instead of redirecting.
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
	b := cur
	b.MSPName = strings.TrimSpace(r.FormValue("msp_name"))
	b.Tagline = strings.TrimSpace(r.FormValue("tagline"))
	b.AccentColor = strings.TrimSpace(r.FormValue("accent_color"))
	for field, dst := range map[string]*string{"header_text": &b.HeaderText, "background_color": &b.BackgroundColor, "text_color": &b.TextColor} {
		if v, ok := r.MultipartForm.Value[field]; ok && len(v) > 0 {
			*dst = strings.TrimSpace(v[0])
		}
	}
	switch r.FormValue("logo_source") {
	case "portico":
		b.PorticoLogo, b.LogoType, b.LogoData = true, "", nil
	case "none":
		b.PorticoLogo, b.LogoType, b.LogoData = false, "", nil
	case "file":
		b.PorticoLogo = false
	}
	b.MinDisplayMs = 0
	if v := strings.TrimSpace(r.FormValue("min_display_ms")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			h.renderBranding(w, r, http.StatusBadRequest, b, "min_display_ms must be a number", "")
			return
		}
		b.MinDisplayMs = n
	}
	// The file field counts only with "file" chosen (or no choice, as
	// older form posts send): with the arch or "none" chosen the preview
	// shows that choice, and the save stores it.
	src := r.FormValue("logo_source")
	if f, hdr, err := r.FormFile("logo"); err == nil && (src == "file" || src == "") {
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, branding.MaxLogoBytes+1))
		if err != nil {
			h.renderBranding(w, r, http.StatusBadRequest, b, "logo could not be read", "")
			return
		}
		b.LogoType, b.LogoData, b.PorticoLogo = hdr.Header.Get("Content-Type"), data, false
	} else if src == "file" && !b.HasLogo() {
		h.renderBranding(w, r, http.StatusBadRequest, b, "choose a logo file, or pick Portico's arch or no logo", "")
		return
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
	if err != nil && !errors.As(err, &ae) {
		h.renderBranding(w, r, http.StatusBadRequest, b, err.Error(), "")
		return
	}
	saved := h.brand.Get(r.Context())
	if warn := branding.Warnings(saved, err); warn != "" {
		h.renderBranding(w, r, http.StatusOK, saved, "", "Saved. "+warn)
		return
	}
	http.Redirect(w, r, "/settings/branding", http.StatusSeeOther)
}

func (h *Handler) brandingLogo(w http.ResponseWriter, r *http.Request) {
	api.ServeLogo(w, h.brand.Get(r.Context()))
}

func (h *Handler) brandingIcon(w http.ResponseWriter, r *http.Request) {
	api.ServeIcon(w, h.brand.Get(r.Context()))
}
