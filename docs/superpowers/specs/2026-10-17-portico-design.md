# Portico — Design (2026-10-17)

## 1. Intent

Portico is a virtual appliance that runs inside a Nutanix cluster and acts as
the single, authenticated entry point through which an MSP's tenants reach
Nutanix services. The first service is Prism Central. Later services (Nutanix
Enterprise AI, Nutanix Kubernetes Platform) plug into the same identity and the
same tenant model.

Why now: Prism Central has projects and roles, but nothing that safely puts a
multi-tenant Prism Central on the Internet and nothing that turns "add a
customer" into one action. Nutanix Central targets a different scale and
model. Small MSPs have no turnkey answer.

## 2. Goals, non-goals, anti-goals

### Goals for step 1 (first public release)

1. **Appliance**: a VM image importable into Prism Central, with a console
   wizard for network settings and a web UI for everything else.
2. **Exposure**: one Prism Central reachable at a public URL, either through a
   Cloudflare Tunnel (`cloudflared`, the "easy" path) or directly on port 443.
   Both modes give the same security guarantees.
3. **Identity**: every request on the public path is authenticated by Portico
   against the identity provider before it reaches Prism Central. Prism
   Central itself trusts the same identity provider via SAML. One login for the
   tenant user.
4. **No local login from outside**: on the public path, Prism Central local
   accounts (including `admin`) can never log in, in any exposure mode, even if
   the identity provider is down (fail closed).
5. **Tenant arrival**: from the Portico web UI, create a tenant: project in
   Prism Central, group in the identity provider, role mapping, first user.
   The tenant user logs in through the public URL and sees only their project.
6. **Admin path**: platform administration (Prism Central local accounts, Portico
   settings) only on an internal listener reachable from LAN/VPN.

### Explicitly deferred (step 2 and later)

- Richer web UI and MSP features (quotas, networks, image catalog per tenant,
  tenant self-service, usage reporting).
- Non-interactive API access for tenants (Portico-issued API keys).
- Brokering each tenant to its own directory via Keycloak. Entra ID and
  other SAML/OIDC providers need no network path. An on-premises Active
  Directory needs LDAP reachability from Keycloak, typically a site-to-site
  VPN terminating in a dedicated management network reachable only from
  Keycloak, never in a tenant network or Portico's own; ADFS or Entra
  Connect on the customer side are the no-VPN alternatives.
- Brownfield analysis: inventory of existing projects (including the pc.7.6
  "Default Project" that holds everything migrated at upgrade) with an
  exclusion list so Portico never touches them.
- Legacy projects on pc.2024.3–7.5 (separate adapter, reduced guarantees), if
  ever.
- Multiple Prism Central instances behind one Portico (the data model allows
  it from day one; step 1 implements and tests one).
- Nutanix Enterprise AI and Nutanix Kubernetes Platform as further services.
  For NKP the direction is one MSP-managed control plane with tenant-only
  worker nodes, "real provider" style. To be measured, not designed yet.
- Cluster-per-customer isolation (the gate must not prevent it; nothing in
  step 1 builds it).
- Non-Nutanix backends (Proxmox and others). The service interface (public
  host name, upstream, access policy, tenant provisioning hooks) must not
  forbid them, but Portico's identity is "the small MSP's path to SP
  Central": the tenant primitive is a Projects 2.0 project, and that is what
  keeps tenants compatible. A Proxmox module would map tenants onto pools,
  users and ACLs with different guarantees and no migration story. Decided
  with Marco on 2026-10-17: an extension point, not a goal.

### Anti-goals (failure even if it "works")

- Any configuration in which a Prism Central local account can authenticate
  through the public URL, including when the identity provider is unavailable
  or when Prism Central falls back to local auth.
- Security that depends on Cloudflare being in front. Direct exposure must be
  as safe as tunnelled exposure.
- An MSP having to hand-edit proxy configuration files to add a tenant or a
  service.
