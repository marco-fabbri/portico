package branding

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// MinContrast is the WCAG 2 AA ratio for normal text; below it a saved
// branding gets a readability warning (design branding goal 5).
const MinContrast = 4.5

// Contrast is the WCAG 2 contrast ratio of two #rrggbb colours, 1 to 21.
func Contrast(fg, bg string) float64 {
	a, b := luminance(fg), luminance(bg)
	if a < b {
		a, b = b, a
	}
	return (a + 0.05) / (b + 0.05)
}

// luminance is the WCAG 2 relative luminance of a #rrggbb colour.
func luminance(hex string) float64 {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil || len(hex) != 7 {
		return 0
	}
	ch := func(c uint64) float64 {
		s := float64(c) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(v>>16&0xff) + 0.7152*ch(v>>8&0xff) + 0.0722*ch(v&0xff)
}

// ReadabilityWarnings names each pair below MinContrast: the text on the
// background, and the button's white label on the accent (the accent
// links on the white card are the same pair).
func (b Branding) ReadabilityWarnings() []string {
	var w []string
	if r := Contrast(b.TextColor, b.BackgroundColor); r < MinContrast {
		w = append(w, fmt.Sprintf("Text colour %s on background %s has contrast %.2f:1, below %.1f:1", b.TextColor, b.BackgroundColor, r, MinContrast))
	}
	if r := Contrast("#ffffff", b.AccentColor); r < MinContrast {
		w = append(w, fmt.Sprintf("White button text on accent colour %s has contrast %.2f:1, below %.1f:1", b.AccentColor, r, MinContrast))
	}
	return w
}

// Warnings is what the MSP is told after a save of b that stood
// (saveErr nil or an *ApplyError from Set): the failed Keycloak apply
// first, then the readability warnings; "" when there is nothing.
func Warnings(b Branding, saveErr error) string {
	var parts []string
	var ae *ApplyError
	if errors.As(saveErr, &ae) {
		parts = append(parts, "Applying the branding to Keycloak failed: "+ae.Err.Error())
	}
	parts = append(parts, b.ReadabilityWarnings()...)
	return strings.Join(parts, "; ")
}
