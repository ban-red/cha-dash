// Package yamlboard imports a plain-YAML board: a file a person writes by
// hand (kind: Board) with sections, tiles in order, sizes, and the services
// they connect to. It compiles to the same plan as the other importers, so
// what lands is the layout document and tiles document a Home file carries;
// it leaves out what a person shouldn't have to write: IDs, coordinates,
// supported sizes, TLS pins. Format in README.md.
package yamlboard

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/importer"
)

// Source names this importer: /api/import/yaml, settings["import/yaml"].
const Source = "yaml"

// APIVersion and Kind identify the file.
const (
	APIVersion = "chadash/v1alpha1"
	Kind       = "Board"
	file       = "board.yaml"
)

// tileFor is the catalog widget an integration's tile defaults to.
var tileFor = map[string]string{
	"sonarr": "arr", "radarr": "arr", "lidarr": "arr",
	"plex": "plex", "jellyfin": "plex", "tautulli": "plex",
	"qbittorrent": "downloads", "transmission": "downloads", "nzbget": "downloads", "sabnzbd": "downloads",
	"homeassistant": "homeassistant", "frigate": "cameras", "opnsense": "opnsense",
	"uptimekuma": "uptimekuma", "traefik": "traefik",
}

var (
	boardKeys   = []string{"apiVersion", "kind", "name", "sections"}
	sectionKeys = []string{"title", "span", "tiles"}
	// tileKeys are a tile's own keys; any other key is a config field of
	// the connection it names.
	tileKeys = []string{"name", "href", "icon", "description", "size", "widget", "connect", "check", "options"}
)

// Build parses a board file and maps it with cat (the hub's integrations).
func Build(src []byte, cat importer.Catalog) (*importer.Result, importer.Secrets, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, nil, fmt.Errorf("%s: %s", file, yamlError(err))
	}
	if len(root.Content) == 0 {
		return nil, nil, fmt.Errorf("%s: the file is empty", file)
	}
	doc := root.Content[0]
	if err := checkAliases(doc, 0); err != nil {
		return nil, nil, err
	}
	top, err := mapping(doc, boardKeys, "the file")
	if err != nil {
		return nil, nil, err
	}
	if v := scalar(top["apiVersion"]); v != APIVersion {
		return nil, nil, at(doc, "apiVersion must be %s", APIVersion)
	}
	if v := scalar(top["kind"]); v != Kind {
		return nil, nil, at(doc, "kind must be %s", Kind)
	}
	name := strings.TrimSpace(scalar(top["name"]))
	if name == "" {
		return nil, nil, at(doc, "the board needs a name")
	}
	secs := top["sections"]
	if secs == nil || secs.Kind != yaml.SequenceNode {
		return nil, nil, at(doc, "sections must be a list")
	}

	k := importer.NewKit(Source)
	for _, sn := range secs.Content {
		s, err := mapping(sn, sectionKeys, "a section")
		if err != nil {
			return nil, nil, err
		}
		span := 0
		if n := s["span"]; n != nil {
			if span, err = strconv.Atoi(n.Value); err != nil || span < 1 || span > importer.LayoutColumns {
				return nil, nil, at(n, "span must be 1 to %d", importer.LayoutColumns)
			}
		}
		title := strings.TrimSpace(scalar(s["title"]))
		sec := k.Section(cmp.Or(title, "section"), title, span)
		tiles := s["tiles"]
		if tiles == nil {
			continue
		}
		if tiles.Kind != yaml.SequenceNode {
			return nil, nil, at(tiles, "tiles must be a list")
		}
		for _, tn := range tiles.Content {
			if err := tile(k, sec, tn, cat); err != nil {
				return nil, nil, err
			}
		}
	}
	return k.Finish(name, name, name)
}

