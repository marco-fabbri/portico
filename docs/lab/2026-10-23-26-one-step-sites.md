# 26 — Sites usability part C: one-step sites on the appliance

## Question

Part C of the sites usability design
(`docs/superpowers/specs/2026-10-22-sites-usability-design.md`, 4.1–4.3,
4.8, 4.9) on the appliance:

1. An upgraded appliance serves its sites stored with verification off,
   and site changes take effect at runtime, without Apply or a restart.
2. A site stored with verification off is pinned after the MSP compares
   its fingerprint.
3. The status card reads Prism Central and Keycloak.
4. Repair after Portico's identity provider was deleted in Prism Central
   by hand, then a tenant's user signs in again.
5. A site removed with the administrator's credentials, then added again
   in one run, a tenant connected to it, its user switching between the
   two sites.
6. What Prism Central answers to the two calls Part C added (Tasks 1 and
   6 of the plan): a storage container's free space and the operations
   an account holds.

## Measured on

- Prism Central: pc.7.6.0.6, site `AZ-IT-MI1` (`site-1`, NTNX-01) and
  site `AZ-US-NYC1` (`10.23.210.185`, cluster NTNX-02)
- Portico: appliance `portico-appliance` (10.23.210.191), upgraded in
  place from `dev-1083008` to `dev-d3c8b7b` (branch
  `sites-usability-c`), then to `dev-7c3c111`, `dev-c57e167`,
  `dev-3912a1f` and `dev-87d60bd` with the four fixes this run found
  (Result 1, 4 and 5)
- Date: 2026-10-23
- By: Claude, through Portico's admin API and the scripts below, with
  each Prism Central's administrator password read from its file
  (`~/portico-secrets/prism-pw`, and `nyc1-pw` written by Marco); Marco
  gave full autonomy on the lab and its appliance. The admin API stands
  in for the pages: the setup page calls the same methods (ADR 0003)

## Method

- Appliance environment of write-up 23, then
  `lab/portico/upgrade-appliance.sh` for each build.
- Portico's journal: `journalctl -u portico` on the appliance.
- Certificates: `openssl s_client -connect <address>:9440` from the Mac,
  `openssl x509 -fingerprint -sha256`, against
  `GET /api/v1/sites/{id}/status`; then `POST /api/v1/sites/{id}/pin`
  with the compared fingerprint.
- Repair: as the lab administrator with `lab/pc.sh`, on NTNX-01, the
  SAML users of Portico's identity provider, its SAML group
  `tenant-acme` and the identity provider deleted (`If-Match` from a
  GET, in that order: IAM-21016 refuses the provider while its users or
  groups exist, write-up 15); then `POST /api/v1/sites/site-1/setup`
  with `{username, password}` written to a 0600 file from the password
  file and removed after the call.
- Removal and addition of `AZ-US-NYC1`: `GET
  /api/v1/tenants/acme/sites/az-us-nyc1/contents`, `DELETE
  /api/v1/tenants/acme/sites/az-us-nyc1` (Disconnect), `DELETE
  /api/v1/sites/az-us-nyc1` with `{username, password}`, `POST
  /api/v1/sites/inspect`, `POST /api/v1/sites` with `tls_pin` and
  `admin`, `POST /api/v1/tenants/acme/sites`; NYC1's identity providers,
  SAML users and the service account's keys read with `lab/pc.sh`
  (`PC_URL=https://10.23.210.185:9440`) before and after.
- A tenant's sign-in: `lab/sites/tenant-login.sh acme --site <id>`,
  which now picks the site on the site page as the tile's form does
  (`--site`, new), through to Prism Central's console; the console's
  Sign Out as `GET /redirect?logout` with that session's cookies.
- No restart: `systemctl show portico -p ActiveEnterTimestamp -p
  NRestarts` before and after section 5.

## Result

### 1. Upgrade and sites at runtime

- MEASURED: the upgraded appliance serves both sites, still stored with
  verification off; each build of such a site logs "tls_verify is
  false: the certificate of this Prism Central is not checked; pin it on
  the site page".
- MEASURED, a defect, fixed: on `dev-d3c8b7b` every settings save
  rebuilt both sites, and the trust checks of both answered 401 at the
  same moment, right after the backfill of the identity provider ids
  saved the document; the 30 s reload rebuilt them again and the 401s
  stopped. `Set` sealed a shallow copy of the document whose sites are
  shared with the caller's, so the change hook that serves the sites got
  the sealed keys. From `dev-7c3c111` a save rebuilds only the site it
  changed (a pin: one "site rebuilt" line) and no 401 follows.

