# Portico — Routing the routed prefix: BGP peering and the edge router (2026-11-01)

Status: approved by Marco on 2026-11-01, written after the BGP gateway measurement of write-up 37
and Marco's decisions of 2026-11-01 (the peer as the requirement, the
edge router as the way out, no NAT, Portico builds it, the four answers
of sections 4.1, 4.3 and 4.5). Target release: after the November article; version to
be set in the plan.

## 1. Intent

A site's ready network (design of 2026-10-29) ends at the transit VPC's
external address: tenant floating IPs are addresses of the **routed
prefix**, and the MSP's network has to send that prefix to the transit
VPC. Today the install guide says "static, or through a BGP gateway" and
leaves the MSP alone with its router; the Networking card cannot tell
whether the prefix is routed at all. Write-up 34 measured the static
route, write-up 37 the Nutanix way: a Network Gateway VM with the BGP
service for the transit VPC advertises the prefix to an eBGP peer with
the transit VPC's address as next hop, so the router follows the prefix
as it changes and Portico can read the session's state.

This design makes the eBGP peering the requirement and Portico's job on
the Nutanix side, and gives the MSP that cannot peer on its own router a
way out that Portico builds and owns: a small **edge router** VM on the
VLAN that peers with the gateway, with one static route on the MSP's core
switch towards it. Not NAT: a prefix that is only NATed cannot be reached
from inside the MSP's network (a backup NAS, a monitoring host) nor
published from the site's firewall, so the prefix must be routed, one way
or the other.

## 2. Goals, non-goals, anti-goals

### Goals

1. **The Nutanix side is Portico's.** For a site with the ready network
   on, Portico creates and keeps the local BGP gateway of the transit
   VPC, the remote gateway that stands for the peer, and the session
   between them, in the system project, with the same idempotent
   "create, read back, adopt or repair" rules as the ready network.
2. **Two kinds of peer.** The MSP's own router (the requirement: the MSP
   gives its address and ASN and configures the router), or an **edge
   router** Portico builds in the site when the MSP cannot peer on its
   router (the MSP gives an address on the VLAN and adds one static
   route on its core).
3. **Only Portico touches the edge router.** No human login, no agent,
   no configuration in place: every change is a replace (a new VM from
   the release's image with new cloud-init, the session moved to it, the
   old VM deleted), and so is an upgrade.
4. **The card tells the truth.** The Networking card shows the peering's
   state read from Prism Central (gateway UP, session UP, the prefix
   advertised) and names what the MSP still has to do on its network.
5. **What the MSP must give** is listed in the card and in the install
   guide, nothing is asked twice.

### Non-goals

- NAT of the routed prefix anywhere in Portico. The site's firewall NATs
  towards the Internet as for any internal network (Marco, 2026-11-01).
- A firewall on the edge router: Flow secures the tenants, the perimeter
  is the MSP's.
- High availability of the edge router (two VMs with VRRP) and of the
  local gateway: later, if measured useful; the card shows a peer down
  within its monitor's pace, and a replace rebuilds it in about three
  minutes.
- Building or configuring the MSP's router. VyOS is documented as a
  router an MSP may run itself (the same FRR, cloud-init and an API);
  Portico only gives the peering parameters.
