# Portico — Tenant networking: a ready network on every site (2026-10-29)

Status: approved by Marco on 2026-10-29 (PR #58), then revised the same
evening with his answers after the transit and tenant measurements: the
transit model of NVD-2219 with one overlay external subnet per tenant is
the only one, its addresses routed by the MSP so one floating IP
publishes a VM, and a Portico Tenant role keeps the tenant from creating
floating IPs. Measurements: write-up 34. Target release: 0.2.0.

## 1. Intent

Today a tenant connected to a site gets a project, a group and a role,
and nothing to plug a VM into: the MSP shares a subnet into the project
by hand in Prism Central, or the tenant cannot run anything. In 0.2.0
connecting a tenant to a site also gives it a **ready network** there:
its own VPC, one internal subnet with addresses served by Prism
Central, Internet access through NAT, and floating IPs for what the
tenant publishes. The tenant creates a VM, picks its subnet, and the VM
works.

The model is the one Nutanix's SP Central uses and its validated design
(NVD-2219) recommends: one VPC per tenant on Flow Virtual Networking,
attached to an external network the MSP owns, NAT towards the outside,
overlapping tenant address spaces allowed. An MSP that later adopts SP
Central finds tenants built from the same pieces.

## 2. Goals, non-goals, anti-goals

### Goals

1. **Site readiness.** For each site Portico reads, and shows on the
   site's page, whether the ready network can be offered there: Flow
   Virtual Networking enabled with its controller up, the Integrated
   mode, a transit VPC of the MSP picked for tenants, a Prism Central that
   reaches the Internet through its subnet's router, the service
   account's networking rights. Each missing item says what to do.
2. **Ready network at connection.** On a site whose ready network is on,
   connecting a tenant (from either page, and at tenant creation) builds
   it after the project: the tenant's own external subnet in the
   transit VPC, shared with the project, the VPC, the internal subnet,
   the floating IPs. Each step is recorded
   and retried like the existing connection steps.
3. **One address plan.** The MSP sets one tenant address pool (for
   example `10.100.0.0/16`); each tenant gets a `/24` from it by its
   tenant number, the same on every site, so a VM keeps its addressing
   when it moves between sites. A tenant may have an exception when the
   default collides with the customer's own network.
4. **Disconnect and delete.** Disconnect removes what Portico built for
   the ready network, in reverse order, whatever the other tenants on the
   site hold. It counts the tenant's floating IPs, which 0.1.0 could not.
5. **Existing tenants.** After an upgrade, a connection made before the
   ready network shows "no ready network" and offers to create it.

### Non-goals (0.2.0)

- Enabling Flow, choosing its mode, creating the external network or a
  transit VPC: platform choices the MSP makes in Prism Central, as with
  SP Central. Portico reads them.
- Tenant-defined networks: more subnets, routing, VPN, BGP, no-NAT
  external networks per tenant (NVD's partial transit model). The tenant
  uses Prism Central for anything beyond the ready network, as far as
  its role allows.
- Security policies (Flow Network Security) inside the tenant's VPC.
- Subnet stretch between sites and cross-cluster live migration: an
  optional per-tenant feature later (backlog, write-up 34).
- Quotas on VPCs or subnets: the tenant's role lets it create them in
  its project, as in Prism Central today. Floating IPs are the exception
  (4.3): they spend the MSP's public addresses.

### Anti-goals

- Portico creating or changing anything outside the tenant's own objects:
  in the MSP's transit VPC only the tenant's external subnet; no change
  to the transit VPC itself, to its VLAN external subnet, to the MSP's
  routers, to `MGMT`, to another tenant.
- A ready network that silently differs between sites (another `/24`,
  another name) without the page saying so.
- Granting the service account more than the networking operations it
  needs, or on more subnets than the picked external networks.
- Deleting a VM, a VM's NIC, or a floating IP the tenant made itself.

## 3. Constraints

- Prism Central pc.7.6 or later, networking API `v4.4` (measured on
  7.6.0.6: `v4.0` to `v4.4` answer, `v4.5` is 404).
- Flow Virtual Networking in the Integrated mode only, as SP Central
  supports today. A site in the Standalone mode is not offered the
  ready network until measured.
- The ready network is per site; a site without it keeps working as in
  0.1.0.
- Every object Portico creates carries the tenant's `projectExtId` and a
  name derived from the project: `<project>-vpc`, `<project>-net`,
  `<project>-fip-<n>`; the tenant's subnet in the transit VPC carries
  the same prefix (`<project>-ext`) and no project.
- Prism Central assigns the VPC's SNAT addresses and the floating IPs
  from the external network's pool in no fixed order (measured): Portico
  reads them back, never predicts them.
- The service account works with an API key; the one-time
  administrator credentials of a site's setup are never stored (main
  design 4.5).

## 4. How it works (decided)

### 4.1 Site readiness

A **Networking** card on the site's page, between Connection and
Tenants. Portico reads, each time the page opens and in the site
monitor at its own pace:

| check | read from | when it fails |
|---|---|---|
| Flow Virtual Networking, controller `UP` | `GET /api/networking/v4.4/config/controllers` | "Enable Flow Virtual Networking in Prism Central (Integrated mode)" |
| Integrated mode | read through the API if section 6 item 4 finds a way; otherwise inferred from the controller running on Prism Central (the measured signs of write-up 34) and confirmed once by the MSP in the card (my call) | "This site runs the Standalone mode; Portico supports the Integrated mode" |
| Prism Central's gateway | `GET /api/prism/v4.2/config/domain-managers`: the gateway must be inside Prism Central's subnet and not one of its cluster's own addresses (VIP, Data Services IP, CVM, host) | "Prism Central's gateway is X, an address of the cluster; Flow cannot download its packages" |
| transit VPC picked | Portico's site row | "Pick the transit VPC tenants use" |
| transit VPC valid | `GET .../vpcs/{extId}`: type `TRANSIT`, a no-NAT VLAN external subnet, a `0.0.0.0/0` route to it, the routed prefix among its externally routable prefixes | names what is wrong |
| networking rights | the service account's policy (4.5) | "Repair" |
| pool | the routed prefix's `/28`s against the tenants that hold one | a warning under 20 % free, not a failure |

The MSP **picks the transit VPC** in the card from a menu of the site's
VPCs of type `TRANSIT`, shown with their VLAN external subnet, address,
routed prefix and free count. The MSP builds it once per site, as in
NVD-2219's shared transit VPC with NAT model (install guide): a no-NAT
VLAN external subnet, a transit VPC on it with one fixed address, its
default route, and a **routed prefix** of the MSP's address space as its
externally routable prefix; the MSP's router sends that prefix to the
transit VPC's address, by a static route or through a BGP gateway. The
NAT happens in each tenant's subnet of the transit VPC, so the transit
VPC itself needs no NAT external subnet, and a tenant's floating IP is
an address of the routed prefix, reachable as it is (measured on
NTNX-01 with a static route, write-up 34).

Only this model is offered (Marco, 2026-10-29, after measuring). The
first approval accepted a NAT VLAN external subnet attached directly as
well, but two tenants on one shared external subnet cannot be
disconnected one at a time: Prism Central refuses to unshare a subnet
while any VPC uses it, and then refuses to delete the project
(write-up 34). In the transit model Portico gives each tenant its own
overlay external subnet in the transit VPC, so a tenant's Disconnect
touches nothing the others use (measured). The direct model would need
one VLAN external subnet per tenant, a VLAN or a pool slice each, which
a small MSP does not want to manage. A cluster accepts one managed
subnet per VLAN and virtual switch (measured), so the no-NAT VLAN of
the transit VPC is a VLAN of its own, or replaces a NAT external subnet
on the same VLAN.

A first measurement put the transit VPC on a NAT VLAN external subnet
with tenant subnets in `100.64.0.0/16`: it works, but every published VM
then takes a second floating IP on the transit VPC. That double NAT is
not in the NVD and is not offered.

The MSP also enters the **DNS servers** for tenant subnets (default:
the ones Prism Central uses). A switch **Ready network for new
connections** is off until every check passes; turning it on does not
touch existing connections.

Choosing another transit VPC later applies to new connections only;
existing ready networks keep theirs. Portico does not move a VPC from
one external subnet to another.

One transit VPC per site (my call for 0.2.0, the lab has one cluster
per site): a connection whose cluster is not the transit VPC's cluster
gets no ready network, and its row says why. A site
with several clusters is measured before it is offered (section 6).

### 4.2 Address plan

- **MSP setting** (Settings, "Tenant networks"): the tenant address pool,
  a private IPv4 prefix between `/8` and `/20`, default `10.100.0.0/16`.
  It can be changed only while no built network uses one of its `/24`s;
  a change moves the tenants' unused pool `/24`s into the new pool
  (Marco, 2026-10-30, ADR 0008: fixing it at the first assigned subnet
  locked it at the upgrade, before the MSP saw it; a second pool is a
  later feature if one runs out).
- **Tenant subnet, stored** (my call, after the builder check): the
  tenant row stores its `/24` once assigned, and every site uses the
  stored value; nothing is recomputed from a formula afterwards. A tenant
  gets its subnet at creation: the lowest `/24` of the pool that no
  tenant row holds, exceptions included (an exception inside the pool
  occupies its slot). A subnet is never reused while its tenant row
  exists. Existing tenants get theirs at the upgrade, in creation order.
  Gateway `.1`, DHCP pool `.10` to `.250`, the site's DNS servers. The
  same on every site.
- **Routed prefix** (per site, read from the transit VPC's externally
  routable prefixes, my call): each tenant gets a `/28` of it on that
  site, the lowest free one, stored on the connection: gateway the first
  address, pool the next ones up to the one before broadcast (13
  addresses). The tenant VPC takes one of them for SNAT and each
  floating IP one (measured), so a `/28` holds the SNAT address and up
  to 12 floating IPs, the largest allowance (4.3). Unlike the tenant subnet it differs between sites: it is the
  MSP's public or routed space of that site.
- **Exception**: in the tenant creation form (under "Advanced", with the
  floating IP allowance, my call) and on the tenant page (and
  `PUT /api/v1/tenants/{id}/subnet`) until its first ready network's
  subnet exists, the MSP may set another `/24` (any private prefix,
  checked not to overlap another tenant's subnet at once, and a site's
  routed prefix or external network when the network is built). After that the subnet is fixed: changing it means
  re-creating the tenant's networks, which Portico does not do.

### 4.3 Building the ready network

When a tenant is connected to a site whose ready network is on, the
connection steps after the project (main design 4.4) gain, each
idempotent by name and recorded like the others:

1. **external-subnet**: `<project>-ext`, an overlay external subnet
   with NAT (`isExternal`, `isNatEnabled`) in the transit VPC, the
   tenant's transit `/24`.
2. **share-external**: `POST .../subnets/{ext}/$actions/share` with the
   project (skipped when already shared). Without it the VPC is refused
   (`NETWORKING-10056`, measured).
3. **vpc**: `<project>-vpc`, type `REGULAR`, in the project, attached
   to `<project>-ext`, the DNS servers in the common DHCP options.
4. **default-route**: a static route `0.0.0.0/0` with next hop
   `<project>-ext` in the VPC's route table. Measured: a VPC created
   through the API has only its local routes, and without this one
   nothing leaves it (no SNAT, no floating IP reply).
5. **subnet**: `<project>-net`, overlay, in the VPC and the project,
   the tenant's `/24`.
6. **floating-ips**: as many as the tenant's allowance (default 1, set
   on the tenant page, 0 to 12, what a `/28` holds besides the VPC's
   SNAT address), unassociated, in the project, from `<project>-ext`;
   each is an address of the routed prefix, reachable from outside once
   the tenant associates it with a VM.
