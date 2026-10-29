# Portico — Sites usability: one-step sites, connecting tenants, signing out (2026-10-22)

Design agreed with Marco on 2026-10-22, after adding a second Prism Central
(`AZ-US-NYC1`) to the lab appliance (write-up 23, "Found on the way,
outside the branding"). It amends the sites design
(`2026-10-20-sites-design.md`, below "the sites design") and one rule of
the main design (`2026-10-17-portico-design.md`, 4.4). Section 5 lists what
changes in each; the reasoning goes in a new ADR (0006).

## 1. Intent

Adding the second Prism Central took Marco an afternoon of separate steps:
a site row, then a service-account form, then a SAML form asking for an
administrator's credentials again and calling them optional, a TLS
checkbox to clear for a factory certificate, cluster and storage container
defaults that stay empty until a check, an Apply and a restart that the
site page never mentions, and a tenant form at the bottom of another page
with free-text identifiers. The SAML card said "Configured" when Prism
Central had no trust at all.

Marco's expectation, in his words: no service accounts to configure by
hand, no hidden buttons, everything "one click". An MSP adds a site by giving its
address and an administrator's credentials once; Portico checks it may
work there, creates what it needs, and the site is live. Tenants are then
connected to sites from either side, suspended for maintenance without
loss, and disconnected only when nothing of theirs is left there.

Success: Marco repeats the NYC1 afternoon in a few minutes. He adds the
site on one page, connects `acme` from the site's page, a user of `acme`
signs in, switches between the two sites, signs out of one site's console
and lands on the site page, and no step needs Apply, a restart, a TLS
checkbox or an identifier typed by hand.

## 2. Goals, non-goals, anti-goals

### Goals

1. **Add a site on one page** (section 4.1): code, name, Prism Central
   address, the certificate confirmed when it is not trusted, an
   administrator's credentials once. Portico creates or reuses the
   service account, creates the SAML trust, picks cluster and storage
   container defaults, and the site is live without Apply. The result is
   a step-by-step summary.
2. **Status that tells the truth** (4.2): a site's SAML and service
   account state is read from Prism Central, not from what Portico
   stored.
3. **Certificate pinning** instead of switching verification off (4.3).
4. **Connect tenants and sites from both sides** (4.4): from the site's
   page and from the tenant's page, with cluster and storage container
   chosen from menus read from Prism Central.
5. **Suspend and resume** a tenant on one site (4.5): access closed, the
   tenant's VMs keep running, the same project comes back on resume.
6. **Disconnect only an empty project** (4.6); delete a tenant only when
   it is connected to no site.
7. **Signing out of a site's console with several sites returns to the
   site page**; with one site it signs out of everything (4.7).
8. **A slow Prism Central at sign-in shows a branded page with Retry**,
   not Prism Central's own provider page (4.8).

### Non-goals (this work)

- Deleting a tenant's VMs or other resources from Portico (a "danger
  zone"). Considered with Marco and kept out: the MSP empties a project in
  Prism Central with Prism Central's tools and logs. Listed in
  `docs/backlog.md` as an idea if MSPs ask for it.
- Adopting a SAML identity provider that someone else created in Prism
  Central (refused, as today: design 4.4).
- Revoking the service account's older API keys (a pair node may still
  use one; design 4.11).
- Connecting many tenants to a site in one action.
- A per-tenant maintenance window or schedule.
- Choosing API versions per site (sites design, section 3).

### Anti-goals

- An administrator's credentials stored, logged, kept across requests,
  or sent to a Prism Central whose certificate is neither trusted nor
  confirmed by the MSP.
- Any path in the admin UI that turns certificate verification off.
- Portico deleting, powering off or moving a VM, on any action.
- Disconnect touching a project that still holds anything.
- A suspended tenant opening anything new on that site's Prism Central
  through Portico, by any means (tile, crafted request, a session opened
  before the suspension). A VM console already open may finish (4.5).
- A status shown as "configured" or "ok" from Portico's records while
  Prism Central says otherwise.
- A half-created site presented as usable.

## 3. Constraints

- All rules of the main design hold: the public path fails closed, local
  Prism Central accounts never authenticate from outside, Portico works
  only through its `portico` service account, secrets at rest are sealed
  with the master key, which never leaves the appliance.