- A static route on the MSP's router without BGP keeps working as in
  0.2.x and stays documented as the manual way, outside the card's
  checks: the peer kind `static` (my call: the card cannot verify it,
  and it is where sites built before this design are; the migration
  gives every existing site `static`, the card shows "the routed prefix
  is routed by your router, not checked" and recommends the peer).
- Dark sites: the Network Gateway image comes from Nutanix's portal
  through Prism Central; an MSP without that access loads it with LCM
  first, as Nutanix documents.

### Anti-goals

- Portico holding a credential, key or password for the edge router.
- An edge router that works because of NAT: a tenant's floating IP must
  answer from inside the MSP's network exactly as from the Internet.
- Peering objects in a tenant's project, or the edge router reachable
  by a tenant.
- A change on the Nutanix side that the MSP must mirror on its router
  by hand: the prefix and the next hop travel in BGP, the only manual
  piece is the one static route on the core (edge mode) or the peer
  (router mode), both shown by the card.

## 3. Constraints

- Prism Central pc.7.6 and later, Flow Virtual Networking Integrated
  mode, the v4.4 networking API: `/gateways`, `/bgp-sessions`,
  `/bgp-sessions/{extId}/bgp-routes` (write-up 37, pc.7.6.0.6).
- The local gateway is a VM Prism Central builds from an image it
  downloads (`7.6.9ede470.20260715` on 2026-10-31), 2 vCPU, 4 GiB,
  10 GiB; one address on the VLAN of the transit VPC's external subnet.
- The gateway's create needs `projectExtId` of the system project
  `_internal` (`00000000-0000-0000-0000-000000000000`): without it the
  workflow cannot read back its own image (write-up 37).
- The gateway reports `DOWN` for about a minute after its VM is on; a
  session created before `UP` is refused (`NETWORKING-10057`).
- The edge router image is Rocky Linux 9, built by the appliance's
  image pipeline (section 4.4); the edge needs no Internet at boot.
- Addresses: the local gateway's address and the edge router's address
  are on the external subnet's VLAN, outside the external subnet's pool,
  given by the MSP. Their NICs go on the external subnet itself when
  Prism Central accepts a VM there with a static address outside the
  pool (section 6 item 9); otherwise Portico creates an unmanaged VLAN
  subnet `portico-<site>-vlan` on the same VLAN ID and virtual switch
  for them (my call: the lab's `MGMT` is such a subnet, a dedicated
  no-NAT VLAN has none). Prism Central must reach the gateway's address
  to read its state: a prerequisite the install guide states. Private ASNs: Portico proposes 65010 for the gateway
  and 65020 for the edge; the MSP's router's ASN is the MSP's.
- The service account's networking rights (design of 2026-10-29, 4.5)
  must cover gateways, BGP sessions, images and VMs in the system
  project: to measure (section 6).
- Public repository, English, no site or customer names in product text.

## 4. How it works (decided)

### 4.1 The peering, always

When the MSP saves the card's Routing section (Marco, 2026-11-01: at
Save, before and independently of the ready-network switch; the switch
stays off until the session is UP, section 4.6), and on Repair, Portico
builds in the site, in the system project:

| object | name | body |
|---|---|---|
| local gateway | `portico-<site>-bgp` | deployment on the site's AOS cluster, one interface on the external subnet's VLAN subnet with the address and gateway the MSP gave, `LocalNetworkServices` with `localBgpService` (ASN, the transit VPC) and the interface address as service address, `projectExtId` of the system project |
| remote gateway | `portico-<site>-peer` | `RemoteNetworkServices` with the peer's address and ASN: the MSP's router, or the edge router |
| session | `portico-<site>-peering` | the two references, the local interface address, `shouldAdvertiseAllExternallyRoutablePrefixes: true` |

The card waits for the gateway `UP` before the session, as
`lab/network/bgp-gateway.sh` does. Names and the system project make
the objects recognisable; read-back prefers the object by name in the
system project, never a tenant's (the 0.2.2 ownership rule). The edge
VM has no project of its own in the VM API: Portico keeps its extId,
reads it back by extId, and by name only to adopt a VM that carries the
category `portico:edge` it sets at create (my call; the policy's filter
on VMs uses that category, section 4.7).

Marco's decision (2026-11-01): the eBGP peer **is the requirement**. The
card's Routing section has one choice, the peer (`static` is what an
upgraded site has until the MSP picks one):

- **Your router** (the requirement): the MSP gives the router's address
  on the VLAN and its ASN, and configures the session on the router
  (`neighbor <gateway address> remote-as 65010`). The card shows the
  three lines the router needs.
- **An edge router built by Portico** (the way out): the MSP gives one
  free address on the VLAN; Portico builds the edge (4.3) and the card
  shows the one static route the MSP adds on its core: `<routed prefix>
  via <edge address>`.

### 4.2 What the card shows and checks

To the nine checks of the 2026-10-29 design the card adds:

| check | read from | when it fails |
|---|---|---|
| local gateway | `GET /gateways/{extId}`: `status.state` `UP`, `installedSoftwareVersion` | "The BGP gateway is DOWN (<message>)"; Repair waits for it to come up (a gateway that stays down is deleted in Prism Central, then Repair builds it again); a gateway that is gone is recreated by Repair |
| session | `GET /bgp-sessions/{extId}`: `status.state` `UP` | "The BGP session is down: <message>"; in router mode "configure the session on your router: …", in edge mode "the edge router may still be booting: Replace the edge router if it stays down" |
| advertised | `GET /bgp-sessions/{extId}/bgp-routes` holds the routed prefix with the transit VPC's address as next hop | "The routed prefix is not advertised: <what is>" |
| edge router (edge mode) | the VM exists and is on; the session UP is the proof it routes | "The edge router is off"; Repair turns it on; an edge router that is gone is built again by Repair; during a build the rows read "being built" |
| reachability | a warning, not a check: Portico cannot see the MSP's core; the card repeats the static route (edge mode) or the peer (router mode) until the MSP marks it done (my call: a checkbox the MSP ticks, stored on the site) | — |

