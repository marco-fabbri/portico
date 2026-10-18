# Milestone 2b: Admin UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An MSP administrator opens the internal listener in a browser,
logs in with the break-glass account, and creates, follows, equips with a
first user, disables, retries and deletes tenants from pages that use
exactly the service layer the REST API uses.

**Architecture:** A new package `internal/web` serves server-rendered Go
templates with HTMX (vendored, no CDN: the appliance may be offline) on
the admin listener next to `/api/v1/`. Authentication is one middleware,
`api.RequireAdmin`, that accepts either HTTP Basic (scripts) or the UI's
session cookie, checks the same bcrypt hash, and shares one failure
limiter. Every state-changing form carries a per-session CSRF token. The
pages call `tenant.Service` and `store.Store` directly, mirroring the API
routes one to one (ADR 0003). Before the UI, Task 0 settles the last open
measurement of the public policy (does the console send `Authorization`?)
and Task 1 absorbs the 2a review minors that touch code this milestone
builds on.

**Tech Stack:** Go 1.27 standard library (`html/template`, `embed`,
`net/http`), `golang.org/x/crypto/bcrypt`, HTMX 2.0 vendored under
`internal/web/static/` (BSD-2-Clause, version recorded in the file
header). Existing packages `store`, `tenant`, `api`, `auth`, `proxy`.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md` sections
4.2 (admin authentication), 4.7 (web UI, settings, API boundary), 5, and
milestone 2b in section 9; ADR 0003. Previous plan:
`docs/superpowers/plans/2026-10-18-milestone-2a-tenant-arrival-api.md`
(its "Deferred minors" are the input of Task 1). Evidence:
`docs/lab/2026-10-18-10-bearer-and-v3-users.md` (the open Authorization
question), `docs/lab/2026-10-18-11-tenant-arrival-acceptance.md`.

## Global Constraints

- All text on disk in English (CLAUDE.md), UI strings included. Commit
  messages in English.
- Work on branch `m2b-admin-ui` from `main` (already created, holds the
  design update); integrate through a pull request merged by the executor
  (Marco's delegation of 2026-10-17).
- `CGO_ENABLED=0` build; no JavaScript toolchain, no CDN: HTMX and CSS are
  files embedded in the binary.
- Never log the value of `Authorization`, `Cookie`, any password, session
  id, CSRF token or temporary password. A temporary password is shown once
  in the page that created it and never again (spec §4.4).
- The admin password reaches the binary only through `portico admin
  set-password` on stdin (2a). Until it is set, the UI shows one page
  saying so and the API answers 401 (spec §4.2).
- Session cookie of the UI: `portico_admin`, `HttpOnly`, `SameSite=Strict`,
  `Path=/`, `Secure` when the listener has TLS; 8 h absolute, 15 min idle
  (spec §4.2). CSRF token per session on every POST form.
- Internal listeners off the loopback require TLS (enforced by `Validate`
  since 2a); the UI never relaxes it.
- The pages call the same service layer as the API and never reach
  Prism Central or Keycloak on their own (spec §4.7, ADR 0003). Every page
  action has an API route doing the same thing.
- `go vet ./...`, `gofmt -l cmd internal` and `go test ./...` are clean at
  every commit; `lab/test_scripts.sh` stays green.
- Lab: Prism Central `https://10.23.210.175:9440`, lab VM `10.23.210.190`,
  hosts `prism.lab.portico.test` and `id.lab.portico.test`, admin listener
  `https://prism.lab.portico.test:8443` (lab certificate, `--resolve` or
  `/etc/hosts`), secrets in `~/portico-secrets/`.

## Review Focus

Failure modes the spec implies but a single task's tests might miss; each
has a test in the task named:

1. A POST to a state-changing form without the CSRF token, or with the
   token of another session: expected 403 and no change in the store or
   the fakes (Task 2).
2. Ten wrong passwords in a row from one address on the login form or on
   the API: expected each answer after an increasing delay and the right
   password still accepted afterwards, no permanent lockout (Task 2).
3. A page request after 15 minutes without activity: expected a redirect
   to `/login`, and the form it was carrying is not applied (Task 2).
4. The page that created a user shows the temporary password once:
   expected the tenant detail, the list, the steps and the log never show
   it again (Task 3).
