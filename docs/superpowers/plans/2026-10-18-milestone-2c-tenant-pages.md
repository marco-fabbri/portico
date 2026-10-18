# Milestone 2c: Tenant-Facing Pages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A tenant user goes from the public URL to the Prism Central
console with one Keycloak login and no Nutanix login page, and every
page seen on the way (Keycloak login, Portico landing page, Portico error
pages) carries the MSP's name, logo and colour set from the admin UI.

**Architecture:** Branding is the first store-held setting
(`settings` table, JSON value) with an API and an admin page. A new
package `internal/branding` validates the settings, renders Portico's
tenant-facing pages (landing, error) from embedded templates, and
generates a Keycloak login theme directory that Keycloak mounts; saving
branding regenerates the theme and sets it on the realm through the
admin client. On the public host Portico answers Prism Central's
provider page with the landing page and starts the SAML flow itself
from `/.portico/saml-start`, replaying the measured PUT with the
browser's own login-request cookie; any failure falls back once to the
original page. The identity-provider-down answers become branded pages
for browsers.

**Tech Stack:** Go 1.27 standard library (`html/template`, `embed`,
`mime/multipart`), existing packages. Keycloak 26.3 login theme on top
of `keycloak.v2` (theme directory mounted into the container). Lab:
NTNX-01, VM `portico-lab`.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md` sections
4.3 step 5, 4.7 (settings file/store), 4.10, 5, milestone 2c in section
9; ADR 0003. Evidence: `docs/lab/2026-10-18-12-console-headers-and-provider-selection.md`
(the PUT body and the Sign Out path), `docs/lab/2026-10-17-06-local-login-and-fallback.md`
(`iam-sessionid`, `initiate-login`, `ERR-412`).

## Global Constraints

- All text on disk in English, UI strings and theme messages included.
- Work on branch `m2c-tenant-pages` from `main` (already created, holds
  the design update); integrate through a pull request merged by the
  executor.
- The public path fails closed: the landing page and `/.portico/saml-start`
  are reachable only with a Portico session; the deny list and the
  allow-list of Milestone 1/2b stay as they are. The SAML start uses the
  browser's own `iam-sessionid` cookie against Prism Central, never the
  service-account key: Prism Central keeps deciding who logs in.
- The automation is convenience, not control (spec §4.3 step 5): on any
  failure Portico forwards the original provider page once, with a
  marker, and never loops.
- Branding input is untrusted: HTML-escaped in every template, escaped
  in the Keycloak properties files, logo limited to PNG, JPEG or SVG of
  at most 256 KiB and served with a Content-Security-Policy that forbids
  scripts (an SVG may carry them).
- Never log secrets; the branding save logs only the field names that
  changed.
- `CGO_ENABLED=0`, no CDN, no JavaScript toolchain; the landing page
  needs no JavaScript (a meta refresh starts the flow).
- `go vet ./...`, `gofmt -l cmd internal` and `go test ./...` clean at
  every commit; `lab/test_scripts.sh` green.
- Lab: Prism Central `https://10.23.210.175:9440`, VM `10.23.210.190`,
  hosts `prism.lab.portico.test` and `id.lab.portico.test`, Keycloak as a
  podman quadlet (`lab/vm/keycloak.container`, installed by
  `lab/vm/provision.sh`), SAML IdP `portico-lab-keycloak`
  (extId `247f2c3d-4566-52fe-84ed-48b14799bf05`).

## Review Focus

Failure modes the spec implies but a single task's tests might miss; each
has a test in the task named:

1. The provider-selection PUT fails (Prism Central answers 4xx, the cookie
   is missing, the IdP is not found): expected one redirect to the
   original provider page with the marker, which Portico then forwards
   unchanged; a second visit without the marker tries again; never a
   redirect loop (Task 3).
2. An SVG logo containing `<script>`: expected it is stored and served,
   but with `Content-Security-Policy: default-src 'none'` and `nosniff`,
   never inlined into a page (Task 1).
3. An MSP name containing `<b>`, `\n` or `=`: expected it is escaped in
   the landing page and in the Keycloak messages file (no injection into
   properties syntax), and the theme still loads (Task 2).
4. `GET /ui/iam/providers` without a Portico session: expected the
   Milestone 1 behaviour, a redirect to `/.portico/login`, and a `POST`
   or a request with the marker is proxied unchanged (Task 3).
