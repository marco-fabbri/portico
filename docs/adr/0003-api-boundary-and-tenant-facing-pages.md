# ADR 0003: The REST API is the boundary; tenant-facing pages are the MSP's

- Status: accepted (2026-10-18)
- Deciders: Marco, Claude

## Context

Milestone 1 showed the tenant's path: Keycloak login, then Prism Central's
own provider-selection page with a SAML button and a local-account link.
Two questions came up on 2026-10-18. Can the second step be automated or
branded? And is Portico's admin UI the product, or one client of it?
Marco also raised a possible future direction: a Portico-owned tenant
console that hides Prism Central's UI entirely, maybe never built.

## Decision

1. Every action of the admin UI is a documented REST call on the internal
   listener (`/api/v1/...`). The HTMX handlers and the API handlers share
   one service layer; no page reaches the store, Keycloak or Prism Central
   on its own. Plans define the endpoint and its test before the page.
2. Everything a tenant sees before Prism Central is branded as the MSP:
   Portico generates the Keycloak login theme from its branding settings
   and shows its own landing page in place of Prism Central's provider
   selection, starting the SAML flow itself. If the selection call fails,
   Portico forwards the original page: the automation is convenience, the
   deny list is the control.
3. The Portico-owned tenant console stays a deferred idea in the design,
   with its prerequisite named: per-user identity towards the v4 API.
   Without it, Portico's service account would decide what each user sees
   and the project barrier in Prism Central would no longer be the first
   control.

## Consequences

- An MSP's script or Ansible creates a tenant with the same checks as the
  page, so there is no second path to protect, and the logic is tested
  without a browser.
- The UI can be replaced or another front end added without touching the
  logic. This is what keeps the deferred tenant console possible at no
  cost today.
- The landing-page automation depends on the IAM `v4.1.b3` API (beta), so
  it is measured in the lab, documented with a re-measure procedure, and
  degrades to the Milestone 1 click.
- Each Prism Central needs its own public host name (absolute paths from
  `/`); the Keycloak session makes moving between hosts password-free.
