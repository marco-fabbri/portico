# ADR 0009: The default tenant address pool keeps clear of Prism Central's reserved subnets

- Status: accepted (2026-10-30)
- Deciders: Marco, Claude

## Context

0.2.0 gives each tenant a `/24` from the tenant address pool, by default
`10.100.0.0/16`. The Prism Central Guide pc.7.6 ("Internal Network
Configuration") lists the subnets its microservices infrastructure uses
inside Prism Central by default: `192.168.5.0/24`, `10.100.0.0/16` and
`10.200.32.0/24`. It warns that they must not overlap an active subnet:
Prism Central does not route traffic from hosts in those ranges. The
Security Guide 7.6 lists the same ranges. On NTNX-01 the Prism Central
VM has its pod network on `10.100.0.0/24` (`cni0`, `10.100.0.1`), the
`/24` the first tenant of write-up 35 was given.

The ready network hides the tenants' subnets behind their VPC's NAT, so
Prism Central never sees a `10.100.x` source and the acceptance worked.
The overlap still breaks a model without NAT (a tenant-dedicated VRF, a
routed tenant subnet), and it goes against Nutanix's explicit guidance.

## Decision

1. **The default pool is `10.128.0.0/16`**, clear of the three reserved
   subnets and of the usual LAN ranges.
2. **A document without a pool gets the new default when it is read.**
   Every save stores the whole document, the filled pool with it, and
   switching a site's ready network on is a save. A document that still
   has no pool has therefore never had a ready network switched on and no
   built network, so the new default moves nothing that exists.
3. **A pool already stored is kept**, `10.100.0.0/16` included. The 0.2.0
   and 0.2.1 release notes tell the MSP to change it on the setup page's
   Tenants card while no network is built (ADR 0008).

## Alternatives considered

- **Refuse a pool that overlaps a reserved subnet.** Rejected: the
  reserved subnets can be changed when Prism Central is deployed, so the
  check would refuse a pool that is free on that Prism Central. It would
  also refuse to save the setup page on every 0.2.0 appliance that stored
  `10.100.0.0/16`.
- **Move a stored `10.100.0.0/16` at the upgrade.** Rejected: once a
  network is built the pool is fixed (ADR 0008), and an upgrade must not
  renumber an MSP's choice.

## Consequences

- Fresh appliances and appliances upgraded from 0.1.x take
  `10.128.0.0/16`.
- A 0.2.0 appliance keeps `10.100.0.0/16` until the MSP changes it.
- The install guide names the three reserved subnets as ranges to avoid.
