// Package homepage imports a Homepage (gethomepage.dev) configuration:
// services.yaml, docker.yaml and settings.yaml go in; a Cha Dash board,
// integration instances and vaulted secrets come out. Build is pure: it
// parses and maps, and touches neither the database nor the network.
// importer.Service applies the result. The mapping table is in README.md.
package homepage

import (
	"cmp"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/importer"
)

// Source names this importer: the route is /api/import/homepage and the
// import record is settings["import/homepage"].
const Source = "homepage"

// File names, as origins report them.
const (
	servicesFile = "services.yaml"
	dockerFile   = "docker.yaml"
	settingsFile = "settings.yaml"
)

// Defaults.
const (
	defaultBoardName = "Homepage"
	defaultSpan      = 4  // a Homepage group without layout hints
	checkInterval    = 60 // seconds
	checksName       = "Homepage"
)

// Input is the request body of POST /api/import/homepage.
type Input struct {
	Services string `json:"services_yaml"`
	Docker   string `json:"docker_yaml"`
	Settings string `json:"settings_yaml"`
}

// Build parses the three files and maps them to a plan. The secret values the
// plan moves come back separately; the plan only names them.
func Build(in Input) (*importer.Result, importer.Secrets, error) {
	b := &builder{
		secrets:    importer.Secrets{},
		sectionIDs: map[string]bool{},
		itemIDs:    map[string]bool{},
		singletons: map[string]bool{},
		checkNames: map[string]bool{},
		instNames:  map[string]bool{},
		docker:     map[string]*importer.Instance{},
		entries:    map[string]instRef{},
		ignoredAt:  map[string]int{},
		digests:    map[string]int{},
		hmacKey:    make([]byte, 32),
	}
	rand.Read(b.hmacKey)
	b.res.Plan.Source = Source

	if err := b.parseSettings(in.Settings); err != nil {
		return nil, nil, err
	}
	if err := b.parseDocker(in.Docker); err != nil {
		return nil, nil, err
	}
	groups, err := b.parseServices(in.Services)
	if err != nil {
		return nil, nil, err
	}
	b.layoutSections(groups)
	for _, sec := range b.sections {
		for _, s := range sec.services {
			b.mapEntry(sec, s)
		}
	}
	b.resolveCalendars()
	b.finishChecks()
	b.res.Plan.Instances = append(b.res.Plan.Instances, b.dockerList...)
	if b.checks != nil {
		b.res.Plan.Instances = append(b.res.Plan.Instances, *b.checks)
	}
	if err := b.finishBoard(); err != nil {
		return nil, nil, err
	}
	b.hints()
	b.summarize()
	for _, d := range []*doc{b.svc, b.dock, b.set} {
		if d != nil && d.err != nil {
			return nil, nil, d.err
		}
	}
	return &b.res, b.secrets, nil
}

type builder struct {
	res     importer.Result
	secrets importer.Secrets

	svc, dock, set *doc

	boardName     string
	instanceName  string
	layout        []groupLayout
	boardSettings map[string]any

	sections   []*section
	sectionIDs map[string]bool
	itemIDs    map[string]bool
	singletons map[string]bool

	checks      *importer.Instance
	checkList   []any
	checkNames  map[string]bool
	checkTiles  []int // tiles bound to a check target
	instNames   map[string]bool
	dockerList  []importer.Instance
	docker      map[string]*importer.Instance // by server name
	entries     map[string]instRef            // "group\x00entry" → the entry's first instance
	calendars   []pendingCalendar
	ignoredAt   map[string]int // ignored key → index in Plan.Ignored
	digests     map[string]int // keyed hash of a secret → index in reused
	reused      [][]importer.Subject
	hmacKey     []byte
	notes       []string
	widenedNote []string
}

// ---------------------------------------------------------------- settings

type groupLayout struct {
	name     string
	style    string
	columns  int
	header   bool
	children []groupLayout
}

func (b *builder) parseSettings(src string) error {
	root, d, err := parseFile(settingsFile, src)
	b.set = d
	if err != nil {
		return err
	}
	b.boardName = defaultBoardName
	if root == nil {
		return nil
	}
	ps, ok := d.mapping(root)
	if !ok {
		return fmt.Errorf("%s must be a mapping of settings", settingsFile)
	}
	for _, p := range ps {
		switch p.key {
		case "title":
			if t, ok := d.scalar(p.val); ok && strings.TrimSpace(t) != "" {
				b.boardName = strings.TrimSpace(t)
			}
		case "instanceName":
			b.instanceName, _ = d.scalar(p.val)
		case "layout":
			b.layout = b.parseLayout(d, p.val)
		case "quicklaunch":
			b.parseQuicklaunch(d, p.val)
		default:
			reason, known := settingsIgnored[p.key]
			if !known {
				reason = "not supported by the importer"
			}
			b.ignore(settingsFile, p.key, "", reason)
		}
	}
	return nil
}

