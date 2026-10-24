# Milestone 5: Backlog Sweep Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close, each with a test, the findings of `docs/backlog.md` that
must not ship in v0.1.0, as selected with Marco on 2026-10-24, and leave
the rest in the backlog as the input of the known-limits section of
Milestone 6.

**Architecture:** Small changes in the package that owns each finding,
grouped in five areas, one branch and one pull request each, all cut
from `main`: the public path (`m5-public-path`), Prism Central
robustness (`m5-prism-retries`), the appliance (`m5-appliance`), the
admin UI (`m5-admin-ui`) and the portal name (`m5-portal-name`). The
areas share no code, so the branches merge in any order; the last
task of each area updates `docs/backlog.md` for its own items only.

**Tech Stack:** Go 1.27 standard library and the existing packages;
bash for `deploy/install.sh` and `image/scripts/harden.sh`; GitHub
Actions. Lab: the appliance on NTNX-01 (`dev-fc5382a` today), sites
`site-1` and `AZ-US-NYC1`.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md` §9
(Milestone 5: "the findings in `docs/backlog.md` that must not ship in a
release closed with a test each"), §4.2 (listeners), §4.9 (appliance
image: "SSH key-only, internal interface only"), §5; `docs/backlog.md`
(the findings, quoted in each task); the sites design
`docs/superpowers/specs/2026-10-20-sites-design.md` for the portal name.

## Global Constraints

- All text on disk in English; Italian only in chat with Marco.
- Commits: `git -c user.email=9093189+marco-fabbri@users.noreply.github.com commit`,
  message ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
  Pull request bodies end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- Pushes, pull requests and merges only with Marco's OK; merge with
  `gh pr merge N --rebase --delete-branch`, never a local merge.
- Never log, print or return a password, API key, client secret, token,
  session id, CSRF token or cookie value.
- `go test ./...`, `go vet ./...`, `gofmt -l cmd internal` clean, and
  `lab/test_scripts.sh` PASS before every commit that touches them. CI's
  shellcheck is older than the local one: no `A && B || C`.
- The public path fails closed; local Prism Central accounts never log in
  from outside.
- Lab passwords only from `~/portico-secrets/` (NYC1: `nyc1-pw`, never
  `prism-pw`) through scripts; browser logins are Marco's.

## Review Focus

1. An `Authorization` header with an empty or blank value on the public
   path must be refused before the session check, like any other value:
   Task 1 asserts `deny-authorization` for `""` and `"   "`.
2. A browser asked to retry `//evil.example/x` must get a link to `/`,
   never to another host: Task 2 asserts it through the 502 page.
3. A provisioning step that meets Prism Central's intermittent 500 must
   not fail the tenant, but a 400 (a real refusal) must fail at once
   without a pause: Task 5 asserts both.
4. The disconnect of a tenant whose resource group Prism Central refuses
   as "not empty" (10018) for minutes must wait inside the operation
   timeout and then fail with that reason, not loop forever: Task 6
   asserts both ends.
5. A settings document stored before the rename (`prism_host`) must load
   with the same name in `portal_host`, and a document carrying both
   keys with different values must be refused: Task 13 asserts both.

---

## Area 1: public path (branch `m5-public-path`)

### Task 1: An empty `Authorization` header is refused

Backlog: "An empty `Authorization` value passes the public policy while
the docs say 'every header' (deny on presence, `internal/proxy`)."

**Files:**
- Modify: `internal/proxy/policy.go` (`PrismPolicy.Deny`)
- Test: `internal/proxy/policy_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestEmptyAuthorizationIsDenied(t *testing.T) {
	for _, v := range []string{"", "   "} {
		r := httptest.NewRequest("GET", "/console/", nil)
		r.Header["Authorization"] = []string{v}
		if rule, denied := (PrismPolicy{}).Deny(r); !denied || rule != "deny-authorization" {
			t.Fatalf("%q: rule %q denied %v", v, rule, denied)
		}
	}
}
```

- [ ] **Step 2: Run it** — `go test ./internal/proxy -run TestEmptyAuthorizationIsDenied -v`. Expected: FAIL (not denied).
- [ ] **Step 3: Implement** — a present header with no fields sets `other = true` instead of `continue`; Basic keeps its own rule.
- [ ] **Step 4: Run** `go test ./internal/proxy`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(proxy): an empty Authorization header is refused like any other`.

### Task 2: The retry link and the error pages stay on this host

Backlog: "The retry link built from `RequestURI` can start with `//`
(guard like `safeNext`, `internal/proxy`)."

