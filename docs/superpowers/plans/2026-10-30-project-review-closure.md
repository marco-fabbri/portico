# Project review closure (write-up 36) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close what the re-read of 2026-10-30 found: the documents stuck at 0.1.0, the eight Important code findings P1–P8 each with a test that fails first, the real addresses and the word "lab" in code, and the backlog.

**Architecture:** Six pull requests from `main`, one per topic, in this order: documents; four small fixes (P2, P1, P4, P7); the ownership rule (P5, P6, P8); the exposure switch (P3); the names in code; the backlog and the 0.2.2 notes. Each code fix is one function and one test; nothing moves between packages (the `settings`/`api` split is its own later plan).

**Tech Stack:** Go 1.27, `net/http`, `regexp`, the existing fakes of Prism Central (`internal/nutanix` `fakePC`, `internal/tenant` `e.pc` with `FailOnce`), `go test ./...`, `lab/test_scripts.sh`.

**Spec:** `docs/lab/2026-10-30-36-project-review.md` (the findings, with file:line), the security rules in `CLAUDE.md`, design 4.3 of `docs/superpowers/specs/2026-10-29-tenant-network-design.md` ("stored by extId, not found again by name").

## Global Constraints

- English everywhere on disk; the conversation with Marco is in Italian.
- Commits: `git -c user.email=9093189+marco-fabbri@users.noreply.github.com commit`, the message ending with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`; PR body ending with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- Each PR is merged only with `gh pr merge N --rebase --delete-branch` after Marco's OK; never a local merge.
- Never log Authorization, cookies, tokens, passwords, session ids or CSRF; status codes only.
- The public repo carries no real address of the lab: documentation blocks `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24` (RFC 5737) and the ERP example `100.96.0.0/24` where a private block is the point.
- No `A && B || C` in shell; BSD `sed` has no `\b`; the lab scripts stay as they are.
- The word "lab" never reaches a UI string. Site codes `AZ-IT-MI1`/`AZ-US-NYC1` are product examples and stay.

## Review Focus

1. A raw path with `%2F` inside the login-request id (`login-requests/a%2Fb/$actions/validate-credentials`): refused 403 before the session check (Task 3).
2. `Authorization: <token>` with no scheme on the admin listener: the access log holds `auth_scheme":"other"` and not one byte of the token (Task 2).
3. A task poll that answers 500 once, then SUCCEEDED: one create call, not two (Task 4).
4. `GET /api/v1/settings` then `PUT` of that body on an appliance with a custom pool: the pool is unchanged (Task 5).
5. Retry network after the tenant deleted `-net` and made its own subnet of that name in its project: Portico creates a new `-net` and never stores the tenant's extId (Task 7).

---

### Task 1: Documents at the current release (PR `docs/current-release`)

**Files:**
- Modify: `README.md` (lines 8, 19–35, 163–172, 183–195, 226–249, 251–267, 269–275)
- Modify: `docs/install.md` (lines 1–6, 185–191, 267–274, 290–293)
- Modify: `SECURITY.md` (lines 3–6)
- Modify: `docs/superpowers/specs/2026-10-17-portico-design.md` (goal 2 at line 22, the milestones table at the end, the "Step 2" line, the order of 4.8–4.10)
- Modify: `docs/backlog.md` (lines 1–10)