func (b *builder) parseLayout(d *doc, n *yaml.Node) []groupLayout {
	var ps []pair
	if seq, ok := d.sequence(n); ok { // the list form keeps order explicit
		for _, item := range seq {
			more, _ := d.mapping(item)
			ps = append(ps, more...)
		}
	} else {
		ps, _ = d.mapping(n)
	}
	out := make([]groupLayout, 0, len(ps))
	for _, p := range ps {
		gl := groupLayout{name: p.key, header: true}
		opts, _ := d.mapping(p.val)
		for _, o := range opts {
			switch o.key {
			case "style":
				gl.style, _ = d.scalar(o.val)
			case "columns":
				gl.columns, _ = d.integer(o.val)
			case "header":
				if v, ok := d.boolean(o.val); ok {
					gl.header = v
				}
			case "icon":
				b.ignore(settingsFile, "layout.icon", p.key, "sections have no icons")
			case "tab":
				b.ignore(settingsFile, "layout.tab", p.key, "tabs aren't imported: every section goes on one board")
			case "initiallyCollapsed", "useEqualHeights":
				b.ignore(settingsFile, "layout."+o.key, p.key, settingsIgnored["groupsInitiallyCollapsed"])
			default:
				if _, isMap := d.mapping(o.val); isMap {
					gl.children = append(gl.children, b.parseLayout(d, &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
						{Kind: yaml.ScalarNode, Value: o.key}, o.val,
					}})...)
				} else {
					b.ignore(settingsFile, "layout."+o.key, p.key, "not supported by the importer")
				}
			}
		}
		out = append(out, gl)
	}
	return out
}

func (b *builder) parseQuicklaunch(d *doc, n *yaml.Node) {
	ps, _ := d.mapping(n)
	ql := map[string]any{}
	for _, p := range ps {
		_, known := quicklaunchKnown[p.key]
		switch {
		case p.key == "searchDescriptions":
			if v, ok := d.boolean(p.val); ok {
				ql["search_descriptions"] = v
			}
		case known: // already how ⌘K behaves
		default:
			b.ignore(settingsFile, "quicklaunch."+p.key, "", "⌘K is always on and has no such option")
		}
	}
	if len(ql) > 0 {
		b.boardSetting("quicklaunch", ql)
	}
}

func (b *builder) boardSetting(key string, v any) {
	if b.boardSettings == nil {
		b.boardSettings = map[string]any{}
	}
	if _, set := b.boardSettings[key]; !set {
		b.boardSettings[key] = v
	}
}

// ------------------------------------------------------------------ docker

func (b *builder) parseDocker(src string) error {
	root, d, err := parseFile(dockerFile, src)
	b.dock = d
	if err != nil || root == nil {
		return err
	}
	ps, ok := d.mapping(root)
	if !ok {
		return fmt.Errorf("%s must map server names to endpoints", dockerFile)
	}
	for _, p := range ps {
		origin := importer.Origin{File: dockerFile, Entry: p.key}
		opts, ok := d.mapping(p.val)
		if !ok {
			b.unmapped(importer.Unmapped{Kind: "docker_server", Origin: origin, Reason: "a server needs host and port, or socket"})
			continue
		}
		var endpoint string
		host, hasHost := d.scalar(get(opts, "host"))
		socket, hasSocket := d.scalar(get(opts, "socket"))
		switch {
		case hasSocket && strings.TrimSpace(socket) != "":
			endpoint = "unix://" + strings.TrimSpace(socket)
		case hasHost && strings.TrimSpace(host) != "":
			port := 2375
			if v, ok := d.integer(get(opts, "port")); ok {
				port = v
			}
			endpoint = "tcp://" + net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
		default:
			b.unmapped(importer.Unmapped{Kind: "docker_server", Origin: origin, Fields: keys(opts), Reason: "a server needs host and port, or socket"})
			continue
		}
		for _, o := range opts {
			switch o.key {
			case "host", "port", "socket":
			case "tls":
				b.unmapped(importer.Unmapped{Kind: "docker_tls", Origin: origin, Fields: keys(mustMapping(d, o.val)),
					Reason: "TLS client certificates are file paths on the Homepage host; add them to the Docker instance in Cha Dash"})
			case "swarm":
				b.ignore(dockerFile, "swarm", p.key, "Swarm services aren't imported; containers are")
			default:
				b.ignore(dockerFile, o.key, p.key, "not supported by the importer")
			}
		}
		inst := importer.Instance{
			ID: instanceID(dockerFile, p.key), Integration: "docker", Name: b.instanceName2("docker", p.key, ""),
			Config: map[string]any{"endpoint": endpoint}, SecretFields: []string{}, Origin: origin,
		}
		b.dockerList = append(b.dockerList, inst)
	}
	for i := range b.dockerList {
		b.docker[b.dockerList[i].Origin.Entry] = &b.dockerList[i]
	}
	return nil
}

// ---------------------------------------------------------------- services

type group struct {
	name      string
	path      []string // names from the top-level group down
	services  []entry
	subgroups []*group
}

func (g *group) key() string     { return strings.Join(g.path, "\x00") }
func (g *group) display() string { return strings.Join(g.path, " › ") }

type entry struct {
	key    string
	node   *yaml.Node
	weight int
	index  int
}

func (b *builder) parseServices(src string) ([]*group, error) {
	root, d, err := parseFile(servicesFile, src)
	b.svc = d
	if err != nil || root == nil {
		return nil, err
	}
	var groups []*group
	add := func(ps []pair) {
		for _, p := range ps {
			groups = append(groups, b.parseGroup(p.key, p.val, nil))
		}
	}
	if seq, ok := d.sequence(root); ok {
		for _, item := range seq {
			ps, ok := d.mapping(item)
			if !ok {
				b.unmapped(importer.Unmapped{Kind: "entry", Origin: importer.Origin{File: servicesFile}, Reason: "a top-level item must be a group: a name holding a list of services"})
				continue
			}
			add(ps)
		}
	} else if ps, ok := d.mapping(root); ok {
		add(ps)
	} else {
		return nil, fmt.Errorf("%s must be a list of groups", servicesFile)
	}
	return groups, nil
}

