# ADR 0001: Prism Central pc.7.6 and Projects 2.0 as the baseline

- Status: accepted (2026-10-17)
- Deciders: Marco, Claude

## Context

The first design assumed "pc.2024.3 and later" with a version adapter over
v4 APIs. Marco pointed out that pc.7.6 introduced Projects 2.0, the primitive
Nutanix's own SP Central uses to onboard tenants. The lab (pc.7.6.0.6)
confirmed that Projects 2.0 is a different model, not the same model behind
another API: projects live in `multidomain/v4.4.b1` (with a required `id`
slug), infrastructure is allocated through resource groups, access through
`iam/v4.1.b3` role memberships with a scope template, identity sources are
shared per project, and the v3 projects API is empty on 7.6. Existing
entities are migrated into a system project `_internal` at upgrade,
irreversibly.

## Decision

Portico supports **pc.7.6 and later** only. A tenant is a Projects 2.0
project plus a resource group plus role memberships plus the IdP shared with
the project. Legacy projects (pc.2024.3–7.5) are out of scope; if ever
supported they get a separate adapter with reduced guarantees. The Prism
Central client pins `multidomain/v4.4.b1`, `iam/v4.1.b3` (memberships,
SAML IdP with `redirectURL`, service accounts and keys) and `iam/v4.0`
(users, roles, policies), and reads `$reserved.$fv` to detect drift.

## Consequences

- Portico tenants are SP Central compatible by construction: same project
  shape, same service-account pattern (`Project Manager` policy, API key).
- The brownfield feature (step 2) must handle `_internal` and migrated
  projects, and never touch `_internal`.
- Beta API versions (`v4.4.b1`, `v4.1.b3`) can change with a Prism Central
  release; the lab scripts (`lab/inventory.sh`, `lab/projects/*.sh`) are the
  re-measurement procedure.
- Quotas are not in these APIs (Policy Engine / Self-Service); step 2.
