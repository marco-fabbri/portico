package branding

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"image"
	_ "image/jpeg" // DecodeConfig of uploaded JPEG logos
	_ "image/png"  // DecodeConfig of uploaded PNG logos
	"math"
	"strconv"
	"strings"
)

// The logo box of the header (design branding §4.2): the logo fits
// inside it with its aspect ratio kept and is never enlarged.
const (
	LogoBoxW = 320
	LogoBoxH = 96
	// The arch's viewBox (mark.go).
	archW, archH = 600, 680
)

// Header is what the top of every tenant-facing page shows, in one
// centred column: the logo, then the text; the claim (Tagline) follows.
type Header struct {
	Logo         bool   // an uploaded logo or Portico's arch
	LogoW, LogoH int    // CSS pixels within LogoBoxW by LogoBoxH
	Text         string // HeaderText, or MSPName with neither a logo nor a text
}

// Header applies the header rules to b.
func (b Branding) Header() Header {
	var h Header
	switch {
	case b.HasLogo():
		w, hh := logoSize(b.LogoType, b.LogoData)
		h.Logo = true
		h.LogoW, h.LogoH = fitLogo(w, hh)
	case b.PorticoLogo:
		h.Logo = true
		h.LogoW, h.LogoH = fitLogo(archW, archH)
	}
	h.Text = strings.TrimSpace(b.HeaderText)
	if !h.Logo && h.Text == "" {
		h.Text = strings.TrimSpace(b.MSPName)
	}
	return h
}

// LogoFile returns the logo as served and written into the Keycloak
// theme: the upload, or Portico's arch drawn in the text colour.
func (b Branding) LogoFile() (data []byte, contentType string, ok bool) {
	switch {
	case b.HasLogo():
		return b.LogoData, b.LogoType, true
	case b.PorticoLogo:
		return []byte(PorticoMark(b.TextColor)), "image/svg+xml", true
	}
	return nil, "", false
}

// LogoFileExt is the file extension of LogoFile ("" without a logo).
func (b Branding) LogoFileExt() string {
	if _, ct, ok := b.LogoFile(); ok {
		return logoTypes[ct]
	}
	return ""
}

// Missing names the new fields a document or a request did not carry.
type Missing struct{ Background, Text, HeaderText bool }

// FillMissing gives the fields named in m the values a document from
// before the colours and the header text reads with (design branding
// goal 7): the background is the accent, the text white, and the header
// text the MSP name when there is a logo, so nobody loses the name.
func (b *Branding) FillMissing(m Missing) {
	if m.Background {
		b.BackgroundColor = b.AccentColor
	}
	if m.Text {
		b.TextColor = "#ffffff"
	}
	if m.HeaderText {
		b.HeaderText = ""
		if b.HasLogo() || b.PorticoLogo {
			b.HeaderText = strings.TrimSpace(b.MSPName)
		}
	}
}

// decodeStored reads a stored document, filling the fields it lacks.
func decodeStored(raw []byte) (Branding, error) {
	var b Branding
	if err := json.Unmarshal(raw, &b); err != nil {
		return Branding{}, err
	}
	var present struct {
		Background *string `json:"background_color"`
		Text       *string `json:"text_color"`
		HeaderText *string `json:"header_text"`
	}
	if err := json.Unmarshal(raw, &present); err != nil {
		return Branding{}, err
	}
	b.FillMissing(Missing{Background: present.Background == nil, Text: present.Text == nil, HeaderText: present.HeaderText == nil})
	return b, nil
}

// fitLogo scales w by h into the logo box, never enlarging it; an
// unknown size gets a square box.
func fitLogo(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return LogoBoxH, LogoBoxH
	}
	s := math.Min(1, math.Min(float64(LogoBoxW)/float64(w), float64(LogoBoxH)/float64(h)))
	return int(math.Round(float64(w) * s)), int(math.Round(float64(h) * s))
}

// logoSize reads a logo's intrinsic size; 0, 0 when it cannot.
func logoSize(ctype string, data []byte) (int, int) {
	if ctype == "image/svg+xml" {
		return svgSize(data)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// svgSize reads the root element's width and height (a number, or one
// in px), else its viewBox.
func svgSize(data []byte) (int, int) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	for {
		tok, err := d.Token()
		if err != nil {
			return 0, 0
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var w, h, vb string
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "width":
				w = a.Value
			case "height":
				h = a.Value
			case "viewBox":
				vb = a.Value
			}
		}
		if pw, ok1 := svgLength(w); ok1 {
			if ph, ok2 := svgLength(h); ok2 {
				return pw, ph
			}
		}
		if f := strings.FieldsFunc(vb, func(r rune) bool { return r == ' ' || r == ',' }); len(f) == 4 {
			vw, err1 := strconv.ParseFloat(f[2], 64)
			vh, err2 := strconv.ParseFloat(f[3], 64)
			if err1 == nil && err2 == nil && vw > 0 && vh > 0 {
				return int(math.Round(vw)), int(math.Round(vh))
			}
		}
		return 0, 0
	}
}

func svgLength(s string) (int, bool) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "px")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return int(math.Round(v)), true
}
