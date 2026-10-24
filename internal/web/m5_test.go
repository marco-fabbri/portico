package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/api"
)

// doWith is do with extra cookies on the request.
func (e *env) doWith(method, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, "https://127.0.0.1:8443"+path, body)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, "https://127.0.0.1:8443"+path, nil)
	}
	for _, c := range cookies {
		if c != nil {
			r.AddCookie(c)
		}
	}
	r.RemoteAddr = "10.0.0.9:4000"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// The CSP is default-src 'self': an inline style attribute is refused by
// the browser, and so is htmx's own indicator <style> unless told not to
// write it.
func TestPagesHaveNoInlineStyle(t *testing.T) {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := fs.ReadFile(templatesFS, "templates/"+e.Name())
		if strings.Contains(string(b), `style="`) || strings.Contains(string(b), "<style") {
			t.Errorf("%s carries an inline style", e.Name())
		}
	}
	layout, _ := fs.ReadFile(templatesFS, "templates/layout.html")
	if !strings.Contains(string(layout), `<meta name="htmx-config" content='{"includeIndicatorStyles":false}'>`) {
		t.Error("layout.html does not turn htmx's indicator style off")
	}
}

// A template that fails half-way must not leave the browser with a 200
// and a torn page: the page is rendered into a buffer and a failure is a
// plain 500.
func TestTemplateErrorIsA500(t *testing.T) {
	e := newEnv(t, true)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://127.0.0.1:8443/password", nil)
	e.web.render(rec, r, http.StatusOK, "password", "Admin password", struct{}{}, "")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("template failure: %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(e.log.String(), `"msg":"template"`) {
		t.Fatalf("not logged: %s", e.log.String())
	}
}

// A notice is what the server has just done, never a text taken from the
// URL: it travels in a short-lived cookie and shows once.
func TestNoticeComesFromTheServerOnly(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, true)
	e.login(t)
	if body := e.do("GET", "/?notice=Call+us+now", nil, true).Body.String(); strings.Contains(body, "Call us now") || strings.Contains(body, "banner notice") {
		t.Fatalf("a text from the URL became a notice:\n%s", body)
	}
	rec := e.do("POST", "/setup/sites/site-a/save", e.form("code", "SITE-A", "name", "Milan", "order", "1"), true)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || loc != "/setup/sites/site-a" {
		t.Fatalf("save: %d %q", rec.Code, loc)
	}
	nc := cookieNamed(rec, noticeCookie)
	if nc == nil || !nc.HttpOnly || !nc.Secure || nc.SameSite != http.SameSiteStrictMode || nc.MaxAge != 60 {
		t.Fatalf("notice cookie = %+v", nc)
	}
	rec = e.doWith("GET", loc, nil, e.cookie, nc)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<div class="banner notice">Saved.</div>`) {
		t.Fatalf("page after the save: %d\n%s", rec.Code, rec.Body.String())
	}
	if c := cookieNamed(rec, noticeCookie); c == nil || c.MaxAge >= 0 {
		t.Fatalf("notice cookie not expired after showing: %+v", c)
	}
	if body := e.do("GET", loc, nil, true).Body.String(); strings.Contains(body, "banner notice") {
		t.Fatalf("notice shown twice:\n%s", body)
	}
}

// The new password must differ from the current one.
func TestPasswordCannotStayTheSame(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	rec := e.do("POST", "/password", e.form("current", adminPassword, "new_password", adminPassword, "confirm", adminPassword), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "the new password must differ from the current one") {
		t.Fatalf("same password: %d\n%s", rec.Code, rec.Body.String())
	}
}

// A password change ends every other admin session (the reason to change
// a password is usually that someone else may know it) and gives the
// changing browser a fresh one.
func TestPasswordChangeEndsTheOtherSessions(t *testing.T) {
	e := newEnv(t, true)
	e.login(t)
	other := e.cookie
	e.login(t)
	if other.Value == e.cookie.Value {
		t.Fatal("two logins, one session")
	}
	rec := e.do("POST", "/password", e.form("current", adminPassword, "new_password", "Correct-Horse-9", "confirm", "Correct-Horse-9"), true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/" {
		t.Fatalf("change: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	fresh := cookieNamed(rec, SessionCookie)
	if fresh == nil || fresh.Value == e.cookie.Value || fresh.MaxAge <= 0 {
		t.Fatalf("no fresh session for the changing browser: %+v", fresh)
	}
	if rec := e.doWith("GET", "/", nil, other); rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("the other session survived: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.doWith("GET", "/", nil, e.cookie); rec.Code != 303 {
		t.Fatalf("the old session of the changing browser survived: %d", rec.Code)
	}
	if rec := e.doWith("GET", "/", nil, fresh); rec.Code != 200 {
		t.Fatalf("the fresh session does not work: %d", rec.Code)
	}
}

// A POST that meets an expired session is sent to the login without a
// return path: after the login a GET of that path would be a 405.
func TestIdlePostLoginReturnsHome(t *testing.T) {
	e := newEnv(t, true)
	rec := e.do("POST", "/tenants/new", url.Values{"slug": {"x"}}, false)
	if rec.Code != 303 || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST without a session: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = e.do("GET", "/tenants/new", nil, false)
	if rec.Code != 303 || rec.Header().Get("Location") != "/login?next=%2Ftenants%2Fnew" {
		t.Fatalf("GET without a session: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	_ = api.FactoryPassword
}
