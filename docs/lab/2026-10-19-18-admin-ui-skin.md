# 18 — Admin UI skin and the setup page state

## Question

Whether the admin UI can take the look of the approved mockup (white
header with the black mark, section tabs, light warm ground, the accent
only where one acts, Red Hat fonts) under its content security policy
(`default-src 'self'`, no external origin), and what an administrator
reads on the setup page of a configured appliance.

## Measured on

- Keycloak 26.3.5 (PatternFly 5 bundle), Portico `dev-fea75cc` to
  `dev-160afcf` (branch `admin-ui-skin`), Rocky Linux 9.8
- Date: 2026-10-19
- By: Claude (Marco's session) for the build, the deploy and the login
  page screenshot; Marco in Chrome for the pages behind the login

## Method

1. Mockup on a design canvas (three boards: tenants, tenant detail,
   admin login), approved by Marco with the mark left for a later pass.
2. Fonts: Keycloak's login page lists its stylesheets; the woff2 URLs
   were read from `patternfly.min.css` and the two variable fonts
   fetched from `/resources/<hash>/common/keycloak/vendor/patternfly-v5/assets/fonts/`.
3. `go test ./...`, `deploy.sh --fresh` on the existing store, the login
   page in Chrome (unauthenticated), the other pages by Marco.

## Result

- MEASURED: the fonts are not on the container's disk (`find` in the
  Keycloak container finds no `RedHat*.woff2`); they live in the theme
  jar and are served under the resources hash. `RedHatDisplayVF.woff2`
  is 39,764 bytes and `RedHatTextVF.woff2` 38,460, weights 400 to 700
  (`font-weight: 400 700`, `format("woff2-variations")`). OFL 1.1, so
  they ship inside the binary with the licence text.
- MEASURED: Go's built-in media-type table has no `.woff2`; served
  through `http.ServeContent` it came out as `text/plain` on the Mac
  before `mime.AddExtensionType(".woff2", "font/woff2")`; after it the
  lab answers `font/woff2`.
- MEASURED: the login page as deployed (screenshot): mark, PORTICO,
  "Admin console", white card centred, accent button, version line.
- MEASURED, Marco on `/setup`: with every field filled and the "Or let
  Portico create it" form open under each card, the page did not say
  Prism Central was configured. Each card now opens with a state line
  (configured, naming what is stored; or what is still needed) and the
  bootstrap form is a `<details>` block open only while the card is
  incomplete; the Apply card states the whole.
- MEASURED: `deploy.sh --fresh` on a filled store exited 1 after
  "portico ready": the setup-mode grep on the journal found nothing and
  `set -e` ended the script. Fixed.

## Implications for the design

None for the model. Marco's reading of the setup page raised the
multi Prism Central item (a list of connected services with add and
edit, later NKP), recorded in `docs/backlog.md` as its own milestone
after the appliance image.

## How to re-measure after an upgrade

After a Keycloak upgrade, compare the font files it serves
(`grep -o 'url([^)]*RedHat[^)]*woff2[^)]*)' patternfly.min.css`) with
`internal/web/static/fonts/`; `go test ./internal/web/ -run TestSkin`
covers the media type, the licence and the stylesheet rules.
