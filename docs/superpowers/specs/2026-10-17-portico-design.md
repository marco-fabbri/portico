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

- Richer web UI and MSP features (quotas, image catalog per tenant,
  tenant self-service, usage reporting). Networks per tenant moved to
  0.2.0 for the **ready network**, one VPC, subnet and floating IPs per
  tenant and site (`2026-10-29-tenant-network-design.md`); further
  networks, the subnet stretch and VPN stay deferred.
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
- Multiple Prism Central instances behind one Portico: brought forward and
  built in Milestone 4 as **sites** (design
  `2026-10-20-sites-design.md`, ADR 0004). Still deferred from it:
  configuring disaster recovery between sites (replication, protection
  policies, recovery plans: today the MSP does it in Prism Central and a
  DR site is a site like any other; Portico managing them is in the
  backlog, decided with Marco on 2026-10-29), grouping sites by region on
  the site page, and choosing API versions per site.
- Nutanix Enterprise AI and Nutanix Kubernetes Platform as further services.
  For NKP the direction is one MSP-managed control plane with tenant-only
  worker nodes, "real provider" style. To be measured, not designed yet.
- Cluster-per-customer isolation (the gate must not prevent it; nothing in
  step 1 builds it).
- A Portico-owned tenant console (VM list, create, console) that hides
  Prism Central's UI entirely, chosen per tenant instead of the Prism
  Central console (Marco, 2026-10-20: the direction the one-public-name
  site model is built for). Not planned yet. Its prerequisite is per-user identity towards the
  v4 API (the deferred Portico-issued keys): with the service account alone,
  Portico would decide what each user sees and the project barrier in Prism
  Central would stop being the first control. The API boundary (section
  4.7) keeps the option open at no cost.
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
| console | tty1 | whoever opens the VM console | network wizard, break-glass password, status, URL to open |

Firewall rule the design depends on: port 9440 of every site's Prism
Central (Milestone 4) must be reachable
only from Portico's internal IP and from the MSP management network. Portico
documents and checks this (it can test from its own address only; the MSP
enforces it on the network). The internal pass-through is a convenience, not
the only way administrators reach Prism Central (design call).

Internal pass-through detail (design call): it listens on Portico's internal
IP on port 9440, so Prism Central's absolute paths work unchanged, with
Portico's internal certificate. The Portico admin UI stays on `:8443`.
Recommended (2b review): give the admin listener its own host name
(`portico-admin.<msp>.example`) rather than the Prism Central name on
another port. Cookies and `SameSite` are port-blind, so a shared name
makes the admin session same-site with the tenant pages; Portico strips
its own cookies before forwarding and accepts the UI session on UI paths
only, but a separate name removes the relation altogether.

Keycloak exposure (design call): Keycloak gets its own public host name
(`id.<msp>.example`), fronted by Portico like any other service. Policy for
that host: no Portico session required, Keycloak admin console paths
(`/admin`, `/realms/master`) denied on the public listener. Two host names
mean two tunnel ingress rules or two SAN entries in the certificate; in
exchange the Keycloak admin console is never published and the "session
required" rule has no exception on the Prism Central host. The two public
names must share a registrable domain (`prism.msp.example` and
`id.msp.example`): Portico's session cookie is `SameSite=Lax` and the SAML
response is a top-level POST from the identity host to the Prism Central
host, which a Lax cookie joins only between same-site hosts (Milestone 1).