- [ ] **Step 1: README.** Badge `release-v0.2.1` linking to the v0.2.1 tag. The callout becomes "**Current release, 0.2.1.** It is for trying Portico with real tenants on your own Prism Central…" with the known-limits link to `docs/releases/v0.2.0.md#known-limits` (0.2.1's notes say its limits are 0.2.0's). "Install" and "Documentation" link `docs/releases/v0.2.1.md`. Add a milestone row `| 7 Tenant ready network, 2026-10-30 | A VPC, a /24 subnet, outbound access and floating IPs per tenant and site on a routed /28 of the site's transit VPC (NVD-2219), built in seven recorded steps and removed at Disconnect; measured on two appliances, the second with a router VM in front of the transit VPC (write-ups 34 and 35); 0.2.1 moves the default pool clear of Prism Central's reserved subnets (ADR 0009) |`. Roadmap: "0.2.0 added the tenant ready network, 0.2.1 its default pool." Repository layout: add `sites/` (sites, probes, trust), `exposure/` (certificate and tunnel switch), `netready/` and `netplan/` (readiness checks, address plan), `console/`, `config/`, `textedit/`, `image/` (appliance build), `docs/history/`; the `cmd/portico` line becomes `serve | settings import|show | admin set-password | console | exposure apply | version`. Decisions line: "nine decisions, from the pc.7.6 baseline to the tenant pool".
- [ ] **Step 2: install guide, version-neutral.** Line 3: "This guide installs the current release of Portico. What each release contains, what it was measured on and its known limits are in its [release notes](releases/): read the limits before you put the appliance in front of customers." Step 1 of the image: "Download `portico-<version>.qcow2` and its `.sha256` from the [latest release](https://github.com/marco-fabbri/portico/releases/latest)…". The host install block uses `<version>` in the four URLs and `--release <version>`, with one line above: "with `<version>` the release you install, `0.2.1` at the time of writing". "in 0.1.0" (Updating) becomes "today".
- [ ] **Step 3: SECURITY.md.** Replace lines 3–6 with: "Portico is released (see the [releases](https://github.com/marco-fabbri/portico/releases)) and still before 1.0: the current release is the supported one, and a fix ships as the next patch release. Security matters from the first commit, because the whole point of the appliance is what it refuses to let through."
- [ ] **Step 4: main design.** Goal 2: "one or more Prism Centrals reachable at one public URL (sites, `2026-10-20-sites-design.md`), either through…". Milestone 4 row: prefix "done 2026-10-23 (write-ups 21, 23 to 25): ". Add row `| 7 | Tenant ready network | done 2026-10-30 (write-ups 34, 35): design 2026-10-29-tenant-network-design.md, ADRs 0008 and 0009; released as 0.2.0 and 0.2.1 |`. "Step 2 (later)" line: drop "multi-Prism Central". Move section 4.10 after 4.9 (cut and paste the block, no text change).
- [ ] **Step 5: backlog header.** Lines 8–10: "The known limits of each release (`docs/releases/`) are drawn from this file."
- [ ] **Step 6: verify.** Run: `grep -rn '0\.1\.0' README.md docs/install.md SECURITY.md | grep -v 'write-up\|v0\.1\.0-rc\|releases/v0.1.0.md)' ` Expected: only the milestone-6 row and the history lines that name 0.1.0 as a past event. Run: `lab/test_scripts.sh` Expected: `PASS`.
- [ ] **Step 7: commit and PR.** `docs: README, install guide and SECURITY at the current release`. Open the PR; report its link.

### Task 2: P2, a scheme-less Authorization never reaches the log (PR `fix/review-36-small`)

**Files:**
- Modify: `internal/proxy/accesslog.go:107-115`
- Test: `internal/proxy/public_test.go:261`

- [ ] **Step 1: failing test.** In `TestAccessLogRecordsAuthSchemeOnly` add the cases `{"eyJhbGciOiJIUzI1NiJ9.e30.abc", "other", "eyJhbGciOiJIUzI1NiJ9"}` and `{"Token abc", "other", "abc"}` (the second asserts an unknown scheme is not echoed either).
- [ ] **Step 2: run.** `go test ./internal/proxy -run TestAccessLogRecordsAuthSchemeOnly` Expected: FAIL, "log lacks auth_scheme "other"" and "log contains the value".
- [ ] **Step 3: implement.** `authScheme` returns the lower-cased first field only when it is one of `basic`, `bearer`, `digest`, `negotiate`, `ntlm`; any other non-empty value gives `"other"`; no header gives `"none"`. Update the function comment.
- [ ] **Step 4: run.** Same command. Expected: PASS. Then `go test ./internal/proxy` Expected: ok.
- [ ] **Step 5: commit.** `fix(proxy): the access log names only a known Authorization scheme`.

