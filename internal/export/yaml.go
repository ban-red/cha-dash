package export

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/integration"
)

// header is the comment at the top of every Home file.
const header = `Cha Dash Home file (` + APIVersion + `): boards, integration instances and settings.
Secrets are never in this file in plain text. Each one is a "!secret <name>"
placeholder: a full backup restores them from the encrypted secrets_bundle
(with its passphrase); otherwise enter them in Cha Dash after importing.
Users, sessions, passkeys and the audit log are never exported.`

// secretTag marks a placeholder: `api_key: !secret instance/i_x/api_key`.
const secretTag = "!secret"

// MaxFileBytes bounds a Home file. A board's layout is at most 1 MiB, so
// this leaves room for a few large boards.
const MaxFileBytes = 4 << 20

// ---------------------------------------------------------------------------
// Encoding

// Encode writes d as a Home file.
func Encode(d *Document) ([]byte, error) {
	root := mapping()
	add(root, "apiVersion", str(APIVersion))
	add(root, "kind", str(Kind))

	meta := mapping()
	if !d.Meta.ExportedAt.IsZero() {
		add(meta, "exported_at", str(d.Meta.ExportedAt.UTC().Format(time.RFC3339)))
	}
	if d.Meta.HubVersion != "" {
		add(meta, "hub_version", str(d.Meta.HubVersion))
	}
	add(root, "meta", meta)

	boards := sequence()
	for _, b := range d.Boards {
		n := mapping()
		add(n, "id", str(b.ID))
		add(n, "name", str(b.Name))
		add(n, "slug", str(b.Slug))
		add(n, "position", integer(b.Position))
		if b.Layout != nil {
			doc, err := jsonToNode(b.Layout.Document)
			if err != nil {
				return nil, fmt.Errorf("export: board %s: layout: %w", b.ID, err)
			}
			l := mapping()
			add(l, "format_version", integer(b.Layout.FormatVersion))
			add(l, "document", doc)
			add(n, "layout", l)
		}
		if len(b.Tiles) > 0 {
			t, err := jsonToNode(b.Tiles)
			if err != nil {
				return nil, fmt.Errorf("export: board %s: tiles: %w", b.ID, err)
			}
			add(n, "tiles", t)
		}
		boards.Content = append(boards.Content, n)
	}
	add(root, "boards", flowIfEmpty(boards))

	insts := sequence()
	for _, in := range d.Instances {
		n := mapping()
		add(n, "id", str(in.ID))
		add(n, "integration", str(in.Integration))
		add(n, "name", str(in.Name))
		if !in.Enabled {
			add(n, "enabled", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"})
		}
		if len(in.Config) > 0 && !bytes.Equal(bytes.TrimSpace(in.Config), []byte("{}")) {
			c, err := jsonToNode(in.Config)
			if err != nil {
				return nil, fmt.Errorf("export: instance %s: config: %w", in.ID, err)
			}
			c.Style = 0 // config reads best as a block
			add(n, "config", c)
		}
		if len(in.TLS) > 0 {
			t, err := jsonToNode(in.TLS)
			if err != nil {
				return nil, fmt.Errorf("export: instance %s: tls: %w", in.ID, err)
			}
			add(n, "tls", t)
		}
		if len(in.Secrets) > 0 {
			s := mapping()
			for _, field := range sortedKeys(in.Secrets) {
				add(s, field, &yaml.Node{Kind: yaml.ScalarNode, Tag: secretTag, Value: in.Secrets[field]})
			}
			add(n, "secrets", s)
		}
		insts.Content = append(insts.Content, n)
	}
	add(root, "instances", flowIfEmpty(insts))

	settings := mapping()
	for _, s := range d.Settings {
		v, err := jsonToNode(s.Value)
		if err != nil {
			return nil, fmt.Errorf("export: setting %s: %w", s.Key, err)
		}
		if v.Kind == yaml.MappingNode {
			v.Style = 0 // a setting reads best as a block, like config
		}
		add(settings, s.Key, v)
	}
	add(root, "settings", flowIfEmpty(settings))

	if b := d.Bundle; b != nil {
		n := mapping()
		add(n, "cipher", str(b.Cipher))
		kdf := mapping()
		kdf.Style = yaml.FlowStyle
		add(kdf, "name", str(b.KDF.Name))
		add(kdf, "log_n", integer(int64(b.KDF.LogN)))
		add(kdf, "r", integer(int64(b.KDF.R)))
		add(kdf, "p", integer(int64(b.KDF.P)))
		add(kdf, "salt", str(base64.StdEncoding.EncodeToString(b.KDF.Salt)))
		add(n, "kdf", kdf)
		add(n, "nonce", str(base64.StdEncoding.EncodeToString(b.Nonce)))
		names := sequence()
		for _, name := range b.Names {
			names.Content = append(names.Content, str(name))
		}
		add(n, "names", flowIfEmpty(names))
		add(n, "ciphertext", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.LiteralStyle, Value: wrap(base64.StdEncoding.EncodeToString(b.Ciphertext), 76)})
		add(root, "secrets_bundle", n)
	}

	doc := &yaml.Node{Kind: yaml.DocumentNode, HeadComment: header, Content: []*yaml.Node{root}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("export: encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func mapping() *yaml.Node  { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }
func sequence() *yaml.Node { return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"} }
func str(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}
func integer(n int64) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(n, 10)}
}
func add(m *yaml.Node, key string, v *yaml.Node) { m.Content = append(m.Content, str(key), v) }

func flowIfEmpty(n *yaml.Node) *yaml.Node {
	if len(n.Content) == 0 {
		n.Style = yaml.FlowStyle
	}
	return n
}

func wrap(s string, width int) string {
	var sb strings.Builder
	for len(s) > width {
		sb.WriteString(s[:width] + "\n")
		s = s[width:]
	}
	sb.WriteString(s + "\n")
	return sb.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// jsonToNode turns a JSON document into YAML nodes, keeping object key
// order and number text. Small objects and arrays of scalars come out in
// flow style, so a layout item is one line.
func jsonToNode(raw json.RawMessage) (*yaml.Node, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	n, err := readJSON(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON value")
	}
	return n, nil
}

func readJSON(dec *json.Decoder) (*yaml.Node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := mapping()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := readJSON(dec)
				if err != nil {
					return nil, err
				}
				add(m, k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			if flowable(m) {
				m.Style = yaml.FlowStyle
			}
			return m, nil
		case '[':
			s := sequence()
			for dec.More() {
				v, err := readJSON(dec)
				if err != nil {
					return nil, err
				}
				s.Content = append(s.Content, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			if flowable(s) {
				s.Style = yaml.FlowStyle
			}
			return s, nil
		}
	case string:
		return str(t), nil
	case json.Number:
		if _, err := strconv.ParseInt(t.String(), 10, 64); err == nil {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: t.String()}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: t.String()}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", tok)
}

// flowable: empty, or scalars only (an object may also hold short arrays of
// scalars, already in flow style), short enough for one line. Children are
// built first, so their style is known.
func flowable(n *yaml.Node) bool {
	width := 0
	for _, c := range n.Content {
		switch {
		case c.Kind == yaml.ScalarNode:
			if strings.Contains(c.Value, "\n") {
				return false
			}
			width += len(c.Value) + 3
		case c.Style == yaml.FlowStyle && (len(c.Content) == 0 || n.Kind == yaml.MappingNode && c.Kind == yaml.SequenceNode):
			for _, cc := range c.Content {
				if cc.Kind != yaml.ScalarNode {
					return false
				}
				width += len(cc.Value) + 2
			}
			width += 2
		default:
			return false
		}
	}
	return width <= 140
}

// ---------------------------------------------------------------------------
// Parsing

// ParseError says where a Home file is wrong. It never quotes a value from
// the file, only paths and line numbers.
type ParseError struct {
	Line int
	Path string
	Msg  string
}

func (e *ParseError) Error() string {
	var sb strings.Builder
	if e.Line > 0 {
		sb.WriteString("line " + strconv.Itoa(e.Line))
	}
	if e.Path != "" {
		if sb.Len() > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(e.Path)
	}
	if sb.Len() > 0 {
		sb.WriteString(": ")
	}
	sb.WriteString(e.Msg)
	return sb.String()
}

var (
	idRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	// vaultNameRE is the vault's own rule (internal/vault).
	vaultNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	fieldRE     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)
)

// Parse reads a Home file. It checks the structure only; Plan checks the
// file against this hub's integrations.
func Parse(data []byte) (*Document, error) {
	if len(data) > MaxFileBytes {
		return nil, &ParseError{Msg: fmt.Sprintf("the file is larger than %d MiB", MaxFileBytes>>20)}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &ParseError{Msg: "the file is empty"}
		}
		return nil, &ParseError{Msg: yamlMessage(err)}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, &ParseError{Line: extra.Line, Msg: "a Home file is one YAML document"}
	}
	if len(root.Content) == 0 {
		return nil, &ParseError{Msg: "the file is empty"}
	}
	if err := noAliases(&root); err != nil {
		return nil, err
	}
	p := parser{}
	d := p.document(root.Content[0])
	if p.err != nil {
		return nil, p.err
	}
	return d, nil
}

// yamlMessage keeps the YAML library's line number but not the text it
// quotes, which might be anything from the file.
func yamlMessage(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "line "); i >= 0 {
		rest := msg[i:]
		if j := strings.IndexByte(rest, ':'); j > 0 {
			return rest[:j] + ": the file isn't valid YAML"
		}
	}
	return "the file isn't valid YAML"
}

func noAliases(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode {
		return &ParseError{Line: n.Line, Msg: "YAML aliases aren't supported in Home files"}
	}
	for _, c := range n.Content {
		if err := noAliases(c); err != nil {
			return err
		}
	}
	return nil
}

// parser collects the first error; later calls are no-ops.
type parser struct{ err error }

func (p *parser) fail(n *yaml.Node, path, format string, args ...any) {
	if p.err != nil {
		return
	}
	line := 0
	if n != nil {
		line = n.Line
	}
	p.err = &ParseError{Line: line, Path: path, Msg: fmt.Sprintf(format, args...)}
}

// fields walks a mapping, calling f for each known key; unknown or repeated
// keys are errors.
func (p *parser) fields(n *yaml.Node, path string, known []string, f func(key string, v *yaml.Node)) {
	if p.err != nil {
		return
	}
	if n.Kind != yaml.MappingNode {
		p.fail(n, path, "must be a mapping")
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content) && p.err == nil; i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			p.fail(k, path, "keys must be plain names")
			return
		}
		if seen[k.Value] {
			p.fail(k, join(path, k.Value), "is repeated")
			return
		}
		seen[k.Value] = true
		if known != nil && !slices.Contains(known, k.Value) {
			p.fail(k, join(path, k.Value), "isn't a field %s has", cmpOr(path, "a Home file"))
			return
		}
		f(k.Value, v)
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (p *parser) scalar(n *yaml.Node, path string) (string, bool) {
	if p.err != nil {
		return "", false
	}
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" || n.Tag == secretTag {
		p.fail(n, path, "must be a value")
		return "", false
	}
	return n.Value, true
}

