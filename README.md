# Portico

**Multi-tenant service layer for Nutanix Prism Central**

Portico is a virtual appliance that runs inside a Nutanix cluster and acts as
the single, authenticated entry point through which a managed service
provider's tenants reach Nutanix services. The first service is Prism
Central; later services (Nutanix Enterprise AI, Nutanix Kubernetes Platform)
plug into the same identity and the same tenant model.

Portico does not replace Nutanix Central. It fills the gap for the small MSP
that wants to expose services to its own tenants on top of one or more Prism
Central instances, without a hyperscaler-sized control plane. Tenants are
created with the same primitive Nutanix's own SP Central uses,
a Projects 2.0 project per tenant with group-based role mapping and no
global roles: an MSP that later adopts SP Central finds tenants it
recognises. Portico is the path towards it, not a detour.

## What it does (step 1)

- Puts one Prism Central behind a public URL, through a Cloudflare Tunnel or
  directly on port 443, with the same security guarantees in both modes.
- Authenticates every public request against an identity provider (Keycloak)
  before it reaches Prism Central. Prism Central trusts the same identity
  provider via SAML: one login for the tenant user.
- Never lets a Prism Central local account (including `admin`) log in from
  the public path, even when the identity provider is down.
- Creates a tenant in one action: a Projects 2.0 project in Prism Central, a
  group in the identity provider, a role mapping, a first user, and an
  automated visibility check.
- Keeps platform administration on an internal listener only.

## Why "Portico"

A portico stands in front of private buildings and makes them reachable
from the public space. It is not the building, it does not replace it, and
it does not change who owns it; it has its own rules about who walks in
and what they see, and one covered walk can join several buildings. That
is Portico's relationship with Prism Central: Prism Central stays the
house, Portico is the way the tenants reach it.

The project is written in Bologna, whose porticoes are a UNESCO World
Heritage Site. The word is the same in Italian and in English, it is one
word with no vendor name in it, and `portico serve` reads well.

## What it is not

- Not a replacement for Nutanix Central or SP Central. Portico
  creates tenants with the same primitive SP Central uses, so an MSP that
  later adopts Nutanix Central finds tenants it can recognise.
- Not a second Prism Central: Portico orchestrates, it does not copy state it
  does not own.
- Not dependent on Cloudflare for security: direct exposure is as safe as
  tunnelled exposure.

## Supported platform

Prism Central **pc.7.6 and later** (Projects 2.0). Older releases use a
different project model and are out of scope.

## Status

**Milestone 2b: tenants are managed from the admin UI in the lab.** `portico
serve` fronts Prism Central and Keycloak on two public names, requires a
Keycloak login before any request reaches Prism Central, refuses every
local-login path and every `Authorization` header measured on pc.7.6, and
offers an internal pass-through for administrators. On the internal
listener an administrator signs in with the break-glass account and
creates a tenant end to end (Keycloak group, Projects 2.0 project with
resource group, SAML group, identity provider sharing, project-scoped role
membership, visibility check), follows the steps, creates its first user,
disables, enables and deletes it; the same actions are a JSON API for
scripts. The tenant user logs in once and sees only that project. No
landing page or Keycloak theme yet (Milestone 2c), no setup wizard
(Milestone 2d), no appliance image (Milestone 3).
The design document is in
[`docs/superpowers/specs/2026-10-17-portico-design.md`](docs/superpowers/specs/2026-10-17-portico-design.md),
lab measurements in `docs/lab/`, decisions in `docs/adr/`.

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