### Task 3: P1, the policy refuses a raw path it cannot vouch for (same PR)

**Files:**
- Modify: `internal/proxy/policy.go` (`Deny`, before the `NormalizePath` switch)
- Test: `internal/proxy/policy_test.go:39` and `internal/proxy/public_test.go`

**Interfaces:**
- Produces: `func rawPathSuspect(r *http.Request) bool` in `policy.go`: true when `r.URL.RawPath` (or `r.URL.EscapedPath()`) contains, case-insensitively, `%2f`, `%5c`, `%00`, `%2e`, `%25`, or any byte below 0x21 or equal to 0x7f.

- [ ] **Step 1: failing test.** In `TestPrismPolicyDeny` add cases built with `httptest.NewRequest` on the raw target (the `req` helper must set `RequestURI`/`URL.RawPath` from the string; if it parses with `url.Parse`, use `httptest.NewRequest(method, "https://prism.test"+target, nil)` for these): `{"validate encoded slash in id", POST ".../login-requests/a%2Fb/$actions/validate-credentials", "deny-encoded-path", true}`, `{"validate encoded dot-dot", POST vc+"%2F..", "deny-encoded-path", true}`, `{"validate trailing nul", POST vc+"%00", "deny-encoded-path", true}`, `{"validate trailing space", POST vc+"%20", "deny-encoded-path", true}`, `{"percent itself", GET "/console/%25", "deny-encoded-path", true}`, and a negative `{"plain console path", GET "/console/app.js", "", false}`.
- [ ] **Step 2: run.** `go test ./internal/proxy -run TestPrismPolicyDeny` Expected: FAIL on the five new cases (rule "" instead of "deny-encoded-path").
- [ ] **Step 3: implement** `rawPathSuspect` and, in `Deny`, after the API-key check and before `NormalizePath`: `if rawPathSuspect(r) { return "deny-encoded-path", true }`. Comment: the policy matches the decoded path and the proxy forwards the raw one; a raw path that decodes to a separator, a dot, a percent or a control byte is refused rather than trusted to Prism Central's parser (write-up 36, P1).
- [ ] **Step 4: end-to-end test.** In `public_test.go` add `TestEncodedPathIsRefusedBeforeTheUpstream`: with a session, `POST https://prism.test/api/iam/v4.1.b3/authn/login-requests/a%2Fb/$actions/validate-credentials` → 403 with body rule `deny-encoded-path`, and the fake upstream's request counter unchanged (use the harness's upstream hit count, as `TestDenyListBeforeSiteCheck` does).
- [ ] **Step 5: run.** `go test ./internal/proxy` Expected: ok, including the existing allow-list and WebSocket tests (a `%` in a query string is not a path and must still pass: add `{"query percent", GET "/console/?x=%2F", "", false}` to the table if no case covers it).
- [ ] **Step 6: commit.** `fix(proxy): a raw path with an encoded separator, dot or control byte is refused`.

### Task 4: P4, WaitTask keeps polling through a transient error (same PR)

**Files:**
- Modify: `internal/nutanix/client.go:367-372`
- Test: `internal/nutanix/client_test.go`

