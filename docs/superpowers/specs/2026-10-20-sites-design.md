# Portico — Sites: several Prism Centrals behind one Portico (2026-10-20)

Design agreed with Marco on 2026-10-20. It brings forward the "multiple
Prism Central instances" item that the main design
(`2026-10-17-portico-design.md`, section 2) deferred to step 2, and it
changes one decision of that design (section 4.10, "Host names"): see
"What changes in the main design" below. Implementation updates the main
design and records the change in `docs/adr/0004-sites-behind-one-host.md`.

## 1. Intent

An MSP rarely runs one datacenter. The MSP connects several Prism
Centrals to one Portico and presents each one to tenants as a **site**,
named like a cloud provider's availability zone (`AZ-IT-MI1`,
`AZ-US-NYC1`). A tenant signs in once, sees the sites where the MSP gave
it resources, and opens one with a click. The tenant never sees a Prism
Central address and never learns that there are several of them.

Why now, before the first release: a single-Prism Central product
undersells what Portico is for, and Marco wants the first release to show
the multi-site model.

Direction this work must not block (step 2, not built now): a Portico user
interface "in the style of a cloud provider" (VMs, their characteristics,
actions) that hides Nutanix entirely, chosen per tenant instead of the
Prism Central console. The site a tenant has chosen is therefore a Portico
concept, held by Portico, not by Prism Central's console.

## 2. Goals, non-goals, anti-goals

### Goals

1. **Sites in the setup.** The setup page shows a list of connected sites
   with "add" and "edit" instead of the single Prism Central card. Each
   site has a code, a display name, a Prism Central connection, its own
   service account and its own SAML identity provider, all created with
   the bootstrap flows that exist today (design 4.4, 4.5), run per site.
2. **Tenants placed on sites.** When the MSP creates a tenant, it chooses
   the sites where the tenant gets resources, and for each site the
   cluster and storage container. Portico creates the tenant's Prism
   Central objects (project, resource group, SAML user group, IdP sharing,
   role membership) on those sites only. A site can be added to or removed
   from an existing tenant later.
3. **One public host name for tenants.** All sites are served on the one
   existing Prism Central public name (`prism_host`). After the Keycloak
   login a tenant with more than one site sees a site page; with one site
   it goes straight to the console as today.
4. **Site page, variant B** (chosen by Marco on the mockup): the MSP's
   logo and branding as on today's landing page, one tile per site the
   tenant has a project in, each tile showing code, display name and
   status. A tile that is not available is visible, greyed out and not
   clickable.
5. **Switching sites** costs no password: the Keycloak session is shared,
   the SAML round trip on the new site is silent.
6. **Per-site visibility check**: the static check of design 4.4 runs on
   every site of the tenant; the tenant page shows the result per site.

### Non-goals (this work)

- Configuring disaster recovery (replication, protection policies,
  recovery plans). A DR site is a site like any other: the MSP gives the
  tenant resources there and Portico creates the project; the replication
  stays the MSP's job in Prism Central. Kept open in `docs/backlog.md`.
- The Portico-native cloud-provider interface (section 1, direction).
- Two sites open at the same time in one browser (section 4.4).
- Grouping sites by region on the site page (mockup variant C): later,
  when an MSP has many sites.
- A per-site public host name (the old 4.10 decision).

### Anti-goals

- A tenant reaching a site where it has no project, by any means
  (crafted request, stale cookie, second tab).
- A request reaching a Prism Central other than the one the tenant
  chose.
- Any weakening of the public-path rules of design 4.3 (deny list,
  fail closed, no local login) on any site.
- Portico copying Prism Central state: a site row holds connection data
  and identifiers, never inventory.

## 3. Constraints

- Every site runs pc.7.6 (design 3); the second lab site is the same
  version. The code keeps its fixed API versions (`iam/v4.1.b3`,
  `multidomain/v4.4.b1`) for every site; choosing API versions per site is
  out of this work. "Add site" reads the Prism Central version and refuses
  one older than pc.7.6, naming the version it found.
- Prism Central accepts one SAML identity provider (design 4.4): every
  site gets Portico's IdP pointing at the same Keycloak realm, with
  `redirectURL=https://<prism_host>`. A site that already has another IdP
  is refused, as today.
- Every site's Prism Central is reachable from Portico on 9440 over the
  internal network and never from the Internet (design 4.2). The firewall
  rule now names every site.