5. The break-glass password is not set: expected every UI path answers
   one page saying how to set it, the login form is not offered, and the
   API answers 401 (Task 2).

---

### Task 0: Measure the console's `Authorization` usage and the provider-selection body

**Files:**
- Modify: `internal/proxy/accesslog.go`, `internal/proxy/public_test.go`
- Create: `docs/lab/2026-10-18-12-console-headers-and-provider-selection.md`
- Modify: `docs/lab/README.md` (row 12)
- Modify (conditional, see Step 6): `internal/proxy/policy.go`,
  `internal/proxy/policy_test.go`, `lab/auth/probe-local-login.sh`,
  `deploy/portico.yaml.example`

**Interfaces:**
- Produces: the access log field `auth_scheme` (lower-case scheme name
  of the first `Authorization` value, `"none"` when absent; never the
  value) and the measured facts the 2c landing page needs.

- [ ] **Step 1: Write the failing test in `public_test.go`**

```go
func TestAccessLogRecordsAuthSchemeOnly(t *testing.T)
// three requests through AccessLog: no header → log line has "auth_scheme":"none";
// "Authorization: Bearer abc.def" → "auth_scheme":"bearer" and the buffer does not contain "abc.def";
// "Authorization: Basic YWJj" → "auth_scheme":"basic" and the buffer does not contain "YWJj"
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/proxy/ -run TestAccessLogRecordsAuthSchemeOnly`
Expected: FAIL (no `auth_scheme` field).

- [ ] **Step 3: Implement the field in `AccessLog`**

`authScheme(r *http.Request) string`: first `Authorization` value,
`strings.Fields`, first field lower-cased, `"none"` when absent or empty.

- [ ] **Step 4: Run the proxy tests, deploy, browse as alice**

Run: `go test ./internal/proxy/ && source lab/env.sh && lab/portico/deploy.sh`
Expected: PASS, `portico ready`.
Then in Chrome as `alice@lab.portico.test`: login, VM list, the VM's
detail, Launch Console (open the popup, wait ten seconds), Projects,
logout. Then:

```
ssh lab@10.23.210.190 "sudo journalctl -u portico --since '-15min' -o cat" | jq -r 'select(.msg=="request" and .host=="prism.lab.portico.test") | .auth_scheme' | sort | uniq -c
```
Expected: the counts per scheme. The fact to record is whether anything
other than `none` appears for the console's own traffic.

- [ ] **Step 5: Capture the provider-selection PUT body**

In the same Chrome session, logged out of Prism Central but with the
Portico session alive (open `https://prism.lab.portico.test/console/`
until the provider page shows): with the `javascript_tool`, wrap
`window.fetch` and `XMLHttpRequest.prototype.send` to `console.log` the
method, URL and body of every request whose URL contains
`login-requests`; click "Log in with portico-lab-keycloak"; read the
console messages. Record the exact PUT body (field names and the
provider identifier it carries) in the write-up, sanitised.

- [ ] **Step 6: Write the write-up and apply the measured rule, test first**

`docs/lab/2026-10-18-12-console-headers-and-provider-selection.md` from
`docs/lab/TEMPLATE.md`; row 12 in `docs/lab/README.md`.

- If the console sent no `Authorization` header at all: extend the deny
  rule to every scheme. Test case in `policy_test.go`
  `{"bearer header", Authorization: "Bearer x", want "deny-authorization"}`
  and `{"digest header", Authorization: "Digest x", want
  "deny-authorization"}` (FAIL first), then `PrismPolicy.Deny` denies any
  non-empty `Authorization` value with rule `deny-authorization` (keep
  `deny-basic-auth` as the rule name when the scheme is basic, so the M1
  probe lines stay valid), one probe line in `probe-local-login.sh`
  expecting 403 for `Authorization: Bearer x`, and a sentence in
  `deploy/portico.yaml.example` next to `policy`.
- If the console did send one (a scheme other than `none` on its own
  calls): no policy change; the write-up names the calls and the scheme,
  and the 2c plan decides.

- [ ] **Step 7: Verify and commit**

Run: `go test ./... && source lab/env.sh && lab/portico/deploy.sh && lab/auth/probe-local-login.sh && lab/test_scripts.sh`
Expected: PASS, `portico ready`, probe exit 0, lab checks PASS.

