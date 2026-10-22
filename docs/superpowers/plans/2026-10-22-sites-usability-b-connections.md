# Sites Usability, Part B: Connecting, Suspending, Disconnecting, Deleting — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An MSP connects a tenant to a site from the site's page or the
tenant's page with cluster and container menus read from Prism Central,
suspends and resumes it for maintenance without touching Prism Central,
disconnects it only when its project is empty (and then deletes the
project), and deletes a tenant only when it is connected to no site.

**Architecture:** A placement gains the state `suspended`. The public
routing reads a tenant's *connections* (active or suspended) instead of
its active placements: suspended ones are greyed tiles and count for
"several sites", only active ones route. The tenant service gains
Suspend, Resume, Disconnect (count the project's contents with the
service account, refuse when anything is there, else remove Portico's
objects and the project), a per-site Retry, cluster/container targets per
site, and a Delete that needs no connection left. The admin UI shows a
connections table on both pages with confirmation pages; no JavaScript
beyond the HTMX already used.

**Tech Stack:** Go 1.27 standard library, SQLite store, `html/template`
+ HTMX in the admin UI; Prism Central v4 APIs (vmm, volumes, networking,
prism, dataprotection v4.0); Keycloak admin API.

**Spec:** `docs/superpowers/specs/2026-10-22-sites-usability-design.md`
4.4, 4.5, 4.6 and section 5 (approved 2026-10-22). Decision lines:
`docs/lab/2026-10-22-24-sites-usability-measurements.md` item 1. Part A
(sign-out, slow page) is done on the same branch; Part C (one-step
sites, status, pinning, runtime reload) follows.

## Global Constraints

