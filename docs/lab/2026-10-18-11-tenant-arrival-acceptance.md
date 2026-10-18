# 11 — Tenant arrival through the API (Milestone 2a acceptance)

## Question

Milestone 2a (design §9 row 2, first half): a tenant created through
Portico's admin API gets a Projects 2.0 project with a resource group, a
SAML group, the identity provider shared, a project-scoped role
membership and a Keycloak group, idempotently and with every step
recorded; its first user logs in through the public URL and sees only
that project; disabling the tenant locks its users out; deleting the
tenant removes every object Portico created (its users in Keycloak stay,
without access) and never touches `_internal`.

## Measured on

- Prism Central: pc.7.6.0.6, cluster NTNX-01 (nested lab, slow)
- Portico: `dev-244e6a6` on the lab VM (`lab/portico/deploy.sh`),
  Keycloak 26.3.5 with the `portico-admin` client (write-up 10 branch,
  `lab/saml/keycloak-realm.sh admin-client`)
- Date: 2026-10-18
- By: Claude (Marco's session), `lab/tenants/acceptance.sh`, then Chrome

## Method

1. `lab/tenants/acceptance.sh`: `POST /api/v1/tenants` for slug `tenant-b`
   (lab prefix `portico-lab` → project `portico-lab-tenant-b`, group
   `tenant-tenant-b`), poll until `active`, read the steps, run the
   visibility check, read the objects back from Prism Central as the
   service account, create `carol@lab.portico.test` with a temporary
   password, disable and enable the tenant checking carol's `enabled`
   flag in Keycloak.
2. Chrome on the Mac: `https://prism.lab.portico.test/` as carol.
3. `lab/tenants/acceptance.sh --delete`: delete with the project, then
   check Prism Central, Keycloak and a before/after snapshot of
   `_internal` (without `modifiedTimestamp`).

## Result

- MEASURED: provisioning took **77 s** on the nested lab; the seven steps
  (`keycloak-group`, `project`, `resource-group`, `saml-group`,
  `idp-share`, `role-membership`, `visibility`) all `ok` with the extIds
  in their detail. Visibility `ok: true`. Read-back as the service
  account: project present with the recorded extId, one role membership
  for the group in the project, IdP shared with the project.
- MEASURED: `POST /users` → 201 with a 20-character temporary password,
  returned once; `/disable` → `disabled` and carol `enabled=false` in
  Keycloak; `/enable` → `active` and `enabled=true`.
- MEASURED, browser: no Portico session → Keycloak login (theme-less,
  "Portico lab") → Keycloak forces **Update password** (required action
  set by Portico) → back on Prism Central's provider page → SAML button →
  console as `carol`. VMs: "No VMs match your filter". Projects (now
  under Admin Center): exactly `portico-lab-tenant-b`, Active, 1
  identity. Clusters: `GET /api/clustermgmt/v4.0.b2/config/clusters` →
  **403**, the page stays on its loading screen (same as bob in write-up
  05).
- MEASURED: deletion with `delete_project=true` took **28 s**: tenant row
  gone, project absent, SAML group absent, Keycloak group absent,
  `_internal` identical before and after.
- MEASURED: what deletion leaves behind, by design: carol's user in
  Keycloak (without a group, so without access) and the SAML user Prism
  Central created for her at first login. The acceptance script removes
  the Keycloak test user on `--delete` so a re-run starts clean (a second
  run against an existing user gets 409 from `POST /users`, and that user
  is not in the new group, so `/disable` does not touch her: measured on
  the second run of 2026-10-18).
- MEASURED after the review fixes (build with migration 0002, `last_op`
  column): the store on the VM migrated in place, the probe and the full
  acceptance passed again (provisioning 87 s, deletion clean).
- NOT MEASURED: whether the console sends an `Authorization` header (the
  Chrome extension's network view shows URLs and statuses, not headers).
  Still open for Milestone 2b (write-up 10).

## Implications for the design

- Milestone 2a's half of §9 row 2 is done: API-created tenants work end
  to end on NTNX-01. The landing page, the Keycloak theme and the admin
  UI (Milestone 2b) complete the row.
- The derived group name `tenant-<slug>` reads badly when the slug already
  starts with `tenant-`; cosmetic, the prefix is configurable.
- Deleting a tenant does not delete its users in Keycloak; a "delete users
  too" option is a Milestone 2b decision for the UI.
- The 77 s are the nested lab's Prism Central (write-up 09 measured one
  API call at 48 s on 2026-10-18); the API is asynchronous for this reason.

## How to re-measure after an upgrade

```
source lab/env.sh && lab/portico/deploy.sh
lab/tenants/acceptance.sh            # create, check, user, disable/enable
# browser: log in as carol with the password in ~/portico-secrets/carol-tmp-pw
lab/tenants/acceptance.sh --delete   # delete with the project, verify clean
```
