# Sites Usability, Part A: Measurements, Sign-out, Slow Sign-in — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Measure on the lab what the rest of the sites usability work
depends on, and ship the two tenant-facing fixes that need no
measurement: signing out of one site's console returns a tenant with
several sites to the site page, and a slow Prism Central at sign-in shows
a branded page with Retry instead of Prism Central's provider page.

**Architecture:** The spec splits into three parts, one plan each, on
the branch `sites-usability`. Part A (this plan) measures (spec section
6), then changes the public listener only: the console's Sign Out branch
in `internal/proxy/public.go`, a per-session "leave site" in the session
store, the SAML-start failure page in `internal/proxy/landing.go` and
the error template. Part B (connecting tenants, suspend, disconnect,
delete; spec 4.4–4.6) and Part C (one-step sites, status, pinning,
runtime reload; spec 4.1–4.3, 4.9) are planned after Task 1's write-up,
because their counting, container filter and permission check are what
Task 1 measures.

**Tech Stack:** Go 1.27 standard library, `html/template`; bash lab
scripts with `jq`, `lab/pc.sh`, `lab/saml/kc.sh`; Keycloak 26.3.5;
Prism Central pc.7.6.

**Spec:** `docs/superpowers/specs/2026-10-22-sites-usability-design.md`
(approved 2026-10-22, a2ed017), amending
`docs/superpowers/specs/2026-10-20-sites-design.md` (4.3 items 6–7) and
the main design. Write-ups 12 (console Sign Out ends at
`/redirect?logout`), 20 (cookie stash), 23 (the gaps this work fixes).

## Global Constraints

