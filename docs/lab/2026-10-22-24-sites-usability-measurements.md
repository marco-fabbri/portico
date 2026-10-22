# 24 — Sites usability: measurements and part A on the appliance

## Question

Section 6 of the sites usability design
(`docs/superpowers/specs/2026-10-22-sites-usability-design.md`):

1. What a Projects 2.0 project can hold on pc.7.6, how to list each kind
   by project with v4, and whether Prism Central refuses to delete a
   project that still holds something.
2. Whether an administrator's account can read its own rights through
   the API, for the pre-check of "add a site".
3. How the v4 clusters API tells a Nutanix Objects storage container
   apart.
4. Keycloak's SAML logout towards Prism Central: what it calls and why
   it fails.
5. Where Prism Central shows its certificate's fingerprint.

And part A of the work on the appliance: the console's sign-out with
several sites, the branded page for a failed SAML start, the full
sign-out.

## Measured on

- Prism Central: pc.7.6.0.6 (NTNX-01, site `AZ-IT-MI1`); the second lab
  Prism Central (site `AZ-US-NYC1`, `10.23.210.185`) for the certificate
- Portico: appliance `portico-appliance` (10.23.210.191), upgraded in
  place from `dev-9c6c7c4` to `dev-f2797d5` (branch `sites-usability`);
  Keycloak 26.3.5
- Browser: Chrome 155.0.8059.85 on macOS 27.0.1, driven by Claude through the
  Chrome extension, as the lab tenant user of `acme`
- Date: 2026-10-22
- By: Claude, with the scripts below; Marco approved the writes

## Method

- `lab/sites/project-contents.sh [PROJECT]` (new, read-only): per kind, a
  v4 list with `$filter=projectExtId eq '<project>'` (and two other
  filter spellings when that one is refused), plus a client-side count of
  list items naming the project. Run on `portico-acme` and, as a positive
  control, on `_internal`.
