package branding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTheme(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

// noLogo is the default branding without Portico's arch.
func noLogo() Branding {
	b := Default()
	b.PorticoLogo = false
	return b
}

func TestWriteThemeFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "theme")
	b := noLogo()
	b.MSPName, b.HeaderText, b.Tagline, b.AccentColor = "ACME Cloud", "", "Your VMs", "#8b1f5f"
	if err := WriteTheme(dir, b); err != nil {
		t.Fatalf("WriteTheme: %v", err)
	}
	props := readTheme(t, dir, "login/theme.properties")
	for _, want := range []string{"parent=keycloak.v2", "import=common/keycloak", "styles=css/styles.css css/portico.css"} {
		if !strings.Contains(props, want) {
			t.Errorf("theme.properties lacks %q: %s", want, props)
		}
	}
	css := readTheme(t, dir, "login/resources/css/portico.css")
	if !strings.Contains(css, "#8b1f5f") || !strings.Contains(css, ".pf-v5-c-button.pf-m-primary") {
		t.Errorf("css lacks the colour or the button rule: %s", css)
	}
	msgs := readTheme(t, dir, "login/messages/messages_en.properties")
	for _, want := range []string{
		`loginTitleHtml=<span class\="portico-text">ACME Cloud</span><span class\="portico-claim">Your VMs</span>` + "\n",
		"loginTitle=Sign in to ACME Cloud\n",
		"loginAccountTitle=Sign in\n",
	} {
		if !strings.Contains(msgs, want) {
			t.Errorf("messages lack %q: %s", want, msgs)
		}
	}
	for _, f := range []string{"login/theme.properties", "login/resources/css/portico.css", "login/messages/messages_en.properties"} {
		if info, _ := os.Stat(filepath.Join(dir, f)); info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode %04o, want 0644", f, info.Mode().Perm())
		}
	}
}

func TestWriteThemeEscapesName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "theme")
	b := noLogo()
	b.MSPName, b.HeaderText = "A=B:C <b>x</b>", ""
	b.Tagline = "line one\nline two #tag"
	if err := WriteTheme(dir, b); err != nil {
		t.Fatal(err)
	}
	msgs := readTheme(t, dir, "login/messages/messages_en.properties")
	if !strings.Contains(msgs, `<span class\="portico-text">A\=B\:C &lt;b&gt;x&lt;/b&gt;</span>`) {
		t.Errorf("name not escaped: %s", msgs)
	}
	if !strings.Contains(msgs, `<span class\="portico-claim">line one\nline two \#tag</span>`) {
		t.Errorf("tagline not escaped: %s", msgs)
	}
	if strings.Count(msgs, "\n") != 5 { // one line per key
		t.Errorf("a raw newline reached the properties file: %q", msgs)
	}
}

func TestWriteThemeLogoByType(t *testing.T) {
	cases := []struct{ ctype, file string }{{"image/png", "logo.png"}, {"image/jpeg", "logo.jpg"}, {"image/svg+xml", "logo.svg"}}
	for _, c := range cases {
		dir := filepath.Join(t.TempDir(), "theme")
		b := noLogo()
		b.LogoType, b.LogoData = c.ctype, []byte("data")
		if err := WriteTheme(dir, b); err != nil {
			t.Fatal(err)
		}
		if got := readTheme(t, dir, "login/resources/img/"+c.file); got != "data" {
			t.Errorf("%s: logo file content %q", c.ctype, got)
		}
		css := readTheme(t, dir, "login/resources/css/portico.css")
		if !strings.Contains(css, "img/"+c.file) {
			t.Errorf("%s: css lacks the logo rule: %s", c.ctype, css)
		}
		// A fixed height on the wrapper clipped the title under the logo
		// (write-up 13): the height belongs to the logo block only, never
		// to #kc-header-wrapper itself.
		for _, line := range strings.Split(css, "\n") {
			if strings.HasPrefix(line, "#kc-header-wrapper {") && (strings.Contains(line, " height:") || strings.Contains(line, "min-height:")) {
				t.Errorf("%s: the wrapper rule sets a height: %s", c.ctype, line)
			}
		}
	}
	dir := filepath.Join(t.TempDir(), "theme")
	if err := WriteTheme(dir, noLogo()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "login/resources/img")); err == nil {
		// Portico's own mark and the tab icon, never a logo file.
		entries, _ := os.ReadDir(filepath.Join(dir, "login/resources/img"))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "logo.") {
				t.Errorf("logo file without a logo: %s", e.Name())
			}
		}
	}
	if css := readTheme(t, dir, "login/resources/css/portico.css"); strings.Contains(css, "img/logo") {
		t.Errorf("css has a logo rule without a logo: %s", css)
	}
}

func TestWriteThemeReplacesOldLogo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "theme")
	b := noLogo()
	b.LogoType, b.LogoData = "image/png", []byte("png")
	if err := WriteTheme(dir, b); err != nil {
		t.Fatal(err)
	}
	b.LogoType, b.LogoData = "image/svg+xml", []byte("<svg/>")
	if err := WriteTheme(dir, b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "login/resources/img/logo.png")); err == nil {
		t.Error("the old logo file was left behind")
	}
}

