package branding

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Portico serves logos under a CSP that forbids scripts, but Keycloak
// serves the theme's copy from the login origin with its own headers, so
// an SVG is parsed and accepted only when it is static drawing: elements
// of an allow-list, no event handlers, links only to the document's own
// ids (or an embedded raster image), no DTD, no stylesheet loaded from
// elsewhere. A substring search is not enough: XML character references
// and entities spell "javascript:" without the letters.

const (
	nsSVG   = "http://www.w3.org/2000/svg"
	nsXLink = "http://www.w3.org/1999/xlink"
	nsXML   = "http://www.w3.org/XML/1998/namespace"
)

// svgElements are the SVG elements a logo may use: shapes, text,
// structure, paint servers, clipping, masking and filters. Not script,
// foreignObject, the animation elements (set and animate can rewrite an
// href) or anything else.
var svgElements = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`svg g defs symbol use title desc metadata switch
		path rect circle ellipse line polyline polygon
		text tspan textPath
		linearGradient radialGradient stop pattern
		clipPath mask marker image style
		filter feBlend feColorMatrix feComponentTransfer feComposite
		feConvolveMatrix feDiffuseLighting feDisplacementMap feDistantLight
		feDropShadow feFlood feFuncA feFuncB feFuncG feFuncR feGaussianBlur
		feImage feMerge feMergeNode feMorphology feOffset fePointLight
		feSpecularLighting feSpotLight feTile feTurbulence`) {
		svgElements[e] = true
	}
}

// inertNamespaces are the editors' private namespaces: browsers do not
// render or run their elements. Adobe's are under ns.adobe.com.
var inertNamespaces = map[string]bool{
	"http://www.inkscape.org/namespaces/inkscape":            true,
	"http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd":     true,
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#":            true,
	"http://purl.org/dc/elements/1.1/":                       true,
	"http://creativecommons.org/ns#":                         true,
	"http://web.resource.org/cc/":                            true,
	"http://www.bohemiancoding.com/sketch/ns":                true,
	"http://www.serif.com/":                                  true,
	"http://www.figma.com/figma/ns":                          true,
	"http://www.w3.org/2000/xmlns/":                          true,
	"https://boxy-svg.com":                                   true,
	"http://ns.adobe.com/AdobeSVGViewerExtensions/3.0/":      true,
	"http://ns.adobe.com/AdobeIllustrator/10.0/":             true,
	"http://ns.adobe.com/Graphs/1.0/":                        true,
	"http://ns.adobe.com/Variables/1.0/":                     true,
	"http://ns.adobe.com/SaveForWeb/1.0/":                    true,
	"http://ns.adobe.com/Extensibility/1.0/":                 true,
	"http://ns.adobe.com/Flows/1.0/":                         true,
	"http://ns.adobe.com/ImageReplacement/1.0/":              true,
	"http://ns.adobe.com/GenericCustomNamespace/1.0/":        true,
	"http://ns.adobe.com/XPath/1.0/":                         true,
	"http://schemas.microsoft.com/visio/2003/SVGExtensions/": true,
}

var (
	// reCSSURL finds a url(...) in a style; only url(#id) is accepted.
	reCSSURL = regexp.MustCompile(`(?i)url\(\s*['"]?\s*([^'")\s]*)`)
	// reDataImage is the one link target besides #id: a raster image
	// embedded in the file, on <image>.
	reDataImage = regexp.MustCompile(`(?i)^data:image/(png|jpeg|gif|webp);`)
)

// checkSVG returns why an SVG is not accepted as a logo, nil when it is
// static drawing.
func checkSVG(data []byte) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true // undeclared entities are errors
	root := true
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			if root {
				return errors.New("it has no <svg> element")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("it is not well-formed XML (%v)", err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			return errors.New("it has a DOCTYPE or an entity declaration")
		case xml.ProcInst:
			if t.Target != "xml" {
				return fmt.Errorf("it has a <?%s?> instruction", t.Target)
			}
		case xml.CharData:
			if err := checkCSS(string(t)); err != nil {
				// Text outside <style> is drawn, not parsed as CSS, but
				// a check that is too strict here costs nothing a logo
				// needs.
				return err
			}
		case xml.StartElement:
			if root && (t.Name.Local != "svg" || (t.Name.Space != nsSVG && t.Name.Space != "")) {
				return fmt.Errorf("its root element is <%s>, not <svg>", t.Name.Local)
			}
			root = false
			if err := checkSVGElement(t); err != nil {
				return err
			}
			if !inertNamespaces[t.Name.Space] {
				continue
			}
			// An editor's element: skipped with everything inside it.
			if err := d.Skip(); err != nil {
				return fmt.Errorf("it is not well-formed XML (%v)", err)
			}
		}
	}
}

func checkSVGElement(t xml.StartElement) error {
	switch {
	case inertNamespaces[t.Name.Space]:
	case t.Name.Space != nsSVG && t.Name.Space != "":
		return fmt.Errorf("it has a <%s> element of namespace %s", t.Name.Local, t.Name.Space)
	case !svgElements[t.Name.Local]:
		return fmt.Errorf("it has a <%s> element", t.Name.Local)
	}
	for _, a := range t.Attr {
		if strings.HasPrefix(strings.ToLower(a.Name.Local), "on") {
			return fmt.Errorf("it has an event handler (%s)", a.Name.Local)
		}
		switch a.Name.Space {
		case "", nsSVG, nsXLink, nsXML, "xmlns":
		default:
			if !inertNamespaces[a.Name.Space] {
				return fmt.Errorf("it has an attribute of namespace %s", a.Name.Space)
			}
			continue
		}
		v := strings.TrimSpace(a.Value)
		if a.Name.Local == "href" && a.Name.Space != "xmlns" {
			if !strings.HasPrefix(v, "#") && !(t.Name.Local == "image" && reDataImage.MatchString(v)) {
				return errors.New("it links outside itself (href to something other than #id or an embedded image)")
			}
			continue
		}
		if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
			continue
		}
		if err := checkCSS(v); err != nil {
			return err
		}
	}
	return nil
}

// checkCSS refuses what loads or runs something from a style or an
// attribute value: @import, url() to anything but #id, javascript:,
// expression().
func checkCSS(s string) error {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "javascript:"):
		return errors.New("it has a javascript: link")
	case strings.Contains(l, "@import"):
		return errors.New("it imports a stylesheet")
	case strings.Contains(l, "expression("):
		return errors.New("it has a CSS expression")
	}
	for _, m := range reCSSURL.FindAllStringSubmatch(s, -1) {
		if !strings.HasPrefix(m[1], "#") {
			return errors.New("it loads something from outside itself (url() to something other than #id)")
		}
	}
	return nil
}