- `lab/sites/project-contents.sh --scratch` (writes, Marco's OK): a
  project `portico-measure-<time>` as the code creates one (id, then a
  resource group on NTNX-01's `default-container`), a powered-off VM with
  no disk in it, a `DELETE` of the project, then the VM, the resource
  group and the project removed (trap).
- `lab/sites/admin-rights.sh` (new, read-only): "who am I" candidates,
  the user by name, the authorization policies naming it, the roles'
  operations joined with the operations list.
- `lab/sites/container-kinds.sh` (new, read-only): every storage
  container with the fields that could mark it.
- Keycloak's journal on the appliance (`journalctl -u keycloak`) and its
  SAML clients (`lab/saml/kc.sh GET /portico/clients?protocol=saml`).
- `openssl s_client -connect 10.23.210.185:9440 | openssl x509
  -fingerprint -sha256`.
- In the browser: the tenant login, `AZ-IT-MI1`, the console's sign-out
  tail `/redirect?logout=…` opened directly (the console's user menu is
  not reachable by the extension), `AZ-US-NYC1`, the site page's Sign
  out, then Keycloak's journal.

## Result

### 1. What a project holds

- MEASURED: `$filter=projectExtId eq '<extId>'` is accepted (200) on
  pc.7.6 for VMs, images, templates and OVAs (`vmm/v4.0`), volume groups
  (`volumes/v4.0`), subnets, VPCs and floating IPs (`networking/v4.0`),
  categories (`prism/v4.0`) and recovery points (`dataprotection/v4.0`).
- MEASURED: the filter is applied, not ignored, for VMs, images, volume
  groups, subnets and categories: on `_internal` it counts 3 VMs, 2
  images, 3 volume groups, 1 subnet, 45 categories; on `portico-acme` 0
  of each while the unfiltered lists hold 4, 2, 3, 1 and 45; on the
  scratch project it counts its one VM. Templates, OVAs, VPCs, floating
  IPs and recovery points were empty on NTNX-01, so for them "applied" is
  not shown. Floating IPs answered 504 once (on `_internal`) and 200 on
  the other two runs.
- MEASURED: the Flow Network Security lists (`microseg/v4.0` policies,
  address groups, service groups) answer 403 to the lab admin, filtered
  or not: they cannot be counted here.
- MEASURED: Prism Central refuses to delete a project that holds a VM:
  the `DELETE` answers 202 and its task ends `FAILED` (`TSKS-20801`, a
  legacy error); the project stays. Write-up 05 measured the same refusal
  for a project still sharing the SAML identity provider
  (`associatedEntities={"shared saml identity providers":1}`). After the
  VM was removed, the resource group and the project were deleted.
- NOT MEASURED: the same counts with the service account's key (no key
  exists outside Portico's sealed store; Part B measures it through
  Portico's code). Flow Network Security entities in a project.

**Decision for Part B.** Disconnect counts, with the filter above, VMs,
images, templates, OVAs, volume groups, subnets, VPCs, floating IPs,
categories and recovery points owned by the project, and leaves out what
the MSP shares in and what Portico created. A kind that answers 403 or
400 (Flow Network Security here) is not counted; Prism Central's refusal
to delete a non-empty project is the safety net (design 4.6), reported as
"the project still holds something". No administrator's credentials are
needed for Disconnect.

### 2. An administrator's own rights

- MEASURED: no "who am I" endpoint: `authn/users/$me`, `users/current`
  (iam v4.0 and v4.1.b3) answer 400.
- MEASURED: an administrator can find itself by name
  (`authn/users?$filter=username eq '<name>'`), list the authorization
  policies and read its roles' operations (1397 operations listed). The
  lab admin's Super Admin role holds all 1397; 72 of them are on users,
  roles and identity providers, among them `Create_User`, `Update_Role`,
  `Create_Saml_Identity_Provider`, `Share_Saml_Identity_Provider` (the
  script prints the first 40 per role, so the name of the role creation
  operation was not read).
- Limit: a policy that names the account through a directory group is
  not found by the user's extId; an account without the right to read
  policies and roles cannot check itself.

**Decision for Part C.** The pre-check of "add a site" is best effort:
find the account, its policies and roles, and look for `Create_User`,
`Update_Role`, `Create_Saml_Identity_Provider` and the role creation
operation (its name read when Part C is built); when it
cannot read them, say so and go on. The creations, in an order where a
refusal leaves nothing behind, remain the authoritative check.

### 3. Nutanix Objects containers

- MEASURED: no field marks the Objects container on NTNX-01: owner,
  internal, shared, encryption flags are the same as the
  `default-container`'s; it has compression on and the name
  `objectsl889411dccd9a44125aa3e31c2596dcf9`. The system containers carry
  `isInternal=true` (`NutanixManagementShare`, `NutanixMetadataContainer`);
  `SelfServiceContainer` does not.

**Decision for Part C.** The default container is `default-container-*`
when the cluster has one; otherwise the one with the most free space,
leaving out `isInternal`, the existing name list (`SelfServiceContainer`,
`NutanixManagementShare`, `NutanixMetadata…`) and names matching
`^objects[a-z0-9]{20,}$`. The MSP can still choose any container from
the menu.

### 4. Keycloak's SAML logout

- MEASURED: at a full sign-out Keycloak logs out every client of the
  session; for Prism Central's SAML clients it attempted a backchannel
  logout (`SamlProtocol.backchannelLogout`) to the client's admin URL,
  `https://prism.lab.portico.test/api/iam/authn/callback`, which failed
  with `UnknownHostException` inside the container.
- Making the name resolvable would not help: the request would reach
  Portico's public listener without a Portico session and be refused,
  as the public path must (fail closed).
- Fixed in `dev-f2797d5`: the SAML client carries no admin URL; the two
  existing clients were updated in Keycloak. MEASURED after it: a full
  sign-out from the site page ends on Keycloak's login page, and the
  journal shows `Can't do backchannel logout. No SingleLogoutService …`
  warnings and no `UnknownHostException`. Prism Central's
  cookies are expired by Portico's logout, as before.
- NOT MEASURED: whether the console's own Sign Out ends the Prism
  Central session before it redirects to `/redirect?logout` (the button
  was not clicked; the tail was opened directly).

**Decision for Task 4 of Part A:** (b), done.

### 5. Certificate fingerprint

- MEASURED: `AZ-US-NYC1` presents the factory certificate,
  `CN=prism.nutanix.local`, O=Nutanix Inc., valid until 2036-10-21,
  SHA-256 fingerprint `EC:D9:49:3C:6E:48:E6:6D:63:2A:B1:74:47:B1:DA:AD:
  57:44:DC:62:AE:EB:41:BC:DB:2E:61:E8:58:E4:1B:E8`.
- NOT MEASURED: whether Prism Central's settings show the fingerprint
  (needs the Prism Central administrator's login, which is Marco's).

**Decision for Part C.** The page shows the fingerprint in the same
colon-separated upper-case form `openssl` and browsers use, and tells the
MSP to compare it with the browser's certificate viewer on the Prism
Central address (Chrome: the padlock, "Certificate is not valid",
Details, SHA-256 fingerprint).

### Part A on the appliance

- MEASURED: after the login the tenant of `acme` (two sites) gets the
  site page; `AZ-IT-MI1` opens the console; the console's sign-out tail
  returns to the site page with the Portico session still valid;
  `AZ-US-NYC1` then opens with no password asked; the site page's Sign
  out ends on Keycloak's login page.
- NOT MEASURED: going back to `AZ-IT-MI1` after `AZ-US-NYC1` (the
  silent SAML round trip to the site that was signed out; unit test of
  the stash only); the console's sign-out with one site (the lab has one
  tenant, on two sites; unit test only); the "responding slowly" page
  (Prism Central was not slow during the session; unit tests only);
  Safari.

## Implications for the design

- Design 4.6: Prism Central's refusal to delete a non-empty project
  exists, so Disconnect never needs an administrator's credentials.
- Design 4.1 step 1: the pre-check exists but is best effort.
- Design 4.1 step 4: Objects containers are told apart by name only.
- Design 4.7: the fix is to stop Keycloak's backchannel logout, not to
  make the name resolvable.

## How to re-measure after an upgrade

```
source lab/env.sh
lab/sites/project-contents.sh portico-acme
lab/sites/project-contents.sh _internal          # positive control
lab/sites/project-contents.sh --scratch          # writes: ask first
lab/sites/admin-rights.sh
lab/sites/container-kinds.sh
echo | openssl s_client -connect <pc>:9440 2>/dev/null | openssl x509 -noout -fingerprint -sha256
# appliance: as write-up 23, then lab/portico/upgrade-appliance.sh (warn first)
lab/saml/kc.sh GET "/portico/clients?protocol=saml" | jq '.[] | {clientId, adminUrl}'
# browser: tenant login, site 1, /redirect?logout=…, site 2, site page Sign out;
# then on the appliance: journalctl -u keycloak --since "-10 min" | grep -i logout
```