- [ ] **Step 1: failing test.** `TestWaitTaskPollsThroughATransientError`: `f := newFakePC(t)`; make the first `GET PathTasks+"/t-1"` answer 500 and the next SUCCEEDED (add a `failOnce map[string]int` to the fake if `f.fail` is permanent: the fake's handler decrements it); `err := newClient(t, f, nil).WaitTask(ctx, "t-1")`; assert `err == nil` and the fake saw exactly two task reads. Add `TestWaitTaskGivesUpOnARefusal`: a 404 on the task read returns the error at once (one read).
- [ ] **Step 2: run.** `go test ./internal/nutanix -run 'TestWaitTask'` Expected: the first FAIL with the 500 error, the second PASS.
- [ ] **Step 3: implement.** In `WaitTask`, on a poll error: `if !IsTransient(err) { return err }`, then fall through to the `select` and poll again; count consecutive transient errors and return the last error after 20 of them (with `taskPoll` 2 s that is 40 s; name the constant `taskPollTransientLimit`). Comment: a 500 on the poll is Prism Central's IDF, not the task; returning it made `record` run the create again while the task still ran (write-up 36, P4).
- [ ] **Step 4: run.** `go test ./internal/nutanix ./internal/tenant` Expected: ok.
- [ ] **Step 5: commit.** `fix(nutanix): WaitTask polls on through a transient error instead of failing the step`.

### Task 5: P7, the API carries the pool and respects its lock (same PR)

**Files:**
- Modify: `internal/api/settings.go` (`settingsJSON.Tenants`, `toSettingsJSON`, `fromSettingsJSON`, `putSettings`)
- Modify: `internal/web/setup.go:157-166` (reuse the parse)
- Test: `internal/api/settings_test.go`, `docs/api.md:83`

**Interfaces:**
- Produces: `func settings.ParsePool(s string) (string, error)` in `internal/settings/settings.go`: trims, `netip.ParsePrefix`, `netplan.CheckPool`, returns `Masked().String()`; an empty string returns `"", nil`. (`internal/settings` may import `internal/netplan`; check with `go list -deps` that `netplan` does not import `settings`.)

- [ ] **Step 1: failing tests.** `TestSettingsPutKeepsThePool`: store a document with `Tenants.AddressPool = "172.20.0.0/16"`; `GET /api/v1/settings`, assert the body has `"address_pool":"172.20.0.0/16"`; `PUT` that body back; read the store, assert the pool is still `172.20.0.0/16`. `TestSettingsPutRefusesPoolChangeWhileInUse`: the fake tenant service reports `PoolInUse() == true`; `PUT` with `"address_pool":"172.21.0.0/16"` → 409 and the body's `error` contains "ready network"; the stored pool unchanged. `TestSettingsPutValidatesThePool`: `"address_pool":"10.0.0.0/8"` → 400 naming `tenants.address_pool` (use the value `netplan.CheckPool` refuses; read `netplan_test.go` for one).
- [ ] **Step 2: run.** `go test ./internal/api -run 'TestSettingsPut'` Expected: the three new ones FAIL.
- [ ] **Step 3: implement.** Add `AddressPool string \`json:"address_pool"\`` to `settingsJSON.Tenants`, copy it both ways; in `fromSettingsJSON` parse with `settings.ParsePool` (error → `tenants.address_pool: <err>`); an empty value keeps `base.Tenants.AddressPool`. In `putSettings`, when the parsed pool differs from the stored one and `h.svc.PoolInUse(ctx)` is true (or errs), answer 409 `the tenant address pool cannot change while a ready network uses one of its subnets`. In `web/setup.go` replace the inline parse with `settings.ParsePool`. Document `address_pool` in `docs/api.md` line 83 ("`address_pool` is kept when omitted and refused with 409 while a ready network uses it").
- [ ] **Step 4: run.** `go test ./internal/api ./internal/web ./internal/settings` Expected: ok.
- [ ] **Step 5: commit and PR.** `fix(api): PUT /settings carries the tenant address pool and keeps its lock`. Open PR `fix/review-36-small` with the four fixes; report the link.

### Task 6: P5, Disconnect removes memberships in the tenant's project only (PR `fix/review-36-ownership`)

**Files:**
- Modify: `internal/tenant/provision.go:522-530`
- Test: `internal/tenant/service_test.go`

- [ ] **Step 1: failing test.** `TestDisconnectKeepsMembershipsInOtherProjects`: provision a tenant on a site; in the fake, add a second role membership for the same `SAMLGroupExtID` with `ProjectExtID: "other-project"`; disconnect; assert the other-project membership still exists in the fake and the tenant's own one is gone. Assert the step detail says `1 deleted`.
- [ ] **Step 2: run.** `go test ./internal/tenant -run TestDisconnectKeepsMembershipsInOtherProjects` Expected: FAIL (the other membership was deleted).
- [ ] **Step 3: implement.** In the `role-membership` removal step: `if rm.IdentityExtID != p.SAMLGroupExtID || rm.ProjectExtID != p.ProjectExtID { continue }`. Comment: the group may be foreign and hold roles elsewhere (write-up 36, P5).
- [ ] **Step 4: run.** `go test ./internal/tenant` Expected: ok.
- [ ] **Step 5: commit.** `fix(tenant): Disconnect deletes the group's role membership in its own project only`.

### Task 7: P6 and P8, network objects are adopted inside the project and repaired on any path (same PR)

**Files:**
- Modify: `internal/tenant/network.go` (`findSubnet`, `findVPC`, the `net-vpc` and `net-subnet` steps, `adoptByName`)
- Test: `internal/tenant/network_test.go`

**Interfaces:**
- Produces: `findSubnet(ctx, pc, extID, name, project, vpc string) (*nutanix.Subnet, error)` and `findVPC(ctx, pc, extID, name, project string) (*nutanix.VPC, error)`. Rules: a stored `extID` that answers 404 returns `nil, nil` (deleted: create anew, never fall back to the name); an empty `extID` lists and returns the first object with that name **and** `ProjectExtID == project` (and, for a subnet, `VPCExtID == vpc` when `vpc != ""`; the external subnet `-ext` passes `vpc == ""` and is checked by its caller for `External`). Any other error is returned.
- Produces: `repairProject[T]` used by both steps: when the found object's project is not the placement's, delete it and create again through `inProject` (the existing helper), instead of returning it.

- [ ] **Step 1: failing tests.** `TestRetryNetworkIgnoresATenantMadeSubnetOfTheSameName`: build a network; in the fake, delete `-net` by its stored extId and create a subnet named `<project>-net` in the tenant's project but with `VPCExtID: "tenant-own-vpc"`; mark the placement `NetFailed` (as the watch would) and call Retry network; assert the placement's `SubnetExtID` is a new extId, not the tenant's, and the tenant's subnet still exists. `TestRetryNetworkNeverAdoptsByNameAcrossProjects`: a subnet `<project>-net` with `ProjectExtID: "other"` and no stored extId; Retry creates a new one and the other-project one is untouched. `TestRetryNetworkRepairsAnObjectWithoutItsProject`: a VPC `<project>-vpc` exists with `ProjectExtID: ""` and the placement stores its extId (the create path's read-back failed once); Retry network ends with `NetReady`, the old VPC deleted and a new one with the project stored. Add `TestBuildNetworkFailsOnAFailedListNotAbsent`: `e.pc.FailOnce["ListVPCs"] = &nutanix.APIError{Status: 503}` with no stored extId → the step fails with that error and creates nothing (count `CreateVPC` calls); same for `ListSubnets`.
- [ ] **Step 2: run.** `go test ./internal/tenant -run 'TestRetryNetwork|TestBuildNetworkFailsOnAFailedList'` Expected: the first three FAIL; the fourth may PASS already (then it stays as the pin).
- [ ] **Step 3: implement** the new signatures; update the three callers (`net-vpc`, `net-subnet`, `adoptByName`); in the two steps, when the found object's `ProjectExtID != p.ProjectExtID`, go through `inProject` after deleting it (reuse `inProject`'s delete-and-create by passing a create closure; do not duplicate its loop). Update the `buildNetwork` comment ("by the stored extId first, by name second" → "by the stored extId; by name only when none was stored, inside the project").
- [ ] **Step 4: run.** `go test ./internal/tenant ./internal/web ./internal/api` Expected: ok.
- [ ] **Step 5: update design 4.3** of `2026-10-29-tenant-network-design.md`: one sentence, "a stored extId that is gone means the object was deleted: the step creates it again and never adopts a same-named object; by name only when no extId was ever stored, and only inside the tenant's project and VPC."
- [ ] **Step 6: commit and PR.** `fix(tenant): network objects are adopted only inside the project, never by name after a delete`. Open PR `fix/review-36-ownership`; report the link.

### Task 8: P3, the exposure switch edits the public block only, and plain HTTP off the loopback is refused (PR `fix/review-36-exposure`)

**Files:**
- Modify: `internal/exposure/apply.go:288-320`
- Modify: `internal/config/config.go:226-240`
- Modify: `deploy/portico.appliance.yaml:8-9`, `deploy/portico.yaml.example:5`
- Test: `internal/exposure/exposure_test.go:485`, `internal/config/config_test.go`

- [ ] **Step 1: failing tests.** `TestSetModeEditsThePublicBlockOnly`: a YAML whose `public:` block has `listen` only (no `tls`) and whose `internal:` block has `cert_file`/`key_file`; `setMode(y, false)` returns an error naming `public.tls.cert_file` and the text unchanged; `setMode(directYAML, true)` still works (existing assertions). `TestValidateRefusesCleartextPublicListenerOffLoopback` in `config_test.go`: `Public.Listen ":443"` with empty `Public.TLS` → error containing `public.listen` and `public.tls is empty`; `"127.0.0.1:8081"` with empty TLS → no error.
- [ ] **Step 2: run.** `go test ./internal/exposure -run TestSetModeEditsThePublicBlockOnly ./internal/config -run TestValidateRefusesCleartextPublicListenerOffLoopback` Expected: both FAIL.
- [ ] **Step 3: implement.** In `setMode`, cut the text into the `public:` block (from the line `^public:` to the next line matching `^\S`, or the end) and apply the three regexes to that segment only, joining back; absent keys error as today. In `ValidateProcess`, mirror the internal check: `if c.Public.TLS.CertFile == "" && !isLoopbackListen(c.Public.Listen)` → `config: public.listen %q is not a loopback address and public.tls is empty; tenants would be served in clear`. Change the two YAML comments to "tunnel mode: listen 127.0.0.1:8081 with empty cert_file and key_file (keep the lines)".
- [ ] **Step 4: run.** `go test ./internal/exposure ./internal/config ./cmd/portico` Expected: ok (if a serve test uses a non-loopback public listener without TLS, give it the loopback).
- [ ] **Step 5: commit and PR.** `fix(exposure): the mode switch edits the public block only; a cleartext public listener off the loopback is refused`. Report the link.

### Task 9: The lab's addresses and wording out of the code (PR `chore/review-36-names`)

**Files:**
- Modify: production comments `internal/nutanix/client.go:2,127`, `internal/nutanix/clusters.go:124`, `internal/console/network.go:60`, `cmd/portico/serve.go:141,249`, `internal/proxy/upstream.go:19`, `internal/netplan/netplan.go:67`, `internal/nutanix/roles.go:59`, `internal/sites/monitor.go:37`, `internal/branding/theme.go:12,79,86`, `internal/settings/networking_rights.go:43`
- Modify: every `_test.go` and `.html` that `grep -rln '10\.23\.21[01]\|NTNX-01' internal cmd` lists (about 20 files)

- [ ] **Step 1: production comments.** "the lab Prism Central" → "a Prism Central"; "as the lab admin" → "as an administrator"; "the lab's cloud-init" → "cloud-init"; "measured on NTNX-01"/"on NTNX-01" → "measured on pc.7.6.0.6 (write-up N)" with the write-up the line already cites or the one in `docs/lab/README.md` for that topic; `10.23.210.175:9440` in `upstream.go:19` → `192.0.2.10:9440`; `10.23.211.0/24` in `netplan.go:67` → `198.51.100.0/24`. References to `docs/lab/...` and `lab/auth/...` paths stay: they are repository paths.
- [ ] **Step 2: tests and templates.** Replace, with one `sed` per file or by hand: `10.23.210.175` → `192.0.2.10`, `10.23.210.173` → `192.0.2.11`, `10.23.210.190` → `192.0.2.20`, `10.23.210.200` → `192.0.2.30`, `10.23.210.254` → `192.0.2.1`, `10.23.210.0/24` → `192.0.2.0/24`, `10.23.211.0/24` → `198.51.100.0/24` (and its `/28`s and hosts accordingly: `10.23.211.N` → `198.51.100.N`), `NTNX-01` → `cluster-1`. Where a test asserts a gateway is or is not inside a subnet, re-check the arithmetic after the change. The site codes stay.
- [ ] **Step 3: verify.** Run: `grep -rn '10\.23\.\|NTNX-01\|the lab\|lab admin' internal cmd deploy image --include='*.go' --include='*.html' --include='*.yaml' --include='*.tf'` Expected: no output. `go test ./...` Expected: ok. `gofmt -l cmd internal` Expected: empty.
- [ ] **Step 4: commit and PR.** `chore: documentation addresses and no lab wording in the code and tests`. Report the link.

### Task 10: Backlog sweep, write-up 36 and the 0.2.2 notes (PR `docs/review-36-backlog`)

**Files:**
- Modify: `docs/backlog.md`
- Create: `docs/releases/v0.2.2.md`
- Add: `docs/lab/2026-10-30-36-project-review.md` and its row in `docs/lab/README.md` (already written, uncommitted)

- [ ] **Step 1: close what is closed.** Remove from "Simplification after v0.1.0" the `go.mod` item; for `IdentityFromContext`, `adoptLegacy`/`LegacySiteCode`, `prism_host`, `nutanix.getRaw`, the three cookie expiries, the retired `lab/vm` scripts: `grep` each; keep the line when the code is still there (it is, as of 2026-10-30: `edge-router.sh` sources `lab/vm/lib.sh`, so reword that item to "the retired VM's scripts except `lab/vm/lib.sh`, which the network scripts use"). Remove the "Tenant ready network (0.2.0)" item on the role update replacement only if a write-up measured it; otherwise keep.
- [ ] **Step 2: add the review's Minor findings** as a new section `## Deferred from the re-read of 2026-10-30 (write-up 36)`, one line each with the file, copied from the write-up's "Code: Minor" and "Structure" lists, plus the test gaps not closed by Tasks 2–8 (the CSRF walk of every route, the fake Prism Central accepting what the real one refuses, the tenant-facing pages never showing a Prism Central address). Add the structural split (`settings` bootstraps into their own package, the service layer out of `api`) as "its own milestone, plan to be written".
- [ ] **Step 3: 0.2.2 notes** (`docs/releases/v0.2.2.md`, for the tag Marco will authorise later): "A fix release after the re-read of the project (write-up 36)"; What changed: P1, P2, P3, P4, P5, P6+P8, P7, one line each as what the MSP gets; Upgrading: none needed; Measured on: "unit tests of each fix; the proxy rules re-measured on pc.7.6 when the appliance is next rebuilt (write-up 06 to be updated for the encoded paths)". Known limits: 0.2.0's.
- [ ] **Step 4: commit and PR.** `docs: backlog after the re-read, write-up 36, the 0.2.2 notes`. Report the link.

### Finish

- Every PR link reported to Marco with one line each; merges only on his OK, with `--rebase --delete-branch`; after the last merge, `main` is pulled and `go test ./...` and `lab/test_scripts.sh` run once more on it.
- The tag `v0.2.2` and the publishing of its draft release need Marco's explicit authorisation each.
