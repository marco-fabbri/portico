package api

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
)

func iconPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBrandingIconRoundTrip(t *testing.T) {
	e := newEnv(t)
	if rec, _ := e.do("GET", "/api/v1/settings/branding/icon", "", true); rec.Code != 404 {
		t.Fatalf("icon without one: %d", rec.Code)
	}
	wide := base64.StdEncoding.EncodeToString(iconPNG(t, 300, 200))
	rec, out := e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"ACME","accent_color":"#000000","icon_data":"`+wide+`"}`, true)
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "icon must be square") {
		t.Fatalf("wide icon: %d %v", rec.Code, out)
	}
	icon := iconPNG(t, 180, 180)
	enc := base64.StdEncoding.EncodeToString(icon)
	rec, out = e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"ACME","accent_color":"#000000","icon_data":"`+enc+`"}`, true)
	if rec.Code != 200 || out["icon_data"] != enc {
		t.Fatalf("PUT icon: %d %v", rec.Code, out["error"])
	}
	rec, _ = e.do("GET", "/api/v1/settings/branding/icon", "", true)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), icon) ||
		rec.Header().Get("Content-Security-Policy") != "default-src 'none'" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("GET icon: %d %v", rec.Code, rec.Header())
	}
	if rec, _ := e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"ACME","accent_color":"#000000","icon_data":"%%%"}`, true); rec.Code != 400 {
		t.Fatalf("icon_data not base64: %d", rec.Code)
	}
}