func (p *parser) text(n *yaml.Node, path string, max int) string {
	s, ok := p.scalar(n, path)
	if !ok {
		return ""
	}
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > max || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		p.fail(n, path, "must be 1–%d characters on one line", max)
		return ""
	}
	return s
}

func (p *parser) matching(n *yaml.Node, path string, re *regexp.Regexp, what string) string {
	s, ok := p.scalar(n, path)
	if ok && !re.MatchString(s) {
		p.fail(n, path, "must be %s", what)
		return ""
	}
	return s
}

func (p *parser) integer(n *yaml.Node, path string, min, max int64) int64 {
	s, ok := p.scalar(n, path)
	if !ok {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n.Tag != "!!int" || v < min || v > max {
		p.fail(n, path, "must be a whole number from %d to %d", min, max)
		return 0
	}
	return v
}

func (p *parser) boolean(n *yaml.Node, path string) bool {
	s, ok := p.scalar(n, path)
	if !ok {
		return false
	}
	v, err := strconv.ParseBool(s)
	if err != nil || n.Tag != "!!bool" {
		p.fail(n, path, "must be true or false")
	}
	return v
}

func (p *parser) base64(n *yaml.Node, path string) []byte {
	s, ok := p.scalar(n, path)
	if !ok {
		return nil
	}
	s = strings.Join(strings.Fields(s), "")
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b
		}
	}
	p.fail(n, path, "must be base64")
	return nil
}