### 2. Pinning a site stored with verification off

- MEASURED: `AZ-US-NYC1`'s status has trust `unverified` and fingerprint
  `EC:D9:49:3C:…:58:E4:1B:E8`, the one `openssl` reads from the Mac:
  Prism Central's factory certificate (`CN=prism.nutanix.local`, valid
  to 2036-10-21). The pin call answers 200 with `tls_pin` set; the
  status then has trust `pinned`, the site is rebuilt alone, and the
  warning is gone.
- MEASURED: the same for `AZ-IT-MI1` (`1F:36:CC:2E:…:F1:18:A2:A7`).

### 3. The status card

- MEASURED: both sites answer four ok parts: connection (version
  7.6.0.6, trust `pinned`), service account ("the key is accepted"),
  SAML trust ("identity provider and Keycloak client present"), defaults
  ("cluster and storage container present").

### 4. Repair

- MEASURED: the four deletions on NTNX-01 answered 204 within four
  seconds. The next status call answered `incomplete`, reason "the
  identity provider portico-keycloak is missing in Prism Central",
  action Repair, and the monitor took it: acme's choice of `AZ-IT-MI1`
  on the site page answered 503 with the "not available" page;
  `AZ-US-NYC1` stayed available.
- MEASURED, a defect, fixed: the first Repair (`dev-d3c8b7b`) stopped at
  the version step after 69 s, "Prism Central answered 504: upstream
  request timeout" (NTNX-01's IAM is slow for a few minutes after an
  identity provider is deleted, write-up 15), yet answered
  `complete: true` and bound acme again, which failed with "404 Failed
  to get SamlIdentityProvider as Connector not found". `complete` came
  from the site's row, complete before the run. From `dev-c57e167` it
  means the run passed its last step; a later run stopped by the same
  504 answered `complete: false` and touched no tenant.
- MEASURED: the second Repair took 9 s, all eight steps ok: identity
  provider created again (Prism Central gives it the same extId,
  derived from the name, so `idp_changed` is false), Keycloak client in
  place, cluster and container kept, then "tenant acme: bound to
  identity provider 09a71928-…".
- MEASURED: the service account step made a new key, although the
  stored one still worked: `site-1` was set up before Part C and has no
  service account id recorded, and the setup keeps a stored key only
  for the account it recorded (a key pasted by hand may be another
  account's). The old key `portico-20261019-163536` stayed valid in
  Prism Central, held by nobody, and was revoked by hand (204). Backlog.
- MEASURED, a defect, fixed: acme's first sign-in after the Repair was
  refused on the site page: the monitor kept the site incomplete until
  its next trust check, every five minutes, or a status call. From
  `dev-3912a1f` a complete run reports the site to the monitor. After
  the status call acme's user reached Prism Central's console (200).
- MEASURED: Finish setup on the complete site (`dev-3912a1f`) took 7 s:
  identity provider "present and is reused", service account "the
  stored key is accepted" (kept), SAML trust "in place", defaults kept;
  nothing was created.

### 5. Removal and the success scenario

- MEASURED: acme's project on `AZ-US-NYC1` counted empty (floating IPs
  and Flow security policies not counted, 403 to the service account as
  in write-up 25). Disconnect answered 202 and the connection was gone
  within 25 s; acme stayed active on `AZ-IT-MI1`.
- MEASURED, a defect, fixed: the first removal with NYC1's
  administrator credentials (`dev-3912a1f`, 4 s) deleted the Keycloak
  client and left the identity provider as "not deleted, connection
  failed". Prism Central had answered 400 "Failed to delete
  SamlIdentityProvider as users or user groups associated with the
  directory service are still present or in use": the SAML users it
  made at the sign-ins of carol and of the probe user. It also left the
  key ("its id was not recorded": the site was set up before Part C,
  write-up 23) and the service account. From `dev-87d60bd` the removal
  deletes the provider's SAML users first, counted and never named, and
  a deletion Prism Central refuses is left with Prism Central's answer.
  The key it could not name, `portico-20261021-150023`, was revoked by
  hand (204).
