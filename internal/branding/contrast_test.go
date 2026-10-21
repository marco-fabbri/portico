package branding

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestContrastReferenceValues(t *testing.T) {
	for _, c := range []struct {
		fg, bg string
		want   float64
	}{
		{"#ffffff", "#3b5bdb", 5.668}, {"#ffffff", "#f165b0", 2.905},
		{"#ffffff", "#a73422", 6.651}, {"#222222", "#f5f5f7", 14.612},
		{"#000000", "#ffffff", 21}, {"#777777", "#ffffff", 4.478},
		{"#ffffff", "#777777", 4.478},
	} {
		if got := Contrast(c.fg, c.bg); math.Abs(got-c.want) > 0.001 {
			t.Errorf("Contrast(%s, %s) = %.4f, want %.3f", c.fg, c.bg, got, c.want)
		}
	}
}

func TestReadabilityWarnings(t *testing.T) {
	b := Default()
	if w := b.ReadabilityWarnings(); len(w) != 0 {
		t.Fatalf("default: %q", w)
	}
	b.AccentColor = "#f165b0"
	want := []string{"White button text on accent colour #f165b0 has contrast 2.91:1, below 4.5:1"}
	if w := b.ReadabilityWarnings(); !reflect.DeepEqual(w, want) {
		t.Fatalf("pink accent: %q", w)
	}
	b.AccentColor, b.BackgroundColor, b.TextColor = "#a73422", "#ffffff", "#777777"
	want = []string{"Text colour #777777 on background #ffffff has contrast 4.48:1, below 4.5:1"}
	if w := b.ReadabilityWarnings(); !reflect.DeepEqual(w, want) {
		t.Fatalf("grey text: %q", w)
	}
}

func TestWarningsJoinApplyFailureAndReadability(t *testing.T) {
	b := Default()
	b.AccentColor = "#f165b0"
	got := Warnings(b, &ApplyError{Err: errors.New("keycloak realm: 502")})
	want := "Applying the branding to Keycloak failed: keycloak realm: 502; White button text on accent colour #f165b0 has contrast 2.91:1, below 4.5:1"
	if got != want {
		t.Fatalf("Warnings = %q", got)
	}
	if got := Warnings(b, nil); got != "White button text on accent colour #f165b0 has contrast 2.91:1, below 4.5:1" {
		t.Fatalf("readability only: %q", got)
	}
	if Warnings(Default(), nil) != "" {
		t.Fatal("no warning expected")
	}
}
