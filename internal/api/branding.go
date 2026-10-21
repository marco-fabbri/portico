package api

import (
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/marco-fabbri/portico/internal/branding"
)

// brandingJSON is the wire shape of the branding settings; the logo and
// the tab icon travel as base64.
type brandingJSON struct {
	MSPName      string `json:"msp_name"`
	Tagline      string `json:"tagline"`
	AccentColor  string `json:"accent_color"`
	LogoType     string `json:"logo_type"`
	LogoData     string `json:"logo_data"`
	IconData     string `json:"icon_data"`
	MinDisplayMs int    `json:"min_display_ms"`
	Warning      string `json:"warning,omitempty"`
}

func toBrandingJSON(b branding.Branding, warning string) brandingJSON {
	return brandingJSON{MSPName: b.MSPName, Tagline: b.Tagline, AccentColor: b.AccentColor, LogoType: b.LogoType,
		LogoData: base64.StdEncoding.EncodeToString(b.LogoData), IconData: base64.StdEncoding.EncodeToString(b.IconData),
		MinDisplayMs: b.MinDisplayMs, Warning: warning}
}

func (h *handler) getBranding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, toBrandingJSON(h.brand.Get(r.Context()), ""))
}

func (h *handler) putBranding(w http.ResponseWriter, r *http.Request) {
	var in brandingJSON
	if err := decode(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	logo, err := base64.StdEncoding.DecodeString(in.LogoData)
	if err != nil {
		writeError(w, http.StatusBadRequest, "logo_data must be base64")
		return
	}
	icon, err := base64.StdEncoding.DecodeString(in.IconData)
	if err != nil {
		writeError(w, http.StatusBadRequest, "icon_data must be base64")
		return
	}
	b := branding.Branding{MSPName: in.MSPName, Tagline: in.Tagline, AccentColor: in.AccentColor, LogoType: in.LogoType, LogoData: logo, IconData: icon, MinDisplayMs: in.MinDisplayMs}
	b.FillMissing(branding.Missing{Background: true, Text: true, HeaderText: true})
	err = h.brand.Set(r.Context(), b)
	var ae *branding.ApplyError
	switch {
	case errors.As(err, &ae):
		writeJSON(w, http.StatusOK, toBrandingJSON(h.brand.Get(r.Context()), ae.Err.Error()))
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, toBrandingJSON(h.brand.Get(r.Context()), ""))
	}
}

func (h *handler) getLogo(w http.ResponseWriter, r *http.Request) {
	ServeLogo(w, h.brand.Get(r.Context()))
}

func (h *handler) getIcon(w http.ResponseWriter, r *http.Request) {
	ServeIcon(w, h.brand.Get(r.Context()))
}

// ServeIcon writes the uploaded tab icon as it was uploaded (a PNG);
// shared with the UI's preview.
func ServeIcon(w http.ResponseWriter, b branding.Branding) {
	if !b.HasIcon() {
		writeError(w, http.StatusNotFound, "no icon")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(b.IconData)
}

// ServeLogo writes the logo with headers that keep an SVG inert: no
// scripts, no sniffing, no caching. Shared with the UI and the public
// landing page.
func ServeLogo(w http.ResponseWriter, b branding.Branding) {
	if !b.HasLogo() {
		writeError(w, http.StatusNotFound, "no logo")
		return
	}
	w.Header().Set("Content-Type", b.LogoType)
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(b.LogoData)
}
