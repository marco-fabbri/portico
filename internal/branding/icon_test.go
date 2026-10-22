package branding

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngOf encodes a w by h image painted by fill.
func pngOf(t *testing.T, w, h int, fill func(x, y int) color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, fill(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func solid(c color.NRGBA) func(x, y int) color.NRGBA {
	return func(int, int) color.NRGBA { return c }
}

var (
	red   = color.NRGBA{255, 0, 0, 255}
	clear = color.NRGBA{0, 0, 0, 0}
)

func TestValidateIcon(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string // "" = valid
	}{
		{"square PNG", pngOf(t, 256, 256, solid(red)), ""},
		{"smallest", pngOf(t, MinIconSide, MinIconSide, solid(red)), ""},
		{"an exported logo (write-up 22)", pngOf(t, 167, 178, solid(red)), ""},
		{"wider than tall", pngOf(t, 300, 200, solid(red)), ""},
		{"not a PNG", []byte("GIF89a not a png"), "icon must be a PNG"},
		{"too small", pngOf(t, 100, 64, solid(red)), "icon must be 128 to 1024 px on its longer side"},
		{"too large", pngOf(t, 1100, 1000, solid(red)), "icon must be 128 to 1024 px on its longer side"},
		{"a banner", pngOf(t, 400, 150, solid(red)), "icon's shorter side must be at least half its longer side"},
		{"a column", pngOf(t, 150, 400, solid(red)), "icon's shorter side must be at least half its longer side"},
		{"too many bytes", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxLogoBytes)...), "icon must be at most 262144 bytes"},
	}
	for _, c := range cases {
		b := Default()
		b.IconData = c.data
		err := b.Validate()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

// icoEntry is one directory entry of an ICO file with its PNG decoded.
type icoEntry struct {
	w, h int
	img  image.Image
}

func parseICO(t *testing.T, data []byte) []icoEntry {
	t.Helper()
	if len(data) < 6 || binary.LittleEndian.Uint16(data[0:]) != 0 || binary.LittleEndian.Uint16(data[2:]) != 1 {
		t.Fatalf("not an ICO header: % x", data[:min(6, len(data))])
	}
	n := int(binary.LittleEndian.Uint16(data[4:]))
	var out []icoEntry
	for i := 0; i < n; i++ {
		e := data[6+16*i : 6+16*(i+1)]
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		if binary.LittleEndian.Uint16(e[4:]) != 1 || binary.LittleEndian.Uint16(e[6:]) != 32 {
			t.Fatalf("entry %d: planes %d bpp %d", i, binary.LittleEndian.Uint16(e[4:]), binary.LittleEndian.Uint16(e[6:]))
		}
		size, off := binary.LittleEndian.Uint32(e[8:]), binary.LittleEndian.Uint32(e[12:])
		img, err := png.Decode(bytes.NewReader(data[off : off+size]))
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
			t.Fatalf("entry %d: directory says %dx%d, PNG is %v", i, w, h, img.Bounds())
		}
		out = append(out, icoEntry{w, h, img})
	}
	return out
}