func (b *builder) parseGroup(name string, n *yaml.Node, parent []string) *group {
	d := b.svc
	g := &group{name: name, path: append(slices.Clone(parent), name)}
	addPair := func(p pair) {
		if _, isSeq := d.sequence(p.val); isSeq {
			g.subgroups = append(g.subgroups, b.parseGroup(p.key, p.val, g.path))
			return
		}
		g.services = append(g.services, entry{key: p.key, node: p.val, index: len(g.services)})
	}
	switch {
	case d.isNull(n):
	default:
		if seq, ok := d.sequence(n); ok {
			for _, item := range seq {
				ps, ok := d.mapping(item)
				if !ok {
					b.unmapped(importer.Unmapped{Kind: "entry", Origin: importer.Origin{File: servicesFile, Group: g.display()},
						Reason: "a group's items must be services (a name holding its settings)"})
					continue
				}
				for _, p := range ps {
					addPair(p)
				}
			}
		} else if ps, ok := d.mapping(n); ok {
			for _, p := range ps {
				addPair(p)
			}
		} else {
			b.unmapped(importer.Unmapped{Kind: "entry", Origin: importer.Origin{File: servicesFile, Group: g.display()},
				Reason: "a group must hold a list of services"})
		}
	}
	// Homepage orders a group by weight, defaulting to (index + 1) × 100.
	for i := range g.services {
		g.services[i].weight = (g.services[i].index + 1) * 100
		if ps, ok := d.mapping(g.services[i].node); ok {
			if w, ok := d.integer(get(ps, "weight")); ok {
				g.services[i].weight = w
			}
		}
	}
	slices.SortStableFunc(g.services, func(a, c entry) int { return cmp.Compare(a.weight, c.weight) })
	return g
}

// ---------------------------------------------------------------- sections

type section struct {
	id       string
	title    string
	untitled bool
	span     int
	group    *group
	services []entry
	items    []importer.LayoutItem
	// layoutOnly sections come from settings.layout alone: Homepage fills
	// them from container labels.
	layoutOnly bool
	origin     string
}

// layoutSections orders sections the way Homepage renders groups: those
// named in settings.layout first, in that order, then the rest in file
// order. A nested group follows its parent.
func (b *builder) layoutSections(groups []*group) {
	layoutByName := map[string]groupLayout{}
	var walkLayout func([]groupLayout)
	walkLayout = func(ls []groupLayout) {
		for _, l := range ls {
			if _, dup := layoutByName[l.name]; !dup {
				layoutByName[l.name] = l
			}
			walkLayout(l.children)
		}
	}
	walkLayout(b.layout)

	byName := map[string]*group{}
	for _, g := range groups {
		if _, dup := byName[g.name]; !dup {
			byName[g.name] = g
		}
	}
	placed := map[*group]bool{}
	var place func(g *group, l *groupLayout)
	place = func(g *group, l *groupLayout) {
		if placed[g] {
			return
		}
		placed[g] = true
		if l == nil {
			if gl, ok := layoutByName[g.name]; ok {
				l = &gl
			}
		}
		b.addSection(g.name, g, l)
		for _, sg := range g.subgroups {
			place(sg, nil)
		}
	}
	for _, l := range b.layout {
		if g, ok := byName[l.name]; ok {
			place(g, &l)
			continue
		}
		b.addSection(l.name, nil, &l)
	}
	for _, g := range groups {
		place(g, nil)
	}
}

func (b *builder) addSection(name string, g *group, l *groupLayout) {
	sec := &section{title: name, span: defaultSpan, group: g, origin: name}
	base := slug(name)
	if g != nil {
		base = slug(strings.Join(g.path, "-"))
		sec.services = g.services
		sec.origin = g.display()
	} else {
		sec.layoutOnly = true
	}
	if base == "" {
		base = "section"
	}
	sec.id = unique(base, b.sectionIDs)
	if l != nil {
		if l.style == "row" && l.columns > 0 {
			sec.span = min(max(l.columns, 1), importer.LayoutColumns)
			if l.columns > importer.LayoutColumns {
				b.notes = append(b.notes, fmt.Sprintf("%s asks for %d columns; the board has %d.", name, l.columns, importer.LayoutColumns))
			}
		}
		sec.untitled = !l.header
	}
	b.sections = append(b.sections, sec)
	if sec.layoutOnly {
		endpoints := make([]string, 0, len(b.dockerList))
		for _, in := range b.dockerList {
			endpoints = append(endpoints, in.Origin.Entry)
		}
		msg := fmt.Sprintf("%s has no services in %s. Homepage fills it from homepage.* container labels, so it's created empty and fills in through discovery", name, servicesFile)
		if len(endpoints) > 0 {
			msg += " (" + strings.Join(endpoints, ", ") + ")"
		}
		b.res.Plan.Discovery = append(b.res.Plan.Discovery, importer.Discovery{Kind: "label_section", Section: sec.id, Message: msg + "."})
	}
}

// ------------------------------------------------------------------ entries

