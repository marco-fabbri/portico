# ADR 0008: The tenant address pool is fixed by built networks, not by assigned subnets

- Status: accepted (2026-10-30)
- Deciders: Marco, Claude

## Context

The tenant networking design (4.2) fixed the address pool as soon as any
tenant had a subnet, and stored each tenant's `/24` once assigned. The
upgrade to 0.2.0 assigns a `/24` of the default pool, `10.100.0.0/16`,
to every existing tenant at the first start. The final review of the
0.2.0 branch found the consequence: on an upgraded appliance the pool is
fixed before the MSP has seen the field, and on a fresh one the first
tenant fixes it, even with no site running the ready network. The
exception, which design 4.2 also allowed "on the tenant page until its
first ready network exists", was only in the creation form. An MSP whose
own network uses `10.100.0.0/16` had no way out.

## Decision

1. **The pool is fixed once a built network uses one of its `/24`s**: a
   connection whose subnet exists in Prism Central, or is being built.
   Until then the MSP changes it in the setup page's Tenants card, and
   Portico moves into the new pool, in creation order, the pool `/24`s
   no built network uses. Exceptions stay where they are.
2. **The exception is changed on the tenant's page and through
   `PUT /api/v1/tenants/{id}/subnet`** until a connection of the tenant
   has its subnet in Prism Central; an empty value returns the tenant to
   the pool's next free `/24`.

## Consequences

- A tenant's `/24` can change until its first ready network uses it; the
  stored value stays the single source for every site afterwards.
- The pool stays fixed while any built network uses it: moving a tenant
  then means disconnecting it, as before.
- The settings API does not carry the pool; it is changed in the admin UI.
