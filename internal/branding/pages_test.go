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
	b := noLogo()
	b.MSPName, b.HeaderText, b.Tagline = "ACME <b>Cloud</b>", "", "Your VMs & more"
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

// renderAll renders the landing page, the site page and an error page
// with a retry link for b.
func renderAll(t *testing.T, b Branding) map[string]string {
	t.Helper()
	p := pagesFor(b)
	req := func() *http.Request { return httptest.NewRequest("GET", "https://prism.test/", nil) }
	out := map[string]string{}
	rec := httptest.NewRecorder()
	p.Landing(rec, req(), "/.portico/saml-start")
	out["landing"] = rec.Body.String()
	rec = httptest.NewRecorder()
	p.Sites(rec, req(), []SiteTile{{ID: "mi1", Code: "AZ-IT-MI1", Status: "available", Selectable: true}}, "carol@acme.example")
	out["sites"] = rec.Body.String()
	rec = httptest.NewRecorder()
	p.Error(rec, req(), http.StatusServiceUnavailable, "Identity provider unavailable", "Try again later.", "/.portico/login")
	out["error"] = rec.Body.String()
	return out
}

// Every tenant page has the header of the login page: logo, text, claim
// (design branding §4.2), and "Powered by Portico" at the foot.
func TestPagesHeaderPieces(t *testing.T) {
	logo := blankPNG(t, 167, 178)
	img := `<img class="logo" src="/.portico/logo" width="90" height="96" alt="Portico MSP">`
	cases := []struct {
		name      string
		mut       func(*Branding)
		want, not []string
	}{
		{"logo and text", func(b *Branding) { b.LogoType, b.LogoData, b.HeaderText = "image/png", logo, "Portico MSP" },
			[]string{img + `<p class="header-text">Portico MSP</p>`}, nil},
		{"logo only", func(b *Branding) { b.LogoType, b.LogoData, b.HeaderText = "image/png", logo, "" },
			[]string{img}, []string{`class="header-text"`}},
		{"text only", func(b *Branding) { b.HeaderText = "Portico MSP" },
			[]string{`<p class="header-text">Portico MSP</p>`}, []string{"<img"}},
		{"neither: the MSP name", func(b *Branding) { b.HeaderText, b.MSPName = "", "ACME" },
			[]string{`<p class="header-text">ACME</p>`}, []string{"<img"}},
	}
	for _, c := range cases {
		b := noLogo()
		b.MSPName, b.Tagline = "Portico MSP", "Claim"
		c.mut(&b)
		for page, body := range renderAll(t, b) {
			for _, want := range append(c.want, `<p class="claim">Claim</p>`, "Powered by Portico") {
				if !strings.Contains(body, want) {
					t.Errorf("%s, %s page lacks %q", c.name, page, want)
				}
			}
			for _, not := range append(c.not, "lockup", "uppercase") {
				if strings.Contains(body, not) {
					t.Errorf("%s, %s page has %q", c.name, page, not)
				}
			}
		}
	}
}

func TestPagesUseTheThreeColours(t *testing.T) {
	b := Default()
	b.BackgroundColor, b.TextColor, b.AccentColor = "#f5f5f7", "#222222", "#3b5bdb"
	pages := renderAll(t, b)
	for page, body := range pages {
		for _, want := range []string{"background: #f5f5f7", "color: #222222", `fill="#222222"`, "overflow-wrap: anywhere", "0 8px 24px rgba(0, 0, 0, 0.16)",
			`<img class="logo" src="/.portico/logo" width="85" height="96" alt="Portico">`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s page lacks %q", page, want)
			}
		}
		for _, old := range []string{"rgba(0, 0, 0, .28)", "rgba(255, 255, 255", `fill="#ffffff"`} {
			if strings.Contains(body, old) {
				t.Errorf("%s page still has %q", page, old)
			}
		}
	}
	for page, want := range map[string]string{
		"landing": "border-top-color: #222222",
		"sites":   "outline: 3px solid #3b5bdb",
		"error":   "background: #3b5bdb",
	} {
		if !strings.Contains(pages[page], want) {
			t.Errorf("%s page lacks %q", page, want)
		}
	}
	if !strings.Contains(pages["landing"], ".status a { color: #222222; }") || !strings.Contains(pages["sites"], ".who a { color: #222222; }") {
		t.Error("the links outside the cards are not in the text colour")
	}
	if !strings.Contains(pages["error"], `<section class="card">`) {
		t.Errorf("error text not in a card:\n%s", pages["error"])
	}
}

func TestSitesTemplateVariantB(t *testing.T) {
	b := Default()
	b.MSPName, b.BackgroundColor = "ACME Cloud", "#1f5f8b"
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
