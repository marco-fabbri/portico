package branding

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Limits of the tab icon: Portico scales it down to 16, 32 and 48 px for
// favicon.ico and to 180 px for the apple-touch-icon, so it is at least
// that large; the upper bound keeps the decoded image small.
const (
	MinIconSide = 180
	MaxIconSide = 1024
	touchSide   = 180
	// iconRev changes when the drawing of the icons changes, so the
	// version in the pages' links changes with it.
	iconRev = "1"
)

var icoSides = []int{16, 32, 48}

// adminIconColor is the admin UI's text colour (--fg in portico.css).
var adminIconColor = color.NRGBA{0x1d, 0x1d, 0x1b, 0xff}

// Icons are the tab icons of a set of pages: favicon.ico with 16, 32 and
// 48 px PNGs inside, which every browser reads (Safari included), and a
// 180 px apple-touch-icon, which Safari uses for favourites and pinned
// tabs and iOS for the home screen.
type Icons struct {
	ICO   []byte
	Touch []byte
}

// HasIcon reports whether a tab icon is uploaded.
func (b Branding) HasIcon() bool { return len(b.IconData) > 0 }

// IconVersion names what the tenant icons show (the uploaded icon, or the
// arch on the accent colour): the pages put it in the icons' URLs, so a
// browser that keeps its own favicon cache (Safari) fetches a new icon.
func (b Branding) IconVersion() string {
	h := sha256.New()
	h.Write([]byte(iconRev + "|" + b.AccentColor + "|"))
	h.Write(b.IconData)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// validateIcon checks an uploaded tab icon: a square PNG within the
// limits. The size is read from the header before the image is decoded.
func validateIcon(data []byte) error {
	if len(data) > MaxLogoBytes {
		return fmt.Errorf("icon must be at most %d bytes", MaxLogoBytes)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return errors.New("icon must be a PNG")
	}
	if cfg.Width != cfg.Height {
		return fmt.Errorf("icon must be square (it is %dx%d px)", cfg.Width, cfg.Height)
	}
	if cfg.Width < MinIconSide || cfg.Width > MaxIconSide {
		return fmt.Errorf("icon must be %d to %d px a side (it is %d)", MinIconSide, MaxIconSide, cfg.Width)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return errors.New("icon must be a PNG (it could not be decoded)")
	}
	return nil
}

// TenantIcons returns the icons of the tenant-facing pages and of the
// Keycloak login page: the uploaded icon scaled down, or Portico's arch
// in white on a rounded square of the accent colour.
func TenantIcons(b Branding) (Icons, error) {
	if b.HasIcon() {
		src, err := png.Decode(bytes.NewReader(b.IconData))
		if err != nil {
			return Icons{}, fmt.Errorf("icon: %w", err)
		}
		n := toNRGBA(src)
		var imgs []*image.NRGBA
		for _, s := range icoSides {
			imgs = append(imgs, scale(n, s))
		}
		return encodeIcons(imgs, onWhite(scale(n, touchSide)))
	}
	return archIcons(parseColor(b.AccentColor, parseColor(Default().AccentColor, adminIconColor))), nil
}

// AdminIcons returns the icons of the admin UI: Portico's arch in white
// on a rounded square of the admin UI's dark grey, visible on light and
// dark tab bars alike.
func AdminIcons() Icons { return archIcons(adminIconColor) }

func archIcons(bg color.NRGBA) Icons {
	var imgs []*image.NRGBA
	for _, s := range icoSides {
		imgs = append(imgs, drawArch(s, bg, true))
	}
	ic, err := encodeIcons(imgs, drawArch(touchSide, bg, false))
	if err != nil {
		panic("branding: encoding the arch icons: " + err.Error()) // in-memory PNGs of a drawn image
	}
	return ic
}

func encodeIcons(ico []*image.NRGBA, touch *image.NRGBA) (Icons, error) {
	var out Icons
	var pngs [][]byte
	for _, img := range append(ico, touch) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return Icons{}, err
		}
		pngs = append(pngs, buf.Bytes())
	}
	out.ICO = encodeICO(ico, pngs[:len(ico)])
	out.Touch = pngs[len(ico)]
	return out, nil
}