- All text on disk in English. Commits end with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`, author email
  `9093189+marco-fabbri@users.noreply.github.com`
  (`git -c user.email=... commit`).
- Branch `sites-usability`, from `favicon`. Push and PR only with Marco's
  OK, after the branding PR; Marco merges with rebase.
- No new Go module dependency. `CGO_ENABLED=0`; `go vet ./...`,
  `gofmt -l cmd internal`, `go test ./...` clean at every commit;
  `lab/test_scripts.sh` green.
- Tenant pages keep their CSP: `default-src 'none'; img-src 'self';
  style-src 'unsafe-inline'; frame-ancestors 'none'`; no JavaScript.
- "Several sites" in this part means what the routing has today: more
  than one **active** placement (`SiteRouting.multiple`). Part B widens it
  to active or suspended (spec 4.5); nothing here may hard-code the
  count elsewhere.
- Never print, log or commit secrets: Authorization, `X-ntnx-api-key`,
  Cookie values, tokens, passwords, session ids, `iam-sessionid`
  values. Lab scripts read `~/portico-secrets/` through `lab/env.sh`.
- Any write to a lab Prism Central (scratch project, scratch VM), any
  appliance upgrade or restart, and any browser login need Marco's OK
  first; warn him before an appliance upgrade and wait for his go-ahead.
- Don't write in docs what the lab hardware is used for.

## Review Focus

1. A tenant with several sites signs out on site 1, then picks site 2:
   site 2's stashed Prism Central cookies must still be there (no new
   password). Test in Task 2.
2. A tenant with several sites signs out while its session has no site
   chosen (a stale console tab): it still lands on the site page, no
   loop, no Keycloak logout. Test in Task 2.
3. The console's Sign Out of a tenant whose other placements were removed
   meanwhile (now one site): full sign-out, as today. Test in Task 2.
4. A SAML-start failure at the cookie step (no `iam-sessionid`): the
   page's Retry goes to `/`, which restarts the flow, never to
   `/.portico/saml-start`, which would fail again. Test in Task 3.
5. A crafted `/ui/iam/providers?portico=manual` must get the landing page
   now that the fallback is gone, never Prism Central's provider page.
   Test in Task 3.

---

### Task 1: Lab measurements (spec section 6) and write-up 24

**Files:**
- Create: `lab/sites/project-contents.sh`, `lab/sites/admin-rights.sh`,
  `lab/sites/container-kinds.sh`
- Create: `docs/lab/2026-10-22-24-sites-usability-measurements.md`
  (from `docs/lab/TEMPLATE.md`)
- Modify: `docs/lab/README.md` (row 24), `lab/test_scripts.sh`
  (`--help` of the three scripts, shellcheck)

**Interfaces:**
- Produces: write-up 24 with one "Decision for Part B/C" line per item;
  Parts B and C are written from it.

Script style: as `lab/sites/idp-entity-issuer.sh` (header comment with
usage, `set -uo pipefail`, `cd` to the repo root, `source lab/env.sh`
required, `lab/pc.sh` for every call, `jq` for output, a temp dir with
`umask 077`, nothing secret printed). API paths come from the v4
reference in `kb/`; record the exact path and API version used in the
write-up.

- [ ] **Step 1: `lab/sites/project-contents.sh [--scratch]` (item 1)**

Read-only by default: for the project named `$1` (default
`portico-acme`), list per kind the entities that belong to it on pc.7.6
(VMs, images, volume groups, subnets, categories, recovery points, and
any other kind the Projects 2.0 guide in `kb/` names), one line each:
`<kind> <count> <api path> <filter used>`, or `<kind> unknown <reason>`
when no filter by project exists. Run once with the lab admin
(`PC_USER`/`PC_PASS`) and once with the service account
(`PC_API_KEY`), so the write-up says which kinds the service account can
count. `--scratch` (needs Marco's OK, writes to Prism Central): create a
project `portico-measure-<UTC time>`, create one powered-off VM with no
disk in it, try `DELETE` on the project and record the status and
error, then delete the VM and the project; a `trap` cleans up on any
exit.

- [ ] **Step 2: `lab/sites/admin-rights.sh` (item 2)**

Read-only: with the lab admin, look for an endpoint that returns the
caller's own roles or permissions (the current user, its authorization
policies); print the endpoint, status and the list of permission names
that cover users, roles and SAML identity providers. With the service
account, call the same and print what it gets.

- [ ] **Step 3: `lab/sites/container-kinds.sh` (item 3)**

Read-only: list the storage containers of every cluster with every
field the v4 clusters API returns that could mark a Nutanix Objects
container (owner, flags, names), as a table; highlight the container
that became `acme`'s default (write-up 23).

- [ ] **Step 4: Run the three scripts and record**

Run: `source lab/env.sh && lab/sites/project-contents.sh && lab/sites/admin-rights.sh && lab/sites/container-kinds.sh`
Expected: output per item, no secret in it. Then, with Marco's OK,
`lab/sites/project-contents.sh --scratch`.

- [ ] **Step 5: With Marco, in the browser (items 4 and 5)**

Item 4: Marco signs in as a tenant user with one site and uses the
console's Sign Out; read Keycloak's log on the appliance
(`journalctl -u keycloak` over SSH, read-only) for the SAML logout it
attempts towards Prism Central, the URL and the error; check from the
Keycloak container whether the public Prism Central name resolves
(`podman exec` of `getent hosts`); from the browser's network log
(Marco), whether Prism Central's own sign-out call precedes the
redirect to `/redirect?logout`. Item 5: where Prism Central's UI shows
its certificate (Settings, SSL certificate) and whether it shows a
SHA-256 fingerprint; what Chrome's certificate viewer shows for
`https://10.23.210.185:9440`.

- [ ] **Step 6: Write-up 24**

