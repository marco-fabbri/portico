# Branding: Colours, Header and Preview Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The MSP sets a background, a text and an accent colour and
composes the header of every tenant-facing page from a large logo, a free
header text and the claim, sees a live preview in the admin UI, and a
fresh Portico shows a pre-filled branding drawn with the same layout.

**Architecture:** The `branding` package gains the new fields, the
reading of old documents, the header rules (`Header()`, logo box), the
logo file (an upload or Portico's arch in the text colour) and the WCAG
contrast check; the Keycloak theme, Portico's three page templates (now
sharing one partial), the JSON API and the admin form consume them. The
preview is a drawing in the admin page driven by one static script; the
theme is applied at every start, also with nothing saved.

**Tech Stack:** Go 1.27 standard library (`image`, `image/png`,
`image/jpeg`, `encoding/xml` allowed), HTMX 2.0.4 (vendored, unchanged),
one plain JavaScript file, Keycloak 26.3.5 (`keycloak.v2` parent).

**Spec:** `docs/superpowers/specs/2026-10-21-branding-design.md`
(approved 2026-10-21, e8fd419), amending
`docs/superpowers/specs/2026-10-17-portico-design.md` section 4.10.
Write-ups 13 (theme without restart), 17 (login page layout), 22 (tab
icons, no `.ftl` override).

## Global Constraints

- All text on disk in English. Commits end with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`, author email
  `9093189+marco-fabbri@users.noreply.github.com`
  (`git -c user.email=... commit`).
- Branch `favicon` (tab icons df33f2f, efe8d3d, cdf0bfe; design e8fd419).
  One "branding" pull request at the end, rebased on `main` after PR #25
  merges; push and PR only with Marco's OK; Marco merges with rebase.
- No new Go module dependency, no JavaScript toolchain; one script file
  `internal/web/static/branding-preview.js`.
- CSP unchanged: tenant pages `default-src 'none'; img-src 'self';
  style-src 'unsafe-inline'; frame-ancestors 'none'`; admin UI
  `default-src 'self'; img-src 'self' data:; form-action 'self';
  frame-ancestors 'none'` (no inline `<style>` or `style=` in admin
  markup: colours reach the preview through `element.style.setProperty`).
- No Keycloak `.ftl` template override; the theme is CSS, messages and
  images only.
- Colours match `^#[0-9a-fA-F]{6}$`; `header_text` at most 80 characters,
  no `\n` or `\r`; tagline at most 160; logo and icon limits unchanged.
- Default branding: MSP name `Portico`, header text `Portico`, tagline
  `The single, authenticated entry point to your Nutanix services.`,
  Portico's arch as logo, background `#a73422`, text `#ffffff`, accent
  `#a73422`, no tab icon, `min_display_ms` 0.
- Contrast: WCAG 2 relative luminance (sRGB threshold 0.03928), ratio
  `(L1 + 0.05) / (L2 + 0.05)`, warning when below 4.5, ratio printed with
  two decimals.
- Logo box: fit inside 320 × 96 CSS px keeping the aspect ratio, never
  enlarged, rounded to whole pixels.
- Card and tile shadow everywhere: `0 8px 24px rgba(0, 0, 0, 0.16)`.
- `CGO_ENABLED=0`; `go vet ./...`, `gofmt -l cmd internal`,
  `go test ./...` clean at every commit; `lab/test_scripts.sh` green.
- The demo logo, the demo MSP name and the demo header text are Marco's:
  never in the repository, never in a commit or doc; scripts take them as
  arguments.
- Lab: appliance deploy and restarts only with Marco's OK; authenticated
  browser logins are Marco's; secrets read by scripts only, never
  printed; docs never say what the lab hardware is used for.

## Review Focus

1. An apostrophe or a brace in the MSP name, header text or claim
   (`O'Brien {0} Cloud`): Keycloak runs every message through Java's
   `MessageFormat`, which eats `'` and treats `{0}` as an argument.
   Expected: the login page and its tab title show the text as typed
   (Task 3 test, Task 9 lab check).
2. A long header text without spaces (80 × `W`) on a phone: expected it
   wraps inside the column, no horizontal scroll (`overflow-wrap:
   anywhere` on the text in the theme and on Portico's pages; Tasks 3, 4
   tests).
3. A logo whose size cannot be read (SVG with `width="100%"`, no
   `viewBox`; SVG with units like `10cm`): expected the 96 × 96 box and
   a page that renders (Task 1 test).
4. The admin form re-rendered after a validation error (e.g. bad
   `min_display_ms`): expected the submitted colours, header text and
   logo choice kept in the fields, so the preview does not jump back to
   the saved branding (Task 6 test).
