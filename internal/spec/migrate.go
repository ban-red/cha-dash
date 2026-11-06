package spec

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Migrate rewrites a v1alpha1 spec as v1beta1 and says what it changed,
// one sentence each. A v1beta1 spec comes back as it is, with no changes.
//
// It's a rewrite of the text, not a re-encoding: comments, quoting, key
// order and blank lines stay as written, and only these change:
//   - apiVersion becomes chadash/v1beta1;
//   - `config.x ?? <literal>` loses its `?? <literal>` when the literal is
//     x's declared default, which v1beta1 applies (and the parentheses
//     around it, when they only held it);
//   - `findIndex(…) ?? -1` and `findLastIndex(…) ?? -1` lose their `?? -1`,
//     which v1beta1 returns when nothing matches.
//
// Both removals mean the same in v1beta1 as the original meant in
// v1alpha1, so the migrated spec publishes the same. What can differ is
// named in the changes without being rewritten: a field with a default
// read some other way (it's no longer null when unset), a findIndex
// compared with nil, and now(), which is the hub's clock in v1beta1 (the
// same in a running hub). When a scalar can't be edited in place (a
// multi-line plain or double-quoted scalar with escapes), the YAML is
// re-encoded instead, which keeps comments but not every line's layout.
func Migrate(b []byte) ([]byte, []string, error) {
	s, err := Parse(b)
	if err != nil {
		return nil, nil, err
	}
	if s.beta {
		return b, nil, nil
	}
	defaults := map[string]any{}
	required := map[string]bool{}
	for _, f := range s.f.Config {
		if f.Type != "secret" && f.Default != nil {
			defaults[f.Name] = jsonValue(f.Default)
			required[f.Name] = f.Required
		}
	}

	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, nil, fmt.Errorf("spec: %w", err)
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("spec: not a mapping")
	}
	doc := root.Content[0]

	changes := []string{fmt.Sprintf("apiVersion: %s becomes %s.", V1Alpha1, V1Beta1)}
	type edit struct {
		n   *yaml.Node
		new string
	}
	var edits []edit
	var apiNode *yaml.Node
	usesNow, otherUses := false, map[string]int{}
	var nilIndex []string
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key, val := doc.Content[i].Value, doc.Content[i+1]
		switch key {
		case "apiVersion":
			apiNode = val
			continue
		case "requests", "source":
			// Templates: {{ config.x }} is x's default in v1beta1 when unset.
			walkScalars(val, key, func(n *yaml.Node, _ string) {
				for _, m := range templateRE.FindAllStringSubmatch(n.Value, -1) {
					if _, ok := defaults[m[2]]; ok && m[1] == "config" && !required[m[2]] {
						otherUses[m[2]]++
					}
				}
			})
			continue
		case "version", "resources", "metrics", "repairs", "targets", "actions":
		default:
			continue
		}
		walkScalars(val, key, func(n *yaml.Node, where string) {
			out, notes, handled := rewriteExpr(n.Value, defaults)
			for _, note := range notes {
				changes = append(changes, where+": "+note)
			}
			if hasCall(out, "now") {
				usesNow = true
			}
			for name := range defaults {
				otherUses[name] += countRefs(n.Value, name) - handled[name]
			}
			if remains := nilCompared(out); remains {
				nilIndex = append(nilIndex, where)
			}
			if out != n.Value {
				edits = append(edits, edit{n, out})
			}
		})
	}
	if apiNode == nil {
		return nil, nil, errors.New("spec: no apiVersion")
	}
	// Other reads of a field with a default: v1beta1 answers them with the
	// default instead of null (a required field is never unset).
	for _, name := range slices.Sorted(maps.Keys(defaults)) {
		if n := otherUses[name]; n > 0 && !required[name] {
			changes = append(changes, fmt.Sprintf("config.%s has a default (%s): v1beta1 reads it as that when an instance leaves it unset, where v1alpha1 read null. Check the %d place(s) the spec reads it.", name, literal(defaults[name]), n))
		}
	}
	for _, where := range nilIndex {
		changes = append(changes, where+": findIndex and findLastIndex return -1 in v1beta1 when nothing matches, not null; this compares one with nil.")
	}
	if usesNow {
		changes = append(changes, "now() is the hub's clock in v1beta1, not the wall clock: the same in a running hub, and chadash spec test pins it with now: in fixtures.yaml.")
	}

	// The expected result, as a tree: what the text edit must decode to.
	apiOld := apiNode.Value
	apiNode.Value = V1Beta1
	olds := make([]string, len(edits))
	for i, e := range edits {
		olds[i] = e.n.Value
		e.n.Value = e.new
	}
	var want any
	if err := root.Decode(&want); err != nil {
		return nil, nil, err
	}
	encode := func() ([]byte, error) {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(&root); err != nil {
			return nil, err
		}
		return buf.Bytes(), enc.Close()
	}

	// Edit the text: last edits first, so earlier positions hold.
	apiNode.Value = apiOld
	for i, e := range edits {
		e.n.Value = olds[i]
	}
	text, ok := editText(b, apiNode, func(yield func(*yaml.Node, string) bool) {
		for _, e := range edits {
			if !yield(e.n, e.new) {
				return
			}
		}
	})
	apiNode.Value = V1Beta1
	for _, e := range edits {
		e.n.Value = e.new
	}
	if ok {
		var got any
		if yaml.Unmarshal(text, &got) == nil && reflect.DeepEqual(got, want) {
			if _, err := Parse(text); err == nil {
				return text, changes, nil
			}
		}
	}
	out, err := encode()
	if err != nil {
		return nil, nil, err
	}
	if _, err := Parse(out); err != nil {
		return nil, nil, fmt.Errorf("spec: the migrated spec doesn't load: %w", err)
	}
	return out, append(changes, "The YAML was re-encoded: a scalar couldn't be edited in place. Comments stay; check the layout."), nil
}