func (b *builder) mapEntry(sec *section, e entry) {
	d := b.svc
	origin := importer.Origin{File: servicesFile, Group: sec.origin, Entry: e.key}
	ps, ok := d.mapping(e.node)
	if !ok && !d.isNull(e.node) {
		b.unmapped(importer.Unmapped{Kind: "entry", Origin: origin, Reason: "a service must be a name holding its settings"})
		return
	}
	name := e.key
	if v, ok := d.scalar(get(ps, "name")); ok {
		name = v
	}
	str := func(k string) string { v, _ := d.scalar(get(ps, k)); return strings.TrimSpace(v) }
	base := importer.Tile{
		Name: name, Icon: str("icon"), Href: str("href"), Description: str("description"),
		Chromeless: strings.TrimSpace(name) == "", Origin: origin,
	}
	if v, ok := d.boolean(get(ps, "showStats")); ok {
		base.Options = map[string]any{"show_stats": v}
	}
	for _, p := range ps {
		if entryFields[p.key] {
			continue
		}
		reason, known := entryIgnored[p.key]
		if !known {
			reason = "not used by Cha Dash"
		}
		b.ignore(servicesFile, p.key, origin.String(), reason)
	}

	// Widgets: `widget` (one) and `widgets` (a list), in that order.
	var widgets []*yaml.Node
	for _, k := range []string{"widget", "widgets"} {
		n := get(ps, k)
		if seq, ok := d.sequence(n); ok {
			widgets = append(widgets, seq...)
		} else if !d.isNull(n) {
			widgets = append(widgets, n)
		}
	}

	var tiles []importer.Tile
	var primary string // the first instance's id
	integrated := false
	for i, wn := range widgets {
		t, inst, ok := b.mapWidget(sec, e, i, wn, base, origin)
		if !ok {
			continue
		}
		integrated = true // an instance, or a calendar bound to other instances
		if primary == "" {
			primary = inst
		}
		tiles = append(tiles, t)
	}
	if len(tiles) == 0 {
		t := base
		t.Widget = "service"
		t.ItemID = b.serviceItemID(sec, e.key, "")
		tiles = append(tiles, t)
	}

	// Checks: siteMonitor always; otherwise a tile without an integration is
	// watched through its href, so every tile has a status.
	first := &tiles[0]
	monitor := str("siteMonitor")
	ping := str("ping")
	switch {
	case monitor != "":
		b.addCheck(first, name, sec, monitor, origin)
	case ping != "" && !integrated && isHTTP(base.Href):
		b.ignore(servicesFile, "ping", origin.String(), "ICMP ping isn't a check kind; the tile gets an HTTP check on its href instead")
		b.addCheck(first, name, sec, base.Href, origin)
	case ping != "":
		b.unmapped(importer.Unmapped{Kind: "check", Origin: origin, Fields: []string{"ping"}, Reason: "ICMP ping isn't a check kind yet; add an HTTP or TCP check in Cha Dash"})
	case !integrated && isHTTP(base.Href):
		b.addCheck(first, name, sec, base.Href, origin)
	}

	// Explicit container link.
	server, container := str("server"), str("container")
	switch {
	case server != "" && container != "":
		dk := b.docker[server]
		if dk == nil {
			b.unmapped(importer.Unmapped{Kind: "container_link", Origin: origin, Fields: []string{"server", "container"},
				Reason: fmt.Sprintf("server %q isn't in %s", server, dockerFile)})
			break
		}
		l := importer.Link{Type: "runs_on", Tile: first.ItemID, Docker: dk.ID, Endpoint: server, Container: container, Origin: origin}
		l.Instance = primary
		b.res.Plan.Links = append(b.res.Plan.Links, l)
	case container != "":
		b.unmapped(importer.Unmapped{Kind: "container_link", Origin: origin, Fields: []string{"container"}, Reason: "container without a server: Homepage needs both to find it"})
	case server != "":
		b.unmapped(importer.Unmapped{Kind: "container_link", Origin: origin, Fields: []string{"server"}, Reason: "server without a container"})
	}

	for _, t := range tiles {
		b.placeTile(sec, t)
	}
}