The site monitor reads the three Prism Central states at its own pace
and the site's page shows them; a session that goes down turns the
Networking card red, with the message from Prism Central.

### 4.3 The edge router

A VM Portico creates with the Prism Central VM API from the edge image
(4.4), cloud-init `CONFIG_DRIVE_V2`, 1 vCPU, 1 GiB, on the external
subnet's VLAN:

- NetworkManager: the address the MSP gave, the VLAN's router as default
  gateway (the external subnet's gateway before this design, read from
  the subnet), DNS from the site's networking settings.
- FRR: `router bgp 65020`, `no bgp ebgp-requires-policy`, the gateway's
  address as neighbour with its ASN, `soft-reconfiguration inbound`.
- `net.ipv4.ip_forward = 1`, ICMP redirects off, no NAT, no firewall
  beyond nftables input: BGP (179) from the gateway's address only,
  nothing else listening; no SSH, no password, no key (goal 3). The
  console shows the hostname and nothing to log in to.
- The external subnet's gateway becomes the edge's address, so the
  transit VPC's traffic leaves through the edge; the edge's own default
  route is the VLAN's router.

Replace (a new image, Repair when the VM is missing or off, a change of
address) is **break-before-make** with the one address (Marco,
2026-11-01): Portico powers off and deletes the old VM, creates the new
one from the current image with the same cloud-init, and waits for the
session to report `UP` again; about three minutes without the prefix, in
a window the MSP picks (the card says so before it starts), and the
static route on the core never changes. A change of address is the same
sequence with the remote gateway updated to the new address (or
recreated and the session updated, or the session recreated when the
update is refused, section 6), the external subnet's gateway updated,
and the "route added" tick reset: the MSP moves its static route.

### 4.4 The edge image

`portico-edge-<version>.qcow2`, built by the appliance's image workflow
next to `portico-<version>.qcow2`: Rocky Linux 9 GenericCloud base, FRR
and nftables installed, the CIS Level 1 Server profile of the appliance
with the same exceptions, cloud-init enabled, no user, no password, no
SSH host key; about 1 GiB. Attached to the release with its checksum.

Portico imports it into the site's image service on the first switch-on
in edge mode, by URL from the release (`POST /api/vmm/v4.0/content/images`
with a URL source, as `lab/appliance/deploy.sh` has Prism Central do for
the appliance), named `portico-edge-<version>`; a site whose Prism
Central cannot reach GitHub gets the image uploaded by the MSP under
that name, which Portico then finds (my call: the URL import first, the
name lookup as the fallback, both measured in section 6). The version
is the appliance's: an appliance upgrade makes the card offer "Replace
the edge router with <version>", never silently.

### 4.5 Moving between the two peers

The edge router is an option next to the router, not the first step of
every site (Marco, 2026-11-01): the card asks for the peer at once, the
router first as the requirement, the edge as the way out, and a site may
move from one to the other at any time.

