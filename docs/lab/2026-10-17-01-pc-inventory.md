# 01 — Prism Central inventory and v4 API discovery

## Question

Design items 6.3 (first part) and 6.8 (first part): which API families exist
on this Prism Central, where Projects 2.0 live, whether a SAML identity
provider already exists, and what the "Default Project" looks like.

## Measured on

- Prism Central: pc.7.6.0.6 (NTNX-PC-01), managing cluster NTNX-01 (AOS 7.6)
- Date: 2026-10-17
- By: Claude (Marco's session), `lab/inventory.sh`

## Method

`source lab/env.sh && lab/inventory.sh` plus manual probes with `lab/pc.sh`
on candidate paths. API paths were taken from the public Go clients in
`github.com/nutanix/ntnx-api-golang-clients` (namespaces `multidomain`,
`iam`, `tenancy`), because the developer portal pages cannot be fetched from
the CLI and `kb/API-Ref-AOS-v7_6.pdf` is the Acropolis v1 reference, not v4.

## Result

- MEASURED: the UI answers **HTTP 200 with HTML** for any unknown `/api/...`
  path (SPA catch-all). A probe must check the content, not the status
  code. `lab/inventory.sh` does.
- MEASURED: `/api/prism/v4.0/config/pc` does not exist on pc.7.6. Version
  comes from `GET /PrismGateway/services/rest/v1/cluster` (`pc.7.6.0.6`) or
  from `GET /api/clustermgmt/v4.0/config/clusters` (2 items: NTNX-01 as
  `AOS`, NTNX-PC-01 as `PRISM_CENTRAL`).
- MEASURED: IAM v4.0 answers for `authn/users` (6: `admin` LOCAL plus five
  SERVICE_ACCOUNT), `authz/roles` (63), `authz/authorization-policies` (5),
  `authn/saml-identity-providers` (**0, none configured**),
  `authn/directory-services` (0), `authn/saml-sp-metadata` (XML).
- MEASURED: **Projects 2.0 are the `multidomain` namespace**:
  `GET /api/multidomain/v4.4.b1/config/projects` → 200, served version
  `v4.r3.b1`. `v4.0`–`v4.4` and `v4.4.b2` return 404 or HTML. One project
  exists: `name=_internal`, `id=default`, `extId=00000000-0000-0000-0000-000000000000`,
  `isDefault=true`, `isSystemDefined=true`. This is the "Default Project" of
  the Projects Guide (hidden from the UI summary page).
- MEASURED: `GET /api/multidomain/v4.4.b1/config/resource-groups` → 200,
  empty. Resource groups are how a project gets clusters and storage
  containers allocated (Projects Guide, "Nutanix resource groups").
- MEASURED: **role memberships** (identity → role within a project) are
  `GET /api/iam/v4.1.b3/authz/role-memberships` → 200, 0 items, served
  version `v4.r1.b3`. `v4.0`, `v4.1`, `v4.2` return 400
  `IAM-20011` schema validation error. Fields (Go model):
  `identityExtId`, `identityType`, `idpExtId`, `projectExtId`, `roleExtId`,
  `scopeTemplateName`, `scopeTemplateNameValues`.
- MEASURED: legacy v3 `POST /api/nutanix/v3/projects/list` → 200 with
  **0 entities**: the v3 project model is empty on 7.6, it does not show
  the Projects 2.0 project.
- MEASURED: SAML SP metadata: `entityID=https://10.23.210.175:9440/api/iam/authn`,
  ACS `Location=https://10.23.210.175:9440/api/iam/authn/callback`,
  binding HTTP-POST, `WantAssertionsSigned=true`, NameID format
  `unspecified`. The host is the IP: item 6.1 (SAML behind another name)
  is a real risk, measured in write-up 04.
- MEASURED: existing authorization policies: `Super Admin_acp`,
  `Self-Service Admin_acp`, `Project Manager_acp`, two `Role_Mapping_*`
  (Prism Viewer, Prism Admin). Roles relevant to Portico, all system
  defined: `Super Admin`, `Prism Admin`, `Project Manager`, `Project Admin`,
  `Developer`, `Consumer`, `Operator`, `Virtual Machine Admin`,
  `Tenant Admin`, `Tenant Consumer`.
- MEASURED (from `kb/` SP Central Admin Guide 1.0, not from the lab): SP
  Central onboards a tenant by creating **one project per tenant per
  domain**, named `sp-<tenant-name>`, with selected clusters and storage
  containers and vCPU/memory/storage quotas; it creates a service account
  `sp_svc_acc_<uuid>` with its own authorization policy in Prism Central;
  each tenant gets a first local admin user and must configure its own
  SAML IdP with a unique entityID; the tenant login URL is
  `<tenant>.tenants.<SP Central FQDN>`. Its public API is the `tenancy`
  namespace (`/api/tenancy/v4.0.a1/config/tenants`, `.../domain-allocations`,
  `.../domain-memberships`), which runs in Nutanix Central, not in Prism
  Central.

## Implications for the design

- Portico's tenant = a `multidomain` project + a resource group (clusters,
  containers, quota) + `iam` role memberships for the tenant group. That is
  the exact shape SP Central produces, so Portico tenants are
  **SP Central compatible by construction** (design §3). Project naming:
  keep a configurable prefix; `sp-` is SP Central's own and should not be
  imitated.
- Portico's `portico` service account mirrors SP Central's `sp_svc_acc_*`
  pattern: a local user plus an authorization policy. Write-up 07 measures
  the minimal policy.
- The Prism Central client in Milestone 1/2 must pin
  `multidomain/v4.4.b1` and `iam/v4.1.b3` for memberships, `iam/v4.0` for
  users, roles, policies and SAML. A version-negotiation step (read
  `$reserved.$fv`) belongs in the client.
- The v3 projects API is irrelevant on 7.6: drop the v3 fallback from the
  design.
- SP Central also warns that Prism Central must not be reachable from
  tenant networks. Same rule as Portico's 9440 firewall rule.

## How to re-measure after an upgrade

`source lab/env.sh && lab/inventory.sh`, then compare the `$reserved.$fv`
values and the HTTP codes with this write-up. If `multidomain` or
`role-memberships` move to a GA version, update `lab/inventory.sh` and the
Portico client version pins.
