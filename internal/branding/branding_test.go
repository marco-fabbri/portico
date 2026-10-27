package branding

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/store"
)

func TestDefaultIsThePrefilledPorticoBranding(t *testing.T) {
	d := Default()
	want := Branding{MSPName: "Portico", HeaderText: "Portico",
		Tagline:     "The single, authenticated entry point to your Nutanix services.",
		PorticoLogo: true, BackgroundColor: "#a73422", TextColor: "#ffffff", AccentColor: "#a73422"}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("Default() = %+v", d)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v", err)
	}
	if h := d.Header(); h != (Header{Logo: true, LogoW: 85, LogoH: 96, Text: "Portico"}) {
		t.Fatalf("Default().Header() = %+v", h)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Branding)
		want string
	}{
		{"long name", func(b *Branding) { b.MSPName = strings.Repeat("a", MaxNameLen+1) }, "msp_name"},
		{"empty name", func(b *Branding) { b.MSPName = " " }, "msp_name"},
		{"long tagline", func(b *Branding) { b.Tagline = strings.Repeat("a", MaxTaglineLen+1) }, "tagline"},
		{"colour word", func(b *Branding) { b.AccentColor = "red" }, "accent_color"},
		{"colour short", func(b *Branding) { b.AccentColor = "#12345" }, "accent_color"},
		{"colour hex", func(b *Branding) { b.AccentColor = "#GGGGGG" }, "accent_color"},
		{"logo gif", func(b *Branding) { b.LogoType, b.LogoData = "image/gif", []byte("GIF89a") }, "logo_type"},
		{"logo big", func(b *Branding) { b.LogoType, b.LogoData = "image/png", bytes.Repeat([]byte{1}, MaxLogoBytes+1) }, "logo"},
		{"logo type without data", func(b *Branding) { b.LogoType = "image/png" }, "logo"},
		{"display negative", func(b *Branding) { b.MinDisplayMs = -1 }, "min_display_ms"},
		{"display huge", func(b *Branding) { b.MinDisplayMs = MaxDisplayMs + 1 }, "min_display_ms"},
		{"background word", func(b *Branding) { b.BackgroundColor = "red" }, "background_color must be #rrggbb"},
		{"text empty", func(b *Branding) { b.TextColor = "" }, "text_color must be #rrggbb"},
		{"header text long", func(b *Branding) { b.HeaderText = strings.Repeat("a", MaxHeaderTextLen+1) }, "header_text must be at most 80 characters, on one line"},
		{"header text two lines", func(b *Branding) { b.HeaderText = "a\nb" }, "header_text must be at most 80 characters, on one line"},
		{"header text carriage return", func(b *Branding) { b.HeaderText = "a\rb" }, "header_text must be at most 80 characters, on one line"},
		{"arch and upload", func(b *Branding) { b.LogoType, b.LogoData = "image/png", blankPNG(t, 10, 10) }, "portico_logo and an uploaded logo are exclusive"},
	}
	for _, c := range cases {
		b := Default()
		c.mut(&b)
		err := b.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Validate() = %v, want an error naming %s", c.name, err, c.want)
		}
	}
}

type countingStore struct {
	store.Store
	gets int
}

func (c *countingStore) GetSetting(ctx context.Context, key string) ([]byte, error) {
	c.gets++
	return c.Store.GetSetting(ctx, key)
}

func newStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestServiceGetDefaultWhenUnset(t *testing.T) {
	svc := NewService(newStore(t), nil)
	if got := svc.Get(context.Background()); got.MSPName != "Portico" {
		t.Fatalf("Get on an empty store = %+v", got)
	}
}

func TestServiceSetStoresAndCaches(t *testing.T) {
	st := newStore(t)
	cs := &countingStore{Store: st}
	svc := NewService(cs, nil)
	ctx := context.Background()
	b := Default()
	b.PorticoLogo = false
	b.MSPName, b.AccentColor, b.LogoType, b.LogoData = "ACME Cloud", "#8b1f5f", "image/png", []byte("\x89PNG\r\n\x1a\n")
	if err := svc.Set(ctx, b); err != nil {
		t.Fatal(err)
	}
	gets := cs.gets
	if got := svc.Get(ctx); got.MSPName != "ACME Cloud" || !bytes.Equal(got.LogoData, b.LogoData) {
		t.Fatalf("Get after Set = %+v", got)
	}
	if cs.gets != gets {
		t.Fatal("Get after Set read the store again instead of the cache")
	}
	other := NewService(st, nil)
	if got := other.Get(ctx); got.MSPName != "ACME Cloud" || got.AccentColor != "#8b1f5f" || got.LogoType != "image/png" {
		t.Fatalf("Get from a second service = %+v", got)
	}
}

func TestServiceSetValidates(t *testing.T) {
	svc := NewService(newStore(t), nil)
	b := Default()
	b.AccentColor = "blue"
	if err := svc.Set(context.Background(), b); err == nil || !strings.Contains(err.Error(), "accent_color") {
		t.Fatalf("Set invalid = %v", err)
	}
	if svc.Get(context.Background()).AccentColor != Default().AccentColor {
		t.Fatal("an invalid Set changed the value")
	}
}

