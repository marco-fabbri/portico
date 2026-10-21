package branding

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"reflect"
	"strings"
	"testing"
)

// blankPNG draws a transparent w by h PNG.
func blankPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	return pngOf(t, w, h, func(int, int) color.NRGBA { return color.NRGBA{} })
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestHeaderPieces(t *testing.T) {
	logo := blankPNG(t, 167, 178)
	cases := []struct {
		name string
		mut  func(*Branding)
		want Header
	}{
		{"logo and text", func(b *Branding) { b.LogoType, b.LogoData, b.HeaderText = "image/png", logo, "Portico MSP" }, Header{Logo: true, LogoW: 90, LogoH: 96, Text: "Portico MSP"}},
		{"logo only", func(b *Branding) { b.LogoType, b.LogoData, b.HeaderText = "image/png", logo, "" }, Header{Logo: true, LogoW: 90, LogoH: 96}},
		{"text only", func(b *Branding) { b.HeaderText = "Portico MSP" }, Header{Text: "Portico MSP"}},
		{"neither: the MSP name", func(b *Branding) { b.HeaderText, b.MSPName = "", "ACME" }, Header{Text: "ACME"}},
		{"the arch is a logo", func(b *Branding) { b.PorticoLogo, b.HeaderText = true, "" }, Header{Logo: true, LogoW: 85, LogoH: 96}},
	}
	for _, c := range cases {
		b := Default()
		b.PorticoLogo = false
		c.mut(&b)
		if got := b.Header(); got != c.want {
			t.Errorf("%s: Header() = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestLogoBox(t *testing.T) {
	svg := func(attrs string) []byte {
		return []byte(`<svg xmlns="http://www.w3.org/2000/svg" ` + attrs + `><rect/></svg>`)
	}
	cases := []struct {
		name         string
		ctype        string
		data         []byte
		wantW, wantH int
	}{
		{"png tall", "image/png", blankPNG(t, 167, 178), 90, 96},
		{"png small stays small", "image/png", blankPNG(t, 40, 40), 40, 40},
		{"jpeg wide", "image/jpeg", jpegOf(t, 1000, 100), 320, 32},
		{"svg size", "image/svg+xml", svg(`width="600" height="680"`), 85, 96},
		{"svg px", "image/svg+xml", svg(`width="600px" height="680px"`), 85, 96},
		{"svg viewBox only", "image/svg+xml", svg(`viewBox="0 0 1200 300"`), 320, 80},
		{"svg percent", "image/svg+xml", svg(`width="100%"`), 96, 96},
		{"svg other units", "image/svg+xml", svg(`width="10cm" height="2cm"`), 96, 96},
	}
	for _, c := range cases {
		b := Default()
		b.PorticoLogo, b.LogoType, b.LogoData = false, c.ctype, c.data
		h := b.Header()
		if !h.Logo || h.LogoW != c.wantW || h.LogoH != c.wantH {
			t.Errorf("%s: box %dx%d (logo %v), want %dx%d", c.name, h.LogoW, h.LogoH, h.Logo, c.wantW, c.wantH)
		}
	}
}

func TestLogoFileIsTheArchInTheTextColour(t *testing.T) {
	b := Default()
	b.TextColor = "#222222"
	data, ct, ok := b.LogoFile()
	if !ok || ct != "image/svg+xml" || !strings.Contains(string(data), `fill="#222222"`) || b.LogoFileExt() != "svg" {
		t.Fatalf("arch logo: ok %v, type %q, ext %q, %s", ok, ct, b.LogoFileExt(), data)
	}
	b.LogoType, b.LogoData, b.PorticoLogo = "image/png", blankPNG(t, 10, 10), false
	if data, ct, ok := b.LogoFile(); !ok || ct != "image/png" || !bytes.Equal(data, b.LogoData) || b.LogoFileExt() != "png" {
		t.Fatalf("uploaded logo: ok %v, type %q, ext %q", ok, ct, b.LogoFileExt())
	}
	b.LogoType, b.LogoData = "", nil
	if _, _, ok := b.LogoFile(); ok || b.LogoFileExt() != "" {
		t.Fatal("no logo expected")
	}
}

func TestServiceLogoFile(t *testing.T) {
	svc := NewService(newStore(t), nil)
	if data, ct, ok := svc.LogoFile(context.Background()); !ok || ct != "image/svg+xml" || !strings.Contains(string(data), `fill="#ffffff"`) {
		t.Fatalf("default logo file: ok %v, type %q", ok, ct)
	}
}

// A document stored before the colours and the header text existed
// reads with the look it had (design branding goal 7).
func TestOldDocumentReadsWithItsLook(t *testing.T) {
	logo := []byte(`<svg width="600" height="680"/>`)
	old := `{"msp_name":"Portico MSP","tagline":"x","accent_color":"#a73422","logo_type":"image/svg+xml","logo_data":"` +
		base64.StdEncoding.EncodeToString(logo) + `","icon_data":null,"min_display_ms":800}`
	st := newStore(t)
	if err := st.SetSetting(context.Background(), SettingKey, []byte(old)); err != nil {
		t.Fatal(err)
	}
	b := NewService(st, nil).Get(context.Background())
	if b.BackgroundColor != "#a73422" || b.TextColor != "#ffffff" || b.HeaderText != "Portico MSP" || b.PorticoLogo ||
		!bytes.Equal(b.LogoData, logo) || b.MinDisplayMs != 800 {
		t.Fatalf("old document with a logo read as %+v", b)
	}
	noLogo := `{"msp_name":"Portico MSP","tagline":"x","accent_color":"#a73422","logo_type":"","logo_data":null,"icon_data":null,"min_display_ms":0}`
	st = newStore(t)
	if err := st.SetSetting(context.Background(), SettingKey, []byte(noLogo)); err != nil {
		t.Fatal(err)
	}
	b = NewService(st, nil).Get(context.Background())
	if b.BackgroundColor != "#a73422" || b.TextColor != "#ffffff" || b.HeaderText != "" || b.Header().Text != "Portico MSP" {
		t.Fatalf("old document without a logo read as %+v", b)
	}
}

func TestFillMissing(t *testing.T) {
	base := Branding{MSPName: "ACME", AccentColor: "#123456", BackgroundColor: "#000000", TextColor: "#111111",
		HeaderText: "kept", LogoType: "image/png", LogoData: blankPNG(t, 10, 10)}
	b := base
	b.FillMissing(Missing{Background: true})
	if b.BackgroundColor != "#123456" || b.TextColor != "#111111" || b.HeaderText != "kept" {
		t.Errorf("background only: %+v", b)
	}
	b = base
	b.FillMissing(Missing{Text: true})
	if b.TextColor != "#ffffff" || b.BackgroundColor != "#000000" || b.HeaderText != "kept" {
		t.Errorf("text only: %+v", b)
	}
	b = base
	b.FillMissing(Missing{HeaderText: true})
	if b.HeaderText != "ACME" || b.BackgroundColor != "#000000" || b.TextColor != "#111111" {
		t.Errorf("header text with a logo: %+v", b)
	}
	b = base
	b.LogoType, b.LogoData = "", nil
	b.FillMissing(Missing{HeaderText: true})
	if b.HeaderText != "" {
		t.Errorf("header text without a logo: %+v", b)
	}
	b = base
	b.FillMissing(Missing{})
	if !reflect.DeepEqual(b, base) {
		t.Errorf("nothing missing changed %+v", b)
	}
}