// mapWidget maps one widget to a tile and, unless it's a calendar, an
// instance (whose id it returns). ok=false means the widget is unmapped
// (and reported); the entry then falls back to a plain service tile.
func (b *builder) mapWidget(sec *section, e entry, idx int, wn *yaml.Node, base importer.Tile, origin importer.Origin) (t importer.Tile, instID string, ok bool) {
	d := b.svc
	ws, ok := d.mapping(wn)
	if !ok {
		b.unmapped(importer.Unmapped{Kind: "widget", Origin: origin, Reason: "a widget must be a mapping with a type"})
		return importer.Tile{}, "", false
	}
	typ, _ := d.scalar(get(ws, "type"))
	typ = strings.ToLower(strings.TrimSpace(typ))
	if typ == "calendar" {
		return b.mapCalendar(sec, e, ws, base, origin), "", true
	}
	spec, known := widgetSpecs[typ]
	if !known {
		reason := fmt.Sprintf("widget type %q has no Cha Dash integration yet", typ)
		if typ == "" {
			reason = "the widget has no type"
		}
		// Field names only: an unmapped widget's values (keys, passwords)
		// stay in the Homepage file and never reach the plan.
		b.unmapped(importer.Unmapped{Kind: "widget", Origin: origin, Type: typ, Fields: keys(ws), Reason: reason})
		return importer.Tile{}, "", false
	}

	name := base.Name
	if strings.TrimSpace(name) == "" {
		name = e.key
	}
	if idx > 0 {
		name += " (" + typ + ")"
	}
	inst := importer.Instance{
		ID:          instanceID(servicesFile, sec.group.key(), e.key, strconv.Itoa(idx)),
		Integration: spec.integration,
		Config:      map[string]any{},
		Origin:      origin,
	}
	inst.Name = b.instanceName2(spec.integration, name, sec.title)

	if u, ok := d.scalar(get(ws, "url")); ok {
		u = strings.TrimSpace(u)
		if isPlaceholder(u) {
			b.unmapped(importer.Unmapped{Kind: "placeholder", Origin: origin, Type: typ, Fields: []string{"url"},
				Reason: "the URL is a {{HOMEPAGE_VAR_…}} placeholder; set it on the instance in Cha Dash"})
		} else {
			inst.Config["url"] = u
			inst.Config["tls"] = tlsFor(u)
		}
	}

	handled := map[string]bool{}
	for _, f := range spec.fields {
		handled[f.from] = true
		v, present := d.scalar(get(ws, f.from))
		v = strings.TrimSpace(v)
		switch {
		case !present || v == "":
			if f.secret && !f.optional {
				inst.MissingSecrets = append(inst.MissingSecrets, f.to)
			}
		case isPlaceholder(v):
			if f.secret {
				inst.MissingSecrets = append(inst.MissingSecrets, f.to)
			} else {
				b.unmapped(importer.Unmapped{Kind: "placeholder", Origin: origin, Type: typ, Fields: []string{f.from},
					Reason: "the value is a {{HOMEPAGE_VAR_…}} placeholder; set it on the instance in Cha Dash"})
			}
		case f.secret:
			name := "instance/" + inst.ID + "/" + f.to
			b.secrets[name] = importer.NewSecret(v)
			inst.Config[f.to] = importer.SecretRefValue(name)
			inst.SecretFields = append(inst.SecretFields, f.to)
			b.res.Plan.Secrets = append(b.res.Plan.Secrets, importer.SecretRef{Name: name, Instance: inst.ID, Field: f.to})
			b.noteDigest(v, importer.Subject{Instance: inst.ID, Field: f.to, Origin: origin})
		default:
			inst.Config[f.to] = v
		}
	}
	if inst.SecretFields == nil {
		inst.SecretFields = []string{}
	}

	t = base
	t.Widget = spec.tile
	t.Instance = inst.ID
	for from, to := range spec.options {
		handled[from] = true
		if n := get(ws, from); !d.isNull(n) {
			t.Options = withOption(t.Options, to, d.value(n, 0))
		}
	}
	if n := get(ws, "fields"); !d.isNull(n) {
		t.Options = withOption(t.Options, "fields", d.value(n, 0))
	}
	for _, p := range ws {
		if !commonWidgetFields[p.key] && !handled[p.key] {
			b.ignore(servicesFile, "widget."+p.key, origin.String(), "not used by the "+spec.integration+" integration")
		}
	}

	// Catalog widgets are singletons on a board ("plex"), or bound to their
	// instance's app ("arr@sonarr:i_…/app", 07 · Wave 5 conventions); a second
	// singleton of a kind gets a service tile.
	id := spec.tile
	if spec.bound {
		id = spec.tile + "@" + spec.integration + ":" + inst.ID + "/app"
	}
	if spec.tile == "service" || b.singletons[id] {
		t.Widget = "service"
		suffix := ""
		if idx > 0 {
			suffix = typ
		}
		t.ItemID = b.serviceItemID(sec, e.key, suffix)
	} else {
		b.singletons[id] = true
		t.ItemID = id
		b.itemIDs[id] = true
	}

	b.res.Plan.Instances = append(b.res.Plan.Instances, inst)
	b.registerEntry(sec, e.key, base.Name, instRef{id: inst.ID, integration: inst.Integration})
	return t, inst.ID, true
}

type instRef struct{ id, integration string }

// registerEntry remembers an entry's first instance under its group and both
// its key and display name, for calendar service_group/service_name lookups.
func (b *builder) registerEntry(sec *section, key, name string, ref instRef) {
	for _, n := range []string{key, name} {
		k := sec.title + "\x00" + n
		if _, set := b.entries[k]; !set {
			b.entries[k] = ref
		}
	}
}

type pendingCalendar struct {
	tile    int // index into Plan.Tiles, set when placed
	section *section
	refs    []calendarRef
	origin  importer.Origin
}

type calendarRef struct {
	typ, group, service string
	options             map[string]any
}

func (b *builder) mapCalendar(sec *section, e entry, ws []pair, base importer.Tile, origin importer.Origin) importer.Tile {
	d := b.svc
	t := base
	t.Widget = "calendar"
	if b.singletons["calendar"] {
		t.ItemID = b.serviceItemID(sec, e.key, "calendar")
	} else {
		b.singletons["calendar"] = true
		t.ItemID = "calendar"
		b.itemIDs["calendar"] = true
	}
	pc := pendingCalendar{tile: -1, section: sec, origin: origin}
	for _, p := range ws {
		switch p.key {
		case "type":
		case "firstDayInWeek":
			if v, ok := d.scalar(p.val); ok {
				b.boardSetting("locale", map[string]any{"first_day_of_week": strings.ToLower(v)})
			}
		case "view":
			t.Options = withOption(t.Options, "view", d.value(p.val, 0))
		case "maxEvents":
			t.Options = withOption(t.Options, "max_events", d.value(p.val, 0))
		case "showTime":
			t.Options = withOption(t.Options, "show_time", d.value(p.val, 0))
		case "previousDays":
			t.Options = withOption(t.Options, "previous_days", d.value(p.val, 0))
		case "timezone":
			t.Options = withOption(t.Options, "timezone", d.value(p.val, 0))
		case "integrations":
			items, _ := d.sequence(p.val)
			for _, it := range items {
				ips, _ := d.mapping(it)
				str := func(k string) string { v, _ := d.scalar(get(ips, k)); return strings.TrimSpace(v) }
				ref := calendarRef{typ: strings.ToLower(str("type")), group: str("service_group"), service: str("service_name")}
				if pn := get(ips, "params"); !d.isNull(pn) {
					if m, ok := d.value(pn, 0).(map[string]any); ok {
						for k, v := range m {
							if v != nil {
								ref.options = withOption(ref.options, k, v)
							}
						}
					}
				}
				if c, ok := d.scalar(get(ips, "color")); ok && strings.TrimSpace(c) != "" {
					ref.options = withOption(ref.options, "color", strings.TrimSpace(c))
				}
				pc.refs = append(pc.refs, ref)
			}
		default:
			b.ignore(servicesFile, "widget."+p.key, origin.String(), "not used by the media calendar")
		}
	}
	b.calendars = append(b.calendars, pc)
	return t
}

