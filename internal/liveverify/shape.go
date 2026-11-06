// Package liveverify is layer 5 of the test plan (docs/plan/17-test-automation.md):
// it runs the integrations once against the maintainer's lab, read-only,
// and compares what the lab answered with the recorded fixtures the
// contract tests replay. A difference means the fixtures no longer match
// the upstream API: the fix is new fixtures and a layer 1–2 test that fails
// on the old ones.
//
// The comparison is by shape, never by value: which fields an answer has
// and what type each holds. What it prints comes from the fixtures, which
// are sanitized, so no live name or value reaches the summary. The run
// itself is live_test.go, behind the live build tag; see README.md.
package liveverify

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Shape is the set of field paths in a JSON document, each with the types
// seen there: "data[].vmid" → {"number"}. Array elements merge into "[]";
// an object whose keys are data (paths, labels, IDs) rather than field
// names merges into ".*".
type Shape map[string]map[string]bool

// fieldRE is what a field name looks like. Keys that don't (they hold a
// slash, a dot, a colon, or start with a digit) are data.
var fieldRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// mapThreshold: an object with more keys than this is a map.
const mapThreshold = 60

// ShapeOf decodes body and returns its shape; nil if it isn't JSON.
func ShapeOf(body []byte) Shape {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}
	s := Shape{}
	s.add("", v)
	return s
}

// Merge adds o's paths to s.
func (s Shape) Merge(o Shape) {
	for p, ts := range o {
		if s[p] == nil {
			s[p] = map[string]bool{}
		}
		maps.Copy(s[p], ts)
	}
}

func (s Shape) note(path, typ string) {
	if s[path] == nil {
		s[path] = map[string]bool{}
	}
	s[path][typ] = true
}

func (s Shape) add(path string, v any) {
	switch x := v.(type) {
	case map[string]any:
		s.note(path, "object")
		isMap := len(x) > mapThreshold
		for k := range x {
			if !fieldRE.MatchString(k) {
				isMap = true
			}
		}
		for k, e := range x {
			if isMap {
				s.add(path+".*", e)
			} else {
				s.add(join(path, k), e)
			}
		}
	case []any:
		s.note(path, "array")
		for _, e := range x {
			s.add(path+"[]", e)
		}
	case string:
		s.note(path, "string")
	case float64:
		s.note(path, "number")
	case bool:
		s.note(path, "bool")
	case nil:
		s.note(path, "null")
	}
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

// Drift is how a live answer's shape differs from the recording's.
type Drift struct {
	// Removed are recorded fields the live answers never had: the ones an
	// integration may depend on. Only the outermost of a removed subtree.
	Removed []string `json:"removed,omitempty"`
	// Changed are recorded fields whose type is now another:
	// "data[].maxmem: number → string".
	Changed []string `json:"changed,omitempty"`
	// Added are live fields the recording lacks. They come from the lab,
	// so they go to the raw results only, never to the summary.
	Added []string `json:"added,omitempty"`
}

// Empty reports no removed or changed fields: what the integration reads
// is still there. Added fields alone aren't drift that breaks anything.
func (d Drift) Empty() bool { return len(d.Removed) == 0 && len(d.Changed) == 0 }

// Compare returns how live differs from recorded. An object both have
// whose keys share nothing is a map by name (PVE's tokens, by token
// name): both sides merge its keys into ".*" first, so its values are
// still compared but its names aren't. A record whose every field was
// renamed would pass as such a map; its values' types still compare.
func Compare(recorded, live Shape) Drift {
	recorded, live = byName(recorded, live)
	var d Drift
	paths := slices.Sorted(maps.Keys(recorded))
	for _, p := range paths {
		rec := nonNull(recorded[p])
		if len(rec) == 0 {
			continue // only ever null: optional, nothing to compare
		}
		got, ok := live[p]
		if !ok {
			if !under(p, d.Removed) {
				d.Removed = append(d.Removed, p)
			}
			continue
		}
		now := nonNull(got)
		if len(now) == 0 {
			continue // null now: still optional
		}
		if !overlaps(rec, now) {
			d.Changed = append(d.Changed, p+": "+strings.Join(rec, "|")+" → "+strings.Join(now, "|"))
		}
	}
	for p := range live {
		if _, ok := recorded[p]; !ok && !under(p, d.Added) {
			d.Added = append(d.Added, p)
		}
	}
	sort.Strings(d.Added)
	// Sorting can put a child before its parent; keep the outermost.
	d.Added = outermost(d.Added)
	return d
}

func nonNull(ts map[string]bool) []string {
	var out []string
	for t := range ts {
		if t != "null" {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// under reports p inside one of roots: "a.b" under "a", "a[].b" under "a".
func under(p string, roots []string) bool {
	for _, r := range roots {
		if strings.HasPrefix(p, r+".") || strings.HasPrefix(p, r+"[]") {
			return true
		}
	}
	return false
}

func outermost(sorted []string) []string {
	var out []string
	for _, p := range sorted {
		if !under(p, out) {
			out = append(out, p)
		}
	}
	return out
}

// byName merges the keys of objects that are maps by name, outermost first.
func byName(rec, live Shape) (Shape, Shape) {
	for _, p := range slices.Sorted(maps.Keys(rec)) {
		if p == "" || !rec[p]["object"] || !live[p]["object"] { // a whole answer is never a map
			continue
		}
		a, b := children(rec, p), children(live, p)
		if len(a) == 0 || len(b) == 0 || overlaps(a, b) {
			continue
		}
		rec, live = merged(rec, p), merged(live, p)
	}
	return rec, live
}

// children are the field names directly under the object at p.
func children(s Shape, p string) []string {
	seen := map[string]bool{}
	prefix := p + "."
	if p == "" {
		prefix = ""
	}
	for q := range s {
		rest, ok := strings.CutPrefix(q, prefix)
		if !ok || rest == "" || q == p {
			continue
		}
		name := rest
		if i := strings.IndexAny(rest, ".["); i >= 0 {
			name = rest[:i]
		}
		seen[name] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// merged is s with the keys under the object at p merged into "p.*".
func merged(s Shape, p string) Shape {
	out := Shape{}
	prefix := p + "."
	for q, ts := range s {
		if rest, ok := strings.CutPrefix(q, prefix); ok && rest != "" {
			if i := strings.IndexAny(rest, ".["); i >= 0 {
				q = prefix + "*" + rest[i:]
			} else {
				q = prefix + "*"
			}
		}
		if out[q] == nil {
			out[q] = map[string]bool{}
		}
		maps.Copy(out[q], ts)
	}
	return out
}