7. **read-back**: the VPC's SNAT address and the floating IPs, stored
   on the connection and shown on the tenant page, and each object's
   `projectExtId`. Measured: one subnet create out of three gave a subnet
   without its project although the request carried it, and a VM of the
   project then cannot use it ("subnet not found"); Portico deletes and
   re-creates such an object once, then fails the step.

Measured as admin: each step one call and one task, 27 to 30 s for the
direct model's five; a VM on the subnet gets its address by DHCP,
reaches the Internet through its subnet's NAT and is reached on its
floating IP through the MSP's route (write-up 34).

The ready network has a **state of its own** on the connection (my
call, after the builder check): `none`, `building`, `ready`, `failed`.
Its steps run after the connection's existing steps, visibility check
included, so the connection becomes **active** and the tenant signs in
exactly as in 0.1.0; a ready network still building or failed never
takes the site away from the tenant. The connection's row shows the
network's state; a failed step shows its reason and **Retry network**,
which runs the network steps only. "Create ready network" on a
connection whose state is `none` (an existing tenant after the upgrade,
or a site whose switch was off at connection time) runs the same steps.

What Portico created is stored on the connection by `extId`, not found
again by name: Disconnect, Retry and the monitor use the stored
`extId`s. The site monitor reads them at its own pace; one the tenant
deleted or changed in Prism Central (its role may allow it) turns the
state to `failed` with "deleted in Prism Central" or "changed in Prism
Central", and Retry re-creates what is missing. Portico does not undo a
tenant's change to an object that still exists (my call).