5. A non-white text colour with Portico's arch: expected the arch logo,
   the "Powered by Portico" mark and `/.portico/logo` all in the text
   colour, on Keycloak and on Portico's pages (Tasks 1, 3, 4 tests).

---

### Task 0: Plan committed

**Files:**
- Create: `docs/superpowers/plans/2026-10-21-branding-colours-header-preview.md` (this file)

- [ ] **Step 1: Commit**

```bash
git add docs/superpowers/plans/2026-10-21-branding-colours-header-preview.md
git commit -m "docs: branding colours, header and preview plan"
```

---

### Task 1: The branding model

**Files:**
- Modify: `internal/branding/branding.go` (fields, `Default`, `Validate`)
- Create: `internal/branding/header.go` (header rules, logo box, logo file, old documents)
- Modify: `internal/branding/service.go` (`load` reads old documents; `LogoFile`)
- Test: `internal/branding/branding_test.go`, `internal/branding/header_test.go`

**Interfaces:**
- Produces:
  - `Branding` fields `BackgroundColor string \`json:"background_color"\``,
    `TextColor string \`json:"text_color"\``,
    `HeaderText string \`json:"header_text"\``,
    `PorticoLogo bool \`json:"portico_logo"\`` (Portico's arch as logo;
    exclusive with an uploaded logo). `MaxHeaderTextLen = 80`.
  - `type Header struct { Logo bool; LogoW, LogoH int; Text string }` and
    `func (b Branding) Header() Header`: `Logo` when an uploaded logo or
    the arch; `Text` = `HeaderText`, or `MSPName` when there is neither a
    logo nor a header text.
  - `func (b Branding) LogoFile() (data []byte, contentType string, ok bool)`:
    the upload, or `PorticoMark(b.TextColor)` as `image/svg+xml`.
  - `func (b Branding) LogoFileExt() string`: `png`, `jpg` or `svg`
    (`svg` for the arch), `""` without a logo.
  - `type Missing struct{ Background, Text, HeaderText bool }` and
    `func (b *Branding) FillMissing(m Missing)`: background = accent,
    text `#ffffff`, header text = `MSPName` when `Header().Logo`.
  - `func (s *Service) LogoFile(ctx context.Context) ([]byte, string, bool)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestDefaultIsThePrefilledPorticoBranding(t *testing.T) {
	d := Default()
	want := Branding{MSPName: "Portico", HeaderText: "Portico",
		Tagline: "The single, authenticated entry point to your Nutanix services.",
		PorticoLogo: true, BackgroundColor: "#a73422", TextColor: "#ffffff", AccentColor: "#a73422"}
	if !reflect.DeepEqual(d, want) { t.Fatalf("Default() = %+v", d) }
	if err := d.Validate(); err != nil { t.Fatal(err) }
	if h := d.Header(); h != (Header{Logo: true, LogoW: 85, LogoH: 96, Text: "Portico"}) { t.Fatalf("header %+v", h) }
}

// header_test.go
func TestHeaderPieces(t *testing.T) // table, base = Default() with PorticoLogo false:
//   uploaded PNG + header "Portico MSP"   -> {Logo:true, 90, 96, "Portico MSP"}
//   uploaded PNG + header ""              -> {Logo:true, 90, 96, ""}
//   no logo + header "Portico MSP"        -> {Logo:false, 0, 0, "Portico MSP"}
//   no logo + header "" (name "ACME")     -> {Logo:false, 0, 0, "ACME"}
//   PorticoLogo + header ""               -> {Logo:true, 85, 96, ""}
// (PNG: 167 x 178 drawn with image/png in the test)

func TestLogoBox(t *testing.T) // via Header() on an uploaded logo:
//   PNG 167x178 -> 90x96; PNG 40x40 -> 40x40; JPEG 1000x100 -> 320x32;
//   SVG width="600" height="680" -> 85x96; SVG width="600px" height="680px" -> 85x96;
//   SVG viewBox="0 0 1200 300" only -> 320x80;
//   SVG width="100%" (no viewBox) -> 96x96; SVG width="10cm" height="2cm" (no viewBox) -> 96x96

func TestLogoFileIsTheArchInTheTextColour(t *testing.T) {
	b := Default(); b.TextColor = "#222222"
	data, ct, ok := b.LogoFile()
	if !ok || ct != "image/svg+xml" || !strings.Contains(string(data), `fill="#222222"`) || b.LogoFileExt() != "svg" { t.Fatal(...) }
	b.PorticoLogo = false
	if _, _, ok := b.LogoFile(); ok || b.LogoFileExt() != "" { t.Fatal("no logo expected") }
}

func TestValidateRejects // existing table, new rows, each error naming its field:
//   background_color "red"                    -> "background_color must be #rrggbb"
//   text_color ""                             -> "text_color must be #rrggbb"
//   header_text 81 x "a"                      -> "header_text must be at most 80 characters, on one line"
//   header_text "a\nb"                        -> same message
//   PorticoLogo true + an uploaded PNG        -> "portico_logo and an uploaded logo are exclusive"

func TestOldDocumentReadsWithItsLook(t *testing.T) {
	// SetSetting(SettingKey) with the pre-change JSON (no new keys):
	// {"msp_name":"Portico MSP","tagline":"x","accent_color":"#a73422",
	//  "logo_type":"image/svg+xml","logo_data":"<base64 of <svg width=\"600\" height=\"680\"/>>",
	//  "icon_data":null,"min_display_ms":800}
	// Service.Get -> BackgroundColor "#a73422", TextColor "#ffffff",
	// HeaderText "Portico MSP", PorticoLogo false, logo bytes unchanged, MinDisplayMs 800.
	// Same document without logo_type/logo_data -> HeaderText "".
}

func TestFillMissing(t *testing.T) // each flag alone fills only its field; a present field is never touched
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/branding/ -run 'Default|Header|LogoBox|LogoFile|Validate|OldDocument|FillMissing'`
Expected: FAIL (build errors: unknown fields `BackgroundColor`, `Header`, `LogoFile`, `FillMissing`).

