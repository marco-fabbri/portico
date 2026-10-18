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