```bash
git add internal/proxy docs/lab lab/auth deploy
git commit -m "lab: console Authorization usage and provider-selection body (write-up 12)"
```

---

### Task 1: Absorb the 2a review minors that this milestone builds on

**Files:**
- Modify: `internal/tenant/service.go`, `internal/tenant/service_test.go`
- Modify: `internal/api/api.go`, `internal/api/api_test.go`
- Modify: `internal/store/store.go`, `internal/store/tenant.go`, `internal/store/store_test.go`
- Modify: `internal/idp/keycloak/client.go`, `internal/idp/keycloak/client_test.go`
- Modify: `cmd/portico/serve.go`, `internal/proxy/internal_test.go`, `go.mod`, `go.sum`

**Interfaces:**
- Consumes: everything from 2a as is.
- Produces: no signature change; `api.New` keeps its signature.

Each item is one RED→GREEN cycle; the file names its test.

- [ ] **Step 1: `CreateUser` holds the per-tenant lock** (`service_test.go`
  `TestCreateUserIsBusyWhileDeleting`: gate `DeleteUserGroup` with a
  `Gate` on the fake, start `Delete`, `CreateUser` → `ErrBusy`). Fix:
  `acquire`/`release` around the body.

- [ ] **Step 2: `delete_project` parsed with `strconv.ParseBool`**
  (`api_test.go` `TestDeleteProjectFlagParsing`: `?delete_project=1` →
  202 and the project is removed; `?delete_project=yes` → 400). Fix in
  `deleteTenant`.

- [ ] **Step 3: store file mode 0600** (`store_test.go`
  `TestOpenSQLiteSetsFileMode`: after `OpenSQLite` and one write, the
  `.db`, `-wal` and `-shm` files that exist have mode `0600`). Fix:
  `os.Chmod` after open and after the first write (`CreateTenant`,
  `SetAdminPassword` call a `chmodSidecars()` once).

- [ ] **Step 4: UNIQUE violation by driver error code** (`store_test.go`
  `TestCreateTenantExistsUsesErrorCode`: the existing `ErrExists`
  assertion stays; the implementation switches to `errors.As` on
  `*sqlite.Error` with `Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY ||
  SQLITE_CONSTRAINT_UNIQUE` from `modernc.org/sqlite/lib`). The test is
  the same assertion; the RED step is replacing the substring check with
  a wrong code first and watching it fail, then the right codes.

- [ ] **Step 5: Keycloak token refreshed once on 401** (`client_test.go`
  `TestTokenRefreshedOnceOn401`: the fake invalidates `tok-1` after the
  first call and issues `tok-2`; the second `FindGroup` succeeds and the
  token endpoint was called twice). Fix in `do`: on 401 clear the cache
  and retry the call once.

- [ ] **Step 6: `GroupMembers` pages** (`client_test.go`
  `TestGroupMembersPages`: the fake returns 100 members on `first=0`, 3
  on `first=100`; the client returns 103). Fix: loop with
  `first`/`max=100` until a short page.

- [ ] **Step 7: `/healthz` with a Basic header is 200** (`internal_test.go`,
  one request with `Authorization: Basic x` → 200). Likely green at once:
  record that in the ledger, it is the Review Focus 5 assertion of 2a.

- [ ] **Step 8: rename the shadowing variable and tidy**
  `cmd/portico/serve.go`: `store := auth.NewMemoryStore(...)` →
  `sessions`. `go mod tidy`. No behaviour change: the suite is the test.

- [ ] **Step 9: Run the suite and commit**

Run: `go test -race ./internal/tenant/ ./internal/api/ ./internal/store/ ./internal/idp/keycloak/ && go test ./... && go vet ./... && gofmt -l cmd internal`
Expected: PASS, clean.

```bash
git add internal cmd go.mod go.sum
git commit -m "fix: absorb the Milestone 2a review minors"
```

---

### Task 2: Admin session, login page, CSRF, failure limiter

**Files:**
- Create: `internal/api/limiter.go`, `internal/api/limiter_test.go`
- Create: `internal/api/require.go` (replaces `BasicAuth` as the public
  entry point; `BasicAuth` stays as an internal function), `internal/api/require_test.go`