func TestWriteThemeUnwritableDir(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "theme")
	err := WriteTheme(dir, Default())
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("WriteTheme into a read-only parent = %v, want an error naming %s", err, dir)
	}
}

func TestPropertiesEscape(t *testing.T) {
	cases := map[string]string{
		"plain":         "plain",
		"a=b":           `a\=b`,
		"a:b":           `a\:b`,
		"#x":            `\#x`,
		"back\\slash":   `back\\slash`,
		"new\nline":     `new\nline`,
		"cr\rlf":        `cr\rlf`,
		"tab\there":     `tab\there`,
		"  lead":        `\ \ lead`,
		"città ünïcode": "città ünïcode",
	}
	for in, want := range cases {
		if got := PropertiesEscape(in); got != want {
			t.Errorf("PropertiesEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

// The login page is one centred column on the accent colour: the header
// (logo left of the MSP name, white, Red Hat Display) above the card,
// and a "Powered by Portico" line under it (layout pass after 2f).
func TestThemeCSSUsesTheThreeColours(t *testing.T) {
	b := Default()
	b.BackgroundColor, b.TextColor, b.AccentColor = "#f5f5f7", "#222222", "#3b5bdb"
	css := themeCSS(b)
	for _, want := range []string{
		":root { --portico-bg: #f5f5f7; --portico-text: #222222; --portico-accent: #3b5bdb; }",
		"body#keycloak-bg { background: var(--portico-bg); }",
		".pf-v5-c-login__container { display: flex; flex-direction: column; align-items: center;",
		".pf-v5-c-login__main { order: 1; grid-area: auto; align-self: center;",
		".pf-v5-c-login__main-header { border-top: 0; }",
		"box-shadow: 0 8px 24px rgba(0, 0, 0, 0.16);",
		".portico-text { font-size: 2.25rem; line-height: 1.1; font-weight: 700; text-align: center; overflow-wrap: anywhere; }",
		".portico-claim { font-size: 1.05rem; font-weight: 400; opacity: 0.9; text-align: center; }",
		`.pf-v5-c-login__container::after { content: "Powered by Portico";`,
		"url(../img/portico-mark.svg)",
		".pf-v5-c-button.pf-m-primary { background-color: var(--portico-accent);",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("css lacks %q:\n%s", want, css)
		}
	}
	for _, line := range strings.Split(css, "\n") {
		if (strings.HasPrefix(line, "#kc-header-wrapper {") || strings.HasPrefix(line, ".pf-v5-c-login__container::after {")) && !strings.Contains(line, "color: var(--portico-text)") {
			t.Errorf("not in the text colour: %s", line)
		}
		// keycloak.v2's styles.css sets #kc-header-wrapper's colour to
		// white with !important; only an !important rule of the same
		// selector, loaded later, wins (write-up 23).
		if strings.HasPrefix(line, "#kc-header-wrapper {") && !strings.Contains(line, "color: var(--portico-text) !important;") {
			t.Errorf("the header colour loses to Keycloak's !important white: %s", line)
		}
	}
	for _, old := range []string{"portico-lockup", "text-transform: uppercase", "rgba(0, 0, 0, 0.28)", "color: #fff"} {
		if strings.Contains(css, old) {
			t.Errorf("css still has %q:\n%s", old, css)
		}
	}
}

func TestThemeCSSLogoBox(t *testing.T) {
	b := noLogo()
	b.LogoType, b.LogoData = "image/png", blankPNG(t, 167, 178)
	if css := themeCSS(b); !strings.Contains(css, ".portico-logo { display: block; width: 90px; height: 96px; max-width: 100%; background: url(../img/logo.png) no-repeat center; background-size: contain; }") {
		t.Errorf("uploaded logo box:\n%s", css)
	}
	if css := themeCSS(Default()); !strings.Contains(css, ".portico-logo { display: block; width: 85px; height: 96px; max-width: 100%; background: url(../img/logo.svg) no-repeat center; background-size: contain; }") {
		t.Errorf("arch box:\n%s", css)
	}
	if css := themeCSS(noLogo()); strings.Contains(css, ".portico-logo") {
		t.Errorf("a logo rule without a logo:\n%s", css)
	}
}

func TestThemeMessagesHeaderPieces(t *testing.T) {
	logo := blankPNG(t, 167, 178)
	cases := []struct {
		name string
		mut  func(*Branding)
		want string
	}{
		{"logo, text and claim", func(b *Branding) {
			b.LogoType, b.LogoData, b.HeaderText, b.Tagline = "image/png", logo, "Portico MSP", "Claim"
		},
			`loginTitleHtml=<span class\="portico-logo" role\="img" aria-label\="Portico MSP"></span><span class\="portico-text">Portico MSP</span><span class\="portico-claim">Claim</span>` + "\n"},
		{"logo only", func(b *Branding) { b.LogoType, b.LogoData, b.HeaderText, b.Tagline = "image/png", logo, "", "" },
			`loginTitleHtml=<span class\="portico-logo" role\="img" aria-label\="Portico MSP"></span>` + "\n"},
		{"text only", func(b *Branding) { b.HeaderText, b.Tagline = "Portico MSP", "Claim" },
			`loginTitleHtml=<span class\="portico-text">Portico MSP</span><span class\="portico-claim">Claim</span>` + "\n"},
		{"neither: the MSP name", func(b *Branding) { b.HeaderText, b.MSPName, b.Tagline = "", "ACME", "" },
			`loginTitleHtml=<span class\="portico-text">ACME</span>` + "\n"},
	}
	for _, c := range cases {
		b := noLogo()
		b.MSPName = "Portico MSP"
		c.mut(&b)
		if msgs := themeMessages(b); !strings.Contains(msgs, c.want) {
			t.Errorf("%s: messages %q, want %q", c.name, msgs, c.want)
		}
	}
}

// Keycloak formats every message with Java's MessageFormat, which eats
// a single quote and reads {0} as an argument (Review Focus 1).
func TestThemeMessagesSurviveMessageFormat(t *testing.T) {
	b := noLogo()
	b.MSPName, b.HeaderText, b.Tagline = "O'Brien {0} Cloud", "O'Brien {0} Cloud", "It's {1}"
	msgs := themeMessages(b)
	for _, want := range []string{
		"loginTitle=Sign in to O''Brien '{'0'}' Cloud\n",
		`<span class\="portico-text">O&\#39;Brien '{'0'}' Cloud</span><span class\="portico-claim">It&\#39;s '{'1'}'</span>`,
	} {
		if !strings.Contains(msgs, want) {
			t.Errorf("messages lack %q: %q", want, msgs)
		}
	}
}

func TestWriteThemeDrawsTheArchInTheTextColour(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "theme")
	b := Default()
	b.TextColor = "#222222"
	if err := WriteTheme(dir, b); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"login/resources/img/logo.svg", "login/resources/img/portico-mark.svg"} {
		if got := readTheme(t, dir, f); !strings.Contains(got, `fill="#222222"`) {
			t.Errorf("%s not in the text colour: %q", f, got)
		}
	}
	b.PorticoLogo, b.LogoType, b.LogoData = false, "image/png", blankPNG(t, 10, 10)
	if err := WriteTheme(dir, b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "login/resources/img/logo.svg")); err == nil {
		t.Error("the arch was left behind after an upload")
	}
	if _, err := os.Stat(filepath.Join(dir, "login/resources/img/logo.png")); err != nil {
		t.Errorf("uploaded logo not written: %v", err)
	}
}