- MEASURED: `POST /api/v1/sites/inspect` answers `trusted: false` with
  the fingerprint compared in section 2. The site added again in one run
  (`dev-87d60bd`) took 12 s, every step ok: the provider the first
  removal left was "present and is reused", a new key, the Keycloak
  client created, defaults cluster NTNX-02 and its
  `default-container-*`.
- MEASURED: removed again with the credentials in 7 s: deleted "the
  identity provider's 2 SAML users (made by Prism Central at sign-in)",
  the identity provider, the key by its id and the Keycloak client; left
  only the service account. NYC1 then held no identity provider, no
  SAML user and no key.
- MEASURED, the success scenario: `AZ-US-NYC1` added from nothing in
  10 s, every step ok ("no identity provider yet: Portico creates
  portico-keycloak"); acme connected to it, active in 7 s; acme's user
  signs in on each site through to Prism Central's console (200); the
  Sign Out of NYC1's console lands on the site page in one redirect,
  both tiles available and the Portico session kept; both sites answer
  four ok parts with trust `pinned`.
- MEASURED: no Apply and no restart: `ActiveEnterTimestamp` stayed at
  15:40:02 UTC (the last upgrade) across the removals, the additions and
  the connection, `NRestarts=0`.
- MEASURED, on the way: from 15:31 to about 15:40 UTC NTNX-01 did not
  answer the probe (timeouts); `AZ-IT-MI1` turned unreachable after three
  failed probes and available again with the next answer, while
  `AZ-US-NYC1` was unaffected.

### 6. Tasks 1 and 6

- MEASURED (Task 1, NTNX-01, as the lab administrator): `GET
  /api/clustermgmt/v4.0/stats/storage-containers/{extId}` needs
  `$startTime` and `$endTime`; with `$samplingInterval=300` and
  `$statType=LAST` it answers `storageFreeBytes` as a list of
  `{value, timestamp}`. The setup's defaults step reads it for each
  candidate container.
- MEASURED (Task 6, MI1, read-only): the operations Portico's creations
  use are `Create_User`, `Create_User_Key`,
  `Update_Authorization_Policy` and `Create_Saml_Identity_Provider`
  (`Create_Role` and `Update_Role` exist too).
  `GET iam/v4.0/authz/operations` accepts `$filter=displayName in
  ('A','B')` and `eq … or eq …`; an authorization policy names a user
  in `identities[].identityFilter.user.uuid.anyof`; `GET
  authz/roles/{id}` returns its operations as extIds (Super Admin:
  1397). The rights step found all four on the lab administrator.

### Not measured

- NOT MEASURED: a renewed certificate on a pinned site (no certificate
  was renewed); the unit tests of Tasks 2 and 5 cover it.
- NOT MEASURED: the pages in the browser (layout and wording of "Add a
  site", the status card, Remove): the admin UI's sign-in needs the
  break-glass password typed in the browser, which is Marco's step; the
  web handler tests cover what the pages call.

## Implications for the design

The sites design's removal paragraph now says the provider's SAML users
are deleted first (Result 5). Four defects fixed (Result 1, 4 and 5).
Backlog: a site set up before Part C gets a new key at its first Finish
setup or Repair, and its removal cannot name its key; either way the old
key stays valid in Prism Central until revoked by hand.

## How to re-measure after an upgrade

```bash
source lab/env.sh
export PORTICO_SECRETS=~/portico-secrets-appliance LAB_VM_IP=10.23.210.191 LAB_SSH_USER=admin \
  LAB_REALM=portico LAB_SAML_IDP=portico-keycloak LAB_PASSTHROUGH=loopback \
  LAB_SSH_OPTS="-o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no"
lab/portico/upgrade-appliance.sh
lab/tenants/api.sh GET /api/v1/sites/site-1/status          # four parts
echo | openssl s_client -connect 10.23.210.175:9440 2>/dev/null | openssl x509 -noout -fingerprint -sha256
lab/sites/tenant-login.sh acme --site site-1                # final: 200 …/console/
lab/sites/tenant-login.sh acme --site az-us-nyc1
```

Repair needs Portico's identity provider deleted in Prism Central with
its SAML users and groups first (Method), then `POST
/api/v1/sites/{id}/setup` with an administrator's credentials from a
file, or Repair on the site's page. Removal and addition: Disconnect the
site's tenants, `DELETE /api/v1/sites/{id}` and `POST /api/v1/sites`
with `{username, password}` written from the password file to a 0600
file, as in Method, then read the provider, the SAML users and the keys
in that Prism Central with `lab/pc.sh`.