- Portico never stores a human administrator's credentials, on any site
  (design 4.5).
- The pair topology (design 4.11) must stay possible: the mechanism that
  remembers a tenant's site must not need state that only one node holds.
- The lab has one Prism Central today (NTNX-01). Marco is deploying a
  second Nutanix cluster with its own Prism Central for the live test;
  until then the two-site behaviour is tested against two fake Prism
  Centrals in the Go tests, and everything measurable on one Prism
  Central is measured on NTNX-01.

## 4. How it works (decided)

### 4.1 Site

A site is a row in Portico's store:

- `code`: what the tenant sees, chosen by the MSP (`AZ-IT-MI1`).
  Uppercase letters, digits and hyphens, 2 to 32 characters, unique.
- `name`: display text under the code (`Milan`).
- `order`: position on the site page.
- Prism Central connection: URL, TLS verification, CA, the service
  account's API key (sealed with the master key), SAML IdP name and
  extId, as the single `prism_central` block of today's settings.
- `maintenance`: a flag the MSP sets on the admin UI and API.

Site status, shown on the site page and on the admin UI:

- **Available**: Portico's last health probe to that Prism Central
  succeeded and the site is not in maintenance.
- **Maintenance**: the MSP's flag is set. The tile says so whatever the
  probe says.

What "maintenance" and "unreachable" do (proposed by me, accepted by
Marco on 2026-10-20): they stop **new entries** only. The tile is greyed out and not
clickable; a tenant whose only site is in that state gets a branded
"site in maintenance" (or "site unreachable") page instead of the
console; choosing that site is refused. Requests of a tenant already
working on the site keep being proxied: the MSP announces the
maintenance, sets the flag ahead of time so nobody new enters, and the
Prism Central upgrade itself ends the remaining sessions. Portico does not
cut a user off in the middle of a VM console.

**Who is still on a site** (Marco, 2026-10-20: after setting the flag the
MSP must know whether anyone is still connected before starting the
work). Every tenant request passes through Portico, so Portico records,
per Portico session, the site it is routed to and the time of its last
request. The admin UI's site page and the API
(`GET /api/v1/sites/{code}/sessions`) list them: tenant, user email,
since when on the site, last activity. Two levels:

- **Active**: a request in the last two minutes. An open console tab
  counts as active, because the console polls Prism Central in the
  background (measured in write-up 21: at least once a minute from a
  hidden Chrome tab); that is what the MSP needs to know, a tab still
  open is a person who may lose work. Prism Central signs such a tab
  out after about 15 minutes without input, through Portico's logout,
  so it leaves the count by itself.
- **Idle**: a valid Portico session on the site with no request for
  longer, until the session's idle limit (15 minutes) ends it.

