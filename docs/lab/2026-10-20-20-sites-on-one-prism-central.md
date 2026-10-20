# 20 — Sites: what one Prism Central tells us before the second

## Question

Design sites §6, items 1, 2, 3 and 7, plus the version call (D5): what
the routing by site needs from Prism Central before a second Prism
Central exists. The cookies Prism Central sets on the public name and
which ones the console's JavaScript reads (D1); what the console keeps
in browser storage (D2); whether Portico's SAML identity provider can
carry a per-site `entityIssuer` (D3); how often an open console polls
(D4); where Prism Central reports its version to the service account
(D5).

## Measured on

- Prism Central: pc.7.6.0.6 (NTNX-PC-01 on NTNX-01, AOS 7.6)
- Portico: the appliance `portico-appliance` (10.23.210.191), `dev-290a6a1`
- Date: 2026-10-20, after NTNX-01 came back from a power loss of both
  Proxmox nodes (IAM answered 503 for about 25 minutes after the boot)
- By: Claude, with the scripts below

## Method

- `lab/sites/tenant-login.sh --ensure-probe acme` creates the probe user
  `portico-probe-acme@lab.portico.test` in `tenant-acme` (Keycloak admin
  API, generated password in the secrets directory, never printed).
- `lab/sites/tenant-login.sh acme --keep-jar DIR` logs it in through the
  public name with curl only, hop by hop (Portico login, Keycloak form,
  callback, landing page, `saml-start`, `initiate-login`, Keycloak's SAML
  answer, the ACS, the console) and prints every `Set-Cookie` without its
  value; a few console API calls with the same jar follow.
- `lab/sites/console-static.sh DIR` downloads the console's bundles
  (9 files, 22 MB) through Portico and counts each cookie name and every
  literal storage key.
- `lab/sites/idp-entity-issuer.sh` changes the IdP's `entityIssuer` to
  `https://prism.lab.portico.test/api/iam/authn/site-1`, renames the
  Keycloak SAML client to match, logs in, and puts both back.
- `lab/sites/pc-version.sh` reads the version from the clusters list and
  from the domain manager.

## Result

- MEASURED (D1): Prism Central sets three cookies on the public name, all
  `Path=/`: `iam-sessionid` (36 bytes, not HttpOnly, session lifetime)
  before the login; `NTNX_SESSION_ID` (36 bytes, HttpOnly, SameSite Lax,
  Max-Age 29700) at `auth_code`; `NTNX_SESSION_META` (64 bytes, HttpOnly,
  SameSite Lax, session) at the first `PrismGateway` call. None of the
  names appears in the console's bundles (14 `document.cookie` reads,
  none naming them): the console does not read them by name.
- MEASURED (D2): the console keeps per-Prism Central state in
  `localStorage`: feature flags (`flowPcDrEnabled`, `flowMigration`,
  `migrationToIAMV4Enabled`, `microsegOnVPC`), negotiated API versions
  (`@nc-ui/iam-sdk:__api_version__`, `@nc-ui/project-association:…`),
  `UserConfig`, licence and upgrade banners; `sessionStorage` holds
  navigation state. No IndexedDB.
- MEASURED (D3): the v4 IdP `PUT` accepts a new `entityIssuer` when the
  body carries `idpMetadataXml` again (the `GET` does not return it) and
  no empty `groupsDelim` (400 IAM-20011 otherwise). The SP metadata then
  names the new entity id, and a login with the Keycloak client renamed
  to it reaches the console. Reverted afterwards; a plain login works.
  Whether Prism Central refuses an assertion made for another audience
  needs a second Prism Central (Keycloak refuses a request whose issuer
  is not a client before any assertion exists): the two-site test.
- MEASURED (D5): `GET /api/clustermgmt/v4.0/config/clusters` lists Prism
  Central itself (`clusterFunction: PRISM_CENTRAL`) with
  `config.buildInfo.version` `7.6.0.6`; `GET /api/prism/v4.0/config/domain-managers`
  reports `pc.7.6.0.6`. The first is the call the service account
  already makes (write-up 07).
- NOT MEASURED HERE (D4): the console's polling needs a browser; it is
  measured in write-up 21 with the site's connected-users list.
- The scripted login needed two things the browser does silently: Prism
  Central sends its SAML request as a self-submitting **GET** form
  (redirect binding), and the form values are HTML-escaped.

## Implications for the design

- D1: `iam-sessionid` is restored without HttpOnly, as Prism Central
  sets it; every other cookie HttpOnly (`internal/proxy/stash.go`). The
  restored cookies are session cookies: Prism Central enforces its own
  session lifetime.
- D2: a switch of site sends `Clear-Site-Data: "storage"`, so a site
  never runs on the other's flags and API versions; the cost is that the
  console's dismissed banners come back after a switch.
- D3: variant A, one Keycloak SAML client per site with
  `entityIssuer = https://<prism_host>/api/iam/authn/<site-id>`
  (`perSiteEntityID = true`). An upgraded appliance's `site-1` keeps the
  shared default until its trust is recreated, which is harmless: the
  names stay distinct.
- D5: the version comes from the clusters list, as implemented.

## How to re-measure after an upgrade

With the appliance's environment (`PORTICO_SECRETS=~/portico-secrets-appliance
LAB_VM_IP=10.23.210.191 LAB_SSH_USER=admin LAB_REALM=portico
LAB_SAML_IDP=portico-keycloak LAB_SSH_OPTS="-o UserKnownHostsFile=/dev/null
-o StrictHostKeyChecking=no"`, then `source lab/env.sh`):
`lab/sites/tenant-login.sh acme --keep-jar /tmp/j`,
`lab/sites/console-static.sh /tmp/j`, `lab/sites/idp-entity-issuer.sh`
(reverts by itself; `--revert` after an interruption),
`lab/sites/pc-version.sh`. Compare the cookie table, the storage keys
and the PUT's answer with the above.