- Portico becoming a second Prism Central: it orchestrates, it does not copy
  state it does not own.

## 3. Constraints

- **Open source**, released, not sold. License: Apache-2.0 (my call; easy to
  change before first release).
- **All project text in English**: code, comments, docs, commit messages, UI
  strings. Only the conversation with Marco is in Italian.
- Supported Prism Central: **pc.7.6 and later**, tested on pc.7.6 / AOS 7.6
  (cluster NTNX-01). Reason: pc.7.6 (released 2026-07-27) introduced a new
  Projects framework ("Projects 2.0"): project scope extended to VMs, images,
  networking, Flow security, DR, alerts and tasks; entity-level authorization
  policies on top of RBAC; per-project-per-cluster quotas; project-scoped
  categories; modernized v4 APIs; automatic, irreversible migration of
  existing entities into a system "Default Project" at upgrade. Nutanix's own
  Service Provider Central (SP Central, GA mid-2026, part of Nutanix Central)
  onboards a tenant by creating a project in Prism Central, so Projects 2.0 is
  the tenant primitive Nutanix itself is building on. Legacy projects
  (pc.2024.3–7.5) are a different model, not the same model behind another
  API: they are out of scope for step 1 and, if ever supported, get a separate
  adapter with reduced guarantees.
- Positioning vs SP Central: Portico creates tenants with the same primitive
  SP Central uses (a Projects 2.0 project per tenant, group-based role
  mapping, no global roles). An MSP that later adopts Nutanix Central / SP
  Central should find tenants it can recognise, not a parallel model to
  unwind. Portico adds no state to Prism Central beyond what a project needs.
- Nutanix documentation (Projects Guide pc.7.6, Security Guide 7.6, v4 API
  reference for IAM and Projects) is kept by Marco in `kb/` (gitignored), outside
  the repository (copyrighted material, never committed). Portal pages are
  not readable from the CLI, so this folder is the source for API details.
- Step 1 tenants use the **browser only**. The public path accepts only
  sessions obtained through interactive login; non-interactive API access for
  tenants (Portico-issued API keys bound to federated identities) is step 2.
- Prism Central SAML limits (Nutanix Security Guide 7.6): SP-initiated login
  only, redirect binding only, `NameID` becomes the username, sessions max 8 h
  / 15 min idle. Behind a proxy the IdP must be created with `redirectURL`
  set to the public name (write-up 04).
- Appliance footprint target: 2 vCPU, 6 GiB RAM, 40 GiB disk (Portico +
  Keycloak + PostgreSQL + cloudflared on one VM).
- Lab: NTNX-01, single node, Prism Central `https://10.23.210.175:9440`,
  VLAN 210 `10.23.210.0/24`, gateway `.254`, free IPs from `.190` (arping is
  not possible from the Mac, which reaches the VLAN through a VPN: verify
  with ping plus the Prism Central VM NIC list, as `lab/vm/create-vm.sh`
  does). Credentials are held by Marco and never enter the repository.

## 4. Architecture

### 4.1 Components on the appliance

| component | role | how it runs |
|---|---|---|
| `portico` (Go, single binary) | reverse proxy, policy, web UI, REST API, tenant provisioning, console wizard | native systemd service |
| Keycloak | identity provider: OIDC for Portico, SAML for Prism Central, brokering to customer directories (AD, Entra ID, LDAP), MFA | podman container, systemd quadlet |
| PostgreSQL | Keycloak's database | podman container, systemd quadlet |
| `cloudflared` | outbound tunnel to Cloudflare, enabled only in tunnel mode | official rpm, systemd service |
| SQLite (embedded) | Portico's own state: connected Prism Centrals, tenants, services, settings | file under `/var/lib/portico` |

Decision: the proxy lives inside the Go binary (`net/http/httputil`
ReverseProxy, WebSocket pass-through for VM consoles), not in nginx. The
per-request decision (session valid? local-login attempt? which tenant? which
service?) is the product; keeping it in code avoids generating and reconciling
nginx configuration and makes "add a service" a Go module instead of an nginx
block. Trade-off accepted: less battle-testing than nginx; MSP scale (tens of
tenants) does not need nginx performance.