Sections as `TEMPLATE.md`. For each item: what was measured, on which
version, how to re-measure, and a line `Decision for Part B` or
`Decision for Part C`:
1. the list of kinds Disconnect counts, which ones the service account
   can count, and whether Prism Central refuses to delete a non-empty
   project (spec 4.6: safety net, or administrator's credentials);
2. whether the pre-check of spec 4.1 step 1 exists or the creations are
   the only check;
3. the rule that tells an Objects container apart;
4. the Keycloak logout fix for Task 4: (a) make the name resolvable from
   the container, or (b) stop Keycloak's SAML logout towards Prism
   Central; with the reason;
5. the text that tells the MSP where to compare the fingerprint.

- [ ] **Step 7: Checks and commit**

Run: `lab/test_scripts.sh`
Expected: `PASS`.

```bash
git add lab/sites docs/lab lab/test_scripts.sh
git commit -m "lab: write-up 24, sites usability measurements"
```

---

### Task 2: Signing out of one site with several sites (spec 4.7)

**Files:**
- Modify: `internal/auth/session.go` (new `LeaveSite`)
- Modify: `internal/proxy/sites.go` (field `LeaveSite`, method `leaveSite`)
- Modify: `internal/proxy/public.go` (Sign Out branch)
- Modify: `cmd/portico/serve.go` (wire `LeaveSite: sessions.LeaveSite`)
- Test: `internal/auth/session_test.go`, `internal/proxy/sites_test.go`,
  `internal/proxy/landing_test.go`

**Interfaces:**
- Produces: `func (m *MemoryStore) LeaveSite(id string, now time.Time) bool`
  — clears `Site` and `SiteSince`, keeps `CookieSite` (so the next choice
  restores the target's stash and does not wipe the other stashes, see
  `switchSiteCookies` with `from == ""`); false when the session is gone.
- Produces: `SiteRouting.LeaveSite func(sessionID string, now time.Time) bool`
  and `func (s *SiteRouting) leaveSite(w http.ResponseWriter, r *http.Request, sess auth.Session)`.

- [ ] **Step 1: Write the failing tests**

`TestLeaveSiteKeepsCookieSite` (session_test.go): after
`SetSite("a","site-1",t0)`, `LeaveSite("a",t1)` is true, `Get` shows
`Site == ""`, `SiteSince.IsZero()`, `CookieSite == "site-1"`;
`LeaveSite("gone",t1)` is false.

`TestSignOutWithSeveralSitesReturnsToSitePage` (sites_test.go, two
placements, session on site 1, request
`GET /redirect?logout` with `Accept: text/html`, cookies
`NTNX_SESSION_ID=x`, `__Host-pstash.site-2.NTNX_SESSION_ID=y` and the
Portico session):
- status 303, `Location: /.portico/sites`;
- a `Set-Cookie` expiring `NTNX_SESSION_ID` (`Max-Age=0` or negative);
- no `Set-Cookie` for `__Host-pstash.site-2.NTNX_SESSION_ID` and none for
  the Portico session cookie;
- the session still exists, its `Site` is `""`, `CookieSite` is site 1;
- `X-Portico-Rule` / access-log rule `sign-out-site`.

`TestSignOutWithoutChosenSiteStillGoesToSitePage`: same, session with no
site chosen → 303 to `/.portico/sites`, no redirect to
`/.portico/logout`.

`TestSignOutWithOneSiteSignsOutOfEverything`: one placement → 302 to
`/.portico/logout` (today's behaviour; keeps
`TestConsoleSignOutGoesToPorticoLogout` meaningful).

`TestSignOutAfterOtherPlacementsRemoved`: session chose site 1 when there
were two; the placements function now returns only site 1 → 302 to
`/.portico/logout`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/auth/ ./internal/proxy/ -run 'LeaveSite|SignOut' -count=1`
Expected: FAIL (`LeaveSite` undefined; sign-out still goes to
`/.portico/logout` with several sites).

- [ ] **Step 3: Implement**

`LeaveSite` in `session.go` beside `SetSite`. In `public.go`, the
`interceptsSignOut` branch: when `o.Sites.multiple(ctx, sess.Tenant)`,
call `o.Sites.leaveSite(w, r, sess)`, else `l.signOut(w, r)` as today.
Note the branch runs after `route`, which for a session without a site
and several placements redirects to the site page before reaching it:
move the Sign Out check before `route` (it needs only the session), so
the second test passes through `leaveSite` too. `leaveSite`: rule
`sign-out-site`; expire every request cookie that is not a Portico
cookie (`isPorticoCookie`) and not a stash cookie (`parseStash`), with
the attributes `switchSiteCookies` uses; call `LeaveSite`; on false,
`Refuse(..., 401, "session-gone", ...)`; `Cache-Control: no-store`; 303
to `sitesPath`. Wire `LeaveSite: sessions.LeaveSite` in `serve.go`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/auth/ ./internal/proxy/ ./cmd/portico/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/auth internal/proxy cmd/portico
git commit -m "feat(proxy): the console's sign-out with several sites returns to the site page"
```

---

### Task 3: A slow Prism Central at sign-in gets a branded page (spec 4.8)

**Files:**
- Modify: `internal/branding/pages.go` (new `ErrorSwitch`),
  `internal/branding/templates/error.html` (the link)
- Modify: `internal/proxy/landing.go` (`samlStart` failure page,
  `SiteCode`, marker removed), `internal/proxy/public.go` (SAML-start
  handler sets `SwitchURL` with several sites)
- Test: `internal/branding/pages_test.go`, `internal/proxy/landing_test.go`

**Interfaces:**
- Produces: `func (p *Pages) ErrorSwitch(w http.ResponseWriter, r *http.Request, status int, title, text, retry, switchURL string)`;
  `Error` calls it with `switchURL ""`.
- Produces: `LandingOptions.SiteCode string`, filled by `forSite` from
  `SiteBackend.Code`.

Exact copy: title `Site responding slowly`; text
`<site code> is responding slowly. Please try again in a moment.`;
status 503; Retry link `/`; with several sites a link
`Choose another site` to `/.portico/sites`, styled like `.status a`
(the text colour, outside the card).

- [ ] **Step 1: Write the failing tests**

`TestErrorPageSwitchLink` (pages_test.go): `ErrorSwitch(..., "/", "/.portico/sites")`
body contains `href="/.portico/sites"` and `Choose another site`;
`Error(...)` body contains neither.

`TestSAMLStartFailureShowsSlowPage` (landing_test.go), table over the
four steps (no `iam-sessionid` cookie; `IdPExtID` error; GET 500; PUT
500), request with `Accept: text/html`:
- status 503, `Content-Type` text/html, the CSP above;
- body contains `Site responding slowly`, `AZ-IT-MI1 is responding
  slowly. Please try again in a moment.` and `href="/"`;
- no `Location` header, body does not contain `/ui/iam/providers` and
  does not contain `/.portico/saml-start`;
- the log line still carries the step (`step=get` etc.) and no cookie
  value (as `TestSAMLStartTransportErrorDoesNotLogCookie`).

`TestSAMLStartFailureWithSeveralSitesLinksSitePage`: two placements →
body contains `href="/.portico/sites"`.

`TestProvidersMarkerNoLongerBypassesLanding`: `GET
/ui/iam/providers?portico=manual` with a session → the landing page
(200, `/.portico/saml-start` in the body), nothing proxied.

Replace `TestSAMLStartFallsBackOnce` and the marker half of
`TestProvidersPostAndMarkerAreProxied` (a POST to the providers path is
still proxied), and update `TestSAMLStartTransportErrorDoesNotLogCookie`
for the 503 page.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/branding/ ./internal/proxy/ -run 'ErrorPageSwitch|SAMLStart|Marker' -count=1`
Expected: FAIL (`ErrorSwitch` undefined; 302 to the provider page).

- [ ] **Step 3: Implement**

`ErrorSwitch` passes `SwitchURL` into `pageData` (the field exists);
`error.html` renders `{{if .SwitchURL}}<p class="status"><a
href="{{.SwitchURL}}">Choose another site</a></p>{{end}}` after the
card. In `landing.go` the `fallback` closure becomes `fail`: same log
line with `"saml-start: sign-in could not start"`, then
`l.Pages.ErrorSwitch(w, r, http.StatusServiceUnavailable, ...)` with the
copy above; delete `manualMarker` and its check in
`interceptsProviders`. In `public.go` the SAML-start handler sets
`l.SwitchURL = sitesPath` when `o.Sites.multiple`, as the landing does.

- [ ] **Step 4: Run the tests**

Run: `go test ./... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/branding internal/proxy
git commit -m "feat(proxy): a failed SAML start shows a branded page with Retry, not Prism Central's provider page"
```

---

### Task 4: Keycloak's SAML logout towards Prism Central (spec 4.7, write-up 24 item 4)

**Files:** decided by write-up 24's decision line; one of:
- (a) `deploy/quadlets/keycloak.container` (an `AddHost=` for the public
  Prism Central name to the appliance's own address) and the installer
  or Apply step that knows that address;
- (b) `internal/settings/bootstrap_saml.go` (the Keycloak SAML client's
  logout attributes) plus a start-time or Apply-time update of existing
  clients.

**Interfaces:**
- Consumes: write-up 24 item 4.

- [ ] **Step 1: Write the failing test**

(a) `TestKeycloakQuadletResolvesPrismHost` in the test that already
covers the deploy files (or `deploy/` checks in `lab/test_scripts.sh`):
the generated or installed unit carries the `AddHost` line for the
configured `prism_host`. (b) `TestSAMLClientHasNoLogoutTowardsPrism`
(bootstrap_saml_test.go): the client sent to Keycloak has the logout
attributes the write-up names, and an existing client is updated to
them.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/settings/ -run SAMLClient -count=1` (b), or the
deploy check (a).
Expected: FAIL.

- [ ] **Step 3: Implement the decided fix**

Record the choice in the ledger as a ruling that cites write-up 24.

- [ ] **Step 4: Run the tests**

Run: `go test ./... -count=1 && lab/test_scripts.sh`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A deploy internal lab
git commit -m "fix(keycloak): a full sign-out no longer fails on the SAML logout towards Prism Central"
```

---

### Task 5: On the appliance, and the documents

**Files:**
- Modify: `docs/lab/2026-10-22-24-sites-usability-measurements.md`
  (section "Part A on the appliance")
- Modify: `docs/superpowers/specs/2026-10-20-sites-design.md` (4.3 items
  6 and 7 replaced by the sign-out of usability spec 4.7, with a pointer)
- Modify: `docs/lab/lab-inventory.md` (appliance version, backup dir)

- [ ] **Step 1: Warn Marco and wait for his go-ahead**, then upgrade

Run (after his OK): the appliance environment of write-up 23, then
`lab/portico/upgrade-appliance.sh`
Expected: the new `dev-<commit>` running, backup dir printed.

- [ ] **Step 2: With Marco, in the browser**

As a tenant user of `acme` (two sites): open site 1, the console's Sign
Out → the site page, Sign out link present; open site 2 → no password
asked; switch back to site 1 → the console asks to sign in to that site
silently (SAML round trip, no password). With a one-site tenant: Sign Out
→ Keycloak's login page, no Keycloak error in its log. Slow sign-in:
not provoked on purpose; record "not measured" unless Prism Central is
slow during the session.

- [ ] **Step 3: Write the results, update the sites design, commit**

```bash
git add docs
git commit -m "docs: sites usability part A on the appliance; sites design sign-out"
```

---

## After this plan

Write Part B (`2026-10-2x-sites-usability-b-connections.md`: spec 4.4,
4.5, 4.6, tenant creation, the "several sites" rule widened to suspended)
and Part C (`2026-10-2x-sites-usability-c-one-step-sites.md`: spec 4.1,
4.2, 4.3, 4.9, repair, site removal, the identity provider id stored at
setup from 4.8) from write-up 24's decision lines, each through the same
review with Marco.
