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

## Part two: the tenant's path in the browser (Milestone 2c acceptance)

Measured on 2026-10-18 with Chrome on the Mac, branch `m2c-tenant-pages`
deployed by `lab/portico/deploy.sh`, branding set through the API (name
"Portico Lab MSP", tagline "Your cloud, your rules", colour `#8b1f5f`, a
1×1 PNG as logo).

- MEASURED, Keycloak login page: the accent colour on the button, the
  tagline as the form title, the logo in the header. The first build
  hid the MSP name under the logo (a fixed `height` on
  `#kc-header-wrapper`): fixed with `min-height`, test added.
- MEASURED, alice from `https://prism.lab.portico.test/`: Keycloak login
  → Portico landing page (`rule=landing`, then `/.portico/logo`) →
  `/.portico/saml-start` 302 → `initiate-login` 200 → `/auth_code` 302 →
  console with her VM, **no click**. From the landing page to
  `/auth_code`: **1.2 s** on the lab (journal 08:33:45.405 →
  08:33:46.620). The whole login, Keycloak submit to console, under
  10 s.
- MEASURED, Keycloak stopped: `/.portico/logout` and `/.portico/login`
  both end on the branded "Service unavailable" page (502, the identity
  host's upstream is down), with the MSP name, colour and a "Try again"
  button. The first build showed a broken logo there because
  `/.portico/logo` existed only on the Prism Central host behind a
  session: the logo is now a public asset on both hosts (test added).
- MEASURED, fallback: with `prism_central.saml_idp_name` set to a wrong
  name, alice's visit ran the landing page, `saml-start` logged one
  warning (`step=idp`) and sent the browser to
  `/ui/iam/providers?portico=manual`: Prism Central's own page with both
  buttons, exactly one fallback, no loop. The name restored, the flow is
  automatic again.
- MEASURED: `lab/auth/probe-local-login.sh` has two new lines, the
  provider page and `/.portico/saml-start` without a session both go to
  `/.portico/login`; PASS.
- MEASURED after the review (Sign Out): with the landing page, the
  console's own Sign Out (`#logout` → `v3/logout` → OIDC logout →
  `/redirect?logout=…`, write-up 12) would have landed on the landing
  page and signed the tenant straight back in, because the Keycloak
  session outlives Prism Central's. Portico now turns `/redirect?logout=`
  into its own `/.portico/logout` (access-log rule `sign-out`): the
  browser reaches Keycloak's "Do you want to log out?" page on the
  identity host (themed: accent button, logo) and, after confirming,
  Prism Central's public name shows the Keycloak login page again.
  Journal: `302 sign-out GET /redirect` → `302 GET /.portico/logout` →
  `200 GET /realms/portico-lab/protocol/openid-connect/logout`.
  Found on the way: the lab's SAML client had front-channel logout on
  with `/api/iam/authn/callback` as single-logout URL, so confirming the
  Keycloak logout posted a SAML LogoutRequest to Prism Central's callback
  through the public name, where no Portico session exists any more:
  `{"error":"portico session required"}` instead of the login page.
  That SLO is pointless (Portico's logout already expired the Prism
  Central cookies): `lab/saml/keycloak-realm.sh saml-client` now sets
  `frontchannelLogout:false` and clears the single-logout URLs; the
  Keycloak clients Portico registers in Milestone 2d must do the same.
- MEASURED after the review (branding form): the logo was uploaded from
  `https://prism.lab.portico.test:8443/settings/branding` in Chrome (a
  240×60 PNG): preview on the page, `logo.png` (247 bytes) in the theme
  directory, no warning. An SVG logo with scripts, event handlers,
  `javascript:` links or `foreignObject` is now refused at validation,
  because Keycloak serves the theme's copy from the login origin with
  its own headers (review finding).

Re-measure: `source lab/env.sh && lab/portico/deploy.sh`, set the branding
from `https://prism.lab.portico.test:8443/settings/branding`, log in as
alice on the public name; stop Keycloak (`sudo systemctl stop keycloak`
on the VM) and open the public name for the error page; set a wrong
`saml_idp_name` in `lab/portico/portico.yaml` and redeploy for the
fallback. Restore both afterwards.