### 4.2 Listeners

| listener | bind | who | what passes |
|---|---|---|---|
| public | `:443` (direct) or loopback behind `cloudflared` (tunnel) | tenants | only requests with a valid Portico session; local login paths blocked |
| internal | internal IP `:8443` | MSP admins | Portico admin UI/API; raw pass-through to Prism Central with local login allowed |
| console | tty1 | whoever opens the VM console | network wizard, status, URL to open |

Firewall rule the design depends on: Prism Central port 9440 must be reachable
only from Portico's internal IP and from the MSP management network. Portico
documents and checks this (it can test from its own address only; the MSP
enforces it on the network). The internal pass-through is a convenience, not
the only way administrators reach Prism Central (design call).

Internal pass-through detail (design call): it listens on Portico's internal
IP on port 9440, so Prism Central's absolute paths work unchanged, with
Portico's internal certificate. The Portico admin UI stays on `:8443`.

Keycloak exposure (design call): Keycloak gets its own public host name
(`id.<msp>.example`), fronted by Portico like any other service. Policy for
that host: no Portico session required, Keycloak admin console paths
(`/admin`, `/realms/master`) denied on the public listener. Two host names
mean two tunnel ingress rules or two SAN entries in the certificate; in
exchange the Keycloak admin console is never published and the "session
required" rule has no exception on the Prism Central host.

Admin authentication on the internal listener (design call): the Portico
admin UI has a local break-glass account created at first boot (password set
on first access from the console wizard or the internal UI), so the admin path
does not depend on Keycloak. OIDC login for admins can be added later; the
break-glass account stays.

### 4.3 Request flow on the public path

1. Browser hits `https://prism.<msp>.example` → Cloudflare edge → tunnel →
   Portico, or directly → Portico `:443`.
2. Portico checks its session cookie. No valid session → redirect to Keycloak
   (OIDC authorization code flow). After login, Portico issues its own cookie.
3. With a session, Portico applies policy (measured on pc.7.6, write-up 06):
   deny `Authorization: Basic` and `X-ntnx-api-key` headers; deny
   `POST /api/iam/*/authn/login-requests/*/$actions/validate-credentials` and
   `POST /PrismGateway/j_spring_security_check`; deny
   `GET /api/iam/*/authn/users` listing for tenants (a Project Admin can
   enumerate all accounts); deny paths outside the allow-list for the
   service once write-up 08 says one is practical; add/strip headers.
   `lab/auth/probe-local-login.sh` must return 403 on every line.
4. Request is proxied to Prism Central on 9440 with the public host name.
5. Prism Central's own login page offers two buttons, SAML and local account
   (write-up 04). The user picks SAML (step 2 may send tenants straight to
   `/api/iam/authn/initiate-login` with the IdP selected; hiding the local
   link is cosmetic because its POST is denied) and is sent to Keycloak,
   which already has a session → immediate return with the SAML assertion.
   Prism Central creates its session, scoped by the user's project
   membership.

Two protocols (OIDC for Portico, SAML for Prism Central), one user login.

### 4.4 Tenant model

A tenant in Portico is a row that ties together:

- a Projects 2.0 project in a given Prism Central (`id` slug and `extId`),
  with a resource group allocating clusters and storage containers to it.
  Quotas are not part of the project APIs (they live in the Policy Engine,
  write-up 05): no quota in step 1, quota management is step 2,
- a group in Keycloak (`tenant-<slug>`), with optional brokered identity
  provider for that customer's directory,
- a role mapping in Prism Central: group → role within the project (never a
  global role),
- metadata (display name, contacts, status).

