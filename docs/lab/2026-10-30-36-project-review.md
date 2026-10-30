# 36. The re-read of the project after 0.2.1

- Date: 2026-10-30
- Version: main `73ed50c` (0.2.1), Prism Central pc.7.6.0.6 for the
  measured items
- Question: after two weeks of milestones and three releases, do the
  documents still describe the product, and does the code still hold the
  four security rules and the design's ownership rules?

## What was read

- Every document a newcomer opens: README, CONTRIBUTING, SECURITY,
  `docs/install.md`, `docs/operating.md`, `docs/api.md`,
  `docs/backlog.md`, the five designs, the nine ADRs, the release notes,
  the index of `docs/lab/`.
- The Go code, by three fresh reviewers with one brief each: the public
  path (`proxy`, `auth`, `exposure`, `secrets`, `config`, `console`,
  `cmd/portico`); provisioning (`tenant`, `nutanix`, `sites`,
  `idp/keycloak`, `netready`, `netplan`); the admin side (`web`, `api`,
  `settings`, `store`, `branding`, `textedit`). The Important findings
  below were re-read in the code before being recorded; the Minor ones
  are the reviewers' and are marked so.

## Documents

Sound: `docs/api.md` lists the 43 routes the API registers and nothing
else; `docs/operating.md`, `deploy/README.md` and `image/README.md`
carry no stale version; every write-up is in the index; `go mod tidy`
changes nothing; the main design points at the sites, branding and
network designs where they superseded it.

To fix, in order:

1. **README and the install guide still say 0.1.0** (badge, "First
   release" callout, three links to `releases/v0.1.0.md`, the milestone
   table without the ready network; in the install guide the title, the
   download URLs, `--release 0.1.0`, "in 0.1.0"). The install guide
   should stop naming a version: `<version>` and the `releases/latest`
   link, so a release does not rewrite it.
2. **SECURITY.md**: "there is no supported installation yet and no
   release to upgrade" is three releases old.
3. **README's repository layout** lacks `sites`, `exposure`, `netready`,
   `netplan`, `console`, `config`, `textedit`, the `console` and
   `exposure` commands, `image/` and `docs/history`.
4. **Main design**: goal 2 still says "one Prism Central"; Milestone 4
   has no completion date; there is no milestone for 0.2.0; "Step 2:
   multi-Prism Central" is done; section 4.10 sits before 4.8.
5. **Backlog**: items already closed (`go.mod` indirect entries; to be
   checked one by one against the code), "known limits of v0.1.0"
   wording.
6. **`docs/lab/README.md`** is titled "Milestones 0 to 2 and the layout
   pass" and indexes 36 write-ups.

## Code: Important (verified in the code)

| # | Where | What a person running the appliance gets |
|---|---|---|
| P1 | `internal/proxy/policy.go:64-71`, `upstream.go:70` | The deny list matches the decoded path; the proxy forwards the raw one. `…/validate-credentials%2F..`, `…%00` or an id with `%2F` pass the local-login rule and reach Prism Central byte for byte. Whether Prism Central then treats them as a login is unmeasured: the rule holds on its parser, not ours. Fix: refuse a raw path with `%2F`, `%5C`, `%00`, `%2E` or control bytes before anything else; test with each. |
| P2 | `internal/proxy/accesslog.go:110-115` | `authScheme` logs the first field of `Authorization`; a scheme-less header (`Authorization: eyJ…`) writes the token into the access log, on all three listeners. Fix: log only a known scheme, else `other`. |
| P3 | `internal/exposure/apply.go:303-320`, `internal/config/config.go:234` | `setMode` rewrites the first `cert_file`/`key_file` lines in the file; a file edited as the shipped comment suggests ("tunnel mode: no tls block") gets the public pair written into `internal.tls` on the switch to direct, and `ValidateProcess` does not refuse a non-loopback public listener without TLS: plain HTTP on 443. Fix: anchor the edit to the public block, refuse that listener, test both. |
| P4 | `internal/nutanix/client.go:367-372`, `internal/tenant/provision.go:24-40` | `WaitTask` returns on the first failed poll; `record` takes that 500 as transient and runs the whole step again while the task still runs, so a VPC, subnet, floating IP or project is created twice. Disconnect is then refused ("VPCs: 1") for an object Portico made. Fix: keep polling on transient errors inside `WaitTask`. |
| P5 | `internal/tenant/provision.go:522-530` | Disconnect deletes every role membership of the SAML group, in every project, before the step that keeps a foreign group. Fix: filter on the placement's project. |
| P6 | `internal/tenant/network.go:461-498`, `678-711` | Network objects are adopted by name across the whole Prism Central, against design 4.3 ("stored by extId, not found again by name"). A tenant that deletes `-net` and makes its own subnet with that name sees it adopted by Retry network and deleted at Disconnect. Fix: adopt only inside the tenant's project and VPC, and only when no extId was ever stored. |
| P7 | `internal/api/settings.go:191-218`, `internal/settings/settings.go:181-184` | `PUT /api/v1/settings` has no `address_pool`: it stores `""`, which the next read fills with the default. The pool lock and `CheckPool` live in the web handler only. A `GET` then `PUT` of the same body resets a custom pool while networks are built from it. Fix: one merge for the document (web and API), the lock inside it, a test on the API. |
| P8 | `internal/tenant/network.go:379-431`, `594-608` | The "created without its project" repair runs only on the create path; `findVPC`/`findSubnet` adopt such an object and `readBack` fails on every Retry network. |

Known and already in the backlog, listed here for weight: the Portico
Networking policy filters every subnet, VPC, route table and floating IP
of the Prism Central (`networking_rights.go:22`); with P6 a bug can
reach outside the transit VPC.

