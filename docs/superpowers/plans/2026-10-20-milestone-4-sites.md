# Milestone 4: Sites Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Several Prism Centrals behind one Portico, presented to tenants
as sites (`AZ-IT-MI1`): the MSP declares sites on the setup page, places
each tenant on the sites where it gets resources, and a tenant signs in
once on the one public name, picks a site on a branded page and reaches
that site's Prism Central; the MSP can put a site in maintenance, see who
is still connected and disconnect them.

**Architecture:** The settings document holds a list of sites (connection,
sealed service-account key, SAML identity provider, placement defaults)
instead of the single `prism_central` block; a legacy document becomes
site `site-1`. Tenants keep one row and gain one placement per site in a
new store table; the tenant service runs the Milestone 2a steps per
placement against that site's Prism Central client. The Portico session
records the tenant (resolved from the Keycloak group at login) and the
chosen site; the public handler routes every request of the Prism Central
host to the session's site after the deny list and the session check,
shows a site page when the tenant has several placements, and swaps
Prism Central's cookies into per-site stash cookies on a switch. A new
`internal/sites` package probes each site and keeps the maintenance
flags; the admin API and UI expose sites, maintenance, connected users
and disconnection.

**Tech Stack:** Go 1.27 standard library, modernc SQLite, HTMX 2.0.4
(vendored), existing packages. Lab: the appliance `portico-appliance`
(10.23.210.191) on NTNX-01, pc.7.6.

**Spec:** `docs/superpowers/specs/2026-10-20-sites-design.md` (approved
2026-10-20), amending `docs/superpowers/specs/2026-10-17-portico-design.md`
sections 2, 4.2, 4.3, 4.4, 4.5, 4.7, 4.10, 4.11, 9. Write-ups 04 (SAML
entityID from `redirectURL`), 05, 07, 08, 12, 13, 15.

## Global Constraints

- All text on disk in English. Commit messages in English, ending with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; author email
  `9093189+marco-fabbri@users.noreply.github.com`.
- Branch `m4-sites` (holds the design, d6171ce). One pull request at the
  end, merged with rebase; tag `m4-sites` pushed as
  `refs/tags/m4-sites:refs/tags/m4-sites`.
- Every site runs pc.7.6; the API versions stay fixed (`iam/v4.1.b3`,
  `multidomain/v4.4.b1`) for every site. "Add site" refuses a Prism
  Central older than pc.7.6, naming the version found.
- The public path fails closed on every site: deny list first, then the
  Portico session, then the site check, then the allow-list. No request
  reaches a Prism Central for a tenant without an active placement there.
- Portico never stores a human administrator's credentials, on any site;
  one-time credentials are never logged, echoed or stored (2e rules).
- Never log or print cookie values, session ids, tokens, API keys or
  passwords. Cookie names may be logged.
- Site ids `^[a-z0-9][a-z0-9-]{0,31}$`, immutable; site codes
  `^[A-Z0-9][A-Z0-9-]{1,31}$`, unique, renamable. Placements and
  sessions reference the id, the tenant sees the code.
- The legacy document's site: id `site-1`, code `SITE-1`, name = the
  Prism Central URL's host.
- Adding, editing or removing a site takes effect at Apply (restart), as
  every setting; maintenance, disconnection and probe results take effect
  at once.
- The API has no external users before the first release: it changes
  without compatibility routes.
- `CGO_ENABLED=0`; `go vet ./...`, `gofmt -l cmd internal`,
  `go test ./...` clean at every commit; `lab/test_scripts.sh` green.
- Lab secrets: `~/portico-secrets/` and `~/portico-secrets-appliance/`,
  read by scripts only, never printed; new secret files 0600.
- Docs and commits never say what the lab hardware is or how the second
  lab cluster is built: "a second pc.7.6 lab cluster" is all.

## Review Focus

1. A tenant selecting a site where it has no active placement (crafted
   POST, unknown id, placement still `creating` or `failed`): expected
   403, the session's site unchanged, nothing proxied (Task 7).
2. A session whose site loses its placement (site removed from the
   tenant, site forgotten) or is disconnected: expected the next request
   not proxied (within the index TTL of 5 s), a navigation sent to the
   site page, anything else 403 (Task 7).
3. Stash cookies reaching an upstream: expected every `__Host-pstash.*`
   cookie and every Portico cookie stripped before any Prism Central or
   Keycloak sees the request, so site A never receives site B's session
   (Task 8).
4. Upgrade of a single-Prism Central store (legacy `prism_central` block,
   tenant `acme` with its identifiers and steps): expected site `site-1`
   / `SITE-1` with the sealed key still opening, one active placement for
   `acme` with every identifier, steps kept with site `site-1` (Tasks 2,
   3, 4).
5. Partial failure: a tenant created on two sites whose second site
   fails at `role-membership`: expected tenant `failed`, first placement
   `active`, second `failed` with its error; a retry runs only the
   second site and ends `active` (Task 4).

---

### Task 0: Plan and the session ruling recorded

**Files:**
- Create: `docs/superpowers/plans/2026-10-20-milestone-4-sites.md` (this file)
- Modify: `docs/superpowers/specs/2026-10-20-sites-design.md` section 4.4

Ruling written into the spec: the chosen site is held in the Portico
session (server-side today), not in a separate `__Host-portico_site`
cookie. Reasons: "Disconnect remaining users" and the connected-users list
need the site on the server anyway; when the pair (design 4.11) turns the
session into a shared encrypted cookie, the site travels inside it. The
browser still cannot choose a site the session check refuses.