The site page shows the count next to the maintenance switch ("3 users
still connected") and refreshes it. Next to it, **"Disconnect remaining
users"** (Marco's call, 2026-10-20: "just in case") ends the routing of those sessions
to that site: their next request lands on the maintenance page. It is
the hard cut, available only after the MSP has seen who would be cut.

Limits, written on the page: only access through Portico is counted
(administrators working on Prism Central directly on the management
network are not); in the pair (design 4.11) each node counts its own
sessions until the pair shares them.
- **Unreachable**: the last three probes failed in a row (amended after
  write-up 21: one slow answer from a Prism Central must not lock its
  tenants out; the first answer makes the site available again).

The probe uses the site's service account, at an interval and with a
timeout the builder chooses, and never blocks a tenant's request.

A site cannot be removed while any tenant has a project on it, with one
exception (builder's call): **forget**, for a Prism Central that is gone
for good. After an explicit confirmation naming the site, Portico deletes
its own records of that site and of the placements on it, makes no call
to that Prism Central, and lists the objects it leaves there (projects,
IdP and its sharing, SAML user groups, role memberships, service account
and key) in the log and on the response, for the MSP to clean up if the
Prism Central ever comes back.

The maintenance flag and the probe results take effect at once, without
Apply: they are runtime state, not settings that need a restart.

### 4.2 Tenant on sites

A tenant keeps one row (name, slug, Keycloak group, status, users) and
gains one **placement** per site: cluster, storage container, and the
Prism Central identifiers and step records that the tenant row holds
today (project `id` and `extId`, resource group, SAML user group, role
memberships, IdP sharing state, visibility result). The Keycloak group
stays one per tenant, so one login serves all its sites.

- Create: the steps of design 4.4 run per chosen site, idempotently, each
  recorded on its placement. A failure on one site leaves the others
  done; the tenant shows which site failed and a retry resumes there.
- Add a site later: the same steps on that site.
- Remove a site from a tenant: the deletion order of design 4.4 on that
  site (unshare before delete); the project stays unless the MSP asks to
  delete it; Portico never deletes VMs.
- Project and group names are the same on every site
  (`<prefix>-<slug>`, `tenant-<slug>`).
- Cluster and storage container move from the global tenant settings
  (`tenants.cluster_ext_id`, `tenants.storage_container_ext_id`) to the
  placement; the global values become the defaults offered for the first
  site.

### 4.3 Request flow with sites

1. Unchanged up to the Keycloak login (design 4.3 steps 1–3).
2. After the login Portico resolves the tenant from the user's Keycloak
   group and the tenant's placements.
   - No placement: an error page, no request to any Prism Central.
   - One placement: that site is selected and the flow continues as
     today (landing page, SAML start).
   - More than one: the site page (`/.portico/sites`).
3. The tenant clicks an available tile. Portico checks that the tenant
   has a placement on that site, records the choice (section 4.4) and
   sends the browser to `/`, which reaches that site's Prism Central; its
   login page is answered by the landing page and SAML starts on that
   site's IdP, as today.
4. Every later request on the public host goes to the Prism Central of
   the recorded site, after the deny list and the session check of design
   4.3. A request whose recorded site is missing, unknown, or not among
   the tenant's placements is not proxied: the browser goes to the site
   page, any other request gets 403.
5. The SAML response comes back as a POST to the ACS URL on `prism_host`;
   it follows the recorded site like any other request, so it reaches the
   Prism Central that started the login.
6. Prism Central's own Sign Out ends the Portico session as today, on
   every site.
7. A way back to the site page from inside the console is not possible
   without touching Prism Central's pages; the tenant opens the public
   address again (or `/.portico/sites`). The landing page and the site
   page carry a "switch site" link.

### 4.4 Remembering the site: one site at a time per browser

Marco's call: one public host name, the site held by Portico. Two
consequences, accepted:

- **One site at a time per browser.** The browser sends the same cookies
  to every tab, so all tabs on the public host talk to the last site
  chosen. A tab left open on the previous site starts failing (Prism
  Central answers for objects it does not know) until it is reloaded.
  This is a usability limit, not an isolation one: the user is the same
  person, authorised by each Prism Central for their own project only.
- **Prism Central's cookies of different sites share one origin.** Both
  Prism Centrals set cookies with the same names on `prism_host`, so they
  would overwrite each other. Portico keeps them apart.

Decided mechanism (builder fixes the details after measuring, section 6):

- The chosen site lives in the Portico session, next to the tenant the
  login resolved, and is checked against the tenant's placements on every
  request; the browser cannot choose a site the check refuses. Builder's
  ruling while planning (2026-10-20), replacing a separate
  `__Host-portico_site` cookie: "Disconnect remaining users" and the list
  of connected users need the site on the server anyway, and when the pair
  (design 4.11) turns the session into a shared encrypted cookie, the site
  travels inside it.
- Prism Central's cookies keep their own names for the **active** site,
  because the console's JavaScript may read some of them. On a switch,
  Portico renames the active site's cookies into a per-site stash
  (prefixed with the site) and restores the target site's stash, in the
  same response. Returning to a site whose Prism Central session is still
  valid therefore needs no new SAML round trip.
- Browser storage of the console (`localStorage`, IndexedDB) is shared
  by both sites on the same origin. If the measurement shows the console
  keeps site-specific data there, the switch response also sends
  `Clear-Site-Data: "storage"`.

### 4.5 Keycloak

One realm and one OIDC client for Portico, unchanged. The SAML side has a
known collision: Prism Central derives the SP entityID and the ACS URL
from the IdP's `redirectURL` only (measured, write-ups 04 and 15:
`https://<prism_host>/api/iam/authn` and `.../callback`), so every site
on the same `prism_host` produces the same entityID, and Keycloak refuses
two clients with the same `clientId` in one realm.

Builder's call, to confirm by measurement (section 6, item 3):

- **Preferred: one SAML client per site.** Each site's IdP is created
  with its own `entityIssuer` (the field exists in the v4 IdP model),
  for example `https://<prism_host>/api/iam/authn/<site-code>`, and that
  value is the Keycloak client id. An assertion issued for one site then
  carries an audience the other sites refuse.
- **Fallback: one SAML client shared by all sites**, if `entityIssuer` is
  not writable or Prism Central ignores the audience. An assertion issued
  for one site would also be accepted by another; isolation then rests on
  two barriers that remain: Portico routes only to sites in the tenant's
  placements (section 4.3), and each Prism Central authorises the user
  only inside the tenant's project there (no placement, no SAML user
  group, no access). The cost is losing a third barrier, not opening a
  path. Recorded in the write-up and in ADR 0004 if taken.