// object reads a mapping as a JSON object, keeping key order.
func (p *parser) object(n *yaml.Node, path string) json.RawMessage {
	if p.err != nil {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		p.fail(n, path, "must be a mapping")
		return nil
	}
	return p.json(n, path)
}

// json converts any YAML value into JSON, keeping key order and number
// text. Placeholders and other custom tags are refused here: they belong
// only under an instance's secrets.
func (p *parser) json(n *yaml.Node, path string) json.RawMessage {
	var buf bytes.Buffer
	p.writeJSON(&buf, n, path)
	if p.err != nil {
		return nil
	}
	return buf.Bytes()
}

var jsonNumberRE = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func (p *parser) writeJSON(buf *bytes.Buffer, n *yaml.Node, path string) {
	if p.err != nil {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		buf.WriteByte('{')
		first := true
		p.fields(n, path, nil, func(key string, v *yaml.Node) {
			if !first {
				buf.WriteByte(',')
			}
			first = false
			k, _ := json.Marshal(key)
			buf.Write(k)
			buf.WriteByte(':')
			p.writeJSON(buf, v, join(path, key))
		})
		buf.WriteByte('}')
	case yaml.SequenceNode:
		buf.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				buf.WriteByte(',')
			}
			p.writeJSON(buf, c, fmt.Sprintf("%s[%d]", path, i))
		}
		buf.WriteByte(']')
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!str", "!!timestamp", "!!binary":
			if strings.Contains(n.Value, "${secret:") {
				p.fail(n, path, "can't hold a secret reference; secrets go under an instance's secrets")
				return
			}
			s, _ := json.Marshal(n.Value)
			buf.Write(s)
		case "!!int", "!!float":
			if jsonNumberRE.MatchString(n.Value) {
				buf.WriteString(n.Value)
				return
			}
			var v float64
			if err := n.Decode(&v); err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
				p.fail(n, path, "must be a number JSON can hold")
				return
			}
			s, _ := json.Marshal(v)
			buf.Write(s)
		case "!!bool":
			v, err := strconv.ParseBool(n.Value)
			if err != nil {
				p.fail(n, path, "must be true or false")
				return
			}
			buf.WriteString(strconv.FormatBool(v))
		case "!!null":
			buf.WriteString("null")
		case secretTag:
			p.fail(n, path, "a !secret placeholder belongs under an instance's secrets")
		default:
			p.fail(n, path, "uses a YAML tag Home files don't support")
		}
	default:
		p.fail(n, path, "isn't a value")
	}
}