// encodeICO writes an ICO file whose entries are PNGs (the format of
// Windows Vista and later, read by every current browser).
func encodeICO(imgs []*image.NRGBA, pngs [][]byte) []byte {
	var buf bytes.Buffer
	le := func(v any) { binary.Write(&buf, binary.LittleEndian, v) }
	le(uint16(0)) // reserved
	le(uint16(1)) // type: icon
	le(uint16(len(imgs)))
	offset := 6 + 16*len(imgs)
	for i, img := range imgs {
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		buf.WriteByte(byte(w % 256)) // 0 means 256
		buf.WriteByte(byte(h % 256))
		buf.WriteByte(0) // no palette
		buf.WriteByte(0) // reserved
		le(uint16(1))    // colour planes
		le(uint16(32))   // bits per pixel
		le(uint32(len(pngs[i])))
		le(uint32(offset))
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return buf.Bytes()
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	n := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(n, n.Bounds(), src, b.Min, draw.Src)
	return n
}

// scale resizes a square image to n by n px by averaging the source
// pixels each target pixel covers (weighted by the covered area, alpha
// premultiplied), which is what a downscale needs.
func scale(src *image.NRGBA, n int) *image.NRGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	fx, fy := float64(sw)/float64(n), float64(sh)/float64(n)
	for dy := 0; dy < n; dy++ {
		y0, y1 := float64(dy)*fy, float64(dy+1)*fy
		for dx := 0; dx < n; dx++ {
			x0, x1 := float64(dx)*fx, float64(dx+1)*fx
			var r, g, b, a, wsum float64
			for sy := int(y0); sy < sh && float64(sy) < y1; sy++ {
				wy := math.Min(y1, float64(sy+1)) - math.Max(y0, float64(sy))
				for sx := int(x0); sx < sw && float64(sx) < x1; sx++ {
					w := wy * (math.Min(x1, float64(sx+1)) - math.Max(x0, float64(sx)))
					p := src.Pix[sy*src.Stride+sx*4:]
					pa := float64(p[3]) / 255
					r += w * float64(p[0]) * pa
					g += w * float64(p[1]) * pa
					b += w * float64(p[2]) * pa
					a += w * pa
					wsum += w
				}
			}
			if wsum == 0 || a == 0 {
				continue
			}
			o := dst.Pix[dy*dst.Stride+dx*4:]
			o[0], o[1], o[2] = clamp8(r/a), clamp8(g/a), clamp8(b/a)
			o[3] = clamp8(a / wsum * 255)
		}
	}
	return dst
}

// onWhite puts img on an opaque white background: iOS fills the
// transparent parts of a touch icon with black.
func onWhite(img *image.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	for i := 0; i < len(img.Pix); i += 4 {
		a := float64(img.Pix[i+3]) / 255
		for c := 0; c < 3; c++ {
			out.Pix[i+c] = clamp8(float64(img.Pix[i+c])*a + 255*(1-a))
		}
		out.Pix[i+3] = 255
	}
	return out
}

func clamp8(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(math.Round(v))
}

// parseColor reads #rrggbb, or returns def.
func parseColor(s string, def color.NRGBA) color.NRGBA {
	if !reColor.MatchString(s) {
		return def
	}
	v, _ := strconv.ParseUint(s[1:], 16, 32)
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}
}

// The drawing of the arch: the mark's two paths (mark.go), flattened to
// polygons and filled with 4 by 4 samples a pixel.

type pt struct{ x, y float64 }

const (
	samples = 4
	// The mark's drawn extent in its 600 by 680 viewBox, and how much of
	// the icon's height it takes.
	markMinX, markMinY, markMaxX, markMaxY = 50.0, 50.0, 550.0, 620.0
	markShare                              = 0.64
	cornerShare                            = 0.2
)

// drawArch returns an n by n icon: Portico's arch in white on bg, on a
// rounded square (favicon) or the full square (touch icon, which Apple
// rounds itself).
func drawArch(n int, bg color.NRGBA, rounded bool) *image.NRGBA {
	size := float64(n)
	square := []pt{{0, 0}, {size, 0}, {size, size}, {0, size}}
	if rounded {
		square = roundedSquare(size, cornerShare*size)
	}
	k := markShare * size / (markMaxY - markMinY)
	tx := size/2 - k*(markMinX+markMaxX)/2
	ty := size/2 - k*(markMinY+markMaxY)/2
	var mark [][]pt
	for _, d := range markPaths {
		poly := flattenPath(d)
		for i := range poly {
			poly[i] = pt{tx + k*poly[i].x, ty + k*poly[i].y}
		}
		mark = append(mark, poly)
	}
	bgCov := coverage(n, [][]pt{square})
	markCov := coverage(n, mark)
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	full := float64(samples * samples)
	for i := 0; i < n*n; i++ {
		ab := float64(bgCov[i]) / full
		if ab == 0 {
			continue
		}
		am := math.Min(float64(markCov[i])/full, ab) / ab
		p := img.Pix[i*4:]
		p[0] = clamp8(float64(bg.R)*(1-am) + 255*am)
		p[1] = clamp8(float64(bg.G)*(1-am) + 255*am)
		p[2] = clamp8(float64(bg.B)*(1-am) + 255*am)
		p[3] = clamp8(ab * 255)
	}
	return img
}

