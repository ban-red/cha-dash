package homepage

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// maxNodes bounds how many YAML nodes one file may make us visit, aliases
// included, so an alias bomb can't turn a 1 MiB body into unbounded work.
const maxNodes = 200_000

// maxDepth bounds nesting when converting widget options to plain values.
const maxDepth = 32

var errTooComplex = errors.New("too many YAML nodes (aliases expand past the limit)")

// doc walks one parsed file. Every accessor resolves aliases and spends from
// the node budget.
type doc struct {
	file   string
	budget int
	err    error
}

var lineRE = regexp.MustCompile(`line (\d+)`)

// parseFile parses src into its root node, or nil for an empty file. Errors
// keep only the line number: YAML errors can quote the offending text, and
// that text might be a credential.
func parseFile(file, src string) (*yaml.Node, *doc, error) {
	d := &doc{file: file, budget: maxNodes}
	if strings.TrimSpace(src) == "" {
		return nil, d, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		if m := lineRE.FindStringSubmatch(err.Error()); m != nil {
			return nil, d, fmt.Errorf("%s is not valid YAML (line %s)", file, m[1])
		}
		return nil, d, fmt.Errorf("%s is not valid YAML", file)
	}
	n := &root
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil, d, nil
		}
		n = root.Content[0]
	}
	if n.Kind == 0 || d.isNull(n) { // only comments
		return nil, d, nil
	}
	return n, d, nil
}

// resolve follows aliases.
func (d *doc) resolve(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode; i++ {
		if i > 64 {
			d.fail(errTooComplex)
			return nil
		}
		n = n.Alias
	}
	if n != nil {
		d.budget--
		if d.budget < 0 {
			d.fail(errTooComplex)
			return nil
		}
	}
	return n
}

func (d *doc) fail(err error) {
	if d.err == nil {
		d.err = fmt.Errorf("%s: %w", d.file, err)
	}
}

type pair struct {
	key string
	val *yaml.Node
}

// mapping returns a mapping's pairs in source order, with merge keys (<<)
// applied: keys written in the mapping win over merged ones.
func (d *doc) mapping(n *yaml.Node) ([]pair, bool) {
	n = d.resolve(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, false
	}
	var own, merged []pair
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := d.resolve(n.Content[i])
		if k == nil || k.Kind != yaml.ScalarNode {
			continue
		}
		if k.Tag == "!!merge" || (k.Value == "<<" && k.Style == 0) {
			src := d.resolve(n.Content[i+1])
			if src == nil {
				continue
			}
			if src.Kind == yaml.SequenceNode {
				for _, s := range src.Content {
					ps, _ := d.mapping(s)
					merged = append(merged, ps...)
				}
			} else {
				ps, _ := d.mapping(src)
				merged = append(merged, ps...)
			}
			continue
		}
		own = append(own, pair{key: k.Value, val: n.Content[i+1]})
	}
	if len(merged) == 0 {
		return own, true
	}
	seen := make(map[string]bool, len(own))
	for _, p := range own {
		seen[p.key] = true
	}
	out := own
	for _, p := range merged {
		if !seen[p.key] {
			seen[p.key] = true
			out = append(out, p)
		}
	}
	return out, true
}

func (d *doc) sequence(n *yaml.Node) ([]*yaml.Node, bool) {
	n = d.resolve(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil, false
	}
	return n.Content, true
}

// isNull is true for a missing value, ~, null or an empty value.
func (d *doc) isNull(n *yaml.Node) bool {
	n = d.resolve(n)
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// scalar returns a scalar's text exactly as written (minus quotes). The text,
// not the decoded value, so a 32-digit API key with leading zeros stays a
// string instead of becoming the number 1.
func (d *doc) scalar(n *yaml.Node) (string, bool) {
	n = d.resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return "", false
	}
	return n.Value, true
}

// value converts a node to a plain value for widget options: bool, int64,
// float64, string, []any or map[string]any. Strings that look like numbers
// but don't round-trip (leading zeros, huge) stay strings.
func (d *doc) value(n *yaml.Node, depth int) any {
	if depth > maxDepth {
		d.fail(errTooComplex)
		return nil
	}
	n = d.resolve(n)
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			return nil
		case "!!bool":
			if b, err := strconv.ParseBool(strings.ToLower(n.Value)); err == nil {
				return b
			}
		case "!!int":
			if i, err := strconv.ParseInt(n.Value, 10, 64); err == nil && strconv.FormatInt(i, 10) == n.Value {
				return i
			}
		case "!!float":
			if f, err := strconv.ParseFloat(n.Value, 64); err == nil {
				return f
			}
		}
		return n.Value
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, d.value(c, depth+1))
		}
		return out
	case yaml.MappingNode:
		ps, _ := d.mapping(n)
		out := make(map[string]any, len(ps))
		for _, p := range ps {
			out[p.key] = d.value(p.val, depth+1)
		}
		return out
	}
	return nil
}

// boolean reads a YAML boolean (true/false, yes/no as YAML 1.1 configs write
// them).
func (d *doc) boolean(n *yaml.Node) (bool, bool) {
	s, ok := d.scalar(n)
	if !ok {
		return false, false
	}
	switch strings.ToLower(s) {
	case "true", "yes", "on":
		return true, true
	case "false", "no", "off":
		return false, true
	}
	return false, false
}

// integer reads a decimal integer.
func (d *doc) integer(n *yaml.Node) (int, bool) {
	s, ok := d.scalar(n)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(strings.TrimSpace(s))
	return i, err == nil
}

func get(ps []pair, key string) *yaml.Node {
	for _, p := range ps {
		if p.key == key {
			return p.val
		}
	}
	return nil
}