- Every site runs pc.7.6 or later; "add site" refuses an older version,
  naming the version found (sites design, section 3).
- Prism Central accepts one SAML identity provider (design 4.4).
- Every action is a REST call first; the admin UI calls the API (design
  4.7, ADR 0003). The API has no external users before the first release
  and changes without a compatibility layer.
- The pair topology (design 4.11) stays possible: nothing here may need
  state that only one node holds.
- The service account cannot manage SAML identity providers (write-up
  15): creating the trust needs an administrator's credentials. That is
  the reason Goal 1 asks for them, once.

## 4. How it works (decided)

### 4.1 Adding a site

One page, `Add a site`, in two steps on the same page.

**Step 1, the address.** Code, name, order (optional, default last) and
the Prism Central address. Portico connects and reads the certificate.
The Prism Central version is read at the start of step 2's run, with the
administrator's credentials and before anything is created: Portico
reads it from the clusters list, which needs them (Part C final review,
2026-10-24).

- The certificate is trusted (system roots, or a CA PEM the MSP pasted
  under "Advanced"): the page says so, naming the issuer.
- It is not trusted (the factory certificate, `CN=prism.nutanix.local`,
  no IP SAN): the page shows its SHA-256 fingerprint, subject, names and
  expiry, and says where to compare it (the browser's certificate viewer
  on the Prism Central address). The MSP confirms it; Portico pins it
  (4.3).
- Nothing answers: the page says what was found and stops. A version
  older than pc.7.6 stops step 2's run at its version step, before
  anything is created.

**Step 2, the administrator.** Username and password of a Prism Central
account allowed to create users, roles and identity providers. The fields
appear only after step 1 has a trusted or confirmed certificate. Why two
steps (my call): the credentials must not travel to a server Portico has
not authenticated, so they cannot be in the first request.

On submit, in this order, each step recorded for the summary:

