# 15 — Fresh-appliance bootstrap and setup mode (Milestone 2e)

## Question

Design §4.4, §4.5 and §4.9 (setup mode and drafts): a fresh appliance
(process YAML, empty store) must start in setup mode, let the
administrator save the setup cards one by one, create the `portico`
service account and its key in Prism Central from one-time credentials,
register the Keycloak realm and the two clients from the Keycloak
administrator's one-time credentials, create the SAML identity provider
and its Keycloak client, and then apply. Which of these steps the service
account can do by itself (open item of write-up 07), how long each takes,
and what a reset of the lab to that fresh state involves.

## Measured on

- Prism Central pc.7.6.0.6, Keycloak 26.3.5 on the lab VM
- Portico `dev-d4ebf68` (branch `m2e-bootstrap`; the first run used
  `dev-64eb57a`), Rocky Linux 9.8
- Date: 2026-10-18
- By: Claude (Marco's session) with `lab/portico/reset-for-bootstrap.sh`,
  `lab/portico/deploy.sh --fresh`, `lab/portico/bootstrap-acceptance.sh`,
  `lab/tenants/acceptance.sh`, Chrome on the Mac

## Method

1. `lab/portico/bootstrap-acceptance.sh --refusal-only` against the
   Milestone 2d appliance, before any reset: the SAML bootstrap with a
   different identity-provider name must refuse (no adoption).
2. `lab/portico/reset-for-bootstrap.sh --yes` (prints its six-step plan
   and refuses without `--yes`): stop the service and move the store
   aside, remove tenant A's role memberships and SAML group and unshare
   the IdP, delete the IdP, delete the service account and its keys,
   delete the Keycloak realm, remove the three stale secret files on the
   Mac. Tenant A's project and VM stay.
3. `lab/portico/deploy.sh --fresh`: no secret file, no import; read the
   journal line.
4. `lab/portico/bootstrap-acceptance.sh`: every API step of the setup in
   order, with timings: hosts card as a draft, apply refused, Keycloak
   bootstrap, Prism Central bootstrap, tenants card, SAML bootstrap with
   the service account and (on refusal) with the administrator's
   credentials, the three checks, apply, wait for `mode=running`,
   branding.
5. `lab/tenants/acceptance.sh` (Milestone 2a acceptance on the
   bootstrapped appliance), Chrome on the public and the admin path,
   `lab/tenants/acceptance.sh --delete`.

## Result

- MEASURED, refusal: `POST /api/v1/settings/bootstrap/saml` with
  `name=portico-lab-other` on the Milestone 2d appliance answered
  `409 an identity provider named portico-lab-keycloak already exists:
  Portico does not adopt an existing one`.
- MEASURED, reset: deleting the identity provider was refused with
  `IAM-21016 Failed to delete SamlIdentityProvider as users or user
  groups associated with the directory service are still present` while
  the two SAML users Prism Central had created at alice's and carol's
  first logins existed, although their memberships and groups were gone.
  `lab/saml/pc-idp.sh delete` now deletes the IdP's SAML users (filter
  `userType eq SAML`, `idpId` equal to the IdP) before the IdP; the
  second run went through. The store backup is
  `/var/lib/portico/portico.db.pre-2e-20261018-163750` (the Milestone 2d
  document with tenant A); the master key stayed.
- MEASURED, fresh start: the journal says `settings source=none` with the
  process-config validation message, then `setup mode: the public and
  pass-through listeners are off` with the missing list (`prism_host`,
  `id_host`, `prism_central.url`, `prism_central.api_key`,
  `prism_central.saml_idp_name`, `keycloak.realm`,
  `keycloak.admin_client_secret`, `oidc.issuer`, `oidc.client_secret`,
  `tenants.cluster_ext_id`, `tenants.storage_container_ext_id`: 11 of 17, the six
  with defaults are not listed), then `listening admin` only. Port 443
  and 9440 do not answer. The first build logged `missing=null` on an
  empty store (the list was only computed for a stored draft): fixed,
  `TestServeSetupModeWithEmptyStore` asserts the line.
- MEASURED, first bootstrap run (`dev-64eb57a`, about two minutes after
  the reset): Keycloak bootstrap **27 s** (realm, `portico-admin` with
  its eight realm-management roles, `portico`); Prism Central bootstrap
  **failed after 121 s**: `GET /authn/users` took 58 s, `POST
  /authn/users` 48 s, and `POST .../keys` hit the bootstrap's two-minute
  deadline, so the answer said `Prism Central at
  https://10.23.210.175:9440 did not answer: timeout`. The key existed in
  Prism Central (`portico-20261018-164245`, VALID) with nobody holding
  it. From the Mac, three minutes later, the same calls took one second:
  IAM is slow for a few minutes after an identity provider and a service
  account are deleted, not in general. Fixed: a failed key creation now
  lists the account's keys and revokes the one with the name Portico
  chose (`TestPrismBootstrapRevokesAKeyLostInTransit`), and the
  revocation after a later failure runs on its own 30 s context, since
  the request's context is usually what expired
  (`TestPrismBootstrapRevokesTheKeyWithACancelledContext`).
