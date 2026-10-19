package web

import (
	"context"
	"github.com/marco-fabbri/portico/internal/api"
	"golang.org/x/crypto/bcrypt"
	"net/url"
	"strings"
	"testing"
)

// The admin UI skin (mockup approved by Marco on 2026-10-19): a white
// header with Portico's black mark and the PORTICO wordmark, section
// tabs with the current one marked, the accent colour only where one
// acts, Red Hat fonts shipped with the binary.

func TestSkinHeaderMarksTheCurrentSection(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	cases := map[string]string{
		"/":                  `<a href="/" aria-current="page">Tenants</a>`,
		"/tenants/new":       `<a href="/" aria-current="page">Tenants</a>`,
		"/settings/branding": `<a href="/settings/branding" aria-current="page">Branding</a>`,
		"/setup":             `<a href="/setup" aria-current="page">Setup</a>`,
	}
	for path, want := range cases {
		page := e.do("GET", path, nil, true).Body.String()
		if !strings.Contains(page, want) {
			t.Errorf("%s: header lacks %q:\n%s", path, want, page)
		}
		if strings.Count(page, `aria-current="page"`) != 1 {
			t.Errorf("%s: %d current tabs, want 1", path, strings.Count(page, `aria-current="page"`))
		}
	}
	page := e.do("GET", "/", nil, true).Body.String()
	// The black mark and the wordmark; the white mark is gone with the
	// dark header.
	for _, want := range []string{`<a class="brand" href="/"><img class="mark" src="/static/portico-mark.svg?v=`, `<span class="wordmark">Portico</span>`} {
		if !strings.Contains(page, want) {
			t.Errorf("header lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "portico-mark-white") {
		t.Errorf("the white mark is still referenced:\n%s", page)
	}
}

func TestSkinLoginPageIsALockupAboveTheCard(t *testing.T) {
	e := newEnv(t, true)
	page := e.do("GET", "/login", nil, false).Body.String()
	for _, want := range []string{
		`<div class="lockup"><img class="mark-large" src="/static/portico-mark.svg?v=`,
		`<span class="wordmark">Portico</span>`,
		`<span class="claim">Admin console</span>`,
		`<section class="card narrow">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("login page lacks %q:\n%s", want, page)
		}
	}
}

func TestSkinPagesHaveAHeadingRowOutsideTheCard(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	page := e.do("GET", "/", nil, true).Body.String()
	for _, want := range []string{`<div class="page-head">`, `<h1>Tenants</h1>`, `<a class="button primary" href="/tenants/new">New tenant</a>`} {
		if !strings.Contains(page, want) {
			t.Errorf("tenants page lacks %q:\n%s", want, page)
		}
	}
}

func TestSkinShipsTheRedHatFonts(t *testing.T) {
	e := newEnv(t, true)
	for _, f := range []string{"/static/fonts/RedHatDisplayVF.woff2", "/static/fonts/RedHatTextVF.woff2"} {
		rec := e.do("GET", f, nil, false)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "font/woff2" || rec.Body.Len() < 10000 {
			t.Errorf("%s: %d %q %d bytes", f, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	}
	css := e.do("GET", "/static/portico.css", nil, false).Body.String()
	for _, want := range []string{
		`@font-face { font-family: "Red Hat Display"; font-weight: 400 700; src: url(fonts/RedHatDisplayVF.woff2) format("woff2-variations"); }`,
		`@font-face { font-family: "Red Hat Text"; font-weight: 400 700; src: url(fonts/RedHatTextVF.woff2) format("woff2-variations"); }`,
		"--accent: #a73422;",
		"--bg: #f6f4f0;",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
	if lic := e.do("GET", "/static/fonts/OFL.txt", nil, false); lic.Code != 200 || !strings.Contains(lic.Body.String(), "SIL Open Font License") {
		t.Errorf("font licence: %d", lic.Code)
	}
}

// The appliance's first login: admin / portico/4u, then the password page
// before anything else, then the UI and the API as usual.
func TestFactoryPasswordIsChangedAtTheFirstLogin(t *testing.T) {
	e := newEnv(t, false)
	api.BcryptCost = bcrypt.MinCost
	if _, err := api.EnsureFactoryPassword(context.Background(), e.st); err != nil {
		t.Fatal(err)
	}
	rec := e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {api.FactoryPassword}}, false)
	if rec.Code != 303 || rec.Header().Get("Location") != "/password" {
		t.Fatalf("login with the factory password: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			e.cookie = c
		}
	}
	if rec := e.do("GET", "/", nil, true); rec.Code != 303 || rec.Header().Get("Location") != "/password" {
		t.Fatalf("home before the change: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	page := e.do("GET", "/password", nil, true)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `name="new_password"`) || !strings.Contains(page.Body.String(), "factory password") {
		t.Fatalf("password page: %d\n%s", page.Code, page.Body.String())
	}
	csrf := csrfOf(t, page.Body.String())
	// Too short, then not matching, then the factory one again: refused.
	for _, f := range []url.Values{
		{"current": {api.FactoryPassword}, "new_password": {"short"}, "confirm": {"short"}},
		{"current": {api.FactoryPassword}, "new_password": {"Correct-Horse-9"}, "confirm": {"Other-Horse-9"}},
		{"current": {api.FactoryPassword}, "new_password": {api.FactoryPassword}, "confirm": {api.FactoryPassword}},
		{"current": {"wrong"}, "new_password": {"Correct-Horse-9"}, "confirm": {"Correct-Horse-9"}},
	} {
		f.Set("_csrf", csrf)
		if rec := e.do("POST", "/password", f, true); rec.Code != 200 || !strings.Contains(rec.Body.String(), "banner error") {
			t.Fatalf("bad change accepted: %v -> %d", f["new_password"], rec.Code)
		}
	}
	f := url.Values{"_csrf": {csrf}, "current": {api.FactoryPassword}, "new_password": {"Correct-Horse-9"}, "confirm": {"Correct-Horse-9"}}
	if rec := e.do("POST", "/password", f, true); rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Fatalf("change: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.do("GET", "/", nil, true); rec.Code != 200 {
		t.Fatalf("home after the change: %d", rec.Code)
	}
	if api.PasswordChangeRequired(context.Background(), e.st) {
		t.Fatal("change still required")
	}
	// The old password is gone, the new one works.
	if rec := e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {api.FactoryPassword}}, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Wrong user or password") {
		t.Fatalf("factory password still accepted: %d", rec.Code)
	}
	if rec := e.do("POST", "/login", url.Values{"user": {"admin"}, "password": {"Correct-Horse-9"}}, false); rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Fatalf("new password login: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
}

func csrfOf(t *testing.T, page string) string {
	t.Helper()
	m := reCSRF.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no CSRF token in the page:\n%s", page)
	}
	return m[1]
}