// resolveCalendars binds each calendar to the instances its integrations
// name, now that every entry is mapped.
func (b *builder) resolveCalendars() {
	for _, pc := range b.calendars {
		if pc.tile < 0 {
			continue
		}
		t := &b.res.Plan.Tiles[pc.tile]
		for _, ref := range pc.refs {
			inst, found := b.entries[ref.group+"\x00"+ref.service]
			switch {
			case !calendarSources[ref.typ]:
				b.unmapped(importer.Unmapped{Kind: "calendar_source", Origin: pc.origin, Type: ref.typ,
					Reason: fmt.Sprintf("the calendar can't read %q yet (Sonarr, Radarr and Lidarr only)", ref.typ)})
			case !found || inst.integration != ref.typ:
				b.unmapped(importer.Unmapped{Kind: "calendar_source", Origin: pc.origin, Type: ref.typ,
					Reason: fmt.Sprintf("no %s service named %q in group %q", ref.typ, ref.service, ref.group)})
			default:
				t.Sources = append(t.Sources, importer.Source{Instance: inst.id, Capability: calendarCapability, Options: ref.options})
			}
		}
	}
}

func (b *builder) serviceItemID(sec *section, key, suffix string) string {
	s := slug(key)
	if s == "" {
		s = "entry"
	}
	if suffix != "" {
		s += "-" + slug(suffix)
	}
	return unique("service@homepage:"+sec.id+"/"+s, b.itemIDs)
}

// placeTile sizes a tile for its section and appends it to the plan and the
// layout.
func (b *builder) placeTile(sec *section, t importer.Tile) {
	cs := catalog[t.Widget]
	size := cs.size
	if importer.SizeWidth(size) > sec.span {
		size = importer.ClosestFit(cs.sizes, cs.size, sec.span)
		if size == "" { // nothing fits: widen the section to the narrowest size
			narrowest := cs.sizes[0]
			for _, s := range cs.sizes {
				if importer.SizeWidth(s) < importer.SizeWidth(narrowest) {
					narrowest = s
				}
			}
			b.widenedNote = append(b.widenedNote, fmt.Sprintf("%s is %d columns wide so %q fits.", sec.title, importer.SizeWidth(narrowest), t.Name))
			sec.span = importer.SizeWidth(narrowest)
			size = narrowest
		}
	}
	t.Section = sec.id
	t.Size = size
	sec.items = append(sec.items, importer.LayoutItem{ID: t.ItemID, Type: t.Widget, Size: size, SupportedSizes: slices.Clone(cs.sizes)})
	b.res.Plan.Tiles = append(b.res.Plan.Tiles, t)
	idx := len(b.res.Plan.Tiles) - 1
	if t.Check != "" {
		b.checkTiles = append(b.checkTiles, idx)
	}
	for i := range b.calendars {
		if b.calendars[i].tile < 0 && b.calendars[i].section == sec && t.Widget == "calendar" && t.Origin == b.calendars[i].origin {
			b.calendars[i].tile = idx
		}
	}
}

// ------------------------------------------------------------------- checks

func (b *builder) addCheck(t *importer.Tile, name string, sec *section, target string, origin importer.Origin) {
	n := strings.TrimSpace(name)
	if n == "" {
		n = origin.Entry
	}
	if b.checkNames[n] {
		n += " (" + sec.title + ")"
	}
	n = unique(n, b.checkNames)
	check := map[string]any{"name": n, "interval_s": checkInterval}
	if u, err := url.Parse(target); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		check["kind"] = "http"
		check["url"] = target
	} else if host, port, err := net.SplitHostPort(target); err == nil {
		p, _ := strconv.Atoi(port)
		check["kind"], check["host"], check["port"] = "tcp", host, p
	} else {
		b.unmapped(importer.Unmapped{Kind: "check", Origin: origin, Fields: []string{"siteMonitor"}, Reason: "the monitor target isn't an http(s) URL or host:port"})
		return
	}
	b.checkList = append(b.checkList, check)
	t.Check = n
}

func (b *builder) finishChecks() {
	if len(b.checkList) == 0 {
		return
	}
	id := instanceID(servicesFile, "checks")
	b.checks = &importer.Instance{
		ID: id, Integration: "checks", Name: b.instanceName2("checks", checksName, ""),
		Config:       map[string]any{"targets": b.checkList},
		SecretFields: []string{},
		Origin:       importer.Origin{File: servicesFile},
	}
	for _, i := range b.checkTiles {
		if b.res.Plan.Tiles[i].Instance == "" {
			b.res.Plan.Tiles[i].Instance = id
		}
	}
}