- All text on disk in English. Commits end with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`, author email
  `9093189+marco-fabbri@users.noreply.github.com`
  (`git -c user.email=... commit`).
- Branch `sites-usability`. Push and PR only with Marco's OK, after the
  branding PR; Marco merges with rebase.
- No new Go module dependency. `CGO_ENABLED=0`; `go vet ./...`,
  `gofmt -l cmd internal`, `go test ./...` clean at every commit;
  `lab/test_scripts.sh` green.
- Wording: **connect**, **disconnect**, **suspend**, **resume** (spec
  4.4). A suspended tile reads "Not available".
- Portico never deletes, powers off or moves a VM, on any action (spec
  anti-goals). Disconnect never touches a project that still holds
  anything Portico can count; an error while counting is not an empty.
- "Several sites" = more than one connection that is active or
  suspended (spec 4.5). One function computes it
  (`SiteRouting.multiple`); nothing else counts placements for routing.
- The API changes without a compatibility layer (spec section 3), but
  `POST /api/v1/tenants` without a `sites` key keeps placing a tenant on
  the only site when exactly one is configured (lab scripts rely on it).
- Tenant pages keep their CSP: `default-src 'none'; img-src 'self';
  style-src 'unsafe-inline'; frame-ancestors 'none'`.
- Never print, log or commit secrets (API keys, cookies, tokens,
  passwords, session ids).
- Any write to a lab Prism Central, any appliance upgrade or restart,
  and any browser login with a non-test credential need Marco's OK
  first; warn him before an appliance upgrade and wait for his go-ahead.

## Review Focus

1. A session already on a site when its connection is suspended sends a
   non-navigation request (console XHR): 403 JSON `site-suspended`, the
   upstream gets nothing; a navigation gets the site page (several) or
   the "not available" page (one). Test in Task 1.
2. A crafted `POST /.portico/sites/select` naming a suspended site is
   refused (403 `site-not-allowed`), and a tenant with one active and one
   suspended connection is not auto-routed: it gets the site page. Test
   in Task 1.
3. Counting a project answers 504 (or times out) on one kind: Disconnect
   is refused with "could not count", the placement keeps its state and
   nothing is deleted. A kind answering 403/400 is skipped, not fatal.
   Test in Task 3.
4. Prism Central refuses the project delete after Portico's objects are
   gone (something uncountable is left): the placement ends
   `remove-failed` with "Prism Central refused to delete the project: it
   still holds something", and the row's Retry runs the disconnect again
   (count first). Test in Task 3.
5. Delete tenant while a connection is being created (`creating`) or is
   `remove-failed`: refused, 409; Suspend on a `removing` placement:
   refused, 409. Tests in Tasks 2 and 4.

---

### Task 1: The suspended state in the public routing

**Files:**
- Modify: `internal/store/placements.go` (constant), `internal/sites/index.go`,
  `internal/proxy/sites.go`, `internal/proxy/public.go`,
  `internal/branding/templates/sites.html`, `cmd/portico/serve.go`
- Test: `internal/sites/index_test.go`, `internal/proxy/sites_test.go`,
  `internal/branding/pages_test.go`

**Interfaces:**
- Produces:
  - `store.PlacementSuspended PlacementStatus = "suspended"` (no
    migration: `status` is free text).
  - `type sites.Connection struct { SiteID string; Suspended bool }`
  - `func (x *Index) Connections(ctx context.Context, tenantID string) ([]Connection, error)`
    — active and suspended placements, same cache and `Invalidate`;
    `Index.Sites` goes (its only caller is the routing).
  - `SiteRouting.Connections func(ctx context.Context, tenantID string) ([]sites.Connection, error)`
    replaces `SiteRouting.Placements`.
  - `branding.SiteTile.Status` gains the value `"suspended"`.

- [ ] **Step 1: Write the failing tests**

`internal/sites/index_test.go`:
- `TestConnectionsListActiveAndSuspended`: placements active (a),
  suspended (b), failed (c), creating (d) → `[{a false} {b true}]`.

`internal/proxy/sites_test.go` (the harness gains
`suspended map[string]map[string]bool`, read by its `Connections`):
- `TestSuspendedSiteIsAGreyTile`: acme on site-a, site-b suspended →
  site page 200, `value="site-b" disabled`, text `Not available`.
- `TestSelectSuspendedSiteRefused`: POST select site-b → 403, body rule
  `site-not-allowed`, `h.store` session Site still "".
- `TestSessionOnSuspendedSiteNavigationGoesToSitePage`: session on
  site-b, b suspended, navigation `GET /console/` → 303
  `/.portico/sites`; `len(*h.b) == 0`.
- `TestSessionOnSuspendedSiteXHRRefused`: same, non-navigation GET
  `/api/x` → 403 with `site-suspended`; `len(*h.b) == 0`.
- `TestOnlySuspendedSiteShowsNotAvailable`: tenant `solo` with only
  site-a, suspended → navigation 503, branded page containing
  `AZ-IT-MI1 is not available at the moment`; no upstream request.
- `TestActivePlusSuspendedIsSeveralSites`: acme, b suspended, session
  with no site → navigation 303 `/.portico/sites` (not auto-chosen), and
  the console's Sign Out (`h.signOut`) with the landing on → 303
  `/.portico/sites` (Part A's rule widened).

`internal/branding/pages_test.go`:
- `TestSitesTileSuspended`: tile `{Status: "suspended"}` → `Not available`
  and class `state suspended`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/sites/ ./internal/proxy/ ./internal/branding/ -run 'Connections|Suspended|NotAvailable|ActivePlusSuspended'`
Expected: FAIL — `Connections` undefined / `suspended` not handled.

- [ ] **Step 3: Implement**

- `Index.Connections` as above.
- `SiteRouting`: `allowed` becomes `connections(ctx, tenant) (active,
  all []*SiteBackend, err error)` (both sorted as today); `route`:
  session site in `active` → use it; `len(all) == 0` → `noSite`;
  `len(all) == 1` → auto-choose when it is active, else
  `suspendedPage`; `len(all) > 1` → as today's "several" branch. A
  session whose site is suspended falls into these branches (its
  `Site` is not cleared, as today). Non-navigation refusals for a
  suspended session site use rule `site-suspended`.
- `suspendedPage`: `Pages.Error(w, r, 503, "Site not available",
  code+" is not available at the moment. Please try again later.", "/")`
  for a navigation, `Refuse` 403 `site-suspended` otherwise.
- Tiles: suspended → `Status: "suspended"`, `Selectable: false`;
  `selectSite` accepts only active ones. `multiple` = `len(all) > 1`.