- Create: `internal/web/web.go` (handler, options, template loading),
  `internal/web/session.go` (cookie, CSRF), `internal/web/login.go`,
  `internal/web/templates/layout.html`, `login.html`, `unset.html`,
  `internal/web/static/portico.css`, `internal/web/static/htmx.min.js`
- Create: `internal/web/web_test.go`
- Modify: `internal/proxy/internal.go` (`NewAdmin(log, api, ui http.Handler)`),
  `internal/proxy/internal_test.go`
- Modify: `cmd/portico/serve.go`, `cmd/portico/serve_test.go`

**Interfaces:**
- Consumes: `store.Store.AdminPasswordHash`, `auth.MemoryStore`,
  `auth.NewSessionID`.
- Produces:

```go
package api

// Limiter delays answers to repeated failures from one source address.
type Limiter struct{ /* mu, failures map[string]entry, now func() time.Time */ }
func NewLimiter() *Limiter
func (l *Limiter) Delay(source string) time.Duration   // 0 for the first 3 failures in 10 min, then 1s, 2s, 4s, capped at 8s
func (l *Limiter) Failed(source string)                  // record one failure
func (l *Limiter) Reset(source string)                   // on success
func (l *Limiter) Sweep(now time.Time)                   // drop entries idle for 10 min

// Identity is who authenticated on the admin listener.
type Identity struct{ Name string; Via string }   // Via: "basic" or "session"

// SessionChecker lets the UI's session be accepted by the API middleware.
type SessionChecker interface {
    Identity(r *http.Request) (Identity, bool)
}

// RequireAdmin accepts HTTP Basic against the stored hash (through the
// limiter) or a UI session; otherwise 401 with the Basic challenge for
// API paths, or a redirect to /login for UI paths (isAPI = path starts
// with /api/). When no password is set: 401 for the API, 503 page for the UI.
func RequireAdmin(st store.Store, sessions SessionChecker, lim *Limiter, realm string, log *slog.Logger) func(http.Handler) http.Handler
func IdentityFromContext(ctx context.Context) (Identity, bool)
```

```go
package web

const SessionCookie = "portico_admin"

type Options struct {
    Store    store.Store
    Service  *tenant.Service
    Sessions *auth.MemoryStore   // 8 h / 15 min
    Limiter  *api.Limiter
    Secure   bool                // the listener has TLS: Secure cookie
    Log      *slog.Logger
}
func New(o Options) *Handler            // implements http.Handler and api.SessionChecker
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request)
func (h *Handler) Identity(r *http.Request) (api.Identity, bool)   // valid session cookie → {Name:"admin", Via:"session"}
// routes: GET /login, POST /login (fields user, password, next), POST /logout,
//         GET /static/{file}; everything else of Task 3.
// POST handlers require the hidden field _csrf equal to the session's token, else 403 "invalid CSRF token".
// Login: wrong credentials → lim.Failed(ip), sleep lim.Delay(ip), 200 with the form and "Wrong user or password"; success → lim.Reset, new session, Set-Cookie, 303 to next (a path only, default "/").
// No password set → every path renders unset.html (503) with the set-password command.
```

`layout.html`: header with "Portico", the admin name and a logout button
(form POST with `_csrf`), a `<main>` block, footer with the version.
Styles in `portico.css` (system font, one accent colour, tables with
zebra rows, status badges `creating` grey, `active` green, `failed` red,
`disabled` amber, `deleting` grey). HTMX loaded from `/static/htmx.min.js`.

- [ ] **Step 1: Write the failing limiter tests**

```go
func TestLimiterDelayGrowsAndResets(t *testing.T)   // 3 failures → Delay 0; 4th → 1s; 5th → 2s; 6th → 4s; 8th → 8s (cap); Reset → 0
func TestLimiterIsPerSource(t *testing.T)           // failures on "a" do not delay "b"
func TestLimiterSweepsIdleSources(t *testing.T)     // after 10 min idle, Delay is 0 again
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/api/ -run TestLimiter`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `Limiter`** (fake clock through a `now` field for tests).

- [ ] **Step 4: Run them**

Run: `go test ./internal/api/ -run TestLimiter`
Expected: PASS.