func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func nrgba(img image.Image, x, y int) color.NRGBA {
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

// near reports whether two colours differ by at most tol per channel.
func near(a, b color.NRGBA, tol int) bool {
	d := func(x, y uint8) bool { v := int(x) - int(y); return v <= tol && v >= -tol }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && d(a.A, b.A)
}

func TestTenantIconsFromUpload(t *testing.T) {
	b := Default()
	b.IconData = pngOf(t, 256, 256, solid(red))
	ic, err := TenantIcons(b)
	if err != nil {
		t.Fatal(err)
	}
	entries := parseICO(t, ic.ICO)
	if len(entries) != 3 || entries[0].w != 16 || entries[1].w != 32 || entries[2].w != 48 {
		t.Fatalf("ICO sizes: %+v", entries)
	}
	for _, e := range entries {
		if c := nrgba(e.img, e.w/2, e.h/2); !near(c, red, 1) {
			t.Errorf("%d px: centre %v, want red", e.w, c)
		}
	}
	touch := decodePNG(t, ic.Touch)
	if touch.Bounds().Dx() != 180 || touch.Bounds().Dy() != 180 {
		t.Fatalf("touch icon: %v", touch.Bounds())
	}
	if c := nrgba(touch, 90, 90); !near(c, red, 1) {
		t.Errorf("touch centre %v, want red", c)
	}
}

// Downscaling averages: the left half black and the right half white
// stay black and white away from the middle.
func TestTenantIconsAverageWhenScaling(t *testing.T) {
	b := Default()
	b.IconData = pngOf(t, 256, 256, func(x, y int) color.NRGBA {
		if x < 128 {
			return color.NRGBA{0, 0, 0, 255}
		}
		return color.NRGBA{255, 255, 255, 255}
	})
	ic, err := TenantIcons(b)
	if err != nil {
		t.Fatal(err)
	}
	e := parseICO(t, ic.ICO)[0]
	if c := nrgba(e.img, 3, 8); !near(c, color.NRGBA{0, 0, 0, 255}, 1) {
		t.Errorf("left: %v", c)
	}
	if c := nrgba(e.img, 12, 8); !near(c, color.NRGBA{255, 255, 255, 255}, 1) {
		t.Errorf("right: %v", c)
	}
}

// A PNG that is not square is centred on a transparent square: 300 by
// 200 px at 48 px is 32 px tall, from row 8 to row 40.
func TestTenantIconsCentreAnIconThatIsNotSquare(t *testing.T) {
	b := Default()
	b.IconData = pngOf(t, 300, 200, solid(red))
	ic, err := TenantIcons(b)
	if err != nil {
		t.Fatal(err)
	}
	e := parseICO(t, ic.ICO)[2]
	if c := nrgba(e.img, 24, 2); c.A != 0 {
		t.Errorf("above the icon: %v, want transparent", c)
	}
	if c := nrgba(e.img, 24, 45); c.A != 0 {
		t.Errorf("below the icon: %v, want transparent", c)
	}
	if c := nrgba(e.img, 2, 24); !near(c, red, 1) {
		t.Errorf("left edge at mid height: %v, want red", c)
	}
	touch := decodePNG(t, ic.Touch)
	if c := nrgba(touch, 90, 5); !near(c, color.NRGBA{255, 255, 255, 255}, 0) {
		t.Errorf("touch icon above the icon: %v, want white", c)
	}
}

// An icon smaller than 180 px is enlarged for the touch icon with a
// smooth filter: a hard black-to-white edge becomes a short ramp, not a
// step, and the halves keep their colour.
func TestTouchIconOfASmallIconIsSmooth(t *testing.T) {
	b := Default()
	b.IconData = pngOf(t, 128, 128, func(x, y int) color.NRGBA {
		if x < 64 {
			return color.NRGBA{0, 0, 0, 255}
		}
		return color.NRGBA{255, 255, 255, 255}
	})
	ic, err := TenantIcons(b)
	if err != nil {
		t.Fatal(err)
	}
	touch := decodePNG(t, ic.Touch)
	if c := nrgba(touch, 20, 90); !near(c, color.NRGBA{0, 0, 0, 255}, 1) {
		t.Errorf("left: %v", c)
	}
	if c := nrgba(touch, 160, 90); !near(c, color.NRGBA{255, 255, 255, 255}, 1) {
		t.Errorf("right: %v", c)
	}
	// The edge falls between pixels 89 and 90: both are grey (a nearest-
	// neighbour enlargement would give pure black and pure white).
	for _, x := range []int{89, 90} {
		if c := nrgba(touch, x, 90); c.R <= 2 || c.R >= 253 {
			t.Errorf("the edge pixel %d is %v: a step, not a ramp", x, c)
		}
	}
}

// iOS fills the transparent parts of a touch icon with black: Portico
// puts the icon on white instead.
func TestTouchIconIsOpaque(t *testing.T) {
	b := Default()
	b.IconData = pngOf(t, 200, 200, solid(clear))
	ic, err := TenantIcons(b)
	if err != nil {
		t.Fatal(err)
	}
	if c := nrgba(decodePNG(t, ic.Touch), 10, 10); !near(c, color.NRGBA{255, 255, 255, 255}, 0) {
		t.Errorf("transparent pixel of the touch icon: %v, want opaque white", c)
	}
	if c := nrgba(parseICO(t, ic.ICO)[1].img, 16, 16); c.A != 0 {
		t.Errorf("the favicon keeps transparency: %v", c)
	}
}

// Without an uploaded icon: Portico's arch in white on a rounded square
// of the accent colour; the touch icon is the full square.
func TestTenantIconsFallbackIsTheArchOnTheAccentColour(t *testing.T) {
	b := Default()
	b.AccentColor = "#1f5f8b"
	accent := color.NRGBA{0x1f, 0x5f, 0x8b, 255}
	white := color.NRGBA{255, 255, 255, 255}
	ic, err := TenantIcons(b)
	if err != nil {
		t.Fatal(err)
	}
	e32 := parseICO(t, ic.ICO)[1]
	if c := nrgba(e32.img, 0, 0); c.A != 0 {
		t.Errorf("32 px corner: %v, want transparent (rounded square)", c)
	}
	if c := nrgba(e32.img, 2, 16); !near(c, accent, 2) {
		t.Errorf("32 px background: %v, want the accent colour", c)
	}
	// The left pillar of the arch (x 50 to 162, y 300 to 620 of the mark).
	if c := nrgba(e32.img, 9, 21); !near(c, white, 8) {
		t.Errorf("32 px left pillar: %v, want white", c)
	}
	touch := decodePNG(t, ic.Touch)
	if c := nrgba(touch, 0, 0); !near(c, accent, 2) {
		t.Errorf("touch corner: %v, want the accent colour, opaque", c)
	}
	if c := nrgba(touch, 50, 123); !near(c, white, 8) {
		t.Errorf("touch left pillar: %v, want white", c)
	}
	// Inside the arch's opening, right of the inner arch (x 400, y 550
	// of the mark): background.
	if c := nrgba(touch, 110, 133); !near(c, accent, 2) {
		t.Errorf("touch inside the opening: %v, want the accent colour", c)
	}
}

func TestAdminIconsAreTheArchOnDarkGrey(t *testing.T) {
	ic := AdminIcons()
	e32 := parseICO(t, ic.ICO)[1]
	if c := nrgba(e32.img, 2, 16); !near(c, color.NRGBA{0x1d, 0x1d, 0x1b, 255}, 2) {
		t.Errorf("admin background: %v", c)
	}
	if c := nrgba(e32.img, 9, 21); !near(c, color.NRGBA{255, 255, 255, 255}, 8) {
		t.Errorf("admin pillar: %v", c)
	}
}

// The version in the pages' links changes with what the icon shows, so
// a browser (Safari keeps its own favicon cache) fetches the new one.
func TestIconVersion(t *testing.T) {
	a, b := Default(), Default()
	if a.IconVersion() != b.IconVersion() || len(a.IconVersion()) != 12 {
		t.Fatalf("same branding, versions %q %q", a.IconVersion(), b.IconVersion())
	}
	b.AccentColor = "#000000"
	if a.IconVersion() == b.IconVersion() {
		t.Error("the accent colour does not change the version")
	}
	c := Default()
	c.IconData = pngOf(t, 180, 180, solid(red))
	if c.IconVersion() == a.IconVersion() {
		t.Error("an uploaded icon does not change the version")
	}
	d := c
	d.MSPName = "Other"
	if d.IconVersion() != c.IconVersion() {
		t.Error("the MSP name changes the version")
	}
}

// The service computes the icons once per branding and again after a
// save that changes them.
func TestServiceIconsFollowTheBranding(t *testing.T) {
	svc := NewService(newStore(t), nil)
	ctx := context.Background()
	first := svc.Icons(ctx)
	want, _ := TenantIcons(Default())
	if !bytes.Equal(first.ICO, want.ICO) || !bytes.Equal(first.Touch, want.Touch) {
		t.Fatal("icons of the default branding differ from TenantIcons(Default())")
	}
	b := Default()
	b.IconData = pngOf(t, 180, 180, solid(red))
	if err := svc.Set(ctx, b); err != nil {
		t.Fatal(err)
	}
	if c := nrgba(decodePNG(t, svc.Icons(ctx).Touch), 90, 90); !near(c, red, 1) {
		t.Errorf("after uploading an icon the touch icon is %v, want red", c)
	}
}

func TestWriteThemeWritesTheFavicon(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "theme")
	b := Default()
	b.IconData = pngOf(t, 180, 180, solid(red))
	if err := WriteTheme(dir, b); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "login/resources/img/favicon.ico"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := TenantIcons(b)
	if !bytes.Equal(got, want.ICO) {
		t.Error("the theme's favicon.ico is not the tenant icon")
	}
}

// Every tenant-facing page links the icons with the version of what they
// show.
func TestPagesLinkTheIcons(t *testing.T) {
	b := Default()
	p := pagesFor(b)
	v := b.IconVersion()
	render := map[string]func(*httptest.ResponseRecorder, *http.Request){
		"landing": func(w *httptest.ResponseRecorder, r *http.Request) { p.Landing(w, r, "/.portico/saml-start") },
		"sites":   func(w *httptest.ResponseRecorder, r *http.Request) { p.Sites(w, r, nil, "a@b.c", 0) },
		"error":   func(w *httptest.ResponseRecorder, r *http.Request) { p.Error(w, r, 503, "Down", "down", "") },
	}
	for name, fn := range render {
		rec := httptest.NewRecorder()
		fn(rec, httptest.NewRequest("GET", "https://prism.test/", nil))
		body := rec.Body.String()
		for _, want := range []string{
			`<link rel="icon" href="/.portico/favicon.ico?v=` + v + `" sizes="16x16 32x32 48x48">`,
			`<link rel="apple-touch-icon" href="/.portico/apple-touch-icon.png?v=` + v + `">`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s page lacks %s", name, want)
			}
		}
	}
}