- `sites.html`: `suspended` → `Not available`, `.state.suspended`
  coloured like maintenance.
- `serve.go`: `Connections: placements.Connections`.
- A WebSocket already upgraded is not re-checked (the check runs per
  request): say so in a comment on `route`.

- [ ] **Step 4: Run the tests to verify they pass, then the suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal cmd
git commit -m "feat(proxy): suspended connections are grey tiles, count as several sites and route nowhere"
```

---

### Task 2: Suspend and resume (service, API)

**Files:**
- Modify: `internal/tenant/service.go`, `internal/tenant/provision.go`,
  `internal/api/api.go`, `docs/api.md`
- Test: `internal/tenant/sites_test.go`, `internal/api/tenants_sites_test.go`

**Interfaces:**
- Consumes: `store.PlacementSuspended` (Task 1).
- Produces:
  - `func (s *Service) Suspend(ctx context.Context, id, siteID string) error`
  - `func (s *Service) Resume(ctx context.Context, id, siteID string) error`
  - API `POST /api/v1/tenants/{id}/sites/{site}/suspend` and `/resume`
    → 200 `placementJSON`.

- [ ] **Step 1: Write the failing tests**

`internal/tenant/sites_test.go`:
- `TestSuspendKeepsPrismCentralUntouched`: active on site-a → Suspend →
  placement `suspended`, `pcA.Calls` unchanged by the call, one
  `changes` entry for the tenant.
- `TestResumeRestoresSameProject`: Suspend then Resume → `active`, same
  `ProjectExtID`, no new create in `pcA.Creates`.
- `TestSuspendRefusedUnlessActive`: placement `removing` → `ErrState`;
  Resume of an active one → `ErrState`; unknown site → `ErrUnknownSite`.
- `TestProvisionSkipsSuspended`: tenant with site-a suspended and site-b
  failed → `Retry` provisions site-b only; site-a stays `suspended`.

`internal/api/tenants_sites_test.go`:
- `TestSuspendResumeAPI`: POST suspend → 200, JSON `"status":"suspended"`;
  POST resume → 200 `"active"`; POST suspend on a removing placement → 409.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/tenant/ ./internal/api/ -run 'Suspend|Resume|SkipsSuspended'`
Expected: FAIL — methods undefined, routes 404.

- [ ] **Step 3: Implement**

Both take the tenant lock (`acquire`, `ErrBusy` when held), change only
the placement row (`UpdatePlacement`, `LastError` cleared) and call
`s.changed(id)`. `provision` adds `PlacementSuspended` to the states it
skips. Routes in `api.go` next to the sites routes, inside `h.running`.
`docs/api.md`: the two routes, and that the pair's other node sees the
change within the index cache (5 s).

