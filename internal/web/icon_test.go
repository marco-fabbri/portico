package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	pngenc "image/png"
	"strings"
	"testing"
)

// squarePNG encodes a w by h red PNG.
func squarePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Set(i%w, i/w, color.NRGBA{255, 0, 0, 255})
	}
	var buf bytes.Buffer
	if err := pngenc.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBrandingFormSavesShowsAndRemovesTheIcon(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	fields := map[string]string{"msp_name": "ACME", "accent_color": "#000000"}
	icon := squarePNG(t, 180, 180)
	if rec := e.do("GET", "/settings/branding", nil, true); !strings.Contains(rec.Body.String(), `name="icon"`) {
		t.Fatalf("no icon field: %s", rec.Body.String())
	}
	rec := e.postBranding(t, fields, []formFile{{"icon", "icon.png", "image/png", icon}}, true)
	if rec.Code != 303 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("GET", "/settings/branding/icon", nil, true)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), icon) {
		t.Fatalf("icon: %d %v", rec.Code, rec.Header())
	}
	if page := e.do("GET", "/settings/branding", nil, true).Body.String(); !strings.Contains(page, `src="/settings/branding/icon"`) || !strings.Contains(page, `name="remove_icon"`) {
		t.Fatalf("no preview or remove box: %s", page)
	}
	// A save without a file keeps the icon (and the logo field alone does
	// not touch it).
	e.postBranding(t, map[string]string{"msp_name": "ACME 2", "accent_color": "#000000"}, nil, true)
	if got := e.brand.Get(context.Background()); !bytes.Equal(got.IconData, icon) {
		t.Fatal("a save without a file dropped the icon")
	}
	fields["remove_icon"] = "on"
	if rec := e.postBranding(t, fields, nil, true); rec.Code != 303 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "/settings/branding/icon", nil, true); rec.Code != 404 {
		t.Fatalf("icon after removal: %d", rec.Code)
	}
}

func TestBrandingFormRejectsABadIcon(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.postBranding(t, map[string]string{"msp_name": "ACME", "accent_color": "#000000"}, []formFile{{"icon", "banner.png", "image/png", squarePNG(t, 400, 150)}}, true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "shorter side must be at least half") {
		t.Fatalf("wide icon: %d %s", rec.Code, rec.Body.String())
	}
	// Both files at their limit still fit in the form.
	big := append(squarePNG(t, 180, 180), bytes.Repeat([]byte{0}, 200<<10)...)
	logo := bytes.Repeat([]byte{1}, 250<<10)
	rec = e.postBranding(t, map[string]string{"msp_name": "ACME", "accent_color": "#000000"}, []formFile{{"logo", "l.png", "image/png", append(png, logo...)}, {"icon", "i.png", "image/png", big}}, true)
	if rec.Code != 303 {
		t.Fatalf("logo and icon near their limits: %d %s", rec.Code, rec.Body.String())
	}
}
