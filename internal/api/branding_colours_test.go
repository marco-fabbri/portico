package api

import (
	"encoding/base64"
	"strings"
	"testing"
)

// A client that does not know the colours and the header text keeps the
// look it had (design branding §4.1).
func TestBrandingPutWithoutTheNewFieldsKeepsTheOldLook(t *testing.T) {
	e := newEnv(t)
	logo := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="600" height="680"/>`))
	old := `{"msp_name":"Portico MSP","tagline":"The single entry point.","accent_color":"#a73422","logo_type":"image/svg+xml","logo_data":"` + logo + `","min_display_ms":800}`
	rec, out := e.do("PUT", "/api/v1/settings/branding", old, true)
	if rec.Code != 200 || out["warning"] != nil {
		t.Fatalf("PUT old shape: %d %v", rec.Code, out)
	}
	_, out = e.do("GET", "/api/v1/settings/branding", "", true)
	if out["background_color"] != "#a73422" || out["text_color"] != "#ffffff" || out["header_text"] != "Portico MSP" || out["portico_logo"] != false {
		t.Fatalf("old shape with a logo read back as %v", out)
	}
	rec, _ = e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"Portico MSP","accent_color":"#a73422"}`, true)
	_, out = e.do("GET", "/api/v1/settings/branding", "", true)
	if rec.Code != 200 || out["header_text"] != "" || out["portico_logo"] != false {
		t.Fatalf("old shape without a logo: %d %v", rec.Code, out)
	}
	rec, _ = e.do("PUT", "/api/v1/settings/branding", strings.TrimSuffix(old, "}")+`,"header_text":""}`, true)
	_, out = e.do("GET", "/api/v1/settings/branding", "", true)
	if rec.Code != 200 || out["header_text"] != "" {
		t.Fatalf("an explicit empty header text: %d %v", rec.Code, out)
	}
}

func TestBrandingPutReadabilityWarning(t *testing.T) {
	e := newEnv(t)
	rec, out := e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"Portico","header_text":"Portico","portico_logo":true,"background_color":"#a73422","text_color":"#ffffff","accent_color":"#f165b0"}`, true)
	if rec.Code != 200 || out["warning"] != "White button text on accent colour #f165b0 has contrast 2.91:1, below 4.5:1" {
		t.Fatalf("PUT pink accent: %d %v", rec.Code, out)
	}
	if _, out = e.do("GET", "/api/v1/settings/branding", "", true); out["accent_color"] != "#f165b0" {
		t.Fatalf("the accent was not saved: %v", out)
	}
}

func TestBrandingPutRejectsArchWithUpload(t *testing.T) {
	e := newEnv(t)
	logo := base64.StdEncoding.EncodeToString([]byte(pngBytes))
	rec, out := e.do("PUT", "/api/v1/settings/branding", `{"msp_name":"ACME","accent_color":"#a73422","portico_logo":true,"logo_type":"image/png","logo_data":"`+logo+`"}`, true)
	if rec.Code != 400 || !strings.Contains(out["error"].(string), "portico_logo and an uploaded logo are exclusive") {
		t.Fatalf("arch and upload: %d %v", rec.Code, out)
	}
}

func TestBrandingLogoServesTheArch(t *testing.T) {
	e := newEnv(t)
	rec, _ := e.do("GET", "/api/v1/settings/branding/logo", "", true)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(rec.Body.String(), `fill="#ffffff"`) ||
		rec.Header().Get("Content-Security-Policy") != "default-src 'none'" {
		t.Fatalf("default logo: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
}