Changing the allowance later creates or deletes unassociated floating
IPs to match; Portico never deletes one that is associated to a VM and
says so instead. Prism Central itself deletes an associated floating IP
without refusing (measured), so Portico reads the association first.

The tenant associates a floating IP with a VM in Prism Central.
Measured with a local Project Admin of one project: it can, and it can
also create and delete floating IPs of its own on the shared subnet, so
the allowance would be no limit. Portico therefore creates on each
site a role **Portico Tenant**, `Project Admin`'s operations without
`Create_Floating_Ip` and `Delete_Floating_Ip`, in the same setup run as
4.5, and maps the tenant groups to it instead of `Project Admin`
(Marco, 2026-10-29); existing role memberships move to it at the
connection's next Retry or Repair.

### 4.4 Disconnect and delete

Disconnect (sites usability design 4.6) counts, as before, what the
project holds; the VPC, the subnet and the floating IPs Portico created,
recognised by their stored `extId`s, do not count, but a VM with a NIC on the tenant's subnet does (it is a
VM). A floating IP the tenant created itself counts. With nothing left,
Portico deletes in reverse: Portico's floating IPs (first checking that none is associated), the subnet, the VPC (its
route goes with it), `unshare` and delete of `<project>-ext`, then the
project's objects as today. Measured with two tenants on one site: the
first one's Disconnect succeeds while the second keeps its ready
network, because no other VPC uses `<project>-ext`. The service account can now list floating IPs (4.5), which
removes the 0.1.0 known limit "Disconnect does not count floating IPs".