func roundedSquare(size, r float64) []pt {
	var out []pt
	corners := []struct {
		c    pt
		from float64
	}{{pt{size - r, r}, -math.Pi / 2}, {pt{size - r, size - r}, 0}, {pt{r, size - r}, math.Pi / 2}, {pt{r, r}, math.Pi}}
	for _, c := range corners {
		for i := 0; i <= 16; i++ {
			th := c.from + math.Pi/2*float64(i)/16
			out = append(out, pt{c.c.x + r*math.Cos(th), c.c.y + r*math.Sin(th)})
		}
	}
	return out
}

// coverage counts, for each pixel of an n by n image, how many of its
// samples fall inside any of the polygons (each filled with the non-zero
// rule, as SVG does by default).
func coverage(n int, polys [][]pt) []int {
	cov := make([]int, n*n)
	row := make([]bool, n*samples)
	type crossing struct {
		x   float64
		dir int
	}
	for sy := 0; sy < n*samples; sy++ {
		y := (float64(sy) + 0.5) / samples
		for i := range row {
			row[i] = false
		}
		for _, poly := range polys {
			var xs []crossing
			for i := range poly {
				a, b := poly[i], poly[(i+1)%len(poly)]
				if a.y == b.y || y < math.Min(a.y, b.y) || y >= math.Max(a.y, b.y) {
					continue
				}
				dir := 1
				if b.y < a.y {
					dir = -1
				}
				xs = append(xs, crossing{a.x + (y-a.y)*(b.x-a.x)/(b.y-a.y), dir})
			}
			sort.Slice(xs, func(i, j int) bool { return xs[i].x < xs[j].x })
			wind := 0
			for i, c := range xs {
				wind += c.dir
				if wind == 0 || i+1 == len(xs) {
					continue
				}
				// Samples whose centre lies between this crossing and the next.
				from := int(math.Ceil(c.x*samples - 0.5))
				to := int(math.Ceil(xs[i+1].x*samples - 0.5))
				for sx := max(from, 0); sx < min(to, len(row)); sx++ {
					row[sx] = true
				}
			}
		}
		py := sy / samples
		for sx, in := range row {
			if in {
				cov[py*n+sx/samples]++
			}
		}
	}
	return cov
}

// flattenPath turns an SVG path of absolute M, L, A (equal radii, no
// rotation) and Z commands, the only ones the mark uses, into a polygon.
func flattenPath(d string) []pt {
	f := strings.Fields(strings.ReplaceAll(d, ",", " "))
	num := func(i int) float64 {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			panic("branding: mark path: " + err.Error())
		}
		return v
	}
	var out []pt
	for i := 0; i < len(f); {
		switch f[i] {
		case "M", "L":
			out = append(out, pt{num(i + 1), num(i + 2)})
			i += 3
		case "A":
			r := num(i + 1)
			large, sweep := f[i+4] == "1", f[i+5] == "1"
			to := pt{num(i + 6), num(i + 7)}
			out = append(out, arc(out[len(out)-1], to, r, large, sweep)...)
			i += 8
		case "Z":
			i++
		default:
			panic("branding: mark path: unsupported command " + f[i])
		}
	}
	return out
}

// arc returns the points of an SVG arc from p0 (excluded) to p1, radius
// r, after the SVG specification's endpoint-to-centre conversion
// (implementation notes F.6.5, with equal radii and no rotation).
func arc(p0, p1 pt, r float64, large, sweep bool) []pt {
	x1, y1 := (p0.x-p1.x)/2, (p0.y-p1.y)/2
	if l := (x1*x1 + y1*y1) / (r * r); l > 1 {
		r *= math.Sqrt(l)
	}
	f := math.Sqrt(math.Max(0, (r*r*r*r-r*r*y1*y1-r*r*x1*x1)/(r*r*y1*y1+r*r*x1*x1)))
	if large == sweep {
		f = -f
	}
	cx, cy := f*y1, -f*x1
	th1 := math.Atan2((y1-cy)/r, (x1-cx)/r)
	dth := math.Atan2((-y1-cy)/r, (-x1-cx)/r) - th1
	if sweep && dth < 0 {
		dth += 2 * math.Pi
	} else if !sweep && dth > 0 {
		dth -= 2 * math.Pi
	}
	cx, cy = cx+(p0.x+p1.x)/2, cy+(p0.y+p1.y)/2
	steps := int(math.Ceil(math.Abs(dth) / (math.Pi / 64)))
	out := make([]pt, 0, steps)
	for i := 1; i <= steps; i++ {
		th := th1 + dth*float64(i)/float64(steps)
		out = append(out, pt{cx + r*math.Cos(th), cy + r*math.Sin(th)})
	}
	out[len(out)-1] = p1
	return out
}
