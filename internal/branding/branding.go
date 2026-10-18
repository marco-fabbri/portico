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
)

// Limits of the settings.
const (
	MaxLogoBytes  = 256 << 10
	MaxNameLen    = 80
	MaxTaglineLen = 160
	MaxDisplayMs  = 10000
	// SettingKey is the store key of the JSON-encoded settings.
	SettingKey = "branding"
)

// Branding is the MSP's identity on the tenant-facing pages.
type Branding struct {
	MSPName      string `json:"msp_name"`
	Tagline      string `json:"tagline"`
	AccentColor  string `json:"accent_color"`
	LogoType     string `json:"logo_type"`
	LogoData     []byte `json:"logo_data"`
	MinDisplayMs int    `json:"min_display_ms"`
}

// Default is what a fresh Portico shows.
func Default() Branding {
	return Branding{MSPName: "Portico", AccentColor: "#1f5f8b"}
}

var (
	reColor   = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	logoTypes = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/svg+xml": "svg"}
	// reSVGEvent matches an event-handler attribute such as onload="...".
	reSVGEvent = regexp.MustCompile(`(?i)[\s"'/]on[a-z]+\s*=`)
)

// svgHasActiveContent reports scripts, event handlers, javascript: URLs
// or foreignObject in an SVG. Portico serves logos under a CSP that
// forbids scripts, but Keycloak serves the theme's copy from the login
// origin with its own headers, so active content is refused outright.
func svgHasActiveContent(data []byte) bool {
	s := strings.ToLower(string(data))
	return strings.Contains(s, "<script") || strings.Contains(s, "javascript:") ||
		strings.Contains(s, "<foreignobject") || reSVGEvent.MatchString(s)
}

// LogoExt returns the file extension of the logo type ("" without a logo).
func (b Branding) LogoExt() string { return logoTypes[b.LogoType] }

// HasLogo reports whether a logo is set.
func (b Branding) HasLogo() bool { return b.LogoType != "" && len(b.LogoData) > 0 }

// Validate reports every violation in one error, each naming its field.
func (b Branding) Validate() error {
	var errs []error
	if n := strings.TrimSpace(b.MSPName); n == "" || len(n) > MaxNameLen {
		errs = append(errs, fmt.Errorf("msp_name must be 1 to %d characters", MaxNameLen))
	}
	if len(b.Tagline) > MaxTaglineLen {
		errs = append(errs, fmt.Errorf("tagline must be at most %d characters", MaxTaglineLen))
	}
	if !reColor.MatchString(b.AccentColor) {
		errs = append(errs, errors.New("accent_color must be #rrggbb"))
	}
	switch {
	case b.LogoType == "" && len(b.LogoData) == 0:
	case b.LogoType == "" || len(b.LogoData) == 0:
		errs = append(errs, errors.New("logo needs both logo_type and logo_data, or neither"))
	case logoTypes[b.LogoType] == "":
		errs = append(errs, errors.New("logo_type must be image/png, image/jpeg or image/svg+xml"))
	case len(b.LogoData) > MaxLogoBytes:
		errs = append(errs, fmt.Errorf("logo must be at most %d bytes", MaxLogoBytes))
	case b.LogoType == "image/svg+xml" && svgHasActiveContent(b.LogoData):
		errs = append(errs, errors.New("logo SVG must not contain scripts, event handlers, javascript: links or foreignObject"))
	}
	if b.MinDisplayMs < 0 || b.MinDisplayMs > MaxDisplayMs {
		errs = append(errs, fmt.Errorf("min_display_ms must be between 0 and %d", MaxDisplayMs))
	}
	return errors.Join(errs...)
}
