# 17 — Keycloak login page layout and Portico's mark

## Question

What the keycloak.v2 theme of Keycloak 26.3 actually ships (sheet names,
markup, fonts), so that Portico's generated theme can lay the login page
out as one centred column on the MSP's accent colour: the lockup (logo
left of the MSP name, name as an uppercase wordmark with the last word
bold), a claim under it, the card, and a "Powered by Portico" line. On
Portico's own pages the black arch mark replaces the plain wordmark.

## Measured on

- Keycloak 26.3.5 (theme parent `keycloak.v2`), Prism Central pc.7.6.0.6
- Portico `dev-1df43d3` to `dev-c8a8147` (branch `layout-login-page`)
- Date: 2026-10-18 and 2026-10-19
- By: Claude (Marco's session) for the theme and the checks on the VM;
  Marco in Chrome for every visual iteration

## Method

1. `lab/portico/deploy.sh --fresh` after each commit, then the branding
   re-saved through `PUT /api/v1/settings/branding`
   (`lab/portico/bootstrap-acceptance.sh` body: name `Portico MSP`, accent
   `#a73422`, logo `lab/portico/portico-msp-logo.svg`, tagline from the
   README) so the theme files under `/var/lib/portico/keycloak-theme` are
   regenerated.
2. The generated files read back over SSH; the page's resources checked
   with `curl -sI` on `https://id.lab.portico.test/resources/<hash>/login/portico/...`
   and on the parent's paths.
3. Marco reloads `https://prism.lab.portico.test/` (hard reload) after
   each deploy and judges the result.

## Result

- MEASURED: keycloak.v2 on 26.3 ships its base sheet as `css/styles.css`;
  `css/login.css` (the name older releases used) answers 404. A child
  theme that lists only its own sheet in `styles=` loses the parent's
  styles: `theme.properties` now says `styles=css/styles.css css/portico.css`.
- MEASURED: the page is PatternFly 5 markup: `body#keycloak-bg` (painted
  with the parent's background image), `.pf-v5-c-login__container` as a
  grid with `header#kc-header > #kc-header-wrapper` (the `loginTitleHtml`
  message, rendered as HTML) beside `main.pf-v5-c-login__main`. The card
  starts at the cross-axis start (`align-self: start`) and its header
  carries a 4 px blue `border-top`; both had to be overridden to centre
  the card under the lockup. The header text is white in the parent, so
  the lockup is readable only on a coloured background.
- MEASURED: Red Hat Display is declared by the parent's `styles.css`
  (`font-family: "RedHatDisplay"`, files under
  `/resources/<hash>/common/keycloak/vendor/patternfly-v5/assets/fonts/`),
  so the lockup can use it without shipping a font.
- MEASURED: in a flex row, `gap` applies between a text node and a
  following `<strong>` as two items: "PORTICO MSP" showed a gap although
  the message had no space. The name is now one `<span class="portico-name">`
  (one flex item) so the wordmark reads `PORTICOMSP` with the last word bold.
- MEASURED: the admin UI serves `/static/` with `Cache-Control:
  max-age=86400`; a new binary ran for a day on the browser's old CSS (the
  mark rendered at full size). The templates now append `?v=<version>` to
  every static URL, so a new binary always fetches its own assets.
- MEASURED: the realm's `displayName` is the page `<title>`; it was set to
  `Portico MSP` by hand (`PUT /admin/realms/portico-lab`) on the lab realm,
  which predates the bootstrap.
- MEASURED: the generated theme on the VM after the last deploy: the
  `messages_en.properties` line is
  `loginTitleHtml=<span class\="portico-lockup"><span class\="portico-name">Portico<strong>MSP</strong></span></span><span class\="portico-claim">The single, authenticated entry point to your Nutanix services.</span>`
  and `portico.css` lays the column out as described (`#kc-header` first,
  the card `max-width: 480px` with a 14 px radius, the powered-by line as
  `.pf-v5-c-login__container::after` with the white mark).

## Implications for the design

None for the model. Branding stays three fields plus a logo; the layout
is Portico's and the MSP only chooses name, accent, logo and tagline. The
tagline is the claim under the lockup, and the branding page says so.

## How to re-measure after an upgrade

After a Keycloak upgrade, with the lab realm on the `portico` theme:
`curl -sI https://id.lab.portico.test/resources/<hash>/login/keycloak.v2/css/styles.css`
(must be 200) and a hard reload of `https://prism.lab.portico.test/`;
if the page is unstyled or the card sits left, compare the selectors in
`internal/branding/theme.go` with the page source.