1. **Check the permissions.** Read-only calls with the administrator's
   credentials that need the same rights as the next steps (list users,
   list identity providers), where pc.7.6 offers them (section 6, item
   2). The creations themselves remain the authoritative check (Marco: "or
   use the creation itself to check it"): steps run in an order where a
   refusal leaves nothing behind, and a refusal names the right that was
   missing.
2. **Service account.** Create `portico`, or reuse it when it exists as a
   service account (as today's bootstrap, `bootstrap_prism.go`), and
   issue a new API key; the account joins the project-manager policy. An
   existing non-service user of that name stops the run with "choose
   another name" (a field under "Advanced").
3. **SAML trust.** Create Portico's identity provider and the Keycloak
   SAML client, as today's SAML bootstrap. Another provider in Prism
   Central stops the run, as today. Portico's own provider already
   present with this site's `entityIssuer` (a site re-added after
   "forget") is reused.
4. **Defaults.** Cluster: the only AOS cluster, or with several, the
   first by name, shown in the summary to change. Storage container on
   that cluster: never a system container and never a Nutanix Objects
   one (section 6, item 3); `default-container-*` if present, else the one
   with the most free space. Both are stored, not left to a browser
   preselection.
5. **Live.** The site starts serving without Apply (4.9).
6. **Forget the credentials.** They lived in memory for this request
   only. The page shows the summary: one line per step, ok or the
   reason.

A failure stops the run at that step. What was done is kept and shown
(for example "service account created; SAML trust: refused, the account
lacks the right to manage identity providers"), the site is listed as
**incomplete** and is not served, and "Finish setup" on the site's page
runs the remaining steps again, asking for the credentials again. Every
step is idempotent, so running it twice is safe.

The run has one deadline (today's bootstrap uses 2 minutes; builder's
choice). The page shows progress while it runs.

**Editing a site** afterwards (its page): rename, reorder, change the
defaults from menus, re-pin a renewed certificate (4.3), "Finish setup"
or "Repair" with administrator credentials. The manual paths of today
(pasting an API key of a pre-created account, design 4.5 path 1; saving
a SAML provider name made by hand) stay in the API and under "Advanced"
on the page, never as the main path.

**Repair on a site in use** (my call, after the builder check). When the
SAML trust is gone from Prism Central and is created again, its id
changes, and every connected tenant's identity provider sharing and SAML
user group on that site point at the old one. Repair therefore recreates
the trust and then re-runs, for each tenant connected to the site, the
SAML steps of its connection (sharing, SAML user group, role membership),
which are the existing idempotent provisioning steps. The summary shows
one line per tenant; a tenant that fails shows Retry on its row.

**Removing a site** with no tenant connected (my call). Portico always
deletes its own side: the Keycloak SAML client of that site (a trust left
in Keycloak would stay live) and the site row. On the Prism Central side,
removal offers the administrator's credentials, as in 4.1: given, Portico
also deletes its identity provider there and the service account's key;
not given, it lists what it leaves there, like "forget" (sites design
4.1). "Forget" for a Prism Central that is gone is unchanged.

### 4.2 Status from Prism Central

A site's page shows, from Prism Central at the time of the check (the
site check and the probe):

- Connection: reachable, version, certificate (trusted or pinned, its
  expiry).
- Service account: the stored key authenticates.
- SAML trust: Portico's provider exists in Prism Central with this
  site's `entityIssuer`, and the Keycloak client exists.
- Defaults: the stored cluster and container still exist.

A stored name that Prism Central does not confirm is shown as missing,
with the action that fixes it. "Configured" is never derived from
Portico's own row.

What the status changes (my call):

- SAML trust or Keycloak client missing, or the service account's key
  refused: the site becomes **incomplete**. It takes no new entries (as
  "unreachable" in the sites design 4.1: greyed tile, choosing it
  refused, the branded "not available" page, not the "responding slowly"
  one of 4.8) and Connect to it is refused, until Repair. The probe checks
  the key on every round, as today; the trust and the client are checked
  at a longer interval of the builder's choice and on every site check.
- A default cluster or container that no longer exists: a warning on the
  site page; the site keeps serving; Connect asks for a choice instead of
  preselecting it.

### 4.3 Certificates

A site trusts its Prism Central in one of two ways:

- **Verified**: system roots or the pasted CA, host name checked, as
  today.
- **Pinned**: the SHA-256 of the leaf certificate's DER, confirmed by the
  MSP. A connection is accepted when the presented leaf has that
  fingerprint, whatever its names or issuer. Why the leaf and not the
  public key (my call): it is the value a browser's certificate viewer
  shows, so the MSP can compare it.

When Prism Central renews its certificate, a pinned site turns
unreachable with "certificate changed" on its page, showing the new
fingerprint for the MSP to confirm. Nothing reconnects by itself.

The UI no longer offers "verify off". A site stored with verification
off (the lab's NYC1) keeps working after the upgrade; its page shows a
warning and "Pin the current certificate". The API keeps the field for
that migration only and refuses to set it to off on new sites.

### 4.4 Connecting tenants and sites

Wording: **connect** and **disconnect** (Marco: "connect tenant to this
site").

- Site page: "Tenants on this site", one row per tenant (cluster,
  storage container, status, actions), and **Connect a tenant to this
  site**: a menu of active tenants not yet there, cluster and storage
  container menus with the site's defaults preselected and labelled
  "site default".
- Tenant page: "Sites", one row per site, and **Connect this tenant to a
  site**, the same form with a site menu.
- Both forms call the same API action. Cluster and container menus are
  read from that site's Prism Central; no identifier is typed.
- Creating a tenant (my call) keeps the choice of sites, with the same
  menus, and keeps placing it on the only site when there is one. Choosing
  no site is now allowed: the tenant is created connected to no site and
  is connected later from either page.
- A failed connection shows its reason and **Retry** on its own row,
  retrying that site only. The "Retry provisioning" button at the top of
  the tenant page goes.
- Rows have room to breathe: one field per line in the forms, taller
  table rows (Marco: "more air between the rows"; mockup v3 of the
  brainstorm).

### 4.5 Suspend and resume

For maintenance on one tenant's site (Marco's first scenario: painless,
everything comes back as it was).

- **Suspend** on a row sets the connection to **suspended**. Portico stops
  routing that tenant to that site: the tile on the site page is greyed
  out with "Not available", choosing it is refused, and a session already
  on that site gets the site page (several sites) or a branded "not
  available" page (one site) on its next request. Before confirming, the
  page shows how many of the tenant's users are on that site now (the
  session list of sites design 4.1).
- In Prism Central nothing changes: project, group, role membership and
  VMs stay as they are, and **the VMs keep running** (Marco). Nothing
  outside Portico reaches Prism Central on 9440 from the Internet
  (design 4.2), so closing Portico's route closes the tenant's access.
- **Resume** sets the connection back to active; the same project
  serves again, nothing is re-created.
- A VM console already open (a WebSocket connection) is not cut: it
  ends when the user closes it or when Prism Central ends the session
  (Marco, 2026-10-22: suspend is for maintenance; the same rule as the
  site's maintenance, sites design 4.1: Portico does not cut a person off
  in the middle of a console). Every new request, a new console included,
  is refused at once.
- Suspend is per tenant and site. The site's maintenance flag (sites
  design 4.1, stops new entries for everyone) is unchanged and separate.
