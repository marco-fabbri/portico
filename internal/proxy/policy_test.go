package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marco-fabbri/portico/internal/config"
)

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/API/IAM//v4.1.b3/./authn/login-requests/x/$actions/validate-credentials/": "/api/iam/v4.1.b3/authn/login-requests/x/$actions/validate-credentials",
		"/PrismGateway/j_spring_security_check;jsessionid=1":                        "/prismgateway/j_spring_security_check",
		"/console/../api/x": "/api/x",
		"":                  "/",
		"/":                 "/",
	}
	for in, want := range cases {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func req(method, path string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(method, "https://prism.test"+path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func withSecondAuth(r *http.Request, v string) *http.Request {
	r.Header.Add("Authorization", v)
	return r
}

func TestPrismPolicyDeny(t *testing.T) {
	p := PrismPolicy{AllowPrefixes: config.DefaultAllowPrefixes}
	vc := "/api/iam/v4.1.b3/authn/login-requests/abc/$actions/validate-credentials"
	cases := []struct {
		name   string
		r      *http.Request
		rule   string
		denied bool
	}{
		{"basic", req("GET", "/api/x", map[string]string{"Authorization": "Basic abc"}), "deny-basic-auth", true},
		{"basic lower", req("GET", "/api/x", map[string]string{"Authorization": "basic abc"}), "deny-basic-auth", true},
		{"basic padded", req("GET", "/api/x", map[string]string{"Authorization": " BASIC abc"}), "deny-basic-auth", true},
		{"bearer", req("GET", "/api/x", map[string]string{"Authorization": "Bearer abc"}), "deny-authorization", true},
		{"digest", req("GET", "/api/x", map[string]string{"Authorization": "Digest abc"}), "deny-authorization", true},
		{"unknown scheme", req("GET", "/api/x", map[string]string{"Authorization": "X-Token abc"}), "deny-authorization", true},
		{"empty authorization", req("GET", "/api/x", map[string]string{"Authorization": ""}), "deny-authorization", true},
		{"basic tab", req("GET", "/api/x", map[string]string{"Authorization": "Basic\tabc"}), "deny-basic-auth", true},
		{"basic second value", withSecondAuth(req("GET", "/api/x", map[string]string{"Authorization": "Bearer abc"}), "Basic abc"), "deny-basic-auth", true},
		{"api key empty", req("GET", "/api/x", map[string]string{"X-ntnx-api-key": ""}), "deny-api-key", true},
		{"api key", req("GET", "/api/x", map[string]string{"X-NTNX-API-KEY": "k"}), "deny-api-key", true},
		{"validate POST", req("POST", vc, nil), "deny-local-login", true},
		{"validate encoded slash in id", req("POST", "/api/iam/v4.1.b3/authn/login-requests/a%2Fb/$actions/validate-credentials", nil), "deny-encoded-path", true},
		{"validate encoded dot-dot", req("POST", vc+"%2F..", nil), "deny-encoded-path", true},
		{"validate trailing nul", req("POST", vc+"%00", nil), "deny-encoded-path", true},
		{"validate trailing space", req("POST", vc+"%20", nil), "deny-encoded-path", true},
		{"percent itself", req("GET", "/console/%25", nil), "deny-encoded-path", true},
		{"plain console path", req("GET", "/console/app.js", nil), "", false},
		{"query percent", req("GET", "/console/?x=%2F", nil), "", false},
		{"validate PUT", req("PUT", vc, nil), "deny-local-login", true},
		{"validate encoded", req("GET", "/api/iam/v4.1.b3/authn/login-requests/abc/%24actions/validate-credentials", nil), "deny-local-login", true},
		{"validate doubled slash", req("POST", "/api/iam//v4.1.b3/authn/login-requests/abc/$actions/validate-credentials/", nil), "deny-local-login", true},
		{"validate upper", req("POST", "/API/IAM/v4.1.b3/authn/LOGIN-REQUESTS/abc/$actions/validate-credentials", nil), "deny-local-login", true},
		{"legacy", req("POST", "/PrismGateway/j_spring_security_check", nil), "deny-legacy-login", true},
		{"legacy jsessionid", req("POST", "/PrismGateway/j_spring_security_check;jsessionid=x", nil), "deny-legacy-login", true},
		{"users list", req("GET", "/api/iam/v4.0/authn/users", nil), "deny-user-listing", true},
		{"users me", req("GET", "/api/iam/v4.0/authn/users/me", nil), "", false},
		{"users post", req("POST", "/api/iam/v4.0/authn/users", nil), "", false},
		{"login request get", req("GET", "/api/iam/v4.1.b3/authn/login-requests/abc", nil), "", false},
	}
	for _, c := range cases {
		rule, denied := p.Deny(c.r)
		if denied != c.denied || rule != c.rule {
			t.Errorf("%s: Deny = (%q, %v), want (%q, %v)", c.name, rule, denied, c.rule, c.denied)
		}
	}
}

func TestPrismPolicyAllowed(t *testing.T) {
	p := PrismPolicy{AllowPrefixes: config.DefaultAllowPrefixes}
	cases := map[string]bool{
		"/":                                      true,
		"/console":                               true,
		"/console/":                              true,
		"/console/app/x.js":                      true,
		"/auth_code":                             true,
		"/auth_code/x":                           false,
		"/dm/infrastructure/vms":                 true,
		"/vnc/vm/x/proxy":                        true,
		"/PrismGateway/services/rest/v1/cluster": true,
		"/PrismGateway/nmb/login":                false,
		"/.portico/login":                        false,
		"/index.html":                            false,
		"/favicon.ico":                           true,
		"/redirect":                              true, // Prism Central's own Sign Out goes through /redirect?logout=...
		"/redirect/x":                            false,
	}
	for path, want := range cases {
		rule, got := p.Allowed(req("GET", path, nil))
		if got != want {
			t.Errorf("Allowed(%q) = %v, want %v", path, got, want)
		}
		if !got && rule != "not-allowed" {
			t.Errorf("Allowed(%q) rule = %q, want not-allowed", path, rule)
		}
	}
}

func TestIDPolicyCheck(t *testing.T) {
	cases := []struct {
		path    string
		rule    string
		allowed bool
	}{
		{"/realms/portico/protocol/openid-connect/auth", "", true},
		{"/resources/abc/login/keycloak/css/x.css", "", true},
		{"/realms/master", "id-master-realm", false},
		{"/realms/master/protocol/openid-connect/token", "id-master-realm", false},
		{"/realms/master-2/x", "", true},
		{"/admin/master/console/", "id-not-allowed", false},
		{"/", "id-not-allowed", false},
		{"/metrics", "id-not-allowed", false},
	}
	for _, c := range cases {
		rule, allowed := IDPolicy{}.Check(req("GET", c.path, nil))
		if allowed != c.allowed || rule != c.rule {
			t.Errorf("Check(%q) = (%q, %v), want (%q, %v)", c.path, rule, allowed, c.rule, c.allowed)
		}
	}
}

// A header that is present but holds no scheme is still a credential
// attempt: "every header" (docs) means presence, not content.
func TestEmptyAuthorizationIsDenied(t *testing.T) {
	for _, v := range []string{"", "   "} {
		r := httptest.NewRequest("GET", "/console/", nil)
		r.Header["Authorization"] = []string{v}
		if rule, denied := (PrismPolicy{}).Deny(r); !denied || rule != "deny-authorization" {
			t.Fatalf("%q: rule %q denied %v", v, rule, denied)
		}
	}
}