func (p *parser) document(n *yaml.Node) *Document {
	d := &Document{Boards: []Board{}, Instances: []Instance{}, Settings: []Setting{}}
	var sawVersion, sawKind bool
	p.fields(n, "", []string{"apiVersion", "kind", "meta", "boards", "instances", "settings", "secrets_bundle"}, func(key string, v *yaml.Node) {
		switch key {
		case "apiVersion":
			sawVersion = true
			if s, _ := p.scalar(v, key); p.err == nil && s != APIVersion {
				p.fail(v, key, "must be %s; this hub reads only that version", APIVersion)
			}
		case "kind":
			sawKind = true
			if s, _ := p.scalar(v, key); p.err == nil && s != Kind {
				p.fail(v, key, "must be %s", Kind)
			}
		case "meta":
			p.meta(v, &d.Meta)
		case "boards":
			d.Boards = p.boards(v)
		case "instances":
			d.Instances = p.instances(v)
		case "settings":
			p.fields(v, key, nil, func(k string, sv *yaml.Node) {
				d.Settings = append(d.Settings, Setting{Key: k, Value: p.json(sv, join(key, k))})
			})
			slices.SortFunc(d.Settings, func(a, b Setting) int { return strings.Compare(a.Key, b.Key) })
		case "secrets_bundle":
			d.Bundle = p.bundle(v)
		}
	})
	if p.err == nil && (!sawVersion || !sawKind) {
		p.fail(n, "", "isn't a Cha Dash Home file: it needs apiVersion: %s and kind: %s", APIVersion, Kind)
	}
	return d
}

func (p *parser) meta(n *yaml.Node, m *Meta) {
	p.fields(n, "meta", []string{"exported_at", "hub_version"}, func(key string, v *yaml.Node) {
		s, ok := p.scalar(v, "meta."+key)
		if !ok {
			return
		}
		switch key {
		case "exported_at":
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				p.fail(v, "meta."+key, "must be an RFC 3339 time")
			}
			m.ExportedAt = t
		case "hub_version":
			m.HubVersion = s
		}
	})
}

