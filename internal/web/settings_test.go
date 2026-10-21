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

// formFile is one file of the branding form.
type formFile struct {
	field, name, ctype string
	data               []byte
}

// postMultipart sends the branding form with an optional logo file.
func (e *env) postMultipart(t *testing.T, fields map[string]string, fileName, fileType string, file []byte, withCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	var files []formFile
	if file != nil {
		files = append(files, formFile{"logo", fileName, fileType, file})
	}
	return e.postBranding(t, fields, files, withCSRF)
}

// postBranding sends the branding form with any files.
func (e *env) postBranding(t *testing.T, fields map[string]string, files []formFile, withCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if withCSRF {
		mw.WriteField("_csrf", e.csrf)
	}
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	for _, f := range files {
		h := make(map[string][]string)
		h["Content-Disposition"] = []string{`form-data; name="` + f.field + `"; filename="` + f.name + `"`}
		h["Content-Type"] = []string{f.ctype}
		part, _ := mw.CreatePart(h)
		part.Write(f.data)
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

func TestBrandingFormSavesColoursAndHeaderText(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.postMultipart(t, map[string]string{"msp_name": "Portico MSP", "header_text": "Portico MSP", "background_color": "#f5f5f7",
		"text_color": "#222222", "accent_color": "#3b5bdb", "logo_source": "portico"}, "", "", nil, true)
	if rec.Code != 303 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	got := e.brand.Get(context.Background())
	if got.HeaderText != "Portico MSP" || got.BackgroundColor != "#f5f5f7" || got.TextColor != "#222222" || got.AccentColor != "#3b5bdb" || !got.PorticoLogo {
		t.Fatalf("stored = %+v", got)
	}
}

func TestBrandingFormLogoSource(t *testing.T) {
	base := map[string]string{"msp_name": "ACME", "accent_color": "#000000"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range base {
			m[a] = b
		}
		m[k] = v
		return m
	}
	e := newEnv(t, true)
	e.login(t)
	// An upload wins over the arch.
	if rec := e.postMultipart(t, with("logo_source", "portico"), "logo.png", "image/png", png, true); rec.Code != 303 {
		t.Fatalf("upload with portico chosen: %d %s", rec.Code, rec.Body.String())
	}
	if got := e.brand.Get(context.Background()); !got.HasLogo() || got.PorticoLogo {
		t.Fatalf("after an upload: %+v", got)
	}
	// "file" without a file keeps the saved upload.
	if rec := e.postMultipart(t, with("logo_source", "file"), "", "", nil, true); rec.Code != 303 || !e.brand.Get(context.Background()).HasLogo() {
		t.Fatalf("keep the upload: %d", rec.Code)
	}
	// "none" removes it.
	if rec := e.postMultipart(t, with("logo_source", "none"), "", "", nil, true); rec.Code != 303 {
		t.Fatalf("none: %d", rec.Code)
	}
	if got := e.brand.Get(context.Background()); got.HasLogo() || got.PorticoLogo {
		t.Fatalf("after none: %+v", got)
	}
	if rec := e.do("GET", "/settings/branding/logo", nil, true); rec.Code != 404 {
		t.Fatalf("logo after none: %d", rec.Code)
	}
	// "file" without a file and nothing saved.
	rec := e.postMultipart(t, with("logo_source", "file"), "", "", nil, true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "choose a logo file") {
		t.Fatalf("file without one: %d %s", rec.Code, rec.Body.String())
	}
	// "portico" puts the arch back.
	if rec := e.postMultipart(t, with("logo_source", "portico"), "", "", nil, true); rec.Code != 303 || !e.brand.Get(context.Background()).PorticoLogo {
		t.Fatalf("arch back: %d", rec.Code)
	}
}

func TestBrandingFormShowsReadabilityWarning(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.postMultipart(t, map[string]string{"msp_name": "ACME", "accent_color": "#f165b0"}, "", "", nil, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Saved. White button text on accent colour #f165b0 has contrast 2.91:1, below 4.5:1") {
		t.Fatalf("readability warning: %d %s", rec.Code, rec.Body.String())
	}
	if e.brand.Get(context.Background()).AccentColor != "#f165b0" {
		t.Fatal("the accent was not saved")
	}
}

// A form re-rendered after an error keeps what was typed, so the fields
// and the preview do not jump back to the saved branding (Review Focus 4).
func TestBrandingFormKeepsSubmittedValuesOnError(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.postMultipart(t, map[string]string{"msp_name": "ACME", "accent_color": "#3b5bdb", "background_color": "#f5f5f7", "text_color": "#222222",
		"header_text": "Portico MSP", "logo_source": "none", "min_display_ms": "abc"}, "", "", nil, true)
	body := rec.Body.String()
	if rec.Code != 400 {
		t.Fatalf("bad min_display_ms: %d", rec.Code)
	}
	for _, want := range []string{`value="#f5f5f7"`, `value="#222222"`, `value="Portico MSP"`, `value="none" checked`} {
		if !strings.Contains(body, want) {
			t.Errorf("re-rendered form lacks %q", want)
		}
	}
}

func TestBrandingPageHasThePreview(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.do("GET", "/settings/branding", nil, true)
	body := rec.Body.String()
	for _, want := range []string{`id="brand-preview"`, `src="/static/branding-preview.js?v=`, `fill="currentColor"`,
		`name="header_text"`, `name="background_color"`, `name="text_color"`, `name="logo_source" value="portico" checked`} {
		if !strings.Contains(body, want) {
			t.Errorf("branding page lacks %q", want)
		}
	}
	if strings.Contains(body, `style="`) || strings.Contains(body, "<style") {
		t.Error("inline styles are blocked by the admin CSP")
	}
	rec = e.do("GET", "/static/branding-preview.js", nil, true)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("preview script: %d %q", rec.Code, rec.Header().Get("Content-Type"))
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