## Code: Minor (reviewers' findings, to be confirmed when taken)

- Public path: sealed values carry no associated data (a value can be
  moved between fields by someone with database access); `Verify=false`
  with no pin runs `InsecureSkipVerify` with only a warning; the
  pass-through keeps forwarding to a removed site when none is left;
  the console hard-codes `/etc/portico/portico.yaml`; `serve` makes a
  fresh master key before checking the store holds sealed settings;
  a failed later bind leaves earlier listeners serving; the access log's
  `session` means "cookie present".
- Provisioning: read-back by name, not by the task's entity; `WaitTask("")`
  on four create paths; floating IPs and base objects not adopted at
  Disconnect when the create call failed after creating; tenant rows
  written from a snapshot outside the lock (`AssignSubnets`,
  `SetAllowance`, `Retry`); the role falls back to Project Admin
  silently, which makes the allowance no limit; `foreignGroup` matches
  the first group of that name; lowering the allowance deletes any
  `<project>-fip-N`.
- Admin side: a site's stored key follows a URL change (the check
  refuses it, the update does not); `http` site URLs allowed; the site
  chooser shows `Site.Name`, which a YAML-made site takes from Prism
  Central's address; the unsealed key reaches two fragments' template
  data; `POST /login` has no CSRF or Origin check (lockout of the
  admin's address from another site); `/password` not rate-limited;
  `portico admin set-password` applies no password policy; site Save
  stores Code and Name before failing on Order; card saves and `PUT
  /settings` update a document read outside the lock; `ForgetSite`
  deletes placements before the document; branding cache and store
  written without one lock; `PUT /settings` without `operation_timeout`
  stores 0.

## Structure (reviewers' reading, agreed)

- `internal/settings` holds the document, the live checks and 1,600
  lines of bootstraps that create things in Keycloak and Prism Central:
  the third part is its own package.
- `internal/api` is the HTTP layer and the service layer at once
  (`SettingsOptions`, the site operations, the admin password and the
  limiter); `web` imports it for all of that. ADR 0003's single service
  layer exists, inside the HTTP package. Three merges of the settings
  document (web, API body, API overlay) are the cause of P7.
- `internal/auth` is tenant sessions, admin sessions and site routing in
  one; `internal/proxy` also runs the SAML automation; `sites_runtime.go`
  is a subsystem in `main`; the step runner is copied four times in
  `tenant`; `nutanix` reads operations, roles and policies through two
  API versions each.
- Exported and unused outside their package: `config.Config.Validate`,
  `exposure.CertPath/KeyPath/TokenPath`, `proxy.CookieAttrs/StashName`,
  `auth.Discover`, `nutanix.ListProjects/WaitTask/Path*`,
  `tenant.DefaultSites/DefaultAllowance/WatchNetworks/UserGroupsLister`,
  `netplan.ErrPoolFull`, `sites.ProbeOnce/CheckTrust/StateKey/...`,
  `branding.Service.LogoFile`, `api.IdentityFromContext` (tests only;
  nothing logs who acted).
- Unreachable: `unset.html` and the "no password" branches, since
  `serve` always stores the factory password.

## Test gaps for the rules

- No test sends an encoded slash, NUL or trailing byte to the deny list;
  none checks the upstream receives the path the policy approved.
- No scheme-less `Authorization` in the access-log tests; no test that
  `Callback`/`Logout` log lines hold no token, code or state.
- The IdP-down test checks two statuses and does not count requests
  reaching Prism Central.
- No failed read (5xx) injected on `GetVPC`, `GetFloatingIP`,
  `ListSubnets`, `ListVPCs`, `ListFloatingIPs`, `Routes` during build or
  Disconnect: "a failed read is not absent" is tested on `GetSubnet`
  only. No "created but the call failed" case, no 500 on a task poll.
- The fake Prism Central accepts what the real one refuses: a VPC on an
  unshared external subnet, deleting a subnet a VPC uses, deleting a
  project that holds resources.
- No CSRF test walks every route; no test that `PUT /settings` keeps
  the pool; none that tenant-facing pages never show a Prism Central
  address.

## Names in the code

No customer, company or person other than the author. The word "lab"
is in no UI string. Production comments say "the lab Prism Central",
"as the lab admin", "the lab's cloud-init" (`nutanix/client.go:127`,
`clusters.go:124`, `console/network.go:60`), and `serve.go:141,249`
say "on NTNX-01". The real addresses `10.23.210.175`, `.173`, `.200`,
`.254`, the range `10.23.211.0/24` and the codes `AZ-IT-MI1`,
`AZ-US-NYC1` are in tests and in `proxy/upstream.go:19`,
`netplan.go:67`, `site_new.html:8-10`; the documents moved to
documentation blocks on 2026-10-30 (`e856cea`), the code did not.

## Implications

- Nothing found breaks a rule on the appliance as shipped: P1 depends on
  Prism Central's parser, P3 on a hand-edited file, P4 to P8 on a
  Prism Central answering 5xx or on a tenant acting against its own
  network. They are the gaps between "holds in the lab" and "holds by
  construction".
- The order to close them: P1, P2, P4, P7 first (small, each with its
  test); then P5, P6, P8 together, since they are one ownership rule;
  P3 with its config check. The structure items are a milestone of
  their own, not a fix pass.

## How to re-measure

Re-run the three reviewer briefs (in this write-up's history) on the
same packages after the fix pass, and `go test ./...`. For P1, measure
on pc.7.6 what `validate-credentials%2F..` and `%00` do when they reach
Prism Central's IAM directly from the management network, and record
the answer in write-up 06.