Creating a tenant performs these steps idempotently (measured calls in
write-up 05: project with `id` slug, resource group with cluster and
container, SAML user group, IdP `$actions/share`, role membership with
`ProjectsScopeTemplate`), records each step, and finishes with an automated
visibility check. Deletion runs the reverse order and must unshare the IdP
before deleting the project. The tenant row stores: project `id` and
`extId`, resource group extId, SAML group extId, role membership extIds,
IdP sharing state. Project naming: `<prefix>-<slug>`, prefix configurable,
default `portico` (SP Central uses `sp-`). A tenant can be disabled (group
disabled → login impossible) and deleted (project left in place unless
explicitly chosen; Portico never deletes VMs).

Step 1 scope decisions (design calls):

- **Tenant users are Keycloak users** in the tenant group, with MFA available.
  Brokering to the customer's own directory (AD, Entra ID, LDAP) is step 2;
  the tenant row keeps the optional field so step 2 adds it without a
  migration of meaning. `NameID` is the email address, unique across the
  whole Prism Central.
- **First user**: the MSP admin creates it from the tenant page; Keycloak
  issues a temporary password shown once, with "update password" required at
  first login. Email delivery is optional (SMTP configured in Keycloak).
- **Visibility check** has two levels. Static (always, step 1): read back the
  project, the group→role mapping, and assert the group holds no global role
  and no mapping outside its project. Live (step 1 if lab item 6.4 confirms
  it is practical): a per-tenant probe user `portico-probe-<slug>` performs a
  real SAML login through the public path and lists what it sees; the probe
  user is disabled in Keycloak after the check and documented as such. If the
  live check proves fragile, it is a button in the UI rather than part of
  "tenant created".
- **SAML trust in Prism Central is configured by Portico** through the API
  during setup. Prism Central accepts a single SAML identity provider: if one
  already exists, Portico refuses to overwrite it and explains (adoption of an
  existing IdP is a brownfield feature, step 2). Lab item 6.8.

### 4.5 Service account in Prism Central

Portico never stores the credentials of `admin` or of any human
administrator. It works through a dedicated **service account** named
`portico` (pc.7.6 `userType: SERVICE_ACCOUNT`, no password) authenticated
with an **API key** sent as `X-ntnx-api-key` (write-up 07). The account is
a member of the predefined `Project Manager_acp` authorization policy,
which was measured sufficient for the whole tenant cycle plus probe-user
creation; no custom role in step 1. This mirrors SP Central's
`sp_svc_acc_<uuid>` pattern. The setup wizard offers two paths:

1. **Pre-created**: the MSP creates the service account and an API key in
   Prism Central (Admin Center → IAM → Identities → Service Accounts), adds
   it to `Project Manager_acp`, and enters the key.
2. **Bootstrap**: the MSP enters, once, the credentials of any account
   allowed to create users and edit authorization policies (it need not be
   the built-in `admin`). Portico creates the service account, generates
   the key, adds the account to the policy, verifies the key works, stores
   only the key (encrypted), and discards the bootstrap credentials (held in
   memory only, never written to disk or logs).

Key rotation (create second key, switch, revoke first) is a step 2 UI
feature; step 1 logs the key expiry. The UI says "an account that can
create users and edit authorization policies", not "the admin account".

### 4.6 Data and secrets

- SQLite with WAL, single file, backup = copy of `/var/lib/portico`.
- Prism Central service account credentials and Keycloak client secrets encrypted at rest
  (AES-GCM) with a key in `/etc/portico/master.key` created at first boot,
  mode 0600, owned by the `portico` user.
- TLS: direct mode uses either an uploaded certificate or ACME (HTTP-01 on
  443/80 or DNS-01 via Cloudflare API). Tunnel mode terminates TLS at
  Cloudflare; Portico listens on loopback plain HTTP for `cloudflared`.

### 4.7 Web UI

Server-rendered Go templates with HTMX for interactivity. One language, one
binary, no JavaScript toolchain; contributors from the infrastructure world
can work on it. Step 1 UI scope: setup wizard (identity, Prism Central
connection, exposure), tenant list, create tenant, tenant status with the
visibility-check result, system status.