// MigrationNotes is what Migrate would change in a v1alpha1 spec, for spec
// lint; nothing for a v1beta1 spec or one that doesn't load.
func MigrationNotes(b []byte) []string {
	_, changes, err := Migrate(b)
	if err != nil {
		return nil
	}
	return changes
}

// walkScalars calls fn on every string scalar under n that's a value, not a
// key, with where it is: resources[2].health.
func walkScalars(n *yaml.Node, where string, fn func(*yaml.Node, string)) {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!str" || n.Tag == "" {
			fn(n, where)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			walkScalars(c, fmt.Sprintf("%s[%d]", where, i), fn)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			walkScalars(n.Content[i+1], where+"."+n.Content[i].Value, fn)
		}
	}
}

// editText applies edits to b's text in place: the apiVersion's value and
// each scalar's new value, in its own style. It returns false when one
// can't be placed exactly.
func editText(b []byte, api *yaml.Node, edits func(func(*yaml.Node, string) bool)) ([]byte, bool) {
	lines := strings.SplitAfter(string(b), "\n")
	type at struct {
		n   *yaml.Node
		new string
	}
	var all []at
	edits(func(n *yaml.Node, s string) bool { all = append(all, at{n, s}); return true })
	all = append(all, at{api, V1Beta1})
	slices.SortFunc(all, func(a, b at) int {
		if a.n.Line != b.n.Line {
			return b.n.Line - a.n.Line
		}
		return b.n.Column - a.n.Column
	})
	for _, e := range all {
		if !replaceScalar(lines, e.n, e.new) {
			return nil, false
		}
	}
	return []byte(strings.Join(lines, "")), true
}

// replaceScalar puts val where n is written, in n's style.
func replaceScalar(lines []string, n *yaml.Node, val string) bool {
	li, col := n.Line-1, n.Column-1
	if li < 0 || li >= len(lines) {
		return false
	}
	line := lines[li]
	switch n.Style {
	case 0, yaml.SingleQuotedStyle, yaml.DoubleQuotedStyle:
		old, ok1 := flow(n.Value, n.Style)
		repl, ok2 := flow(val, n.Style)
		if !ok1 || !ok2 || col+len(old) > len(line) || line[col:col+len(old)] != old {
			return false
		}
		lines[li] = line[:col] + repl + line[col+len(old):]
		return true
	case yaml.LiteralStyle:
		// | (clip) and |- (strip): the value's lines follow, indented.
		header := strings.TrimRight(line[col:], "\r\n")
		if i := strings.Index(header, " #"); i >= 0 {
			header = header[:i]
		}
		header = strings.TrimSpace(header)
		if header != "|" && header != "|-" {
			return false
		}
		oldLines := strings.Split(strings.TrimSuffix(n.Value, "\n"), "\n")
		newLines := strings.Split(strings.TrimSuffix(val, "\n"), "\n")
		// The rewrite never adds or removes a line.
		if len(newLines) != len(oldLines) || li+len(oldLines) >= len(lines) {
			return false
		}
		first := lines[li+1]
		indent := first[:len(first)-len(strings.TrimLeft(first, " "))]
		if indent == "" {
			return false
		}
		for i, ol := range oldLines {
			raw := strings.TrimRight(lines[li+1+i], "\r\n")
			if ol == "" && strings.TrimSpace(raw) == "" {
				continue
			}
			if raw != indent+ol {
				return false
			}
		}
		for i, l := range newLines {
			at := li + 1 + i
			if l != oldLines[i] {
				eol := lines[at][len(strings.TrimRight(lines[at], "\r\n")):]
				lines[at] = indent + l + eol
			}
		}
		return true
	}
	return false
}