**Edge to router**: the MSP gives the router's address and ASN and
configures the neighbour; Portico updates the remote gateway to the
router (or recreates it and updates the session), waits for the session
`UP` through the router, puts the external subnet's gateway back to the
VLAN's router, and **powers the edge off but keeps it** for a rollback
(Marco, 2026-11-01): the card offers "Back to the edge router" (power
on, the remote gateway and the subnet's gateway back) and "Delete the
edge router"; the static route on the core is the MSP's to remove as
soon as the router peers (on FRR a static route keeps the BGP route out
of the kernel whatever its metric, write-up 39), the card reminds it
until the edge is deleted. The stored kind follows the remote gateway in
Prism Central and every operation ends with the session's state read
back (ADR 0010).

**Router to edge**: the MSP gives the edge's address; Portico builds the
edge, updates the remote gateway to it, waits for the session `UP`,
moves the external subnet's gateway to the edge; the card shows the
static route for the core and the neighbour to remove from the router.

### 4.6 State

On the site's networking settings (`SiteNetwork`): the peer kind
(`static`, `router` or `edge`), the gateway's address and ASN, the
router's address and ASN, the edge's address and whether it is kept
powered off, the "route added" tick; the objects' extIds (local gateway,
remote gateway, session, edge VM, edge image) and a `peering_state` with
its error, as the tenant placement's `net_state`. The ready-network
switch requires `peering_state` UP for `router` and `edge`; `static`
imposes nothing (the 0.2.x behaviour).
The peering lives as long as the switch is on **or any tenant holds a
ready network on the site** (my call: the switch is "for new
connections", as in the 2026-10-29 design, and the floating IPs in use
keep their route). Only when the switch is off and the last ready
network is gone does Portico delete the session, the gateways and the
edge VM, and put the external subnet's gateway back; the image stays
(the gateway's is Nutanix's; the edge's is deleted when no site uses
it).

### 4.7 Rights

The service account's Portico Networking policy (2026-10-29, 4.5)
filters every entity type; the operations it needs for gateways, BGP
sessions, images and VMs in the system project are listed from the
measurement of section 6 and added to `networking_rights.go`, with Repair
offering them to a site set up before.

## 5. What changes in the other documents

- Install guide, "Before switching it on", step 3: the eBGP peer is the
  requirement; the edge router is Portico's way out with one static
  route on the core; the plain static route on the router is the manual
  way of 0.2.x; VyOS named for an MSP that runs its own router; the
  addresses the MSP sets aside (gateway, peer or edge).
- Tenant networking design 4.1: the Routing section and the four checks
  of 4.2; "the MSP's router sends that prefix … by a static route or
  through a BGP gateway" becomes a pointer here.
- Write-up 37: a note that `portico-edge` in the lab stands for the
  MSP's router, and is the ancestor of the edge router here.
- Image README and workflow: the second artefact.
- Backlog: HA of the edge router; the Networking card's BGP state
  (already listed) is done here.

## 6. To measure before the code is final

1. `PUT /bgp-sessions/{extId}` with a different `localGatewayReference`
   or `remoteGatewayReference`: accepted, or refused (then delete and
   recreate, and how long the prefix is gone from the peer).
2. `PUT /gateways/{extId}` on a remote gateway with a new address:
   accepted, and whether the session follows.
3. `PUT /subnets/{extId}` changing an external subnet's gateway while a
   transit VPC uses it: accepted, or the subnet must be rebuilt (then
   the design changes: the edge keeps its address for life, and a
   change of address is a Disconnect-and-rebuild of the site's network).
4. `POST /api/vmm/v4.0/content/images` with a URL source from the
   service account: rights, time for 1 GiB, behaviour when the URL is
   unreachable.
5. The minimal operations for the service account on gateways, BGP
   sessions, images and VMs in the system project.
6. The session's state and the peer's routes when the edge VM is powered
   off: how long until Prism Central reports `DOWN`.
7. The edge image: FRR up and the session `UP` within how long from
   power-on; boot without Internet; the CIS profile's effect on
   forwarding (none expected).
9. A VM with a static address outside the pool on the external subnet
   itself: accepted by Prism Central, and the gateway's and the edge's
   traffic as expected; otherwise the unmanaged VLAN subnet of section 3.
10. The CIS Level 1 profile on the edge image: `ip_forward` is a CIS
   control, so the tailoring keeps it on, and the scan's report says so.
8. The VLAN's router as the edge's default gateway when the external
   subnet's gateway was already the MSP's router: no change for the
   edge; when the external subnet's gateway was a previous edge: read
   the VLAN's router from the MSP (asked once in the card, my call).

## 7. Left to the builder

- The exact rows and migration for 4.5, and whether `SiteNetwork` keeps
  the extIds or a `site_peering` table does.
- The card's layout for the Routing section and the "lines for your
  router" box.
- The cloud-init template of the edge and the nftables input rules.
- The Packer stage that builds the edge image from the GenericCloud
  qcow2 (no anaconda needed) and its OpenSCAP tailoring.
- The state machine of replace and Repair, reusing the ready network's
  step list and `createWithTask`.
- Tests: a fake Prism Central for gateways and sessions, the replace
  sequence, the card's checks; a lab run on the lab cluster scripted but manual.
