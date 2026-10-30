package importer

import (
	"encoding/json"
	"fmt"
)

// LayoutVersion is packages/layout's LAYOUT_VERSION: the document format the
// hub stores next to each layout revision (layouts.format_version).
const LayoutVersion = 1

// LayoutColumns is the canonical board width (CANONICAL_COLUMNS).
const LayoutColumns = 12

// LayoutDocument mirrors packages/layout's LayoutDocument (version 1). Field
// order matches serialize.ts, so a document built here encodes to the same
// JSON the web app would write for the same board.
type LayoutDocument struct {
	Version int `json:"version"`
	Columns int `json:"columns"`
	// Flow is how sections share the board ("bento", "mosaic"); empty is
	// the default ("sections"). Kept as it is when an import adds to a board.
	Flow     string          `json:"flow,omitempty"`
	Sections []LayoutSection `json:"sections"`
}

// LayoutSection is a SectionDocument. A section without a title is untitled
// (Homepage's header: false).
type LayoutSection struct {
	ID    string       `json:"id"`
	Title *string      `json:"title,omitempty"`
	Span  int          `json:"span"`
	Items []LayoutItem `json:"items"`
	// Overrides are a board's per-breakpoint section overrides, kept as they
	// are when an import adds to a board that has them.
	Overrides json.RawMessage `json:"overrides,omitempty"`
}

// LayoutItem is an ItemDocument. X and Y are informational: the engine
// recomputes them from item order on load. They're filled in by Pack anyway,
// so the stored document equals what the engine would serialize.
type LayoutItem struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Size           string   `json:"size"`
	SupportedSizes []string `json:"supportedSizes"`
	X              int      `json:"x"`
	Y              int      `json:"y"`
}

type sizeSpec struct{ w, h int }

// sizeIDs is SIZE_IDS, in canonical order (the tie-breaker in closestFit).
var sizeIDs = []string{"glyph", "strip", "strip-long", "small", "wide", "panel", "tall", "column", "large", "hero"}

var sizes = map[string]sizeSpec{
	"glyph":      {1, 1},
	"strip":      {4, 1},
	"strip-long": {8, 1},
	"small":      {2, 2},
	"wide":       {4, 2},
	"panel":      {4, 3},
	"tall":       {2, 4},
	"column":     {3, 4},
	"large":      {4, 4},
	"hero":       {8, 4},
}

// SizeWidth returns a size's width in grid columns, or 0 for an unknown size.
func SizeWidth(size string) int { return sizes[size].w }

// ClosestFit ports closestFit from packages/layout/src/sizes.ts: the candidate
// that fits in columns and is closest in shape to preferred (same height
// first, then closest width, then canonical size order). It returns "" when
// nothing fits.
func ClosestFit(candidates []string, preferred string, columns int) string {
	p, ok := sizes[preferred]
	if !ok {
		return ""
	}
	best, bestKey := "", [3]int{}
	for _, id := range candidates {
		s, ok := sizes[id]
		if !ok || s.w > columns {
			continue
		}
		key := [3]int{abs(s.h - p.h), abs(s.w - p.w), sizeIndex(id)}
		if best == "" || less(key, bestKey) {
			best, bestKey = id, key
		}
	}
	return best
}

func sizeIndex(id string) int {
	for i, s := range sizeIDs {
		if s == id {
			return i
		}
	}
	return len(sizeIDs)
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Pack fills in every item's x/y with the engine's dense Z-reflow: each item,
// in order, takes the first free cell in reading order where it fits
// (pack.ts, the same as CSS grid-auto-flow: row dense).
func (d *LayoutDocument) Pack() error {
	for s := range d.Sections {
		sec := &d.Sections[s]
		if sec.Span < 1 || sec.Span > LayoutColumns {
			return fmt.Errorf("importer: section %q: span %d is outside 1..%d", sec.ID, sec.Span, LayoutColumns)
		}
		var rows []uint32
		for i := range sec.Items {
			it := &sec.Items[i]
			sz, ok := sizes[it.Size]
			if !ok || sz.w > sec.Span {
				return fmt.Errorf("importer: item %q: size %q does not fit a %d-column section", it.ID, it.Size, sec.Span)
			}
			it.X, it.Y = firstFit(&rows, sec.Span, sz.w, sz.h)
		}
	}
	return nil
}

func firstFit(rows *[]uint32, columns, w, h int) (int, int) {
	full := uint32(1)<<columns - 1
	base := uint32(1)<<w - 1
	at := func(r int) uint32 {
		if r < len(*rows) {
			return (*rows)[r]
		}
		return 0
	}
	for y := 0; ; y++ {
		if at(y) == full {
			continue
		}
		for x := 0; x <= columns-w; x++ {
			mask := base << x
			free := true
			for r := y; r < y+h; r++ {
				if at(r)&mask != 0 {
					free = false
					break
				}
			}
			if free {
				for len(*rows) < y+h {
					*rows = append(*rows, 0)
				}
				for r := y; r < y+h; r++ {
					(*rows)[r] |= mask
				}
				return x, y
			}
		}
	}
}