**Files:**
- Modify: `internal/branding/pages.go` (`ErrorSwitch`)
- Test: `internal/branding/pages_test.go`, `internal/proxy/upstream_test.go`

**Interfaces:**
- Produces: `func safeRetry(retry string) string` in package `branding`:
  `""` stays `""`; a value that does not start with one `/`, starts with
  `//`, or holds `\`, CR or LF becomes `/`; anything else is unchanged.
  The guard sits in the page, so every caller (upstream 502, identity
  provider down, site pages) is covered.

- [ ] **Step 1: Write the failing tests**

```go
// pages_test.go
func TestErrorRetryNeverLeavesTheHost(t *testing.T) {
	for in, want := range map[string]string{"//evil.example/x": `href="/"`, "/\\evil": `href="/"`, "/console/#/x": `href="/console/#/x"`} {
		w := httptest.NewRecorder()
		p.Error(w, httptest.NewRequest("GET", "/", nil), 502, "Down", "down", in)
		if !strings.Contains(w.Body.String(), want) { t.Fatalf("%q: %s", in, w.Body) }
	}
}
// upstream_test.go
func TestUpstreamErrorPageRetryOnDoubleSlashPath(t *testing.T) {
	// upstream unreachable, ErrorPage set to a branding.Pages, GET "//evil.example/x" with Accept text/html
	// assert the body links "Try again" to "/" and does not contain "//evil.example"
}
```

- [ ] **Step 2: Run them.** Expected: FAIL (the link is `//evil.example/x`).
- [ ] **Step 3: Implement** `safeRetry` and apply it in `ErrorSwitch`.
- [ ] **Step 4: Run** `go test ./internal/branding ./internal/proxy`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(branding): the retry link of an error page stays on this host`.

### Task 3: An upstream `Set-Cookie` never sets a Portico or stash cookie

Backlog (Milestone 4 minors): "an upstream `Set-Cookie` carrying a stash
or Portico cookie name is not filtered".

**Files:**
- Modify: `internal/proxy/upstream.go` (`NewUpstream`: a `ModifyResponse`)
- Test: `internal/proxy/upstream_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestUpstreamSetCookieWithAPorticoNameIsDropped(t *testing.T) {
	// backend answers three Set-Cookie: <a StripCookies name>=x, <stashPrefix>site-1=y, NTNX_IGW_SESSION=z
	// proxy built with StripCookies: auth.PorticoCookies()
	// assert the response carries exactly one Set-Cookie, the NTNX_IGW_SESSION one
}
```

- [ ] **Step 2: Run it.** Expected: FAIL (three Set-Cookie).
- [ ] **Step 3: Implement** a `ModifyResponse` that parses each `Set-Cookie` with `http.ParseSetCookie` and drops the ones whose name is in `strip` or starts with `stashPrefix` (an unparsable line is dropped too), logging the name only at Warn.
- [ ] **Step 4: Run** `go test ./internal/proxy`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(proxy): an upstream Set-Cookie cannot set a Portico or stash cookie`.

### Task 4: Sessions: a second login retires the first; the no-site page signs out

Backlog: "A second login does not retire the first session
(`internal/auth`)" and (Milestone 4 minors) "the no-site page has no
sign-out link (a transient Keycloak error strands the user for the
session)".

**Files:**
- Modify: `internal/auth/handlers.go` (`Callback`), `internal/branding/pages.go`, `internal/branding/templates/error.html`, `internal/proxy/sites.go` (`noSite`)
- Test: `internal/auth/oidc_test.go`, `internal/proxy/sites_test.go`

**Interfaces:**
- Produces: `func (p *Pages) ErrorSignOut(w, r, status int, title, text string)`
  in `branding`: the error page with a "Sign out" button to
  `/.portico/logout` (field `SignOut bool` in `pageData`) and no retry.

- [ ] **Step 1: Write the failing tests**

```go
// oidc_test.go
func TestSecondLoginRetiresTheFirstSession(t *testing.T) {
	// log in once (helper of the file), keep cookie A; run the callback again with cookie A on the request
	// assert store.Get(A) is gone and the new cookie differs from A
}
// sites_test.go
func TestNoSitePageOffersSignOut(t *testing.T) {
	// a session whose tenant has no placement, GET / with Accept text/html
	// assert 403, body contains `href="/.portico/logout"`
}
```

- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement** — the callback deletes the session named by the request's session cookie before creating the new one; `noSite` calls `ErrorSignOut` when `Pages` is set.
- [ ] **Step 4: Run** `go test ./internal/auth ./internal/proxy ./internal/branding`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(auth): a new login retires the session it replaces; the no-site page signs out`, then remove the four public-path lines from `docs/backlog.md` (and the stash line from the Milestone 4 minors) in `docs: the public-path findings closed by Milestone 5`.

---

## Area 2: Prism Central robustness (branch `m5-prism-retries`)

### Task 5: A step that meets Prism Central's transient 500 is tried again

Backlog: "Prism Central pc.7.6 answers 500 'Failed to store new cookie
in IDF' intermittently for minutes after identity deletions (write-up
19) … Retry a 500 from Prism Central once or twice with a pause before
failing the step (`internal/tenant`)." Write-up 25 measured
`500 "failed to get client"` the same way.

**Files:**
- Modify: `internal/tenant/provision.go` (`record`), `internal/tenant/service.go` (`Config`), `internal/nutanix/client.go`
- Test: `internal/tenant/service_test.go`, `internal/nutanix/client_test.go`

**Interfaces:**
- Produces: `func IsTransient(err error) bool` in `nutanix`: an
  `*APIError` with `Task == false` and `Status` 500, 502 or 503.
  `Config.RetryPauses []time.Duration` in `tenant`, default
  `{5 * time.Second, 15 * time.Second}`: `record` runs a step again after
  each pause while its error `IsTransient`, stops at once on any other
  error or on the context's end, and records one step line with the last
  outcome. Steps are idempotent (each reads before it creates), so a
  repeat is safe.

- [ ] **Step 1: Write the failing tests**

```go
// client_test.go
func TestIsTransient(t *testing.T) {
	// 500, 502, 503 → true; 400, 404, 504, a Task failure with status 500, a non-APIError → false
}
// service_test.go
func TestStepRetriesPrismCentralTransient500(t *testing.T) {
	// cfg.RetryPauses = {time.Millisecond, time.Millisecond}
	// pc.FailCall("CreateUserGroup", 1, &nutanix.APIError{Status: 500, Message: "Failed to store new cookie in IDF"})
	// create a tenant on one site, Wait(); assert status active and the saml-group step recorded once, ok
}
func TestStepDoesNotRetryARefusal(t *testing.T) {
	// FailCall("CreateUserGroup", 1, &nutanix.APIError{Status: 400}); assert failed and the fake saw one CreateUserGroup call
}
```

- [ ] **Step 2: Run them.** Expected: FAIL (`IsTransient` undefined; tenant failed).
- [ ] **Step 3: Implement** as in Interfaces; each repeat logs `tenant step retried` at Info with tenant, site, step and status (never the message).
- [ ] **Step 4: Run** `go test ./internal/tenant ./internal/nutanix`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(tenant): a step that meets Prism Central's transient 500 is tried again`.

### Task 6: The resource group refused as "not empty" is retried within the operation timeout

Backlog: "Prism Central refused to delete the resource group ('not
empty', 10018) for between 3 and 10 minutes on 2026-10-20 (write-up
21) … should retry that refusal with a backoff within
`operation_timeout`." Write-up 21 saw it as a gateway 504 after about
60 s, its task saying `failed to delete Nutanix Resource Group as it is
not empty.: 10018`.

**Files:**
- Modify: `internal/tenant/provision.go` (the `resource-group` step of `removePlacement`), `internal/tenant/service.go` (`Config`)
- Test: `internal/tenant/service_test.go`

**Interfaces:**
- Produces: `Config.NotEmptyBackoff time.Duration`, default 30 s, doubling
  to at most 2 minutes. `func notEmptyYet(err error) bool` in `tenant`:
  an `*nutanix.APIError` with status 504, or a task failure whose message
  contains `10018`. The step deletes each resource group again after the
  backoff while `notEmptyYet`; when the context ends it fails with the
  last error wrapped as `resource group still refused as not empty after
  <elapsed>: <err>`.

- [ ] **Step 1: Write the failing tests**