5. The theme directory is not writable or Keycloak refuses the realm
   update: expected the branding save still succeeds for Portico's own
   pages and the admin sees a warning naming the failed part, not a 500
   (Task 2).

---

### Task 0: Lab measurements: theme mounting, login-request shape, `/redirect` target

**Files:**
- Modify: `lab/vm/keycloak.container` (theme volume, cache settings),
  `lab/vm/provision.sh` (creates `/var/lib/portico/keycloak-theme` owned
  by `portico`), `lab/saml/keycloak-realm.sh` (`admin-client` maps
  `manage-realm` too)
- Create: `lab/saml/theme-probe.sh`, `lab/auth/probe-login-request.sh`
- Create: `docs/lab/2026-10-18-13-keycloak-theme-and-landing.md`
- Modify: `docs/lab/README.md` (row 13), `lab/auth/probe-local-login.sh`
  (one line for `/redirect` if the measurement warrants it)

**Interfaces:**
- Produces: the measured facts Tasks 2 and 3 depend on: the minimal
  theme file set Keycloak 26.3 accepts, whether a saved theme applies
  without a restart with cache disabled, the JSON shape of
  `GET /api/iam/v4.1.b3/authn/login-requests/{id}`, and whether
  `/redirect?logout=<url>` follows an external URL.

- [ ] **Step 1: Theme mount in the quadlet**

In `lab/vm/keycloak.container` add
`Volume=/var/lib/portico/keycloak-theme:/opt/keycloak/themes/portico:ro,z`
and `Environment=KC_SPI_THEME_CACHE_THEMES=false`,
`Environment=KC_SPI_THEME_CACHE_TEMPLATES=false`,
`Environment=KC_SPI_THEME_STATIC_MAX_AGE=-1`. In `lab/vm/provision.sh`
create the directory (`install -d -m 0755 -o portico -g portico`, after
the `portico` user exists: create it there too when missing, same
command as `lab/portico/deploy.sh`). `lab/saml/keycloak-realm.sh
admin-client`: add `manage-realm` to the role list. Run
`source lab/env.sh && lab/vm/provision.sh && lab/saml/keycloak-realm.sh admin-client`.
Expected: Keycloak restarts healthy (`lab/portico/deploy.sh`'s health
wait, or `curl http://127.0.0.1:8080/health/ready` over SSH → 200),
`roles mapped: manage-realm`.

- [ ] **Step 2: Write `lab/saml/theme-probe.sh` and run it**

Writes a minimal theme into the mounted directory over SSH as `portico`:
`login/theme.properties` (`parent=keycloak.v2`, `import=common/keycloak`,
`styles=css/login.css css/portico.css`), `login/resources/css/portico.css`
(`.pf-v5-c-login__main-header::before { content: "PROBE"; }` or whatever
selector the measured page uses; the first run finds the selector),
`login/messages/messages_en.properties` (`loginTitleHtml=Probe MSP`),
then `PUT /admin/realms/portico-lab` with `loginTheme: portico` through
`lab/saml/kc.sh` (GET, edit `loginTheme`, PUT). Then
`curl -s --cacert ... https://id.lab.portico.test/realms/portico-lab/protocol/openid-connect/auth?client_id=portico&response_type=code&redirect_uri=https://prism.lab.portico.test/.portico/callback&scope=openid`
and grep the HTML for `portico.css` and `Probe MSP`.
Expected: both present without a Keycloak restart. If not, record what
was needed (restart, different file names) and adjust Task 2's file set.
Then set `loginTheme` back to `keycloak` (or leave the probe theme; Task 2
replaces it).

- [ ] **Step 3: Write `lab/auth/probe-login-request.sh` and run it**

On the pass-through as in `probe-bearer-and-v3-users.sh`: `GET /console/`
with a cookie jar → `iam-sessionid`; `GET /api/iam/v4.1.b3/authn/login-requests/<id>`
with the cookie → print the JSON with values longer than 12 characters
redacted (as in Task 0 of 2a). Then PUT the same object back with
`loginProviderId` set to the lab IdP extId and `GET /api/iam/authn/initiate-login`
without following redirects → expect a 302 to `https://id.lab.portico.test/realms/portico-lab/protocol/saml?...`.
Expected: the GET answers 200 with `clientId`, `scope`, `redirectUri`
present; the PUT 200; `initiate-login` 302 to Keycloak. Record the field
names and whether the PUT needs `If-Match` or `NTNX-Request-Id` (the
console sent neither).