### 4.5 The service account's networking rights

Measured (write-up 34): `Project Manager_acp` lets the service account
create VPCs and subnets in a project, but not share the external
network (it belongs to the system project `_internal`) nor view or
manage floating IPs.

Portico therefore creates, in the same site setup run that creates the
service account (main design 4.5, one-time administrator credentials,
never stored):

- a role **Portico Networking** with `Share_Subnet`, `Unshare_Subnet`,
  `View_Floating_Ip`, `Create_Floating_Ip`, `Update_Floating_Ip`,
  `Delete_Floating_Ip`, the route table operations `View_VPC_Route_Table`,
  `View_Route_Table_Route`, `Create_Route_Table_Route`,
  `Delete_Route_Table_Route` (measured: `Project Manager_acp` cannot read
  the VPC's route table, `NETWORKING-10071`), and `View_Subnet`,
  `Create_Subnet`, `Delete_Subnet` for `<project>-ext` in the transit
  VPC, which belongs to no tenant project (section 6 item 1);
- an authorization policy **Portico Networking_portico** binding it to
  the service account, the subnet operations limited to the subnets of
  the picked transit VPCs, by a filter Prism Central evaluates by itself
  (section 6 item 1), since the tenants' external subnets are created at
  each Connect. The floating IP operations (Marco,
  2026-10-29): limited to the tenants' projects by a filter Prism
  Central evaluates by itself (a project or category attribute, section
  6 item 1), so a new connection needs no credentials; if no such filter
  works, on every floating IP of the site. A fixed list of projects is
  not used: it would take the one-time credentials at every Connect.

If the filter has to name the transit VPCs, the policy holds every
transit VPC picked on the site and still used by a ready network, so
Disconnect keeps working after the MSP picks another one; a VPC leaves
the list when no ready network uses it. Changing the list needs the
one-time credentials (the service account cannot edit policies): the Networking
card's "Save" asks for them in the same form when the pick changes the
policy (my call), so a new site takes them twice, at "Add a site" and at
the first pick, unless the MSP picks the transit VPC in the "Add a
site" run itself, where the menu is offered once the site's subnets can
be read. An existing site
gets the role from its "Repair" after the upgrade; until then its
networking check fails with that instruction. The pre-created path
(Advanced) documents the same role for an MSP that prefers to create it.

`lab/auth/networking-role.sh` creates exactly this role and policy for
the measurement.

### 4.6 What the tenant sees

Nothing new in Portico's pages: the tenant reaches Prism Central as
today, where its project now holds the VPC, the subnet and the
floating IPs. Portico's tenant-facing site page may show, per site, the
tenant's subnet and floating IPs (my call: no, in 0.2.0; the admin's
tenant page shows them, and the MSP tells the tenant).

