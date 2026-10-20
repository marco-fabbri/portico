package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/auth"
)

// cookieJar applies Set-Cookie headers the way a browser would for one host.
type cookieJar map[string]string

func (j cookieJar) apply(rec *httptest.ResponseRecorder) {
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(j, c.Name)
			continue
		}
		j[c.Name] = c.Value
	}
}

func (j cookieJar) header() string {
	var parts []string
	for k, v := range j {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

func (h *sitesHarness) doJar(method, path string, jar cookieJar, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://prism.test"+path, strings.NewReader(form.Encode()))
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.Header.Set("Accept", "text/html")
	r.Header.Set("Cookie", jar.header())
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r)
	jar.apply(rec)
	return rec
}

func TestSwitchStashesAndRestores(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "acme", "")
	jar := cookieJar{auth.SessionCookie: "s1"}
	h.doJar("POST", "/.portico/sites/select", jar, url.Values{"site": {"site-a"}})
	// Site A's Prism Central sets its session cookies.
	jar["NTNX_IGW_SESSION"], jar["iam-sessionid"] = "a-token", "a-login"
	h.doJar("POST", "/.portico/sites/select", jar, url.Values{"site": {"site-b"}})
	if _, ok := jar["NTNX_IGW_SESSION"]; ok {
		t.Fatalf("site A's cookie still live on site B: %v", jar)
	}
	if jar[StashName("site-a", "NTNX_IGW_SESSION")] != "a-token" || jar[StashName("site-a", "iam-sessionid")] != "a-login" {
		t.Fatalf("site A's cookies not stashed: %v", jar)
	}
	jar["NTNX_IGW_SESSION"] = "b-token"
	h.doJar("POST", "/.portico/sites/select", jar, url.Values{"site": {"site-a"}})
	if jar["NTNX_IGW_SESSION"] != "a-token" || jar["iam-sessionid"] != "a-login" || jar[StashName("site-b", "NTNX_IGW_SESSION")] != "b-token" {
		t.Fatalf("back on A: %v", jar)
	}
	if _, ok := jar[StashName("site-a", "NTNX_IGW_SESSION")]; ok {
		t.Fatalf("A's stash kept after the restore: %v", jar)
	}
	if jar[auth.SessionCookie] != "s1" {
		t.Fatal("the Portico session cookie was touched")
	}
}

// Review Focus 3: no stash cookie and no Portico cookie ever reaches a
// Prism Central, so site A never sees site B's session.
func TestStashNeverReachesUpstream(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "acme", "")
	jar := cookieJar{auth.SessionCookie: "s1", auth.OIDCCookie: "x", auth.AdminCookie: "y"}
	h.doJar("POST", "/.portico/sites/select", jar, url.Values{"site": {"site-a"}})
	jar["NTNX_IGW_SESSION"] = "a-token"
	h.doJar("POST", "/.portico/sites/select", jar, url.Values{"site": {"site-b"}})
	jar["NTNX_IGW_SESSION"] = "b-token"
	h.doJar("GET", "/console/", jar, nil)
	h.doJar("POST", "/.portico/sites/select", jar, url.Values{"site": {"site-a"}})
	h.doJar("GET", "/console/", jar, nil)
	for name, got := range map[string]*[]recorded{"a": h.a, "b": h.b} {
		if len(*got) == 0 {
			t.Fatalf("site %s got no request", name)
		}
		for _, r := range *got {
			c := r.Header.Get("Cookie")
			for _, banned := range []string{"__Host-pstash.", auth.SessionCookie, auth.OIDCCookie, auth.AdminCookie} {
				if strings.Contains(c, banned) {
					t.Fatalf("site %s received %q in %q", name, banned, c)
				}
			}
			if name == "a" && strings.Contains(c, "b-token") || name == "b" && strings.Contains(c, "a-token") {
				t.Fatalf("site %s received the other site's session: %q", name, c)
			}
		}
	}
}

func TestSiteIDsWithDashesDoNotCollide(t *testing.T) {
	jar := map[string]string{StashName("az-it-mi1", "x"): "1", StashName("az-it-mi1-b", "x"): "2"}
	r := httptest.NewRequest("GET", "/", nil)
	for k, v := range jar {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	rec := httptest.NewRecorder()
	switchSiteCookies(rec, r, "", "az-it-mi1", time.Hour)
	got := cookieJar{}
	for k, v := range jar {
		got[k] = v
	}
	got.apply(rec)
	if got["x"] != "1" || got[StashName("az-it-mi1-b", "x")] != "2" {
		t.Fatalf("restore of az-it-mi1 = %v", got)
	}
}

func TestFirstSelectionStashesNothing(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "acme", "")
	jar := cookieJar{auth.SessionCookie: "s1", "leftover": "x"}
	rec := h.doJar("POST", "/.portico/sites/select", jar, url.Values{"site": {"site-a"}})
	for _, c := range rec.Result().Cookies() {
		if strings.HasPrefix(c.Name, "__Host-pstash.") {
			t.Fatalf("first selection stashed %s", c.Name)
		}
	}
}

func TestRestoredAttributesFollowTable(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: StashName("b", "NTNX_IGW_SESSION"), Value: "t"})
	r.AddCookie(&http.Cookie{Name: StashName("b", "some-js-cookie"), Value: "v"})
	rec := httptest.NewRecorder()
	old := prismCookieAttrs
	prismCookieAttrs = map[string]CookieAttrs{"some-js-cookie": {HttpOnly: false}}
	t.Cleanup(func() { prismCookieAttrs = old })
	switchSiteCookies(rec, r, "a", "b", time.Hour)
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case "NTNX_IGW_SESSION":
			if !c.HttpOnly || !c.Secure || c.Path != "/" {
				t.Fatalf("unknown cookie restored without HttpOnly: %+v", c)
			}
		case "some-js-cookie":
			if c.HttpOnly || !c.Secure {
				t.Fatalf("a cookie the console reads restored HttpOnly: %+v", c)
			}
		}
	}
}

func TestClearSiteDataFollowsD2(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	switchSiteCookies(rec, r, "a", "b", time.Hour)
	got := rec.Header().Get("Clear-Site-Data")
	if clearStorageOnSwitch && got != `"storage"` || !clearStorageOnSwitch && got != "" {
		t.Fatalf("Clear-Site-Data = %q with clearStorageOnSwitch=%v", got, clearStorageOnSwitch)
	}
}