- MEASURED, second run (`dev-d4ebf68`, reset repeated, IAM answering in
  1 s): `bootstrap-acceptance.sh` PASS in **41 s** end to end. Keycloak
  bootstrap **2 s** (`created realm, client portico-admin, client
  portico`); Prism Central bootstrap **3 s** (`created service account
  portico, api key portico-20261018-165057, policy member; Prism Central
  answered: 1 cluster(s), 3 storage container(s)`, key expiry 100 years);
  after the tenants card the only missing key was
  `prism_central.saml_idp_name`.
- MEASURED, answer to write-up 07's open item: the SAML bootstrap with
  the service account's key answered `Prism Central refused the service
  account (403): give the credentials of an account that can manage
  identity providers`: a member of `Project Manager_acp` cannot create a
  SAML identity provider. With the administrator's credentials the step
  took **17 s** (`created identity provider portico-lab-keycloak,
  keycloak client https://prism.lab.portico.test/api/iam/authn, mappers
  email, groups`, `used_credentials: true`), entityID
  `https://prism.lab.portico.test/api/iam/authn`, ACS
  `.../api/iam/authn/callback`.
- MEASURED: the recreated objects got the extIds they had before the
  reset (service account `c22f4a4b-4624-5e84-bff1-ea5e719aa1e5`, IdP
  `247f2c3d-4566-52fe-84ed-48b14799bf05`): Prism Central derives them from
  the name. Settings after the three bootstraps: `complete: true`,
  `missing: []`; the three checks green; apply 202; the listeners were
  back **2.2 s** after `exiting for the restart`; `mode=running`;
  branding PUT 200 (the Keycloak theme was pushed to the new realm by
  the save).
- Fixed in the review pass, with tests: each bootstrap now stores what
  it set through `Service.Patch` (read, change, write under a lock), so
  a card saved while a bootstrap runs, or two bootstraps started from
  the same page, no longer revert each other
  (`TestKeycloakBootstrapKeepsASaveMadeMeanwhile`); the store step runs
  inside the bootstrapper on a context of its own, and a Prism Central
  key that could not be stored is revoked at once instead of staying
  valid with nobody holding it (`TestPrismBootstrapRevokesTheKeyWhenTheStoreFails`).
- MEASURED: `lab/tenants/acceptance.sh` PASS on the bootstrapped
  appliance: provisioning of tenant-b **11 s**, seven steps `ok`,
  visibility `ok`, carol created, disabled and enabled.
- MEASURED, browser: `https://prism.lab.portico.test/` with no session
  lands on the Keycloak login page of the new realm, branded (name,
  colour, logo, tagline). Marco's observation on that page: the realm
  display name "Portico Lab MSP" is drawn across the logo instead of
  under it (the theme CSS in `internal/branding/theme.go` puts the logo
  as a background of `#kc-header-wrapper` with a top padding, which
  Keycloak 26.3's own header rules do not honour). Deferred: a theme
  fix, to re-check in Chrome. The setup page in setup mode (same commit,
  local instance with an empty store) shows the banner "Setup mode: the
  public listener is off until every card is saved and applied", the
  notice "No settings yet", the defaults prefilled (`portico-admin`,
  `portico`, service account `portico`), the three bootstrap forms under
  their cards, and any tenant page answers the "Setup mode" page with a
  link to the setup. Carol's full login through the public path was
  Marco's check, as in write-up 11: user name `carol@lab.portico.test`
  (the user name is the email, so a login typed as `carol` answers
  "invalid username or password": Keycloak's `user_not_found`), password
  change required at first login, then the landing page and the Prism
  Central console with the empty `portico-lab-tenant-b` project and no
  VM (tenant A's VM is out of scope). Two observations, deferred: the
  realm created by the bootstrap has no password policy, so a weak
  password was accepted (the Milestone 0 lab realm had none either);
  and a login that takes longer than the 10-minute login-state cookie
  (the page had been open for a while) answers a bare `login state
  missing or expired, start again` on `/.portico/callback`, and the
  user has to open the public URL again, which then goes straight
  through on the Keycloak session. `acceptance.sh --delete` PASS.

## Implications for the design

- §4.5: the service account's policy (`Project Manager_acp`) covers
  projects, role memberships and the visibility check, not identity
  providers. The SAML trust step needs an account that can manage
  identity providers, once; Portico asks for it only after the service
  account was refused, and never stores it. Recorded in §4.5.
- Reset and re-bootstrap of the same Prism Central: deleting an
  identity provider first needs its SAML users deleted (they survive the
  removal of memberships and groups), and IAM answers slowly for a few
  minutes afterwards. A bootstrap that times out leaves nothing behind
  now; retrying a few minutes later works. A future "remove the trust"
  action (step 2, brownfield) must delete the SAML users first.
- Setup mode behaves as §4.9 describes: one listener, drafts card by
  card, apply refused until complete, then one restart.

## How to re-measure after an upgrade

```
source lab/env.sh
lab/portico/bootstrap-acceptance.sh --refusal-only   # on a configured appliance
lab/portico/reset-for-bootstrap.sh --yes             # destructive, prints the plan first
lab/portico/deploy.sh --fresh                        # journal: setup mode
lab/portico/bootstrap-acceptance.sh                  # every line ok, timings printed
lab/tenants/acceptance.sh                            # tenant-b active, carol created
# browser: https://prism.lab.portico.test/ as carol (password in ~/portico-secrets/carol-tmp-pw)
lab/tenants/acceptance.sh --delete
```

Wait two or three minutes between the reset and the bootstrap, or expect
the Prism Central step to time out once and retry it.
