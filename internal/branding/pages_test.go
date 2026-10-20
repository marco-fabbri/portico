package branding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pagesFor(b Branding) *Pages {
	return NewPages(func(ctx context.Context) Branding { return b })
}

func TestLandingPageContent(t *testing.T) {
	b := Default()
	b.MSPName, b.Tagline = "ACME <b>Cloud</b>", "Your VMs & more"
	p := pagesFor(b)
	rec := httptest.NewRecorder()
	p.Landing(rec, httptest.NewRequest("GET", "https://prism.test/ui/iam/providers", nil), "/.portico/saml-start")
	body := rec.Body.String()
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("landing: %d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(body, "ACME &lt;b&gt;Cloud&lt;/b&gt;") || strings.Contains(body, "<b>Cloud</b>") {
		t.Errorf("name not escaped: %s", body)
	}
	if !strings.Contains(body, "Your VMs &amp; more") {
		t.Errorf("tagline not escaped: %s", body)
	}
	if !strings.Contains(body, `<meta http-equiv="refresh" content="0;url=/.portico/saml-start">`) {
		t.Errorf("meta refresh missing or wrong: %s", body)
	}
	if !strings.Contains(body, `href="/.portico/saml-start"`) {
		t.Errorf("no plain link to continue: %s", body)
	}
	if strings.Contains(body, "<script") {
		t.Errorf("the landing page must not need JavaScript: %s", body)
	}
	if strings.Contains(body, "/.portico/logo") {
		t.Errorf("logo referenced without a logo: %s", body)
	}
	b.MinDisplayMs = 1500
	b.LogoType, b.LogoData = "image/png", []byte("x")
	rec = httptest.NewRecorder()
	pagesFor(b).Landing(rec, httptest.NewRequest("GET", "https://prism.test/ui/iam/providers", nil), "/.portico/saml-start")
	if !strings.Contains(rec.Body.String(), `content="1;url=/.portico/saml-start"`) || !strings.Contains(rec.Body.String(), `src="/.portico/logo"`) {
		t.Errorf("min display or logo: %s", rec.Body.String())
	}
}

func TestErrorPageContent(t *testing.T) {
	p := pagesFor(Default())
	rec := httptest.NewRecorder()
	p.Error(rec, httptest.NewRequest("GET", "https://prism.test/.portico/login", nil), http.StatusServiceUnavailable, "Identity provider unavailable", "Try again in a <minute>", "/.portico/login?x=1&y=2")
	body := rec.Body.String()
	if rec.Code != 503 || !strings.Contains(body, "Identity provider unavailable") || !strings.Contains(body, "Try again in a &lt;minute&gt;") {
		t.Fatalf("error page: %d %s", rec.Code, body)
	}
	if !strings.Contains(body, `href="/.portico/login?x=1&amp;y=2"`) || !strings.Contains(body, "Portico") {
		t.Errorf("retry link or name: %s", body)
	}
	rec = httptest.NewRecorder()
	p.Error(rec, httptest.NewRequest("GET", "https://prism.test/x", nil), http.StatusBadGateway, "Upstream unavailable", "x", "")
	if strings.Contains(rec.Body.String(), "href=\"\"") {
		t.Errorf("empty retry link rendered: %s", rec.Body.String())
	}
}

// The landing and error pages look like the Keycloak login page the user
// just left (Marco, 2026-10-19: the landing page was a light grey page
// with the white logo on it): the accent colour as background, the
// lockup in white (logo left of the name, last word bold, the tagline
// under it), the error in a white card, "Powered by Portico" at the foot.
func TestPagesWearTheLoginPageLook(t *testing.T) {
	b := Default()
	b.MSPName, b.Tagline, b.AccentColor = "Portico MSP", "The single entry point.", "#a73422"
	b.LogoType, b.LogoData = "image/svg+xml", []byte("<svg/>")
	p := pagesFor(b)
	rec := httptest.NewRecorder()
	p.Landing(rec, httptest.NewRequest("GET", "https://prism.test/", nil), "/.portico/saml-start")
	landing := rec.Body.String()
	rec = httptest.NewRecorder()
	p.Error(rec, httptest.NewRequest("GET", "https://prism.test/", nil), http.StatusServiceUnavailable, "Identity provider unavailable", "Try again later.", "/.portico/login")
	errPage := rec.Body.String()
	for name, body := range map[string]string{"landing": landing, "error": errPage} {
		for _, want := range []string{
			"background: #a73422",
			`<div class="lockup"><img class="logo" src="/.portico/logo" alt=""><span class="name">Portico<strong>MSP</strong></span></div>`,
			`<p class="claim">The single entry point.</p>`,
			`class="powered"`,
			"Powered by Portico",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s page lacks %q:\n%s", name, want, body)
			}
		}
		if strings.Contains(body, "#f4f6f8") {
			t.Errorf("%s page still has the grey background", name)
		}
	}
	if !strings.Contains(errPage, `<section class="card">`) {
		t.Errorf("error text not in a card:\n%s", errPage)
	}
	// Without a logo the lockup is the name alone; one word is all bold-free.
	b.LogoType, b.LogoData, b.MSPName = "", nil, "Solo"
	rec = httptest.NewRecorder()
	pagesFor(b).Landing(rec, httptest.NewRequest("GET", "https://prism.test/", nil), "/x")
	if !strings.Contains(rec.Body.String(), `<div class="lockup"><span class="name">Solo</span></div>`) {
		t.Errorf("no-logo lockup:\n%s", rec.Body.String())
	}
}

// The site page, variant B of the mockup: one tile per site with code,
// name and status; a tile that is not available cannot be chosen.
func TestSitesTemplateVariantB(t *testing.T) {
	b := Default()
	b.MSPName, b.AccentColor = "ACME Cloud", "#1f5f8b"
	rec := httptest.NewRecorder()
	pagesFor(b).Sites(rec, httptest.NewRequest("GET", "https://prism.test/.portico/sites", nil), []SiteTile{
		{ID: "mi1", Code: "AZ-IT-MI1", Name: "Milan", Status: "available", Selectable: true, Current: true},
		{ID: "nyc1", Code: "AZ-US-NYC1", Name: "New York", Status: "maintenance"},
	}, "carol@acme.example")
	body := rec.Body.String()
	for _, want := range []string{
		"Choose a site", "background: #1f5f8b", `<form method="post" action="/.portico/sites/select">`,
		`name="site" value="mi1">`, `name="site" value="nyc1" disabled>`, "AZ-IT-MI1", "Milan", "Available · current",
		"In maintenance", "carol@acme.example", `href="/.portico/logout"`, "Powered by Portico",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("site page lacks %q", want)
		}
	}
	if rec.Header().Get("Content-Security-Policy") == "" || strings.Contains(body, "<script") {
		t.Fatal("the site page must keep the CSP and carry no script")
	}
}
