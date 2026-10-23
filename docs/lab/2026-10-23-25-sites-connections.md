# 25 — Sites usability part B: connections, suspend and disconnect

## Question

Part B of the sites usability design
(`docs/superpowers/specs/2026-10-22-sites-usability-design.md`, 4.4–4.6;
ADR 0006) on the appliance:

1. A tenant created with no site, connected later, and a failed
   connection retried.
2. Suspend and resume of one connection.
3. Disconnect refused while the project holds a VM, then done once it is
   empty; the tenant deleted only with no site left, with its Keycloak
   users.
4. What the service account can count in a project (write-up 24 counted
   as the lab admin).
5. Whether Prism Central reports a SAML group's identity provider
   (`idpId`) and keeps the description Portico writes on a project, the
   two marks Disconnect uses to delete only what Portico created.

## Measured on

- Prism Central: pc.7.6.0.6, site `AZ-IT-MI1` (NTNX-01) and site
  `AZ-US-NYC1` (`10.23.210.185`, cluster NTNX-02)
- Portico: appliance `portico-appliance` (10.23.210.191), upgraded in
  place from `dev-f2797d5` to `dev-1083008` (branch `sites-usability`)
- Date: 2026-10-23
- By: Claude, with the scripts below through Portico's admin API; Marco
  approved the upgrade and the writes

## Method

- Appliance environment of write-up 23, then
  `lab/portico/upgrade-appliance.sh`.
- `lab/sites/connections.sh suspend az-us-nyc1 acme` (new): the site's
  sessions, Suspend, Suspend again, Resume, through `/api/v1`.
- `lab/sites/connections.sh cycle site-1 beta` (new, writes): tenant
  `beta` with `"sites": []`, a Keycloak user, Connect to `AZ-IT-MI1`
  with the site's defaults (a failed connection is retried once), the
  project's description and the SAML group's `idpId` read back, a
  powered-off VM with no disk created in the project with the lab
  admin's credentials, the contents, Disconnect, tenant Delete, the VM
  removed, the contents, Disconnect, then the project and SAML group
  looked up in Prism Central, tenant Delete, the Keycloak group and user
  looked up. Rerunnable: an existing tenant or connection is kept.
- `GET /api/v1/tenants/acme/sites/{site}/contents` on both sites, and
  Portico's journal (`journalctl -u portico`) for each kind's answer
  and each step.
- `lab/pc.sh GET` of `iam/v4.0/authn/user-groups` and
  `multidomain/v4.4.b1/config/projects` on NTNX-01, as the lab admin.

## Result

### 1. No site, connect, retry

- MEASURED: `POST /api/v1/tenants` with `"sites": []` answers 202 with
  `placements: []`; the tenant becomes `active` (Keycloak group only).
- MEASURED: Connect answers 202. On `AZ-IT-MI1` the first two attempts
  ended `failed` within seconds, each with Prism Central's own error as
  the reason (`500 {"message": "failed to get client"}` on the identity
  provider list; `500 {"message": "Failed to store new cookie in IDF"}`
  on the role lookup). Retry answered 202 and, on the third attempt, the
  connection was `active` in 20 s: project, resource group, SAML group,
  identity provider sharing, role membership, visibility check.
- The 500s are NTNX-01's IAM service, intermittent that morning: the
  site check answered at the same time, its lists answered 504 now and
  then (the first count of acme there failed with `504 upstream request
  timeout`, 503 from Portico), and the site probe got four isolated 401s
  in an hour (three in a row are needed to mark a site unreachable; it
  stayed `available`).

### 2. Suspend and resume