- [ ] **Step 4: Run the tests to verify they pass, then the suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal docs/api.md
git commit -m "feat(tenant): suspend and resume a tenant on one site, Prism Central untouched"
```

---

### Task 3: Disconnect only an empty project

**Files:**
- Create: `internal/nutanix/contents.go`, `internal/nutanix/contents_test.go`
- Modify: `internal/tenant/service.go`, `internal/tenant/provision.go`,
  `internal/tenant/tenanttest/fakes.go`, `internal/api/api.go`,
  `cmd/portico/serve.go`, `docs/api.md`
- Test: `internal/tenant/sites_test.go`, `internal/api/tenants_sites_test.go`

**Interfaces:**
- Produces:
  - `type nutanix.Contents struct { Counts map[string]int; Uncounted []string }`
    with `func (c Contents) Empty() bool` (no count above zero) and
    `func (c Contents) String() string` (`"VMs: 2, images: 1"`, kinds in
    the order of the table below).
  - `func (c *Client) ProjectContents(ctx context.Context, projectExtID string) (Contents, error)`
  - `tenant.PrismCentral` gains `ProjectContents` (the fake: field
    `Contents map[string]nutanix.Contents` keyed by project extId, and
    `FailOnce["ProjectContents"]`).
  - `tenant.Site` gains `URL string` (filled in `serve.go`).
  - `type tenant.NotEmptyError struct { Site, Project, URL string; Contents nutanix.Contents }`
  - `var tenant.ErrCount = errors.New("the project's contents could not be counted")`
  - `func (s *Service) Contents(ctx context.Context, id, siteID string) (nutanix.Contents, error)`
  - `func (s *Service) Disconnect(ctx context.Context, id, siteID string) error`
    replaces `RemoveSite`; `func (s *Service) RetrySite(ctx context.Context, id, siteID string) error`.
  - API: `GET /api/v1/tenants/{id}/sites/{site}/contents` → 200
    `{"counts":{...},"uncounted":[...],"empty":bool}`;
    `DELETE /api/v1/tenants/{id}/sites/{site}` (no query) → 202, or 409
    `{"error":..., "counts":{...}}` when not empty, 503 on `ErrCount`;
    `POST /api/v1/tenants/{id}/sites/{site}/retry` → 202.
  - `ErrLastSite` goes.

The kinds, their list paths and their names in messages (write-up 24):

| key | path | name |
|---|---|---|
| vms | `/api/vmm/v4.0/ahv/config/vms` | VMs |
| images | `/api/vmm/v4.0/content/images` | images |
| templates | `/api/vmm/v4.0/content/templates` | templates |
| ovas | `/api/vmm/v4.0/content/ovas` | OVAs |
| volume-groups | `/api/volumes/v4.0/config/volume-groups` | volume groups |
| subnets | `/api/networking/v4.0/config/subnets` | subnets |
| vpcs | `/api/networking/v4.0/config/vpcs` | VPCs |
| floating-ips | `/api/networking/v4.0/config/floating-ips` | floating IPs |
| security-policies | `/api/microseg/v4.0/config/policies` | security policies |
| categories | `/api/prism/v4.0/config/categories` | categories |
| recovery-points | `/api/dataprotection/v4.0/config/recovery-points` | recovery points |

Each is one request `?$limit=1&$filter=projectExtId eq '<extId>'` read
for `metadata.totalAvailableResults`, all kinds in parallel under the
caller's context. 403 or 400 → the key goes to `Uncounted`; any other
error (5xx, timeout) fails the whole count. A subnet or category shared
into the project carries another project's id and is not counted (the
filter is on the owner).

- [ ] **Step 1: Write the failing tests**

`internal/nutanix/contents_test.go` (with `newFakePC`):
- `TestProjectContentsCountsEveryKind`: the fake answers
  `totalAvailableResults` 2 for vms, 1 for images, 0 elsewhere, 403 for
  security-policies → `Counts{vms:2, images:1, ...}`,
  `Uncounted ["security-policies"]`, `Empty() == false`,
  `String() == "VMs: 2, images: 1"`; every request carries
  `$filter=projectExtId eq 'p-1'`.
- `TestProjectContentsFailsOnServerError`: 504 on floating-ips → error.

`internal/tenant/sites_test.go`:
- `TestDisconnectRefusedWhenNotEmpty`: contents `vms:1` → `errors.As`
  `*NotEmptyError` with Site `SITE-A`, Project `portico-acme`, URL the
  site's; placement still `active`; no delete call in `pcA.Calls`.
- `TestDisconnectRefusedWhenCountFails`: `FailOnce["ProjectContents"]`
  → `errors.Is(err, ErrCount)`; placement unchanged; no delete call.
- `TestDisconnectEmptyDeletesProject`: empty → after `Wait`, the
  placement is gone, `pcA` holds no project, resource group, SAML group
  or membership for acme, the IdP is unshared; steps `role-membership`,
  `idp-unshare`, `saml-group`, `resource-group`, `project` all OK.
- `TestDisconnectSuspendedAllowed`: a suspended placement, empty →
  disconnected.
- `TestDisconnectLastSiteAllowed`: tenant on site-a only → disconnected;
  tenant has zero placements and stays `active`.
- `TestDisconnectProjectDeleteRefusedByPrismCentral`: empty count but
  `FailOnce["DeleteProject"]` = task failure → placement
  `remove-failed`, LastError contains `Prism Central refused to delete
  the project: it still holds something`; then `RetrySite` with the
  failure cleared → disconnected.
- `TestDisconnectNeverDeletesForeignProject`: placement whose
  `ProjectID` is not `portico-acme` → project step fails, project kept
  (today's guard, kept).
- `TestRetrySiteProvisionsOnlyThatSite`: site-a failed, site-b failed →
  `RetrySite(acme, site-a)` provisions site-a only.

`internal/api/tenants_sites_test.go`:
- `TestDisconnectAPI`: non-empty → 409 with `"counts":{"vms":1}`; empty
  → 202; `GET .../contents` → `"empty":true`; the old
  `?delete_project=true` query is ignored (202 too).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/nutanix/ ./internal/tenant/ ./internal/api/ -run 'Contents|Disconnect|RetrySite'`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

