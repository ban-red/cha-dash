package icons

import (
	"strings"
	"testing"
)

const svgNS = `xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"`

// Ways around a string filter, each refused by the parser and allowlists.
func TestCheckSVGRefusesBypasses(t *testing.T) {
	for name, bad := range map[string]string{
		"entity-encoded javascript href":   `<svg ` + svgNS + `><a href="&#106;avascript:alert(1)"><rect/></a></svg>`,
		"entity-encoded javascript on use": `<svg ` + svgNS + `><use href="&#x6A;avascript:alert(1)"/></svg>`,
		"javascript with a tab":            `<svg ` + svgNS + `><use xlink:href="java&#9;script:alert(1)"/></svg>`,
		"animate rewriting href":           `<svg ` + svgNS + `><a><animate attributeName="href" to="javascript:alert(1)"/><rect/></a></svg>`,
		"animate rewriting href, encoded":  `<svg ` + svgNS + `><a><animate attributeName="href" values="&#106;avascript:alert(1)"/><rect/></a></svg>`,
		"set":                              `<svg ` + svgNS + `><a><set attributeName="href" to="javascript:alert(1)"/><rect/></a></svg>`,
		"animateTransform":                 `<svg ` + svgNS + `><rect><animateTransform attributeName="transform" type="rotate" to="90"/></rect></svg>`,
		"animateMotion":                    `<svg ` + svgNS + `><rect><animateMotion path="M0 0h1"/></rect></svg>`,
		"external xlink:href":              `<svg ` + svgNS + `><use xlink:href="https://evil.example/s.svg#a"/></svg>`,
		"relative xlink:href":              `<svg ` + svgNS + `><use xlink:href="sprite.svg#a"/></svg>`,
		"undeclared xlink prefix":          `<svg xmlns="http://www.w3.org/2000/svg"><use xlink:href="//evil.example/s.svg#a"/></svg>`,
		"image from the network":           `<svg ` + svgNS + `><image href="https://tracker.example/p.png"/></svg>`,
		"image of an svg":                  `<svg ` + svgNS + `><image href="data:image/svg+xml;base64,PHN2Zy8+"/></svg>`,
		"feImage":                          `<svg ` + svgNS + `><filter id="f"><feImage href="#a"/></filter></svg>`,
		"style attribute url":              `<svg ` + svgNS + `><rect style="fill:url(http://x.example/p)"/></svg>`,
		"style attribute url, quoted":      `<svg ` + svgNS + `><rect style="fill:url( 'https://x.example/p' )"/></svg>`,
		"presentation attribute url":       `<svg ` + svgNS + `><rect fill="url(//x.example/p#g)"/></svg>`,
		"style element url":                `<svg ` + svgNS + `><style>rect{fill:url(https://x.example/p)}</style><rect/></svg>`,
		"style element url in CDATA":       `<svg ` + svgNS + `><style><![CDATA[rect{fill:URL(https://x.example/p)}]]></style><rect/></svg>`,
		"style element escaped url":        `<svg ` + svgNS + `><style>rect{fill:\75rl(https://x.example/p)}</style><rect/></svg>`,
		"style element import":             `<svg ` + svgNS + `><style>@import "https://x.example/a.css";</style></svg>`,
		"style element image-set":          `<svg ` + svgNS + `><style>rect{fill:image-set("https://x.example/p.png" 1x)}</style></svg>`,
		"style javascript":                 `<svg ` + svgNS + `><style>rect{fill:url(javascript:alert(1))}</style></svg>`,
		"onload":                           `<svg ` + svgNS + ` onload="alert(1)"><rect/></svg>`,
		"onload, upper case":               `<svg ` + svgNS + `><rect ONCLICK="alert(1)"/></svg>`,
		"namespaced event handler":         `<svg ` + svgNS + ` xmlns:ev="http://www.w3.org/2001/xml-events"><rect ev:onclick="x()"/></svg>`,
		"script":                           `<svg ` + svgNS + `><script>alert(1)</script></svg>`,
		"script, upper case":               `<svg ` + svgNS + `><SCRIPT>alert(1)</SCRIPT></svg>`,
		"xhtml script":                     `<svg ` + svgNS + `><script xmlns="http://www.w3.org/1999/xhtml">alert(1)</script></svg>`,
		"xhtml script in metadata":         `<svg ` + svgNS + `><metadata><h:script xmlns:h="http://www.w3.org/1999/xhtml">alert(1)</h:script></metadata></svg>`,
		"foreignObject":                    `<svg ` + svgNS + `><foreignObject><div xmlns="http://www.w3.org/1999/xhtml"/></foreignObject></svg>`,
		"iframe":                           `<svg ` + svgNS + `><iframe src="https://x.example"/></svg>`,
		"a link":                           `<svg ` + svgNS + `><a href="#x"><rect/></a></svg>`,
		"DOCTYPE with entity":              `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x "javascript:alert(1)">]><svg ` + svgNS + `><use href="&x;"/></svg>`,
		"DOCTYPE alone":                    `<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd"><svg ` + svgNS + `/>`,
		"undeclared entity":                `<svg ` + svgNS + `><text>&nbsp;</text></svg>`,
		"xml-stylesheet":                   `<?xml version="1.0"?><?xml-stylesheet href="https://x.example/a.css"?><svg ` + svgNS + `/>`,
		"xml:base":                         `<svg ` + svgNS + ` xml:base="https://x.example/"><use href="#a"/></svg>`,
		"unknown attribute":                `<svg ` + svgNS + `><rect formaction="x"/></svg>`,
		"two roots":                        `<svg ` + svgNS + `/><svg ` + svgNS + `/>`,
		"not well-formed":                  `<svg ` + svgNS + `><rect></svg>`,
		"html root":                        `<html><svg/></html>`,
		"svg in another namespace":         `<svg xmlns="http://www.w3.org/1999/xhtml"/>`,
	} {
		if _, err := Check([]byte(bad)); err == nil {
			t.Errorf("%s: accepted %s", name, bad)
		}
	}
}