### 4.8 Console wizard

`portico console` (Go TUI) on tty1 via a getty override: hostname, IP/mask/
gateway/DNS for the internal interface, re-runnable at any time, writes
NetworkManager profiles with `nmcli`. Shows service health and the URL of the
internal admin UI. Nothing else is configurable from the console.

### 4.9 Appliance image

- Rocky Linux 9, built from the official GenericCloud qcow2 with a repeatable
  build (Packer + QEMU or a kickstart-based build; builder's choice).
- CIS Rocky Linux 9 Benchmark **Level 1 Server** applied at build time with
  OpenSCAP (`scap-security-guide`, profile `cis_server_l1`), remediation then
  re-scan; the HTML report ships with each release.
- Known CIS exceptions, documented in the report: `/var/lib/containers` on its
  own partition (exec allowed) so `/tmp`, `/var/tmp`, `/dev/shm` can stay
  noexec; separate partitions for `/var`, `/var/log`, `/var/log/audit`,
  `/home` sized within the 40 GiB target.
- SELinux enforcing. SSH key-only, internal interface only. firewalld with the
  listeners above and nothing else.
- Output: `portico-<version>.qcow2` + checksum + OpenSCAP report, importable
  into the Prism Central image service. Built by GitHub Actions and attached
  to a GitHub release, so an MSP downloads a finished image and never builds
  one. The repository goes public when this pipeline works (public
  repositories get Actions minutes for free). CIS hardening reuses the
  approach already built in Marco's `nkp-preprovisioned-toolkit/cis`. Cloud-init accepted for unattended
  network config; the console wizard covers the attended case.
- Updates: `dnf` for the OS, a new `portico` rpm for the application,
  container image tags for Keycloak/PostgreSQL. Full image replacement
  documented as the clean path.

## 5. Security model summary

| threat | control |
|---|---|
| local Prism Central account used from Internet | no request reaches 9440 without a Portico session; `Basic` auth denied; local-login endpoints denied; Prism Central local auth remains usable only via internal listener |
| identity provider outage | public path fails closed (no session → no proxying); admin path unaffected |
| tenant reaches another tenant's resources | Prism Central projects/roles (first barrier); Portico never grants global roles; automated visibility check per tenant |
| tenant reaches platform admin functions | role scope in Prism Central; Portico allow-list of paths per service as second barrier |
| appliance compromise | CIS L1, SELinux, minimal services, secrets encrypted at rest, no inbound ports in tunnel mode |
| Cloudflare removed/bypassed | direct mode has identical policy; Cloudflare is transport, not security |

## 6. Lab measurements (Milestone 0, 2026-10-17, pc.7.6.0.6)

Each item below was a hypothesis; the write-ups in `docs/lab/` hold the
evidence and the re-measure procedure.

1. **SAML behind a different host name** — MEASURED: the global
   SP metadata is pinned to the IP, but a SAML IdP created with
   `redirectURL=https://<public host>` (iam `v4.1.b3`) gets its own
   `.../saml-identity-providers/{id}/sp-metadata` with entityID and ACS on
   the public name; Keycloak's client must use that entityID. Browser
   round trip measured end to end: SAML button → Keycloak → callback on
   the public name → console with only the tenant's VM (write-up 04).
2. **Local login endpoints** — MEASURED: `POST /api/iam/v4.1.b3/authn/login-requests/{iam-sessionid}/$actions/validate-credentials`,
   legacy `POST /PrismGateway/j_spring_security_check`, `Authorization: Basic`
   on every API generation, and `X-ntnx-api-key`. Deny list in §4.3
   (write-up 06).
3. **Projects 2.0 API** — MEASURED: `multidomain/v4.4.b1` projects (`id`
   slug required) and resource groups; `iam/v4.1.b3` role memberships with
   `scopeTemplateName=ProjectsScopeTemplate`; IdP shared per project; v3 is
   empty; a full create/bind/delete cycle leaves `_internal` unchanged except
   its `modifiedTimestamp` (the lab VM and the subnet share touched it,
   write-up 05); quotas live in the Policy Engine, not in these APIs
   (write-up 05).
4. **Tenant UI behaviour and WebSockets** — MEASURED: the console uses four
   API generations (`/api/*/v4*`, `/api/nutanix/v3`, `/dm/v3`,
   `/PrismGateway/services/rest/v1`) plus `/console`, `/ui/iam`, `/static`;
   a short prefix allow-list is realistic; the VM console WebSocket is
   `wss://<page host>/vnc/vm/{uuid}/proxy` and the handshake passes the
   proxy (101) (write-up 08).
5. **Fallback to local auth** — MEASURED: local login works with the IdP
   down; nothing "falls back", local auth is always on; the proxy policy is
   the only thing that closes it (write-up 06).
6. **Resource cost** — MEASURED: Keycloak 26.3 + PostgreSQL 16 idle ≈ 1.3 GiB
   of the 6 GiB VM; 6 GiB stays (write-up 03).
7. **`portico` identity** — MEASURED: a service account with an API key
   (`X-ntnx-api-key`), member of the predefined `Project Manager_acp`
   policy, performs the whole tenant cycle and creates probe users
   (write-up 07).
8. **SAML IdP via API** — MEASURED: create/list/share/unshare/delete on
   `iam/v4.1.b3`; `v4.0` rejects `redirectURL`; an existing IdP is detected
   by listing (write-up 04).

Still open after Milestone 0: the VM console popup end to end in a browser
(handshake measured with curl only), whether `Project Manager` may create
the SAML IdP and share subnets (7), quota automation (Policy Engine).

## 7. Repository layout (initial)

```
portico/
  README.md                 what it is, what it is not, status
  LICENSE                   Apache-2.0
  docs/
    superpowers/specs/      this design
    lab/                    measurements from NTNX-01
    adr/                    architecture decision records
  cmd/portico/              main: serve | console | provision | ...
  internal/
    proxy/                  reverse proxy + policy
    auth/                   OIDC session handling
    idp/keycloak/           Keycloak admin client
    nutanix/                Prism Central client (version adapters)
    tenant/                 tenant model and provisioning steps
    web/                    templates, HTMX handlers, static assets
    store/                  SQLite
    console/                TUI network wizard
  image/                    appliance build, kickstart/packer, CIS profile
  deploy/                   systemd units, quadlets, firewalld, cloudflared
```

## 8. Left to the builder

- Go module layout details, HTTP router, template engine, migration tool.
- Exact OpenSCAP tailoring file and partition sizes within 40 GiB.
- Packer vs kickstart for the image build.
- Keycloak realm layout (one realm for the MSP, groups per tenant, brokered
  IdPs per customer) and how Portico bootstraps it on first run.
- Session cookie parameters, CSRF handling, rate limiting on the public
  listener.
- Test strategy: unit tests for policy and provisioning; integration tests
  against a recorded Prism Central API; lab runs against NTNX-01 scripted but
  manual to launch.

## 9. Milestones

| # | milestone | done when |
|---|---|---|
| 0 | Lab measurements (section 6) | write-ups in `docs/lab/`, design updated |
| 1 | Core binary: proxy + OIDC session + policy + internal listener | a browser through Portico reaches Prism Central only after Keycloak login; `curl -u admin:…` through the public path gets 403 from Portico |
| 2 | Tenant arrival | `portico provision` / UI creates project + group + mapping, visibility check passes on NTNX-01 |
| 3 | Appliance image | Rocky 9 qcow2 with CIS L1 report, console wizard, cloudflared option, imported and booted in NTNX-01 |
| 4 | First release (step 1 complete) | README, install guide, release artefacts, known limits documented |

Step 2 (later): richer UI, MSP features, brownfield inventory with exclusion
list, multi-Prism Central, further services.