// -------------------------------------------------------------------- board

func (b *builder) finishBoard() error {
	p := &b.res.Plan
	p.Board = importer.Board{
		ID:            "b_" + hashID("board"),
		Name:          b.boardName,
		Slug:          cmp.Or(slug(b.boardName), "homepage"),
		FormatVersion: importer.LayoutVersion,
		Settings:      b.boardSettings,
		Layout:        importer.LayoutDocument{Version: importer.LayoutVersion, Columns: importer.LayoutColumns, Sections: []importer.LayoutSection{}},
	}
	for _, sec := range b.sections {
		ls := importer.LayoutSection{ID: sec.id, Span: sec.span, Items: sec.items}
		if ls.Items == nil {
			ls.Items = []importer.LayoutItem{}
		}
		if !sec.untitled {
			title := sec.title
			ls.Title = &title
		}
		p.Board.Layout.Sections = append(p.Board.Layout.Sections, ls)
	}
	if err := p.Board.Layout.Pack(); err != nil {
		return err
	}
	p.SecretsMoved = len(p.Secrets)
	for _, in := range b.dockerList {
		p.Discovery = append(p.Discovery, importer.Discovery{
			Kind: "label_endpoint", Docker: in.ID, Endpoint: in.Origin.Entry,
			Message: fmt.Sprintf("Containers on %s with homepage.* labels will appear through Docker discovery, in the group their label names.", in.Origin.Entry),
		})
	}
	return nil
}

// -------------------------------------------------------------------- hints

func (b *builder) noteDigest(v string, s importer.Subject) {
	mac := hmac.New(sha256.New, b.hmacKey)
	mac.Write([]byte(v))
	k := string(mac.Sum(nil))
	if i, ok := b.digests[k]; ok {
		b.reused[i] = append(b.reused[i], s)
		return
	}
	b.digests[k] = len(b.reused)
	b.reused = append(b.reused, []importer.Subject{s})
}

func (b *builder) hints() {
	p := &b.res.Plan
	byID := map[string]importer.Instance{}
	for _, in := range p.Instances {
		byID[in.ID] = in
	}

	// Reused secrets, found by keyed hash; the hint names who shares one,
	// never the value or its hash.
	for _, subjects := range b.reused {
		if len(subjects) < 2 {
			continue
		}
		names := make([]string, 0, len(subjects))
		kind := subjects[0].Field
		for _, s := range subjects {
			names = append(names, byID[s.Instance].Name)
			if s.Field != kind {
				kind = "secret"
			}
		}
		if kind != "password" {
			kind = "secret"
		}
		b.res.Hints = append(b.res.Hints, importer.Hint{
			Code: "reused_secret", Severity: "attention", Inbox: true, Subjects: subjects,
			Message: fmt.Sprintf("The same %s is used by %s. Give each service its own.", kind, list(names)),
		})
	}

	// Credentials over plain http://.
	for _, in := range p.Instances {
		u, _ := in.Config["url"].(string)
		if len(in.SecretFields) == 0 || !strings.HasPrefix(strings.ToLower(u), "http://") {
			continue
		}
		host := u
		if pu, err := url.Parse(u); err == nil {
			host = pu.Host
		}
		b.res.Hints = append(b.res.Hints, importer.Hint{
			Code: "plain_http_credentials", Severity: "attention", Inbox: true,
			Subjects: []importer.Subject{{Instance: in.ID, Origin: in.Origin}},
			Message:  fmt.Sprintf("%s sends its credentials over plain http:// to %s. Use https:// if it supports it.", in.Name, host),
		})
	}

	// Docker endpoints: a note, not a warning. Which tier an endpoint is in
	// (raw API, coarse proxy, path-anchored proxy) is only known from the
	// read-only probe the Docker integration runs when it connects.
	for _, in := range b.dockerList {
		ep, _ := in.Config["endpoint"].(string)
		msg := fmt.Sprintf("%s (%s): ", in.Name, ep)
		if strings.HasPrefix(ep, "unix://") {
			msg += "a Docker socket. The hub reads Docker through a read-only socket proxy, never the socket itself; point this instance at a proxy."
		} else {
			msg += "a Docker API over plain TCP. When it connects, Cha Dash runs a safe, read-only probe to tell a raw API from a filtering proxy, and never probes write endpoints."
			if u, err := url.Parse(ep); err == nil && !strings.Contains(u.Hostname(), ".") && net.ParseIP(u.Hostname()) == nil {
				msg += fmt.Sprintf(" %q is a bare host name, probably a compose service, so it only resolves on that compose network.", u.Hostname())
			}
		}
		b.res.Hints = append(b.res.Hints, importer.Hint{
			Code: "docker_endpoint", Severity: "info", Inbox: false, Message: msg,
			Subjects: []importer.Subject{{Instance: in.ID, Origin: in.Origin}},
		})
	}

	if strings.TrimSpace(b.instanceName) != "" {
		b.res.Hints = append(b.res.Hints, importer.Hint{
			Code: "self_host", Severity: "info", Inbox: false,
			Subjects: []importer.Subject{{Origin: importer.Origin{File: settingsFile}}},
			Message: fmt.Sprintf("This config lives on %s (instanceName), so the hub probably runs there too. Cha Dash confirms it once it can see its own container.",
				strings.TrimSpace(b.instanceName)),
		})
	}

	for _, in := range p.Instances {
		if len(in.MissingSecrets) > 0 {
			b.res.Hints = append(b.res.Hints, importer.Hint{
				Code: "missing_secret", Severity: "attention", Inbox: false,
				Subjects: []importer.Subject{{Instance: in.ID, Origin: in.Origin}},
				Message:  fmt.Sprintf("%s has no %s in the Homepage file; set it on the instance in Cha Dash.", in.Name, list(in.MissingSecrets)),
			})
		}
	}
}