func tile(k *importer.Kit, sec *importer.KitSection, n *yaml.Node, cat importer.Catalog) error {
	if n.Kind != yaml.MappingNode {
		return at(n, "a tile must be a mapping")
	}
	fields := map[string]*yaml.Node{}
	var order []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		fields[n.Content[i].Value] = n.Content[i+1]
		order = append(order, n.Content[i].Value)
	}
	name := strings.TrimSpace(scalar(fields["name"]))
	if name == "" {
		return at(n, "every tile needs a name")
	}
	origin := importer.Origin{File: file, Group: sec.Title, Entry: name}
	t := importer.Tile{Widget: cmp.Or(scalar(fields["widget"]), "service"), Name: name, Icon: scalar(fields["icon"]),
		Description: scalar(fields["description"]), Origin: origin}
	if href := scalar(fields["href"]); href != "" {
		if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") {
			return at(fields["href"], "href must be an http(s) link")
		}
		t.Href = href
	}
	if o := fields["options"]; o != nil {
		var opts map[string]any
		if err := o.Decode(&opts); err != nil {
			return at(o, "options must be a mapping")
		}
		t.Options = opts
	}
	if w := scalar(fields["widget"]); w != "" {
		if _, ok := importer.WidgetSizes[w]; !ok {
			return at(fields["widget"], "%q isn't a widget Cha Dash can place from a file", w)
		}
	}

	if integ := scalar(fields["connect"]); integ != "" {
		secret, required, ok := cat.Integration(integ)
		if !ok {
			return at(fields["connect"], "%q isn't an integration this hub has", integ)
		}
		cfg := map[string]any{}
		var extra []string
		for _, key := range order {
			if slices.Contains(tileKeys, key) {
				continue
			}
			extra = append(extra, key)
		}
		u := scalar(fields["url"])
		if u != "" {
			cfg["tls"] = importer.TLSFor(u)
		}
		inst := k.Instance(importer.Instance{ID: k.ID("i_", integ, cmp.Or(u, "name:"+name)), Integration: integ, Name: name, Config: cfg, Origin: origin})
		for _, key := range extra {
			v := fields[key]
			switch {
			case slices.Contains(secret, key):
				val := scalar(v)
				if val == "" || strings.HasPrefix(val, "${") {
					k.Missing(inst, key)
					continue
				}
				if inst.Config[key] == nil {
					k.Secret(inst, key, val)
					k.Hint(importer.Hint{Code: "secret_in_file", Severity: "attention",
						Message:  fmt.Sprintf("%s's %s is now in the vault. Remove it from the file.", inst.Name, key),
						Subjects: []importer.Subject{{Instance: inst.ID, Field: key, Origin: origin}}})
				}
			default:
				var val any
				if err := v.Decode(&val); err != nil {
					return at(v, "%s has the wrong shape", key)
				}
				if s, ok := val.(string); ok && strings.HasPrefix(s, "${") {
					k.Unmapped(importer.Unmapped{Kind: "placeholder", Origin: origin, Type: integ, Fields: []string{key}, Reason: "${…} isn't resolved; set it on the connection in Cha Dash"})
					continue
				}
				if v.Kind == yaml.ScalarNode && !isNumberish(v) {
					val = v.Value // keep the text: an ID with leading zeros stays one
				}
				inst.Config[key] = val
			}
		}
		for _, f := range secret {
			if slices.Contains(required, f) && inst.Config[f] == nil {
				k.Missing(inst, f)
			}
		}
		for _, f := range required {
			if f != "tls" && !slices.Contains(secret, f) && inst.Config[f] == nil {
				k.Unmapped(importer.Unmapped{Kind: "field", Origin: origin, Type: integ, Fields: []string{f}, Reason: "the connection needs it; set it in the file or in Cha Dash"})
			}
		}
		t.Instance = inst.ID
		if scalar(fields["widget"]) == "" {
			t.Widget = cmp.Or(tileFor[integ], "service")
		}
	} else if c := fields["check"]; c == nil || c.Value != "false" {
		target := t.Href
		if c != nil && c.Value != "true" {
			target = c.Value
		}
		t.Check = k.Check(name, target)
	}
	k.Tile(sec, t, scalar(fields["size"]))
	return nil
}

// isNumberish reports whether a plain scalar reads as a number or a bool,
// which keeps its type in config (span: 4, verify: false).
func isNumberish(n *yaml.Node) bool {
	return n.Style == 0 && (n.Tag == "!!int" || n.Tag == "!!float" || n.Tag == "!!bool")
}

func mapping(n *yaml.Node, keys []string, what string) (map[string]*yaml.Node, error) {
	if n.Kind != yaml.MappingNode {
		return nil, at(n, "%s must be a mapping", what)
	}
	out := map[string]*yaml.Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if !slices.Contains(keys, k.Value) {
			return nil, at(k, "%q isn't a key here (expected %s)", k.Value, strings.Join(keys, ", "))
		}
		out[k.Value] = n.Content[i+1]
	}
	return out, nil
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(n.Value)
}

// at is an error naming the file and line, never the text there.
func at(n *yaml.Node, format string, args ...any) error {
	return fmt.Errorf("%s: line %d: %s", file, n.Line, fmt.Sprintf(format, args...))
}

// checkAliases refuses aliases: a hand-written board has no use for them,
// and they're how an alias bomb gets in.
func checkAliases(n *yaml.Node, depth int) error {
	if depth > 64 {
		return at(n, "the file nests too deeply")
	}
	if n.Kind == yaml.AliasNode {
		return at(n, "aliases (*name) aren't allowed in a board file")
	}
	for _, c := range n.Content {
		if err := checkAliases(c, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// yamlError keeps yaml's line number and drops anything that quotes the
// file.
func yamlError(err error) string {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		return "the file has the wrong shape"
	}
	msg := err.Error()
	if i := strings.Index(msg, "line "); i >= 0 {
		rest := msg[i:]
		if j := strings.Index(rest, ":"); j > 0 {
			return rest[:j] + ": not valid YAML"
		}
	}
	return "not valid YAML"
}