- [ ] **Step 5: Write the failing `RequireAdmin` tests in `require_test.go`**

```go
func TestRequireAdminBasicStillWorks(t *testing.T)          // valid Basic → 200, IdentityFromContext Via "basic"; wrong → 401 + WWW-Authenticate; existing BasicAuth tests move here
func TestRequireAdminAcceptsSession(t *testing.T)           // fake SessionChecker returns an identity → 200 Via "session"; no Basic challenge issued
func TestRequireAdminRedirectsUIPaths(t *testing.T)         // GET /tenants without credentials → 303 to /login?next=/tenants; GET /api/v1/tenants → 401 JSON
func TestRequireAdminDelaysAfterFailures(t *testing.T)      // with a limiter whose clock is fake: 4th wrong Basic from 10.0.0.1 waits lim.Delay (assert the sleep hook was called with 1s); right password afterwards → 200 (Review Focus 2)
func TestRequireAdminNoPasswordSet(t *testing.T)            // empty store: /api/v1/tenants → 401; /tenants → 503 (Review Focus 5)
```

(`RequireAdmin` takes the sleep through a package variable
`sleep = time.Sleep` that tests replace.)

- [ ] **Step 6: Run them**

Run: `go test ./internal/api/ -run TestRequireAdmin`
Expected: FAIL to compile.

- [ ] **Step 7: Implement `RequireAdmin`, keep `BasicAuth` as its Basic branch**

Source address: `r.RemoteAddr` host part. `api.New` unchanged; `serve`
switches to `RequireAdmin`.

- [ ] **Step 8: Run the api tests**

Run: `go test ./internal/api/`
Expected: PASS.

- [ ] **Step 9: Write the failing web tests in `web_test.go`** (real
  store on a temp file, `tenanttest` fakes, `auth.NewMemoryStore(8h, 15m)`)

```go
func TestLoginPageAndSuccess(t *testing.T)            // GET /login → 200 with the form and a hidden _csrf; POST /login right password → 303 to "/", Set-Cookie portico_admin HttpOnly SameSite=Strict (Secure when Options.Secure); Identity(r with cookie) → admin/session
func TestLoginWrongPasswordShowsErrorAndFails(t *testing.T)   // POST wrong → 200, body has "Wrong user or password", no Set-Cookie, limiter.Failed called
func TestLoginNextIsPathOnly(t *testing.T)            // next=https://evil.test → 303 to "/"; next=/tenants/x → 303 to /tenants/x
func TestCSRFRequiredOnPost(t *testing.T)             // POST /logout with the cookie but no _csrf → 403; with another session's token → 403; with the right token → 303 and the session is gone (Review Focus 1)
func TestSessionIdleExpires(t *testing.T)             // sessions store with idle 1ms: after sleeping 5ms, GET / → 303 /login (Review Focus 3)
func TestUnsetPasswordPage(t *testing.T)              // empty store: GET /login → 503, body contains "portico admin set-password", no form (Review Focus 5)
func TestStaticServed(t *testing.T)                   // GET /static/htmx.min.js → 200, Content-Type javascript, Cache-Control max-age set
func TestNoSecretInLog(t *testing.T)                  // after a wrong and a right login the log buffer has neither password, nor the session id, nor the CSRF token
```

- [ ] **Step 10: Run them**

Run: `go test ./internal/web/`
Expected: FAIL to compile.

- [ ] **Step 11: Implement `internal/web`** (templates parsed once at
  `New` from the embedded FS; `html/template` auto-escaping; the CSRF
  token is 32 random bytes base64url kept in a new `CSRF string` field of
  `auth.Session`, generated at login), vendor `htmx.min.js` 2.0.x and
  write `portico.css`.

- [ ] **Step 12: Run the web tests**

Run: `go test ./internal/web/`
Expected: PASS.

- [ ] **Step 13: Mount in the admin listener, test first**

`internal_test.go` `TestAdminMountsUIAtRoot`: `NewAdmin(log, api, ui)`
routes `/` and `/login` to `ui`, `/api/v1/` to `api`, `/healthz` open.
RED, then `NewAdmin(log *slog.Logger, api, ui http.Handler)`. `serve`:
`web.New(...)` with `Secure: internalTLS != nil`, `RequireAdmin(st, ui,
lim, "portico", log)` wraps both `api` and `ui`; the limiter and the
admin sessions are swept with the tenant sessions every minute.
`serve_test.go` `TestServeServesLoginPage`: GET `/login` on the admin
listener → 503 (no password set) with the set-password text.

