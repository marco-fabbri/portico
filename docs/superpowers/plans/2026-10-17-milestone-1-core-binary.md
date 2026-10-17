# Milestone 1: Core Binary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single Go binary, `portico serve`, that puts Prism Central and
Keycloak behind two public host names, lets a browser reach Prism Central
only after a Keycloak login, answers 403 to every local-login path measured
in write-up 06, and offers an internal raw pass-through for administrators.

**Architecture:** One `net/http` process with three listeners. The public
listener routes by `Host`: the Prism Central host runs deny list → Portico
session (OIDC authorization code flow against Keycloak) → prefix allow-list →
reverse proxy to port 9440; the identity host proxies to Keycloak on the
loopback with only the realm and static paths allowed. The internal
listeners are a raw pass-through on 9440 and a health endpoint on 8443.
Policy is code in `internal/proxy`, sessions and OIDC in `internal/auth`,
settings in a YAML file until Milestone 2 moves them into SQLite behind the
setup wizard. Portico's own endpoints live under the reserved prefix
`/.portico/` on the Prism Central host.

**Tech Stack:** Go 1.27 (standard library `net/http`, `net/http/httputil`,
`log/slog`, `crypto/tls`), `gopkg.in/yaml.v3`, `github.com/coreos/go-oidc/v3`,
`golang.org/x/oauth2`. Lab: Keycloak 26.3 and the `portico-lab` VM from
Milestone 0, deployed as a systemd service that replaces the nginx stand-in.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md`, sections
4.1–4.3, 4.6 (TLS), 5, 6.2, 6.4, 6.5, and milestone 1 in section 9. ADR
0002 (proxy in the Go binary). Evidence: `docs/lab/2026-10-17-06-local-login-and-fallback.md`,
`docs/lab/2026-10-17-08-tenant-ui-behaviour.md`, `docs/lab/2026-10-17-04-saml-hostname.md`,
`lab/vm/nginx.conf` (reference behaviour).

## Global Constraints

- All text on disk in English (CLAUDE.md). Commit messages in English.
- Work on branch `m1-core-binary` from `main`; integrate through a pull
  request (Marco's delegation of 2026-10-17: PRs merged by the executor).
- Public path fails closed: no Portico session, no request reaches Prism
  Central (spec §5). The deny list of spec §4.3 is evaluated before the
  session check, so a credential sent without a session also gets 403.
- Never log the value of `Authorization`, `X-ntnx-api-key`, `Cookie`,
  `Set-Cookie`, or any query string (write-up 03, CORRECTED note). Access
  logs carry method, host, path, status, duration, rule name.
- Secrets reach the binary only through files with mode 0600 (client secret,
  TLS key). No secret in the YAML file, in flags, or in environment variables.
- Supported Prism Central: pc.7.6 and later; deny-list paths as measured on
  pc.7.6.0.6 (write-up 06), allow-list prefixes as measured (write-up 08).
- Session cookie: `__Host-portico_session`, `Secure`, `HttpOnly`,
  `SameSite=Lax`, `Path=/`; absolute lifetime 8 h, idle 15 min (aligned with
  Prism Central's own limits, spec §3).
- The two public host names must share a registrable domain (e.g.
  `prism.msp.example` and `id.msp.example`): the SAML response is a top-level
  POST from the identity host to the Prism Central host and a `SameSite=Lax`
  cookie travels only on same-site requests. Documented in the design by
  Task 9.
- Lab: Prism Central `https://10.23.210.175:9440`, lab VM `10.23.210.190`,
  hosts `prism.lab.portico.test` and `id.lab.portico.test`, lab CA and keys
  in `~/portico-secrets/`. Lab-only choices (TLS verification off towards
  9440, lab certificate on the internal listeners, `sudo` without password)
  stay in `lab/`, never in `internal/` or `deploy/`.
- `go vet ./...` and `gofmt -l .` are clean at every commit; `go test ./...`
  is the suite.

## Review Focus

Failure modes the spec implies but a single task's tests might miss; each
has a test in the task named:

1. A deny-listed path reached through a variant spelling:
   `%24actions`, doubled slashes, `/./` segments, upper-case letters, a
   trailing slash, a `;` parameter. Expected: still 403 (Task 2).
2. A request with credentials and no session: expected 403 from the deny
   list, not a redirect to Keycloak, and the upstream sees zero requests
   (Task 6).
3. The access log after a request carrying Basic credentials, an API key,
   a cookie and a query string: expected none of the values in the log
   output (Task 6).
4. The `rd` return parameter of `/.portico/login` set to an absolute URL,
   a protocol-relative `//host` or a non-path: expected the login ends on
   `/` of the same host, never elsewhere (Task 4).
5. Keycloak unreachable: expected `/.portico/login` answers 503 with a
   plain message, deny rules still answer 403, health stays 200, and the
   process does not exit (Tasks 4 and 7).

---

### Task 1: Configuration and binary skeleton

**Files:**
- Create: `cmd/portico/main.go`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `internal/config/testdata/minimal.yaml`
- Create: `deploy/portico.yaml.example`
- Modify: `go.mod` (add `gopkg.in/yaml.v3`)

**Interfaces:**
- Produces:

```go
package config

type TLS struct { CertFile string `yaml:"cert_file"`; KeyFile string `yaml:"key_file"` }
type Public struct {
    Listen    string `yaml:"listen"`      // default ":443"
    TLS       TLS    `yaml:"tls"`         // both empty = plain HTTP (tunnel mode)
    PrismHost string `yaml:"prism_host"`  // required
    IDHost    string `yaml:"id_host"`     // required
}
type Internal struct {
    AdminListen       string `yaml:"admin_listen"`       // default "127.0.0.1:8443"
    PassthroughListen string `yaml:"passthrough_listen"` // default "127.0.0.1:9440"
    TLS               TLS    `yaml:"tls"`
}
type PrismCentral struct {
    URL       string `yaml:"url"`        // required, https://host:9440
    TLSVerify bool   `yaml:"tls_verify"` // default true
    CAFile    string `yaml:"ca_file"`
}
type Keycloak struct { URL string `yaml:"url"` } // backchannel, default "http://127.0.0.1:8080"
type OIDC struct {
    Issuer           string `yaml:"issuer"`             // required, public issuer URL
    ClientID         string `yaml:"client_id"`          // required
    ClientSecretFile string `yaml:"client_secret_file"` // required, mode 0600
}
type Session struct { MaxAge time.Duration `yaml:"max_age"`; Idle time.Duration `yaml:"idle"` } // defaults 8h, 15m
type Policy struct { AllowPrefixes []string `yaml:"allow_prefixes"` } // default DefaultAllowPrefixes
type Log struct { Level string `yaml:"level"` } // default "info"
type Config struct {
    Public Public; Internal Internal; PrismCentral PrismCentral `yaml:"prism_central"`
    Keycloak Keycloak; OIDC OIDC; Session Session; Policy Policy; Log Log
}

var DefaultAllowPrefixes = []string{"/", "/console/", "/static/", "/ui/iam/",
    "/api/", "/dm/", "/PrismGateway/services/rest/v1/", "/vnc/", "/auth_code"}

func Default() Config
func Load(path string) (*Config, error) // defaults, then YAML, then Validate
func (c *Config) Validate() error        // one error listing every missing or inconsistent field
func (c *Config) ClientSecret() (string, error) // reads OIDC.ClientSecretFile, trims one trailing newline, refuses mode wider than 0600
```