## 5. What changes in the other documents

- Main design 4.4: the connection steps gain those of 4.3; the tenant
  row gains its number, its subnet exception and its floating IP
  allowance; a connection stores its ready network's objects.
- Main design 4.5: the service account gets the networking role beside
  `Project Manager_acp`.
- Main design 2, "Explicitly deferred": networks per tenant move from
  deferred to 0.2.0 for the ready network; the rest stays deferred.
- Sites usability design 4.6: Disconnect removes the ready network.
- Install guide: the Prism Central prerequisites of write-up 34's table
  (Flow, Integrated mode, Prism Central's gateway and capacity) and how
  to build the transit VPC: a no-NAT VLAN external subnet, the transit
  VPC with its fixed address, default route and routed prefix, and the
  route of that prefix on the MSP's router.
- Release notes 0.1.0's known limit on floating IPs is lifted in 0.2.0.

## 6. To measure before the code is final

1. The networking role of 4.5 as the service account
   (`lab/auth/networking-role.sh` then `lab/network/transit.sh` and
   `lab/network/tenant-network.sh` with the key): enough for the seven
   steps and their removal, `<project>-ext` included? Which entity filter scopes the subnet part to the transit
   VPC's subnets and the floating IP part to the tenants' projects and
   the transit VPC without listing them (a VPC or project attribute, or
   a category Portico puts on what it creates)?
2. Measured: the transit model with one overlay external subnet per
   tenant, two tenants, the first one's Disconnect with the second
   active, on a NAT transit VPC (write-up 34); the routed form of the
   NVD with two tenants, inbound through a static route, and the
   second one's Disconnect with the first active. To measure: its
   Internet access through the MSP's router.
3. Measured for `Project Admin` (a local user of one project): VM on
   `<project>-net`, floating IP association, and floating IPs of its
   own. To measure: the **Portico Tenant** role (Project Admin without
   `Create_Floating_Ip` and `Delete_Floating_Ip`) still creates the VM
   and associates the floating IP, and is refused a new floating IP;
   then the same through Portico with a Keycloak user.
4. How to read the Flow mode through the API. Measured: the
   controller's API has no such field, so the inference and the MSP's
   confirmation of 4.1 apply unless another API shows it.
5. Measured: the gateway check of 4.1 reads from `domain-managers` and
   `clusters`; NYC1's old `.183` is its cluster's Data Services IP and
   would have been caught, `.254` passes on both sites.
6. Measured: with a VM on the subnet, Prism Central refuses to delete
   the subnet and the VPC and to unshare the external network
   (`NETWORKING-10068`), but deletes an associated floating IP.
7. A site with two clusters: can a tenant VPC on one cluster use the
   external network of the other, or does each cluster need its own?

## 7. Left to the builder

- Package layout (a `networking` part of the Nutanix client, the steps
  in the tenant service), the store schema for the new fields and its
  migration.
- How often the site monitor reads the networking checks.
- The exact wording of the card and the messages, following the
  existing cards.
- The order of the new settings in the admin UI.
