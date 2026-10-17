# 04 — SAML between Prism Central and Keycloak behind a different host name (design 6.1, 6.8)

## Question

Prism Central believes it is `10.23.210.175`; the public name is
`prism.lab.portico.test`. Does the SAML round trip survive, which Prism
Central setting fixes the external name, and can the IdP be configured
through the API?

## Measured on

- Prism Central: pc.7.6.0.6; Keycloak 26.3.5 on the lab VM; nginx stand-in
  with no policy; Chrome on the Mac with the lab CA trusted and the
  `/etc/hosts` line for both names
- Date: 2026-10-17
- By: Claude (Marco's session) with `lab/saml/*.sh`, then Chrome via the
  Claude in Chrome extension

## Method

1. Read the SP metadata directly and through the proxy.
2. `lab/saml/keycloak-realm.sh realm|group|user` (realm `portico-lab`,
   group `tenant-a`, user `alice@lab.portico.test`).
3. `lab/saml/pc-idp.sh create https://prism.lab.portico.test` (IdP from the
   realm descriptor, inline base64, with `redirectURL`).
4. `lab/saml/keycloak-realm.sh saml-client <entityID> <ACS>`.
5. Project `portico-lab-tenant-a` bound to group `tenant-a` as Project
   Admin (write-up 05), IdP shared with the project.
6. Browser: `https://prism.lab.portico.test/console/` → "Log in with
   portico-lab-keycloak" → Keycloak form → console.

## Result

- MEASURED: the **global** SP metadata (`GET /api/iam/v4.0/authn/saml-sp-metadata`)
  carries the IP in `entityID` and in the ACS `Location`, whatever the
  `Host` header says. Through the proxy it is identical.
- MEASURED: the v4.0 create schema rejects `redirectURL`
  ("property redirectURL is unsupported"); `POST /api/iam/v4.1.b3/authn/saml-identity-providers`
  accepts it. Supported IAM versions reported by the server:
  `v4.0, v4.0.b1–b3, v4.1.b1–b3`.
- MEASURED: with `redirectURL=https://prism.lab.portico.test`, Prism
  Central sets `entityIssuer=https://prism.lab.portico.test/api/iam/authn`
  on the IdP object and the **per-IdP** metadata
  (`GET .../saml-identity-providers/{extId}/sp-metadata`) has
  `entityID=https://prism.lab.portico.test/api/iam/authn` and
  ACS `https://prism.lab.portico.test/api/iam/authn/callback`. The global
  metadata stays on the IP. The Keycloak client must therefore use the
  public entityID as `clientId`, not the one from the global metadata
  (first attempt with the IP entityID was replaced).
- MEASURED: the Keycloak client has "force POST binding" on (the plan said
  off): Prism Central only needs the *IdP sign-on URL* to support redirect
  binding, which Keycloak's descriptor lists; the response is POSTed to the
  ACS either way. Login works end to end with this setting.
- MEASURED: `nameIdPolicyFormat` of the created IdP is `emailAddress`;
  `usernameAttribute`/`emailAttribute`/`groupsAttribute` map to the
  Keycloak mappers `email` and `groups` (group membership mapper, single
  attribute, no full path).
- MEASURED, browser: the login page offers "Log in with
  portico-lab-keycloak" and "Log in with your Nutanix Local Account". The
  SAML button goes `/api/iam/authn/initiate-login` →
  `https://id.lab.portico.test/realms/portico-lab/protocol/saml?RelayState=...&SAMLRequest=...`
  (redirect binding) → Keycloak form → `POST https://prism.lab.portico.test/api/iam/authn/callback`
  (200) → `GET /auth_code?code=...` → `/console/`. **The round trip works
  through the public name; no request went to the IP.**
- MEASURED: alice (group `tenant-a`, Project Admin of `portico-lab-tenant-a`)
  lands on the VM list and sees exactly one VM (the project's), with
  project and owner columns; the top-right user is `alice`. The console's
  own calls for a tenant are listed in `docs/lab/payloads/tenant-console-paths.txt`
  (write-up 08).
- MEASURED: the console's own OIDC step (`/api/iam/authn/v1/oidc/auth`,
  `redirect_uri`, `authCodeUri`) builds URLs from the request `Host`, so
  it needs no setting.
- MEASURED (6.8): list/create/get/share/unshare/delete of the SAML IdP all
  work on `iam/v4.1.b3`; an existing IdP is detected by listing
  (`lab/saml/pc-idp.sh` refuses to create a second one with another name).
  Prism Central accepted the IdP metadata inline as base64 XML
  (`idpMetadataXml`), which avoids making Prism Central resolve the IdP's
  host name.

## Implications for the design

- Design 6.1 is answered: Portico creates the IdP with `redirectURL` set to
  the public Prism Central name and configures Keycloak's SAML client from
  the **per-IdP** SP metadata. No Prism Central FQDN change is needed.
- The IdP metadata goes inline; Prism Central does not need DNS for the
  Keycloak host (but the browser does, and in production that is the
  public DNS of `id.<msp>`).
- Section 4.3 step 5 ("Portico auto-starts SAML, to be measured"): the
  login page shows both buttons. Portico can send tenants straight to
  `/api/iam/authn/initiate-login` with the IdP selected, or let them click;
  the local-account link stays visible but its POST is denied by the
  proxy. Hiding it is cosmetic (step 2).
- One SAML IdP per Prism Central, shared per project: Portico shares the
  IdP with each tenant project it creates and unshares before deleting.

## How to re-measure after an upgrade

`lab/saml/pc-idp.sh list`, then compare
`GET /api/iam/v4.1.b3/authn/saml-identity-providers/{extId}/sp-metadata`
with this write-up; repeat the browser login as alice.
