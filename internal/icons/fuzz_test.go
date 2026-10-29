package icons

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
)

// FuzzCheck: Check never panics, and an SVG it accepts passes an
// independent reading: no element that runs or embeds content, no event
// handler, no javascript: URL (with the whitespace and control characters a
// browser ignores removed), no DOCTYPE. Icon bytes come from the network.
//
//	go test -run '^$' -fuzz '^FuzzCheck$' -fuzztime 60s ./internal/icons/
func FuzzCheck(f *testing.F) {
	f.Add(safeSVG)
	for _, s := range []string{
		`<svg><script>alert(1)</script></svg>`,
		`<svg onload="alert(1)"><rect/></svg>`,
		`<svg><foreignObject><div/></foreignObject></svg>`,
		`<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x "y">]><svg>&x;</svg>`,
		`<svg><a href="java&#x09;script:alert(1)"><rect/></a></svg>`,
		`<svg><style>@import url(https://x.example/a.css);</style></svg>`,
		`<!-- logo --><svg><image href="data:image/png;base64,iVBORw0KGgo="/></svg>`,
		"\x89PNG\r\n\x1a\n",
		"RIFF\x00\x00\x00\x00WEBP",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		ct, err := Check(b)
		if err != nil || ct != TypeSVG {
			return
		}
		if problem := unsafeSVG(b); problem != "" {
			t.Fatalf("Check accepted an SVG with %s: %q", problem, b)
		}
	})
}

// unsafeSVG reads b the way the fuzz oracle sees it and names the first
// thing that must never be served, or "".
func unsafeSVG(b []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))))
	dec.Strict = true
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return ""
		}
		if err != nil {
			return "" // Check reads with the same decoder: it refused, or this is a stricter reading
		}
		switch tok := tok.(type) {
		case xml.Directive:
			return "a directive"
		case xml.StartElement:
			switch strings.ToLower(tok.Name.Local) {
			case "script", "foreignobject", "iframe", "embed", "object", "handler", "listener":
				return "a <" + tok.Name.Local + "> element"
			}
			for _, a := range tok.Attr {
				if strings.HasPrefix(strings.ToLower(a.Name.Local), "on") {
					return "an event handler (" + a.Name.Local + ")"
				}
				v := strings.Map(func(r rune) rune {
					if r <= ' ' || r == 0x7f {
						return -1
					}
					return r
				}, strings.ToLower(a.Value))
				if strings.Contains(v, "javascript:") || strings.Contains(v, "vbscript:") {
					return "a script URL in " + a.Name.Local
				}
			}
		}
	}
}