- [ ] **Step 14: Run the suite and commit**

Run: `go test ./... && go vet ./... && gofmt -l cmd internal`
Expected: PASS, clean.

```bash
git add internal/api internal/web internal/proxy internal/auth cmd/portico
git commit -m "feat: admin login with session, CSRF and failure limiter on the internal listener"
```

---

### Task 3: Tenant pages

**Files:**
- Create: `internal/web/tenants.go`, `internal/web/templates/tenants.html`,
  `tenant_new.html`, `tenant.html`, `tenant_steps.html` (fragment),
  `tenant_user_created.html`
- Modify: `internal/web/web.go` (routes), `internal/web/web_test.go`

**Interfaces:**
- Consumes: `tenant.Service` (`Create`, `Retry`, `Delete`, `Disable`,
  `Enable`, `CreateUser`, `Visibility`), `store.Store` (`ListTenants`,
  `GetTenant`, `ListSteps`), `tenant.ValidSlug`.
- Produces: the routes below; no new Go API.

Routes (all behind `RequireAdmin`; POST forms carry `_csrf`):

| route | does | API twin |
|---|---|---|
| `GET /` | tenant table: id, display name, status badge, project, updated; rows in `creating`/`deleting` make the table poll `GET /?partial=rows` every 10 s with `hx-trigger="every 10s"` | `GET /api/v1/tenants` |
| `GET /tenants/new` | form: slug, display name, contacts | |
| `POST /tenants` | create → 303 `/tenants/{id}`; invalid slug → 400 form with the message | `POST /api/v1/tenants` |
| `GET /tenants/{id}` | detail: fields, steps table, visibility result (ok or findings), users form, buttons disable/enable/retry/delete (delete has a checkbox "delete the project too") | `GET /api/v1/tenants/{id}`, `/steps`, `/visibility` |
| `GET /tenants/{id}/steps` | the steps fragment, polled every 5 s while status is `creating`/`deleting` | `GET /api/v1/tenants/{id}/steps` |
| `POST /tenants/{id}/users` | create user → 201 page `tenant_user_created.html` showing the temporary password once with a "copied, continue" link | `POST /api/v1/tenants/{id}/users` |
| `POST /tenants/{id}/disable`, `/enable`, `/provision` | → 303 back to the detail; errors shown as a banner (409 text) | same paths on the API |
| `POST /tenants/{id}/delete` | field `delete_project` checkbox → 303 `/` | `DELETE /api/v1/tenants/{id}` |

Visibility is computed on the detail page only when the tenant is
`active` or `disabled` (it calls Prism Central); otherwise the section
says "available once the tenant is active".

- [ ] **Step 1: Write the failing tests**

```go
func TestTenantListShowsRowsAndPollsWhileCreating(t *testing.T)   // one active, one creating → both rows, badge classes, hx-trigger present; none creating → no hx-trigger
func TestCreateTenantFormAndRedirect(t *testing.T)                // POST /tenants slug acme → 303 /tenants/acme; Wait; detail shows "active" and 7 steps
func TestCreateTenantInvalidSlugShowsForm(t *testing.T)           // slug "Acme" → 400 with the form and the ErrInvalidSlug text; no row
func TestDetailShowsVisibilityFindings(t *testing.T)              // fake with a membership in another project → page lists the finding text
func TestUserCreatedPageShowsPasswordOnce(t *testing.T)           // POST users → 201 with the password; GET detail, GET /, GET steps and the log buffer lack it (Review Focus 4)
func TestDisableEnableRetryDeleteForms(t *testing.T)              // POST disable → 303 and status disabled; enable → active; delete with delete_project=on → 303 /, row gone after Wait, project gone in the fake; retry on active → detail with "not allowed" banner
func TestAllPostsRequireCSRF(t *testing.T)                        // each POST route without _csrf → 403 and the store unchanged
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/web/ -run 'TestTenant|TestCreateTenant|TestDetail|TestUserCreated|TestDisable|TestAllPosts'`
Expected: FAIL (404 on the routes).

