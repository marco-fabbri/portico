# ADR 0005: One header layout, three colours, a preview drawn by Portico

- Status: accepted (2026-10-21)
- Deciders: Marco, Claude

## Context

The branding of section 4.10 let an MSP set its name, a claim, one
accent colour and a small logo beside the name. Every tenant-facing page
had the accent as background and white text, and the name was set as a
lockup (uppercase, spaced, last word bold). An MSP whose identity is a
light page, or whose logo is a mark meant to stand alone, could not make
the login page look like its own. Marco asked to set the background and
text colours and the claim, and to show a text in place of the logo
(design `2026-10-21-branding-design.md`).

## Decision

1. One header layout for every tenant-facing page, Keycloak's and
   Portico's: a logo large and centred, a free header text in bold
   exactly as typed, the claim under them; with neither logo nor header
   text, the MSP name stands in for the text. Portico's own look is not
   a separate default skin: a fresh appliance gets a pre-filled branding
   (the arch as logo, "Portico" as header text, the claim, `#a73422` and
   white) drawn by the same layout, and Portico applies it to Keycloak
   at every start, saved or not.
2. Three colours instead of the accent painting the page: background,
   text (everything on the background, the arch logo included), accent
   (the button and the links in the white card). A contrast below 4.5:1
   for the text on the background or for white on the accent saves the
   branding with a warning, so an unreadable page never goes unnoticed
   and a deliberate choice is never refused.
3. The admin UI previews the login page with a drawing made by Portico
   from the fields being edited (a small script served from `/static/`
   under the admin CSP), not the real Keycloak page in a frame: showing
   the real page before saving would need a temporary Keycloak theme for
   every change. The real page is what the MSP sees after saving.
4. Keycloak's page templates stay untouched: the header is the theme's
   `loginTitleHtml` message and CSS, as before (write-up 22).

## Consequences

- An upgrade keeps each MSP's identity: an old document reads with its
  accent as background, white text and its name as header text when it
  has a logo; only the header layout changes (the logo moves above the
  name and grows). An API client that omits the new fields gets the same
  reading.
- The preview and the real pages are two drawings of the same rules: a
  change to one needs the same change to the other. The tests pin the
  pieces each shows; the lab acceptance compares them by eye.
- Writing the theme at every start makes the login page right from the
  first start after setup, and repairs a theme directory changed by
  hand. It costs one theme write and one realm update per start.
- No per-tenant or per-site branding, no custom CSS or fonts, no
  background image (the old section 4.10 listed one; it was never built
  and is dropped).