Admin authentication on the internal listener (design call): the Portico
admin UI has a local break-glass account `admin`, so the admin path does
not depend on Keycloak. Its password is set with `portico admin
set-password` (stdin, Milestone 2a) or from the console wizard; the UI
refuses to serve pages until it is set. The REST API authenticates with
HTTP Basic against that account; the UI with a session cookie obtained
from a login form on the same listener (`portico_admin`, HttpOnly,
SameSite=Strict, Secure when the listener has TLS, 8 h, 15 min idle), a
per-session CSRF token on every state-changing form, and a lockout
after failed logins shared with the API's Basic check (after three
failures in ten minutes the address is refused with 429 for 1, 2, 4,
then 8 s, before any password is checked). Polls on a timer do not
count as activity, and the API refuses changes a browser marks as sent
from another site. Off the loopback the
listener must have TLS (enforced since 2a). OIDC login for admins can be
added later; the break-glass account stays.
Marco's call on 2026-10-19 (Milestone 3): the account starts with the
factory password `portico/4u` (Nutanix's convention) and the first login
on the admin UI forces a new one; the API refuses the factory password.
The window between the first boot and that login is the same one
Nutanix accepts with `nutanix/4u`, mitigated by never publishing 8443.

### 4.3 Request flow on the public path

1. Browser hits `https://prism.<msp>.example` → Cloudflare edge → tunnel →
   Portico, or directly → Portico `:443`.
2. Portico applies the deny list first, before looking for a session
   (measured on pc.7.6, write-ups 06 and 12): every `Authorization`
   header, whatever its scheme (the console authenticates with cookies
   only), `X-ntnx-api-key`, any method on
   `/api/iam/*/authn/login-requests/*/$actions/validate-credentials` and on
   `/PrismGateway/j_spring_security_check`, `GET /api/iam/*/authn/users`
   (a Project Admin can enumerate all accounts). Paths are matched after
   normalisation (case, repeated slashes, `;` parameters, `.` and `..`).
   A credential sent without a session gets 403, never a login redirect.
3. Portico checks its session cookie (`__Host-portico_session`, Secure,
   HttpOnly, SameSite=Lax, 8 h absolute, 15 min idle). No valid session: a
   browser navigation is redirected to `/.portico/login`, anything else
   gets 401. The login is the OIDC authorization code flow with PKCE
   against Keycloak; Portico's own endpoints live under the reserved
   prefix `/.portico/` (`login`, `callback`, `logout`) on the Prism Central
   host. Portico talks to Keycloak on the loopback (Keycloak's dynamic
   backchannel keeps the public issuer in tokens) and discovers it lazily,
   so Portico runs with Keycloak down and fails closed. The login state
   (`__Host-portico_oidc`) lives ten minutes; a callback that arrives
   without it (the login page sat open longer) restarts the login once,
   with a marker in the OIDC state so a browser that blocks cookies gets
   the plain error instead of a loop (Milestone 2f). With a session, the
   path must be inside the allow-list measured in write-up 08 (prefixes
   `/console/`, `/static/`, `/ui/iam/`, `/api/`, `/dm/`,
   `/PrismGateway/services/rest/v1/`, `/vnc/`, plus `/`, `/auth_code`,
   `/favicon.ico`, `/redirect` (Prism Central's own Sign Out, write-up 12);
   configurable). Portico's cookies are stripped before
   forwarding. `lab/auth/probe-local-login.sh` is the acceptance test
   (write-up 09).
4. Request is proxied on 9440, with the public host name, to the Prism
   Central of the session's site (Milestone 4): the site must be among
   the tenant's active placements at every request; without one, a
   single placement is chosen when available and several send the
   browser to the site page (`/.portico/sites`), anything else gets 403.
   A site in maintenance or unreachable refuses new entries with a
   branded page; sessions already on it keep working (design sites §4.1,
   §4.3).
5. Prism Central's own login page (`/ui/iam/providers`) offers two buttons,
   SAML and local account (write-up 04). Since Milestone 2c Portico answers
   that path on the public host with its own landing page (section 4.10)
   which selects the SAML identity provider and starts the flow by itself:
   `GET` then `PUT /api/iam/v4.1.b3/authn/login-requests/{iam-sessionid}`
   with the console's own body and `loginProviderId` set, then
   `GET /api/iam/authn/initiate-login`, which answers a self-submitting
   SAML form (write-ups 12 and 13; 1.2 s on the lab). The console's own
   Sign Out ends on `/redirect?logout=…`; Portico turns it into its own
   logout, so signing out of Prism Central also ends the Keycloak session
   instead of signing the tenant straight back in. A sign-out the
   browser marks as coming from another site goes home and ends nothing. Keycloak already
   has a session → immediate return with the SAML assertion. Prism Central
   creates its session, scoped by the user's project membership. If the
   selection call fails (for example after a Prism Central upgrade changes
   the IAM API), Portico forwards the original page and the user clicks as
   in Milestone 1: the automation is convenience, the deny list is the
   control. Hiding the local-account link is cosmetic because its POST is
   denied.

Two protocols (OIDC for Portico, SAML for Prism Central), one user login,
and the user never sees a Nutanix login page.

### 4.4 Tenant model

A tenant in Portico is a row that ties together (since Milestone 4 the
Prism Central parts below are one **placement** per site where the tenant
has resources; the Keycloak group stays one per tenant, so one login
serves every site; design `2026-10-20-sites-design.md` §4.2):

- a Projects 2.0 project in a given Prism Central (`id` slug and `extId`),
  with a resource group allocating clusters and storage containers to it.
  Quotas are not part of the project APIs (they live in the Policy Engine,
  write-up 05): no quota in step 1, quota management is step 2,
- a group in Keycloak (`tenant-<slug>`), with optional brokered identity
  provider for that customer's directory,
- a role mapping in Prism Central: group → role within the project (never a
  global role),
- metadata (display name, contacts, status),
- since 0.2.0, its network plan: a `/24` from the MSP's address pool (or a
  subnet exception) and the floating IPs per site; on a site whose ready
  network is on, the connection gains the network steps (overlay external
  subnet on a `/28` of the transit VPC's routed prefix, sharing, VPC,
  default route, subnet, floating IPs, read-back) and stores those
  objects by `extId` with a network state of its own
  (`2026-10-29-tenant-network-design.md` 4.3).

Creating a tenant performs these steps idempotently (measured calls in
write-up 05: project with `id` slug, resource group with cluster and
container, SAML user group, IdP `$actions/share`, role membership with
`ProjectsScopeTemplate`), records each step, and finishes with an automated
visibility check. Deletion runs the reverse order and must unshare the IdP
before deleting the project. The tenant row stores: project `id` and
`extId`, resource group extId, SAML group extId, role membership extIds,
IdP sharing state. Project naming: `<prefix>-<slug>`, prefix configurable,
default `portico` (SP Central uses `sp-`); the project and group prefixes
are fixed once a tenant exists. A tenant can be disabled (group
disabled → login impossible). Disconnecting a tenant from a site deletes
an empty project and refuses a non-empty one; deleting a tenant needs no
sites left; Portico never deletes VMs (sites usability design 4.6,
ADR 0006).

Step 1 scope decisions (design calls):

- **Tenant users are Keycloak users** in the tenant group, with MFA available.
  Brokering to the customer's own directory (AD, Entra ID, LDAP) is step 2;
  the tenant row keeps the optional field so step 2 adds it without a
  migration of meaning. `NameID` is the email address, unique across the
  whole Prism Central.
- **First user**: the MSP admin creates it from the tenant page; Keycloak
  issues a temporary password shown once, with "update password" required at
  first login. Email delivery is optional (SMTP configured in Keycloak).
  The realm Portico creates carries the password policy `length(12) and
  notUsername and notEmail` (Milestone 2f); MFA is a realm setting the MSP
  turns on in Keycloak.
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
  existing IdP is a brownfield feature, step 2). Lab item 6.8. Details
  (the `saml-trust` step of `POST /api/v1/sites/{id}/setup`; Milestone 2e
  had it as a call of its own, removed before 0.1.0): Portico fetches
  the realm's SAML descriptor on the Keycloak backchannel (measured: with
  a fixed `KC_HOSTNAME` it already carries the public URLs) and refuses
  when its entityID is not `https://<id_host>/realms/<realm>`; creates the
  identity provider with the descriptor inline (`idpMetadataXml`,
  base64), `redirectURL=https://<portal_host>`, username and email
  attribute `email`, groups attribute `groups`; reads the per-IdP SP
  metadata (plain XML on `.../sp-metadata`) for the entityID and the ACS
  URL; and registers the SAML client in Keycloak with that entityID as
  `clientId`, the ACS as redirect URI, assertions and responses signed,
  POST binding, name id format `email`, no single logout (write-up 13),
  `frontchannelLogout: false`, and the `email` and `groups` mappers. The
  step runs with the service account; when Prism Central refuses it
  (403), the response says so and the step accepts the one-time
  credentials of section 4.5 instead. Measured (write-up 15): a member
  of `Project Manager_acp` cannot create an identity provider, so on
  pc.7.6 this step always takes the one-time credentials; they are used
  for that call and never stored.