- [ ] **Step 4: `/redirect` target probe**

With alice's Portico session is awkward from curl; instead on the
pass-through (no session needed): `GET /redirect?logout=https://example.invalid/`
without following redirects. Record the status and `Location`.
Expected: either a 302 to the given URL (open redirect: record it, add a
probe line on the public name expecting Portico to… no change, Portico
cannot validate Prism Central's parameter; note it for the design) or a
same-host redirect only.

- [ ] **Step 5: Write-up 13 (part one), README row, lab checks, commit**

`docs/lab/2026-10-18-13-keycloak-theme-and-landing.md` from the
template, row 13 in `docs/lab/README.md`.

Run: `lab/test_scripts.sh && shellcheck -x lab/saml/theme-probe.sh lab/auth/probe-login-request.sh`
Expected: PASS.

```bash
git add lab docs/lab
git commit -m "lab: Keycloak theme mount, login-request shape, redirect target (write-up 13)"
```

---

### Task 1: Branding settings: store, model, API, admin page

**Files:**
- Create: `internal/store/migrations/0003_settings.sql`; modify
  `internal/store/store.go` (interface + `SQLite` methods),
  `internal/store/settings.go`, `internal/store/store_test.go`
- Create: `internal/branding/branding.go` (model, defaults, validation,
  JSON codec), `internal/branding/service.go` (`Service` over the store
  with cache and change hook), `internal/branding/branding_test.go`
- Modify: `internal/api/api.go` (settings routes), `internal/api/api_test.go`
- Create: `internal/web/settings.go`, `internal/web/templates/settings.html`,
  `internal/web/settings_test.go`; modify `internal/web/web.go`
  (`Options.Branding`, routes, nav link), `internal/web/templates/layout.html`
- Modify: `cmd/portico/serve.go`

**Interfaces:**
- Produces:

```go
package store
// in the Store interface:
GetSetting(ctx context.Context, key string) ([]byte, error)   // ErrNotFound
SetSetting(ctx context.Context, key string, value []byte) error  // upsert
```

```go
package branding

const (
    MaxLogoBytes   = 256 << 10
    MaxNameLen     = 80
    MaxTaglineLen  = 160
    MaxDisplayMs   = 10000
    SettingKey     = "branding"
)

type Branding struct {
    MSPName      string `json:"msp_name"`       // default "Portico"
    Tagline      string `json:"tagline"`        // default ""
    AccentColor  string `json:"accent_color"`   // "#rrggbb", default "#1f5f8b"
    LogoType     string `json:"logo_type"`      // "", "image/png", "image/jpeg", "image/svg+xml"
    LogoData     []byte `json:"logo_data"`      // raw bytes (base64 on the wire)
    MinDisplayMs int    `json:"min_display_ms"` // 0..10000, default 0
}
func Default() Branding
func (b Branding) Validate() error            // one error joining every violation
func (b Branding) HasLogo() bool

type Service struct{ /* store, cache, mu, onChange */ }
func NewService(st store.Store, log *slog.Logger) *Service
func (s *Service) Get(ctx context.Context) Branding            // Default() when unset or unreadable (logged)
func (s *Service) Set(ctx context.Context, b Branding) error   // Validate, store, cache, then OnChange(b) (errors from the hook are returned as *ApplyError, the save itself stays)
func (s *Service) OnChange(fn func(ctx context.Context, b Branding) error)

type ApplyError struct{ Err error }   // the settings were saved; applying them elsewhere failed
```

API, behind `RequireAdmin` like the rest:

| route | body / answer |
|---|---|
| `GET /api/v1/settings/branding` | 200 `{msp_name, tagline, accent_color, logo_type, logo_data (base64), min_display_ms}` |
| `PUT /api/v1/settings/branding` | same JSON; 200 with the stored value; 400 on validation (message lists the fields); 200 with `{"warning": "<apply error>"}` merged in when the save worked but applying failed (Review Focus 5) |
| `GET /api/v1/settings/branding/logo` | the logo bytes with its type, `Content-Security-Policy: default-src 'none'`, `X-Content-Type-Options: nosniff`, `Cache-Control: no-store`; 404 without a logo |

UI: `GET /settings/branding` (form: name, tagline, colour input, logo
file input with the current logo shown via `/settings/branding/logo`,
minimum display time in ms, "Remove logo" checkbox), `POST
/settings/branding` (multipart, CSRF; 400 re-renders with the message;
an apply warning shows as an amber banner), `GET /settings/branding/logo`
(same headers as the API). A "Branding" link in the layout's nav.

- [ ] **Step 1: Write the failing store tests**

```go
func TestSettingsRoundTripAndNotFound(t *testing.T)   // GetSetting unknown → ErrNotFound; Set/Get bytes equal; Set again overwrites
```

- [ ] **Step 2: Run, implement (migration 0003: `settings(key TEXT PRIMARY KEY, value BLOB NOT NULL, updated_at TEXT NOT NULL)`), run**

Run: `go test ./internal/store/`
Expected: FAIL to compile, then PASS.

- [ ] **Step 3: Write the failing branding tests**

```go
func TestDefaultIsValid(t *testing.T)
func TestValidateRejects(t *testing.T)   // name 81 chars; tagline 161; colour "red", "#12345", "#GGGGGG"; logo type "image/gif"; logo 256 KiB + 1; min display -1 and 10001; each named in the error
func TestServiceGetDefaultWhenUnset(t *testing.T)
func TestServiceSetStoresAndCaches(t *testing.T)    // Set then Get from a second Service over the same store equals; the first Service's Get returns the new value without a store read (count store calls through a wrapper)
func TestServiceSetCallsHookAndKeepsSaveOnHookError(t *testing.T)   // hook returns an error → Set returns *ApplyError, Get returns the new value (Review Focus 5)
func TestLogoSVGWithScriptIsStoredAsIs(t *testing.T)   // Validate accepts it; it is the servers' headers that protect (asserted in the API/web tests)
```

- [ ] **Step 4: Run, implement, run**

Run: `go test ./internal/branding/`
Expected: FAIL to compile, then PASS.

- [ ] **Step 5: Write the failing API tests**

```go
func TestBrandingGetDefault(t *testing.T)                 // GET → 200 with msp_name "Portico"
func TestBrandingPutValidatesAndStores(t *testing.T)      // PUT bad colour → 400 naming accent_color; PUT good with a PNG (base64 of 8 bytes "\x89PNG\r\n\x1a\n") → 200; GET returns it; logo endpoint → 200 image/png with CSP default-src 'none' and nosniff
func TestBrandingLogoSVGHeaders(t *testing.T)             // SVG with <script> → stored; GET logo → Content-Type image/svg+xml, CSP default-src 'none' (Review Focus 2)
func TestBrandingPutWarningWhenApplyFails(t *testing.T)   // hook fails → 200 with "warning"
```

- [ ] **Step 6: Run, implement (`api.New` gains a `*branding.Service` parameter), run**

Run: `go test ./internal/api/`
Expected: FAIL to compile, then PASS.

- [ ] **Step 7: Write the failing web tests**

```go
func TestBrandingPageShowsCurrentValues(t *testing.T)     // GET /settings/branding → 200 with the name in the input and the nav link
func TestBrandingFormSavesAndShowsLogo(t *testing.T)      // multipart POST with name, colour, a PNG file → 303 /settings/branding; GET shows the name; GET /settings/branding/logo → 200 image/png with the CSP header
func TestBrandingFormRejectsBadInput(t *testing.T)        // GIF file → 400 with "logo" in the banner; 300 KiB PNG → 400
func TestBrandingFormRemoveLogo(t *testing.T)             // remove_logo=on → logo endpoint 404 afterwards
func TestBrandingFormRequiresCSRF(t *testing.T)
func TestBrandingFormShowsApplyWarning(t *testing.T)      // hook error → 200 page with an amber banner naming it, values saved
```

- [ ] **Step 8: Run, implement, run; wire `serve` (branding service built after the store, passed to `api.New` and `web.Options.Branding`)**

Run: `go test ./internal/web/ && go test ./... && go vet ./... && gofmt -l cmd internal`
Expected: PASS, clean.

- [ ] **Step 9: Commit**

```bash
git add internal cmd
git commit -m "feat: branding settings in the store with API and admin page"
```

---

### Task 2: Keycloak login theme generated from the branding

**Files:**
- Create: `internal/branding/theme.go`, `internal/branding/theme_test.go`,
  `internal/branding/theme/` (embedded template files: `theme.properties`,
  `portico.css.tmpl`, `messages_en.properties.tmpl`)
- Modify: `internal/idp/keycloak/client.go` (`SetRealmLoginTheme`),
  `internal/idp/keycloak/client_test.go`
- Modify: `internal/config/config.go` (`Keycloak.ThemeDir`, default
  `/var/lib/portico/keycloak-theme`, `Keycloak.ThemeName` default
  `portico`), `internal/config/config_test.go`
- Modify: `cmd/portico/serve.go` (OnChange hook: write theme, set realm
  theme; also applied once at start when branding exists)
- Modify: `deploy/portico.yaml.example`, `lab/portico/portico.yaml`,
  `lab/portico/deploy.sh` (creates the theme dir owned by `portico`)

**Interfaces:**
- Produces:

```go
package branding
// WriteTheme writes the Keycloak login theme for b into dir (created with
// 0755; files 0644), atomically per file. File set (adjust to Task 0's
// measurement): login/theme.properties, login/resources/css/portico.css,
// login/resources/img/logo.<png|jpg|svg> (when present),
// login/messages/messages_en.properties.
func WriteTheme(dir string, b Branding) error
// PropertiesEscape escapes a value for a Java .properties file: backslash,
// newline, carriage return, tab, leading spaces, and "=", ":" and "#"
// are escaped; non-ASCII stays (UTF-8 is read by Keycloak).
func PropertiesEscape(s string) string
```

```go
package keycloak
func (c *Client) SetRealmLoginTheme(ctx context.Context, theme string) error   // GET /admin/realms/{r}, set loginTheme, PUT
```

The CSS sets the accent colour on the primary button and links, the
logo as the header image (`.pf-v5-c-login__main-header` or the selector
measured in Task 0), and hides Keycloak's own title when a logo exists.
`messages_en.properties`: `loginTitleHtml=<escaped MSP name>`,
`loginAccountTitle=<escaped tagline or "Sign in">`.

- [ ] **Step 1: Write the failing theme tests**

```go
func TestWriteThemeFiles(t *testing.T)            // temp dir; files exist; theme.properties has parent=keycloak.v2; css contains the colour; messages has loginTitleHtml=
func TestWriteThemeEscapesName(t *testing.T)      // name "A=B:C\nD <b>x</b>" → messages line "loginTitleHtml=A\=B\:C\nD <b>x</b>"? No: HTML in loginTitleHtml renders; the name is HTML-escaped first then properties-escaped: assert "&lt;b&gt;" and "\=" and "\n" (Review Focus 3)
func TestWriteThemeLogoByType(t *testing.T)       // png → img/logo.png; svg → img/logo.svg; none → no img file and css without the logo rule
func TestWriteThemeUnwritableDir(t *testing.T)    // dir under a read-only parent → error naming the dir
func TestPropertiesEscape(t *testing.T)           // table
```

- [ ] **Step 2: Run, implement, run**

Run: `go test ./internal/branding/`
Expected: FAIL to compile, then PASS.

- [ ] **Step 3: Write the failing Keycloak client test**

```go
func TestSetRealmLoginTheme(t *testing.T)   // fake GET /admin/realms/lab → {realm:"lab", loginTheme:"keycloak", displayName:"x"}; PUT body has loginTheme "portico" and displayName "x" preserved
```

- [ ] **Step 4: Run, implement, run**

Run: `go test ./internal/idp/keycloak/`
Expected: FAIL, then PASS.

- [ ] **Step 5: Config fields with tests (`TestKeycloakThemeDefaults`), serve hook**

In `serve`: `brand.OnChange(func(ctx, b) error { if err := branding.WriteTheme(cfg.Keycloak.ThemeDir, b); err != nil { return fmt.Errorf("theme files: %w", err) }; if err := kcClient.SetRealmLoginTheme(ctx, cfg.Keycloak.ThemeName); err != nil { return fmt.Errorf("keycloak realm: %w", err) }; return nil })`.
At start, when a branding setting exists, run the same once and log a
warning on failure (the service keeps serving).

- [ ] **Step 6: Deploy and look**

`lab/portico/deploy.sh` creates `/var/lib/portico/keycloak-theme`
(`portico:portico` 0755) if missing. Run:
`source lab/env.sh && lab/portico/deploy.sh`, then from the admin UI (or
`lab/tenants/api.sh PUT /api/v1/settings/branding` with a JSON file)
set name "Portico Lab MSP", colour `#8b1f5f`, a small PNG logo. Then open
`https://id.lab.portico.test/realms/portico-lab/protocol/openid-connect/auth?...`
in Chrome (or curl and grep) → the login page shows the name, the colour
and the logo.
Expected: the theme applies without a Keycloak restart (Task 0).

- [ ] **Step 7: Suite and commit**

Run: `go test ./... && go vet ./... && gofmt -l cmd internal && lab/test_scripts.sh`
Expected: PASS.

```bash
git add internal cmd deploy lab
git commit -m "feat: Keycloak login theme generated from the branding settings"
```

---

### Task 3: Landing page and SAML start on the public host

**Files:**
- Create: `internal/branding/pages.go` (embedded templates `landing.html`,
  `error.html`; `Pages` renderer), `internal/branding/pages_test.go`
- Create: `internal/proxy/landing.go`, `internal/proxy/landing_test.go`
- Modify: `internal/proxy/public.go` (`PublicOptions.Landing`,
  the providers intercept, the `/.portico/saml-start` route behind
  `Require`, `/.portico/logo`), `internal/proxy/public_test.go`
- Modify: `cmd/portico/serve.go`

**Interfaces:**
- Produces:

```go
package branding
type Pages struct{ /* templates */ }
func NewPages(get func(ctx context.Context) Branding) *Pages
// Landing renders the branded waiting page: logo (or the name), tagline,
// a CSS spinner, and <meta http-equiv="refresh" content="<MinDisplayMs/1000>;url=<next>">
// plus a plain link to next for browsers without meta refresh. No scripts.
func (p *Pages) Landing(w http.ResponseWriter, r *http.Request, next string)
// Error renders a branded error page (status, title, text, optional retry link).
func (p *Pages) Error(w http.ResponseWriter, r *http.Request, status int, title, text, retry string)
```

```go
package proxy
type LandingOptions struct {
    Pages      *branding.Pages
    Logo       func(ctx context.Context) (data []byte, ctype string, ok bool)
    PrismHTTP  *http.Client   // transport to Prism Central (same TLS as the upstream), no redirects
    PrismBase  *url.URL       // https://<pc>:9440
    PrismHost  string         // public host name, sent as Host and used in redirectUri
    IdPExtID   func(ctx context.Context) (string, error)   // the SAML identity provider to select
    Log        *slog.Logger
}
// PublicOptions gains: Landing *LandingOptions (nil = Milestone 1 behaviour)
```

Behaviour on the Prism Central host, inside the session-checked chain
(after `Require`, before the allow-list):

- `GET /ui/iam/providers` (normalised path) without the query marker
  `portico=manual` → `Pages.Landing(next="/.portico/saml-start")` with
  `Cache-Control: no-store`. Any other method or the marker → proxied as
  before (the allow-list already includes `/ui/iam/`).
- `GET /.portico/saml-start` (behind `Require`): read the `iam-sessionid`
  cookie; `GET {PrismBase}/api/iam/v4.1.b3/authn/login-requests/{id}`
  with `Host: PrismHost`, `Cookie: iam-sessionid=<id>` (only that cookie),
  `Accept: application/json`; decode the `data` object as `map[string]any`;
  set `loginProviderId` to `IdPExtID()`; `PUT` the object back (same
  headers plus `Content-Type: application/json` and the headers Task 0
  found necessary); on 2xx → `302 /api/iam/authn/initiate-login`. Any
  failure (no cookie, non-2xx, IdP lookup error, transport error) →
  log at Warn with the step name and status (never the cookie), then
  `302 /ui/iam/providers?portico=manual`.
- `GET /.portico/logo` (behind `Require`? No: the landing page needs it
  and the session exists; keep it behind `Require` for consistency, the
  landing page is behind it too) → the logo bytes with the same headers
  as the admin endpoint; 404 without a logo.

`IdPExtID` in `serve`: a cached lookup through `nutanix.Client.ListSAMLIdPs`
filtered by `cfg.PrismCentral.SAMLIdPName` (10 min cache; errors not
cached).

- [ ] **Step 1: Write the failing page tests**

```go
func TestLandingPageContent(t *testing.T)      // name, tagline escaped (<b> → &lt;b&gt;), meta refresh "0;url=/.portico/saml-start", no <script>, Cache-Control no-store; with MinDisplayMs 1500 → "1;url=" (rounded down, minimum 0)
func TestErrorPageContent(t *testing.T)        // status, title, retry link escaped
```

- [ ] **Step 2: Run, implement, run**

Run: `go test ./internal/branding/`
Expected: FAIL to compile, then PASS.

- [ ] **Step 3: Write the failing proxy tests** (the public harness of
  Milestone 1 with a fake Prism Central that records the login-request
  GET/PUT and can fail them)

```go
func TestProvidersPageServesLandingWithSession(t *testing.T)   // session + GET /ui/iam/providers → 200 landing HTML, nothing reaches the upstream
func TestProvidersPageWithoutSessionRedirectsToLogin(t *testing.T)   // Review Focus 4
func TestProvidersPostAndMarkerAreProxied(t *testing.T)        // POST /ui/iam/providers and GET ...?portico=manual reach the upstream (Review Focus 4)
func TestSAMLStartHappyPath(t *testing.T)                      // cookie iam-sessionid=abc; fake GET answers the measured shape; PUT body has loginProviderId "idp-1" and the other fields unchanged; → 302 /api/iam/authn/initiate-login; the PUT carried only the iam-sessionid cookie, no Portico cookie, no API key
func TestSAMLStartFallsBackOnce(t *testing.T)                  // PUT → 400 → 302 /ui/iam/providers?portico=manual; a GET of that URL is proxied (no second landing); missing cookie → same fallback; IdP lookup error → same (Review Focus 1)
func TestSAMLStartRequiresSession(t *testing.T)                // no session → 302 login
func TestLogoServedWithCSP(t *testing.T)                       // session + GET /.portico/logo → bytes, CSP default-src 'none', nosniff
func TestLandingAccessLogRule(t *testing.T)                    // the access log line for the landing page carries rule "landing", saml-start "saml-start" (names the executor picks, asserted)
```

- [ ] **Step 4: Run, implement, run**

Run: `go test ./internal/proxy/`
Expected: FAIL to compile, then PASS.

- [ ] **Step 5: Wire `serve`, suite, commit**

`PrismHTTP`: `&http.Client{Transport: <same transport settings as the upstream, pcTLS>, Timeout: 30 * time.Second, CheckRedirect: return http.ErrUseLastResponse}`.

Run: `go test ./... && go vet ./... && gofmt -l cmd internal`
Expected: PASS, clean.

```bash
git add internal cmd
git commit -m "feat: branded landing page that starts the SAML login itself"
```

---

### Task 4: Branded error pages when the identity provider is down

**Files:**
- Modify: `internal/auth/oidc.go` (`OIDCConfig.ErrorPage func(w, r, status int, title, text, retry string)`),
  `internal/auth/handlers.go` (Login and Callback use it for `Accept: text/html`
  when set, JSON/text otherwise), `internal/auth/oidc_test.go`
- Modify: `internal/proxy/upstream.go` (`UpstreamOptions.ErrorPage`, same
  signature; the ErrorHandler uses it for navigations), `internal/proxy/upstream_test.go`
- Modify: `cmd/portico/serve.go`

- [ ] **Step 1: Write the failing tests**

```go
// auth
func TestLoginIdPDownRendersErrorPageForBrowsers(t *testing.T)   // discovery fails; Accept text/html → 503 HTML with the branded title and a retry link to /.portico/login; Accept application/json → the existing text answer
// proxy
func TestUpstreamErrorPageForNavigations(t *testing.T)   // dead upstream; Accept text/html → 502 HTML with the title; otherwise the JSON of Milestone 1
```

- [ ] **Step 2: Run, implement, run; wire `serve` (both hooks → `pages.Error`)**

Run: `go test ./internal/auth/ ./internal/proxy/ && go test ./...`
Expected: FAIL, then PASS.

- [ ] **Step 3: Commit**

```bash
git add internal cmd
git commit -m "feat: branded error pages for browsers when the identity provider is down"
```

---

### Task 5: Lab acceptance, write-up 13 (part two), documentation

**Files:**
- Modify: `docs/lab/2026-10-18-13-keycloak-theme-and-landing.md`,
  `docs/lab/README.md`, `docs/lab/lab-inventory.md`, `README.md` (Status),
  `docs/superpowers/specs/2026-10-17-portico-design.md` (§9 row 2c done;
  §4.3 step 5 "Milestone 1 leaves the click to the user" updated),
  `deploy/portico.yaml.example`, `lab/README.md`, `lab/auth/probe-local-login.sh`
  (lines: `GET /ui/iam/providers` without session → 302 login; `GET
  /.portico/saml-start` without session → 302 login)

- [ ] **Step 1: Deploy and set the branding from the admin UI** (name,
  tagline, colour, logo). Expected: the save answers without a warning;
  the theme directory on the VM holds the files; the realm's `loginTheme`
  is `portico` (`lab/saml/kc.sh GET /portico-lab | jq .loginTheme`).

- [ ] **Step 2: Browser acceptance as alice (Chrome)**

Fresh window, `https://prism.lab.portico.test/`: Keycloak login page
shows the MSP name, colour and logo → login → Portico landing page (logo,
spinner) → console as alice **without clicking anything**, VMs list
visible. Record the time from the Keycloak submit to the console. Then
Sign Out in the console → the provider page is replaced by the landing
page again → console again (the Keycloak session is still alive).
Then stop Keycloak on the VM (`sudo systemctl stop keycloak`), open a
fresh window on the public URL → the branded "identity provider
unavailable" page with a retry link; start Keycloak again.

- [ ] **Step 3: Failure path** — temporarily set `prism_central.saml_idp_name`
  to a wrong name in the lab YAML, redeploy, open the public URL as
  alice: the landing page falls back to the original provider page with
  both buttons (one fallback, no loop: the network tab shows one
  `/.portico/saml-start` then one `/ui/iam/providers?portico=manual`).
  Restore the name and redeploy.

- [ ] **Step 4: Write-up, docs, probe lines**

Write-up 13 part two with the measurements above; README row; inventory
(theme directory on the VM, realm theme set, branding stored in the
store); `README.md` Status ("Milestone 2c: the tenant never sees a
Nutanix login page"); design §9 row 2c; example YAML (`keycloak.theme_dir`,
`theme_name`, the `manage-realm` role); `lab/README.md` for the two new
probes; the two probe lines.

- [ ] **Step 5: Full verification and commit**

Run: `go test ./... && go vet ./... && gofmt -l cmd internal && lab/test_scripts.sh && source lab/env.sh && lab/auth/probe-local-login.sh`
Expected: all green.

```bash
git add lab docs README.md deploy
git commit -m "lab: landing page and Keycloak theme acceptance (write-up 13)"
```

---

## Self-review notes

- Spec coverage: §4.10 Keycloak login theme (name, tagline, logo,
  background image, accent colour; OTP/update-password/reset pages share
  the login theme) → Task 2 (the background image is dropped: logo and
  colour cover the brand, a background upload doubles the storage and
  the CSP surface for little; ruled here, Marco can ask for it in 2d);
  landing page with logo and progress indicator, no artificial delay,
  configurable minimum display time, selector for several Prism
  Centrals deferred → Task 3 (one Prism Central); optional banner link
  → not built (nothing to point it at yet; the template has the slot
  when a URL setting exists); host names → unchanged. §4.3 step 5
  mechanics and the one-shot fallback → Task 3. §4.7 branding as the
  first store-held setting → Task 1. IdP-down friendlier page (§9 row
  2c) → Task 4.
- Interfaces: `branding.Service` is consumed by `api.New`, `web.Options`
  and `serve`; `branding.Pages` by `proxy.LandingOptions` and the two
  error hooks; `store.Store` gains two methods implemented by `SQLite`
  (Task 1) before anyone consumes them.
- Review Focus 1–5 each name their task's test.
- Proportion: signatures, routes and test names; the only prose
  algorithms are the SAML start sequence and the properties escaping,
  which the signatures do not determine.
