package importer

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

// WidgetSize is a tile's default size and its supported sizes, as declared
// in web/src/model/widgets.ts. "service" is the generic service tile.
type WidgetSize struct {
	Size  string
	Sizes []string
}

// WidgetSizes are the catalog widgets an importer can place.
var WidgetSizes = map[string]WidgetSize{
	"homeassistant": {"wide", []string{"small", "wide"}},
	"cameras":       {"large", []string{"wide", "large", "hero"}},
	"opnsense":      {"wide", []string{"small", "wide"}},
	"uptimekuma":    {"small", []string{"small", "wide"}},
	"traefik":       {"small", []string{"small", "wide"}},
	"plex":          {"wide", []string{"small", "wide"}},
	"arr":           {"small", []string{"small", "wide"}},
	"downloads":     {"wide", []string{"small", "wide"}},
	"calendar":      {"wide", []string{"wide", "tall"}},
	"service":       {"small", []string{"small", "wide"}},
	"clock":         {"small", []string{"glyph", "strip", "small", "wide"}},
	"weather":       {"small", []string{"glyph", "strip", "small", "wide"}},
	"bookmarks":     {"small", []string{"glyph", "small", "wide", "tall"}},
	"incidents":     {"large", []string{"wide", "tall", "large"}},
	"fleet":         {"large", []string{"wide", "tall", "large", "hero"}},
	"containers":    {"small", []string{"small", "wide", "tall", "large"}},
}

// privateSuffixes are names that only resolve on a LAN, where certificates
// are usually self-signed (Proxmox on :8006, for example).
var privateSuffixes = []string{".lan", ".local", ".localdomain", ".home.arpa", ".internal", ".home", ".localhost", ".intranet", ".corp", ".private"}

// TLSFor picks the TLS policy for an imported URL. Other dashboards rarely
// verified certificates. Trust-on-first-use keeps a LAN host's self-signed
// certificate working while still catching a changed one; public names
// verify normally.
func TLSFor(raw string) map[string]any {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return map[string]any{"mode": "verify"}
	}
	host := strings.ToLower(u.Hostname())
	private := net.ParseIP(host) != nil || !strings.Contains(host, ".")
	for _, s := range privateSuffixes {
		private = private || strings.HasSuffix(host, s)
	}
	if private {
		return map[string]any{"mode": "tofu"}
	}
	return map[string]any{"mode": "verify"}
}

// Slug lowercases s and keeps ASCII letters and digits, joining runs of
// anything else with a single dash.
func Slug(s string) string {
	var sb strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && sb.Len() > 0 {
				sb.WriteByte('-')
			}
			sb.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	out := sb.String()
	if len(out) > 48 {
		out = strings.TrimRight(out[:48], "-")
	}
	return out
}

var kitB32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Catalog is what a parser may ask about the hub's integrations. It's
// filled from the registry; parsers stay free of it otherwise.
type Catalog interface {
	// Integration returns the integration's config fields: the secret ones,
	// and the required ones. ok is false for an integration the hub
	// doesn't have.
	Integration(id string) (secret, required []string, ok bool)
}

// Kit builds a plan for sources whose entries are simple: a section holds
// tiles; a tile may read a connection or be watched by a check. Homarr and
// plain YAML use it. IDs are derived from the source's own keys, so a
// re-import finds the same rows.
type Kit struct {
	source   string
	res      *Result
	secrets  Secrets
	sections []*KitSection
	used     map[string]bool // item and section IDs
	names    map[string]bool // integration + "\x00" + instance name
	checks   []map[string]any
	checkTs  []int
	single   map[string]bool
}

// KitSection is one section being built.
type KitSection struct {
	ID    string
	Title string
	Span  int
	items []LayoutItem
}

// NewKit starts a plan for source.
func NewKit(source string) *Kit {
	return &Kit{
		source: source, secrets: Secrets{},
		res:  &Result{Hints: []Hint{}, Unmapped: []Unmapped{}, Plan: Plan{Source: source, Tiles: []Tile{}, Instances: []Instance{}, Secrets: []SecretRef{}, Links: []Link{}, Discovery: []Discovery{}, Ignored: []Ignored{}}},
		used: map[string]bool{}, names: map[string]bool{}, single: map[string]bool{},
	}
}