// The Portico mark ships with every theme write, and the theme lists
// keycloak.v2's styles.css (login.css is gone in Keycloak 26.3: 404 on
// Keycloak 26.3, so the parent's base styles were not loaded).
func TestWriteThemeShipsTheMarkAndTheParentStyles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "theme")
	if err := WriteTheme(dir, Default()); err != nil {
		t.Fatal(err)
	}
	if mark := readTheme(t, dir, "login/resources/img/portico-mark.svg"); !strings.Contains(mark, "<svg") || !strings.Contains(mark, `fill="#ffffff"`) {
		t.Fatalf("mark: %q", mark)
	}
	if props := readTheme(t, dir, "login/theme.properties"); !strings.Contains(props, "styles=css/styles.css css/portico.css") {
		t.Fatalf("theme.properties: %q", props)
	}
}

// Keycloak's SAML post page, shown while the assertion goes to Prism
// Central, looks like Portico's landing page that follows it: no card,
// a spinner and "Signing you in…" in the text colour, centred
// (write-up 23, Marco: "continuity").
func TestThemeSAMLPostPageLooksLikeTheLanding(t *testing.T) {
	b := Default()
	css := themeCSS(b)
	const page = `body[data-page-id="login-saml-post-form"]`
	for _, want := range []string{
		page + " .pf-v5-c-login__main { background: none; box-shadow: none; }",
		page + " .pf-v5-c-login__main-header { display: none; }",
		page + " .pf-v5-c-login__main-body { text-align: center; color: var(--portico-text); }",
		page + " .pf-v5-c-login__main-body::before { content: \"\"; display: block; width: 36px; height: 36px; margin: 0 auto 1rem; border: 4px solid color-mix(in srgb, var(--portico-text) 35%, transparent); border-top-color: var(--portico-text); border-radius: 50%; animation: portico-spin 1s linear infinite; }",
		"@keyframes portico-spin { to { transform: rotate(360deg); } }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("css lacks %q", want)
		}
	}
	msgs := themeMessages(b)
	for _, want := range []string{"saml.post-form.title=Signing you in\n", "saml.post-form.message=Signing you in\\u2026\n"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("messages lack %q:\n%s", want, msgs)
		}
	}
}
