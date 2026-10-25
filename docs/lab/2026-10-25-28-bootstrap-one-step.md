# 28 — The bootstrap script on the one-step site setup, the scripts on the appliance

## Question

Write-up 27 found that `lab/portico/bootstrap-acceptance.sh` still added
its site with the Milestone 2e two-step path (`tls_verify: false`, then
`bootstrap/prism-central` and `bootstrap/saml`), which a current build
refuses: a new site needs a trusted or pinned certificate. The same
sweep moved the lab scripts' defaults to the appliance (the VM of
Milestones 0 to 2 is retired) and took the word "lab" out of every name
the system shows: the probe user's address, the test users' addresses,
the project safety rule, the identity provider and realm defaults. Does
the script, rewritten on the one-step path, set a fresh appliance up to
the end, and do the renamed scripts still reach it?

## Measured on

- Prism Central: pc.7.6.0.6 on both sites (`AZ-IT-MI1`, NTNX-01
  10.23.210.175; `AZ-US-NYC1`, 10.23.210.185)
- Portico: appliance `portico-appliance` (10.23.210.191) on
  `dev-1d6ac38` (main after Milestone 5); the scripts of this branch
- Date: 2026-10-25, 22:28–22:40 CEST
- By: Claude, through the admin API and the lab scripts

## Method

`source lab/env.sh` with `PORTICO_SECRETS=~/portico-secrets-appliance`
and `LAB_SSH_OPTS` for the appliance's host key only: address, SSH
user, portal names, realm, identity provider name, project prefix and
loopback pass-through are now the defaults. Passwords from the secrets
files only, in JSON bodies built with `jq --rawfile` in a private temp
dir.

1. Teardown with the product's own flow, as in write-up 27: branding
   saved, acme disconnected from each site in turn
   (`DELETE /api/v1/tenants/acme/sites/{site}`), the tenant deleted,
   each site removed with its administrator's credentials
   (`DELETE /api/v1/sites/{id}`), the realm `portico` deleted
   (`lab/saml/kc.sh DELETE /portico`), Portico stopped, the store moved
   to `/var/lib/portico/portico.db.pre-write-up-28-20261025-223029`
   (master key kept), Portico started, the break-glass password set from
   its file.
2. `lab/portico/bootstrap-acceptance.sh` (full run): hosts, Keycloak
   bootstrap, tenants card, then the site `AZ-IT-MI1` in one step
   (`POST /api/v1/sites/inspect`, `POST /api/v1/sites` with `tls_pin`,
   `service_account` and `admin`), checks, Apply, branding.
3. Marco's branding back (`lab/portico/demo-branding.sh --restore`), the
   site `AZ-US-NYC1` in one step by hand with its administrator's
   credentials, acme on both sites (`POST /api/v1/tenants`),
   `GET /api/v1/tenants/acme/visibility`.
4. `lab/sites/tenant-login.sh --ensure-probe acme` (the probe user now
   `portico-probe-acme@portico.test`), then `tenant-login.sh acme --site`
   on each site; `carol@acme.example` created again
   (`POST /api/v1/tenants/acme/users`).

## Result

- MEASURED: the teardown took two minutes end to end: each disconnect
  30 s (NTNX-01 fast this time; write-up 27 had 8 minutes), the tenant
  delete 6 s, the removal of `AZ-IT-MI1` 35 s and of `AZ-US-NYC1` 5 s,
  each listing the identity provider's SAML users (1 and 2), the
  identity provider, the service account's key and the Keycloak client.
- MEASURED: `bootstrap-acceptance.sh` passed every line in 24 s: the
  Keycloak bootstrap took 3 s (76 s in write-up 27, where it was the
  realm's first creation after a Keycloak restart), the tenants card
  left `missing=sites`, `sites/inspect` returned the fingerprint, the
  one-step `POST /api/v1/sites` answered 201 in 8 s with the eight steps
  `ok` (certificate pinned `1F:36:…:A2:A7`, pc.7.6.0.6, rights,
  identity provider `portico-keycloak` created, service account
  `portico` found with a new key, SAML trust both ways, defaults
  cluster NTNX-01 and `default-container-44365534254097`, live), the
  site read back with `api_key_set=true`, the identity provider name
  and the defaults stored, settings complete, the three checks ok,
  Apply 202 and the service back `running`.
- MEASURED: `AZ-US-NYC1` in one step: 201 in 10 s, eight steps `ok`
  (pin `EC:D9:…:1B:E8`, cluster NTNX-02).
- MEASURED: acme on both sites ended `failed` after 210 s: on
  `AZ-IT-MI1` the project step took 79 s and the resource-group step was
  then refused by NTNX-01's gateway with `401 AUTHENTICATION_REQUIRED
  "Intent Gateway Login Required"` (the visibility read at that moment
  got `504 upstream request timeout`); on `AZ-US-NYC1` every step passed
  in 2 s. `POST /api/v1/tenants/acme/provision` two minutes later made
  the tenant `active` in 11 s (the project step `ok` at once, the
  resource group created), visibility `ok` on both sites. The 401 is
  not a wrong key: the same key had just created the project and
  created the resource group on the retry. NTNX-01's IAM is known to
  answer intermittently under load (write-up 25); Portico's step retry
  of Milestone 5 covers 500, 502 and 503 only, so a 401 or a 504 from
  the gateway fails the tenant and leaves the retry to the MSP. Backlog.
- MEASURED: the probe user `portico-probe-acme@portico.test` was created
  (201) and added to `tenant-acme` (204); its login through
  `https://msp.portico.test/` reached `/console/` (200) on each site
  in 19 hops, as in write-up 27.
- MEASURED: `POST /api/v1/tenants/acme/users` answered 409 while the
  tenant was `failed` and 201 once it was `active`: carol's temporary
  password is in `~/portico-secrets-appliance/carol-tmp-pw`.
- MEASURED: the offline checks (`lab/test_scripts.sh`) pin the new
  defaults: `env.sh` names the appliance, the scripts that reach it
  carry no name of the retired VM, `bootstrap-acceptance.sh` uses
  `sites/inspect` and `tls_pin` and no two-step call.

## Implications for the design

- None for the product. The install guide of Milestone 6 can follow
  `bootstrap-acceptance.sh` step by step: names, Keycloak, tenants card,
  one site in one step, checks, Apply.
- The tenant provisioning's retry (design §4.4, Milestone 5) treats a
  gateway 504 and a 401 on a key that just worked as refusals. On a
  Prism Central whose IAM hiccups under load the MSP sees a failed
  tenant and a "Retry" that then succeeds at once. Backlog: retry a 504
  on a create step when the entity does not exist yet, and a 401 once
  after a short pause.

## How to re-measure after an upgrade

1. Teardown as in Method step 1 (or `lab/appliance/destroy.sh` and a new
   appliance from the image).
2. `source lab/env.sh && lab/portico/bootstrap-acceptance.sh` on the
   fresh appliance: expect PASS, the one-step line `201 az-it-mi1
   complete=true failed=0`.
3. `lab/sites/tenant-login.sh --ensure-probe acme` and
   `lab/sites/tenant-login.sh acme --site az-it-mi1`: expect the last
   hop `200 https://msp.portico.test/console/`.