- `ProjectContents` in `contents.go`, through the client's own request
  path (authentication, TLS, the logging rules of `client.go`; extend
  `do` to return the `metadata` total if that is the smallest change).
- `Disconnect`: lock; placement must be `active`, `suspended`, `failed`
  or `remove-failed` (else `ErrState`); when `ProjectExtID != ""`,
  count under `OperationTimeout` — error → `ErrCount` wrapped with the
  site code, not empty → `*NotEmptyError` (message: `the project
  <name> on <CODE> still holds <Contents>: empty it in Prism Central
  first`); then `removing`, `s.changed(id)` and `removePlacement` in
  the background, now always deleting resource group and project. A
  failed project-delete task is reported as above.
- `RetrySite`: `failed` → provision that site only (`provision` gains an
  `only string` site filter, "" for all); `remove-failed` → `Disconnect`.
  Tenant-level `Retry` stays for a tenant whose own step (Keycloak
  group) failed.
- API routes and `fail()` mappings (`*NotEmptyError` 409 with counts,
  `ErrCount` 503). `docs/api.md`: the three routes; `delete_project`
  gone.

- [ ] **Step 4: Run the tests to verify they pass, then the suite**

Run: `go test ./...`
Expected: PASS (fix the existing `RemoveSite`/`ErrLastSite` tests to the
new rule: they now assert disconnect of the last site succeeds).

- [ ] **Step 5: Commit**

```bash
git add internal cmd docs/api.md
git commit -m "feat(tenant): disconnect counts the project's contents and deletes only an empty project"
```

---

### Task 4: Tenants with no sites; delete only then

**Files:**
- Modify: `internal/tenant/service.go`, `internal/tenant/provision.go`,
  `internal/idp/keycloak/client.go`, `internal/tenant/tenanttest/fakes.go`,
  `internal/api/api.go`, `lab/tenants/acceptance.sh`, `docs/api.md`
- Test: `internal/tenant/service_test.go`, `internal/idp/keycloak/client_test.go`,
  `internal/api/api_test.go`

**Interfaces:**
- Produces:
  - `tenant.Keycloak` gains `DeleteUser(ctx context.Context, id string) error`
    (404 tolerated).
  - `var tenant.ErrHasSites = errors.New("the tenant is still connected to a site")`
  - `func (s *Service) Delete(ctx context.Context, id string) error`
    (the `deleteProject` parameter goes).
  - `func (s *Service) DefaultSites() []PlacementRequest` — the only
    site when exactly one is configured, else none.
  - API `POST /api/v1/tenants`: `sites` absent → `DefaultSites()`;
    `"sites": []` → none. `DELETE /api/v1/tenants/{id}` → 202, 409 on
    `ErrHasSites`.

- [ ] **Step 1: Write the failing tests**

- `TestCreateWithNoSites` (service): two sites configured,
  `CreateRequest{Sites: nil}` → tenant `active`, zero placements,
  Keycloak group created.
- `TestDeleteRefusedWithSites`: placements in each of `active`,
  `suspended`, `creating`, `remove-failed` → `ErrHasSites`, tenant
  unchanged.
- `TestDeleteRemovesGroupUsersAndRow`: no placements, two users in the
  group → after `Wait`, both users deleted (`kc`), group deleted,
  `GetTenant` → `store.ErrNotFound`; no Prism Central call.
- `TestDeleteUser` (keycloak client, httptest): `DELETE
  /admin/realms/portico/users/u1` sent; a 404 answer → nil.
- `TestCreateTenantSitesAbsentVsEmpty` (API, one site configured):
  without `sites` → one placement; with `"sites":[]` → none.
