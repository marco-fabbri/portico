# Portico — Branding: colours, header and preview (2026-10-21)

Design agreed with Marco on 2026-10-21, on mockups. It changes the
branding of the main design (`2026-10-17-portico-design.md`, section
4.10, "Keycloak login theme" and "Branding settings"): see "What changes
in the main design" below. Implementation updates the main design and
records the change in `docs/adr/0005-branding-header-and-colours.md`.

## 1. Intent

The login page is the first thing an MSP's customer sees, and today the
MSP can change only its name, a claim, one colour and a small logo beside
the name. The page always has the accent colour as background and white
text, so an MSP whose identity is a light page, or whose logo is a mark
meant to stand alone, cannot make the page look like its own.

This work lets the MSP set three colours (background, text, accent) and
compose the header from a logo shown large, a free text and the claim,
and see the result in the admin UI while choosing, before saving.

Why now: Marco wants the demo appliance to show an MSP's own identity, not
Portico's (the demo uses a logo that is Marco's and not in the
repository).

## 2. Goals, non-goals, anti-goals

### Goals

1. **Three colours.** Background of the page, text (everything written on
   the background), accent (the primary button and the links inside the
   white card). The white "Sign in" card stays white.
2. **Header in three optional pieces**, in one centred column:
   - **logo**: the uploaded image, large and centred (it replaces today's
     lockup, where a small logo sat left of the name);
   - **header text**: a free text the MSP writes, separate from the MSP
     name, shown under the logo or alone, **bold, exactly as typed** (no
     case change, no letter spacing);
   - **claim**: today's tagline, always under the other two.
   Valid combinations: logo only, logo and text, text only. With neither
   a logo nor a header text, the MSP name takes the header text's place,
   in the same style, so no page is without a header.
3. **One header layout everywhere.** Today's lockup (uppercase, spaced,
   last word bold, logo one em tall beside the name) goes away. Every
   tenant-facing page uses the new header: all Keycloak login-theme pages
   (login, OTP, update password, reset password, Keycloak's own error
   page) and Portico's pages (landing, site page, error pages including
   maintenance and site unreachable).
4. **A fresh Portico is a pre-filled branding**, drawn with the same
   layout as any MSP's (decided with Marco: one layout, no special
   default skin):
   - MSP name `Portico`, header text `Portico`;
   - claim `The single, authenticated entry point to your Nutanix services.`;
   - logo: **Portico's arch**, drawn in the text colour (so it stays
     visible if the MSP picks a light background and keeps the arch);
   - background `#a73422`, text `#ffffff`, accent `#a73422`;
   - no tab icon (Portico's arch on the accent colour, as today),
     minimum landing time 0.
   Uploading a logo replaces the arch; removing the logo leaves no logo;
   the admin UI can put the arch back. The default is applied to Keycloak
   like a saved branding: at start Portico writes the theme and sets it on
   the realm also when no branding was ever saved (today it waits for the
   first save, `brand.Stored` in `cmd/portico/serve.go`), so a fresh
   appliance's login page is the default one from its first start after
   setup (my call; a failure is logged as today and retried at the next
   start or save).
5. **Readability warning.** On save (admin UI and API) Portico computes
   the WCAG 2 contrast ratio of the text colour on the background and of
   white on the accent (the button label; the accent links on the white
   card are the same pair). Below 4.5:1 the branding is saved anyway and
   the answer carries a warning naming the pair and its ratio. Reference
   values: white on `#3b5bdb` is about 5.3 (no warning), white on
   `#f165b0` about 2.9 (warning), white on `#a73422` about 6.6.
6. **Live preview in the admin UI.** The Branding page shows, next to the
   fields, a drawing of the login page (header, claim, the white card with
   two fields, the button and a link, "Powered by Portico") that follows
   every change of the fields without saving, including a logo chosen in
   the file field and not yet uploaded.
7. **Existing brandings keep their identity.** A stored branding from
   before this change reads as: background = its accent, text `#ffffff`,
   accent unchanged, header text = its MSP name when it has a logo
   (otherwise empty, and the name shows through the fallback). Its look
   changes only in the header layout: the logo moves above the name and
   grows. A store without a branding document gets the new default.

### Non-goals (this work)

- A branding per tenant or per site; one branding per appliance, as
  today.
- Custom CSS, fonts, a card colour, a background image (listed in section
  4.10 and never built: dropped), a dark variant of the login page.
- Overriding Keycloak's page templates (write-up 22 already set this
  limit: templates change with every Keycloak release).