func (b *builder) summarize() {
	p := &b.res.Plan
	s := importer.Summary{
		Sections: len(p.Board.Layout.Sections), Tiles: len(p.Tiles), Instances: len(p.Instances),
		Integrations: map[string]int{}, Checks: len(b.checkList), Links: len(p.Links),
		SecretsMoved: p.SecretsMoved, Hints: len(b.res.Hints), Unmapped: len(b.res.Unmapped),
		Discovery: len(p.Discovery), IgnoredFields: len(p.Ignored),
	}
	for _, in := range p.Instances {
		s.Integrations[in.Integration]++
	}
	for _, h := range b.res.Hints {
		if h.Inbox {
			s.InboxHints++
		}
	}
	for _, d := range p.Discovery {
		if d.Kind == "label_section" {
			s.Notes = append(s.Notes, d.Message)
		}
	}
	s.Notes = append(s.Notes, b.notes...)
	s.Notes = append(s.Notes, b.widenedNote...)
	p.Summary = s
	if p.Tiles == nil {
		p.Tiles = []importer.Tile{}
	}
	if p.Links == nil {
		p.Links = []importer.Link{}
	}
	if p.Instances == nil {
		p.Instances = []importer.Instance{}
	}
	if p.Secrets == nil {
		p.Secrets = []importer.SecretRef{}
	}
	if p.Discovery == nil {
		p.Discovery = []importer.Discovery{}
	}
	if p.Ignored == nil {
		p.Ignored = []importer.Ignored{}
	}
	if b.res.Hints == nil {
		b.res.Hints = []importer.Hint{}
	}
	if b.res.Unmapped == nil {
		b.res.Unmapped = []importer.Unmapped{}
	}
}

// ------------------------------------------------------------------ helpers

func (b *builder) unmapped(u importer.Unmapped) { b.res.Unmapped = append(b.res.Unmapped, u) }

// ignore records an ignored key once, collecting the entries it appears in.
func (b *builder) ignore(file, key, where, reason string) {
	k := file + "\x00" + key
	if i, ok := b.ignoredAt[k]; ok {
		if where != "" && !slices.Contains(b.res.Plan.Ignored[i].Entries, where) {
			b.res.Plan.Ignored[i].Entries = append(b.res.Plan.Ignored[i].Entries, where)
		}
		return
	}
	ig := importer.Ignored{File: file, Key: key, Reason: reason}
	if where != "" {
		ig.Entries = []string{where}
	}
	b.ignoredAt[k] = len(b.res.Plan.Ignored)
	b.res.Plan.Ignored = append(b.res.Plan.Ignored, ig)
}

// instanceName2 returns a name unique per integration: the display name, then
// "name (group)", then numbered.
func (b *builder) instanceName2(integration, name, group string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = integration
	}
	k := func(n string) string { return integration + "\x00" + n }
	if b.instNames[k(name)] && group != "" {
		name += " (" + group + ")"
	}
	base := name
	for n := 2; b.instNames[k(name)]; n++ {
		name = fmt.Sprintf("%s %d", base, n)
	}
	b.instNames[k(name)] = true
	return name
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// hashID is a stable id fragment for a source path: re-importing the same
// entry yields the same id, so the import is idempotent.
func hashID(parts ...string) string {
	h := sha256.Sum256([]byte("chadash/import/" + Source + "\x00" + strings.Join(parts, "\x00")))
	return strings.ToLower(b32.EncodeToString(h[:10]))
}

func instanceID(parts ...string) string { return "i_" + hashID(parts...) }

func withOption(m map[string]any, k string, v any) map[string]any {
	if m == nil {
		m = map[string]any{}
	}
	m[k] = v
	return m
}

func keys(ps []pair) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.key)
	}
	return out
}

func mustMapping(d *doc, n *yaml.Node) []pair {
	ps, _ := d.mapping(n)
	return ps
}

func isPlaceholder(v string) bool { return strings.Contains(v, "{{HOMEPAGE_") }

func isHTTP(v string) bool {
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// privateSuffixes are names that only resolve on a LAN, where certificates
// are usually self-signed (Proxmox on :8006, for example).
var privateSuffixes = []string{".lan", ".local", ".localdomain", ".home.arpa", ".internal", ".home", ".localhost", ".intranet", ".corp", ".private"}

// tlsFor picks the TLS policy for an imported URL. Homepage never verified
// certificates. Trust-on-first-use keeps a LAN host's self-signed certificate
// working while still catching a changed one; public names verify normally.
func tlsFor(raw string) map[string]any {
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

// slug lowercases s and keeps ASCII letters and digits, joining runs of
// anything else with a single dash.
func slug(s string) string {
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

// unique returns base, or base-2, base-3, … and marks it used.
func unique(base string, used map[string]bool) string {
	s := base
	for n := 2; used[s]; n++ {
		s = base + "-" + strconv.Itoa(n)
	}
	used[s] = true
	return s
}

func list(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}