- MEASURED: Suspend of acme on `AZ-US-NYC1` answers 200 with the
  connection `suspended`; a second Suspend 409 ("the connection to
  AZ-US-NYC1 is suspended"); Resume 200, `active`, the same project
  extId. Nothing was written to Prism Central (no step in the journal).
- NOT MEASURED: the tenant side (a user's next click landing on the site
  page with the site greyed, an open VM console, the way back with no
  password) and the confirmation page with its user count: the admin
  and tenant logins in the browser were not available to Claude this
  session. Unit tests only.

### 3. Disconnect and Delete

- MEASURED: with one VM in `portico-beta`, contents answer
  `vms: 1, empty: false`; Disconnect answers 409 with the counts and
  "the project portico-beta on AZ-IT-MI1 still holds VMs: 1: empty it in
  Prism Central first"; nothing is deleted. Tenant Delete answers 409
  ("still connected to a site").
- MEASURED: with the VM removed, contents `empty: true`; Disconnect
  answers 202 and the connection is gone in 15 s, steps in order: role
  membership, identity provider unshared, SAML group, resource group,
  project. Prism Central lists neither the project nor the SAML group
  afterwards. Every count and step took 1–12 s on NTNX-01.
- MEASURED: tenant Delete answers 202, then 404; steps: Keycloak users
  (1 deleted), Keycloak group; Keycloak finds neither afterwards.
- NOT MEASURED: Prism Central's own refusal path (a kind Portico cannot
  count holding something, ending `remove-failed`): nothing uncountable
  was put in the project. Unit tests only.

### 4. What the service account counts

- MEASURED, on both sites: VMs, images, templates, OVAs, volume groups,
  subnets, VPCs, categories and recovery points are counted. Floating
  IPs answer 403 to the service account (the admin could list them,
  write-up 24) and Flow Network Security policies 403 as for the admin;
  both are reported in `uncounted`. A count of all eleven kinds took
  17 s on `AZ-US-NYC1` and about 1 s on `AZ-IT-MI1` once it answered.

### 5. Ownership marks

- MEASURED: `iam/v4.0/authn/user-groups` lists `idpId` on SAML groups
  (`tenant-acme` and `tenant-beta` carry the extId of
  `portico-keycloak`).
- MEASURED: a project keeps the description Portico writes:
  `portico-beta` reads "Portico tenant Lab beta", `portico-acme`
  "Portico tenant …". The lab's hand-made `portico-lab-tenant-a` reads
  "Portico lab tenant A", so Disconnect would keep it.
- NOT MEASURED on `AZ-US-NYC1` as the lab admin: three read calls with
  NTNX-01's admin password answered 401 and then "User locked out"; that
  Prism Central's administrator password is not the lab's. The scripts
  are not to be pointed at it with `lab/env.sh`'s credentials.

### Pages

- NOT MEASURED: the tenant, site, confirmation and new-tenant pages of
  the admin UI (Task 6 of the plan): the admin login in the browser was
  not available to Claude. To look at with Marco.

## Implications for the design

- Design 4.5 (counting): floating IPs join Flow Network Security among
  the kinds the service account cannot list on pc.7.6. Prism Central's
  refusal remains the safety net for both; the role of the service
  account (`Project Manager`) could gain the floating IP read, to measure
  when Part C builds the "add a site" role.
- The retry of a failed connection is what an intermittent Prism Central
  needs: nothing else changes.
- The ownership rulings of the final review hold on pc.7.6: both marks
  are there.

## How to re-measure after an upgrade

```
export PORTICO_SECRETS=~/portico-secrets-appliance LAB_VM_IP=10.23.210.191 LAB_SSH_USER=admin \
  LAB_REALM=portico LAB_SAML_IDP=portico-keycloak LAB_PASSTHROUGH=loopback \
  LAB_SSH_OPTS="-o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no"
source lab/env.sh
lab/portico/upgrade-appliance.sh                      # warn first
lab/sites/connections.sh suspend az-us-nyc1 acme      # writes Portico's state
lab/sites/connections.sh cycle site-1 beta            # writes: ask first
lab/tenants/api.sh GET /api/v1/tenants/acme/sites/az-us-nyc1/contents
ssh admin@10.23.210.191 sudo journalctl -u portico --since -15min | grep -E 'tenant step|prism central error'
```