// flow is v as a one-line flow scalar in style, or false when it isn't one
// the editor writes back exactly.
func flow(v string, style yaml.Style) (string, bool) {
	if strings.ContainsAny(v, "\n\r\t") {
		return "", false
	}
	switch style {
	case yaml.SingleQuotedStyle:
		return "'" + strings.ReplaceAll(v, "'", "''") + "'", true
	case yaml.DoubleQuotedStyle:
		if strings.ContainsAny(v, `"\`) {
			return "", false
		}
		return `"` + v + `"`, true
	}
	return v, v != "" && strings.TrimSpace(v) == v
}

// masked marks the bytes of src inside a string literal or a comment, where
// a rewrite mustn't look.
func masked(src string) []bool {
	m := make([]bool, len(src))
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '"' || c == '\'' || c == '`':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' && c != '`' {
					j++
				}
				j++
			}
			for k := i; k <= j && k < len(src); k++ {
				m[k] = true
			}
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for ; i < len(src) && src[i] != '\n'; i++ {
				m[i] = true
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			end := len(src)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			for k := i; k < end; k++ {
				m[k] = true
			}
			i = end - 1
		}
	}
	return m
}

func identByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

var (
	indexCallRE  = regexp.MustCompile(`\b(findIndex|findLastIndex)\(`)
	minusOneRE   = regexp.MustCompile(`^\s*\?\?\s*-1`)
	configOrRE   = regexp.MustCompile(`config\.([a-z_][a-z0-9_]*)\s*\?\?\s*(-?[0-9]+(?:\.[0-9]+)?|"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|true|false)`)
	nilCompareRE = regexp.MustCompile(`\b(findIndex|findLastIndex)\(`)
)