### 4.5 Service account in Prism Central

Portico never stores the credentials of `admin` or of any human
administrator. It works through a dedicated **service account** named
`portico` (pc.7.6 `userType: SERVICE_ACCOUNT`, no password) authenticated
with an **API key** sent as `X-ntnx-api-key` (write-up 07). The account is
a member of the predefined `Project Manager_acp` authorization policy,
which was measured sufficient for the whole tenant cycle plus probe-user
creation; no custom role in step 1. Since 0.2.0 the site setup also
creates the **Portico Networking** role and its policy for the account
(subnets, VPCs, route tables, floating IPs, sharing), and the **Portico
Tenant** role tenants get in their project, Project Admin without the
floating IP operations (`2026-10-29-tenant-network-design.md` 4.5). This mirrors SP Central's
`sp_svc_acc_<uuid>` pattern. Each site offers two paths (sites
usability design 4.1, 2026-10-23):

1. **One page, the main path** ("Add a site", and "Finish setup" or
   "Repair" on the site's page): Portico reads the site's certificate
   first (trusted, or pinned once the MSP compared its fingerprint), and
   only then asks, once, for the credentials of any account allowed to
   create users, roles and identity providers (it need not be the
   built-in `admin`). One run checks the version and the rights, creates
   the service account and its key, adds the account to the policy,
   verifies the key, creates the SAML trust and picks the defaults; it
   stores only the key (encrypted) and discards the credentials (held in
   memory only, never written to disk or logs). The summary shows one
   line per step; a failed step leaves the site incomplete and not
   served, and running it again goes on from there.
2. **Pre-created, under "Advanced"**: the MSP creates the service account
   and an API key in Prism Central (Admin Center → IAM → Identities →
   Service Accounts), adds it to `Project Manager_acp`, and enters the
   key; a SAML provider made by hand is named there too. The API keeps
   both, never as the main path.

Key rotation (create second key, switch, revoke first) is a step 2 UI
feature; step 1 logs the key expiry. The UI says "an account that can
create users and edit authorization policies", not "the admin account".

Bootstrap details (the `service-account` step of
`POST /api/v1/sites/{id}/setup`, sites usability design 4.1; Milestone 2e
had it as a call of its own, removed before 0.1.0). The call carries the
one-time credentials; the site's address and TLS settings are the stored
ones. Portico opens a
Basic-auth client for that call only; the credentials live in the request
and in that client, and the log records the username at most. Steps, each
idempotent: find or create the service account (`username` as given,
default `portico`, `userType: SERVICE_ACCOUNT`); create an API key named
`portico-<yyyymmdd-hhmmss>` (a key is shown once by Prism Central, so an
existing account gets a new key and the old ones stay until an
administrator revokes them); find the predefined `Project Manager_acp`
policy and add the account's extId to its user identity filter when
missing (PUT with the read-back body stripped of its read-only fields,
`If-Match` with the ETag); verify the new key by listing clusters with
the ordinary API-key client; store URL, TLS settings and key. A failure
after the key was created deletes that key (best effort, on a context of
its own, since the request's deadline is usually what failed) so a retry
does not leave unused keys behind; when the creation call itself fails
after Prism Central created the key (measured: a timeout, write-up 15),
Portico lists the account's keys and revokes the one with the name it
chose. The store step is part of the bootstrap: each step writes only the
fields it set, through a read-change-write under the settings service's
lock, so a card saved while a step runs is kept; a key that could not be
stored is revoked at once. The same one-time credentials may be offered
to the SAML step (section 4.4) when the service account is refused there.

The Keycloak side has the same shape: `POST /api/v1/settings/bootstrap/keycloak`
takes the one-time credentials of a Keycloak administrator (the master
realm's bootstrap admin created with the appliance, or any user allowed to
create realms) and the realm name. Portico creates the realm when missing
(`loginWithEmailAllowed`, `registrationEmailAsUsername`), the confidential
admin client `portico-admin` with a service account holding the
realm-management roles `manage-users view-users query-users query-groups
view-realm manage-realm manage-clients view-clients`, and the OIDC client
`portico` (standard flow, PKCE S256, redirect
`https://<portal_host>/.portico/callback`, post-logout redirect
`https://<portal_host>/`); it reads both client secrets and stores realm,
client ids, issuer `https://<id_host>/realms/<realm>` and secrets. Every
client Portico creates has `frontchannelLogout: false`. The Keycloak
administrator's credentials are never stored.

Since 2026-10-26 (ADR 0007) nobody types those credentials on a new
appliance. The first boot writes a copy of the temporary administrator
(`kcadmin`) readable by the `portico` user alone; once the public names
are saved, a background job runs the same bootstrap with it for the
realm `portico` (display name "Portico MSP"), stores the settings, then
deletes `kcadmin` from the master realm and the copy, in that order, so
a restart in between finishes the job. Keycloak 26.3 does not create the
temporary administrator again at its next start (measured). Public names
changed later move the OIDC client's redirect and the issuer through
Portico's own admin client. The typed path stays for a realm made
outside Portico.

### 4.6 Data and secrets

- SQLite with WAL, single file, backup = copy of `/var/lib/portico`.
- Prism Central service account credentials and Keycloak client secrets encrypted at rest
  (AES-GCM) with a key in `/var/lib/portico/master.key` created at first
  boot, mode 0600, owned by the `portico` user (`/etc/portico` is
  root-owned and read-only for the service, so the key lives next to the
  store; a backup must copy the key separately). Details (Milestone 2d): the
  key is 32 random bytes; each secret is sealed on its own with
  AES-256-GCM and a fresh nonce, stored as `v1:<base64 nonce+ciphertext>`
  inside the JSON settings document; the key file is created by `portico
  serve` when missing and refused when readable by group or others. A
  store whose secrets cannot be opened with the key stops the service
  with a clear error: no fallback to plain text, no silent defaults.
  Backup = copy of `/var/lib/portico` plus the key file, kept apart.
  Measured in write-up 14: the error names the field; since the unit
  restarts always, systemd's start limit leaves it `failed` after ten
  seconds of retries, and recovery is "key back, `systemctl reset-failed
  portico`, `systemctl start portico`".
- TLS: direct mode uses either an uploaded certificate or ACME (HTTP-01 on
  443/80 or DNS-01 via Cloudflare API). Tunnel mode terminates TLS at
  Cloudflare; Portico listens on loopback plain HTTP for `cloudflared`.
  Since 2026-10-26 (ADR 0007) the certificate, a self-signed trial pair
  and the tunnel token are given on the admin UI's setup page: Portico
  checks them and writes a request that a root path unit
  (`portico-exposure.path`) applies, because the service is
  unprivileged. The public listener reloads its pair when the file
  changes, so a renewal keeps the tenants' sessions; a mode change
  restarts Portico. ACME is not built yet.

### 4.7 Web UI

Server-rendered Go templates with HTMX for interactivity. One language, one
binary, no JavaScript toolchain; contributors from the infrastructure world
can work on it. Step 1 UI scope: setup wizard (identity, Prism Central
connection, exposure), tenant list, create tenant, tenant status with the
visibility-check result, system status, branding (section 4.10).

**Settings: file and store** (design call for Milestone 2). The YAML file
keeps only what the process needs before it can open its store: listeners,
TLS files, store path, master key path, log level. Everything an MSP
decides after installation lives in the store and is entered through the
UI and API: public host names, Prism Central connection and service
account key, Keycloak realm and admin client, OIDC client, tenant naming
and placement, branding. Secrets in the store are encrypted with the
master key (section 4.6). Branding was the first store-held setting
(Milestone 2c); Milestone 2d moves the rest. Precedence: when the store
holds settings they win and the YAML's runtime sections are ignored
(logged once at start); when it holds none, the YAML's runtime sections
apply as since Milestone 1, so an installation upgrades without a
flag day, and `portico settings import` copies the YAML and its secret
files into the store once. Changing settings needs a restart of the
process (listeners, TLS, clients): the wizard's "Apply" asks `serve` to
stop cleanly and systemd restarts it (`Restart=always`); the page says
so and polls a readiness fragment until Portico is back. Apply is
refused (409) while a tenant operation runs, because the restart closes
the listeners before waiting for the operation and the service would
stay dark until it ended (write-up 14). A connectivity check sends the
stored service-account key only to the stored Prism Central host: a
check against another URL must carry its own key. The bootstrap
paths (service account created by Portico, Keycloak realm and clients
registered by Portico, SAML identity provider created by Portico) are
Milestone 2e (sections 4.4 and 4.5): in 2d the wizard takes the values
the MSP already has.

**Setup mode and drafts** (Milestone 2e). A fresh appliance has a YAML
with process settings only and an empty store. `portico serve` then
starts in *setup mode*: only the admin listener is up, serving the login,
the setup page, the settings and branding API and the health endpoint;
the public and pass-through listeners are not started (nothing can reach
Prism Central, fail closed) and the tenant routes answer 503 "setup
mode". The stored document may be incomplete: a card is saved on its own,
the bootstrap calls fill the fields they produce, and the setup page lists
what is still missing. Apply is refused (409) while a required field is
missing, naming the fields; after Apply the restart finds a complete
document and starts in running mode. A store whose document is incomplete
at start (an interrupted setup) also starts in setup mode, logged once.
The YAML path keeps its own rule: a file-based installation must be
complete, as since Milestone 1.

**The API is the boundary** (decided with Marco on 2026-10-18, ADR 0003).
Every action the admin UI performs is a documented REST call on the
internal listener (`/api/v1/...`, JSON, same break-glass or OIDC session as
the UI). The HTMX handlers call the same service layer as the API handlers
and never reach the store, Keycloak or Prism Central on their own; the plan
for a feature defines the endpoint and its test before the page. Reasons:
an MSP's script or Ansible creates a tenant with exactly the checks the
page applies, so there is no second path to protect; the logic is tested
without a browser; and the UI can be replaced, or another front end put on
top (section 2, deferred), without touching the logic.

### 4.10 Tenant-facing pages and branding

Everything a tenant user sees before Prism Central is either Keycloak or
Portico, and both carry the MSP's identity, not Nutanix's or Portico's.

- **Keycloak login theme.** Portico generates a Keycloak login theme
  (login, OTP, update-password, reset-password pages) from its branding
  settings: MSP name, header text, claim (the tagline), logo, and three
  colours (background, text, accent). The theme directory is mounted into
  the Keycloak container; Portico writes it and sets it as the realm's
  login theme through the admin API at every start and on every save,
  also when no branding was ever saved (the default below). The MSP never
  edits Keycloak. The theme sets CSS and messages only (the header is
  Keycloak's `loginTitleHtml` message, the tab title "Sign in to <MSP
  name>" its `loginTitle`); no page template is overridden (ADR 0005).
- **Landing page.** After the Keycloak login Portico shows its own page
  with logo and a progress indicator while the SAML round trip runs
  (section 4.3 step 5). One Prism Central: the redirect starts at once, no
  artificial delay; a configurable minimum display time exists with
  default 0. Mechanics (measured in write-up 12): Portico answers
  `GET /ui/iam/providers` on the public host with the landing page, which
  sends the browser to `/.portico/saml-start`; that handler reads the
  login request Prism Central created for this browser
  (`GET /api/iam/v4.1.b3/authn/login-requests/{iam-sessionid}`, with the
  browser's cookie), writes it back with `loginProviderId` set to the
  SAML identity provider, and redirects to `/api/iam/authn/initiate-login`.
  If any step fails, the browser is sent to the original provider page
  with a one-shot marker so Portico forwards it unchanged (no loop) and
  the user clicks as in Milestone 1.
- **Branding settings** are the first store-held settings (section 4.7):
  MSP name (required: tab title, landing title, the logo's alternative
  text, the fallback header), header text (at most 80 characters, one
  line, may be empty), claim (the tagline, at most 160), three colours
  as `#rrggbb` (background of the page; text, i.e. everything written on
  the background; accent, i.e. the primary button and the links inside
  the white card, which stays white), logo (Portico's arch drawn in the
  text colour, an uploaded image, or none; an upload is PNG, JPEG or
  SVG, at most 256 KiB; an SVG is parsed and accepted only as static
  drawing (allow-listed elements, no event handlers, no DTD, links and
  `url()` only to its own ids or an embedded raster image), because
  Keycloak serves the theme's copy from the login origin), tab icon (a PNG of 128
  to 1024 px on its longer side, the shorter at least half of it, at
  most 256 KiB, centred on a square; without one, Portico's arch in
  white on the accent colour), minimum landing display time. Edited on
  the admin UI and the API; applied to Portico's pages at once and to
  the Keycloak theme on save.
  Every tenant-facing page (Keycloak's login, OTP, update-password,
  reset-password and error pages; Portico's landing, site and error
  pages) has one header in a centred column: the logo, at most 96 px
  tall and 320 px wide with its aspect ratio kept; the header text, bold
  and exactly as typed; the claim under them. With neither a logo nor a
  header text, the MSP name takes the header text's place. A fresh
  Portico is a pre-filled branding drawn with the same layout: MSP name
  and header text `Portico`, the claim "The single, authenticated entry
  point to your Nutanix services.", the arch, background and accent
  `#a73422`, text `#ffffff`. A branding stored before the colours and
  the header text existed (and a `PUT` that omits them) reads as
  background = its accent, text `#ffffff`, header text = its MSP name
  when it has a logo, so an upgrade keeps an MSP's identity.
  On save the admin UI and the API compute the WCAG 2 contrast of the
  text on the background and of white on the accent: below 4.5:1 the
  branding is saved and the answer carries a warning naming the pair
  and its ratio. The admin Branding page shows a live preview of the
  login page, drawn by Portico from the fields being edited (a logo
  chosen and not yet uploaded included), not the real Keycloak page in
  a frame (ADR 0005). Several Prism Centrals (Milestone 4): after the Keycloak login a
  tenant with more than one site gets the **site page**, one tile per
  site with its code, name and status (design sites §2, variant B); with
  one site it goes straight on, and the landing page offers a "switch
  site" link when there are several. An optional banner is a link that
  opens in a new tab and never blocks the redirect.
- **Tab icons.** From the tab icon Portico makes `favicon.ico` (16, 32
  and 48 px PNGs inside, read by every browser, Safari included) and a
  180 px `apple-touch-icon` (Safari's favourites and pinned tabs, iOS),
  served without a session at `/.portico/favicon.ico` and
  `/.portico/apple-touch-icon.png` on both public names and written into
  the Keycloak theme as `img/favicon.ico`. Portico's pages link them
  with a version of what they show, so a browser that keeps its own
  favicon cache fetches the new icon after a change. Keycloak's login
  page links its copy at a fixed address: Safari keeps the icon it saw
  there until its caches are emptied (write-up 22); a versioned link
  would mean overriding Keycloak's whole page template. The admin UI
  has its own: the arch on dark grey. Prism Central's console keeps
  Nutanix's icon: Portico does not change Prism Central's pages.
- **Host names.** Prism Central uses absolute paths from `/`, so it cannot
  live under a prefix. Decided with Marco on 2026-10-20 (Milestone 4,
  ADR 0004), replacing "one public host name per Prism Central": every
  site shares the one Prism Central public name. The tenant's chosen site
  is held in the Portico session and checked against the tenant's active
  placements on every request; the routing sends each request to that
  site's Prism Central on the internal network. The browser holds one
  site at a time; Prism Central's cookies of the other sites wait in
  per-site stash cookies that never reach an upstream, so returning to a
  site needs no new login. No Prism Central address is ever shown to the
  tenant.

### 4.8 Console wizard

`portico console` (Go TUI) on tty1 via a getty override: hostname, IP/mask/
gateway/DNS for the internal interface, re-runnable at any time, writes
NetworkManager profiles with `nmcli`. Sets the break-glass password. Shows service health and the URL of the
internal admin UI. Nothing else is configurable from the console.
Milestone 3 had added the exposure and the Keycloak administrator's
credentials to it; Marco's install test of 2026-10-26 moved both back to
the admin UI (ADR 0007): the certificate is uploaded from the browser and
Portico creates the Keycloak realm with the first boot's temporary
administrator, which it then deletes.

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
- Second delivery form (agreed with Marco on 2026-10-18): the same
  components installed on any Rocky Linux 9 host the MSP already runs,
  on Proxmox or in a public cloud, with an rpm or an install script (the
  systemd unit and the quadlets are host-agnostic; the lab VM is exactly
  that since Milestone 1). Only network reachability matters: port 9440
  of every Prism Central from Portico's address, never from the Internet,
  and the public names towards the tenants.
- Updates: `dnf` for the OS; for the application the installer of the
  new release run in place (`install.sh --release`), which stops
  Portico, copies the store and the master key to
  `/var/lib/portico/backup-<old version>-<time>/` and replaces the binary,
  the units and the quadlets (container image tags for
  Keycloak/PostgreSQL included); no rpm. The backup of an appliance is a
  snapshot of its VM in Prism Central (decided with Marco on 2026-10-26,
  Milestone 6): Keycloak's database lives in the podman volume
  `postgres-data`, outside `/var/lib/portico`, so a copy of that
  directory onto a new image loses the realm and every tenant user, and
  copying the master key off the appliance would break "the master key
  never leaves the appliance"; the snapshot holds both and keeps the key
  inside. Full image replacement is therefore not a path in v0.1.0; an
  export of Keycloak's database with the store is in the backlog. The qcow2 carries no container image:
  PostgreSQL and Keycloak are pulled from their registries at first boot
  (write-up 29), a known limit of v0.1.0 until they ship in the image.
- Builder's calls, fixed with Marco on 2026-10-19 for Milestone 3:
  - Build: Packer with the QEMU builder on a GitHub Actions x86_64 runner
    (KVM available), from the Rocky Linux 9 boot ISO with a kickstart,
    because the CIS partition layout needs a fresh disk; the GenericCloud
    image stays the base of the lab VM and of the "existing host" form.
  - One installer, `deploy/install.sh`, used by the image build and by
    the second delivery form: packages, the `portico` user and
    directories, the binary, the quadlets (PostgreSQL, Keycloak), the
    units (`portico.service`, the console on tty1, cloudflared), firewalld
    and SSH policy. Re-running it on an installed host upgrades it.
  - Hardening: OpenSCAP with `scap-security-guide` profile
    `xccdf_org.ssgproject.content_profile_cis_server_l1` and a tailoring
    file for the documented exceptions, remediation then evaluation; the
    HTML report is a release asset. The control list and the disclaimer
    wording follow `nkp-preprovisioned-toolkit/cis`; the Ansible subset
    itself is not reused (no Ansible on the appliance).
  - Console wizard (section 4.8) also sets the break-glass password, shows
    the generated Keycloak administrator credentials once for the setup
    page, and configures exposure: direct with a certificate and key
    (pasted or self-signed for a trial) or a Cloudflare tunnel token.
    These need root and a tty, which the web UI does not have; the
    exposure card in the web UI and ACME are deferred.
  - Keycloak needs its public name as `KC_HOSTNAME` (measured, write-up
    19: with `KC_HOSTNAME_STRICT=false` alone the SAML descriptor read on
    the loopback names the loopback). Portico writes it into a file the
    Keycloak quadlet loads on every settings save that carries the
    identity host, and a path unit restarts Keycloak; the installer
    needs no public name.
  - Secrets (Keycloak administrator and database passwords) and the TLS
    pairs are generated at the first boot of every appliance by
    `portico-firstboot.service`, never in the image.
  - Release: a tag `v*` builds the image, the OpenSCAP report, the
    checksums and the binary tarball and attaches them to a GitHub
    release; `deploy/tofu/nutanix` (reusing the Nutanix module of the
    NKP toolkit) uploads the image from the release URL and creates the
    VM, with optional cloud-init network for the unattended case.
  - Out of Milestone 3: the pair (4.11), the rpm, ACME, the exposure card.

### 4.11 Deployment topologies: single node and pair

Direction agreed with Marco on 2026-10-18; step 1 builds and tests the
single node, the pair is planned after Milestone 2b. The point of the
section is that nothing built now may forbid the pair. High availability
is optional: the topology is chosen at installation time, "install single"
or "install pair". Turning a running single node into a pair ("add a
node") is a separate piece of work, planned on its own after the pair
install exists.

**Single node** (step 1): one appliance VM with Portico, Keycloak,
PostgreSQL (Keycloak's) and SQLite (Portico's). Availability comes from
the Nutanix VM high availability: the VM restarts on another host.

**Pair**: two appliance VMs, typically in two datacenters, both able to
reach every connected Prism Central on port 9440 across the datacenter
interconnect (the firewall rule of section 4.2 then names both Portico
addresses). The nodes are equal in front of the tenants: the two public
host names (Prism Central, shared by every site since Milestone 4,
section 4.10, and identity) resolve to both, through two `cloudflared` connectors on the same tunnel
or a DNS/GSLB with health checks in direct mode. Serving a Prism Central
from the node in the other datacenter costs the interconnect round trip;
origin priority in the tunnel or GSLB affinity keeps the local node
preferred.

What each kind of state does in the pair:

- **Sessions**: the Portico session (with the tenant's chosen site since
  Milestone 4) becomes a cookie encrypted with a key
  shared by the two nodes, so a node restart or a switch between nodes
  does not log the user out. Small change, planned for Milestone 2b
  together with the landing page.
- **Identity**: one Keycloak per VM, clustered with each other, over one
  PostgreSQL primary with a streaming replica on the other VM. Keycloak is
  active/active, the database active/passive. Portico keeps talking to
  its local Keycloak on the loopback. PostgreSQL active/active
  (multi-master) is rejected: Keycloak expects a single writer, the write
  load is tiny, and conflict resolution on identity data is a risk with
  no benefit.
- **Portico store**: in the pair it lives in the same PostgreSQL, so there
  is one database failover for everything. In the single node it stays
  SQLite. The store is therefore behind a Go interface from Milestone 2a
  on, with SQLite as the first implementation. The admin API runs on the
  node whose database is primary; proxying works on both, because
  forwarding needs only the list of Prism Centrals. The store stays a
  rebuildable record: a `reconcile` command (step 2, shared with the
  brownfield inventory) rebuilds the rows from the projects with
  Portico's prefix, the groups and the role memberships.
- **Failover**: promote the replica, Keycloak and Portico on the surviving
  node use it, names already resolve there; users log in again, nothing
  else is lost. Automatic failover needs a quorum: with two VMs alone an
  automatic promotion risks two primaries when the interconnect fails, so
  it is offered only with a third witness node placed elsewhere (a small
  VM in a third site, another cluster, a VPS); without a witness the
  promotion is a documented, scripted Portico command. Synchronous commit
  is an option when the interconnect latency allows it.

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
  IdPs per customer); Portico bootstraps it from the setup page (section
  4.5). Keycloak's own hostname setting (`KC_HOSTNAME=https://<id_host>`)
  must match the identity public name: the lab sets it by hand, the
  appliance image writes it from the settings (Milestone 3).
- Session cookie parameters, CSRF handling, rate limiting on the public
  listener.
- Test strategy: unit tests for policy and provisioning; integration tests
  against a recorded Prism Central API; lab runs against NTNX-01 scripted but
  manual to launch.

## 9. Milestones

| # | milestone | done when |
|---|---|---|
| 0 | Lab measurements (section 6) | done 2026-10-17: write-ups in `docs/lab/`, design updated |
| 1 | Core binary: proxy + OIDC session + policy + internal listener | done 2026-10-17 (write-up 09): a browser through Portico reaches Prism Central only after Keycloak login; `curl -u admin:…` through the public path gets 403 from Portico |
| 2a | Tenant arrival, API | done 2026-10-18 (write-up 11): `POST /api/v1/tenants` creates project + resource group + SAML group + IdP sharing + role membership + Keycloak group, records every step, visibility check passes on NTNX-01; first user, disable/enable, clean deletion |
| 2b | Tenant arrival, admin UI | done 2026-10-18 (write-up 12): admin UI over the API with the break-glass login (tenant list, create, detail with steps and visibility, users, disable/enable, delete), the 2a review minors absorbed, every `Authorization` scheme refused on the public name after measuring the console, Prism Central's Sign Out path allowed |
| 2c | Tenant arrival, tenant-facing pages | done 2026-10-18 (write-up 13): branding settings in the store, landing page that starts SAML itself, Keycloak theme, branded identity-provider-down page; a tenant user goes from the public URL to the console with one Keycloak login and no Nutanix login page |
| 2d | Setup wizard, settings and secrets at rest | done 2026-10-18 (write-up 14): settings in the store sealed with the master key; `portico settings import` and `settings show`; setup page with connectivity checks and pickers; Apply = controlled restart; the lab runs from the store with no secret file on the VM |
| 2e | Bootstrap and setup mode | done 2026-10-18 (write-up 15): a fresh appliance (process YAML, empty store) starts in setup mode and is configured card by card from the setup page; Portico creates the service account and its key in Prism Central, the Keycloak realm and clients, and the SAML identity provider from one-time credentials; the service account cannot create the IdP (`Project Manager_acp`), so that step asks for an administrator once; 41 s end to end on the lab |
| 2f | Minor sweep after 2d and 2e | done 2026-10-18 (write-up 16): the deferred minors of write-ups 14 and 15 closed with a test each (password policy on the realm Portico creates, login restarted once when its state expired, logo above the realm name, URL with credentials refused, store failures as 500, typed Keycloak errors, no start limit on the service); the older deferred findings live in `docs/backlog.md` |
| 3 | Appliance image | done 2026-10-20 (write-up 19): Rocky 9 qcow2 with CIS L1 report, console wizard, cloudflared option, imported and booted in NTNX-01 |
| 4 | Sites | several Prism Centrals behind one Portico, shown to tenants as sites on one public name (design `2026-10-20-sites-design.md`, ADR 0004); accepted on the appliance with one site (write-up 21), the two-site live test when a second pc.7.6 lab cluster is up |
| 5 | Backlog sweep | done 2026-10-25 (write-ups 27 and 28): the findings in `docs/backlog.md` that must not ship in a release closed with a test each |
| 6 | First release (step 1 complete) | done 2026-10-27 (write-ups 29, 30, 33): README, install guide, release artefacts, known limits documented; tag `v0.1.0` published, its image downloaded by Prism Central from GitHub and its installer run from the public release |

Step 2 (later): richer UI, MSP features, brownfield inventory with exclusion
list, multi-Prism Central, further services.

