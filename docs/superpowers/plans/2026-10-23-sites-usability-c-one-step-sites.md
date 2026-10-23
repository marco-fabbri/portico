# Sites Usability, Part C: One-Step Sites, Status, Pinning, Runtime Changes — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An MSP adds a Prism Central on one page, giving its address,
confirming its certificate when it is not trusted, and entering an
administrator's credentials once. Portico creates the service account,
the SAML trust and the defaults, and the site is live with no Apply. A
site's page shows what Prism Central says, not what Portico stored. A
renewed certificate stops the site until the MSP confirms the new one.
Repair and removal clean up after themselves.

**Architecture:**
- **Trust.** A site trusts its Prism Central either by verified roots or
  by a pinned SHA-256 of the leaf certificate. One TLS builder serves
  every caller, and "verify off" is gone from the UI. A pin mismatch is a
  typed error.
- **Setup run.** One synchronous call with a deadline. Each step is
  idempotent and recorded for the summary, and a failure leaves the site
  incomplete and not served. Repair is the same run: when the identity
  provider's id changes, it re-runs the SAML steps of every connected
  tenant.
- **Runtime changes.** A site runtime in `cmd/portico` builds each
  site's client, upstream, probe and tenant entry from the settings
  document. It swaps them in when the document changes (at once on this
  node, within 30 s on the pair's other node).
- **Status.** The monitor gains the state **incomplete** (key refused,
  trust missing) and a reason ("certificate changed", with the new
  fingerprint).

**Tech Stack:** Go 1.27 standard library (`crypto/tls`, `crypto/sha256`,
`crypto/x509`, `sync/atomic`), SQLite store, `html/template` + HTMX in the
admin UI, Prism Central v4 IAM and clustermgmt APIs, Keycloak admin API.

**Spec:** `docs/superpowers/specs/2026-10-22-sites-usability-design.md`
4.1, 4.2, 4.3, 4.8 (last bullet), 4.9 and section 5, approved 2026-10-22.
- Decision lines: `docs/lab/2026-10-22-24-sites-usability-measurements.md`
  items 2, 3 and 5.
- Findings: `docs/lab/2026-10-23-25-sites-connections.md`. The floating
  IP read is part of the role question; NTNX-01's sporadic 401s matter
  for the key check.
- Parts A and B are merged (PR #27).

## Global Constraints

**Repository and commits**
- All text on disk in English.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`
  and use the author email `9093189+marco-fabbri@users.noreply.github.com`
  (`git -c user.email=... commit`).
- Branch `sites-usability-c` from `main`. Push and PR only with Marco's
  OK; Marco merges with rebase.
- No new Go module dependency, and `CGO_ENABLED=0`. At every commit:
  `go vet ./...`, `gofmt -l cmd internal` and `go test ./...` clean, and
  `lab/test_scripts.sh` green.
- Shell scripts avoid `A && B || C` unless B is `echo` (CI's shellcheck,
  PR #25).
- Pages use the existing styles and keep the admin CSP. No JavaScript
  beyond the HTMX already used.

**Administrator credentials**
- They are never stored, logged or kept beyond the request.
- They are never sent to a Prism Central whose certificate is neither
  verified nor matching the pin the MSP confirmed in the same request
  (spec anti-goals). The credentialed client is built only after the
  certificate check.
- No path turns certificate verification off for a new site. A stored
  site with `tls_verify: false` and no pin keeps working (the lab's
  NYC1), with a warning.

**Status and site state**
- A status is never shown as configured or ok from Portico's own row.
  It comes from a call to Prism Central or Keycloak.
- A half-created site is never served: `ServedSites()` stays the only
  gate, and an incomplete site is listed with "Finish setup".
- Wording:
  - **Add a site**, **Finish setup**, **Repair**, **Remove**, **Forget**;
  - **Pin the current certificate** and **Confirm the new certificate**;
  - the states read **incomplete** and **certificate changed**.

**Lab safety**
- Never print, log or commit secrets: API keys, cookies, tokens,
  passwords, session ids.
- Marco's OK is needed first for:
  - any write to a lab Prism Central;
  - any appliance upgrade or restart (warn him and wait);
  - any browser step that needs the Prism Central administrator's
    password, which only Marco types.
- Never call NYC1 (`10.23.210.185`) with `lab/env.sh`'s credentials
  (write-up 25, "User locked out").

## Review Focus

1. **A pin that does not match.** A setup request whose pin does not
   match the presented certificate is refused before any call carrying
   the administrator's credentials. The fake Prism Central records no
   `Authorization` header. Test in Task 2.
2. **A renewed certificate on a pinned site.** It turns the site
   unreachable with "certificate changed" and the new fingerprint, at
   once rather than after three probes. Requests through the upstream
   fail with the branded "not available" page, and nothing reconnects
   until the pin is confirmed. Tests in Tasks 2 and 5.
3. **A site swap while a request is in flight.** A request that started
   on the old backend completes. A session whose site was removed gets
   the "not available" page, not a 500. Old transports close their idle
   connections. Test in Task 4.
4. **The setup fails late.** The Keycloak client is refused after Prism
   Central's identity provider was created. The site stays incomplete
   and is not served, and "Finish setup" reuses that identity provider
   instead of refusing it as "another provider". When the run fails at
   the foreign-provider check, nothing has been created in Prism
   Central. Tests in Task 6.
5. **One 401 is not a refused key.** A single 401 from Prism Central (the
   lab's IAM gave four isolated ones in an hour, write-up 25) does not
   make the site incomplete. Three in a row do. Test in Task 5.

---

### Task 1: Containers, Objects and the default pick

Spec 4.1 step 4 and write-up 24 item 3.

**Files:**
- Modify: `internal/nutanix/clusters.go`
- Create: `internal/settings/defaults.go`
- Test: `internal/nutanix/client_test.go` (container parsing),
  `internal/settings/defaults_test.go`

**Interfaces:**
- Produces:
  - `nutanix.StorageContainer` gains the fields `IsInternal bool` (JSON
    `isInternal`), `IsObjects bool` and `FreeBytes int64`. `FreeBytes`
    is 0 when it is unknown. `IsSystem` stays the menu filter: it is
    `IsInternal` or a name in the existing list.
  - `func settings.PickDefaults(clusters []nutanix.Cluster, containers []nutanix.StorageContainer) (clusterExtID, containerExtID string)`
    returns `""` for what it cannot pick.

- [ ] **Step 1: Measure the free space field (read-only, MI1, lab admin)**

`lab/pc.sh GET /api/clustermgmt/v4.0/config/storage-containers` is already
recorded in `docs/lab/raw/24/m3.txt` without usage. Try
`GET /api/clustermgmt/v4.0/stats/storage-containers/{extId}` on the
`default-container` of NTNX-01 and note which field gives used or free
bytes.
- If no call answers with the service account's rights, rank by name.
  The ledger gets `Ruling: most free space unmeasurable, fallback by
  name`.
- Record the outcome as a line in Task 11's write-up.

- [ ] **Step 2: Write the failing tests**

`internal/settings/defaults_test.go`:
- `TestPickDefaultsSingleAOSCluster`: one AOS cluster and the Prism
  Central "cluster". The pick is the AOS cluster.
- `TestPickDefaultsSeveralClustersFirstByName`: AOS clusters `NTNX-02`
  and `NTNX-01`. The pick is `NTNX-01`.
- `TestPickDefaultsPrefersDefaultContainer`: `default-container-123`,
  `big` (more free), `SelfServiceContainer`. The pick is
  `default-container-123`.
- `TestPickDefaultsSkipsObjectsAndInternal`: no `default-container-*`;
  the candidates are `objectsl889411dccd9a44125aa3e31c2596dcf9` (most
  free), an `IsInternal` one and `data` (less free). The pick is `data`.
- `TestPickDefaultsOnlyOnThatCluster`: a container on another cluster is
  never picked.
- `TestPickDefaultsNothingEligible`: the result is `""` for the
  container.

`internal/nutanix/client_test.go`:
- `TestListStorageContainersFlags`: a list with `isInternal: true` and
  the Objects name gives `IsInternal`, `IsSystem` and `IsObjects` as
  expected. `SelfServiceContainer` is `IsSystem` without `IsInternal`.

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/settings/ ./internal/nutanix/ -run 'PickDefaults|StorageContainersFlags'`
Expected: FAIL (`PickDefaults` undefined, `IsObjects` unknown field).

- [ ] **Step 4: Implement**

- `IsObjects` is `regexp.MustCompile("^objects[a-z0-9]{20,}$")` on the
  name.
- `PickDefaults`:
  - cluster: AOS clusters sorted by name, take the first;
  - container on it: not `IsSystem`, not `IsObjects`;
  - prefix `default-container` first;
  - then the highest `FreeBytes`;
  - ties are broken by name.
- `Checker.PrismCentral`'s menus stay as they are: Objects containers
  remain choosable (decision of write-up 24).

- [ ] **Step 5: Run the suite, commit**

Run: `go test ./...` — Expected: PASS.

```bash
git add internal
git commit -m "feat(settings): default cluster and storage container picked as the design says, never an Objects container"
```

---

### Task 2: Certificates, verified or pinned

Spec 4.3, plus the step 1 of 4.1.

**Files:**
- Create: `internal/proxy/pin.go`, `internal/settings/inspect.go`
- Modify: `internal/proxy/upstream.go` (`PrismTLSPEM`),
  `internal/settings/sites.go` (`Site`), every `PrismTLSPEM` caller
  (`cmd/portico/serve.go` `siteTarget`, `internal/settings/check.go`,
  `bootstrap_prism.go`, `bootstrap_saml.go`)
- Test: `internal/proxy/pin_test.go`, `internal/settings/inspect_test.go`,
  `internal/settings/sites_test.go`

**Interfaces:**
- Produces:
  - `type proxy.SiteTrust struct { CAPEM []byte; Pin string; Verify bool }`.
    `Pin` is the colon-separated upper-case SHA-256 of the leaf DER.
  - `func proxy.PrismTLSFor(host string, t SiteTrust) (*tls.Config, error)`
    replaces `PrismTLSPEM`, and every caller moves to it.
    - `Pin != ""`: `InsecureSkipVerify: true` plus a
      `VerifyPeerCertificate` that compares `rawCerts[0]` with the pin,
      whatever `Verify` says.
    - Otherwise as today: `ServerName`, TLS 1.2 minimum, roots.
  - `type proxy.PinMismatchError struct { Want, Got string }`, with
    `Error()` reading `certificate changed: presented <Got>`. It reaches
    callers through `errors.As` from the `url.Error` chain.
  - `func proxy.Fingerprint(der []byte) string`, as
    `AB:CD:…` (32 pairs).
  - `settings.Site` gains `TLSPin string` (JSON `tls_pin`).
  - `func (s Site) Trust() proxy.SiteTrust`.
  - `type settings.CertInfo struct { Trusted bool; VerifyError, Issuer, Subject, Fingerprint string; Names []string; NotAfter time.Time }`
    (JSON snake case).
  - `func settings.InspectCertificate(ctx context.Context, rawURL string, caPEM []byte) (CertInfo, error)`
    - It dials once with verification off and reads the leaf, then
      verifies the chain with the system roots plus `caPEM` and the URL's
      host. `Trusted` is the result, and `VerifyError` says why not.
    - It sends no HTTP request and no credential, so it needs nothing
      but the URL.
    - The error covers only "nothing answers", with a 10 s dial
      deadline.

- [ ] **Step 1: Write the failing tests**

`internal/proxy/pin_test.go` uses an `httptest.NewTLSServer`, whose
certificate is not trusted by the system:
- `TestPinnedAcceptsMatchingLeaf`: the pin equals `Fingerprint(srv.Certificate().Raw)`,
  so GET answers 200.
- `TestPinnedRefusesOtherLeaf`: a pin of a different certificate makes
  GET fail. `errors.As(err, &*PinMismatchError)` holds and `Got` equals
  the server's fingerprint.
- `TestPinnedIgnoresNames`: the pin matches and `host` is `prism.example`
  (not in the SANs), so GET answers 200.
- `TestVerifiedRefusesUntrusted`: `Verify: true` with no CA and no pin
  makes GET fail with an x509 error, not a `PinMismatchError`.
- `TestFingerprintFormat`: a known DER gives 95 characters and the
  `^([0-9A-F]{2}:){31}[0-9A-F]{2}$` pattern.

`internal/settings/inspect_test.go`:
- `TestInspectUntrustedShowsFingerprint`: on the TLS test server,
  `Trusted == false`, `Fingerprint` matches, `Names` holds the server's
  SANs, `NotAfter` is set, and the server's handler counted 0 requests.
- `TestInspectTrustedWithCAPEM`: the server's certificate as `caPEM`
  makes `Trusted == true` and sets `Issuer`.
- `TestInspectNothingAnswers`: a closed port returns an error.

`internal/settings/sites_test.go`:
- `TestSiteTrustPinWins`: a site with a pin and `TLSVerify: false` gives
  a `Trust()` that carries the pin.
- `TestSecretFieldsUnchangedByPin`: the pin is not sealed, because it is
  public.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/proxy/ ./internal/settings/ -run 'Pinned|Verified|Fingerprint|Inspect|SiteTrust|ByPin'`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

Implement as in the Interfaces block. `siteTarget` in `serve.go` and the
checker and bootstrappers call `PrismTLSFor(host, site.Trust())`. The
`tls_verify is false` warning at start is logged once per site, which
also fixes the write-up 21 minor about the doubled warning.

- [ ] **Step 4: Run the suite, commit**

Run: `go test ./...` — Expected: PASS.

```bash
git add internal cmd
git commit -m "feat: a site's certificate is verified or pinned by the SHA-256 of its leaf"
```

---

### Task 3: The identity provider's id stored on the site

Spec 4.8 last bullet. It also fixes Disconnect's fallback, under which a
failed lookup counted a group as Portico's.

**Files:**
- Modify:
  - `internal/settings/sites.go` (`Site`) and `bootstrap_saml.go`;
  - `internal/tenant/service.go` (`Site`) and `provision.go` (`idp`,
    `foreignGroup`, removal at :372);
  - `cmd/portico/serve.go` (`cachedIdPLookup` goes).
- Test: `internal/settings/bootstrap_saml_test.go`,
  `internal/tenant/sites_test.go`, `cmd/portico/serve_test.go`

**Interfaces:**
- Produces:
  - `settings.Site` gains `SAMLIdPExtID string` (JSON
    `saml_idp_ext_id`), `ServiceAccountExtID string` and
    `APIKeyExtID string`. All three are written by the bootstraps; the
    removal of Task 8 reads the last two.
  - `tenant.Site.SAMLIdPExtID string`.
  - `func (s *Service) idp(ctx, site Site) (*nutanix.SAMLIdP, error)`:
    - with an extId, `GetSAMLIdP(extID)`;
    - without one, the name lookup as today (a legacy site, until the
      runtime of Task 4 backfills it).
  - `foreignGroup`:
    - with an extId known: the group is Portico's when its `idpId` is
      that extId;
    - with no extId and a failed lookup: the group is **kept**, and the
      step says "identity provider unknown: group kept".
  - `SiteBackend.IdPExtID` returns the stored id with no Prism Central
    call when it is set.

- [ ] **Step 1: Write the failing tests**

- `TestSAMLBootstrapStoresIdPExtID` (settings): after `SAML(...)`, the
  stored site has `SAMLIdPExtID` equal to the fake's id.
- `TestPrismBootstrapStoresAccountAndKeyIDs` (settings): the stored site
  has `ServiceAccountExtID` and `APIKeyExtID`.
- `TestDisconnectUsesStoredIdP` (tenant): the site has `SAMLIdPExtID:
  "idp-1"` and the fake's `ListSAMLIdPs` fails, so Disconnect still
  deletes the group whose `idpId` is `idp-1`.
- `TestDisconnectKeepsGroupWhenIdPUnknown` (tenant): no stored extId and
  a failed lookup, so the group is not deleted and the step message
  contains `group kept`.
- `TestSignInUsesStoredIdPWithoutListing` (serve): a site with
  `saml_idp_ext_id` makes the landing's SAML start without
  `GET …/saml-identity-providers` on the fake.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/settings/ ./internal/tenant/ ./cmd/portico/ -run 'StoresIdPExtID|AccountAndKeyIDs|StoredIdP|IdPUnknown|WithoutListing'`
Expected: FAIL.

- [ ] **Step 3: Implement**

Implement as in the Interfaces block. `prepare` passes the extId it
resolved down to the steps, so one provisioning run looks the provider
up at most once.

- [ ] **Step 4: Run the suite, commit**

Run: `go test ./...` — Expected: PASS.

```bash
git add internal cmd
git commit -m "feat: the site keeps its identity provider's id; disconnect keeps a group it cannot attribute"
```

---

### Task 4: Sites change without a restart

Spec 4.9.

**Files:**
- Create: `cmd/portico/sites_runtime.go`, `internal/proxy/backends.go`
- Modify: `cmd/portico/serve.go` (the loop at :223-248 moves into the
  runtime), `internal/proxy/sites.go` (`SiteRouting.Backends`),
  `internal/sites/monitor.go`, `internal/tenant/service.go`
- Test: `cmd/portico/sites_runtime_test.go`, `internal/proxy/backends_test.go`,
  `internal/sites/monitor_test.go`, `internal/tenant/service_test.go`

**Interfaces:**
- Produces:
  - `type proxy.BackendSet struct` with
    `NewBackendSet(map[string]*SiteBackend) *BackendSet`,
    `Get(id string) (*SiteBackend, bool)`, `All() []*SiteBackend` (sorted
    by order) and `Replace(map[string]*SiteBackend)`. It is safe for
    concurrent use (`atomic.Pointer` to an immutable map).
    `SiteRouting.Backends` becomes `*BackendSet`.
  - `type proxy.SwapHandler struct`, built by
    `NewSwapHandler(http.Handler) *SwapHandler`, with
    `Swap(http.Handler)` and `ServeHTTP`. The pass-through upstream sits
    behind it.
  - `func (m *Monitor) SetProbes(probes map[string]Prober)`: it keeps the
    failure counts of the ids it keeps, drops the others, and a new id
    starts at 0.
  - `func (s *tenant.Service) SetSites(sites []Site)`. The site map and
    order are guarded by an `RWMutex`, and every reader goes through
    `site(id)` and `orderedSites()`. An operation already running keeps
    the `Site` value it read.
  - `cmd/portico` gets `type siteRuntime struct`, with
    `newSiteRuntime(deps siteDeps) *siteRuntime` and
    `(r *siteRuntime) Apply(ctx context.Context, doc settings.Settings) error`.
    - `Apply` builds the parts of every site in `doc.ServedSites()` whose
      `siteFingerprint(settings.Site) string` changed: a hash over the
      URL, key, trust, pin, CA, IdP name and extId, defaults, code, name
      and order.
    - It keeps the unchanged parts, then swaps backends, probes, tenant
      sites and the pass-through in that order.
    - It calls `CloseIdleConnections` on the replaced transports.
  - A legacy site with `SAMLIdPName` but no `SAMLIdPExtID` is looked up
    once by name at `Apply`, and the id is stored with
    `settings.Service.Patch`. That patch triggers one more `Apply`, which
    finds nothing changed apart from the extId.
  - Wiring:
    - `settings.Service.OnChange` calls `Apply` (on this node, at once);
    - a 30 s ticker reads the document and calls `Apply` (the pair's
      other node, within 30 s);
    - both run only in running mode.

- [ ] **Step 1: Write the failing tests**

- `TestBackendSetReplaceConcurrent` (proxy): 50 goroutines `Get` while
  one goroutine `Replace`s, run with `-race`, and no nil backend for an
  id present in both maps.
- `TestInFlightRequestFinishesOnOldBackend` (proxy): the upstream
  handler blocks on a channel, `Replace` drops the site, the channel is
  released, and the first request answers 200.
- `TestSessionOnRemovedSiteGetsNotAvailable` (proxy): the session's site
  is absent from the set. A navigation gets 503 with the branded "not
  available" page; an XHR gets 403 `site-not-allowed`.
- `TestSetProbesKeepsFailures` (sites): site a has 2 failures,
  `SetProbes` with a and b, and a keeps 2 while b starts at 0. A dropped
  id reports `Unreachable` and is no longer probed.
- `TestSetSitesDuringOperation` (tenant): a gated `AddSite` runs on
  site-a while `SetSites` drops site-a. The operation finishes, and a
  new `AddSite` on site-a gets `tenant.ErrUnknownSite`.
- `TestRuntimeApplyAddsSiteWithoutRestart` (cmd/portico): serve with one
  site, then `settings.Service.Update` adds a complete second site on a
  second fake Prism Central. Within 1 s the site page lists it and
  choosing it reaches the second fake.
- `TestRuntimeApplyRebuildsOnlyChanged` (cmd/portico): renaming site-b
  keeps site-a's `*SiteBackend` pointer, and site-b's is new.
- `TestRuntimeBackfillsIdPExtID` (cmd/portico): a stored site without an
  extId gets it in the document after `Apply`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -race ./internal/proxy/ ./internal/sites/ ./internal/tenant/ ./cmd/portico/ -run 'BackendSet|InFlight|RemovedSite|SetProbes|SetSites|RuntimeApply|Backfills'`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

Implement as in the Interfaces block.
- `serve.go` builds the first state with the same `Apply`, so start-up
  and changes share one path.
- `api.SettingsOptions.Apply` (restart) stays for Keycloak, public names
  and listeners. The site API's texts "take effect after Apply" go.

- [ ] **Step 4: Run the suite with the race detector, commit**

Run: `go test -race ./...` — Expected: PASS.

```bash
git add internal cmd
git commit -m "feat: sites are added, changed and removed without a restart"
```

---

### Task 5: Status from Prism Central; the incomplete state

Spec 4.2, plus the "certificate changed" state of 4.3.

**Files:**
- Modify: `internal/sites/monitor.go`, `internal/proxy/sites.go`
  (`unavailable`), `internal/branding/templates/sites.html`,
  `internal/tenant/sites.go` (or `service.go`, `AddSite`),
  `cmd/portico/sites_runtime.go` (probes), `internal/api/sites.go`
- Create: `internal/settings/status.go`
- Test: `internal/sites/monitor_test.go`, `internal/proxy/sites_test.go`,
  `internal/settings/status_test.go`, `internal/tenant/sites_test.go`,
  `internal/api/sites_test.go`

**Interfaces:**
- Produces:
  - `sites.Incomplete Status = "incomplete"`.
  - Two error types that probes return:
    - `type sites.IncompleteError struct { Reason string; Immediate bool }`;
    - `type sites.CertChangedError struct { Fingerprint string }`.
    The runtime's probe maps `*proxy.PinMismatchError` to the second one.
  - `type sites.Detail struct { Status Status; Reason, NewFingerprint string; CheckedAt time.Time }`
    and `func (m *Monitor) Detail(id string) Detail`. `Status(id)` stays.
  - Status rules, in this order:
    1. maintenance;
    2. a `CertChangedError` makes the site `Unreachable` with the reason
       `certificate changed`, at once;
    3. an `IncompleteError{Immediate: true}` (trust or Keycloak client
       missing) makes it `Incomplete` at once;
    4. three consecutive `IncompleteError{Immediate: false}` (key refused
       with 401 or 403) make it `Incomplete`;
    5. three consecutive other failures make it `Unreachable`;
    6. otherwise `Available`.
    Any success clears the counters.
  - `func (m *Monitor) SetTrustChecks(checks map[string]Prober)`. These
    run every `sites.TrustInterval = 5 * time.Minute` inside `Run`, and
    on `Report`.
  - `func (m *Monitor) Report(id string, err error)` records the outcome
    of an on-demand check.
  - `func settings.SiteStatus(ctx, kc *keycloak.Client, s Site) StatusReport`
    makes live calls:
    - **connection:** reachable, version, trust mode
      (`verified|pinned|unverified`), the fingerprint and `NotAfter`;
    - **service account:** whether the key authenticates;
    - **SAML:** the IdP with `SAMLIdPExtID` exists and its `entityIssuer`
      is this site's, and the Keycloak client `SAMLEntityID` exists;
    - **defaults:** the cluster and the container exist.
    Each part is `{ok bool, message string, action string}`.
  - `GET /api/v1/sites/{id}/status` runs `SiteStatus` and `Report`s the
    outcome to the monitor. It returns
    `{status, reason, new_fingerprint, connection, service_account, saml, defaults}`.
  - `tenant.ErrSiteIncomplete`: `AddSite` and `Connect` refuse an
    incomplete site through the gate set by
    `func (s *Service) SetSiteGate(func(siteID string) error)`. The API
    maps it to 409 with "finish the setup of <code> first".
  - Proxy: an incomplete site's tile is greyed. Choosing it is refused
    (`site-incomplete`), and a single connection to it gets the same
    branded "not available" page as unreachable.

- [ ] **Step 1: Write the failing tests**

`internal/sites/monitor_test.go`:
- `TestOneKeyRefusalIsNotIncomplete`: one refusal, then a success, keeps
  the site `Available`.
- `TestThreeKeyRefusalsMakeIncomplete`: three refusals in a row make it
  `Incomplete` with the reason.
- `TestTrustMissingIsIncompleteAtOnce`: a trust check returning an
  `Immediate` error makes the site `Incomplete` after one round.
- `TestCertChangedAtOnce`: one `CertChangedError` makes it
  `Unreachable`, with `Detail().Reason == "certificate changed"` and the
  new fingerprint.
- `TestMaintenanceWinsOverIncomplete`.

`internal/proxy/sites_test.go`:
- `TestIncompleteSiteTileGreyAndRefused`: the tile is disabled and
  POST select returns 403 `site-incomplete`.
- `TestOnlyIncompleteSiteNotAvailablePage`: the page shows the branded
  "not available" text, not "responding slowly".

`internal/settings/status_test.go` (settingstest fakes):
- `TestStatusTrustMissing`: the fake has no IdP with the stored extId,
  so `saml.ok == false` and the action is `Repair`.
- `TestStatusOtherEntityIssuer`: the IdP exists with another site's
  issuer, so `saml.ok == false`.
- `TestStatusDefaultsGone`: the stored container is absent, so
  `defaults.ok == false` while the site's status is still not
  `Incomplete` (a warning only).
- `TestStatusNeverFromRow`: the stored `SAMLIdPName` is set and the fake
  has no IdP, so the status is not ok. This pins the anti-goal.

`internal/tenant/sites_test.go`:
- `TestConnectRefusedOnIncompleteSite`: the gate returns an error for
  site-b, so `AddSite(acme, site-b)` returns `ErrSiteIncomplete` and no
  placement row is created.

`internal/api/sites_test.go`:
- `TestSiteStatusEndpointReportsToMonitor`: GET status with the trust
  missing returns `status: "incomplete"`, and the monitor's `Detail`
  agrees.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/sites/ ./internal/proxy/ ./internal/settings/ ./internal/tenant/ ./internal/api/ -run 'KeyRefusal|TrustMissing|CertChanged|MaintenanceWins|Incomplete|Status'`
Expected: FAIL.

- [ ] **Step 3: Implement**

Implement as in the Interfaces block. The runtime of Task 4 builds both
checks for each site:
- the probe: `PrismCentralVersion` with the key, where 401 or 403 is a
  non-immediate `IncompleteError`;
- the trust check: `GetSAMLIdP(extID)` and the issuer, plus Keycloak
  `FindClient(entityID)`.

- [ ] **Step 4: Run the suite, commit**

Run: `go test ./...` — Expected: PASS.

```bash
git add internal cmd
git commit -m "feat: a site's status comes from Prism Central and Keycloak; incomplete sites take no new entries"
```

---

### Task 6: The setup run

Spec 4.1, from "On submit" to the summary.

**Files:**
- Create: `internal/settings/setup_site.go`
- Modify:
  - `internal/settings/bootstrap_prism.go` and `bootstrap_saml.go`: their
    bodies become step functions that `SetupSite` calls, and the old
    entry points call the same functions;
  - `internal/nutanix/bootstrap.go`: the rights read;
  - `internal/api/sites.go`.
- Test: `internal/settings/setup_site_test.go`, `internal/api/sites_test.go`

**Interfaces:**
- Produces:
  - `type settings.SetupInput struct { Username, Password, ServiceAccount, Pin string }`.
    `Pin` is the fingerprint the MSP confirmed on this page; it is
    required when the stored site is not verified by roots.
  - `type settings.StepLine struct { Name, Status, Message string }`
    (JSON snake case). `Status` is `ok`, `failed`, `skipped` or `kept`.
  - `type settings.SetupResult struct { Site Site; Steps []StepLine; Complete bool; IdPChanged bool }`.
  - `func (b *Bootstrapper) SetupSite(ctx context.Context, keycloakURL, siteID string, in SetupInput) (SetupResult, error)`.
    It has one deadline of `SetupDeadline = 2 * time.Minute`, and the
    error covers only invalid input or an unknown site; a failed step is
    a result. The steps run in this order, and a failure stops the run:
    1. `certificate`: `InspectCertificate`. Either `Trusted`, or the
       site's stored pin or `in.Pin` equals the fingerprint, in which
       case the pin is stored. Otherwise the step fails with "the
       certificate is not confirmed", and no credentialed request has
       been made.
    2. `version`: `ListClusters` with the credentials; pc.7.6 or later,
       otherwise "Prism Central <v> is older than pc.7.6".
    3. `rights`: best effort (write-up 24 item 2). `FindUser(username)`,
       its policies and roles; look for `Create_User`, `Update_Role`,
       `Create_Saml_Identity_Provider` and the role creation operation.
       Unreadable gives `skipped` with "could not read the account's
       rights; the next steps will tell". A missing right fails the step
       and names it.
    4. `identity-provider`: `ListSAMLIdPs` is read-only. Another
       provider fails the run, as today. Portico's own (by
       `SAMLIdPExtID`, or by name with this site's `entityIssuer`) is
       noted for reuse.
    5. `service-account`: create or reuse, issue the key, join the
       policy, as `bootstrap_prism.go` does. Store the key and both
       extIds.
    6. `saml-trust`: create or reuse the IdP, then the Keycloak client
       and mappers, as `bootstrap_saml.go` does. Store the name, extId
       and entity id. `IdPChanged` is true when a stored extId was
       replaced.
    7. `defaults`: `PickDefaults` over the lists. It fills only empty or
       vanished fields and keeps a choice the MSP made.
    8. `live`: the patch has run, so the runtime's `OnChange` serves the
       site. The step is `ok` when `Site.Complete()`.
  - The credentials live in the `nutanix.Client` built for this call
    and in nothing else. On return the result is all that is left.
  - API:
    - `POST /api/v1/sites/inspect`, with
      `{"url": "...", "ca_pem": "..."}`, returns `CertInfo`. Before
      anything is stored it needs only an admin session.
    - `POST /api/v1/sites` with
      `{code, name, order, url, ca_pem, tls_pin, admin: {username, password}, service_account}`
      creates the row and, when `admin` is given, runs `SetupSite`. It
      answers 201 with `{site, steps, complete}`.
      - Without `admin`, the row is created incomplete (the "Advanced"
        manual path).
      - It refuses `tls_verify: false` without a pin (400, "a new site
        needs a trusted or pinned certificate").
    - `POST /api/v1/sites/{id}/setup`, with
      `{username, password, service_account, pin}`, runs `SetupSite`
      again (Finish setup, Repair). It answers 200 with the same shape.
    - `POST /api/v1/sites/{id}/pin`, with `{"fingerprint": "..."}`,
      inspects the current certificate. It stores the pin when the
      fingerprint equals the one shown (the MSP confirmed what is
      presented now) and answers 409 when it differs.
    - `PUT /api/v1/sites/{id}` refuses turning `tls_verify` from true
      to false.
  - `bootstrap/prism-central` and `bootstrap/saml` stay as the
    "Advanced" paths, on the same step functions.

- [ ] **Step 1: Write the failing tests**

`internal/settings/setup_site_test.go` uses the settingstest fakes, a
TLS fake, and a counter of `Authorization` headers:
- `TestSetupUnconfirmedCertificateSendsNoCredentials`: an untrusted
  certificate with a wrong `in.Pin` fails the `certificate` step, and
  the fake counted 0 credentialed requests (Review Focus 1).
- `TestSetupPinnedRunsAllSteps`: the right pin runs eight steps, all
  `ok` (or `skipped` for rights). `Complete` is true, the stored site
  has a key, `SAMLIdPExtID`, `TLSPin`, cluster and container, and no
  stored field holds the password.
- `TestSetupOlderVersionStops`: version `pc.7.5` fails the `version`
  step, and the fake saw no user or IdP created.
- `TestSetupForeignIdPStopsBeforeCreating`: the fake has the provider
  `other-idp`, so the `identity-provider` step fails and the fake has no
  new user and no key (Review Focus 4, second half).
- `TestSetupKeycloakRefusedLeavesIncomplete`: Keycloak answers 403 to
  the client creation. `saml-trust` fails, `Complete` is false, the site
  is absent from `ServedSites()`, and the IdP exists in the fake.
- `TestFinishSetupReusesOwnIdP`: following the previous test, Keycloak
  is healthy again. `SetupSite` succeeds, the IdP is not reported as
  "another provider", the fake has one IdP, and `IdPChanged == false`
  (Review Focus 4).
- `TestSetupKeepsChosenDefaults`: a stored cluster and container that
  still exist are unchanged after the run.
- `TestSetupRightsMissingNamed`: the fake's roles lack
  `Create_Saml_Identity_Provider`. `rights` fails with that name and
  nothing is created.
- `TestSetupRightsUnreadableSkips`: a 403 on the role read makes
  `rights` `skipped` and the run goes on.

`internal/api/sites_test.go`:
- `TestCreateSiteRefusesVerifyOff`: POST `tls_verify: false` without a
  pin returns 400.
- `TestCreateSiteWithAdminRunsSetup`: 201 with `complete: true` and
  eight steps.
- `TestPinEndpointRefusesChangedFingerprint`: the body carries an old
  fingerprint, so the answer is 409 and the pin is unchanged.
- `TestUpdateRefusesTurningVerifyOff`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/settings/ ./internal/api/ -run 'Setup|FinishSetup|VerifyOff|PinEndpoint|WithAdminRunsSetup'`
Expected: FAIL.

- [ ] **Step 3: Implement**

Implement as in the Interfaces block. If the role creation operation's
name is not in `kb/`, read it on MI1 with the lab admin (read-only) and
record it in Task 11's write-up.

- [ ] **Step 4: Run the suite, commit**

Run: `go test ./...` — Expected: PASS.

```bash
git add internal
git commit -m "feat: add a site in one run: certificate, version, rights, service account, SAML trust, defaults"
```

---

### Task 7: Repair on a site in use

Spec 4.1, "Repair on a site in use".

**Files:**
- Modify: `internal/tenant/provision.go` (the `saml-group`,
  `idp-share` and `role-membership` closures of `provisionPlacement`
  become named methods), `internal/tenant/service.go`,
  `internal/api/sites.go`
- Test: `internal/tenant/sites_test.go`, `internal/api/sites_test.go`

**Interfaces:**
- Produces:
  - `func (s *Service) samlGroupStep(ctx, t *store.Tenant, p *store.Placement, idp *nutanix.SAMLIdP) error`,
    `idpShareStep(...)` and `roleMembershipStep(...)`. They are used by
    `provisionPlacement` exactly as today.
  - `type tenant.RepairLine struct { Tenant string; OK bool; Message string }`.
  - `func (s *Service) RepairSite(ctx context.Context, siteID string) ([]RepairLine, error)`:
    - for every placement on the site that is active or suspended, in
      tenant order;
    - run the three steps against the site's current IdP. A SAML group
      bound to the old IdP is replaced: the new one is created and the
      placement's `SAMLGroupExtID` updated, and the old one is deleted
      only when its `idpId` is the stored old extId;
    - the status stays as it was, so a suspended placement stays
      suspended.
  - `SetupSite`'s caller in the API (`sites/{id}/setup`) calls
    `RepairSite` when `IdPChanged`. It appends one `StepLine` per tenant
    (name `tenant <slug>`), and a failed tenant's line carries
    "Repair again" through the same endpoint.

- [ ] **Step 1: Write the failing tests**

- `TestRepairSiteRebindsGroups`: acme (active) and beta (suspended) are
  on site-a, the fake's IdP is recreated with the new extId `idp-2`,
  then `RepairSite`. Both placements have groups with `idpId == idp-2`,
  the IdP is shared, there is one role membership each, acme is still
  active and beta still suspended.
- `TestRepairSiteOneTenantFails`: the fake fails the membership for
  beta, so the lines read `acme ok` and `beta` with the reason. acme's
  changes are kept.
- `TestProvisionUnchangedAfterExtract`: the existing provisioning tests
  pass unchanged. This is a run of the existing suite, not a new test.
- `TestSetupEndpointRepairsWhenIdPChanged` (api): `IdPChanged` makes the
  response hold a step `tenant acme`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/tenant/ ./internal/api/ -run 'Repair'`
Expected: FAIL.

- [ ] **Step 3: Implement, run the suite, commit**

Run: `go test ./...` — Expected: PASS.

```bash
git add internal
git commit -m "feat(tenant): repair re-binds every connected tenant to a recreated SAML trust"
```

---

### Task 8: Removing a site

Spec 4.1, "Removing a site".

**Files:**
- Modify: `internal/idp/keycloak/admin.go`, `internal/nutanix/iam.go`,
  `internal/api/sites.go` (`DeleteSite`), `internal/sites/monitor.go`
- Test: `internal/idp/keycloak/admin_test.go`, `internal/nutanix/client_test.go`,
  `internal/api/sites_test.go`

**Interfaces:**
- Produces:
  - `func (c *keycloak.Client) DeleteClient(ctx context.Context, clientID string) error`:
    find by `clientId`, `DELETE /admin/realms/<realm>/clients/<uuid>`.
    An absent client is not an error.
  - `func (c *nutanix.Client) DeleteSAMLIdP(ctx context.Context, extID string) error`:
    `DELETE /api/iam/v4.0/authn/saml-identity-providers/{extId}` with
    the ETag, as the other v4 deletes. A 404 is not an error.
  - `type api.RemoveResult struct { Removed bool; Deleted, LeftInPrismCentral []string }`
    (JSON snake case).
  - `DELETE /api/v1/sites/{id}`, with the optional body
    `{username, password}`:
    - refused with 409 while a tenant is connected (active, suspended,
      creating, removing or remove-failed), or while tenant operations
      run (`SettingsOptions.Busy() > 0`, the backlog minor; the count is
      global, not per site);
    - with credentials: once the certificate check (as step 1 of
      `SetupSite`) has passed, delete the IdP and the stored key
      (`APIKeyExtID`) in Prism Central;
    - in every case: the Keycloak client `SAMLEntityID`, the site's
      maintenance flag (backlog minor) and the row.
    - It answers 200 with the `RemoveResult`. Without credentials,
      `left_in_prism_central` names the IdP, the service account and its
      key.
  - `func (m *Monitor) ClearMaintenance(ctx context.Context, id string) error`.
  - "Forget" stays as it is, and also deletes the Keycloak client.

- [ ] **Step 1: Write the failing tests**

- `TestDeleteClient` (keycloak): delete an existing client (one DELETE)
  and an absent one (no error, no DELETE).
- `TestDeleteSAMLIdPSendsIfMatch` (nutanix).
- `TestRemoveSiteWithoutCredentials` (api): the Keycloak fake has no
  client afterwards, the Prism Central fake still has the IdP, and
  `left_in_prism_central` lists three things.
- `TestRemoveSiteWithCredentials` (api): the fake loses the IdP and key
  `APIKeyExtID`, the service account remains, and the maintenance flag
  is gone from `site_state`.
- `TestRemoveSiteRefusedWithTenant` (api): with a suspended placement it
  answers 409.
- `TestForgetDeletesKeycloakClient` (api).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/idp/keycloak/ ./internal/nutanix/ ./internal/api/ -run 'DeleteClient|DeleteSAMLIdP|RemoveSite|ForgetDeletes'`
Expected: FAIL.

- [ ] **Step 3: Implement, run the suite, commit**

Run: `go test ./...` — Expected: PASS.

```bash
git add internal
git commit -m "feat: removing a site deletes Portico's Keycloak client and, with credentials, its Prism Central objects"
```

---

### Task 9: The admin pages

Spec 4.1, 4.2 and 4.3, on the page.

**Files:**
- Create: `internal/web/templates/site_new.html`, `internal/web/site_setup.go`
- Modify: `internal/web/sites.go`, `internal/web/templates/site.html`,
  `internal/web/templates/sites.html` (the list), `internal/web/setup.go`
  (notices)
- Test: `internal/web/sites_test.go`, `internal/web/site_setup_test.go`

**Interfaces:**
- Consumes: the API shapes of Tasks 5, 6, 7 and 8, through the same
  `SettingsOptions` methods the API calls (ADR 0003: the page calls what
  the API calls).
- Produces these routes:
  - `GET /setup/sites/new`: step 1, with code, name, order, address and
    an "Advanced" section holding the CA PEM, the service account name
    and the manual path.
  - `POST /setup/sites/new/inspect`, an HTMX fragment `site_cert`:
    - trusted: the issuer;
    - not trusted: the fingerprint, subject, names and expiry, the
      comparison text of write-up 24 item 5, and the checkbox "I
      compared it: this is my Prism Central's certificate" with a hidden
      `pin` field;
    - nothing answers: the reason.
    The step 2 fields (username and password, `autocomplete="off"`)
    appear only in a trusted fragment or once the checkbox is ticked
    (HTMX `hx-trigger="change"` on it).
  - `POST /setup/sites/new`: runs the create plus setup, then renders the
    summary `site_summary`, one line per step, with the site's link. The
    form carries `hx-indicator`, reading "Setting up the site, this takes
    up to two minutes".
  - The site page, `GET /setup/sites/{id}`:
    - a status card filled from `GET …/status` (HTMX on load), with four
      rows: connection, service account, SAML trust, defaults. Each row
      shows ok or the message and its action;
    - an incomplete site shows the **Finish setup** form (credentials);
    - a broken trust shows the **Repair** form;
    - `certificate changed` shows the new fingerprint and **Confirm the
      new certificate**;
    - a legacy verify-off site shows a warning and **Pin the current
      certificate**.
  - `POST /setup/sites/{id}/setup` (Finish setup and Repair, summary
    fragment) and `POST /setup/sites/{id}/pin`.
  - `POST /setup/sites/{id}/delete` gains the optional credentials and
    shows the `RemoveResult`.
- The TLS checkbox and the pasted API key leave the main form. The key
  and the SAML provider name sit under "Advanced". No page says "Apply"
  about a site.

- [ ] **Step 1: Write the failing tests**

- `TestNewSiteStep1HasNoPasswordField`: GET new has no
  `name="password"`.
- `TestInspectUntrustedShowsFingerprintAndCheckbox`: the fragment
  contains the fingerprint, `Certificate is not valid` (the comparison
  text) and the checkbox.
- `TestInspectTrustedShowsCredentials`: a trusted fragment contains
  `name="password"`.
- `TestNewSiteRunShowsSummary`: POST with the fakes shows eight lines
  and the site link, and the body never contains the password value.
- `TestSitePageStatusCardFromPrismCentral`: the trust is missing in the
  fake, so the card's SAML row reads `missing` and shows the Repair
  form. This pins the anti-goal that the status comes from Prism
  Central.
- `TestSitePageNoVerifyOffControl`: no `name="tls_verify"` anywhere on
  the site pages.
- `TestLegacyVerifyOffShowsPinAction`.
- `TestCertificateChangedShowsConfirm`.
- `TestRemoveWithoutCredentialsListsLeftovers`.
- `TestNoApplyNoticeForSites`: after saving a site, the notice does not
  contain `Apply`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/web/ -run 'NewSite|Inspect|SitePage|VerifyOff|CertificateChanged|RemoveWithout|NoApplyNotice'`
Expected: FAIL.

- [ ] **Step 3: Implement**

Use the texts of the spec and the existing page style. Passwords are
never echoed back into a re-rendered form.

- [ ] **Step 4: Run the suite, commit**

Run: `go test ./...` — Expected: PASS. Layout and wording in a browser
are looked at on the appliance in Task 11, with Marco signed in.

```bash
git add internal
git commit -m "feat(web): add a site on one page; status, finish setup, repair, pin and remove on the site page"
```

---

### Task 10: The documents

**Files:**
- Modify:
  - `docs/superpowers/specs/2026-10-17-portico-design.md` (4.5);
  - `docs/superpowers/specs/2026-10-20-sites-design.md` (4.1, 4.6);
  - `docs/adr/0006-sites-at-runtime-and-connections.md` (Consequences:
    how fast the pair picks a change up; what pinning means at renewal);
  - `docs/api.md`, `docs/operating.md` and `docs/backlog.md`.

- [ ] **Step 1: Write**

- **Main design 4.5:** per site, the one-page flow is the main path and
  the pre-created path sits under "Advanced".
- **Sites design 4.1:**
  - the states **incomplete** and **certificate changed**;
  - no Apply for site changes;
  - removal deletes the Keycloak client, and with credentials the Prism
    Central objects.
- **Sites design 4.6:** "adding or editing a site uses the existing
  Apply" goes.
- **`docs/api.md`:** the new routes and shapes, checked against the code.
- **`docs/operating.md`:**
  - a renewed Prism Central certificate stops a pinned site until it is
    confirmed;
  - a change reaches the pair's other node within 30 s.
- **Backlog:**
  - remove the minors this plan fixed: maintenance flags of removed
    sites, no busy check on removal, the doubled `tls_verify` warning;
  - add what is left, if the measurement of Task 6 finds one: the role
    of the service account and the floating IP read (write-up 25).

- [ ] **Step 2: Check and commit**

Run: `grep -rn 'after Apply\|then apply' internal/web internal/api docs/operating.md docs/api.md`
Expected: no line about sites.

```bash
git add docs
git commit -m "docs: one-step sites, status from Prism Central, pinning and runtime changes in the designs"
```

---

### Task 11: On the appliance (with Marco), write-up 26

This is the spec's success scenario.

**Files:**
- Create: `docs/lab/2026-10-2x-26-one-step-sites.md`
- Modify: `docs/lab/README.md`, `docs/lab/lab-inventory.md`

- [ ] **Step 1: Prerequisites from Marco**

- NYC1's administrator unlocked (write-up 25).
- His OK for the upgrade, given after a warning.

Then run the appliance environment of write-up 23, followed by
`lab/portico/upgrade-appliance.sh`.
Expected: the new `dev-<commit>` running, and the backup dir printed.

- [ ] **Step 2: In the browser, Marco signed in, his OK for each write**

1. **The migration.** NYC1's page (stored verify-off) shows the warning.
   Marco compares the fingerprint with Chrome's viewer and pins it. The
   status card shows four rows from Prism Central.
2. **Removal.** acme is disconnected from NYC1 after its project is
   emptied (the write-up 25 steps). Then NYC1 is removed with Marco's
   administrator credentials, which he types. The IdP and the key are
   gone in Prism Central, and the Keycloak client is gone.
3. **The success scenario.**
   - Add a site: code `AZ-US-NYC1`, address, fingerprint confirmed,
     credentials typed by Marco. Record the summary and the elapsed time.
   - Connect acme from the site's page.
   - A user of acme signs in, switches between both sites, signs out of
     NYC1's console and lands on the site page.
   - No Apply and no restart happen (`systemctl show portico -p
     ActiveEnterTimestamp` is unchanged).
4. **Status.** The status card shows four ok rows.
5. **Repair (optional, Marco's call).** It needs the trust deleted in
   Prism Central by hand, then Repair, after which acme's user signs in
   again.
6. **A NOT MEASURED line.** A renewed certificate is measured only if
   Marco renews one. Otherwise it is covered by unit tests.

- [ ] **Step 3: Write write-up 26**

Use the template: Measured on, Method, Result with MEASURED / NOT
MEASURED lines, and how to re-measure. Include the measurements of
Tasks 1 and 6 (the free space field and the role creation operation).
Add README row 26 and update the inventory. Then commit:

```bash
git add docs
git commit -m "docs(lab): write-up 26, one-step sites on the appliance"
```

---

## After this plan

- PR "Sites usability, part C" with Marco's OK.
- Then the Milestone 5 backlog sweep (`docs/backlog.md`), which leads to
  M6 and `v0.1.0`.
