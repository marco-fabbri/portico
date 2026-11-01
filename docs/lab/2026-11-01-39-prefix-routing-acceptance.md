# 39 — Prefix routing acceptance: the router, the edge, the moves

## Question

The prefix routing design (2026-11-01) built: on a site set up before,
after the upgrade and a Repair of the rights, does the Routing section
build the BGP gateway and the session to the MSP's router, does the
switch follow the session, is a tenant's VM reached on its floating IP
through the BGP route; does the edge router built from the release
image peer and route, how long is the routed prefix gone on a Replace,
do the moves router → edge → router → edge and Delete the edge leave the
site as the design says?

## Measured on

- Prism Central: pc.7.6.0.6 on NTNX-01 (`10.23.210.175`, site AZ-IT-MI1),
  AOS 7.6, Flow Virtual Networking in the Integrated mode; the transit
  VPC `portico-transit` on the no-NAT external subnet `external-routed`
  (gateway `10.23.210.194`, the lab's router), routed prefix
  `100.96.0.0/24`
- Portico: the second appliance `portico-acceptance` (`10.23.210.193`),
  upgraded in place from 0.2.2 to the development builds `dev-2105b13`
  and `dev-c7ce446` of the prefix-routing branch; the edge image
  `portico-edge-0.3.0-dev.qcow2` built on the build host (write-up 33)
  and imported by URL under the name the build expects
- The MSP's router: `portico-edge` (`10.23.210.194`, FRR, ASN 65020, the
  static route `100.96.0.0/24 via 10.23.210.200` as its fallback,
  write-ups 35 and 37)
- Date: 2026-11-01
- By: Claude, on the plan Marco approved (the Repair of the rights, an
  IAM grant, is the plan's step 2)

## Method

The hand-made peering of write-up 37 deleted first
(`lab/network/bgp-gateway.sh delete`: session, local and remote gateway;
`portico-edge` kept). The Routing section exists only in the admin UI,
so `lab/portico/ui.sh` drives it as a browser would (sign-in, the page's
CSRF token, the form posted with `HX-Request`); the environment of the
acceptance appliance is `PORTICO_SECRETS=~/portico-secrets-appliance
LAB_VM_IP=10.23.210.193 LAB_PRISM_HOST=portico LAB_ADMIN_CA=<its own
certificate>`.

1. `LAB_VM_IP=10.23.210.193 LAB_SSH_USER=admin
   lab/portico/upgrade-appliance.sh`, then `POST /api/v1/sites/az-it-mi1/setup`
   with the administrator's one-time credentials (the Repair).
2. Router mode: `ui.sh POST /setup/sites/az-it-mi1/routing peer=router
   gateway_addr=10.23.210.195 gateway_gw=10.23.210.254 gateway_asn=65010
   router_addr=10.23.210.194 router_asn=65020`, the card read every 10 s.
3. `LAB_JUMP=build@10.23.210.192 KEEP=1 lab/portico/ready-network.sh
   az-it-mi1`: the tenant `netcheck`, its ready network, a probe VM
   reached on its floating IP from the build host; the static route on
   `portico-edge` removed by hand so the BGP route is the one used.
4. Edge mode: `peer=edge edge_addr=10.23.210.196 edge_asn=65020`; on
   `portico-edge` the static route replaced by `100.96.0.0/24 via
   10.23.210.196` (the lab's "core").
5. Replace the edge, timed with a ping on the floating IP; Move to your
   router; Back to the edge router; Delete the edge router.

## Result

- MEASURED (1): the upgrade in place from 0.2.2 took the store backup
  and restarted in under a minute; the Networking card then reported
  `rights failed: the service account lacks View_Network_Gateway, …,
  Update_Virtual_Machine_Categories: Repair the site` (12 operations).
  The Repair (`POST …/setup`) answered in 32 s: `networking-rights: ok
  role Portico Networking given 37 operations, policy Portico
  Networking_portico-acceptance created`; the card's check went `ok`.
- MEASURED (2): the build in router mode took 238 s from Save to the
  card's "Working on the peering" going (the gateway VM
  `portico-az-it-mi1-bgp` is the long part, as in write-up 37); the
  session `portico-az-it-mi1-peering` was `UP` on the card at 276 s,
  FRR on `portico-edge` `Established` with `100.96.0.0/24` received from
  AS 65010 with next hop `10.23.210.200`. While building, the card's
  `bgp session` check says "The BGP session is not built; Repair creates
  it": the live check runs before the session exists.
- MEASURED (3): on `portico-edge` the kernel's static route (metric 100,
  from NetworkManager) stays the one used although FRR holds the BGP
  route (distance 20): zebra sees the static as a kernel route (distance
  0). With the static removed by hand the BGP route is installed
  (`proto bgp metric 20`). An MSP whose router keeps a static fallback
  gets the same: the static wins until it is removed.