```go
func TestDisconnectWaitsForTheResourceGroup(t *testing.T) {
	// NotEmptyBackoff 1ms; FailCall("DeleteResourceGroup", 1 and 2, &nutanix.APIError{Status: 504})
	// disconnect, Wait(); assert the placement is gone
}
func TestDisconnectGivesUpOnTheResourceGroupAtTheTimeout(t *testing.T) {
	// OperationTimeout 50ms, NotEmptyBackoff 5ms, DeleteResourceGroup always a Task failure "…not empty.: 10018"
	// assert remove-failed with LastError containing "still refused as not empty"
}
```

- [ ] **Step 2: Run them.** Expected: FAIL (remove-failed at the first 504).
- [ ] **Step 3: Implement** as in Interfaces (`select` on `ctx.Done()` and a timer).
- [ ] **Step 4: Run** `go test ./internal/tenant`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(tenant): the resource group refused as not empty is retried within the operation timeout`.

### Task 7: `CreateUser` takes the tenant lock before it reads the tenant

Backlog: "`CreateUser` takes no per-tenant lock and reads the status
before taking the lock (`internal/tenant`)". The lock is there today;
the read is still before it, so a user can be added to a tenant being
deleted.

**Files:**
- Modify: `internal/tenant/service.go` (`CreateUser`)
- Test: `internal/tenant/service_test.go`

- [ ] **Step 1: Write the failing test** `TestCreateUserReadsTheStatusUnderTheLock`: with the tenant's lock held (`s.acquire(id)`) and its stored status `deleting`, `CreateUser` answers `ErrBusy` (today `ErrState`, which proves the status was read before the lock).
- [ ] **Step 2: Run it.** Expected: FAIL (`ErrState`).
- [ ] **Step 3: Implement** — acquire, then `GetTenant` and the status check.
- [ ] **Step 4: Run** `go test ./internal/tenant`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(tenant): CreateUser reads the tenant under its lock`, then update `docs/backlog.md`: remove the three lines, and the Keycloak 401 line, which the client already handles (`keycloak.Client.do` drops the token and retries once), in `docs: the Prism Central findings closed by Milestone 5`.

---

## Area 3: the appliance (branch `m5-appliance`)

### Task 8: SSH listens on the internal interface when there are two

Design §4.9: "SSH key-only, internal interface only." Backlog (Milestone
3 minors): "the kickstart opens SSH on every interface". The console
already moves the admin listener to the internal interface (two
interfaces, the one without a gateway); SSH follows the same rule, so a
single-interface appliance keeps SSH on its one address, as 8443 does.

**Files:**
- Modify: `internal/console/console.go` (`internalAdmin`)
- Test: `internal/console/console_test.go`
- Docs: `docs/install.md` ("What listens where", row 22)

**Interfaces:**
- Produces: the console writes `/etc/ssh/sshd_config.d/10-portico-listen.conf`
  (mode 0600) holding `ListenAddress <internal address>` and runs
  `sshd -t` then `systemctl reload sshd`; when `sshd -t` fails it removes
  the file again and prints `SSH not moved: …`. The file is rewritten at
  every network change, so a new internal address follows.

- [ ] **Step 1: Write the failing tests** `TestTwoInterfacesMoveSSHToTheInternalOne` (fake sys records the file `ListenAddress 192.168.10.5` and the exec calls `sshd -t`, `systemctl reload sshd`) and `TestSSHNotMovedWhenTheConfigIsRefused` (fake `sshd -t` fails: the file is removed, the message printed).
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement** inside `internalAdmin`, after the admin listener.
- [ ] **Step 4: Run** `go test ./internal/console`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(console): SSH listens on the internal interface when there are two`.

### Task 9: `install.sh` runs from the release tree and restarts what changed

Backlog (Milestone 3 minors): "`install.sh` copies `$0` (breaks under
`curl | bash`, use `BASH_SOURCE`); a second `install.sh` run does not
restart PostgreSQL and Keycloak when their quadlets changed."

Ruling for the plan: `curl | bash` cannot work in any form, because the
script installs the quadlets and units beside it; the script resolves
itself with `BASH_SOURCE`, refuses with `install.sh: run it from the
unpacked release: <dir>/quadlets is missing` when its tree is not there,
and the install guide says to unpack the release first.

**Files:**
- Modify: `deploy/install.sh`
- Test: `lab/test_scripts.sh`

**Interfaces:**
- Produces: `put` records the destination in `CHANGED` when the file was
  absent or differs (`cmp -s`); after `daemon-reload`, with `START=1`,
  `postgres.service` and `keycloak.service` are restarted when their
  quadlet or `portico.network` changed and started otherwise. In render
  mode the script logs `would restart: postgres keycloak` (or nothing),
  which the tests read.

- [ ] **Step 1: Write the failing checks** in `lab/test_scripts.sh`: running `bash deploy/install.sh` copied alone into a temp dir with `--render-only` fails with the "unpacked release" line; rendering twice into one root logs no `would restart`; editing the rendered `keycloak.container` and rendering again logs `would restart: postgres keycloak`; the rendered `/usr/local/sbin/portico-install` equals `deploy/install.sh`.
- [ ] **Step 2: Run** `lab/test_scripts.sh`. Expected: those checks FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `lab/test_scripts.sh` and `shellcheck deploy/install.sh`. Expected: PASS, clean.
- [ ] **Step 5: Commit** `fix(install): run from the release tree; restart PostgreSQL and Keycloak when their quadlets change`.

### Task 10: The image build fails on an OpenSCAP regression

Backlog (Milestone 3 minors): "`harden.sh` never fails the build on an
OpenSCAP regression".

**Files:**
- Create: `image/oscap/expected-failures.txt` (one rule id per line, the
  rules failing today with their reason as a `#` comment, from the last
  CI build's `oscap-results.xml`)