- **"Several sites"** (my call, used by the routing after login, by the
  sign-out of 4.7 and by the page a suspended session gets): the tenant
  has more than one connection that is active or suspended. Those are the
  tiles of the site page; a suspended one is greyed. Connections being
  created, failed or being disconnected are not tiles and do not count.
  A tenant with one active and one suspended connection therefore sees
  the site page, and its sign-out returns there.
- Tenant disable (all sites, Keycloak users disabled) is unchanged.

### 4.6 Disconnect and delete

For a tenant leaving a site (Marco: you cannot disconnect the tenant unless
it is empty, and emptying it is not Portico's problem).

- **Disconnect** first lists what the tenant's project holds on that site
  (section 6, item 1). If anything is there, Disconnect is refused: the
  page names what is left, with a link to the project in Prism Central.
- What counts (my call): what belongs to the project and was made in it,
  VMs, images, volume groups, recovery points and every other kind the
  measurement finds. What the MSP shares into the project (subnets,
  categories) or what Portico itself created there (resource group,
  role memberships, SAML user group) does not count; Disconnect removes
  Portico's objects and leaves the MSP's shared ones alone. Since 0.2.0
  that includes the ready network, removed first and in reverse order:
  floating IPs, subnet, VPC, the sharing, the overlay external subnet; a
  floating IP still associated with a VM refuses the Disconnect and is
  named (`2026-10-29-tenant-network-design.md` 4.4).
- A kind Portico cannot count with the service account (my call): an
  unknown is not an empty. If the measurement shows Prism Central itself
  refuses to delete a project that still holds something, that refusal
  is the safety net: Portico counts what it can, deletes, and reports
  Prism Central's refusal as "the project still holds something". If
  Prism Central does not refuse, Disconnect asks for an administrator's
  credentials for the count, as in 4.1. Either way Disconnect, and with
  it Delete tenant, can always be completed.
- With an empty project, after a confirmation naming tenant and site,
  Portico removes the role memberships, unshares the identity provider,
  deletes the SAML user group, the resource group and the project, then
  the connection. The "delete the project too" checkbox goes: an empty
  project has nothing to keep.
- The rule "a tenant keeps at least one site" goes: a tenant can be
  connected to no site. Its users then get the existing "no site" page.
- **Delete tenant** is possible only when the tenant is connected to no
  site; it deletes the Keycloak group, its users and the tenant row.
  Today's delete, which removes every placement and keeps the projects,
  goes.
- Portico never deletes, powers off or moves a VM (main design 4.4,
  unchanged).

### 4.7 Signing out

Prism Central's Sign Out reaches Portico at `/redirect?logout` (as
today).

