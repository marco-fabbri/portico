// Package branding holds the MSP's identity as the tenant sees it (design
// §4.10): name, tagline, accent colour, logo and the minimum display time
// of the landing page. It validates the settings, keeps them in the store
// as the first store-held setting (design §4.7), renders Portico's
// tenant-facing pages and generates the Keycloak login theme.
package branding

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Limits of the settings.
const (
	MaxLogoBytes  = 256 << 10
	MaxNameLen    = 80
	MaxTaglineLen = 160
	// MaxHeaderTextLen bounds the free text under the logo.
	MaxHeaderTextLen = 80
	MaxDisplayMs     = 10000
	// SettingKey is the store key of the JSON-encoded settings.
	SettingKey = "branding"
)

// Branding is the MSP's identity on the tenant-facing pages.
type Branding struct {
	MSPName string `json:"msp_name"`
	// HeaderText is the free text under the logo, shown as typed; the
	// header falls back to MSPName with neither a logo nor a text.
	HeaderText string `json:"header_text"`
	Tagline    string `json:"tagline"`
	// BackgroundColor paints the page, TextColor everything written on
	// it, AccentColor the primary button and the links in the card.
	BackgroundColor string `json:"background_color"`
	TextColor       string `json:"text_color"`
	AccentColor     string `json:"accent_color"`
	// PorticoLogo shows Portico's arch, in the text colour, as the logo;
	// it excludes an uploaded logo (LogoType, LogoData).
	PorticoLogo bool   `json:"portico_logo"`
	LogoType    string `json:"logo_type"`
	LogoData    []byte `json:"logo_data"`
	// IconData is the tab icon, a square PNG (icon.go); empty means
	// Portico's arch on the accent colour.
	IconData     []byte `json:"icon_data"`
	MinDisplayMs int    `json:"min_display_ms"`
}

// Default is what a fresh Portico shows: a branding filled in with
// Portico's own identity, drawn like any MSP's (design branding goal 4).
func Default() Branding {
	return Branding{
		MSPName:         "Portico",
		HeaderText:      "Portico",
		Tagline:         "The single, authenticated entry point to your Nutanix services.",
		BackgroundColor: "#a73422",
		TextColor:       "#ffffff",
		AccentColor:     "#a73422",
		PorticoLogo:     true,
	}
}

var (
	reColor   = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	logoTypes = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/svg+xml": "svg"}
)

// HasLogo reports whether a logo is set.
func (b Branding) HasLogo() bool { return b.LogoType != "" && len(b.LogoData) > 0 }

// Validate reports every violation in one error, each naming its field.
func (b Branding) Validate() error {
	var errs []error
	if n := strings.TrimSpace(b.MSPName); n == "" || utf8.RuneCountInString(n) > MaxNameLen {
		errs = append(errs, fmt.Errorf("msp_name must be 1 to %d characters", MaxNameLen))
	}
	if utf8.RuneCountInString(b.Tagline) > MaxTaglineLen {
		errs = append(errs, fmt.Errorf("tagline must be at most %d characters", MaxTaglineLen))
	}
	if utf8.RuneCountInString(b.HeaderText) > MaxHeaderTextLen || strings.ContainsAny(b.HeaderText, "\r\n") {
		errs = append(errs, fmt.Errorf("header_text must be at most %d characters, on one line", MaxHeaderTextLen))
	}
	for _, c := range []struct{ field, value string }{
		{"background_color", b.BackgroundColor}, {"text_color", b.TextColor}, {"accent_color", b.AccentColor},
	} {
		if !reColor.MatchString(c.value) {
			errs = append(errs, fmt.Errorf("%s must be #rrggbb", c.field))
		}
	}
	if b.PorticoLogo && (b.LogoType != "" || len(b.LogoData) > 0) {
		errs = append(errs, errors.New("portico_logo and an uploaded logo are exclusive"))
	}
	switch {
	case b.LogoType == "" && len(b.LogoData) == 0:
	case b.LogoType == "" || len(b.LogoData) == 0:
		errs = append(errs, errors.New("logo needs both logo_type and logo_data, or neither"))
	case logoTypes[b.LogoType] == "":
		errs = append(errs, errors.New("logo_type must be image/png, image/jpeg or image/svg+xml"))
	case len(b.LogoData) > MaxLogoBytes:
		errs = append(errs, fmt.Errorf("logo must be at most %d bytes", MaxLogoBytes))
	case b.LogoType == "image/svg+xml":
		if err := checkSVG(b.LogoData); err != nil {
			errs = append(errs, fmt.Errorf("logo SVG refused: %v; export it as plain SVG, or upload a PNG", err))
		}
	}
	if b.HasIcon() {
		if err := validateIcon(b.IconData); err != nil {
			errs = append(errs, err)
		}
	}
	if b.MinDisplayMs < 0 || b.MinDisplayMs > MaxDisplayMs {
		errs = append(errs, fmt.Errorf("min_display_ms must be between 0 and %d", MaxDisplayMs))
	}
	return errors.Join(errs...)
}
