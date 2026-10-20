package proxy

import (
	"net/http"
	"strings"
	"time"

	"github.com/marco-fabbri/portico/internal/auth"
)

// stashPrefix names the cookies that keep a site's Prism Central cookies
// while the browser is on another site (design sites §4.4). Site ids
// have no dot, so "__Host-pstash.<site-id>.<cookie-name>" parses without
// ambiguity; the __Host- prefix makes the browser require Secure, Path=/
// and no Domain.
const stashPrefix = "__Host-pstash."

// StashName is the stash cookie of a site's cookie.
func StashName(siteID, name string) string { return stashPrefix + siteID + "." + name }

// parseStash returns the site and the cookie name of a stash cookie.
func parseStash(name string) (site, cookie string, ok bool) {
	rest, ok := strings.CutPrefix(name, stashPrefix)
	if !ok {
		return "", "", false
	}
	site, cookie, ok = strings.Cut(rest, ".")
	return site, cookie, ok && site != "" && cookie != ""
}

// CookieAttrs is how a Prism Central cookie is restored.
type CookieAttrs struct{ HttpOnly bool }

// prismCookieAttrs lists the Prism Central cookies the console's
// JavaScript reads, which must come back without HttpOnly; any other
// cookie is restored HttpOnly. Decision D1 of write-up 20; empty until
// it is measured.
var prismCookieAttrs = map[string]CookieAttrs{}

// clearStorageOnSwitch sends Clear-Site-Data "storage" with a switch,
// when the console keeps site-specific data in browser storage.
// Decision D2 of write-up 20; off until it is measured.
const clearStorageOnSwitch = false

func attrsOf(name string) CookieAttrs {
	if a, ok := prismCookieAttrs[name]; ok {
		return a
	}
	return CookieAttrs{HttpOnly: true}
}

func isPorticoCookie(name string) bool {
	for _, p := range auth.PorticoCookies() {
		if name == p {
			return true
		}
	}
	return false
}

// switchSiteCookies moves the browser from site from to site to in one
// response: the live Prism Central cookies go into from's stash (kept for
// maxAge) and leave their names; to's stash comes back under the real
// names. A first selection (from empty) stashes nothing. The expiries
// are written before the restores, so a restored name wins in the browser.
func switchSiteCookies(w http.ResponseWriter, r *http.Request, from, to string, maxAge time.Duration) {
	var restore []*http.Cookie
	for _, c := range r.Cookies() {
		if isPorticoCookie(c.Name) {
			continue
		}
		if site, name, ok := parseStash(c.Name); ok {
			if site == to {
				restore = append(restore, &http.Cookie{Name: name, Value: c.Value})
				http.SetCookie(w, &http.Cookie{Name: c.Name, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
			}
			continue
		}
		if from == "" || from == to {
			continue
		}
		http.SetCookie(w, &http.Cookie{Name: StashName(from, c.Name), Value: c.Value, Path: "/", MaxAge: int(maxAge.Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.SetCookie(w, &http.Cookie{Name: c.Name, Value: "", Path: "/", MaxAge: -1, Secure: true})
	}
	for _, c := range restore {
		http.SetCookie(w, &http.Cookie{Name: c.Name, Value: c.Value, Path: "/", Secure: true, HttpOnly: attrsOf(c.Name).HttpOnly, SameSite: http.SameSiteLaxMode})
	}
	if clearStorageOnSwitch && from != "" && from != to {
		w.Header().Set("Clear-Site-Data", `"storage"`)
	}
}
