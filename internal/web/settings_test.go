package web

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/branding"
)

// postMultipart sends the branding form with an optional file.
func (e *env) postMultipart(t *testing.T, fields map[string]string, fileName, fileType string, file []byte, withCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if withCSRF {
		mw.WriteField("_csrf", e.csrf)
	}
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if file != nil {
		h := make(map[string][]string)
		h["Content-Disposition"] = []string{`form-data; name="logo"; filename="` + fileName + `"`}
		h["Content-Type"] = []string{fileType}
		part, _ := mw.CreatePart(h)
		part.Write(file)
	}
	mw.Close()
	r := httptest.NewRequest("POST", "https://127.0.0.1:8443/settings/branding", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if e.cookie != nil {
		r.AddCookie(e.cookie)
	}
	r.RemoteAddr = "10.0.0.9:4000"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

var png = []byte("\x89PNG\r\n\x1a\n")

func TestBrandingPageShowsCurrentValues(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.do("GET", "/settings/branding", nil, true)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `value="Portico"`) || !strings.Contains(body, `name="logo"`) || !strings.Contains(body, `href="/settings/branding"`) {
		t.Fatalf("branding page: %d %s", rec.Code, body)
	}
}

func TestBrandingFormSavesAndShowsLogo(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.postMultipart(t, map[string]string{"msp_name": "ACME Cloud", "tagline": "Your VMs", "accent_color": "#8b1f5f", "min_display_ms": "250"}, "logo.png", "image/png", png, true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/branding" {
		t.Fatalf("save: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	rec = e.do("GET", "/settings/branding", nil, true)
	if !strings.Contains(rec.Body.String(), `value="ACME Cloud"`) || !strings.Contains(rec.Body.String(), `value="#8b1f5f"`) {
		t.Fatalf("page after save: %s", rec.Body.String())
	}
	rec = e.do("GET", "/settings/branding/logo", nil, true)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Content-Security-Policy") != "default-src 'none'" || !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("logo: %d %v", rec.Code, rec.Header())
	}
	if got := e.brand.Get(context.Background()); got.MinDisplayMs != 250 || got.Tagline != "Your VMs" {
		t.Fatalf("stored = %+v", got)
	}
}

func TestBrandingFormRejectsBadInput(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.postMultipart(t, map[string]string{"msp_name": "ACME", "accent_color": "#000000"}, "logo.gif", "image/gif", []byte("GIF89a"), true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "logo") {
		t.Fatalf("gif: %d %s", rec.Code, rec.Body.String())
	}
	big := bytes.Repeat([]byte{1}, 300<<10)
	rec = e.postMultipart(t, map[string]string{"msp_name": "ACME", "accent_color": "#000000"}, "logo.png", "image/png", big, true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "logo") {
		t.Fatalf("big: %d", rec.Code)
	}
	if e.brand.Get(context.Background()).MSPName != "Portico" {
		t.Fatal("a rejected form changed the branding")
	}
}

func TestBrandingFormRemoveLogo(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.postMultipart(t, map[string]string{"msp_name": "ACME", "accent_color": "#000000"}, "logo.png", "image/png", png, true)
	if rec := e.do("GET", "/settings/branding/logo", nil, true); rec.Code != 200 {
		t.Fatalf("logo before removal: %d", rec.Code)
	}
	rec := e.postMultipart(t, map[string]string{"msp_name": "ACME", "accent_color": "#000000", "remove_logo": "on"}, "", "", nil, true)
	if rec.Code != 303 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "/settings/branding/logo", nil, true); rec.Code != 404 {
		t.Fatalf("logo after removal: %d", rec.Code)
	}
	// Saving without a file and without the checkbox keeps the logo.
	e.postMultipart(t, map[string]string{"msp_name": "ACME", "accent_color": "#000000"}, "logo.png", "image/png", png, true)
	e.postMultipart(t, map[string]string{"msp_name": "ACME 2", "accent_color": "#000000"}, "", "", nil, true)
	if rec := e.do("GET", "/settings/branding/logo", nil, true); rec.Code != 200 {
		t.Fatalf("logo after a save without a file: %d", rec.Code)
	}
}

func TestBrandingFormRequiresCSRF(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.postMultipart(t, map[string]string{"msp_name": "Evil", "accent_color": "#000000"}, "", "", nil, false)
	if rec.Code != 403 || e.brand.Get(context.Background()).MSPName != "Portico" {
		t.Fatalf("without CSRF: %d, name %q", rec.Code, e.brand.Get(context.Background()).MSPName)
	}
}

func TestBrandingFormShowsApplyWarning(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	e.brand.OnChange(func(ctx context.Context, b branding.Branding) error {
		return errors.New("theme files: permission denied")
	})
	rec := e.postMultipart(t, map[string]string{"msp_name": "ACME", "accent_color": "#000000"}, "", "", nil, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "theme files") || !strings.Contains(rec.Body.String(), `class="banner warning"`) {
		t.Fatalf("apply warning: %d %s", rec.Code, rec.Body.String())
	}
	if e.brand.Get(context.Background()).MSPName != "ACME" {
		t.Fatal("values not saved although the hook failed")
	}
	_ = http.StatusOK
}