- [ ] **Step 1: Write the failing tests**

```go
func TestLoadAppliesDefaults(t *testing.T)        // minimal.yaml → Listen ":443", Keycloak.URL loopback, Session 8h/15m, AllowPrefixes == DefaultAllowPrefixes, TLSVerify true
func TestValidateListsEveryMissingField(t *testing.T) // empty Config → error mentions prism_host, id_host, prism_central.url, oidc.issuer, oidc.client_id, oidc.client_secret_file
func TestValidateRejectsHalfTLS(t *testing.T)     // cert_file set, key_file empty → error contains "tls"
func TestClientSecretRefusesWorldReadable(t *testing.T) // temp file 0644 → error contains "mode"
func TestClientSecretTrimsNewline(t *testing.T)   // "s3cret\n" in a 0600 file → "s3cret"
```

`minimal.yaml` holds only the six required fields with lab-like values
(`prism.example.test`, `id.example.test`, `https://pc.example.test:9440`,
`https://id.example.test/realms/portico`, `portico`, `/nonexistent`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -v`
Expected: FAIL, build error `undefined: config.Load` (package has no code yet)

- [ ] **Step 3: Implement `internal/config/config.go`**

`go get gopkg.in/yaml.v3`. `Load` reads the file, starts from `Default()`,
`yaml.Unmarshal` into it (yaml.v3 decodes `"8h"` into `time.Duration`),
then `Validate`. Validation errors are joined with `errors.Join` so one run
shows all of them.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS, 5 tests

- [ ] **Step 5: Write `cmd/portico/main.go` and `deploy/portico.yaml.example`**

`main.go`: subcommands `serve -config PATH` (loads config, reads the
client secret file, prints `portico: configuration ok` and exits 0 for now;
Task 7 wires the servers)
and `version`. Unknown subcommand → usage on stderr, exit 2. `version` prints
`portico dev` (release builds set it with `-ldflags -X main.version=`).

`deploy/portico.yaml.example`: every key with its default and a one-line
comment, required ones marked `# required`.

- [ ] **Step 6: Verify the binary and the example**

Run: `go build ./... && go vet ./... && gofmt -l . && go run ./cmd/portico serve -config deploy/portico.yaml.example; echo rc=$?`
Expected: no build or vet output; `gofmt -l` prints nothing; the run ends
with `rc=1` and an error naming `oidc.client_secret_file` (the example
points to a file that does not exist on the Mac). Then
`go run ./cmd/portico version` prints `portico dev`.

- [ ] **Step 7: Commit**

```bash
git add cmd internal/config deploy go.mod go.sum
git commit -m "config: YAML settings with defaults and validation; portico serve/version skeleton"
```

---

### Task 2: Request policy for the two public hosts

**Files:**
- Create: `internal/proxy/policy.go`
- Create: `internal/proxy/policy_test.go`

**Interfaces:**
- Consumes: `config.DefaultAllowPrefixes` (Task 1).
- Produces:

```go
package proxy

// NormalizePath lower-cases the path, decodes nothing further (r.URL.Path is
// already decoded), applies path.Clean, collapses repeated slashes and strips
// a ";param" suffix from every segment. Used only for matching.
func NormalizePath(p string) string

type PrismPolicy struct { AllowPrefixes []string }

// Deny returns the name of the first deny rule that matches, before any
// session check. Rule names: "deny-basic-auth", "deny-api-key",
// "deny-local-login", "deny-legacy-login", "deny-user-listing".
func (p PrismPolicy) Deny(r *http.Request) (rule string, denied bool)

// Allowed reports whether the path is inside the allow-list. An entry ending
// in "/" matches the path itself without the slash and everything below it;
// any other entry matches exactly. Rule name on refusal: "not-allowed".
func (p PrismPolicy) Allowed(r *http.Request) (rule string, allowed bool)

type IDPolicy struct{}

// Check allows only "/realms/<realm>/..." and "/resources/..." and refuses
// "/realms/master" and everything below it. Rule names: "id-not-allowed",
// "id-master-realm".
func (IDPolicy) Check(r *http.Request) (rule string, allowed bool)
```

Deny rules (write-up 06, spec §4.3), matched on `NormalizePath`:

| rule | match |
|---|---|
| `deny-basic-auth` | `Authorization` header whose scheme, trimmed and case-folded, is `basic` |
| `deny-api-key` | `X-ntnx-api-key` header present, even empty |
| `deny-local-login` | any method, `^/api/iam/[^/]+/authn/login-requests/[^/]+/\$actions/validate-credentials$` |
| `deny-legacy-login` | any method, `^/prismgateway/j_spring_security_check$` |
| `deny-user-listing` | `GET`, `^/api/iam/[^/]+/authn/users$` |

- [ ] **Step 1: Write the failing tests**

Table-driven, one table per method:

```go
func TestNormalizePath(t *testing.T)
// "/API/IAM//v4.1.b3/./authn/login-requests/x/$actions/validate-credentials/" → "/api/iam/v4.1.b3/authn/login-requests/x/$actions/validate-credentials"
// "/PrismGateway/j_spring_security_check;jsessionid=1" → "/prismgateway/j_spring_security_check"

func TestPrismPolicyDeny(t *testing.T)
// Basic header "Basic abc" → deny-basic-auth; "basic abc" → deny-basic-auth; " BASIC abc" → deny-basic-auth
// "Bearer abc" → not denied (logged elsewhere)
// X-ntnx-api-key "" → deny-api-key
// POST /api/iam/v4.1.b3/authn/login-requests/abc/$actions/validate-credentials → deny-local-login
// PUT same path → deny-local-login; GET same path with /%24actions/ (decoded by net/url to "$") → deny-local-login
// POST /api/iam//v4.1.b3/authn/login-requests/abc/$actions/validate-credentials/ → deny-local-login
// POST /API/IAM/v4.1.b3/authn/LOGIN-REQUESTS/abc/$actions/validate-credentials → deny-local-login
// POST /PrismGateway/j_spring_security_check → deny-legacy-login; with ";jsessionid=x" → deny-legacy-login
// GET /api/iam/v4.0/authn/users → deny-user-listing; GET /api/iam/v4.0/authn/users/me → not denied; POST /api/iam/v4.0/authn/users → not denied
// GET /api/iam/v4.1.b3/authn/login-requests/abc (provider selection) → not denied

func TestPrismPolicyAllowed(t *testing.T)   // with config.DefaultAllowPrefixes
// "/" → allowed; "/console" and "/console/" and "/console/app/x.js" → allowed
// "/auth_code" → allowed; "/auth_code/x" → not-allowed
// "/dm/infrastructure/vms" → allowed; "/vnc/vm/x/proxy" → allowed
// "/PrismGateway/services/rest/v1/cluster" → allowed; "/PrismGateway/nmb/login" → not-allowed
// "/.portico/login" → not-allowed (handled before policy, never proxied)
// "/index.html" → not-allowed

func TestIDPolicyCheck(t *testing.T)
// "/realms/portico-lab/protocol/openid-connect/auth" → allowed
// "/resources/abc/login/keycloak/css/x.css" → allowed
// "/realms/master" → id-master-realm; "/realms/master/protocol/openid-connect/token" → id-master-realm
// "/realms/master-2/x" → allowed (segment match, not prefix)
// "/admin/master/console/" → id-not-allowed; "/" → id-not-allowed; "/metrics" → id-not-allowed
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/proxy/ -run 'TestNormalizePath|TestPrismPolicy|TestIDPolicy' -v`
Expected: FAIL, `undefined: NormalizePath`

- [ ] **Step 3: Implement `internal/proxy/policy.go`**

Regular expressions compiled once at package level. `Deny` checks headers
first, paths second. `Allowed` and `Check` match on the normalized path.
Segment matching for `/realms/master`: equal to it or has the prefix
`/realms/master/`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/proxy/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/policy.go internal/proxy/policy_test.go
git commit -m "proxy: deny list and allow-lists measured on pc.7.6"
```

---

### Task 3: Session store and cookies

**Files:**
- Create: `internal/auth/session.go`
- Create: `internal/auth/session_test.go`

**Interfaces:**
- Produces:

```go
package auth

const SessionCookie = "__Host-portico_session"
const OIDCCookie = "__Host-portico_oidc"

type Session struct {
    ID        string
    Subject   string // OIDC "sub"
    Email     string
    CreatedAt time.Time
    LastSeen  time.Time
}

// Store is implemented in memory now and in SQLite in Milestone 2.
type Store interface {
    Create(s Session)
    // Get returns the session if it exists and is neither past MaxAge since
    // CreatedAt nor past Idle since LastSeen; on success it sets LastSeen=now.
    Get(id string, now time.Time) (Session, bool)
    Delete(id string)
}

func NewMemoryStore(maxAge, idle time.Duration) *MemoryStore // mutex-guarded map; Sweep(now) drops expired entries
func (m *MemoryStore) Sweep(now time.Time) int

func NewSessionID() string // 32 random bytes, base64url without padding
func SetSessionCookie(w http.ResponseWriter, id string, maxAge time.Duration)
func ClearSessionCookie(w http.ResponseWriter)
func SessionFromRequest(st Store, r *http.Request, now time.Time) (Session, bool)
```

- [ ] **Step 1: Write the failing tests**

```go
func TestMemoryStoreIdleExpiry(t *testing.T)     // idle 15m: Get at +14m ok and LastSeen moves; Get at +14m+15m ok; Get at +45m with no touch in between → false
func TestMemoryStoreMaxAge(t *testing.T)         // maxAge 8h, touched every 10m: Get at 8h+1s → false
func TestMemoryStoreSweep(t *testing.T)          // 3 sessions, 2 expired → Sweep returns 2, Get of the live one still true
func TestSessionIDIsRandomAndLong(t *testing.T)  // two IDs differ, len ≥ 43, only base64url characters
func TestSessionCookieAttributes(t *testing.T)   // Name __Host-portico_session, Secure, HttpOnly, SameSite=Lax, Path "/", no Domain, MaxAge == 8h in seconds
func TestClearSessionCookie(t *testing.T)        // MaxAge -1, empty value
func TestSessionFromRequest(t *testing.T)        // no cookie → false; unknown id → false; known id → session
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/auth/ -v`
Expected: FAIL, `undefined: NewMemoryStore`

- [ ] **Step 3: Implement `internal/auth/session.go`**

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS, 7 tests

- [ ] **Step 5: Commit**

```bash
git add internal/auth/session.go internal/auth/session_test.go
git commit -m "auth: in-memory session store and __Host- session cookie"
```

---

### Task 4: OIDC login, callback, logout and the session middleware

**Files:**
- Create: `internal/auth/oidc.go`
- Create: `internal/auth/handlers.go`
- Create: `internal/auth/fakeidp_test.go`
- Create: `internal/auth/oidc_test.go`
- Modify: `go.mod` (add `github.com/coreos/go-oidc/v3`, `golang.org/x/oauth2`)

**Interfaces:**
- Consumes: `Store`, `Session`, cookie helpers (Task 3).
- Produces:

```go
package auth

type OIDCConfig struct {
    Issuer       string   // public issuer, must equal the "issuer" of the discovery document
    DiscoveryURL string   // backchannel base, e.g. http://127.0.0.1:8080/realms/portico-lab
    ClientID     string
    ClientSecret string
    RedirectURL  string   // https://<prism host>/.portico/callback
    Scopes       []string // default ["openid", "email"]
}

// Discover fetches <DiscoveryURL>/.well-known/openid-configuration, refuses a
// document whose issuer differs from cfg.Issuer, and builds the provider from
// the document's endpoints (go-oidc ProviderConfig) so the browser is sent to
// the public authorization endpoint while Portico talks to the token and JWKS
// endpoints the document lists (loopback when Keycloak's backchannel is dynamic).
func Discover(ctx context.Context, client *http.Client, cfg OIDCConfig) (*oidc.Provider, error)

type Authenticator struct { /* cfg, store, httpClient, logger, lazily discovered provider */ }
func NewAuthenticator(cfg OIDCConfig, st Store, client *http.Client, log *slog.Logger) *Authenticator

// Provider runs Discover on first use and caches success; a failure is
// returned to the caller and retried on the next call (Keycloak may start
// after Portico).
func (a *Authenticator) Provider(ctx context.Context) (*oidc.Provider, error)

func (a *Authenticator) Login() http.Handler    // GET /.portico/login?rd=<path>: sets OIDCCookie {state, nonce, pkce verifier, rd}, 302 to the authorization endpoint; 503 "identity provider unavailable" when Provider fails
func (a *Authenticator) Callback() http.Handler // GET /.portico/callback: state must match the cookie, exchanges the code with PKCE, verifies the ID token (issuer, audience, nonce), creates the session, clears OIDCCookie, 302 to rd
func (a *Authenticator) Logout() http.Handler   // GET /.portico/logout: deletes the session, clears the cookie, 302 to the end_session endpoint with post_logout_redirect_uri=https://<prism host>/ and client_id; plain 302 to "/" if discovery is unavailable

// Require passes the request on with the session in its context when a valid
// session cookie is present. Otherwise: GET or HEAD with "text/html" in Accept
// → 302 to /.portico/login?rd=<path>; anything else → 401 JSON {"error":"portico session required"}.
func (a *Authenticator) Require(next http.Handler) http.Handler
func SessionFromContext(ctx context.Context) (Session, bool)

// SafeReturnPath keeps only an absolute path on the same host: it must start
// with a single "/" (not "//"), contain no scheme or host, and never point into
// "/.portico/". Anything else returns "/".
func SafeReturnPath(rd string) string
```

- [ ] **Step 1: Write the fake identity provider for tests**

`fakeidp_test.go`: `newFakeIDP(t *testing.T) *fakeIDP` starts an
`httptest.NewTLSServer` with an RSA key generated in the test and serves
`/.well-known/openid-configuration` (issuer = its own URL unless
`idp.issuerOverride` is set), `/auth` (records the request, 302 to
`redirect_uri` with `code=<random>&state=<state>`), `/token` (checks
`grant_type`, `code`, `code_verifier` against the stored challenge, client
credentials in Basic auth, returns `id_token` signed RS256 with `sub`,
`email`, `nonce`, `aud`, `iss`, `exp`), `/jwks`. `idp.down bool` makes
every endpoint close the connection.

- [ ] **Step 2: Write the failing tests**

```go
func TestDiscoverRejectsIssuerMismatch(t *testing.T) // issuerOverride "https://other.test" → error contains "issuer"
func TestDiscoverUsesDocumentEndpoints(t *testing.T)  // provider.Endpoint().AuthURL == idp.URL+"/auth"
func TestLoginRedirectsToAuthorizationEndpoint(t *testing.T) // 302, Location host == idp host, query has state, nonce, code_challenge_method=S256, scope "openid email"; OIDCCookie set Secure HttpOnly SameSite=Lax
func TestLoginUnavailableIdP(t *testing.T)            // idp.down → 503, body contains "identity provider unavailable"
func TestCallbackRoundTripCreatesSession(t *testing.T) // client with cookie jar follows login → fake auth → callback → ends 302 to "/console/"; jar has SessionCookie; store.Get returns Subject "alice", Email "alice@lab.portico.test"
func TestCallbackRejectsBadState(t *testing.T)        // state differs from the cookie → 400, no session created
func TestCallbackRejectsReplay(t *testing.T)          // same callback URL twice → second gets 400 (OIDCCookie cleared)
func TestSafeReturnPath(t *testing.T)                 // "/console/"→same; "https://evil.test/x"→"/"; "//evil.test"→"/"; "console"→"/"; "/.portico/callback"→"/"; "" → "/"
func TestRequireRedirectsNavigations(t *testing.T)    // GET Accept text/html no cookie → 302 /.portico/login?rd=%2Fconsole%2F
func TestRequireRejectsAPICalls(t *testing.T)         // POST /api/x no cookie → 401 JSON; GET Accept application/json → 401
func TestRequirePassesWithSession(t *testing.T)       // valid cookie → next called, SessionFromContext true
func TestLogoutClearsSessionAndRedirects(t *testing.T) // session gone from store, cookie MaxAge -1, 302 Location starts with idp end_session endpoint
```

The round-trip test uses a `http.Client` whose transport dials
`prism.test:443` to the handler-under-test server and `id.test:443` to the
fake IdP (`dialMap`), so cookies and redirects see real host names.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/auth/ -run 'TestDiscover|TestLogin|TestCallback|TestSafeReturnPath|TestRequire|TestLogout' -v`
Expected: FAIL, `undefined: Discover`

- [ ] **Step 4: Implement `oidc.go` and `handlers.go`**

`go get github.com/coreos/go-oidc/v3 golang.org/x/oauth2`. PKCE with
`oauth2.GenerateVerifier`/`oauth2.S256ChallengeOption`. The OIDC cookie
holds a JSON `{state, nonce, verifier, rd}` encoded base64url, `MaxAge`
10 min, same attributes as the session cookie; it is cleared on every
callback outcome. The ID token is verified with
`provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})` and the nonce
compared to the cookie. The `rd` query value goes through `SafeReturnPath`
both when stored and when used.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/auth go.mod go.sum
git commit -m "auth: OIDC authorization code flow with PKCE, session middleware, logout"
```

---

### Task 5: Reverse proxy upstreams

**Files:**
- Create: `internal/proxy/upstream.go`
- Create: `internal/proxy/upstream_test.go`

**Interfaces:**
- Produces:

```go
package proxy

type UpstreamOptions struct {
    Target       *url.URL    // https://10.23.210.175:9440 or http://127.0.0.1:8080
    HostHeader   string      // value of Host sent upstream (the public host name)
    TLS          *tls.Config // nil for plain HTTP targets
    StripCookies []string    // cookie names removed before forwarding (Portico's own)
    Log          *slog.Logger
}

// NewUpstream returns a ReverseProxy that rewrites the request to Target,
// sets Host and X-Forwarded-Host to HostHeader, X-Forwarded-For from the
// client, X-Forwarded-Proto always "https" (the public URL is https in both
// exposure modes), removes StripCookies from the Cookie header, forces
// HTTP/1.1 upstream, disables response buffering (FlushInterval -1) and
// answers 502 JSON {"error":"upstream unavailable"} on transport errors.
func NewUpstream(o UpstreamOptions) *httputil.ReverseProxy

// PrismTLS builds the client TLS configuration for Prism Central: ServerName
// from the target host, RootCAs from caFile when given, InsecureSkipVerify
// only when verify is false (logged once at warn level by the caller).
func PrismTLS(targetHost, caFile string, verify bool) (*tls.Config, error)
```

- [ ] **Step 1: Write the failing tests**

Each test starts an `httptest` upstream that records the received request
and runs the proxy in a second `httptest` server.

```go
func TestUpstreamRewritesHostAndForwardedHeaders(t *testing.T) // upstream sees Host "prism.test", X-Forwarded-Host "prism.test", X-Forwarded-Proto "https", X-Forwarded-For set
func TestUpstreamStripsPorticoCookies(t *testing.T) // Cookie "__Host-portico_session=a; __Host-portico_oidc=b; NTNX=c" → upstream sees only "NTNX=c"; no Cookie header at all when nothing remains
func TestUpstreamKeepsQueryAndBody(t *testing.T)    // POST with body and ?a=1 reach the upstream unchanged
func TestUpstreamPassesSetCookieAndRedirects(t *testing.T) // upstream 302 with Set-Cookie → client gets both unchanged (no redirect following)
func TestUpstreamWebSocketUpgrade(t *testing.T)     // upstream hijacks and echoes; client sends an HTTP/1.1 Upgrade request over a raw net.Conn through the proxy, reads "101", writes "ping", reads "ping"
func TestUpstreamDown(t *testing.T)                 // target port closed → 502, Content-Type application/json, body {"error":"upstream unavailable"}
func TestUpstreamUsesHTTP11(t *testing.T)           // TLS upstream with HTTP/2 enabled still receives r.ProtoMajor == 1
func TestPrismTLS(t *testing.T)                     // verify false → InsecureSkipVerify; caFile with a test CA → RootCAs non-nil; bad caFile → error
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/proxy/ -run 'TestUpstream|TestPrismTLS' -v`
Expected: FAIL, `undefined: NewUpstream`

- [ ] **Step 3: Implement `internal/proxy/upstream.go`**

`httputil.ReverseProxy` with `Rewrite` (`SetURL`, `SetXForwarded`, then
override `X-Forwarded-Proto` and `Out.Host`), a dedicated
`http.Transport` (`ForceAttemptHTTP2: false`, `TLSNextProto` set to an
empty map to disable HTTP/2, `TLSClientConfig` from options, `ResponseHeaderTimeout` 60 s,
`IdleConnTimeout` 90 s), `FlushInterval: -1`, `ErrorHandler` logging the
error class without the URL query.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/proxy/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/upstream.go internal/proxy/upstream_test.go
git commit -m "proxy: reverse proxy upstream with host rewrite, cookie stripping and WebSocket pass-through"
```

---

### Task 6: Public listener handler

**Files:**
- Create: `internal/proxy/public.go`
- Create: `internal/proxy/accesslog.go`
- Create: `internal/proxy/public_test.go`

**Interfaces:**
- Consumes: `PrismPolicy`, `IDPolicy` (Task 2); `auth.Authenticator`
  (`Login`, `Callback`, `Logout`, `Require`), `auth.SessionCookie`,
  `auth.OIDCCookie` (Tasks 3–4); `NewUpstream` (Task 5).
- Produces:

```go
package proxy

type PublicOptions struct {
    PrismHost string
    IDHost    string
    Prism     http.Handler // upstream to Prism Central
    Keycloak  http.Handler // upstream to Keycloak
    Auth      *auth.Authenticator
    Policy    PrismPolicy
    Log       *slog.Logger
}

// NewPublic routes by the request Host (port stripped, case-folded):
//   PrismHost: /.portico/login|callback|logout → Auth handlers;
//              /.portico/anything else → 404;
//              otherwise Policy.Deny → 403, then Auth.Require, then
//              Policy.Allowed → 403, then Prism.
//   IDHost:    IDPolicy.Check → 403, then Keycloak (no session).
//   other:     421 Misdirected Request.
// Every response passes through AccessLog.
func NewPublic(o PublicOptions) http.Handler

// AccessLog logs one line per request at info level with: host, method,
// path (no query), status, bytes, duration_ms, rule (deny/allow rule or
// "ok"), session (true/false). Never headers, never the query string.
func AccessLog(log *slog.Logger, next http.Handler) http.Handler

// Refuse writes a JSON error {"error": msg} with the given status and the
// rule name in the X-Portico-Rule header, so the probe script can show why.
func Refuse(w http.ResponseWriter, status int, rule, msg string)
```

- [ ] **Step 1: Write the failing tests**

Harness: a recording fake Prism upstream, a recording fake Keycloak
upstream, the fake IdP of Task 4, an `Authenticator` with a `MemoryStore`,
a helper `withSession(req)` that creates a session in the store and sets
the cookie, and a `bytes.Buffer` slog handler to inspect the log.

```go
func TestPublicBasicWithoutSessionIs403(t *testing.T)       // GET /api/nutanix/v3/users/me, Basic → 403, X-Portico-Rule deny-basic-auth, prism upstream received 0 requests
func TestPublicBasicWithSessionIs403(t *testing.T)          // same with a valid session → 403
func TestPublicAPIKeyIs403(t *testing.T)                    // X-ntnx-api-key with session → 403 deny-api-key
func TestPublicValidateCredentialsIs403(t *testing.T)       // POST .../$actions/validate-credentials with session → 403; without session → 403 (not 401)
func TestPublicLegacyLoginIs403(t *testing.T)               // POST /PrismGateway/j_spring_security_check → 403
func TestPublicNoSessionNavigationRedirects(t *testing.T)   // GET /console/ Accept text/html → 302 Location /.portico/login?rd=%2Fconsole%2F, upstream 0 requests
func TestPublicNoSessionAPIIs401(t *testing.T)              // POST /api/nutanix/v3/groups → 401
func TestPublicWithSessionProxies(t *testing.T)             // GET /console/ with session → upstream received Host prism.test, no __Host- cookies, response body from upstream
func TestPublicWebSocketRequiresSession(t *testing.T)       // GET /vnc/vm/x/proxy with Upgrade: websocket and no cookie → 401, upstream 0 requests
func TestPublicNotAllowedPathIs403(t *testing.T)            // GET /index.html with session → 403 not-allowed
func TestPublicPorticoPrefixNeverProxied(t *testing.T)      // GET /.portico/unknown with session → 404, upstream 0 requests
func TestPublicIDHostProxiesRealm(t *testing.T)             // Host id.test GET /realms/portico-lab/protocol/openid-connect/auth, no cookie → keycloak upstream received it with Host id.test
func TestPublicIDHostDeniesAdmin(t *testing.T)              // /admin/master/console/ → 403; /realms/master → 403
func TestPublicUnknownHostIs421(t *testing.T)               // Host other.test → 421
func TestPublicHostMatchingIgnoresPortAndCase(t *testing.T) // Host "PRISM.test:443" → treated as prism host
func TestAccessLogNeverContainsSecrets(t *testing.T)        // request with Basic "YWRtaW46cHc=", X-ntnx-api-key "k3y", Cookie "__Host-portico_session=sid", query "?code=c0de" → log buffer contains "deny-basic-auth" and "/api/x" but none of "YWRtaW46cHc=", "k3y", "sid", "c0de"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/proxy/ -run 'TestPublic|TestAccessLog' -v`
Expected: FAIL, `undefined: NewPublic`

- [ ] **Step 3: Implement `public.go` and `accesslog.go`**

Host from `r.Host` through `net.SplitHostPort` when it has a port,
`strings.ToLower`. A small `statusRecorder` wraps the `ResponseWriter` for
the log and must implement `http.Hijacker` and `http.Flusher` (WebSocket
and streaming pass through it). The rule name travels in a request-context
value set by the policy step and read by the logger.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/proxy/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/public.go internal/proxy/accesslog.go internal/proxy/public_test.go
git commit -m "proxy: public listener with host routing, deny-before-session policy and redacted access log"
```

---

### Task 7: Internal listeners, TLS and `portico serve`

**Files:**
- Create: `internal/proxy/internal.go`
- Create: `internal/proxy/internal_test.go`
- Create: `cmd/portico/serve.go`
- Create: `cmd/portico/serve_test.go`
- Modify: `cmd/portico/main.go` (the `serve` subcommand calls `runServe`)

**Interfaces:**
- Consumes: everything above; `config.Config` (Task 1).
- Produces:

```go
package proxy

// NewPassthrough forwards every request to prism unchanged except for the
// access log: no session, no policy. Bound only to the internal listener.
func NewPassthrough(prism http.Handler, log *slog.Logger) http.Handler

// NewAdmin serves GET /healthz → 200 {"status":"ok"} and anything else → 404.
// Milestone 2 mounts the admin UI here.
func NewAdmin(log *slog.Logger) http.Handler
```

```go
package main

// runServe builds the three servers from cfg and runs them until ctx is done,
// then shuts them down with a 10 s grace period. Returns the first listener
// error. Exposed for tests with free ports in cfg.
func runServe(ctx context.Context, cfg *config.Config, log *slog.Logger) error

// serverTLS returns a *tls.Config with MinVersion TLS 1.2 and the certificate
// from t, or nil when both files are empty (plain HTTP for tunnel mode).
func serverTLS(t config.TLS) (*tls.Config, error)
```

- [ ] **Step 1: Write the failing tests**

```go
// internal/proxy/internal_test.go
func TestPassthroughForwardsBasicAuthUnchanged(t *testing.T) // Authorization Basic reaches the upstream; Host is the request Host
func TestAdminHealthz(t *testing.T)                          // GET /healthz → 200 JSON; GET /x → 404

// cmd/portico/serve_test.go
func TestServeTLSNilWhenUnset(t *testing.T)   // empty TLS → nil, nil
func TestServeTLSLoadsPair(t *testing.T)      // self-signed pair generated in the test → MinVersion == tls.VersionTLS12
func TestRunServeStartsAndStops(t *testing.T) // cfg with three "127.0.0.1:0"-style free ports chosen by the test, fake Prism and fake IdP, plain HTTP; GET /healthz on admin → 200; GET /console/ on public with Host prism.test → 302 to /.portico/login; cancel ctx → runServe returns nil within 2 s
func TestRunServeWithIdPDown(t *testing.T)    // fake IdP down: process still answers healthz 200 and Basic → 403 on public; /.portico/login → 503
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/proxy/ ./cmd/portico/ -run 'TestPassthrough|TestAdmin|TestServe|TestRunServe' -v`
Expected: FAIL, `undefined: NewPassthrough` and `undefined: runServe`

- [ ] **Step 3: Implement `internal.go` and `serve.go`**

`serve.go`: logger from `cfg.Log.Level` (slog JSON on stderr);
`cfg.ClientSecret()`; `auth.NewAuthenticator` with
`DiscoveryURL = cfg.Keycloak.URL + <path of cfg.OIDC.Issuer>` (the issuer's
path is the realm path, so the loopback base plus that path is the same
realm on the backchannel);
`RedirectURL = "https://" + PrismHost + "/.portico/callback"`; Prism
upstream with `PrismTLS`, Keycloak upstream plain; public server
(`ReadHeaderTimeout` 10 s, no `WriteTimeout` because of WebSockets,
`IdleTimeout` 120 s); passthrough and admin servers on the internal
addresses with `cfg.Internal.TLS`; a goroutine sweeps the session store
every minute; `signal.NotifyContext` for SIGINT/SIGTERM in `main`. When
`cfg.PrismCentral.TLSVerify` is false, one warn line at startup.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: PASS for every package; vet and gofmt silent

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/internal.go internal/proxy/internal_test.go cmd/portico
git commit -m "serve: public, pass-through and admin listeners with graceful shutdown"
```

---

### Task 8: systemd unit, Keycloak OIDC client and lab deployment

**Files:**
- Create: `deploy/portico.service`
- Create: `lab/portico/portico.yaml`
- Create: `lab/portico/deploy.sh`
- Create: `lab/portico/undeploy.sh`
- Modify: `lab/saml/keycloak-realm.sh` (add `oidc-client REDIRECT_URI`)
- Modify: `lab/vm/keycloak.container` (add `Environment=KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`)
- Modify: `lab/vm/provision.sh` (restart Keycloak after `daemon-reload`)
- Modify: `lab/test_scripts.sh`, `lab/README.md`

**Interfaces:**
- Consumes: the `portico` binary (`serve -config`), `deploy/portico.yaml.example` keys.
- Produces: a running `portico.service` on the lab VM listening on
  `:443`, `10.23.210.190:8443`, `10.23.210.190:9440`; a Keycloak OIDC client
  `portico` in realm `portico-lab` whose secret is in
  `~/portico-secrets/portico-oidc-secret`.

- [ ] **Step 1: Write the failing script checks in `lab/test_scripts.sh`**

Append:

```bash
# M1-1: the lab config file carries no secret value, only file paths.
if grep -nE 'secret:|password|apiKey' lab/portico/portico.yaml | grep -vq 'client_secret_file'; then ko "portico.yaml holds a secret value"; else ok "portico.yaml references secrets by path only"; fi
# M1-2: deploy.sh refuses to run without the lab environment.
out=$(env -i PATH="$PATH" HOME="$HOME" bash lab/portico/deploy.sh 2>&1); rc=$?
if [ "$rc" -ne 0 ] && grep -q 'source lab/env.sh' <<<"$out"; then ok "deploy.sh needs lab/env.sh"; else ko "deploy.sh ran without env (rc=$rc)"; fi
# M1-3: the unit runs unprivileged and binds 443 through a capability.
if grep -q '^User=portico' deploy/portico.service && grep -q 'AmbientCapabilities=CAP_NET_BIND_SERVICE' deploy/portico.service && grep -q 'ProtectSystem=strict' deploy/portico.service; then ok "portico.service is unprivileged and hardened"; else ko "portico.service lacks hardening"; fi
# M1-4: Keycloak publishes backchannel endpoints dynamically (OIDC over loopback).
if grep -q 'KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true' lab/vm/keycloak.container; then ok "keycloak.container enables dynamic backchannel"; else ko "keycloak.container lacks KC_HOSTNAME_BACKCHANNEL_DYNAMIC"; fi
```

- [ ] **Step 2: Run the checks to verify they fail**

Run: `lab/test_scripts.sh`
Expected: the four `M1-*` lines print `FAIL`, final line `FAIL`

- [ ] **Step 3: Write `deploy/portico.service`**

`[Unit]` `After=network-online.target keycloak.service` (no `Requires`:
Portico must run with Keycloak down). `[Service]` `User=portico`,
`Group=portico`, `ExecStart=/usr/local/bin/portico serve -config /etc/portico/portico.yaml`,
`Restart=on-failure`, `RestartSec=5`, `AmbientCapabilities=CAP_NET_BIND_SERVICE`,
`CapabilityBoundingSet=CAP_NET_BIND_SERVICE`, `NoNewPrivileges=yes`,
`ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`,
`StateDirectory=portico`, `ConfigurationDirectory=portico`,
`LimitNOFILE=65536`. `[Install]` `WantedBy=multi-user.target`.

- [ ] **Step 4: Write `lab/portico/portico.yaml`**

Lab values: `public.listen ":443"`, `public.tls` `/etc/portico/tls/lab.crt`
and `.key`, hosts `prism.lab.portico.test` / `id.lab.portico.test`,
`internal.admin_listen "10.23.210.190:8443"`,
`internal.passthrough_listen "10.23.210.190:9440"`, `internal.tls` same
files, `prism_central.url https://10.23.210.175:9440`, `tls_verify: false`
(lab only: Prism Central's self-signed certificate; a product deployment
pins it with `ca_file`), `keycloak.url http://127.0.0.1:8080`,
`oidc.issuer https://id.lab.portico.test/realms/portico-lab`,
`client_id portico`, `client_secret_file /etc/portico/oidc-client-secret`,
`log.level debug`.

- [ ] **Step 5: Add `oidc-client` to `lab/saml/keycloak-realm.sh` and the Keycloak settings**

`oidc-client REDIRECT_URI`: create or update client `portico` in realm
`portico-lab`: `protocol openid-connect`, `publicClient false`,
`standardFlowEnabled true`, `directAccessGrantsEnabled false`,
`redirectUris [REDIRECT_URI]`, attribute `pkce.code.challenge.method S256`;
then `GET /clients/{id}/client-secret` and save `.value` to
`$S/portico-oidc-secret` (umask 077) when the file is missing. Idempotent.

`keycloak.container`: add `Environment=KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`.
`provision.sh`: after `systemctl daemon-reload`, `systemctl restart
keycloak.service` so a changed quadlet takes effect.

- [ ] **Step 6: Write `lab/portico/deploy.sh` and `undeploy.sh`**

`deploy.sh` (requires `source lab/env.sh`, exits 1 with
`deploy.sh: source lab/env.sh first` otherwise):
1. `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$tmp/portico" ./cmd/portico`
   (the lab VM is x86_64: verified by `ssh lab@$LAB_VM_IP uname -m` at the
   start, abort if not `x86_64`).
2. `scp` binary, `lab/portico/portico.yaml`, `deploy/portico.service`,
   `$S/portico-oidc-secret`, `$S/lab-ca.crt`, `$S/lab-ca.key` to `/tmp/` on
   the VM (umask 077 locally).
3. Remote `sudo bash -s`: `useradd -r -s /sbin/nologin portico` if missing;
   `install -d -m 0750 -o root -g portico /etc/portico /etc/portico/tls`;
   `install -m 0640 -g portico` config, secret, cert, key (secret and key
   0640 root:portico so `ProtectSystem=strict` and a non-root user both
   work); `install -m 0755 /tmp/portico /usr/local/bin/portico`; unit to
   `/etc/systemd/system/`; `rm -f` the temporary secret and key;
   `restorecon -R /etc/portico /usr/local/bin/portico`;
   `firewall-cmd --permanent --add-port={8443,9440}/tcp && firewall-cmd --reload`;
   `systemctl disable --now nginx`; `systemctl daemon-reload`;
   `systemctl enable --now portico` then `systemctl restart portico`.
4. Wait up to 30 s for `curl -sk https://$LAB_VM_IP:8443/healthz` from the
   Mac to answer 200, print `portico ready`, else dump
   `journalctl -u portico -n 50` and exit 1.

`undeploy.sh`: `systemctl disable --now portico`, `systemctl enable --now
nginx`, leave files in place. Prints `nginx stand-in restored`.

- [ ] **Step 7: Run the checks to verify they pass**

Run: `lab/test_scripts.sh`
Expected: every line `ok`, final line `PASS` (shellcheck included)

- [ ] **Step 8: Apply to the lab**

Run, in order:

```bash
source lab/env.sh
lab/vm/provision.sh
ssh lab@$LAB_VM_IP "curl -s http://127.0.0.1:8080/realms/portico-lab/.well-known/openid-configuration" | jq '{issuer, authorization_endpoint, token_endpoint, jwks_uri}'
```

Expected: `issuer` and `authorization_endpoint` on
`https://id.lab.portico.test/...`, `token_endpoint` and `jwks_uri` on
`http://127.0.0.1:8080/...`. If Keycloak keeps every endpoint on the public
name, the fallback is `Discover` rewriting token and JWKS URLs to the
backchannel base: ledger it as a ruling and add the rewrite with a test
before continuing.

```bash
lab/saml/keycloak-realm.sh oidc-client https://prism.lab.portico.test/.portico/callback
lab/portico/deploy.sh
```

Expected: `saml client`-style confirmation line for the OIDC client, then
`portico ready`.

- [ ] **Step 9: Commit**

```bash
git add deploy/portico.service lab/portico lab/saml/keycloak-realm.sh lab/vm/keycloak.container lab/vm/provision.sh lab/test_scripts.sh lab/README.md
git commit -m "deploy: hardened systemd unit; lab: Keycloak OIDC client and Portico deployment replacing nginx"
```

---

### Task 9: Lab acceptance, probe script, documentation

**Files:**
- Modify: `lab/auth/probe-local-login.sh` (expected codes and exit status)
- Create: `docs/lab/2026-10-17-09-go-proxy-acceptance.md` (date of the run)
- Modify: `docs/lab/README.md` (index row 09), `docs/lab/lab-inventory.md`
  (OIDC client, Portico service on the VM, undeploy command)
- Modify: `docs/superpowers/specs/2026-10-17-portico-design.md` (§4.2:
  same-site requirement for the two public names; §4.3: `/.portico/*`
  endpoints, cookie attributes, deny-before-session order, OIDC backchannel
  over loopback; §9 milestone 1 row marked done with the date)
- Modify: `docs/adr/0002-proxy-in-go-binary.md` (consequence: WebSocket
  through the Go proxy measured), `README.md` (Status)

- [ ] **Step 1: Rewrite the probe script with expectations**

Each line prints `METHOD PATH -> got (expected)`; the script exits 1 if any
line differs. Lines and expected codes through the public name:

| request | expected |
|---|---|
| `GET /console/` (no session) | 302, `Location` starts with `/.portico/login?rd=` |
| `POST /api/iam/v4.1.b3/authn/login-requests/<random>/$actions/validate-credentials` with admin credentials | 403 |
| `POST /PrismGateway/j_spring_security_check` with form credentials | 403 |
| `GET` ×4 with Basic admin (the four generations of the old script) | 403 |
| `GET /api/multidomain/v4.4.b1/config/projects` with `X-ntnx-api-key` | 403 |
| `POST /api/nutanix/v3/groups` (no session, no credentials) | 401 |
| `GET https://id.lab.portico.test/admin/master/console/` | 403 |
| `GET https://id.lab.portico.test/realms/portico-lab/` | 200 |
| `GET https://10.23.210.190:9440/PrismGateway/services/rest/v1/cluster` with Basic admin (internal pass-through, `-k`: the lab certificate has no IP SAN) | 200 |

Credentials stay in curl config files as today.

- [ ] **Step 2: Run the probe against the deployed Portico**

Run: `source lab/env.sh && lab/auth/probe-local-login.sh; echo rc=$?`
Expected: every line `got == expected`, `rc=0`. Any 502 means the upstream
TLS or host settings are wrong: check `journalctl -u portico` on the VM.

- [ ] **Step 3: Browser round trip as alice**

With the `/etc/hosts` line and the lab CA trusted in Chrome (both done in
Milestone 0): open `https://prism.lab.portico.test/console/`.
Expected: Keycloak login form on `id.lab.portico.test` (Portico OIDC) →
after alice's credentials, Prism Central's login page → "Log in with
portico-lab-keycloak" → straight into the console without a second
password prompt (Keycloak SSO) → VM list with only the project's VM.
Then "Launch Console" on the VM: the noVNC popup connects (allow popups
for the site if Chrome blocks it). Record both outcomes, including the
popup result that stayed PENDING in write-up 08.

- [ ] **Step 4: IdP down**

Run on the VM: `sudo systemctl stop keycloak`, then from the Mac
`lab/auth/probe-local-login.sh` and `curl -sk -o /dev/null -w '%{http_code}\n' https://10.23.210.190:8443/healthz`.
Expected: credential lines still 403, the `/realms/portico-lab/` line 502
(Keycloak unreachable through Portico) so the probe exits 1 on that single
line, health 200, `/.portico/login` answers 503. Then `sudo systemctl start
keycloak`, wait, re-run the probe: `rc=0`.

- [ ] **Step 5: Resource check**

Run on the VM: `systemctl status portico --no-pager | grep Memory` and
`free -m`.
Expected: Portico resident memory under 50 MiB idle; record the numbers.

- [ ] **Step 6: Write the write-up and update the documents**

`docs/lab/2026-10-17-09-go-proxy-acceptance.md` from `TEMPLATE.md`:
probe output (codes only), browser outcome, popup outcome, IdP-down
behaviour, memory, how to re-run (`lab/portico/deploy.sh` then the probe).
Design, ADR 0002, README Status ("Milestone 1: the core binary runs in the
lab; no tenant provisioning yet") and the lab index as listed in Files.

- [ ] **Step 7: Verify text rules and the suite**

Run: `go test ./... && lab/test_scripts.sh && lab/test_env.sh && grep -rniE '\b(della|degli|questo|questa|perché|però)\b' README.md docs/superpowers docs/lab docs/adr lab internal cmd deploy | grep -v docs/history`
Expected: tests PASS; the grep prints nothing.

- [ ] **Step 8: Commit**

```bash
git add lab/auth/probe-local-login.sh docs README.md
git commit -m "docs: Milestone 1 acceptance in the lab; design updated with the measured proxy behaviour"
```

---

## Self-review notes

- Spec coverage: §4.2 listeners (Tasks 6–7), §4.3 request flow and deny
  list (Tasks 2, 4, 6), §4.6 TLS modes (Task 7, ACME deferred to the
  appliance milestone), §5 threats 1, 2, 4 and the "Cloudflare removed"
  row (same policy in both modes: tunnel mode is plain HTTP on the same
  handler), §9 milestone 1 acceptance (Task 9). Keycloak admin console
  never published (Task 2 `IDPolicy`). Not in this milestone, by the spec:
  admin UI and break-glass account (§4.2, Milestone 2), tenant
  provisioning (§4.4), service account use (§4.5), SQLite and encryption
  at rest (§4.6, Milestone 2 when there is state to keep), console wizard
  and image (§4.8–4.9).
- Interfaces: `auth.Authenticator` methods used in Task 6 match Task 4;
  `config.DefaultAllowPrefixes` used by Task 2 tests and Task 7 wiring;
  `Refuse` is the single place writing 403/401 bodies.
- Rate limiting on `/.portico/login` is left out: Keycloak's own brute-force
  detection covers the credential form, and Portico's login endpoint only
  redirects. A CSRF token on `/.portico/logout` is also left out (GET, no
  state change beyond ending the caller's own session).