- The admin UI's own look, Keycloak's e-mails, Prism Central's pages.
- A live readability hint in the preview: the preview shows the colours,
  the warning comes with the save.

### Anti-goals (failure even if it works)

- An MSP saves colours that make its login page unreadable and nothing
  tells it.
- An appliance upgrade that removes an MSP's name or logo from its login
  page.
- An API client written before this change (the lab scripts
  `bootstrap-acceptance.sh` and `tab-icons.sh` among them) that can no
  longer save the branding.
- The preview showing something the real pages would not show for the
  same settings (a piece present in one and absent in the other, other
  colours).
- An uploaded file able to run script on the login origin: the SVG rule of
  section 4.10 stays as it is.
- A fresh appliance whose login page looks unfinished.

## 3. Constraints

- Keycloak 26.3.5, theme parent `keycloak.v2`. Portico writes the theme
  directory mounted into the container (CSS, `messages_en.properties`
  with `loginTitleHtml`, images) and Keycloak applies it without a
  restart (write-up 13). No `.ftl` template is overridden.
- Content-Security-Policy of Portico's tenant pages:
  `default-src 'none'; img-src 'self'; style-src 'unsafe-inline'`. No
  script on tenant pages; the arch in the text colour is inline SVG or a
  file Portico serves, as the "Powered by Portico" mark is today.
- Admin UI CSP: `default-src 'self'; img-src 'self' data:; form-action
  'self'; frame-ancestors 'none'`, unchanged. Server-rendered pages with
  HTMX (ADR 0003); the preview may use one small script file embedded in
  the binary and served from `/static/`; no new dependency.
- The branding is one JSON document in the store (`branding` key);
  `PUT /api/v1/settings/branding` replaces the whole document; the answer
  already has a `warning` field. The store format is versioned and
  migrated at start.
- Logo limits unchanged: PNG, JPEG or SVG, at most 256 KiB, active SVG
  content refused.
- Everything on disk in English. The demo logo and the demo MSP name are
  Marco's and never enter the repository: the lab script takes the image
  path and the texts as arguments, the write-up describes the image
  ("a 167×178 RGBA PNG mark"), not its content.

## 4. Decisions

### 4.1 Settings

New fields of the branding document, next to the existing ones:

| field | rule |
|---|---|
| `background_color` | `#rrggbb` |
| `text_color` | `#rrggbb` |
| `header_text` | at most 80 characters, one line (no newline), may be empty |

`accent_color`, `tagline` (the claim, at most 160), `msp_name` (still
required: the tab title "Sign in to …", the landing page's title, the
logo's alternative text, the fallback header), logo, tab icon and minimum
landing time keep their rules. The logo has three states: Portico's arch,
an uploaded image, none.

The tab title of the Keycloak pages follows `msp_name`. Today it does
not: Keycloak builds it from its `loginTitle` message and the realm's
display name, set once by the Keycloak bootstrap (`Portico MSP` on the
lab). The theme's messages therefore set `loginTitle` from `msp_name`, so
a renamed MSP's login tab says "Sign in to <its name>" (my call, to be checked
on the lab).

A `PUT` without one of the new fields gets it as an old document is read
(goal 7): background = accent, text `#ffffff`, header text = the MSP name
when the document has a logo. A client that does not know the new fields
keeps the look it had, and re-running `lab/portico/bootstrap-acceptance.sh`
gives the same page as upgrading (my call). An empty `header_text` sent
explicitly stays empty. The lab scripts that write the branding are
updated to send the new fields. (Builder's call: how "absent" is told
apart from "empty" on the wire, and how the arch state is represented in
the JSON.)

### 4.2 Header rendering