- [ ] **Step 3: Implement**

`header.go`: intrinsic size with `image.DecodeConfig` for PNG/JPEG; for
SVG decode the root element with `encoding/xml` and read `width` and
`height` (a bare number or one ending in `px`), else the 3rd and 4th
numbers of `viewBox`, else unknown (96 × 96 box). Scale =
`min(1, 320/w, 96/h)`, `math.Round` each side. The arch is 600 × 680.
`service.go` `load`: unmarshal into `Branding` and into a struct of
`*string` for the three new keys; call `FillMissing` for the absent ones.
Update the tests that assumed the old default (`#1f5f8b`, no logo).

- [ ] **Step 4: Run the package and the suite**

Run: `go test ./internal/branding/ && go test ./...`
Expected: PASS (other packages may need the default's new values in their
expectations; fix those expectations, not the default).

- [ ] **Step 5: Commit**

```bash
git add internal/branding
git commit -m "feat(branding): background and text colours, header text, Portico's arch as default logo"
```

---

### Task 2: Readability warning

**Files:**
- Create: `internal/branding/contrast.go`, `internal/branding/contrast_test.go`

**Interfaces:**
- Consumes: `Branding` fields of Task 1; `*ApplyError` (service.go).
- Produces:
  - `func Contrast(fg, bg string) float64`
  - `func (b Branding) ReadabilityWarnings() []string`
  - `func Warnings(b Branding, saveErr error) string`: after a save that
    stood (`saveErr` nil or `*ApplyError`), the apply failure first
    (`Applying the branding to Keycloak failed: <err>`), then the
    readability warnings, each a capitalised sentence without a final
    full stop, joined with `"; "`; `""` when nothing.

- [ ] **Step 1: Write the failing tests**

```go
func TestContrastReferenceValues(t *testing.T) {
	for _, c := range []struct{ fg, bg string; want float64 }{
		{"#ffffff", "#3b5bdb", 5.298}, {"#ffffff", "#f165b0", 2.905},
		{"#ffffff", "#a73422", 6.651}, {"#222222", "#f5f5f7", 12.018},
		{"#000000", "#ffffff", 21}, {"#777777", "#ffffff", 4.478},
	} {
		if got := Contrast(c.fg, c.bg); math.Abs(got-c.want) > 0.001 { t.Errorf(...) }
	}
}

func TestReadabilityWarnings(t *testing.T) {
	b := Default()
	if w := b.ReadabilityWarnings(); len(w) != 0 { t.Fatal(w) }
	b.AccentColor = "#f165b0"
	// exactly: "White button text on accent colour #f165b0 has contrast 2.90:1, below 4.5:1"
	b.AccentColor, b.BackgroundColor, b.TextColor = "#a73422", "#ffffff", "#777777"
	// exactly: "Text colour #777777 on background #ffffff has contrast 4.48:1, below 4.5:1"
}

func TestWarningsJoinApplyFailureAndReadability(t *testing.T) {
	b := Default(); b.AccentColor = "#f165b0"
	got := Warnings(b, &ApplyError{Err: errors.New("keycloak realm: 502")})
	// "Applying the branding to Keycloak failed: keycloak realm: 502; White button text on accent colour #f165b0 has contrast 2.90:1, below 4.5:1"
	if Warnings(Default(), nil) != "" { t.Fatal("no warning expected") }
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/branding/ -run 'Contrast|Readability|Warnings'`
Expected: FAIL (undefined: `Contrast`).

