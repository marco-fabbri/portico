# 13 — Keycloak login theme, login request, `/redirect` target (Milestone 2c Task 0)

## Question

Three facts the branding and the landing page of design §4.10 need:

1. Can Portico write a Keycloak login theme into a directory mounted in
   the container and have it apply without a restart, through the admin
   API? Which file set and which CSS selectors does Keycloak 26.3's
   `keycloak.v2` login page expose?
2. What does the console read and write back to select the SAML
   provider (`login-requests/{id}`, write-up 12), and what does
   `initiate-login` answer, so Portico can do it on the user's behalf?
3. Does Prism Central's `/redirect?logout=<url>` (its own Sign Out path,
   allowed since 2b) follow an external URL?

## Measured on

- Keycloak 26.3.5 on the lab VM, quadlet `lab/vm/keycloak.container`
  with `Volume=/var/lib/portico/keycloak-theme:/opt/keycloak/themes/portico:ro,z`
  and `KC_SPI_THEME_CACHE_THEMES=false`, `KC_SPI_THEME_CACHE_TEMPLATES=false`,
  `KC_SPI_THEME_STATIC_MAX_AGE=-1`; admin client `portico-admin` with
  `manage-realm` added
- Prism Central pc.7.6.0.6
- Date: 2026-10-18
- By: Claude (Marco's session), `lab/saml/theme-probe.sh`,
  `lab/auth/probe-login-request.sh`

## Method

1. `lab/vm/provision.sh` (re-run: it now leaves nginx disabled when
   `portico.service` exists), `lab/saml/keycloak-realm.sh admin-client`,
   then `lab/saml/theme-probe.sh`: writes a three-file theme as the
   `portico` user, sets `loginTheme=portico` on the realm with a GET/PUT
   of the realm representation, fetches the login page through Portico's
   identity host (the `portico` client requires PKCE, so the probe URL
   carries a dummy S256 challenge) and greps it.
2. `lab/auth/probe-login-request.sh` on the internal pass-through:
   `GET /console/` → `iam-sessionid`; GET the login request; PUT it back
   with `loginProviderId`; GET `initiate-login`; GET `/redirect?logout=https://example.invalid/`.

## Result

- MEASURED: the theme applies **without a Keycloak restart**: the login
  page references `portico.css` and shows `loginTitleHtml` and
  `loginAccountTitle` from `messages_en.properties` right after the PUT
  on the realm. File set that works:
  `login/theme.properties` (`parent=keycloak.v2`, `import=common/keycloak`,
  `styles=css/login.css css/portico.css`), `login/resources/css/portico.css`,
  `login/messages/messages_en.properties`. The mounted directory is
  owned by `portico` (uid 993 inside the container, readable), SELinux
  label `container_file_t` from the `:z` option.
- MEASURED: header markup of the `keycloak.v2` login page:
  `<header id="kc-header" class="pf-v5-c-login__header">` (holds
  `#kc-header-wrapper` with `loginTitleHtml`),
  `<div class="pf-v5-c-login__main-header">` with
  `<h1 id="kc-page-title" class="pf-v5-c-title pf-m-3xl">`
  (`loginAccountTitle`); the primary button is
  `.pf-v5-c-button.pf-m-primary`.
- MEASURED: `GET /api/iam/v4.1.b3/authn/login-requests/{iam-sessionid}`
  with only that cookie answers 200 with `data: {clientId (36 chars),
  extId, isServiceProviderEnabled, isUserLoggedIn, loginProviderId: "",
  redirectUri, responseType: "CODE", scope: [OFFLINE_ACCESS, OPENID,
  PROFILE, EMAIL, GROUPS]}` and an ETag in `$reserved`. A `PUT` of the
  console's minimal body (`clientId`, `responseType`, `scope`,
  `redirectUri`, `loginProviderId`) with no `If-Match` and no
  `NTNX-Request-Id` answers 200 and the read-back has the provider set.
- MEASURED: `GET /api/iam/authn/initiate-login` after that answers
  **200 with an HTML page** titled "IAMv2 SAML SSO login": a form with
  `method="GET"` to `https://id.lab.portico.test/realms/portico-lab/protocol/saml`
  carrying `RelayState` and `SAMLRequest` hidden fields, submitted by the
  page itself. Not a 302. Portico's landing flow therefore ends with a
  redirect of the browser to `initiate-login`, which it proxies as any
  `/api/` path; the browser submits the form.
- MEASURED: `GET /redirect?logout=https://example.invalid/` answers
  302 to `https://<request host>:9440/api/iam/authn/v1/oidc/logout?post_logout_redirect_uri=https://<request host>:9440`:
  the parameter is not used as the target, Prism Central builds it from
  the request `Host`. No open redirect through `/redirect`.

## Implications for the design

- Branding → Keycloak theme (Task 2): write the three files, set
  `loginTheme` once (idempotent PUT), no restart; the CSS targets
  `#kc-header-wrapper` (logo and name), `#kc-page-title` and
  `.pf-v5-c-button.pf-m-primary` (accent colour). The lab quadlet
  carries the mount and the cache settings; the appliance image
  (Milestone 3) must carry the same.
- Landing page (Task 3): GET the login request with the browser's
  `iam-sessionid` cookie, PUT the minimal body with `loginProviderId`,
  then `302 /api/iam/authn/initiate-login`; the browser submits the SAML
  form to Keycloak, which already has a session.
- `/redirect` stays in the allow-list; the open-redirect question of the
  2b review is closed.

## How to re-measure after an upgrade

`source lab/env.sh && lab/saml/theme-probe.sh` (then
`lab/saml/theme-probe.sh reset` to put the default theme back), and
`lab/auth/probe-login-request.sh`.