- `TestDeleteTenantAPIWithSites`: 409, body names the rule.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/tenant/ ./internal/idp/keycloak/ ./internal/api/ -run 'NoSites|DeleteRefused|DeleteRemoves|DeleteUser|SitesAbsent|DeleteTenantAPI'`
Expected: FAIL.

- [ ] **Step 2b: Update the lab script**

`lab/tenants/acceptance.sh --delete`: disconnect each site
(`DELETE /api/v1/tenants/$slug/sites/<site>`, wait until the placement
is gone), then `DELETE /api/v1/tenants/$slug` expecting 202.

- [ ] **Step 3: Implement**

`Create` drops `ErrNoSites` and places exactly `req.Sites`. `Delete`:
lock; any placement → `ErrHasSites`; else `deleting` and in the
background steps `keycloak-users` (`GroupMembers`, `DeleteUser` each)
and `keycloak-group`, then `DeleteTenant`. `remove` and its
`deleteProject` go.

- [ ] **Step 4: Run the tests to verify they pass, then the suite**

Run: `go test ./... && bash lab/test_scripts.sh`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal lab docs/api.md
git commit -m "feat(tenant): a tenant can have no site; deleting it needs no site left and removes its users"
```

---

### Task 5: Cluster and container menus per site

**Files:**
- Modify: `internal/nutanix/clusters.go`, `internal/tenant/service.go`,
  `internal/tenant/tenanttest/fakes.go`
- Test: `internal/nutanix/clusters_test.go` (or the existing test file
  for clusters), `internal/tenant/sites_test.go`

**Interfaces:**
- Produces:
  - `nutanix.StorageContainer` gains `ClusterExtID string` (`clusterExtId`).
  - `tenant.PrismCentral` gains `ListClusters` and
    `ListStorageContainers` (as on `*nutanix.Client`).
  - `type tenant.Target struct { ExtID, Name, ClusterName string; SiteDefault bool }`
  - `type tenant.Targets struct { Clusters, Containers []Target }`
  - `func (s *Service) Targets(ctx context.Context, siteID string) (Targets, error)`
    — AOS clusters; containers that are not system ones, on an AOS
    cluster, each with its cluster's name; the site's defaults marked
    and first; under a 10 s timeout. On error it returns the site's
    defaults alone (marked) together with the error.

- [ ] **Step 1: Write the failing tests**

- `TestStorageContainersCarryCluster`: the fake list has
  `clusterExtId` → field set.
- `TestTargetsMarkSiteDefaults`: two clusters (one Prism Central), three
  containers (one system) → one cluster, two containers, the default
  first with `SiteDefault`, `ClusterName` set.
- `TestTargetsOnErrorOffersDefaults`: `FailOnce["ListClusters"]` →
  error, and Targets holding the defaults only.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/nutanix/ ./internal/tenant/ -run 'CarryCluster|Targets'`
Expected: FAIL.

- [ ] **Step 3: Implement** as in the Interfaces block.

- [ ] **Step 4: Run the tests to verify they pass, then the suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal
git commit -m "feat(tenant): cluster and storage container menus read from the site's Prism Central"
```

---

### Task 6: The admin pages

**Files:**
- Create: `internal/web/templates/confirm.html`,
  `internal/web/connections.go` (handlers below)
- Modify: `internal/web/tenants.go`, `internal/web/sites.go`,
  `internal/web/templates/tenant.html`, `site.html`, `tenant_new.html`,
  `internal/web/static/portico.css`
- Test: `internal/web/connections_test.go`, `internal/web/tenants_test.go`,
  `internal/web/tenants_sites_test.go`

**Interfaces:**
- Consumes: Tasks 2–5 (`Suspend`, `Resume`, `Contents`, `Disconnect`,
  `RetrySite`, `Delete`, `Targets`, `NotEmptyError`, `ErrHasSites`).