- Modify: `image/scripts/harden.sh`, `image/oscap/README.md`
- Test: `lab/test_scripts.sh`

**Interfaces:**
- Produces: `harden.sh` exits 1 when a rule fails that is not in
  `expected-failures.txt`, naming it (`harden: new failing rule: <id>`),
  and prints `harden: expected failure now passes: <id>` (no failure) for
  a listed rule that passes. The comparison is a function
  `oscap_regressions RESULTS EXPECTED` the test sources with a fixture.

- [ ] **Step 1: Write the failing check**: a fixture results file with one listed and one unlisted failing rule makes `oscap_regressions` print the unlisted one and return 1; with only listed failures it returns 0.
- [ ] **Step 2: Run** `lab/test_scripts.sh`. Expected: FAIL.
- [ ] **Step 3: Implement**; fill `expected-failures.txt` from the latest image build artefact (`gh run download` of the last `image.yml` run).
- [ ] **Step 4: Run** `lab/test_scripts.sh`, `shellcheck image/scripts/harden.sh`. Expected: PASS.
- [ ] **Step 5: Commit** `fix(image): the build fails on a new OpenSCAP failure`.

### Task 11: `portico.service` hardening and the workflows' supply chain

Backlog: "`portico.service` could add `RestrictAddressFamilies`,
`SystemCallFilter`, `RestrictNamespaces`, `ProtectHostname`,
`ProtectClock`, `ProtectProc`, `UMask`"; "Store file created with the
umask, not chmod 0600 (plus `-wal`/`-shm`)" (the store chmods its files
today; `UMask=0077` closes the window before the chmod); (Milestone 3
minors) "the image workflow gives the `contents: write` token to
Packer's plugin getter and pins actions by tag".

**Files:**
- Modify: `deploy/portico.service`, `.github/workflows/ci.yml`, `.github/workflows/image.yml`
- Test: `lab/test_scripts.sh`

**Interfaces:**
- Produces: `portico.service` gains `UMask=0077`,
  `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK`,
  `SystemCallFilter=@system-service`, `SystemCallArchitectures=native`,
  `RestrictNamespaces=yes`, `ProtectHostname=yes`, `ProtectClock=yes`,
  `ProtectProc=invisible`, `ProtectKernelLogs=yes`, `MemoryDenyWriteExecute=yes`.
  Every `uses:` names a full commit SHA with the tag as a comment;
  `image.yml` gives `contents: write` only to the job that publishes the
  release, the build job runs with `contents: read`.

- [ ] **Step 1: Write the failing checks** in `lab/test_scripts.sh`: the unit carries each directive above; no `uses:` line in `.github/workflows/` lacks a 40-hex SHA; `contents: write` appears only under the release job.
- [ ] **Step 2: Run** `lab/test_scripts.sh`. Expected: FAIL.
- [ ] **Step 3: Implement**; resolve each SHA with `gh api repos/<owner>/<action>/commits/<tag> --jq .sha`; `systemd-analyze verify` is not available on the Mac, so the unit is checked on the appliance in Step 5.
- [ ] **Step 4: Run** `lab/test_scripts.sh`. Expected: PASS. Push the branch only with Marco's OK; CI green proves the pinned actions resolve.
- [ ] **Step 5: Lab** (announced in one line): install the unit on the appliance, `systemctl daemon-reload && systemctl restart portico`, then `systemd-analyze security portico` (record the score before and after), `/healthz` 200, the admin login page, a site status POST, and the journal free of `SECCOMP`/`audit` denials for portico. Revert the unit on the appliance if any fails, and drop the directive that caused it with a ruling.
- [ ] **Step 6: Commit** `fix(deploy): tighter portico.service sandbox; actions pinned by commit`, then `docs: the appliance findings closed by Milestone 5` (backlog lines; `docs/install.md` row 22).

