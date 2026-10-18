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

func TestWriteThemeFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "theme")
	b := Default()
	b.MSPName, b.Tagline, b.AccentColor = "ACME Cloud", "Your VMs", "#8b1f5f"
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
	if !strings.Contains(msgs, "loginTitleHtml=ACME <strong>Cloud</strong>") || !strings.Contains(msgs, "loginAccountTitle=Your VMs") {
		t.Errorf("messages: %s", msgs)
	}
	for _, f := range []string{"login/theme.properties", "login/resources/css/portico.css", "login/messages/messages_en.properties"} {
		if info, _ := os.Stat(filepath.Join(dir, f)); info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode %04o, want 0644", f, info.Mode().Perm())
		}
	}
}

func TestWriteThemeEscapesName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "theme")
	b := Default()
	b.MSPName = "A=B:C <b>x</b>"
	b.Tagline = "line one\nline two #tag"
	if err := WriteTheme(dir, b); err != nil {
		t.Fatal(err)
	}
	msgs := readTheme(t, dir, "login/messages/messages_en.properties")
	if !strings.Contains(msgs, `loginTitleHtml=A\=B\:C <strong>&lt;b&gt;x&lt;/b&gt;</strong>`) {
		t.Errorf("name not escaped: %s", msgs)
	}
	if !strings.Contains(msgs, `loginAccountTitle=line one\nline two \#tag`) {
		t.Errorf("tagline not escaped: %s", msgs)
	}
	if strings.Count(msgs, "\n") != 2 {
		t.Errorf("a raw newline reached the properties file: %q", msgs)
	}
}

func TestWriteThemeLogoByType(t *testing.T) {
	cases := []struct{ ctype, file string }{{"image/png", "logo.png"}, {"image/jpeg", "logo.jpg"}, {"image/svg+xml", "logo.svg"}}
	for _, c := range cases {
		dir := filepath.Join(t.TempDir(), "theme")
		b := Default()
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
	if err := WriteTheme(dir, Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "login/resources/img")); err == nil {
		// Only Portico's own mark, never a logo file.
		entries, _ := os.ReadDir(filepath.Join(dir, "login/resources/img"))
		if len(entries) != 1 || entries[0].Name() != "portico-mark.svg" {
			t.Errorf("logo files without a logo: %v", entries)
		}
	}
	if css := readTheme(t, dir, "login/resources/css/portico.css"); strings.Contains(css, "img/logo") {
		t.Errorf("css has a logo rule without a logo: %s", css)
	}
}

func TestWriteThemeReplacesOldLogo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "theme")
	b := Default()
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
func TestThemeCSSCentresTheLoginUnderTheLockup(t *testing.T) {
	b := Default()
	b.LogoType, b.LogoData = "image/svg+xml", []byte("data")
	css := themeCSS(b)
	for _, want := range []string{
		"body#keycloak-bg { background: var(--portico-accent); }",
		".pf-v5-c-login__container { display: flex; flex-direction: column; align-items: center;",
		"#kc-header-wrapper { display: flex; align-items: center;",
		".pf-v5-c-login__main { order: 1; grid-area: auto; align-self: center;",
		".pf-v5-c-login__main-header { border-top: 0; }",
		`font-family: "RedHatDisplay"`,
		"color: #fff;",
		`#kc-header-wrapper::before { content: ""; width: 56px; height: 64px;`,
		"url(../img/logo.svg)",
		"#kc-header-wrapper strong { font-weight: 700; }",
		`.pf-v5-c-login__container::after { content: "Powered by Portico";`,
		"url(../img/portico-mark.svg)",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("css lacks %q:\n%s", want, css)
		}
	}
	if strings.Contains(css, "padding-top") || strings.Contains(css, "#1f2937") {
		t.Fatalf("old header rules still there:\n%s", css)
	}
	// Without a logo the lockup is the name alone.
	if strings.Contains(themeCSS(Default()), "#kc-header-wrapper::before") {
		t.Fatal("a logo rule without a logo")
	}
}

// The last word of the MSP name is bold in the lockup ("Portico MSP"):
// the title is HTML, escaped per word.
func TestThemeMessagesBoldTheLastWord(t *testing.T) {
	b := Default()
	b.MSPName = "Portico MSP"
	if msgs := themeMessages(b); !strings.Contains(msgs, "loginTitleHtml=Portico <strong>MSP</strong>\n") {
		t.Fatalf("messages: %q", msgs)
	}
	b.MSPName = "A&B <x>"
	if msgs := themeMessages(b); !strings.Contains(msgs, "loginTitleHtml=A&amp;B <strong>&lt;x&gt;</strong>\n") {
		t.Fatalf("escaped: %q", msgs)
	}
	b.MSPName = "Solo"
	if msgs := themeMessages(b); !strings.Contains(msgs, "loginTitleHtml=Solo\n") {
		t.Fatalf("one word: %q", msgs)
	}
}

// The Portico mark ships with every theme write, and the theme lists
// keycloak.v2's styles.css (login.css is gone in Keycloak 26.3: 404 on
// the lab, so the parent's base styles were not loaded).
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