// Icons as editors and icon sets write them pass.
func TestCheckSVGAllowsRealIcons(t *testing.T) {
	for name, good := range map[string]string{
		"gradient and use by id": `<?xml version="1.0" encoding="UTF-8"?>
<svg ` + svgNS + ` width="64" height="64" viewBox="0 0 64 64">
  <title>Plex</title>
  <defs>
    <linearGradient id="g" x1="0" y1="0" x2="1" y2="1" gradientUnits="objectBoundingBox">
      <stop offset="0" stop-color="#e5a00d"/>
      <stop offset="1" stop-color="#cc7b19" stop-opacity=".9"/>
    </linearGradient>
    <radialGradient id="r" cx="50%" cy="50%" r="50%"><stop offset="0%" style="stop-color:#fff"/></radialGradient>
    <path id="chev" d="M22 12h12l12 20-12 20H22l12-20z"/>
    <clipPath id="c"><rect width="64" height="64" rx="12"/></clipPath>
  </defs>
  <g clip-path="url(#c)">
    <rect width="64" height="64" fill="url(#g)"/>
    <use xlink:href="#chev" fill="#fff"/>
    <use href="#chev" transform="translate(2 2)" opacity="0.3"/>
  </g>
</svg>`,
		"style element with url(#)": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><defs><style>.a{fill:url(#b);}.c{fill:none;stroke:#333;stroke-width:2px}</style><linearGradient id="b"><stop offset="0" stop-color="#09f"/></linearGradient></defs><circle class="a" cx="12" cy="12" r="10"/><path class="c" d="M7 12l3 3 7-7"/></svg>`,
		"style element in CDATA":    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><style type="text/css"><![CDATA[ rect { fill: url( "#p" ) } ]]></style><pattern id="p" width="2" height="2" patternUnits="userSpaceOnUse"><rect width="1" height="1"/></pattern><rect width="8" height="8"/></svg>`,
		"filter and mask":           `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><filter id="s" x="-20%" y="-20%" width="140%" height="140%" color-interpolation-filters="sRGB"><feGaussianBlur in="SourceAlpha" stdDeviation="1"/><feOffset dy="1" result="o"/><feFlood flood-color="#000" flood-opacity=".3"/><feComposite in2="o" operator="in"/><feMerge><feMergeNode/><feMergeNode in="SourceGraphic"/></feMerge></filter><mask id="m"><rect width="32" height="32" fill="#fff"/></mask><g filter="url(#s)" mask="url(#m)"><polygon points="16,2 30,30 2,30" fill-rule="evenodd"/></g></svg>`,
		"inkscape file": `<?xml version="1.0" encoding="UTF-8" standalone="no"?>
<!-- Created with Inkscape (http://www.inkscape.org/) -->
<svg xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:cc="http://creativecommons.org/ns#" xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:svg="http://www.w3.org/2000/svg" xmlns="http://www.w3.org/2000/svg" xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" width="48" height="48" viewBox="0 0 48 48" version="1.1" id="svg8" inkscape:version="1.2" sodipodi:docname="logo.svg">
  <sodipodi:namedview id="base" pagecolor="#ffffff" inkscape:zoom="4"/>
  <metadata id="metadata5"><rdf:RDF><cc:Work rdf:about=""><dc:format>image/svg+xml</dc:format><dc:title>Logo</dc:title></cc:Work></rdf:RDF></metadata>
  <g inkscape:label="Layer 1" inkscape:groupmode="layer" id="layer1">
    <ellipse style="fill:#4caf50;fill-opacity:1;stroke:none" id="e1" cx="24" cy="24" rx="20" ry="18"/>
    <text x="24" y="30" font-family="sans-serif" font-size="14" text-anchor="middle" xml:space="preserve"><tspan>Hi &amp; bye</tspan></text>
  </g>
</svg>`,
		"symbol sprite with aria": `<svg xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Home" data-icon="home"><symbol id="i" viewBox="0 0 24 24"><path d="M3 12l9-9 9 9"/><line x1="5" y1="12" x2="5" y2="21" stroke="currentColor" stroke-linecap="round"/><polyline points="1,2 3,4"/></symbol><use href="#i" width="24" height="24"/></svg>`,
		"embedded raster":         `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 1 1"><image width="1" height="1" xlink:href="data:image/png;base64,iVBORw0KGgo="/></svg>`,
		"no xmlns":                `<svg viewBox="0 0 1 1"><path d="M0 0h1v1z"/></svg>`,
	} {
		if ct, err := Check([]byte(good)); err != nil || ct != TypeSVG {
			t.Errorf("%s: %q, %v", name, ct, err)
		}
	}
}

// encoding/xml decodes character references in attribute values, which the
// javascript: check relies on.
func TestXMLDecodesAttributeEntities(t *testing.T) {
	_, err := Check([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><use href="&#106;&#x61;vascript:x"/></svg>`))
	if err == nil || !strings.Contains(err.Error(), "javascript") {
		t.Fatalf("err = %v, want the javascript: refusal", err)
	}
}