---

## Area 4: the admin UI (branch `m5-admin-ui`)

### Task 12: The CSP holds, templates fail cleanly, notices come from the server, the password page is strict

Backlog: "CSP `default-src 'self'` blocks the four inline style
attributes and HTMX's indicator style"; "Template errors after headers
are sent: render into a buffer, 500 on error"; "Unused reflected
`?notice=` banner" (it is used now, by three redirects, and still
prints any text from the URL as a Portico message); (Milestone 3 minors)
"the password page accepts the current password as the new one and
keeps the session"; "A POST after idle expiry carries `next=<POST
path>` and lands on a 405 after login: set `next` for GET/HEAD only".

**Files:**
- Modify: `internal/web/templates/{tenant_user_created,tenant_new,site_forgotten,password}.html`, `internal/web/templates/layout.html`, `internal/web/static/portico.css`, `internal/web/web.go` (`render`), `internal/web/site_setup.go`, `internal/web/sites.go`, `internal/web/password.go`, `internal/api/require.go`, `internal/auth/session.go`
- Test: `internal/web/web_test.go`, `internal/web/settings_test.go`, `internal/api/require_test.go`

**Interfaces:**
- Produces: CSS class `.card.narrow.spaced` (`margin-top: 1rem`);
  `<meta name="htmx-config" content='{"includeIndicatorStyles":false}'>`
  in the layout. `func (h *Handler) notice(w http.ResponseWriter, text string)`
  sets cookie `portico_admin_notice` (Path `/`, HttpOnly, Secure as the
  session cookie, SameSite Strict, MaxAge 60, value URL-encoded);
  `render` shows it once and expires it; `?notice=` is ignored.
  `render` executes into a `bytes.Buffer` and answers a plain 500 when
  the template fails. `MemoryStore.DeleteSubject(subject string) int`
  in `auth`; a password change deletes every `admin` session, then
  creates a new one for this browser. The admin `Require` sets `next`
  only for GET and HEAD.

- [ ] **Step 1: Write the failing tests**

```go
func TestPagesHaveNoInlineStyle(t *testing.T)           // every template file: no `style="`; layout has the htmx-config meta
func TestTemplateErrorIsA500(t *testing.T)               // a page whose data makes the template fail → 500, body has no partial HTML
func TestNoticeComesFromTheServerOnly(t *testing.T)      // GET /?notice=Call+us → no banner; after the pin redirect the next GET shows "Certificate pinned" once, the GET after it nothing
func TestPasswordCannotStayTheSame(t *testing.T)         // new == current → "the new password must differ from the current one"
func TestPasswordChangeEndsTheOtherSessions(t *testing.T) // two sessions, change from one: the other is gone, the changing browser has a new cookie
func TestIdlePostLoginReturnsHome(t *testing.T)          // require.go: POST without session → /login (no next); GET keeps next
```

- [ ] **Step 2: Run them** — `go test ./internal/web ./internal/api -run '…' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** as in Interfaces; the three redirects with `?notice=` call `h.notice` and redirect without the query.
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** in three commits: `fix(web): no inline styles, no htmx indicator style` ; `fix(web): template errors answer 500; notices come from the server` ; `fix(web): the password must change and ends the other sessions; idle POSTs return home`; then `docs: the admin UI findings closed by Milestone 5`.

---

## Area 5: the portal name (branch `m5-portal-name`)

### Task 13: `public.prism_host` becomes `public.portal_host`

Backlog: "With sites the tenant name leads to the portal, not to a
Prism Central: rename `public.prism_host` to a portal name (old key
accepted) before v0.1.0". Decided with Marco on 2026-10-20.