- MEASURED (3, the 0.2.2 bug): the tenant's ready network failed at
  `net-external-subnet: prism central: 404 portico-netcheck-ext not
  found` two seconds after Prism Central's `kSubnetCreate` task
  succeeded. The subnet existed, with `projectExtId
  00000000-0000-0000-0000-000000000000` (the system project
  `_internal`); the read-back of v0.2.2 (`6eb2bd6`) takes only an empty
  `projectExtId` as "no project", so every new ready network fails on
  0.2.2. Fixed on the branch (`c7ce446`: the system project reads as no
  project; the tenant's object put there is the orphan Retry network
  repairs), the appliance upgraded to `dev-c7ce446` and the edge image
  imported again under that name.

- MEASURED (3): with the BGP route in use, the probe VM answered on its
  floating IP `100.96.0.14` from the build host and reached the
  Internet (`internet: 200`) through the router's NAT (write-up 35).
- MEASURED (4, the move to the edge): Save with `peer=edge` built the
  edge VM `portico-az-it-mi1-edge` from the image, moved the remote
  gateway to `10.23.210.196` and the subnet's gateway to it. The first
  build hit Prism Central's `VMM-30128 no host has enough resources` on
  the power-on (the nested host at 72 % memory, the same flavour the
  probe VM gets) and a wave of `504 upstream request timeout` and `500
  Failed to store new cookie in IDF` answers; the objects that got a 504
  on their create were adopted by name by Repair. The floating IP was
  gone for 687 s and 430 s over those two attempts; a clean Repair
  (edge on, session moved) took 28 s with the floating IP gone 40 s.