- [ ] **Step 1: Edit section 4.4's first bullet to the ruling above.**
- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/plans/2026-10-20-milestone-4-sites.md docs/superpowers/specs/2026-10-20-sites-design.md
git commit -m "docs: Milestone 4 plan; the chosen site lives in the Portico session"
```

---

### Task 1: Measurements on NTNX-01 (write-up 20)

**Files:**
- Create: `lab/sites/tenant-login.sh`, `lab/sites/console-static.sh`, `lab/sites/idp-entity-issuer.sh`, `lab/sites/pc-version.sh`
- Create: `docs/lab/2026-10-20-20-sites-on-one-prism-central.md`, row in `docs/lab/README.md`
- Modify: `lab/test_scripts.sh` (shellcheck and `--help` checks of the new scripts)

Measured on the appliance (`LAB_PRISM_HOST`, `LAB_ID_HOST`, `LAB_VM_IP`
pointing at 10.23.210.191) and Prism Central 10.23.210.175. Results fix
five values later tasks consume; each is written in the write-up's
"Implications" as a named decision.

- [ ] **Step 1: Probe user.** `lab/sites/tenant-login.sh --ensure-probe acme`
  creates `portico-probe-acme@lab.portico.test` in Keycloak (admin API,
  realm admin client from `~/portico-secrets-appliance/`), member of
  `tenant-acme`, non-temporary generated password saved to
  `~/portico-secrets-appliance/probe-acme-pw` (0600), never printed.
- [ ] **Step 2: Scripted tenant login.** `lab/sites/tenant-login.sh acme`
  logs the probe user in through the public name with curl only:
  `/` → Keycloak form → callback → landing → `/.portico/saml-start` →
  initiate-login → Keycloak's auto-post form → ACS POST → `/console/`.
  It prints each hop's status and every `Set-Cookie` as
  `name path httpOnly sameSite maxAge size` (no values) and leaves the
  jar in a 0700 temp dir for the next steps (`--keep-jar <dir>`).
  Expected: HTTP 200 on `/console/` and the cookie table.
- [ ] **Step 3: Item 1 and item 2, static part.** `lab/sites/console-static.sh <jar-dir>`
  downloads the console's JavaScript bundles referenced by `/console/`
  with that jar and greps, per cookie name from Step 2, for reads
  (`document.cookie`, cookie helper calls naming it) and for
  `localStorage`/`sessionStorage`/`indexedDB` keys. Output: which cookie
  names the JavaScript reads, which storage keys exist.
- [ ] **Step 4: Item 2, live part (Marco's browser).** With Marco logged in
  as a tenant user, read `Object.keys(localStorage)`,
  `Object.keys(sessionStorage)` and `indexedDB.databases()` in the console
  tab (Chrome extension `javascript_tool`, after asking Marco); record
  which keys carry Prism Central identifiers (cluster or entity UUIDs).
- [ ] **Step 5: Item 7.** Marco leaves one console tab idle for 5 minutes;
  count that session's requests in the appliance journal
  (`journalctl -u portico` access log, by time window and path). Record
  the polling interval.
- [ ] **Step 6: Item 3.** `lab/sites/idp-entity-issuer.sh` (Prism Central
  admin credentials from `~/portico-secrets/prism-pw`): reads the IdP,
  tries `PUT` with `entityIssuer=https://$LAB_PRISM_HOST/api/iam/authn/site-1`
  (If-Match), reads `sp-metadata`; when the PUT took, renames the Keycloak
  SAML client to the new entityID and runs `tenant-login.sh` (expected
  success), then sets the Keycloak client id to a different value and runs
  it again (audience check: does Prism Central refuse?). `--revert`
  restores the original IdP and client. Always ends reverted.
- [ ] **Step 7: Prism Central version.** `lab/sites/pc-version.sh` finds the
  v4 call that returns the Prism Central version with the service-account
  key (candidates: the PC entry of `clustermgmt` clusters with
  `config.buildInfo.version`, the `prism` domain manager endpoint); record
  path and field.
- [ ] **Step 8: Write the write-up** (template `docs/lab/TEMPLATE.md`) with
  these named decisions:
  - **D1 cookie table**: Prism Central cookie names, which are HttpOnly,
    which the JavaScript reads, largest size.
  - **D2 clear storage**: `true` when Step 4 found Prism Central
    identifiers in browser storage, else `false`.
  - **D3 SAML variant**: `A` (per-site `entityIssuer`, one Keycloak client
    per site) when Step 6's PUT took and the audience mismatch was
    refused; `B` (one shared Keycloak client) otherwise.
  - **D4 active window**: 2 minutes, or twice the polling interval when
    Step 5 found it above 1 minute.
  - **D5 version call**: path and field of Step 7.
- [ ] **Step 9: Verify and commit.** Run: `lab/test_scripts.sh`. Expected: green.

```bash
git add lab/sites lab/test_scripts.sh docs/lab
git commit -m "lab: write-up 20, Prism Central cookies, browser storage, SAML entity issuer and version call measured for sites"
```

---

### Task 2: Placements in the store

**Files:**
- Create: `internal/store/migrations/0004_placements.sql`, `internal/store/placements.go`
- Modify: `internal/store/store.go` (interface, `Step.SiteID`), the step methods' file
- Test: `internal/store/store_test.go`, `internal/store/placements_test.go`