- **Several sites** (Marco's proposal): it ends that site only. Portico
  expires that site's Prism Central cookies and their stash, clears the
  chosen site in the Portico session and sends the browser to the site
  page. The Portico and Keycloak sessions stay; another site opens with
  no password; the site page's Sign out ends everything.
- **One site**: it ends everything, as today (Portico session, every
  cookie, Keycloak's end-session).
- Prism Central's idle sign-out (about 15 minutes, write-up 21) arrives
  the same way, so with several sites it too ends at the site page. The
  site page, which sends no requests, signs out of everything when left
  open a minute less than the Portico session's idle limit: an
  unattended browser ends with no session, as with one site (builder's
  call from the branch review).
- Keycloak's SAML logout towards Prism Central fails today because the
  Keycloak container cannot resolve the public Prism Central name. A full
  sign-out must end without an error page and without a Prism Central
  session left usable. The builder chooses the fix after measuring
  (section 6, item 4): make the name resolvable from the container, or
  stop Keycloak from attempting a logout that the cookie expiry already
  makes unnecessary. Chosen (write-up 24): the second. The failing call
  was a SAML backchannel logout to the client's admin URL; a resolvable
  name would only reach Portico's public listener without a session and
  be refused. Portico's SAML clients carry no admin URL.

### 4.8 A slow Prism Central at sign-in

Today any failure of the SAML start (session cookie, identity provider
lookup, login request) sends the tenant to Prism Central's own provider
page, which reads "Authentication providers not registered yet".

- Instead, Portico shows a branded page: "This site is responding
  slowly", a **Retry** link to the public address `/` (it restarts the
  flow with a new login request; the SAML start alone would fail again
  on the same request), and with several sites a link to the site page.
  The reason is logged, as today.
- The identity provider's id is stored at site setup, so the sign-in no
  longer lists providers on every cache miss (builder's call if a
  measurement shows the id can change).

### 4.9 Sites change without a restart

Today every site's client, upstream, probe and landing backend is built
once at start, so any site change needs Apply and a restart. For a site
to go live at the end of 4.1, site changes take effect at once:

- Adding, editing, suspending, forgetting a site and changing its
  defaults rebuild that site's backends and swap them in; requests in
  flight finish on the old ones.
- Apply and restart remain for what is read at start outside the sites
  (Keycloak, public names, listeners).
- In the pair (design 4.11) each node picks up the change from the shared
  store; the builder states how quickly.

## 5. What changes in the other documents

- Main design 4.4: "deleted (project left in place unless explicitly
  chosen; Portico never deletes VMs)" becomes: disconnect deletes an
  empty project and refuses a non-empty one; deleting a tenant needs no
  sites left; Portico never deletes VMs.
- Main design 4.5: the bootstrap path is the main path, per site, in the
  one-page flow of 4.1; the pre-created path stays under "Advanced".
- Sites design 4.1: site states gain **incomplete** and, for pinned
  sites, "certificate changed"; site changes need no Apply; removing a
  site deletes Portico's Keycloak client and, with an administrator's
  credentials, its Prism Central objects (4.1).
- Sites design 4.3 step 2: "one placement" and "more than one" follow the
  "several sites" rule of 4.5.
- Sites design 4.2: placements gain **suspended**; "remove a site" becomes
  disconnect of an empty project; the last-site rule goes.
- Sites design 4.3 items 6 and 7: the sign-out of 4.7 replaces them.
- Sites design 4.6: "adding or editing a site uses the existing Apply"
  goes; the tenant and site pages are those of 4.4.
- ADR 0006: site changes at runtime, certificate pinning, disconnect only
  when empty, no deletion of resources by Portico.

## 6. To measure before the code is final

On the lab (NTNX-01 and the NYC1 Prism Central), a write-up in
`docs/lab/`:

1. What a Projects 2.0 project can hold on pc.7.6 (VMs, images, volume
   groups, subnets, categories, recovery points, …), how to list each by
   project with v4, and whether Prism Central refuses to delete a project
   that still holds something.
2. Whether an administrator's account can read its own rights through
   the API, for the pre-check of 4.1 step 1.
3. How the v4 clusters API tells a Nutanix Objects storage container
   apart (a flag, a name pattern), on the NTNX-01 container that became
   the default.
4. Keycloak's SAML logout towards Prism Central: what it calls, why it
   fails in the container, and whether Prism Central's own Sign Out has
   already ended its session when it redirects to `/redirect?logout`.
5. Where Prism Central shows its certificate's fingerprint, if anywhere,
   for the text of 4.1 step 1.

## 7. Left to the builder

- API shape of the one-page flow (one call per step or one call for the
  run), and how the page shows progress.
- How site backends are rebuilt and swapped (4.9), and the pair's delay.
- The order of the permission checks and creations in 4.1, within the
  rule "a refusal leaves nothing behind".
- Store migration for the new placement state and the site states.
- Exact texts of the pages, in the style of the existing pages.