func TestServiceSetCallsHookAndKeepsSaveOnHookError(t *testing.T) {
	var buf bytes.Buffer
	svc := NewService(newStore(t), slog.New(slog.NewJSONHandler(&buf, nil)))
	called := ""
	svc.OnChange(func(ctx context.Context, b Branding) error {
		called = b.MSPName
		return errors.New("keycloak realm: status 500")
	})
	b := Default()
	b.MSPName = "Hooked"
	err := svc.Set(context.Background(), b)
	var ae *ApplyError
	if !errors.As(err, &ae) || !strings.Contains(ae.Error(), "keycloak realm") {
		t.Fatalf("Set with a failing hook = %v, want *ApplyError", err)
	}
	if called != "Hooked" {
		t.Fatalf("hook saw %q", called)
	}
	if svc.Get(context.Background()).MSPName != "Hooked" {
		t.Fatal("the save was lost because the hook failed")
	}
}

// Review fix 2: Keycloak serves the theme's logo.svg from the login
// origin without a script-forbidding CSP, so an SVG with active content
// is refused at validation, not only neutralised by Portico's headers.
func TestLogoSVGWithActiveContentIsRejected(t *testing.T) {
	for _, bad := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><SCRIPT href="x"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><rect/></a></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><body onload="x"/></foreignObject></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><rect onclick="alert(1)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><rect ONMOUSEOVER = "alert(1)"/></svg>`,
	} {
		b := Default()
		b.LogoType, b.LogoData = "image/svg+xml", []byte(bad)
		if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "logo") {
			t.Errorf("Validate accepted active SVG %q: %v", bad, err)
		}
	}
	b := Default()
	b.PorticoLogo = false
	b.LogoType, b.LogoData = "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10" fill="#8b1f5f"/><text x="1" y="8">on</text></svg>`)
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate rejected an inert SVG: %v", err)
	}
}

// Review before v0.1.0: the check was a substring search, which XML
// character references, entities and other namespaces walk past. The
// SVG is now parsed and only static drawing is accepted.
func TestLogoSVGIsParsedNotSearched(t *testing.T) {
	for name, bad := range map[string]string{
		"char reference":   `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><a xlink:href="java&#115;cript:alert(1)"><rect/></a></svg>`,
		"entity":           `<!DOCTYPE svg [<!ENTITY s "&#60;script>alert(1)&#60;/script>">]><svg xmlns="http://www.w3.org/2000/svg">&s;</svg>`,
		"xhtml script":     `<svg xmlns="http://www.w3.org/2000/svg" xmlns:h="http://www.w3.org/1999/xhtml"><h:script>alert(1)</h:script></svg>`,
		"animated href":    `<svg xmlns="http://www.w3.org/2000/svg"><a href="#x"><set attributeName="href" to="javascript:alert(1)"/><rect/></a></svg>`,
		"external use":     `<svg xmlns="http://www.w3.org/2000/svg"><use href="https://evil.example/x.svg#a"/></svg>`,
		"external css url": `<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:url(https://evil.example/t)"/></svg>`,
		"css import":       `<svg xmlns="http://www.w3.org/2000/svg"><style>@import url(https://evil.example/a.css);</style></svg>`,
		"stylesheet pi":    `<?xml-stylesheet href="https://evil.example/a.css"?><svg xmlns="http://www.w3.org/2000/svg"/>`,
		"not xml":          `<svg xmlns="http://www.w3.org/2000/svg"><rect></svg>`,
	} {
		b := Default()
		b.PorticoLogo = false
		b.LogoType, b.LogoData = "image/svg+xml", []byte(bad)
		if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "logo") {
			t.Errorf("%s: accepted: %v", name, err)
		}
	}
	// What editors export: a prolog, a comment, Inkscape's namespaces,
	// gradients and clip paths referenced by #id, a class stylesheet, an
	// embedded PNG.
	good := `<?xml version="1.0" encoding="UTF-8"?>
<!-- Generator: an editor -->
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd" viewBox="0 0 120 40" width="120" height="40">
  <sodipodi:namedview inkscape:zoom="1"/>
  <title>MSP</title>
  <defs>
    <linearGradient id="g"><stop offset="0" stop-color="#8b1f5f"/><stop offset="1" stop-color="#222"/></linearGradient>
    <clipPath id="c"><rect width="120" height="40"/></clipPath>
    <style>.a{fill:url(#g);} .b { font-family: sans-serif; }</style>
  </defs>
  <g clip-path="url(#c)" inkscape:label="layer">
    <path class="a" d="M0 0h40v40H0z"/>
    <use xlink:href="#p"/>
    <text class="b" x="50" y="25">MSP<tspan dx="2">one</tspan></text>
    <image width="10" height="10" xlink:href="data:image/png;base64,iVBORw0KGgo="/>
  </g>
</svg>`
	b := Default()
	b.PorticoLogo = false
	b.LogoType, b.LogoData = "image/svg+xml", []byte(good)
	if err := b.Validate(); err != nil {
		t.Fatalf("an editor's SVG was refused: %v", err)
	}
}