- MEASURED (4, the edge's gateway): a VM behind the edge lost the
  Internet: the edge NATs nothing (design 4.3), its default route is
  the VLAN router of the Routing section (`gateway_gw`), and the lab's
  one was the FortiGate `10.23.210.254`, which has no route back for
  the prefix and no NAT for it. The VLAN router the section asks for is
  the one that forwards (here NATs) the routed prefix, `10.23.210.194`
  in this lab; with it the edge routes and the VM reaches the Internet.
  The Save that corrected it at 12:36 was refused by a `500` on the VPC
  read and not stored; the one at 13:13 stored it.
- MEASURED (4, the subnet's gateway): a router-mode Save that changes
  `gateway_gw` stored it but left the external subnet's gateway where
  it was (only a move or the teardown set it): fixed on the branch
  (`c963bbd`, a router-mode build sets it when the session is up).
- MEASURED (5, Replace): break-before-make from the card: the old VM
  off and deleted in 12 s, the new one created in 25 s (38 s from the
  click to "done"), the floating IP gone 102 s (the edge's boot and
  the session through it); the new edge got the corrected default
  route. Back to the edge router (the kept edge powered on, the remote
  and the subnet's gateway back): 23 s on the card, the floating IP
  gone 99 s.
- MEASURED (5, the core's static route): on the lab's core the static
  `100.96.0.0/24 via 10.23.210.196` stayed the route used after the
  move to the router, a blackhole until removed by hand; with `metric
  100` the same: zebra takes a kernel route as distance 0 and keeps the
  BGP route (distance 20) out of the kernel whatever the metric, as in
  (3). The card said to remove it "once the edge router is deleted": it
  now says to remove it as soon as the router peers (`dc7ef12`).
- MEASURED (5, Prism Central under load): from 13:27 Prism Central
  answered `504 upstream request timeout` in waves of two to four
  minutes (both nested clusters on the same host, each `kvm` at 12 to
  14 cores); Move to your router, Repair and Delete the edge router
  failed at their first read of the transit VPC. Three defects came
  out of those failures, all fixed on the branch:
  1. MoveToRouter stored the new kind before its first step; after
     the failed move the store said "router" while Prism Central still
     peered with the edge, and Delete the edge router, trusting the
     store, deleted the live peer at 13:43 (session down, the tenant
     VM isolated). The kind now follows the remote gateway (`a521a7a`).
  2. DeleteEdge stored the "building" mark the card set as its final
     state, so the card stayed on "Working on the peering" with
     nothing running and refused every action (`796a22c`).
  3. A restart (the upgrade) during an operation would leave the same
     mark; at startup such a peering is failed with "Portico restarted
     while the operation ran; press Repair" (`b2a7d46`).
  The recovery was Repair in router mode once Prism Central answered
  (the remote gateway back on `10.23.210.194`, the subnet's gateway
  with it): see the last line.
- MEASURED (5, the final state): Repair at 14:12 moved the remote
  gateway back to the router in 8 s and ended `down` (the session
  through the router was not up yet; the monitor read it `UP` two
  minutes later); a second Repair at 14:20, with the session up, put
  the external subnet's gateway back on `10.23.210.194` (35 s on the
  card); the core's static route removed, the BGP route installed, the
  probe VM reached on its floating IP and `internet: 200`. The floating
  IP had been gone 2206 s, from the deletion of the live peer to the
  session through the router. The site ends in router mode with the
  router, no edge VM, the gateway VM and the session in the system
  project, the edge image `portico-edge-0.3.0-dev` in the image service
  (the one of the previous build deleted); the tenant `netcheck` kept.
- Ping gaps on the floating IP over the day (UTC): 40 s (the move to
  the edge), 687 s and 430 s (the edge's two builds under the power-on
  failure), 99 s (Back to the edge router), 102 s (Replace), 291 s (the
  failed move with the core's static removed, until it was put back),
  2206 s (the deleted peer).

- The final review of the branch (a fresh reviewer, after this
  acceptance) found the same theme in other places and the fix pass
  closed them with tests: the Save that moves the peer bypassed the
  kind rule; a Save of the Networking card tore the peering down when
  Portico itself had forced the switch off; the gateway's 504 had become
  transient for the tenant steps too (a create sent again); the waits
  gave up on one transient answer; a remote gateway update recreated the
  remote on any error; the monitor wrote a whole peering over an
  operation that finished meanwhile; peer fields outside the chosen kind
  were not validated; a new edge address was a dead end (now a Replace);
  Delete the edge router left the subnet's gateway on the edge; a static
  site got a red rights row after the upgrade; the VLAN subnet was
  adopted without its virtual switch; the boot test now proves the edge
  image has no build user and no password hash (`users=none; hashes=0`
  on this image). ADR 0010 records the rules.

## Implications for the design

- The fix of the read-back is in 0.3.0's notes under Fixed; a 0.2.2 site
  gets its tenants' networks with Retry network after the upgrade.
- The install guide's step 3 says the router's static route must go
  once the session is up, or it keeps winning; the core's static route
  to the edge needs a metric above BGP's, or goes as soon as the router
  peers.
- The VLAN router of the Routing section is the router that forwards
  the routed prefix (the edge's default route and the subnet's gateway
  in router mode): the section's help says so now.
- The stored kind of the peering follows the remote gateway in Prism
  Central, never the operation asked for; every operation ends with the
  session's state read back; a restart fails the operation it cut.
- Prism Central's `504` waves are the nested lab's, not the product's:
  the transient list covers the task polls only, every other call fails
  the step and Repair adopts what was created.

- The gateway VM and its session survived every move; the edge VM is
  the only object Replace recreates, in 38 s plus its boot.

## How to re-measure after an upgrade

The method above, from `lab/network/bgp-gateway.sh delete`; the timings
are the card read every 10 s with `lab/portico/ui.sh GET
/setup/sites/<site>/networking`, and a ping on the floating IP during
Replace.
