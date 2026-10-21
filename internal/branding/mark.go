package branding

// The two paths of Portico's mark in its 600 by 680 viewBox: the outer
// arch, and the second arch in perspective inside it. icon.go draws the
// tab icons from the same paths.
const (
	markOuterArch = "M 50,620 L 50,300 A 250,250 0 0,1 550,300 L 550,620 L 438,620 L 438,293 A 138,138 0 0,0 162,293 L 162,620 Z"
	markInnerArch = "M 217,208 A 154,154 0 0,1 365,361 L 365,488 L 181,596 L 181,522 L 293,488 L 293,361 A 81,81 0 0,0 182,285 A 123,123 0 0,1 217,208 Z"
)

var markPaths = []string{markOuterArch, markInnerArch}

// PorticoMark is Portico's own mark (an arch with a second arch in
// perspective), in the colour asked for. Portico's pages use it in black
// (internal/web/static); the Keycloak login page gets it in white, on the
// MSP's accent colour, for the "Powered by Portico" line.
func PorticoMark(fill string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 600 680" width="600" height="680" role="img" aria-label="Portico">
  <g fill="` + fill + `">
    <path d="` + markOuterArch + `"/>
    <path d="` + markInnerArch + `"/>
  </g>
</svg>
`
}
