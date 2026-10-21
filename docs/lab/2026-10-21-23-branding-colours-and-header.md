# 23 — Branding colours and header on the appliance

## Question

Does the branding of design 4.10 as changed on 2026-10-21 (three
colours, a header of logo, header text and claim, the arch as default
logo, the readability warning, the admin preview) reach Keycloak's login
and Portico's pages on the appliance; does an upgraded appliance keep
the MSP's identity; does a header text with quotes and braces survive
Keycloak's message formatting; and does an MSP's light identity read
well?

## Measured on

- Prism Central: pc.7.6.0.6 (the site `AZ-IT-MI1`), and a second pc.7.6
  lab cluster added during the session as the site `AZ-US-NYC1`
- Portico: the appliance `portico-appliance` (10.23.210.191), upgraded
  in place from `dev-efe8d3d` to `dev-8c55e96`, then to the fixes below
  (`dev-424b5a4`, `dev-a9df544`, `dev-9c6c7c4`, branch `favicon`);
  Keycloak 26.3.5, login theme `keycloak.v2` with Portico's theme on
  top; the installer also upgraded `cloudflared` to 2026.10.3
- Browsers: Chrome 155.0.8059.85 on macOS 27.0.1 (Marco's views); Safari
  27.0.1 is installed but its views were not reported
- Date: 2026-10-21 and 2026-10-22
- By: Claude, with the scripts below; the browser views were Marco's

## Method

- `lab/portico/upgrade-appliance.sh` (store and master key copied to
  `/var/lib/portico/backup-<running version>-<time>/` before each
  upgrade).
- `lab/portico/demo-branding.sh` (new): saves the branding to
  `$PORTICO_SECRETS/branding-before-<UTC time>.json`, changes only the
  fields given through `PUT /api/v1/settings/branding`, prints a
  readability warning, and checks the fields not given, the header text
  on Keycloak's login page, the three colours in the theme CSS and
  `/.portico/logo`; `--restore FILE` puts a saved branding back.
- `lab/portico/tab-icons.sh` (checks only) after the upgrade.
- The demo: a 167×178 RGBA PNG mark (not in the repository), an MSP
  name and header text given by Marco (not in the repository), the
  default claim, and a light palette Marco chose (dark text on a light
  background, a mid-tone accent; not in the repository).
- In the browsers: the tenant login, the site page, a tenant sign-in
  through to each site's console, and the admin Branding page with its
  preview, changing fields without saving.
- Keycloak's `keycloak.v2` CSS and templates read from the appliance
  (`styles.css` as served, `saml-post-form.ftl` and `template.ftl` from
  the themes jar).

## Result

- MEASURED: the upgrade kept the lab's identity. The branding stored
  before this change read as background `#a73422` (its accent), text
  `#ffffff`, header text `Portico MSP` (its name, since it has a logo),
  its SVG logo kept, no arch. The login page showed the logo, then
  `<span class="portico-text">Portico MSP</span>` and the claim; tab
  title `Sign in to Portico MSP`. `tab-icons.sh`: 13 of 13 ok.
- MEASURED: `--msp-name "Lab's MSP {0}" --header-text "Lab's MSP {0}"`:
  header text and tab title `Lab&#39;s MSP {0}` in the HTML, shown as
  typed: the MessageFormat and properties escaping hold.
- MEASURED: Keycloak's sanitizer of `loginTitleHtml` keeps `class` and
  removes `role` and `aria-label` from the logo's span. With a header
  text the header still has its name; a logo-only header has none on
  Keycloak's pages (the tab title still names the MSP).
- MEASURED: accent `#f165b0`: saved (200) with `warning: White button
  text on accent colour #f165b0 has contrast 2.91:1, below 4.5:1`; the
  restore put the original back (same fingerprint of every field).
- MEASURED: the demo saved with no warning, every check ok, tab title
  `Sign in to <demo name>`, `/.portico/logo` the uploaded PNG byte for
  byte.
- MEASURED, fixed: on the first look the header text and the claim
  were white on the light background. `keycloak.v2`'s `styles.css` sets
  `#kc-header-wrapper { color: var(--pf-v5-global--Color--light-100)
  !important; }`; with the old white text nothing showed it. Portico's
  rule, loaded after it, now carries `!important` too (`dev-424b5a4`).
  After it Marco's view: "molto bello".
- MEASURED: the site page with the demo branding: logo, header text,
  claim, the two site tiles on the light background, the "who" line and
  "Powered by Portico" in the text colour.
- MEASURED, fixed: a site that turned unreachable between the site page
  and the click answered the browser's choice with the JSON refusal
  instead of the branded page: the choice is a form POST and Portico
  took only GET and HEAD as page loads. A POST that accepts `text/html`
  now gets the page (`dev-a9df544`).
- MEASURED, changed at Marco's request: Keycloak's SAML post page, shown
  while the assertion goes to Prism Central, had the white card with
  "Authentication Redirect" left-aligned. It is now like Portico's
  landing page that follows it: no card, a spinner and "Signing you in…"
  centred in the text colour, through CSS on
  `body[data-page-id="login-saml-post-form"]` and the messages
  `saml.post-form.title` and `saml.post-form.message` (`dev-9c6c7c4`).
- MEASURED: an upgrade during a tenant login breaks that login once:
  the installer restarts Keycloak's database (Keycloak answered "Unexpected
  error when handling authentication request to identity provider") and
  Portico (its callback found no login state). The next login worked.
- MEASURED, before the deploy only: the admin preview, on the Branding
  page rendered by a local `portico serve` and served with the admin CSP
  (Chrome): the colours, the arch, an uploaded logo, no logo, the MSP
  name fallback and a PNG chosen and not uploaded (45×48, the file radio
  selected) followed the fields with no CSP error.
- NOT MEASURED: the preview on the appliance's admin UI, every page in
  Safari, a fresh store's default branding written at the first start
  (unit test only), Firefox, iOS.

### Found on the way, outside the branding

The second Prism Central was added during the session. What got in the
way is listed for the sites work, not fixed here:

- the site's SAML card said "Configured" from the stored provider name,
  not from Prism Central, and hid the button that creates the trust;
- creating the trust needs an administrator's credentials (write-up 15:
  the service account cannot manage identity providers), while the form
  calls them optional; the administrator's credentials are asked in two
  separate places;