**Files:**
- Modify: `internal/config/config.go`, `internal/settings/settings.go`, `internal/api/settings.go`, `internal/web/setup.go`, `internal/web/templates/setup.html`, `cmd/portico/settings_cmd.go`, every Go use of `PrismHost` (rename to `PortalHost`), `deploy/portico.yaml.example`, `deploy/portico.appliance.yaml` if it names it, `lab/portico/portico.yaml`, `docs/api.md`, the design §4.2 and the sites design (one note that the key was renamed), `docs/install.md`
- Test: `internal/config/config_test.go`, `internal/settings/settings_test.go`, `internal/api/settings_test.go`

**Interfaces:**
- Produces: YAML `public.portal_host`, settings JSON `portal_host`, API
  JSON `portal_host`, Go field `PortalHost`. The legacy key
  `prism_host` is read in all three when the new one is empty; both set
  and different is an error (`public.prism_host is the old name of
  public.portal_host; keep one`); written documents and API answers
  carry only `portal_host`. The setup label reads "Portal public host",
  placeholder `portal.msp.example`.

- [ ] **Step 1: Write the failing tests**

```go
func TestConfigReadsTheOldPrismHostKey(t *testing.T)        // yaml with prism_host only → PortalHost set, no error
func TestConfigRefusesBothHostKeysDiffering(t *testing.T)   // both keys, different → the error above
func TestSettingsDocumentFromBeforeTheRename(t *testing.T)  // stored JSON {"prism_host":"a.example",...} → PortalHost "a.example"; saved again → JSON has portal_host, no prism_host
func TestSettingsAPIAcceptsTheOldField(t *testing.T)        // PUT with prism_host → stored; GET answers portal_host only
```

- [ ] **Step 2: Run them.** Expected: FAIL (field undefined).
- [ ] **Step 3: Implement** with a custom `UnmarshalYAML`/`UnmarshalJSON` on the struct holding the host (an alias type with both fields), then rename the field everywhere (`gofmt -r` or the editor).
- [ ] **Step 4: Run** `go test ./...`, `lab/test_scripts.sh`, `grep -rn 'prism_host\|PrismHost' cmd internal deploy lab --include='*.go' --include='*.yaml' --include='*.sh' --include='*.html'` (only the legacy-key code and its tests). Expected: PASS.
- [ ] **Step 5: Commit** `feat: public.prism_host becomes public.portal_host (old key accepted)`, then `docs: the portal name`.

### Task 14: The lab takes the new names (acceptance)

Backlog: "rename the lab's names as the acceptance: `msp.portico.test`
for the portal … and `id.portico.test` for the identity provider, in
place of `prism.lab.portico.test` and `id.lab.portico.test`". The
public names cannot change after setup (the bootstraps write them into
Keycloak and into each site's SAML provider, whose entity id carries the
portal name), so the acceptance is a fresh setup of the appliance with
the new names, which is also the rehearsal of Milestone 6's install
guide.

**Needs Marco:** the `/etc/hosts` entries on his Mac for the two new
names (sudo), and his browser login at the end. Everything else runs
from scripts with the passwords from files, announced in one line each.

- [ ] **Step 1:** Deploy the `m5-portal-name` build to the appliance (`lab/appliance` deploy script), check `/healthz`.
- [ ] **Step 2:** Tear down: Disconnect `acme` from both sites, delete it; Remove `AZ-US-NYC1` (credentials from `nyc1-pw`) and `site-1` (from `prism-pw`) through the admin API; check in both Prism Centrals that the providers, their SAML users and the keys are gone; record what had to be removed by hand.
- [ ] **Step 3:** New lab certificate for `msp.portico.test` and `id.portico.test` from the lab CA; `lab/env.sh` defaults to the new names.
- [ ] **Step 4:** Setup through the admin API: public names (`portal_host`), Keycloak bootstrap, both sites added again in one step each, Apply; tenant `acme` created on both sites; visibility ok.
- [ ] **Step 5:** Marco logs in at `https://msp.portico.test/` as the probe user and reaches each site.
- [ ] **Step 6:** Write-up `docs/lab/2026-10-2x-27-portal-name.md` (what was measured, on which build, how to re-run), `docs/lab/lab-inventory.md` with the new names and objects, backlog line removed; commit `docs(lab): the lab on the portal names`.

---

## After the five areas

- What is left in `docs/backlog.md` is the input of Milestone 6's known
  limits; this milestone removes only the lines it closes.
- One final review on the most capable model (Fable) over the five
  branches, then one fix pass; each branch becomes one pull request,
  opened and merged only with Marco's OK.