- Produces (UI routes, CSRF-checked POSTs answering 303, as today):
  - `GET /targets?site=<id>&form=<connect|create>` — fragment: the two
    `<select>` elements (`cluster_ext_id`/`storage_container_ext_id`,
    or `cluster_<id>`/`container_<id>` for `create`), defaults labelled
    `(site default)`; on error a line "Prism Central did not answer:
    the site's defaults are offered" above them.
  - `POST /setup/sites/{id}/tenants` — connect from the site page
    (fields `tenant`, cluster, container) → `AddSite`.
  - `POST /tenants/{id}/sites` — connect from the tenant page (field
    `site` added) → `AddSite` (today's route, new form).
  - `GET|POST /tenants/{id}/sites/{site}/suspend`, `.../resume` (POST
    only), `GET|POST .../disconnect`, `POST .../retry`,
    `GET|POST /tenants/{id}/delete` — each GET is a confirmation page
    (`confirm.html`: title, text, a list, one button, Cancel back).
  - Today's `.../remove` route and the "delete the project too"
    checkboxes go.

Pages (spec 4.4–4.6, mockup v3 of the brainstorm):
- Tenant page: "Sites" table (site code and name, cluster, container,
  status pill, "N users on this site now" from the runtime sessions
  filtered by tenant, actions: Suspend/Resume, Disconnect, and on a
  failed row its reason and Retry), then the card **Connect this tenant
  to a site** (site menu of configured sites not yet connected; menus
  loaded with `hx-get="/targets?form=connect"` `hx-trigger="load,
  change from:#site"` `hx-include="#site"`). Header: Disable/Enable;
  **Delete tenant** only with no connection, otherwise the muted line
  "Disconnect it from every site to delete it". The top "Retry
  provisioning" button shows only for a tenant whose own step failed.
- Site page: card "Tenants on this site" (rows as above with the tenant
  name linked) above the live sessions, and **Connect a tenant to this
  site** (menu of active tenants not connected here; menus with
  `hx-trigger="load"`).
- Suspend confirmation: "Suspend <tenant> on <CODE>? <N> of its users
  are on this site now. Their open VM consoles finish; nothing else
  opens until you resume. VMs keep running." Disconnect confirmation:
  either the refusal (Prism Central's list of what is left, a link to
  the site's Prism Central, no button) or "Disconnect <tenant> from
  <CODE>? Portico deletes the project <name>, its resource group, the
  SAML group and the role membership in that Prism Central." Delete
  confirmation: "Delete <tenant>? Its Keycloak group and its <N> users
  are deleted."
- New tenant form: per site a checkbox and the menus fragment
  (`form=create`), the only site pre-checked as today; no site checked
  is allowed.
- CSS: `.status-suspended` like `-maintenance`; table rows and forms
  with more vertical room (`td` padding, one field per line).

- [ ] **Step 1: Write the failing tests**

`internal/web/connections_test.go` (with `withTwoSites`):
- `TestTenantPageListsConnections`: rows for both sites, a Suspend
  button on the active one, no "delete the project too" text.
- `TestSuspendConfirmCountsUsers`: two runtime sessions of acme on
  site-a, one of another tenant → confirm page text `2 of its users`.
- `TestSuspendPostSuspends`: POST with CSRF → 303 to the tenant page,
  placement `suspended`; without CSRF → 403.
- `TestDisconnectConfirmShowsRefusal`: contents `vms:2` → page contains
  `VMs: 2`, a link to the site's URL, and no disconnect button.
- `TestDisconnectPostEmpty`: 303, placement `removing`.
- `TestConnectFromSitePage`: POST `/setup/sites/site-b/tenants` →
  placement on site-b with the posted cluster and container.
- `TestTargetsFragment`: `form=connect` → `name="cluster_ext_id"`,
  `(site default)`; `form=create` → `name="cluster_site-a"`.
- `TestDeleteHiddenWithConnections`: tenant page without a Delete
  button and with `Disconnect it from every site`; with none → button.
- `TestCreateTenantNoSite`: form with no site checked → 303, tenant with
  zero placements.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/web/ -run 'Connections|Suspend|Disconnect|ConnectFrom|Targets|DeleteHidden|NoSite'`
Expected: FAIL.

- [ ] **Step 3: Implement** the routes, templates and CSS above; fix the
existing web tests that assert the removed checkbox and button.

- [ ] **Step 4: Run the tests to verify they pass, then the suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Look at the pages**

Run the admin UI locally against the fakes if a dev mode exists,
otherwise on the appliance in Task 8; record what was looked at.

- [ ] **Step 6: Commit**

```bash
git add internal
git commit -m "feat(web): connect, suspend, disconnect and delete from the tenant and site pages"
```

---

### Task 7: The documents

**Files:**
- Create: `docs/adr/0006-sites-at-runtime-and-connections.md`
- Modify: `docs/superpowers/specs/2026-10-17-portico-design.md` (4.4),
  `docs/superpowers/specs/2026-10-20-sites-design.md` (4.2, 4.3 step 2,
  4.6), `docs/backlog.md`, `docs/api.md` (check against the code)

- [ ] **Step 1: Write**

- Main design 4.4: the sentence of spec section 5 ("disconnect deletes
  an empty project and refuses a non-empty one; deleting a tenant needs
  no sites left; Portico never deletes VMs").
- Sites design 4.2: placements gain **suspended**; "remove a site" is
  disconnect of an empty project; the last-site rule goes. 4.3 step 2:
  "one" and "more than one" follow the "several sites" rule of usability
  4.5. 4.6: the tenant and site pages are those of usability 4.4.
- ADR 0006 (Context, Decision, Consequences, as ADR 0005): disconnect
  only when empty with Prism Central's refusal as the safety net; no
  deletion of resources by Portico; suspend closes Portico's route only;
  site changes at runtime and certificate pinning are decided in the
  same spec and their implementation is Part C.
- Backlog: "danger zone" (delete a tenant's resources from Portico),
  kept out with Marco on 2026-10-22, if MSPs ask for it.

- [ ] **Step 2: Check and commit**

Run: `grep -rn 'delete_project\|ErrLastSite\|keeps at least one site' docs internal lab | grep -v docs/history | grep -v 'docs/superpowers/plans/2026-10-1\|docs/superpowers/plans/2026-10-20\|docs/lab/2026-10-1\|docs/lab/2026-10-2[0-3]'`
Expected: no line outside historical write-ups and old plans.

```bash
git add docs
git commit -m "docs: connections, suspend and disconnect in the designs; ADR 0006"
```

---

### Task 8: On the appliance (with Marco), write-up 25

**Files:**
- Create: `docs/lab/2026-10-2x-25-sites-connections.md`
- Modify: `docs/lab/README.md`, `docs/lab/lab-inventory.md`

- [ ] **Step 1: Warn Marco and wait for his go-ahead**, then upgrade

Run (after his OK): the appliance environment of write-up 23, then
`lab/portico/upgrade-appliance.sh`.
Expected: the new `dev-<commit>` running, backup dir printed.

- [ ] **Step 2: In the browser, with Marco's OK for each write**

1. Site page of `AZ-US-NYC1`: "Tenants on this site" lists acme; the
   menus show NYC1's clusters and containers with the default marked.
2. A user of acme signed in on NYC1; Suspend acme on NYC1 (confirmation
   shows 1 user) → the next click in the console lands on the site
   page, NYC1 greyed "Not available"; an open VM console keeps running
   (if one is open); Resume → NYC1 opens again with no password.
3. Create tenant `beta` with no site; connect it from NYC1's site page;
   its user signs in. Disconnect with a VM in its project (Marco creates
   it in Prism Central, or `--scratch`-style with his OK) → refused with
   `VMs: 1`; remove the VM; Disconnect → project gone in Prism Central;
   Delete `beta` → its Keycloak group and user gone.
4. The counts with the service account: record each kind's answer (the
   403s of write-up 24 as the admin) from Portico's log or the
   contents API.

- [ ] **Step 3: Write write-up 25** (template; Measured on, Method,
Result with MEASURED / NOT MEASURED, re-measure), README row 25,
inventory (backup dir, version, beta removed or kept), commit:

```bash
git add docs
git commit -m "docs(lab): write-up 25, connections, suspend and disconnect on the appliance"
```

---

## After this plan

Part C (`2026-10-2x-sites-usability-c-one-step-sites.md`: spec 4.1, 4.2,
4.3, 4.9, repair, site removal, the identity provider id stored at setup
from 4.8, the Objects container filter of write-up 24 item 3), through
the same review with Marco.