func (p *parser) boards(n *yaml.Node) []Board {
	out := []Board{}
	if n.Kind != yaml.SequenceNode {
		p.fail(n, "boards", "must be a list")
		return out
	}
	ids, slugs := map[string]bool{}, map[string]bool{}
	for i, bn := range n.Content {
		path := fmt.Sprintf("boards[%d]", i)
		var b Board
		var sawID, sawName, sawSlug bool
		p.fields(bn, path, []string{"id", "name", "slug", "position", "layout", "tiles"}, func(key string, v *yaml.Node) {
			kp := path + "." + key
			switch key {
			case "id":
				sawID, b.ID = true, p.matching(v, kp, idRE, "an id of letters, digits, _ . and -")
			case "name":
				sawName, b.Name = true, p.text(v, kp, 64)
			case "slug":
				sawSlug, b.Slug = true, p.matching(v, kp, slugRE, "lowercase letters, digits and dashes")
			case "position":
				b.Position = p.integer(v, kp, 0, math.MaxInt32)
			case "layout":
				b.Layout = p.layout(v, kp)
			case "tiles":
				b.Tiles = p.json(v, kp)
			}
		})
		if p.err != nil {
			return out
		}
		switch {
		case !sawID || !sawName || !sawSlug:
			p.fail(bn, path, "needs id, name and slug")
		case ids[b.ID]:
			p.fail(bn, path+".id", "is used by another board in this file")
		case slugs[b.Slug]:
			p.fail(bn, path+".slug", "is used by another board in this file")
		}
		ids[b.ID], slugs[b.Slug] = true, true
		out = append(out, b)
	}
	return out
}

func (p *parser) layout(n *yaml.Node, path string) *Layout {
	l := &Layout{}
	var sawDoc, sawFV bool
	p.fields(n, path, []string{"format_version", "document"}, func(key string, v *yaml.Node) {
		switch key {
		case "format_version":
			sawFV, l.FormatVersion = true, p.integer(v, path+"."+key, 1, math.MaxInt32)
		case "document":
			sawDoc, l.Document = true, p.object(v, path+"."+key)
		}
	})
	if p.err == nil && (!sawDoc || !sawFV) {
		p.fail(n, path, "needs format_version and document")
	}
	if p.err == nil && len(l.Document) > 1<<20 {
		p.fail(n, path+".document", "is larger than 1 MiB")
	}
	return l
}

func (p *parser) instances(n *yaml.Node) []Instance {
	out := []Instance{}
	if n.Kind != yaml.SequenceNode {
		p.fail(n, "instances", "must be a list")
		return out
	}
	ids := map[string]bool{}
	names := map[[2]string]bool{}
	for i, in := range n.Content {
		path := fmt.Sprintf("instances[%d]", i)
		inst := Instance{Enabled: true, Secrets: map[string]string{}, Config: json.RawMessage(`{}`)}
		var sawID, sawInteg, sawName bool
		p.fields(in, path, []string{"id", "integration", "name", "enabled", "config", "tls", "secrets"}, func(key string, v *yaml.Node) {
			kp := path + "." + key
			switch key {
			case "id":
				sawID, inst.ID = true, p.matching(v, kp, idRE, "an id of letters, digits, _ . and -")
			case "integration":
				sawInteg, inst.Integration = true, p.matching(v, kp, idRE, "an integration id")
			case "name":
				sawName, inst.Name = true, p.text(v, kp, 64)
			case "enabled":
				inst.Enabled = p.boolean(v, kp)
			case "config":
				// tls inside config (the stored shape) is accepted too, as
				// long as it isn't given twice.
				if t := cutKey(v, "tls"); t != nil {
					if len(inst.TLS) > 0 {
						p.fail(t, kp+".tls", "is given twice: keep tls at the instance level")
						return
					}
					inst.TLS = p.object(t, kp+".tls")
				}
				inst.Config = p.object(v, kp)
			case "tls":
				if len(inst.TLS) > 0 {
					p.fail(v, kp, "is given twice: keep tls at the instance level")
					return
				}
				inst.TLS = p.object(v, kp)
			case "secrets":
				p.secrets(v, kp, inst.Secrets)
			}
		})
		if p.err != nil {
			return out
		}
		key := [2]string{inst.Integration, strings.ToLower(inst.Name)}
		switch {
		case !sawID || !sawInteg || !sawName:
			p.fail(in, path, "needs id, integration and name")
		case ids[inst.ID]:
			p.fail(in, path+".id", "is used by another instance in this file")
		case names[key]:
			p.fail(in, path+".name", "is used by another %s instance in this file", inst.Integration)
		}
		ids[inst.ID], names[key] = true, true
		if p.err == nil && len(inst.TLS) > 0 {
			var tls any
			_ = json.Unmarshal(inst.TLS, &tls)
			if _, err := integration.ParseTLS(tls); err != nil {
				p.fail(in, path+".tls", "%s", err.Error())
			}
		}
		out = append(out, inst)
	}
	return out
}