// ID derives a stable ID from parts: "<prefix><hash>".
func (k *Kit) ID(prefix string, parts ...string) string {
	h := sha256.Sum256([]byte("chadash/import/" + k.source + "\x00" + strings.Join(parts, "\x00")))
	return prefix + strings.ToLower(kitB32.EncodeToString(h[:10]))
}

func (k *Kit) unique(base string) string {
	id := base
	for n := 2; k.used[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	k.used[id] = true
	return id
}

// Section adds a section. span is clamped to 1–12; 0 means 4.
func (k *Kit) Section(key, title string, span int) *KitSection {
	if span == 0 {
		span = 4
	}
	s := &KitSection{ID: k.unique("section:" + cmpOr(Slug(key), "section"))[len("section:"):], Title: title, Span: min(max(span, 1), LayoutColumns)}
	k.sections = append(k.sections, s)
	return s
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Instance adds a connection, unless one with the same ID is in the plan
// already (two tiles reading the same service share it). It returns the
// plan's copy.
func (k *Kit) Instance(in Instance) *Instance {
	p := &k.res.Plan
	if i := slices.IndexFunc(p.Instances, func(x Instance) bool { return x.ID == in.ID }); i >= 0 {
		return &p.Instances[i]
	}
	base := strings.TrimSpace(in.Name)
	for n := 2; k.names[in.Integration+"\x00"+in.Name]; n++ {
		in.Name = fmt.Sprintf("%s %d", base, n)
	}
	k.names[in.Integration+"\x00"+in.Name] = true
	if in.Config == nil {
		in.Config = map[string]any{}
	}
	if in.SecretFields == nil {
		in.SecretFields = []string{}
	}
	p.Instances = append(p.Instances, in)
	return &p.Instances[len(p.Instances)-1]
}

// Secret moves a value into the vault for in's field.
func (k *Kit) Secret(in *Instance, field, value string) {
	name := "instance/" + in.ID + "/" + field
	k.secrets[name] = NewSecret(value)
	in.Config[field] = SecretRefValue(name)
	in.SecretFields = append(in.SecretFields, field)
	k.res.Plan.Secrets = append(k.res.Plan.Secrets, SecretRef{Name: name, Instance: in.ID, Field: field})
}

// Missing marks in's field as a secret to set in Cha Dash.
func (k *Kit) Missing(in *Instance, field string) {
	if !slices.Contains(in.MissingSecrets, field) {
		in.MissingSecrets = append(in.MissingSecrets, field)
	}
}

// Tile places t in sec at size (or the widget's default), sized to fit.
// A catalog widget that's bound or a singleton gets its catalog ID; the
// rest get a service ID under the section. It returns the item ID.
func (k *Kit) Tile(sec *KitSection, t Tile, size string) string {
	ws, ok := WidgetSizes[t.Widget]
	if !ok {
		t.Widget, ws = "service", WidgetSizes["service"]
	}
	if !slices.Contains(ws.Sizes, size) {
		size = ws.Size
	}
	if SizeWidth(size) > sec.Span {
		if fit := ClosestFit(ws.Sizes, size, sec.Span); fit != "" {
			size = fit
		} else {
			narrowest := ws.Sizes[0]
			for _, s := range ws.Sizes {
				if SizeWidth(s) < SizeWidth(narrowest) {
					narrowest = s
				}
			}
			k.res.Plan.Summary.Notes = append(k.res.Plan.Summary.Notes, fmt.Sprintf("%s is %d columns wide so %q fits.", sec.Title, SizeWidth(narrowest), t.Name))
			sec.Span, size = SizeWidth(narrowest), narrowest
		}
	}
	if t.ItemID == "" {
		switch {
		case t.Widget == "arr" && t.Instance != "":
			in := k.instance(t.Instance)
			t.ItemID = "arr@" + in.Integration + ":" + in.ID + "/app"
		case t.Widget != "service" && !k.single[t.Widget]:
			t.ItemID = t.Widget
		default:
			t.Widget = "service"
			t.ItemID = "service@" + k.source + ":" + sec.ID + "/" + cmpOr(Slug(t.Name), "entry")
		}
		if t.Widget != "service" {
			k.single[t.Widget] = true
		}
	}
	t.ItemID = k.unique(t.ItemID)
	t.Section, t.Size = sec.ID, size
	sec.items = append(sec.items, LayoutItem{ID: t.ItemID, Type: t.Widget, Size: size, SupportedSizes: slices.Clone(ws.Sizes)})
	k.res.Plan.Tiles = append(k.res.Plan.Tiles, t)
	if t.Check != "" {
		k.checkTs = append(k.checkTs, len(k.res.Plan.Tiles)-1)
	}
	return t.ItemID
}

func (k *Kit) instance(id string) *Instance {
	for i := range k.res.Plan.Instances {
		if k.res.Plan.Instances[i].ID == id {
			return &k.res.Plan.Instances[i]
		}
	}
	return &Instance{ID: id}
}

// Check adds an http check on target for the tile being built, named after
// it, and returns the check's name ("" when target isn't http(s)).
func (k *Kit) Check(name, target string) string {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	base := cmpOr(strings.TrimSpace(name), u.Host)
	n := base
	for i := 2; slices.ContainsFunc(k.checks, func(c map[string]any) bool { return c["name"] == n }); i++ {
		n = fmt.Sprintf("%s %d", base, i)
	}
	k.checks = append(k.checks, map[string]any{"name": n, "kind": "http", "url": target, "interval_s": 60})
	return n
}

// Unmapped notes something the source has that Cha Dash can't take.
func (k *Kit) Unmapped(u Unmapped) { k.res.Unmapped = append(k.res.Unmapped, u) }

// Ignore notes a setting that isn't imported.
func (k *Kit) Ignore(g Ignored) { k.res.Plan.Ignored = append(k.res.Plan.Ignored, g) }

// Hint adds a hint to the summary.
func (k *Kit) Hint(h Hint) { k.res.Hints = append(k.res.Hints, h) }

// Note adds a line to the summary's notes.
func (k *Kit) Note(s string) { k.res.Plan.Summary.Notes = append(k.res.Plan.Summary.Notes, s) }

// Finish lays out the board (name; boardKey derives its ID) and returns
// the result.
func (k *Kit) Finish(boardKey, name, checksName string) (*Result, Secrets, error) {
	p := &k.res.Plan
	if len(k.checks) > 0 {
		in := k.Instance(Instance{ID: k.ID("i_", "checks", boardKey), Integration: "checks", Name: checksName,
			Config: map[string]any{"targets": k.checks}, Origin: Origin{File: boardKey}})
		for _, i := range k.checkTs {
			if p.Tiles[i].Instance == "" {
				p.Tiles[i].Instance = in.ID
			}
		}
	}
	for _, in := range p.Instances {
		if len(in.MissingSecrets) > 0 {
			k.Hint(Hint{Code: "missing_secret", Severity: "info", Message: fmt.Sprintf("%s needs %s; add it here or on the connection later.", in.Name, strings.Join(in.MissingSecrets, " and ")),
				Subjects: []Subject{{Instance: in.ID, Origin: in.Origin}}})
		}
	}
	p.Board = Board{ID: k.ID("b_", "board", boardKey), Name: name, Slug: cmpOr(Slug(name), k.source), FormatVersion: LayoutVersion,
		Layout: LayoutDocument{Version: LayoutVersion, Columns: LayoutColumns, Sections: []LayoutSection{}}}
	for _, sec := range k.sections {
		ls := LayoutSection{ID: sec.ID, Span: sec.Span, Items: sec.items}
		if ls.Items == nil {
			ls.Items = []LayoutItem{}
		}
		if sec.Title != "" {
			title := sec.Title
			ls.Title = &title
		}
		p.Board.Layout.Sections = append(p.Board.Layout.Sections, ls)
	}
	if err := p.Board.Layout.Pack(); err != nil {
		return nil, nil, err
	}
	recount(k.res)
	s := &p.Summary
	s.Unmapped, s.IgnoredFields = len(k.res.Unmapped), len(p.Ignored)
	return k.res, k.secrets, nil
}
