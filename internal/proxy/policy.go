package proxy

import (
	"net/http"
	"path"
	"regexp"
	"strings"
)

// Deny and allow rules for the public listener, measured on pc.7.6.0.6
// (docs/lab/2026-10-17-06-local-login-and-fallback.md and
// docs/lab/2026-10-17-08-tenant-ui-behaviour.md). Paths are matched on
// NormalizePath so spelling variants cannot slip past a rule.

var (
	reLocalLogin  = regexp.MustCompile(`^/api/iam/[^/]+/authn/login-requests/[^/]+/\$actions/validate-credentials$`)
	reLegacyLogin = regexp.MustCompile(`^/prismgateway/j_spring_security_check$`)
	reUserListing = regexp.MustCompile(`^/api/iam/[^/]+/authn/users$`)
)

// NormalizePath lower-cases p, drops a ";param" suffix from every segment,
// collapses repeated slashes and resolves "." and ".." segments. The result
// always starts with "/" and has no trailing slash except for the root. It
// is used for matching only; the request keeps its original path.
func NormalizePath(p string) string {
	p = strings.ToLower(p)
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if j := strings.IndexByte(s, ';'); j >= 0 {
			segs[i] = s[:j]
		}
	}
	p = path.Clean("/" + strings.Join(segs, "/"))
	return p
}

// PrismPolicy is the policy of the Prism Central host.
type PrismPolicy struct {
	AllowPrefixes []string
}

// Deny returns the first matching deny rule. It runs before the session
// check: a credential sent without a session is refused, not redirected.
func (p PrismPolicy) Deny(r *http.Request) (rule string, denied bool) {
	// Every Authorization value counts, and any whitespace separates the
	// scheme: the deny list must be broader than the upstream's parser.
	// The console never sends an Authorization header (write-up 12: 644
	// requests, none), so every scheme is refused: Basic by name, the
	// rest as deny-authorization. A tenant's browser has only cookies. An
	// empty value is present too: deny on presence, not on content.
	other := false
	for _, auth := range r.Header.Values("Authorization") {
		f := strings.Fields(auth)
		if len(f) == 0 {
			other = true
			continue
		}
		if strings.EqualFold(f[0], "basic") {
			return "deny-basic-auth", true
		}
		other = true
	}
	if other {
		return "deny-authorization", true
	}
	if _, present := r.Header["X-Ntnx-Api-Key"]; present {
		return "deny-api-key", true
	}
	np := NormalizePath(r.URL.Path)
	switch {
	case reLocalLogin.MatchString(np):
		return "deny-local-login", true
	case reLegacyLogin.MatchString(np):
		return "deny-legacy-login", true
	case r.Method == http.MethodGet && reUserListing.MatchString(np):
		return "deny-user-listing", true
	}
	return "", false
}

// Allowed reports whether the path is inside the allow-list. An entry ending
// in "/" matches that path (with or without the slash) and everything below
// it; any other entry matches exactly. The root "/" entry matches only "/".
func (p PrismPolicy) Allowed(r *http.Request) (rule string, allowed bool) {
	np := NormalizePath(r.URL.Path)
	for _, e := range p.AllowPrefixes {
		e = strings.ToLower(e)
		if e == "/" {
			if np == "/" {
				return "", true
			}
			continue
		}
		if strings.HasSuffix(e, "/") {
			base := strings.TrimSuffix(e, "/")
			if np == base || strings.HasPrefix(np, e) {
				return "", true
			}
			continue
		}
		if np == e {
			return "", true
		}
	}
	return "not-allowed", false
}

// IDPolicy is the policy of the identity host: only realm and static paths,
// never the master realm (and therefore never the admin console).
type IDPolicy struct{}

// Check applies the identity-host policy.
func (IDPolicy) Check(r *http.Request) (rule string, allowed bool) {
	np := NormalizePath(r.URL.Path)
	if np == "/realms/master" || strings.HasPrefix(np, "/realms/master/") {
		return "id-master-realm", false
	}
	if strings.HasPrefix(np, "/realms/") || strings.HasPrefix(np, "/resources/") {
		return "", true
	}
	return "id-not-allowed", false
}