- [ ] **Step 3: Implement** the three functions in `contrast.go` with the
  Global Constraints' formula.

- [ ] **Step 4: Run**

Run: `go test ./internal/branding/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/branding/contrast.go internal/branding/contrast_test.go
git commit -m "feat(branding): readability warning, WCAG contrast below 4.5:1"
```

---

### Task 3: Keycloak theme

**Files:**
- Modify: `internal/branding/theme.go`
- Test: `internal/branding/theme_test.go` (the lockup tests are replaced)

**Interfaces:**
- Consumes: `Header()`, `LogoFile()`, `LogoFileExt()`, the colour fields.
- Produces: theme files only (CSS classes `portico-logo`, `portico-text`,
  `portico-claim`; messages `loginTitle`, `loginTitleHtml`,
  `loginAccountTitle`).

Generated `loginTitleHtml`, each piece present only when the header has
it, each text HTML-escaped, then MessageFormat-escaped, then
`PropertiesEscape`d:
`<span class="portico-logo" role="img" aria-label="NAME"></span><span class="portico-text">TEXT</span><span class="portico-claim">CLAIM</span>`.
`loginTitle=Sign in to NAME` (MessageFormat-escaped and
properties-escaped, not HTML-escaped: FreeMarker escapes the `<title>`).
MessageFormat escape: `'` → `''`, `{` → `'{'`, `}` → `'}'`.

- [ ] **Step 1: Write the failing tests**

```go
func TestThemeCSSUsesTheThreeColours(t *testing.T) {
	b := Default(); b.BackgroundColor, b.TextColor, b.AccentColor = "#f5f5f7", "#222222", "#3b5bdb"
	css := themeCSS(b)
	// contains: ":root { --portico-bg: #f5f5f7; --portico-text: #222222; --portico-accent: #3b5bdb; }",
	// "body#keycloak-bg { background: var(--portico-bg); }",
	// "color: var(--portico-text);" in the #kc-header-wrapper rule and the ::after rule,
	// "box-shadow: 0 8px 24px rgba(0, 0, 0, 0.16);",
	// ".portico-text { font-size: 2.25rem; line-height: 1.1; font-weight: 700; text-align: center; overflow-wrap: anywhere; }"
	// not: "portico-lockup", "text-transform: uppercase", "rgba(0, 0, 0, 0.28)"
}

func TestThemeCSSLogoBox(t *testing.T)
//   uploaded PNG 167x178 -> ".portico-logo { display: block; width: 90px; height: 96px; max-width: 100%; background: url(../img/logo.png) no-repeat center; background-size: contain; }"
//   Default() (arch)     -> same rule with "width: 85px; height: 96px;" and "url(../img/logo.svg)"
//   no logo              -> no ".portico-logo" rule

func TestThemeMessagesHeaderPieces(t *testing.T) // table on the loginTitleHtml line:
//   logo + text + claim -> `loginTitleHtml=<span class\="portico-logo" role\="img" aria-label\="Portico MSP"></span><span class\="portico-text">Portico MSP</span><span class\="portico-claim">Claim</span>`
//   logo only, no claim -> `loginTitleHtml=<span class\="portico-logo" role\="img" aria-label\="Portico MSP"></span>`
//   text only           -> `loginTitleHtml=<span class\="portico-text">Portico MSP</span>...`
//   neither (name ACME) -> `loginTitleHtml=<span class\="portico-text">ACME</span>...`

func TestThemeMessagesSurviveMessageFormat(t *testing.T) {
	b := Default(); b.PorticoLogo = false
	b.MSPName, b.HeaderText, b.Tagline = "O'Brien {0} Cloud", "O'Brien {0} Cloud", "It's {1}"
	msgs := themeMessages(b)
	// contains "loginTitle=Sign in to O''Brien '{'0'}' Cloud\n"
	// contains `<span class\="portico-text">O&#39;Brien '{'0'}' Cloud</span><span class\="portico-claim">It&#39;s '{'1'}'</span>`
}