- Logo: at most 96 px tall and 320 px wide, aspect ratio kept, centred
  (Marco's call on the mockup: variant C, "logo only, large and
  centred"). A square mark like the demo's and a wide wordmark both fit.
  On a phone it shrinks with the column.
- Header text and fallback name: bold, as typed, the size of today's
  lockup (2.25 rem), wrapping centred when long.
- Claim: as today, under the header, in the text colour.
- "Powered by Portico" and its arch in the text colour, at today's
  opacity.
- On Portico's pages the text colour also applies to the landing page's
  spinner and its "Signing you in…" line, to the links outside the cards
  ("Continue", "Switch site", "Sign out") and to the "who" line of the
  site page. Inside cards and tiles colours are as today, with the accent
  for links and buttons.
- The card's shadow must look clean on a light background as well as on
  a dark one (today's 28 % black shadow looks dirty on near-white).

### 4.3 Preview

A drawing of the Keycloak login page inside the admin Branding page, made
by Portico with the rules of 4.2, not the real Keycloak page in a frame:
showing the real page before saving would need a temporary Keycloak
theme for every change. The real page is what the MSP sees after saving.
A logo chosen but not uploaded is shown from the browser's copy of the
file (a `data:` URL, which the admin CSP allows; an SVG in an `<img>`
runs no script).

### 4.4 Readability warning

WCAG 2 relative luminance and contrast ratio; threshold 4.5:1 for both
pairs of goal 5. The check runs in the branding package, so the admin UI
and the API give the same warning. A failed theme apply (today's warning)
and the readability warnings are reported together.

### 4.5 Demo

On the lab appliance, after the upgrade: Marco's logo, header text and
claim from him, background `#f5f5f7`, text `#222222`, accent `#3b5bdb`
(colours taken from the logo, chosen by Marco on the mockup; `#222222` on
`#f5f5f7` and white on `#3b5bdb` pass the 4.5:1 check). Applied with a lab
script that takes the logo path and the texts as arguments and leaves the
rest of the branding unchanged, in the style of `lab/portico/tab-icons.sh`.

## 5. What changes in the main design

Section 4.10:
- "Keycloak login theme": the settings list becomes MSP name, header text,
  claim, logo, background, text and accent colours; "background image"
  goes.
- "Branding settings": the new fields, their limits, the arch as the
  default logo, the readability warning and the preview.
- The fallback and the default branding of goals 2 and 4.

ADR 0005 records: one header layout instead of a default skin plus an MSP
layout; three colours instead of the accent painting the page; the preview
drawn by Portico instead of the real Keycloak page.

## 6. Done when

- Unit tests: validation of the new fields; the contrast ratios of the
  reference values in goal 5; reading an old document with and without a
  logo; a `PUT` without the new fields, with and without a logo, and one
  with an explicit empty header text; `loginTitle` from the MSP name; the
  theme applied at start with nothing stored; the theme CSS and
  `loginTitleHtml` and each Portico page for logo only, logo and text,
  text only, neither (fallback), the arch in a non-white text colour.
- The lab appliance upgraded (with Marco's OK): its existing branding
  (`Portico MSP`, accent `#a73422`, the white arch SVG as logo) shows the
  arch above "Portico MSP" on the red background; then the demo branding
  applied; the login page, landing page and site page checked by Marco in
  Chrome and Safari.
- A lab write-up (23) with what was checked and how to repeat it; the lab
  inventory's branding row updated; `docs/operating.md` if anything an
  operator does changes.

## 7. Left to the builder

- The JSON representation of the logo's arch state and of absent colour
  fields; where old documents are converted (on read or as a store
  migration).
- How the preview shares the layout rules with the real pages (one Go
  template fragment rendered into the admin page with CSS variables the
  script updates is one way) and its tests.
- The exact markup and CSS in the Keycloak theme (`loginTitleHtml` plus
  the generated stylesheet) and in Portico's three templates, within 4.2.
- The admin form's layout (fields beside the preview on a wide screen,
  above it on a narrow one) and how "use Portico's arch" is offered.
- Wording of the warnings, naming the pair and the ratio.

## 8. Calls made in the design without Marco

Marco decided: the three colours, the header pieces and variant C, the
header text bold as typed, one layout with the default as a pre-filled
branding (arch, "Portico", claim, `#a73422`), the live preview, the demo
palette. The rest was proposed in the conversation or settled while
writing; listed here so it can be checked:

- the fallback to the MSP name when there is neither logo nor header text;
- the arch drawn in the text colour;
- the readability warning, its 4.5:1 threshold and its two pairs, on save
  only;
- the conversion of old documents and of a `PUT` without the new fields;
- the default theme applied to Keycloak at start without a saved branding;
- `loginTitle` from the MSP name;
- the logo box (96 by 320 px) and the 80-character header text;
- the preview drawn by Portico, not the real Keycloak page, with one
  script file.