**Interfaces:**
- Produces:
  ```go
  type PlacementStatus string // "creating", "active", "failed", "removing"
  type Placement struct {
      TenantID, SiteID                     string
      Status                               PlacementStatus
      ClusterExtID, StorageContainerExtID  string
      ProjectID, ProjectExtID, ResourceGroupExtID string
      SAMLGroupExtID, RoleMembershipExtID  string
      IdPShared                            bool
      LastError                            string
      CreatedAt, UpdatedAt                 time.Time
  }
  // Store gains:
  CreatePlacement(ctx context.Context, p *Placement) error          // ErrExists on (tenant, site)
  GetPlacement(ctx context.Context, tenantID, siteID string) (*Placement, error)
  ListPlacements(ctx context.Context, tenantID string) ([]Placement, error) // by created_at
  ListPlacementsBySite(ctx context.Context, siteID string) ([]Placement, error)
  UpdatePlacement(ctx context.Context, p *Placement) error
  DeletePlacement(ctx context.Context, tenantID, siteID string) error
  DeletePlacementsBySite(ctx context.Context, siteID string) (int, error)
  ```
  `Step` gains `SiteID string` (`""` for tenant-wide steps).
  `LegacySiteID = "site-1"` lives in `store` (the migration's literal).

- [ ] **Step 1: Write the failing tests**: `TestPlacementCRUD` (create,
  duplicate → `ErrExists`, get, list order, update, delete, delete of a
  missing one → `ErrNotFound`), `TestPlacementsCascadeWithTenant`
  (deleting the tenant deletes its placements),
  `TestDeletePlacementsBySite` (count), `TestStepSiteRoundTrip`, and
  `TestMigrationMovesTenantIdentifiersToSite1`: open a database at schema
  3 (apply 0001–0003 by hand), insert tenant `acme` with every identifier
  and a settings row whose JSON has `tenants.cluster_ext_id` `c-1` and
  `tenants.storage_container_ext_id` `sc-1`, plus one step; reopen with
  `OpenSQLite`; expect one placement `acme`/`site-1` with status `active`
  (tenant `active`), the five identifiers, `IdPShared`, cluster `c-1`,
  container `sc-1`, and the step's `SiteID` `site-1` except the
  `keycloak-group` step (`""`).
- [ ] **Step 2: Run** `go test ./internal/store/ -run 'Placement|Migration|StepSite' -v`.
  Expected: FAIL (undefined).
- [ ] **Step 3: Implement.** Migration 0004: table `tenant_placements`
  (primary key `(tenant_id, site_id)`, `tenant_id` references `tenants`
  on delete cascade, the fields above), `tenant_steps.site_id TEXT NOT NULL
  DEFAULT ''`; copy every tenant with a non-empty `project_id` into a
  placement on `site-1` (status: `active` when the tenant is `active` or
  `disabled`, else the tenant's status mapped to `creating`/`failed`),
  cluster and container from
  `json_extract(CAST(value AS TEXT), '$.tenants.cluster_ext_id')` of
  setting `settings` (empty when absent); set `site_id='site-1'` on steps
  whose name is not `keycloak-group`. The tenant columns stay until Task 4.
- [ ] **Step 4: Run** `go test ./internal/store/ -v`. Expected: PASS.
- [ ] **Step 5: Commit** — `store: placements of tenants on sites; legacy tenants moved to site-1`.

---

### Task 3: Sites in the settings and on the setup page

**Files:**
- Create: `internal/settings/sites.go`, `internal/settings/version.go`
- Modify: `internal/settings/{settings.go,service.go,check.go,bootstrap.go,bootstrap_prism.go,bootstrap_saml.go}`
- Modify: `internal/nutanix/{iam.go,clusters.go}` (version call D5, `EntityIssuer` in `SAMLIdPSpec`)
- Create: `internal/api/sites.go`; Modify: `internal/api/{api.go,settings.go}`
- Create: `internal/web/sites.go`, `internal/web/templates/site.html`, `internal/web/templates/site_new.html`; Modify: `internal/web/{setup.go,templates/setup.html}`
- Modify: `cmd/portico/{serve.go,settings_cmd.go}`, `internal/config/config.go` (only what `FromConfig` needs)
- Test: `internal/settings/{settings_test.go,sites_test.go,bootstrap_prism_test.go,bootstrap_saml_test.go}`, `internal/settings/settingstest/fakes.go`, `internal/nutanix/client_test.go`, `internal/api/{settings_test.go,sites_test.go,bootstrap_test.go}`, `internal/web/{setup_test.go,sites_test.go}`, `cmd/portico/{serve_test.go,main_test.go}`

**Interfaces:**
- Produces (settings):
  ```go
  type Site struct {
      ID, Code, Name string; Order int
      URL string; TLSVerify bool; CAPEM string; APIKey Secret
      SAMLIdPName, SAMLEntityID string
      ClusterExtID, StorageContainerExtID string // defaults for placements
  } // JSON: id code name order url tls_verify ca_pem api_key saml_idp_name saml_entity_id cluster_ext_id storage_container_ext_id
  Settings.Sites []Site            // `json:"sites"`; replaces PrismCentral and tenants.cluster/container
  Settings.PassthroughSite string  // `json:"passthrough_site"`, "" = first by order
  func (s Settings) Site(id string) (Site, bool)
  func (s Settings) OrderedSites() []Site        // by Order, then Code
  func (s Settings) PassthroughSiteOrFirst() (Site, bool)
  func ValidSiteID(string) bool; func ValidSiteCode(string) bool
  func SiteIDFromCode(code string) string        // lower-case of the code
  const LegacySiteID, LegacySiteCode = store.LegacySiteID, "SITE-1"
  ```
  `Service.Get` turns a stored document with a non-empty legacy
  `prism_central.url` and no `sites` into one site (`site-1`, `SITE-1`,
  name = URL host, the sealed key moved before opening, cluster and
  container from the legacy `tenants` fields) and logs it once.
  `secretFields` iterates the sites (`sites.<id>.api_key`); `Update`
  keeps a stored site key when the incoming one is empty, matching by id.
  `Missing()`: `sites` when none; per site in order
  `sites.<id>.url`, `sites.<id>.api_key`, `sites.<id>.saml_idp_name`.
  `ValidateFormat`: id and code shapes, unique ids and codes, URL shape
  per site, `passthrough_site` names an existing site when set.
- Produces (settings, per site): `Checker.PrismCentral(ctx, site Site,
  apiKey string) CheckResult`, `CheckResult.Version string` (`json:"version,omitempty"`),
  failing with "Prism Central <version> is older than pc.7.6" below 7.6
  (this is where "add site" first talks to Prism Central: `POST /sites`
  stores the connection, the check and the Prism Central bootstrap refuse
  an old version and the bootstrap stores no key);
  `func VersionAtLeast(v string, major, minor int) bool` (`pc.7.6.0.6`,
  `7.6`, `pc.2024.3` → false for 7.6). `Bootstrapper.PrismCentral(ctx, s,
  siteID string, in PrismInput)` and `Bootstrapper.SAML(ctx, keycloakURL,
  s, siteID string, in SAMLInput)`: an unknown site id is a `*StateError`;
  results are written into that site only. SAML, by D3:
  variant A sets `EntityIssuer: "https://<prism_host>/api/iam/authn/<site-id>"`
  and registers one Keycloak client per site with that client id;
  variant B registers the client once and, when it exists, only ensures
  its redirect URIs; both store `SAMLEntityID` on the site.
- Produces (nutanix): `(*Client) PrismCentralVersion(ctx) (string, error)`
  (D5); `SAMLIdPSpec.EntityIssuer string` (sent only when non-empty).
- Produces (API, admin listener):
  - `GET /api/v1/sites` → `{"sites":[siteJSON...]}`, siteJSON = the Site
    fields without `api_key`, plus `api_key_set`, `tenants` (ids with a
    placement); ordered.
  - `POST /api/v1/sites` → 201 siteJSON; body code, name, order, url,
    tls_verify, ca_pem, cluster_ext_id, storage_container_ext_id, optional
    `id` (default `SiteIDFromCode(code)`); 400 on shape, 409 on a
    duplicate id or code.
  - `GET|PUT /api/v1/sites/{id}`: PUT changes everything but the id; an
    empty `api_key` keeps the stored one.
  - `DELETE /api/v1/sites/{id}` → 204; 409 `{"error":"site <code> has
    tenants: <ids>"}` when placements exist.
  - `POST /api/v1/sites/{id}/forget` body `{"confirm":"<code>"}` → 200
    `{"forgotten":"<code>","left_in_prism_central":[...]}` listing per
    placement the project, SAML user group, role membership and IdP
    sharing, plus the IdP and the service account; 400 when `confirm`
    differs from the code; 409 while a tenant operation runs. Deletes the
    placements (`DeletePlacementsBySite`) and the site from the document,
    logs the list at Warn, makes no call to that Prism Central.
  - `POST /api/v1/sites/{id}/check`, `/bootstrap/prism-central`,
    `/bootstrap/saml`: the 2e bodies and answers, per site.
  - Settings JSON loses `prism_central` and the tenants cluster and
    container; gains `passthrough_site`; `GET /settings` keeps `missing`.
    The routes `settings/bootstrap/prism-central` and `settings/bootstrap/saml`
    are removed.
- Produces (UI): the setup page's Prism Central card becomes "Sites": a
  table (code, name, URL, key set, identity provider set, tenants) and
  "Add site" (`GET|POST /setup/sites/new`); `GET /setup/sites/{id}` with
  three cards (connection with save and check and the cluster/container
  default pickers; service account bootstrap; SAML bootstrap) and a
  "Remove" card (delete, or forget with the code typed again).
- Produces (serve): running mode builds one Prism Central client, TLS
  configuration and upstream per site; until Task 7 the public handler,
  the landing page and the tenant service use the first site by order;
  the pass-through uses `PassthroughSiteOrFirst`. `settings show` prints
  the sites; `settings import` builds `site-1` from the YAML.

- [ ] **Step 1: Write the failing tests**:
  `TestLegacyDocumentBecomesSite1` (store a legacy JSON with a sealed
  key; `Get` returns one site with the opened key and no legacy block),
  `TestMissingListsSiteFields`, `TestValidateSiteShapes` (bad id, bad
  code, duplicate code, `passthrough_site` unknown),
  `TestUpdateKeepsSiteKeyByID`, `TestVersionAtLeast` (table above),
  `TestCheckerRefusesOldPrismCentral` (fake version `pc.2024.3` →
  `ok:false`, message names it), `TestBootstrapPrismUnknownSite`,
  `TestBootstrapSAMLPerSite` (the D3 variant: two sites against two fake
  Prism Centrals → variant A: two Keycloak clients with the per-site
  entity ids; variant B: one client, the second bootstrap only ensures
  it), `TestSitesAPICRUD`, `TestSitesAPIDeleteRefusedWithTenants`,
  `TestSitesAPIForget` (confirm mismatch 400; success removes placements
  and site, answer lists the leftovers, no request reaches the fake
  Prism Central), `TestSetupPageListsSites`, `TestSiteNewForm`,
  `TestServeRunsFromSitesDocument` (store with one site → running mode).
- [ ] **Step 2: Run** `go test ./internal/settings/ ./internal/api/ ./internal/web/ ./cmd/portico/ 2>&1 | tail -30`.
  Expected: FAIL on the new tests (undefined names), nothing else.
- [ ] **Step 3: Implement** the settings, nutanix, API, UI and serve parts
  above; update every existing test that built a `prism_central` block to
  build a site instead (no test deleted without its replacement).
- [ ] **Step 4: Run** `go vet ./... && gofmt -l cmd internal && go test ./...`.
  Expected: clean and PASS.
- [ ] **Step 5: Commit** — `settings: sites instead of one Prism Central; sites API and setup pages; version check`.

---

### Task 4: Tenants on sites

**Files:**
- Modify: `internal/tenant/{service.go,provision.go,visibility.go}`, `internal/tenant/tenanttest/fakes.go`
- Create: `internal/store/migrations/0005_tenant_columns.sql`; Modify: `internal/store/store.go` and the tenant methods (drop the moved fields)
- Modify: `internal/api/api.go`, `internal/web/{tenants.go,templates/tenant.html,templates/tenant_new.html,templates/tenants.html}`, `cmd/portico/serve.go`
- Test: `internal/tenant/service_test.go`, `internal/api/api_test.go`, `internal/web/tenants_test.go`, `internal/store/store_test.go`

**Interfaces:**
- Consumes: Task 2 placements; Task 3 `settings.Site`.
- Produces (tenant):
  ```go
  type Site struct { ID, Code string; PC PrismCentral; SAMLIdPName string }
  func NewService(st store.Store, sites []Site, kc Keycloak, cfg Config, log *slog.Logger) *Service
  // Config loses SAMLIdPName, ClusterExtID, StorageContainerExtID.
  type PlacementRequest struct { Site, ClusterExtID, StorageContainerExtID string }
  type CreateRequest struct { Slug, DisplayName, Contacts string; Sites []PlacementRequest }
  func (s *Service) AddSite(ctx context.Context, id string, p PlacementRequest) error              // background
  func (s *Service) RemoveSite(ctx context.Context, id, siteID string, deleteProject bool) error    // background
  func (s *Service) Visibility(ctx context.Context, id string) (map[string]*Visibility, error)      // by site id
  func (s *Service) OnPlacementChange(fn func(tenantID string))                                    // Task 6's index
  var ErrUnknownSite, ErrNoSites, ErrLastSite, ErrPlacementExists, ErrNoPlacementTarget error
  ```
  `Create` with no sites and exactly one configured site uses it;
  otherwise `ErrNoSites`. A placement request without cluster or
  container takes the site's defaults; both empty → `ErrNoPlacementTarget`.
  Provision: `keycloak-group` once (step site `""`), then per placement
  not `active`: the 2a steps from `project` to `visibility` against that
  site's client and IdP, steps recorded with the site id, placement
  status `creating` → `active`/`failed` with `LastError`. Tenant status
  `active` when every placement is active, `failed` when any failed,
  `LastError` = `site <code>: <step>: <error>`. `Retry` runs on a failed
  tenant (not after a failed deletion, as today) and only on placements
  not `active`. `AddSite` refuses an existing placement and an unknown
  site; the tenant stays `active` and the new placement carries its own
  status. `RemoveSite` refuses the last placement (`ErrLastSite`), runs the
  2a deletion steps on that site, deletes the placement row. `Delete`
  removes every placement, then the Keycloak group, then the row; a
  failure leaves the tenant `failed` with `LastOp` delete and the
  remaining placements.
- Produces (API): tenantJSON loses the project fields and gains
  `placements` (site, code, status, cluster_ext_id,
  storage_container_ext_id, project_id, project_ext_id,
  resource_group_ext_id, saml_group_ext_id, role_membership_ext_id,
  idp_shared, last_error); steps gain `site`; `POST /tenants` takes
  `sites` (`[{"site","cluster_ext_id","storage_container_ext_id"}]`);
  `POST /tenants/{id}/sites` → 202, `DELETE /tenants/{id}/sites/{site}?delete_project=true`
  → 202 (409 `ErrLastSite`, 404 unknown); `GET /tenants/{id}/visibility`
  → `{"sites":{"<id>":{"ok":..,"findings":[..]}}}`.
- Produces (UI): tenant list shows the site codes; "New tenant" shows one
  row per site (checkbox, cluster and container selects loaded with HTMX
  from `GET /tenants/new/sites/{id}/pickers`, prefilled with the site's
  defaults); tenant detail has one section per placement (status, steps of
  that site, visibility, "Remove from this site" with the delete-project
  choice) and "Add a site".
- Migration 0005 drops the tenant columns moved in Task 2 and the
  `store.Tenant` fields go with them.

- [ ] **Step 1: Write the failing tests** (two fake Prism Centrals,
  `tenanttest.NewPC` each): `TestCreateOnTwoSites` (both placements
  active, project on each fake, one Keycloak group),
  `TestPartialFailureAndRetry` (Review Focus 5),
  `TestCreateDefaultsToOnlySite`, `TestPlacementDefaultsFromSite`,
  `TestAddSiteLater`, `TestRemoveSiteKeepsOtherSite`,
  `TestRemoveLastSiteRefused`, `TestDeleteRemovesEverySite`,
  `TestVisibilityPerSite`, `TestPlacementChangeHookFires`,
  `TestTenantAPIPlacementsShape`, `TestTenantAPIAddRemoveSite`,
  `TestNewTenantFormHasOneRowPerSite`,
  `TestMigrationDropsTenantColumns`.
- [ ] **Step 2: Run** `go test ./internal/tenant/ ./internal/api/ ./internal/web/ ./internal/store/`.
  Expected: FAIL on the new tests.
- [ ] **Step 3: Implement**; serve passes every site to `NewService`.
- [ ] **Step 4: Run** `go vet ./... && gofmt -l cmd internal && go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** — `tenant: placements on sites; add and remove a site; per-site visibility`.

---

### Task 5: The session knows its tenant and its site

**Files:**
- Modify: `internal/auth/{session.go,handlers.go,oidc.go}`, `internal/idp/keycloak/client.go`
- Create: `internal/tenant/resolve.go`
- Test: `internal/auth/session_test.go`, `internal/auth/oidc_test.go`, `internal/idp/keycloak/client_test.go`, `internal/tenant/resolve_test.go`

**Interfaces:**
- Produces:
  ```go
  // auth.Session gains:
  Tenant    string    // tenant id resolved at login; "" when none
  Site      string    // chosen site id; "" until chosen
  SiteSince time.Time
  // auth.MemoryStore gains:
  func (m *MemoryStore) SetSite(id, site string, now time.Time) bool // false when the session is gone
  func (m *MemoryStore) ClearSite(site string) int                   // sessions on that site lose it
  func (m *MemoryStore) List(now time.Time) []Session                // live sessions, expired ones dropped
  const ActiveWindow = 2 * time.Minute                                // or D4
  // auth.OIDCConfig gains:
  ResolveTenant func(ctx context.Context, subject string) (string, error)
  // keycloak:
  func (c *Client) UserGroups(ctx context.Context, userID string) ([]Group, error)
  // tenant:
  func Resolver(kc interface{ UserGroups(context.Context, string) ([]keycloak.Group, error) }, st store.Store, groupPrefix string, log *slog.Logger) func(ctx context.Context, subject string) (string, error)
  ```
  The callback stores the resolved tenant in the session; a resolver error
  is logged and leaves `Tenant` empty (the login itself succeeds; Task 7
  shows the "no site" page). The resolver returns the tenant whose group
  `<prefix><slug>` the user is in and whose row exists; none → `""`; more
  than one → `""` and a Warn naming the groups.

- [ ] **Step 1: Write the failing tests**: `TestSetSiteAndClearSite`,
  `TestListDropsExpired`, `TestCallbackStoresTenant` (fake IdP, resolver
  returns `acme`), `TestCallbackResolverErrorStillLogsIn`,
  `TestUserGroups` (fake Keycloak `GET /users/{id}/groups`),
  `TestResolverOneTenant`, `TestResolverNoGroup`,
  `TestResolverTwoTenantsIsNone`, `TestResolverGroupWithoutRow`.
- [ ] **Step 2: Run** `go test ./internal/auth/ ./internal/idp/keycloak/ ./internal/tenant/ -run 'Site|List|Callback|UserGroups|Resolver'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** — `auth: the session carries its tenant and site; tenant resolved from the Keycloak group at login`.

---

### Task 6: Site runtime: probe, maintenance, connected users

**Files:**
- Create: `internal/sites/{monitor.go,index.go}`, `internal/sites/{monitor_test.go,index_test.go}`
- Modify: `internal/api/{sites.go,settings.go}`, `internal/web/{sites.go,templates/site.html}`, `cmd/portico/serve.go`
- Test: `internal/api/sites_test.go`, `internal/web/sites_test.go`

**Interfaces:**
- Produces (sites):
  ```go
  type Status string
  const (Available Status = "available"; Maintenance Status = "maintenance"; Unreachable Status = "unreachable")
  const StateKey = "site_state" // store setting: {"maintenance":{"<site-id>":true}}
  type Prober func(ctx context.Context) error
  func NewMonitor(st store.Store, probes map[string]Prober, log *slog.Logger) *Monitor
  func (m *Monitor) Load(ctx context.Context) error
  func (m *Monitor) Run(ctx context.Context, interval, timeout time.Duration) // probes at once, then every interval
  func (m *Monitor) Status(siteID string) Status   // maintenance wins; unknown result counts as available
  func (m *Monitor) SetMaintenance(ctx context.Context, siteID string, on bool) error
  func NewIndex(st store.Store, ttl time.Duration) *Index
  func (x *Index) Sites(ctx context.Context, tenantID string) ([]string, error) // active placements only
  func (x *Index) Invalidate(tenantID string)
  ```
  serve: probes = each site client's `PrismCentralVersion`, interval 30 s,
  timeout 5 s; index TTL 5 s, invalidated by `tenant.OnPlacementChange`.
- Produces (API): `api.SitesRuntime` interface
  (`Status`, `SetMaintenance`, `Sessions(siteID string, now time.Time) []auth.Session`,
  `Disconnect(siteID string) int`) as `SettingsOptions.Runtime`; nil in
  setup mode → these routes answer 503 with `SetupModeMessage`.
  `PUT /api/v1/sites/{id}/maintenance` `{"maintenance":true}` → 200
  `{"status":"maintenance"}`; `GET /api/v1/sites/{id}/sessions` →
  `{"active":n,"idle":n,"sessions":[{"tenant","email","since","last_activity","state"}]}`
  (state `active` when `LastSeen` is within `auth.ActiveWindow`);
  `POST /api/v1/sites/{id}/sessions/disconnect` → `{"disconnected":n}`,
  logged at Warn with the site and the count. `GET /sites` adds `status`,
  `maintenance`, `active`, `idle`.
- Produces (UI): the site page gets a status badge, the maintenance
  switch, "N users still connected" refreshed every 10 s with HTMX
  (`GET /setup/sites/{id}/sessions`), the list (tenant, email, on the
  site since, last activity, active/idle), and "Disconnect remaining
  users" (POST, shown only when N > 0, the button names N). The page
  states the two limits of spec 4.1 (only access through Portico; per
  node in a pair).

- [ ] **Step 1: Write the failing tests**: `TestMonitorStatus`
  (probe ok → available; probe error → unreachable; maintenance wins;
  before any probe → available), `TestMaintenancePersists` (new monitor
  over the same store reads it), `TestIndexTTLAndInvalidate`,
  `TestIndexActiveOnly`, `TestMaintenanceAPIImmediate`,
  `TestSessionsAPIActiveIdle` (a session with LastSeen 30 s ago → active,
  10 min ago → idle), `TestDisconnectAPIClearsSite`,
  `TestRuntimeRoutes503InSetupMode`, `TestSitePageShowsCountAndDisconnect`.
- [ ] **Step 2: Run** `go test ./internal/sites/ ./internal/api/ ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement**, wire in serve (`Run` under the serve context).
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** — `sites: probe, maintenance and connected users on the admin API and UI`.

---

### Task 7: Public routing by site and the site page

**Files:**
- Create: `internal/proxy/sites.go`, `internal/proxy/sites_test.go`, `internal/branding/templates/sites.html`
- Modify: `internal/proxy/{public.go,landing.go}`, `internal/branding/pages.go`, `internal/branding/templates/landing.html`, `cmd/portico/serve.go`
- Test: `internal/proxy/public_test.go`, `internal/proxy/landing_test.go`, `internal/branding/pages_test.go`, `cmd/portico/serve_test.go`

**Interfaces:**
- Consumes: Task 5 `Session.Tenant/Site`, `SetSite`; Task 6 `Status`, `Index.Sites`.
- Produces:
  ```go
  type SiteBackend struct {
      ID, Code, Name string; Order int
      Upstream http.Handler      // Prism Central of the site
      Landing  *LandingOptions   // per site: PrismHTTP, PrismBase, IdPExtID
  }
  type SiteRouting struct {
      Backends   map[string]*SiteBackend
      Placements func(ctx context.Context, tenantID string) ([]string, error)
      Status     func(siteID string) sites.Status
      SetSite    func(sessionID, siteID string, now time.Time) bool
      Pages      *branding.Pages
  }
  // PublicOptions: Prism and Landing are replaced by Sites *SiteRouting
  // and Logo func(ctx) ([]byte, string, bool).
  // branding:
  type SiteTile struct { ID, Code, Name, Status string; Selectable bool }
  func (p *Pages) Sites(w http.ResponseWriter, r *http.Request, tiles []SiteTile, email string)
  ```
- Behaviour on the Prism Central host, after the deny list and the session:
  - `GET /.portico/sites`: the tiles of the tenant's active placements in
    site order (variant B: code, name, status; not selectable unless
    `available`), the user's email, "Sign out" (`/.portico/logout`).
    One placement → 303 `/`.
  - `POST /.portico/sites/select` (form `site`): refused 403 when
    `Sec-Fetch-Site` is present and not `same-origin`, when the site is not
    among the tenant's active placements, or when it is not `available`;
    otherwise `SetSite` and 303 `/`.
  - Any other path: the session's site must be among the tenant's active
    placements and have a backend. No site and one placement: selected
    automatically when `available`, else the branded 503 page "<code> is
    in maintenance" / "<code> is unreachable" (JSON 503 for
    non-navigations). No site and several placements, or a site no longer
    allowed: navigation → 303 `/.portico/sites`, anything else 403 rule
    `site-required`. No placement (tenant `""` or none active): branded
    403 "No site is available for your account. Contact your provider."
    A session already on a site keeps being proxied when the site is in
    maintenance or unreachable.
  - Then the site's landing interception, sign-out interception,
    allow-list and upstream, as today.
  - The landing page gets a "Switch site" link to `/.portico/sites`
    when the tenant has more than one active placement.
- `/.portico/login`, `callback`, `logout`, `logo` stay site-independent.

- [ ] **Step 1: Write the failing tests** (two `httptest` Prism Centrals
  that echo their name): `TestSitePageListsActivePlacementsOnly`,
  `TestSelectRefusesForeignSite` (Review Focus 1, three cases),
  `TestSelectRefusesCrossSite`, `TestSelectThenProxyGoesToChosenSite`,
  `TestSingleSiteAutoSelected`, `TestSingleSiteInMaintenanceShowsPage`,
  `TestSessionOnSiteInMaintenanceKeepsWorking`,
  `TestRemovedPlacementStopsRouting` (Review Focus 2, including
  `ClearSite`), `TestNoTenantShowsNoSitePage` (no request reaches either
  upstream), `TestDenyListBeforeSiteCheck` (`Authorization` header with a
  session on a site → 403 deny rule), `TestLandingUsesSiteIdP`,
  `TestSitesTemplateVariantB`.
- [ ] **Step 2: Run** `go test ./internal/proxy/ ./internal/branding/`. Expected: FAIL.
- [ ] **Step 3: Implement**; serve builds one `SiteBackend` per site.
- [ ] **Step 4: Run** `go test ./...` and `lab/test_scripts.sh`. Expected: PASS.
- [ ] **Step 5: Commit** — `proxy: tenants reach the Prism Central of the chosen site; site page`.

---

### Task 8: Prism Central's cookies kept apart per site

**Files:**
- Create: `internal/proxy/stash.go`, `internal/proxy/stash_test.go`
- Modify: `internal/proxy/{sites.go,upstream.go}`, `cmd/portico/serve.go`

**Interfaces:**
- Produces:
  ```go
  const stashPrefix = "__Host-pstash." // "__Host-pstash.<site-id>.<cookie-name>"
  func StashName(siteID, name string) string
  type CookieAttrs struct{ HttpOnly bool }
  var prismCookieAttrs map[string]CookieAttrs // D1; unknown names: HttpOnly true
  const clearStorageOnSwitch = false          // D2
  func switchSiteCookies(w http.ResponseWriter, r *http.Request, from, to string, maxAge time.Duration)
  // UpstreamOptions gains StripCookiePrefixes []string
  ```
  On a select whose target differs from the current site: every request
  cookie that is neither a Portico cookie nor a stash cookie is written as
  `StashName(from, name)` (Secure, HttpOnly, SameSite Lax, Path `/`,
  max age = the session's) and expired under its own name; every
  `StashName(to, *)` cookie is restored under its name with
  `prismCookieAttrs` and expired as stash. First selection (`from` empty)
  stashes nothing. With D2 true the response also carries
  `Clear-Site-Data: "storage"`. Every upstream strips the stash prefix and
  the Portico cookies. Logout already expires every cookie.

- [ ] **Step 1: Write the failing tests**: `TestSwitchStashesAndRestores`
  (A→B→A: B starts without A's cookies, back on A the original names and
  values return), `TestStashNeverReachesUpstream` (Review Focus 3: both
  fake Prism Centrals record the Cookie header; no `__Host-pstash.` and no
  Portico cookie ever), `TestSiteIDsWithDashesDoNotCollide`
  (`az-it-mi1` and `az-it-mi1-b`), `TestFirstSelectionStashesNothing`,
  `TestRestoredAttributesFollowTable`, `TestClearSiteDataFollowsD2`.
- [ ] **Step 2: Run** `go test ./internal/proxy/ -run 'Stash|Switch|Restored|ClearSite|FirstSelection|Dashes'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** — `proxy: Prism Central cookies stashed per site on a switch`.

---

### Task 9: Documentation

**Files:**
- Modify: `docs/superpowers/specs/2026-10-17-portico-design.md` (the changes listed in spec section 5; 4.2 firewall names every site; 4.4 placements; 4.10 host names replaced; 4.11 pair; 2 deferred list with DR and the native interface; 9 milestones 4 sites, 5 backlog sweep, 6 first release)
- Create: `docs/adr/0004-sites-behind-one-host.md` (one host, site in the session, cookie stash, D3's SAML variant, costs)
- Modify: `docs/install.md` (9440 of every site from Portico's address; one public name for all sites), `docs/operating.md` (sites, add/edit/forget, maintenance, connected users, disconnect), `README.md` (status, milestones), `docs/backlog.md` (DR configuration; region grouping, variant C; API versions per site; the two-site live test; session list shared in the pair)

- [ ] **Step 1: Write the changes.**
- [ ] **Step 2: Verify**: `grep -n "one public host name\|own public host name" docs/superpowers/specs/2026-10-17-portico-design.md` shows no "own public host name" per Prism Central left; `lab/test_scripts.sh` green.
- [ ] **Step 3: Commit** — `docs: sites in the design, ADR 0004, install and operating guides, backlog`.

---

### Task 10: Lab acceptance on the appliance (write-up 21)

**Files:**
- Create: `lab/sites/acceptance.sh`, `docs/lab/2026-10-20-21-sites-acceptance.md`, row in `docs/lab/README.md`
- Modify: `lab/tenants/acceptance.sh` (site parameter, placements shape), `lab/portico/bootstrap-acceptance.sh` (per-site bootstrap routes), `lab/test_scripts.sh`

Steps on 10.23.210.191, announced to Marco before Step 2 (the appliance
restarts and tenant sessions end):

- [ ] **Step 1: Back up** the appliance store and master key (as in 2e) to the appliance's own disk.
- [ ] **Step 2: Upgrade** the binary in place with the installer
  (`deploy/install.sh --binary`, design 4.9: re-running upgrades).
  Expected: `portico` active, log line naming the legacy document moved
  to `site-1`.
- [ ] **Step 3: `lab/sites/acceptance.sh`** checks, each printed ok/FAIL:
  `GET /sites` has `site-1`/`SITE-1` with the key set and tenant `acme`;
  `acme` has one active placement with its previous identifiers; rename
  the code to `AZ-IT-MI1`, Apply, wait ready; `tenant-login.sh acme`
  reaches `/console/` without the site page (one site); maintenance on →
  `/sites/site-1/sessions` shows the probe session `active`; a new login
  gets the maintenance page; disconnect → `{"disconnected":1}`; the old
  session's next navigation gets the maintenance page; maintenance off →
  login reaches the console; `lab/auth/probe-local-login.sh` all 403;
  `lab/tenants/acceptance.sh` creates, checks and deletes a tenant on
  `site-1`.
- [ ] **Step 4: Two-site live test**: not run; the write-up lists the
  checks to define with Marco when the second pc.7.6 lab cluster is up
  (spec section 6 items 4–6 plus the SAML variant measured on two Prism
  Centrals) and `docs/backlog.md` carries it.
- [ ] **Step 5: Write the write-up, commit** — `lab: write-up 21, sites accepted on the appliance with one site`.

---

## Handoff

After Task 10: whole-branch review, fix pass, pull request (rebase
merge), tag `m4-sites`, memory `portico-lab-state.md` updated (sites,
probe user, second cluster pending). Tell Marco: what the appliance now
shows, the D1–D5 decisions, that the two-site live test waits for his
second cluster.