// closing is the index of the parenthesis that closes the one at open, or -1.
func closing(src string, m []bool, open int) int {
	depth := 0
	for i := open; i < len(src); i++ {
		if m[i] {
			continue
		}
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// endsOperand says the text from i on doesn't continue the operand before
// it: no member access, index, call or more digits.
func endsOperand(src string, i int) bool {
	return i >= len(src) || !identByte(src[i]) && !strings.ContainsRune(".[(", rune(src[i]))
}

// rewriteExpr makes the v1beta1 removals in one expression and says what it
// did, or what it left and why. handled counts, by field, the config.x ??
// literal it removed or named.
func rewriteExpr(src string, defaults map[string]any) (string, []string, map[string]int) {
	var notes []string
	handled := map[string]int{}
	for changed := true; changed; {
		changed = false
		m := masked(src)
		for _, loc := range indexCallRE.FindAllStringSubmatchIndex(src, -1) {
			start, open := loc[0], loc[1]-1
			if m[start] || start > 0 && (identByte(src[start-1]) || src[start-1] == '.') {
				continue
			}
			end := closing(src, m, open)
			if end < 0 {
				continue
			}
			mo := minusOneRE.FindStringIndex(src[end+1:])
			if mo == nil || !endsOperand(src, end+1+mo[1]) {
				continue
			}
			src = src[:end+1] + src[end+1+mo[1]:]
			notes = append(notes, fmt.Sprintf("drops the ?? -1 after %s, which returns -1 in v1beta1 when nothing matches.", src[loc[2]:loc[3]]))
			changed = true
			break
		}
		if changed {
			continue
		}
		for _, loc := range configOrRE.FindAllStringSubmatchIndex(src, -1) {
			start, end := loc[0], loc[1]
			if m[start] || start > 0 && (identByte(src[start-1]) || src[start-1] == '.') || !endsOperand(src, end) {
				continue
			}
			name, lit := src[loc[2]:loc[3]], src[loc[4]:loc[5]]
			d, ok := defaults[name]
			if !ok {
				continue
			}
			if v, ok := parseLiteral(lit); !ok || !reflect.DeepEqual(v, d) {
				continue // named below
			}
			handled[name]++
			ref := "config." + name
			// Parentheses that only held it go too, unless they're a call's.
			before := strings.TrimRight(src[:start], " ")
			after := strings.TrimLeft(src[end:], " ")
			if strings.HasSuffix(before, "(") && strings.HasPrefix(after, ")") {
				pre := strings.TrimRight(before[:len(before)-1], " ")
				if pre == "" || !identByte(pre[len(pre)-1]) && !strings.ContainsRune(")]?", rune(pre[len(pre)-1])) {
					src = before[:len(before)-1] + ref + after[1:]
					notes = append(notes, fmt.Sprintf("drops the ?? %s after config.%s, its default, which v1beta1 applies.", lit, name))
					changed = true
					break
				}
			}
			src = src[:start] + ref + src[end:]
			notes = append(notes, fmt.Sprintf("drops the ?? %s after config.%s, its default, which v1beta1 applies.", lit, name))
			changed = true
			break
		}
	}
	// A fallback that isn't the default is kept, and never used.
	m := masked(src)
	for _, loc := range configOrRE.FindAllStringSubmatchIndex(src, -1) {
		if m[loc[0]] || loc[0] > 0 && (identByte(src[loc[0]-1]) || src[loc[0]-1] == '.') {
			continue
		}
		name, lit := src[loc[2]:loc[3]], src[loc[4]:loc[5]]
		if d, ok := defaults[name]; ok {
			handled[name]++
			notes = append(notes, fmt.Sprintf("keeps config.%s ?? %s, but in v1beta1 an unset %s is its default, %s, so the %s is never used.", name, lit, name, literal(d), lit))
		}
	}
	return src, notes, handled
}

// parseLiteral reads a number, string or boolean literal as JSON has it.
func parseLiteral(lit string) (any, bool) {
	switch {
	case lit == "true":
		return true, true
	case lit == "false":
		return false, true
	case strings.HasPrefix(lit, `"`):
		s, err := strconv.Unquote(lit)
		return s, err == nil
	case strings.HasPrefix(lit, "'"):
		s, err := strconv.Unquote(`"` + strings.ReplaceAll(strings.ReplaceAll(lit[1:len(lit)-1], `\'`, "'"), `"`, `\"`) + `"`)
		return s, err == nil
	}
	f, err := strconv.ParseFloat(lit, 64)
	return f, err == nil
}

// literal writes a default the way a spec would.
func literal(v any) string {
	if s, ok := v.(string); ok {
		return strconv.Quote(s)
	}
	return fmt.Sprint(v)
}

// hasCall says src calls name(…) outside strings and comments.
func hasCall(src, name string) bool {
	m := masked(src)
	re := regexp.MustCompile(`\b` + name + `\s*\(`)
	for _, loc := range re.FindAllStringIndex(src, -1) {
		if !m[loc[0]] && (loc[0] == 0 || src[loc[0]-1] != '.') {
			return true
		}
	}
	return false
}

// countRefs counts config.name in src outside strings and comments.
func countRefs(src, name string) int {
	m := masked(src)
	re := regexp.MustCompile(`config\.` + name + `\b`)
	n := 0
	for _, loc := range re.FindAllStringIndex(src, -1) {
		if !m[loc[0]] && (loc[0] == 0 || !identByte(src[loc[0]-1]) && src[loc[0]-1] != '.') {
			n++
		}
	}
	return n
}

// nilCompared says src compares a findIndex or findLastIndex with nil or
// falls back from one with ??, which -1 changes.
func nilCompared(src string) bool {
	m := masked(src)
	for _, loc := range nilCompareRE.FindAllStringIndex(src, -1) {
		if m[loc[0]] {
			continue
		}
		end := closing(src, m, loc[1]-1)
		if end < 0 {
			continue
		}
		rest := strings.TrimLeft(src[end+1:], " \n")
		if strings.HasPrefix(rest, "== nil") || strings.HasPrefix(rest, "!= nil") || strings.HasPrefix(rest, "??") {
			return true
		}
	}
	return false
}