// cutKey removes key from a mapping node and returns its value, or nil.
func cutKey(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			v := m.Content[i+1]
			m.Content = slices.Delete(m.Content, i, i+2)
			return v
		}
	}
	return nil
}

func (p *parser) secrets(n *yaml.Node, path string, into map[string]string) {
	p.fields(n, path, nil, func(field string, v *yaml.Node) {
		fp := path + "." + field
		if !fieldRE.MatchString(field) {
			p.fail(v, fp, "isn't a field name")
			return
		}
		name, ok := placeholder(v)
		switch {
		case !ok:
			// Never quote the value: it may be the secret itself.
			p.fail(v, fp, "must be a placeholder (!secret <vault name>, or ${secret:<vault name>}); Home files never hold secret values")
		case !vaultNameRE.MatchString(name):
			p.fail(v, fp, "names a vault entry that isn't valid: use letters, digits and . _ / -")
		default:
			into[field] = name
		}
	})
}

// placeholder returns the vault name a secret's node names.
func placeholder(n *yaml.Node) (string, bool) {
	if n.Kind != yaml.ScalarNode {
		return "", false
	}
	if n.Tag == secretTag {
		return strings.TrimSpace(n.Value), true
	}
	if n.Tag == "!!str" {
		return integration.ParseSecretRef(strings.TrimSpace(n.Value))
	}
	return "", false
}

func (p *parser) bundle(n *yaml.Node) *Bundle {
	b := &Bundle{}
	seen := map[string]bool{}
	p.fields(n, "secrets_bundle", []string{"cipher", "kdf", "nonce", "names", "ciphertext"}, func(key string, v *yaml.Node) {
		kp := "secrets_bundle." + key
		seen[key] = true
		switch key {
		case "cipher":
			b.Cipher, _ = p.scalar(v, kp)
		case "kdf":
			p.fields(v, kp, []string{"name", "log_n", "r", "p", "salt"}, func(k string, kv *yaml.Node) {
				switch k {
				case "name":
					b.KDF.Name, _ = p.scalar(kv, kp+"."+k)
				case "log_n":
					b.KDF.LogN = int(p.integer(kv, kp+"."+k, 1, maxLogN))
				case "r":
					b.KDF.R = int(p.integer(kv, kp+"."+k, 1, 64))
				case "p":
					b.KDF.P = int(p.integer(kv, kp+"."+k, 1, 16))
				case "salt":
					b.KDF.Salt = p.base64(kv, kp+"."+k)
				}
			})
		case "nonce":
			b.Nonce = p.base64(v, kp)
		case "names":
			if v.Kind != yaml.SequenceNode {
				p.fail(v, kp, "must be a list")
				return
			}
			b.Names = []string{}
			for i, c := range v.Content {
				b.Names = append(b.Names, p.matching(c, fmt.Sprintf("%s[%d]", kp, i), vaultNameRE, "a vault name"))
			}
		case "ciphertext":
			b.Ciphertext = p.base64(v, kp)
		}
	})
	if p.err != nil {
		return nil
	}
	for _, k := range []string{"cipher", "kdf", "nonce", "names", "ciphertext"} {
		if !seen[k] {
			p.fail(n, "secrets_bundle", "needs cipher, kdf, nonce, names and ciphertext")
			return nil
		}
	}
	if err := b.check(); err != nil {
		p.fail(n, "secrets_bundle", "is damaged or uses parameters this hub doesn't support")
		return nil
	}
	return b
}