- [ ] **Step 3: Implement the pages**

Status badge class `status-<status>`; the steps fragment reuses the same
template for the full page and the HTMX partial (`?partial=` or the
`HX-Request` header selects the fragment).

- [ ] **Step 4: Run the web tests and the suite**

Run: `go test ./internal/web/ && go test ./... && go vet ./... && gofmt -l cmd internal`
Expected: PASS, clean.

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat: tenant pages on the admin listener"
```

---

### Task 4: Lab acceptance in the browser, write-up 12 (part two), documentation

**Files:**
- Modify: `lab/portico/deploy.sh` (prints the admin UI URL at the end),
  `lab/README.md`, `docs/lab/2026-10-18-12-console-headers-and-provider-selection.md`
  (or a separate write-up 13 if the Task 0 one is long: executor's call),
  `docs/lab/README.md`, `docs/lab/lab-inventory.md`, `README.md` (Status),
  `docs/superpowers/specs/2026-10-17-portico-design.md` (§9 row 2b done),
  `deploy/portico.yaml.example` (comment on the UI URL)

- [ ] **Step 1: Deploy and check the UI is served**

Run: `source lab/env.sh && lab/portico/deploy.sh && curl -s -o /dev/null -w '%{http_code}\n' --cacert ~/portico-secrets/lab-ca.crt --resolve prism.lab.portico.test:8443:10.23.210.190 https://prism.lab.portico.test:8443/login`
Expected: `portico ready`, `200`.

- [ ] **Step 2: Browser acceptance (Chrome, by the executor)**

`https://prism.lab.portico.test:8443/`: redirected to `/login`; login as
`admin` with `~/portico-secrets/portico-admin-pw` (a wrong password first:
the error banner); tenant list empty; "New tenant": slug `tenant-b`,
display name "Tenant B (lab)"; the detail page polls until `active` with
seven green steps and visibility ok; create user
`carol@lab.portico.test`, read the one-time password page; disable (badge
amber), enable; delete with the project; the list is empty again. Then
`lab/tenants/acceptance.sh --delete` removes carol. Record every screen
in the write-up, with what the operator sees at each step and the time
the page took to reach `active`.

- [ ] **Step 3: Write-up and documentation**

Write-up: the browser acceptance (second half of write-up 12, or 13).
`docs/lab/README.md` row. `lab-inventory.md`: nothing new on the VM
except the UI being served (one sentence on the `portico.service` row).
`lab/README.md`: `deploy.sh` prints the UI URL. `README.md` Status:
"Milestone 2b: tenants are managed from the admin UI in the lab".
Design §9 row 2b: done, date, write-up. `deploy/portico.yaml.example`:
one comment next to `admin_listen` ("the admin UI and API; off the
loopback it needs internal.tls").

- [ ] **Step 4: Full verification**

Run: `go test ./... && go vet ./... && gofmt -l cmd internal && lab/test_scripts.sh && source lab/env.sh && lab/auth/probe-local-login.sh`
Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add lab docs README.md deploy
git commit -m "lab: admin UI acceptance in the browser (write-up 12)"
```

---

## Self-review notes

- Spec coverage: §4.2 admin authentication (break-glass, session cookie
  parameters, CSRF, failure delay shared with Basic, unset-password
  behaviour) → Task 2; §4.7 UI scope for tenants (list, create, status
  with visibility, users) → Task 3; "the API is the boundary" → Task 3's
  route table names the API twin of every page action; setup wizard,
  system status, branding → Milestones 2c/2d (design §9). §9 row 2b
  "console Authorization usage measured" → Task 0; "2a review minors
  absorbed" → Task 1 (the ones touching code this milestone builds on;
  the PC HTTP timeout, the deletion tombstone, the Disable-session note
  and the FindGroup subgroup case stay deferred and are listed again at
  the end of the execution report).
- Interfaces: `api.RequireAdmin` replaces `api.BasicAuth` in `serve`;
  `web.Handler` satisfies `api.SessionChecker`; `proxy.NewAdmin` gains a
  third argument (Task 2 Step 13 updates its test).
- Review Focus 1–5 each name their task's test.
- Proportion: route table, signatures and test names; no bodies.
