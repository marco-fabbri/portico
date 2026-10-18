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
	for _, want := range []string{"parent=keycloak.v2", "import=common/keycloak", "styles=css/login.css css/portico.css"} {
		if !strings.Contains(props, want) {
			t.Errorf("theme.properties lacks %q: %s", want, props)
		}
	}
	css := readTheme(t, dir, "login/resources/css/portico.css")
	if !strings.Contains(css, "#8b1f5f") || !strings.Contains(css, ".pf-v5-c-button.pf-m-primary") {
		t.Errorf("css lacks the colour or the button rule: %s", css)
	}
	msgs := readTheme(t, dir, "login/messages/messages_en.properties")
	if !strings.Contains(msgs, "loginTitleHtml=ACME Cloud") || !strings.Contains(msgs, "loginAccountTitle=Your VMs") {
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
	if !strings.Contains(msgs, `loginTitleHtml=A\=B\:C &lt;b&gt;x&lt;/b&gt;`) {
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
		// A fixed height clipped the title under the logo on the lab (write-up 13).
		if !strings.Contains(css, "min-height") || strings.Contains(css, " height:") {
			t.Errorf("%s: the logo rule must use min-height, not height: %s", c.ctype, css)
		}
	}
	dir := filepath.Join(t.TempDir(), "theme")
	if err := WriteTheme(dir, Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "login/resources/img")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(dir, "login/resources/img"))
		if len(entries) != 0 {
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