func TestWriteThemeDrawsTheArchInTheTextColour(t *testing.T) {
	b := Default(); b.TextColor = "#222222"
	// after WriteTheme: login/resources/img/logo.svg and login/resources/img/portico-mark.svg contain fill="#222222";
	// with an uploaded PNG instead: logo.png written, logo.svg removed (existing replace rule)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/branding/ -run 'Theme'`
Expected: FAIL (old lockup CSS and messages).

- [ ] **Step 3: Implement** in `theme.go`: `themeCSS` and `themeMessages`
  as above; `WriteTheme` writes the logo from `LogoFile()` under
  `img/logo.<LogoFileExt()>` and the mark as `PorticoMark(b.TextColor)`;
  the other rules of today's CSS (column layout, card, button, links)
  stay, with the new shadow. Update `TestWriteThemeFiles` and
  `TestWriteThemeEscapesName` to the new markup.

- [ ] **Step 4: Run**

Run: `go test ./internal/branding/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/branding/theme.go internal/branding/theme_test.go
git commit -m "feat(branding): Keycloak theme with the new header, three colours and the tab title from the MSP name"
```

---

### Task 4: Portico's tenant pages

**Files:**
- Create: `internal/branding/templates/brand.html` (partials `brand-css`, `brand-header`, `brand-powered`)
- Modify: `internal/branding/templates/landing.html`, `sites.html`, `error.html`, `internal/branding/pages.go`
- Test: `internal/branding/pages_test.go`

**Interfaces:**
- Consumes: `Header()`, colour fields, `PorticoMark`.
- Produces: `pageData` loses `NameHead`/`NameTail`, gains
  `Header Header`; `Mark` = `PorticoMark(b.TextColor)`; `HasLogo` goes
  (the header carries it).

`brand-header`:
`<header class="brand">{{if .Header.Logo}}<img class="logo" src="/.portico/logo" width="W" height="H" alt="MSPNAME">{{end}}{{if .Header.Text}}<p class="header-text">TEXT</p>{{end}}{{if .B.Tagline}}<p class="claim">CLAIM</p>{{end}}</header>`.
`brand-css` holds what the three pages share: body background and text
colour, the header column, `.logo { max-width: 100%; height: auto; }`,
`.header-text` (2.25rem, 700, centred, `overflow-wrap: anywhere`), the
claim, `.powered`, the shadow. In the text colour: spinner
(`border: 4px solid color-mix(in srgb, TEXT 35%, transparent);
border-top-color: TEXT`), `.status a`, `.who`, `.who a`. Tile hover
outline: `3px solid ACCENT`.

- [ ] **Step 1: Write the failing tests** (replace `TestPagesWearTheLoginPageLook`)

```go
func TestPagesHeaderPieces(t *testing.T) // landing, sites and error pages, table as in Task 1:
//   logo + text -> `<img class="logo" src="/.portico/logo" width="90" height="96" alt="Portico MSP"><p class="header-text">Portico MSP</p>`
//   logo only   -> the img, no `class="header-text"`
//   text only   -> no `<img`, `<p class="header-text">Portico MSP</p>`
//   neither     -> `<p class="header-text">ACME</p>`
//   every page  -> `<p class="claim">Claim</p>`, "Powered by Portico", not "lockup", not "uppercase"

func TestPagesUseTheThreeColours(t *testing.T) {
	b := Default(); b.BackgroundColor, b.TextColor, b.AccentColor = "#f5f5f7", "#222222", "#3b5bdb"
	// landing contains "background: #f5f5f7", "color: #222222", "border-top-color: #222222",
	//   the mark with fill="#222222", "overflow-wrap: anywhere", "0 8px 24px rgba(0, 0, 0, 0.16)"
	// error page with retry contains "background: #3b5bdb" on a.button
	// sites page contains "outline: 3px solid #3b5bdb"
	// landing contains ".status a { color: #222222; }"; sites page contains ".who a { color: #222222; }";
	// no page contains "rgba(0, 0, 0, .28)" or "rgba(255, 255, 255" (the old white-on-accent rules)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/branding/ -run 'Pages|Sites|Landing|Error'`
Expected: FAIL (old lockup markup).

- [ ] **Step 3: Implement** the partial file, the three templates using
  it, and `pages.go` (`write` fills `Header` and `Mark`; delete `lockup`).
  Keep each page's own rules (spinner, tiles, card text) in its template.

- [ ] **Step 4: Run**

Run: `go test ./internal/branding/ && go test ./internal/proxy/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/branding/templates internal/branding/pages.go internal/branding/pages_test.go
git commit -m "feat(branding): landing, site and error pages with the new header and colours"
```

---

### Task 5: API, logo route and the theme at start

**Files:**
- Modify: `internal/api/branding.go`, `cmd/portico/serve.go`
- Test: `internal/api/api_test.go`, `cmd/portico/serve_test.go`

**Interfaces:**
- Consumes: `FillMissing`, `Missing`, `Warnings`, `LogoFile`,
  `Service.LogoFile`.
- Produces: wire fields `background_color`, `text_color`, `header_text`
  (pointers on input: absent ≠ empty), `portico_logo` (bool, absent =
  false); `warning` = `branding.Warnings(...)`. `ServeLogo` serves
  `LogoFile()`. `serve.go`: `LandingOptions.Logo = brand.LogoFile`; the
  `if brand.Stored(ctx)` gate removed (delete `Service.Stored` if nothing
  else uses it).

- [ ] **Step 1: Write the failing tests**

```go
func TestBrandingGetDefault // updated: msp_name "Portico", header_text "Portico", portico_logo true,
//   background_color "#a73422", text_color "#ffffff", accent_color "#a73422",
//   tagline "The single, authenticated entry point to your Nutanix services.", logo_data ""

func TestBrandingPutWithoutTheNewFieldsKeepsTheOldLook(t *testing.T)
//   PUT {msp_name "Portico MSP", tagline, accent_color "#a73422", logo_type svg, logo_data, min_display_ms 800}
//   -> 200, no warning; GET -> background "#a73422", text "#ffffff", header_text "Portico MSP", portico_logo false
//   same PUT without the logo -> header_text ""
//   PUT with the logo and "header_text": "" -> header_text ""

func TestBrandingPutReadabilityWarning(t *testing.T)
//   PUT Default's fields with accent_color "#f165b0" -> 200,
//   warning "White button text on accent colour #f165b0 has contrast 2.90:1, below 4.5:1"; GET shows the accent saved

func TestBrandingPutRejectsArchWithUpload(t *testing.T) // portico_logo true + logo_data -> 400 naming portico_logo

func TestBrandingLogoServesTheArch(t *testing.T)
//   default branding: GET /api/v1/settings/branding/logo -> 200, "image/svg+xml", body contains fill="#ffffff",
//   CSP "default-src 'none'"

// serve_test.go
func TestServeAppliesTheDefaultBrandingAtStart(t *testing.T)
//   storeServeConfig with no branding stored, cfg.Keycloak.ThemeDir = t.TempDir();
//   after waitHealthy: <ThemeDir>/login/resources/css/portico.css contains "--portico-bg: #a73422"
//   and <ThemeDir>/login/resources/img/logo.svg exists
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/api/ -run Branding && go test ./cmd/portico/ -run TestServeAppliesTheDefaultBrandingAtStart`
Expected: FAIL (unknown fields; theme dir empty at start).

- [ ] **Step 3: Implement** the wire changes, `ServeLogo` via
  `LogoFile`, the `serve.go` changes. `TestBrandingPutWarningWhenApplyFails`
  keeps passing with the new joined text (update its expected string to
  `Warnings`' format).

- [ ] **Step 4: Run**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api cmd/portico
git commit -m "feat: branding API with colours and header text, old clients keep their look; theme applied at every start"
```

---

### Task 6: Admin Branding page with the live preview

**Files:**
- Modify: `internal/web/templates/settings.html`, `internal/web/settings.go`, `internal/web/static/portico.css`
- Create: `internal/web/static/branding-preview.js`
- Test: `internal/web/settings_test.go`, `internal/web/icon_test.go` (if its form posts need the new fields)

**Interfaces:**
- Consumes: `Branding` fields, `Warnings`, `PorticoMark`.
- Produces: form fields `header_text`, `background_color`, `text_color`
  (`<input type="color">`), `logo_source` radios `portico` | `file` |
  `none` (replacing the `remove_logo` checkbox); preview element
  `id="brand-preview"` with `data-saved-logo="1"` when an upload is saved.

Form order: MSP name, logo (radios, current upload shown, file input),
header text, tagline (claim), background, text, accent, tab icon,
minimum landing time. Server rule for the logo: a file uploaded wins and
clears `PorticoLogo`; else `portico` → arch, `none` → no logo, `file` →
keep the saved upload, or 400 `choose a logo file` when there is none.
After a save that stood: `Warnings` not empty → render the page (200)
with `Warning: "Saved. " + warnings`; else redirect as today.

Preview: a drawing next to the form (CSS grid in `portico.css`: two
columns from 960 px, one below), with an inline arch
(`PorticoMark("currentColor")`), an `<img>` for an upload, the header
text, the claim, a white card ("Sign in", Email, Password, the button
"Sign In", the link "Forgot password?") and "Powered by Portico" with the
arch. All its styling in `portico.css` through `--bp-bg`, `--bp-text`,
`--bp-accent`; logo `max-width: 320px; max-height: 96px` scaled with the
drawing. `branding-preview.js` (no library, `defer`): on load and on
every `input`/`change` in the form, set the three variables on
`#brand-preview`; show the arch, the chosen file (`FileReader` →
`data:` URL), the saved upload (`/settings/branding/logo`) or nothing per
`logo_source`; choosing a file selects `file`; header text, else the MSP
name when no logo is shown; the claim; hide empty pieces.

- [ ] **Step 1: Write the failing tests**

```go
func TestBrandingFormSavesColoursAndHeaderText(t *testing.T)
//   POST background_color "#f5f5f7", text_color "#222222", accent_color "#3b5bdb", header_text "Portico MSP", logo_source "portico"
//   -> 303; stored branding has those values and PorticoLogo true

func TestBrandingFormLogoSource(t *testing.T) // table:
//   "none" with a saved upload -> no logo, PorticoLogo false
//   "file" + uploaded PNG      -> upload stored, PorticoLogo false
//   "portico" + uploaded PNG   -> the upload wins
//   "file" without a file and nothing saved -> 400, body contains "choose a logo file"

func TestBrandingFormShowsReadabilityWarning(t *testing.T)
//   POST with accent_color "#f165b0" -> 200, body contains
//   "Saved. White button text on accent colour #f165b0 has contrast 2.90:1, below 4.5:1"; stored accent "#f165b0"

func TestBrandingFormKeepsSubmittedValuesOnError(t *testing.T)
//   POST background_color "#f5f5f7", header_text "Portico MSP", logo_source "none", min_display_ms "abc"
//   -> 400; body has value="#f5f5f7", value="Portico MSP" and the "none" radio checked

func TestBrandingPageHasThePreview(t *testing.T)
//   GET /settings/branding -> `id="brand-preview"`, `src="/static/branding-preview.js?v=`,
//   `fill="currentColor"`, no `style="` attribute and no `<style` in the page;
//   GET /static/branding-preview.js -> 200, Content-Type starting "text/javascript"
```

Replace `TestBrandingFormRemoveLogo` by the `none` row above.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/web/ -run Branding`
Expected: FAIL

- [ ] **Step 3: Implement** form, handler, CSS and script as above.

- [ ] **Step 4: Run**

Run: `go test ./internal/web/ && go test ./...`
Expected: PASS

- [ ] **Step 5: Check the preview by hand** on a local `portico serve`
  (admin listener, factory password) in Chrome: change each colour, the
  header text, the claim, each logo choice and a chosen file; the drawing
  follows without saving; the console shows no CSP error. Note what was
  checked for the write-up.

- [ ] **Step 6: Commit**

```bash
git add internal/web
git commit -m "feat(web): Branding page with colours, header text, logo choice and a live preview"
```

---

### Task 7: Lab scripts

**Files:**
- Modify: `lab/portico/bootstrap-acceptance.sh` (line 115: send `background_color "#a73422"`, `text_color "#ffffff"`, `header_text "Portico MSP"`, `portico_logo false`)
- Modify: `lab/portico/tab-icons.sh` (the "rest unchanged" checksum also covers `background_color`, `text_color`, `header_text`, `portico_logo`)
- Create: `lab/portico/demo-branding.sh`
- Modify: `lab/test_scripts.sh` (shellcheck and `--help` of the new script)

`demo-branding.sh` usage:
`[--logo PNG|SVG] [--arch] [--no-logo] [--msp-name T] [--header-text T] [--tagline T] [--background C] [--text C] [--accent C] | --restore FILE`.
Same environment and curl style as `tab-icons.sh`. Before a change it
saves the current branding JSON to
`$PORTICO_SECRETS/branding-before-<UTC timestamp>.json` (0600, outside
the repository) and prints that path; it then GETs, changes only the
given fields with `jq`, PUTs, prints `warning:` and the text when there
is one (exit 0), and checks: HTTP 200; the fields not given are
unchanged; the login page HTML (`--resolve` to `LAB_VM_IP`) contains
`portico-text` with the header text when one is expected and the theme
CSS contains the three colours; `/.portico/logo` on the Prism Central
name returns the uploaded bytes (shasum) or the arch. It never prints
the logo's content or the texts' sources beyond what was passed.
`--restore FILE` PUTs a saved file back and runs the same checks.

- [ ] **Step 1: Run the checks before the change**

Run: `lab/test_scripts.sh`
Expected: green (baseline).

- [ ] **Step 2: Write the script and the two edits**

- [ ] **Step 3: Run**

Run: `lab/test_scripts.sh && lab/portico/demo-branding.sh --help`
Expected: green; usage printed, exit 0.

- [ ] **Step 4: Commit**

```bash
git add lab
git commit -m "lab: demo branding script; bootstrap and tab icon scripts know the new branding fields"
```

---

### Task 8: Documentation

**Files:**
- Modify: `docs/superpowers/specs/2026-10-17-portico-design.md` section 4.10 ("Keycloak login theme", "Branding settings", per spec section 5)
- Create: `docs/adr/0005-branding-header-and-colours.md` (format of ADR 0004: Status accepted 2026-10-21, Deciders Marco, Claude; Context, Decision, Consequences: one header layout with the default as a pre-filled branding; three colours; the preview drawn by Portico; the theme applied at every start)
- Modify: `docs/operating.md` (keycloak-theme row: written at every start from the branding, the default one when none is saved)

- [ ] **Step 1: Write the three changes.**
- [ ] **Step 2: Check:** `grep -n "background image\|lockup" docs/superpowers/specs/2026-10-17-portico-design.md docs/operating.md`
  Expected: no line describing the old lockup or a background image as current.
- [ ] **Step 3: Commit**

```bash
git add docs
git commit -m "docs: branding colours and header in the design, ADR 0005, operating notes"
```

---

### Task 9: Lab acceptance on the appliance (write-up 23)

**Files:**
- Create: `docs/lab/2026-10-21-23-branding-colours-and-header.md` (template `docs/lab/TEMPLATE.md`), row in `docs/lab/README.md`
- Modify: `docs/lab/lab-inventory.md` (branding row; the new backup dir)

Environment as for `tab-icons.sh` (`PORTICO_SECRETS`, `LAB_VM_IP`, …,
`source lab/env.sh`).

- [ ] **Step 1: Ask Marco** for the deploy. With his OK: back up
  `/var/lib/portico` to `/var/lib/portico/backup-dev-<current sha>-<UTC timestamp>/`
  as in write-up 22, then `lab/portico/deploy.sh`.
- [ ] **Step 2: Upgrade of the existing branding.** GET the branding.
  Expected: background `#a73422`, text `#ffffff`, header text
  `Portico MSP`, `portico_logo` false, the arch SVG kept. Login page HTML:
  `portico-logo` and `<span class="portico-text">Portico MSP</span>`;
  `<title>Sign in to Portico MSP</title>`. `lab/portico/tab-icons.sh`
  (checks only): every line ok.
- [ ] **Step 3: Quotes.** `demo-branding.sh --msp-name "Lab's MSP {0}" --header-text "Lab's MSP {0}"`.
  Expected in the login page HTML: the header text shown as typed (`Lab&#39;s MSP {0}`
  or `Lab's MSP {0}`) and the title `Sign in to Lab&#39;s MSP {0}` (or the
  unescaped form). Record whether `aria-label` survived Keycloak's
  sanitizer. Then `--restore` the file Step 3 saved.
- [ ] **Step 4: Warning.** `demo-branding.sh --accent "#f165b0"`.
  Expected: `warning: White button text on accent colour #f165b0 has contrast 2.90:1, below 4.5:1`.
  `--restore`.
- [ ] **Step 5: Demo.** With the logo path, MSP name and header text
  Marco gives in chat: `demo-branding.sh --logo <path> --msp-name <…> --header-text <…> --background "#f5f5f7" --text "#222222" --accent "#3b5bdb"`.
  Expected: no warning, every check ok.
- [ ] **Step 6: Marco's browser check**, Chrome and Safari: the login
  page (header, colours, tab title), a tenant sign-in through the landing
  page, the site page; in the admin UI the preview following colour,
  text and logo changes without saving. Crops from Marco go only into
  the conversation, not the repository.
- [ ] **Step 7: Write-up 23** (measured on: pc.7.6.0.6, the dev build,
  Keycloak 26.3.5, the browsers and versions, 2026-10-21): the results of
  Steps 2–6, the logo as "a 167×178 RGBA PNG mark (not in the
  repository)", NOT MEASURED: a fresh store's default at start (unit
  test only), Firefox, iOS. Re-measure block with `demo-branding.sh`.
  Inventory: branding row with the demo values (colours only, no names),
  the backup dir.
- [ ] **Step 8: Verify and commit**

Run: `lab/test_scripts.sh && go test ./...`
Expected: green.

```bash
git add docs/lab
git commit -m "lab: write-up 23, branding colours and header on the appliance"
```