- `tls_verify` defaults to on, and a Prism Central with its factory
  certificate (`CN=prism.nutanix.local`, no IP SAN) fails it;
- a site's cluster and storage container defaults stay empty until a
  check succeeds, while the tenant page calls them optional;
- the first storage container listed became the default, here the
  Nutanix Objects one, for the first site and its first tenant;
- a site's changes need Apply and restart, which the site page does not
  say;
- "Retry provisioning" sits at the top of the tenant page, far from the
  failed site;
- inside a site's console there is no way back to the site page;
  Marco's proposal: with several sites, Prism Central's sign-out leaves
  only that site and returns to the site page, which has the full Sign
  out;
- when Prism Central is slow, the SAML start falls back to Prism
  Central's own provider page, whose error reads "Authentication
  providers not registered yet";
- Keycloak's SAML logout to Prism Central fails: the container does not
  resolve the public Prism Central name.

The first site's Prism Central answered slowly for minutes at a time
(clusters list in 15 s, timeouts) twice during the session.

## Implications for the design

- None for design 4.10 beyond what ADR 0005 records; the Keycloak
  `!important` rule and the SAML post page are theme details.
- Known limit: a logo-only header has no accessible name on Keycloak's
  pages.

## How to re-measure after an upgrade

```
export PORTICO_SECRETS=~/portico-secrets-appliance LAB_VM_IP=10.23.210.191 \
  LAB_SSH_USER=admin LAB_REALM=portico LAB_SAML_IDP=portico-keycloak \
  LAB_PASSTHROUGH=loopback \
  LAB_SSH_OPTS="-o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no"
source lab/env.sh
lab/portico/upgrade-appliance.sh            # restarts Portico and Keycloak's database: logins in flight fail
lab/portico/demo-branding.sh --msp-name "Lab's MSP {0}" --header-text "Lab's MSP {0}"
lab/portico/demo-branding.sh --restore "$PORTICO_SECRETS"/branding-before-<time>.json
lab/portico/demo-branding.sh --accent "#f165b0"    # expect the 2.91:1 warning
lab/portico/demo-branding.sh --restore "$PORTICO_SECRETS"/branding-before-<time>.json
lab/portico/demo-branding.sh --logo MARK.png --msp-name NAME --header-text NAME \
  --background "#f5f5f7" --text "#222222" --accent "#3b5bdb"   # any light palette
# in Chrome and Safari: the login, a tenant sign-in through the site page,
# the admin Branding page's preview without saving
```