### 4.6 Admin side

- Setup page: the Prism Central card becomes the site list; "add site"
  runs the Prism Central bootstrap and the SAML step for that site. The
  Keycloak and public-name cards are unchanged. Adding or editing a site
  uses the existing Apply (controlled restart), refused while a tenant
  operation runs, as today.
- Tenant pages: create with site choice and per-site cluster and
  container pickers; tenant detail with one section per placement (steps,
  visibility, add/remove site).
- Every action is a REST call first (design 4.7, ADR 0003): sites CRUD,
  per-site bootstrap, maintenance flag, tenant placements add/remove.
  The API has no external users before the first release, so it changes
  without a compatibility layer.
- Internal pass-through (design 4.2): it serves one site, chosen in the
  settings (default: the first). Administrators reach the other Prism
  Centrals directly on the management network, which the design already
  allows; the pass-through is a convenience.

### 4.7 Upgrade of an existing appliance

A store with today's single `prism_central` block becomes one site at
start: code `SITE-1`, name taken from the Prism Central host, every tenant
gets a placement on it with the cluster and container in its row. The
MSP renames the code afterwards. The appliance on NTNX-01 (tenant `acme`)
is the test of this path.

## 5. What changes in the main design

- 4.10 "Host names": replaced. All sites share `prism_host`; the site is
  chosen on the site page and held in a Portico cookie. The selector
  sentence in 4.10's branding bullet becomes the site page of this
  document.
- 4.11 "Pair": "every public host name (one per Prism Central, section
  4.10, plus the identity host)" becomes "the two public host names".
- 2 "Explicitly deferred": "Multiple Prism Central instances" moves to
  done when this work lands; DR configuration and the native interface
  are listed instead.
- 4.4 tenant row: identifiers move to placements.
- 9 Milestones: this work is Milestone 4 (sites); the backlog sweep is
  Milestone 5; the first release `v0.1.0` becomes Milestone 6.

## 6. To measure before the code is final

On NTNX-01 now (one Prism Central), each a short write-up in `docs/lab/`:

1. Prism Central's cookies on the public host after a tenant login: names,
   `Path`, `HttpOnly`, lifetimes, and which ones the console's JavaScript
   reads (the stash must not break those).
2. What the console keeps in `localStorage` and IndexedDB, and whether it
   is specific to a Prism Central (cluster or entity identifiers).
3. Whether `entityIssuer` can be set on Portico's IdP (create and update),
   whether the SP metadata then carries it, and whether Prism Central
   refuses an assertion whose audience is another value (section 4.5).

On two Prism Centrals, when Marco's second cluster is up (the live test,
defined then):

4. Two IdPs with the same `redirectURL`, two Keycloak SAML clients, one
   realm: a tenant with placements on both sites logs in, switches,
   switches back without a new password, and sees only its project on
   each.
5. A tenant with one placement never reaches the other site, by tile,
   crafted cookie or crafted request.
6. The tab left on the previous site: what the user sees, and that a
   reload recovers.
7. On NTNX-01 now: the console's background polling interval with a tab open and the user
   idle, which sets the "active" window of section 4.1.

## 7. Left to the builder

- Schema of sites and placements, the migration of section 4.7, the
  shape of the REST endpoints.
- How Portico finds the user's tenant (group claim in the ID token or a
  Keycloak lookup), as long as it is checked on every request through
  the site cookie, not only at login.
- Cookie authentication and format; the exact stash naming; the probe
  interval and timeout.
- Admin UI layout of the site list and of the per-placement sections.
