package integration

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// A Recipe says how to create a credential with exactly the access an
// integration needs, in plain steps with commands to copy. The settings form
// shows it beside the integration's fields, and the onboarding wizard links
// to it.
//
// Recipes are registered apart from the integration, so adding one never
// touches the integration's own files: integrations/<id>/recipe.go embeds a
// recipe.md and calls RegisterRecipe from init. The bundled specs keep theirs
// in specs/recipes/<id>.md.
//
// Commands are templates: {{name}} is replaced by the value of the parameter
// called name, as the person fills it in (a user name, the instance's URL).
// The web app does the filling; a value that doesn't match its parameter's
// pattern is never put into a command. Anything else in braces (a Go
// template, say) is left as it is.
//
// On the wire (GET /api/integrations/catalog, each entry's "recipe"):
//
//	{"title", "summary", "access": "read-only"|"scoped"|"full"|"none",
//	 "params": [{"name", "label", "help"?, "default", "pattern", "options"?: [{"label", "value"}], "from"?: "name"|"config.<field>"}],
//	 "steps": [{"title", "blocks": [block]}],
//	 "notes": [block],
//	 "fills": {"<config field>": "<template>"},
//	 "grants": ["<grant name>"]}
//
// where a block is {"kind": "text", "text"} | {"kind": "list", "items", "ordered"?} |
// {"kind": "code", "lang", "code"}. Text may use `code` and **bold**.
type Recipe struct {
	// Title names the credential: "A read-only API token".
	Title string `json:"title"`
	// Summary says in a sentence what it can and can't do.
	Summary string `json:"summary"`
	// Access is the honest label for the best credential the upstream
	// offers: RecipeReadOnly, RecipeScoped, RecipeFull or RecipeNone.
	Access string        `json:"access"`
	Params []RecipeParam `json:"params"`
	Steps  []RecipeStep  `json:"steps"`
	Notes  []RecipeBlock `json:"notes"`
	// Fills are non-secret config fields the recipe determines, as
	// templates (Proxmox's token_id is "{{user}}!{{token}}"), so the form can
	// take them from the generator.
	Fills map[string]string `json:"fills"`
	// Grants are the names Test connection reports for a credential made
	// this way, so the form can compare.
	Grants []string `json:"grants"`
	// Discover says how to recognise the service on a machine the owner
	// names (the onboarding wizard's "check these machines"). Hub-only.
	Discover []DiscoverHint `json:"-"`
}

// Recipe access labels.
const (
	// RecipeReadOnly: the upstream offers a credential that can only read,
	// and the recipe makes exactly that.
	RecipeReadOnly = "read-only"
	// RecipeScoped: the credential is limited, but what Cha Dash needs also
	// lets it change something.
	RecipeScoped = "scoped"
	// RecipeFull: the upstream only has credentials with full access.
	RecipeFull = "full"
	// RecipeNone: no credential at all.
	RecipeNone = "none"
)

// RecipeParam is one input of the generator.
type RecipeParam struct {
	Name  string `json:"name" yaml:"name"`
	Label string `json:"label" yaml:"label"`
	Help  string `json:"help,omitempty" yaml:"help"`
	// Default is the value before the person types one. For a parameter
	// bound to the form (From), it's an example the pattern is checked with.
	Default string `json:"default" yaml:"default"`
	// Pattern is an anchored regular expression (^…$) in the subset Go and
	// JavaScript share. A value is only put into a command when it matches;
	// it defaults to DefaultParamPattern, and must refuse shell
	// metacharacters, quotes and spaces.
	Pattern string `json:"pattern" yaml:"pattern"`
	// Options make the parameter a choice between authored values (which
	// may hold spaces: they're never typed by anyone).
	Options []RecipeOption `json:"options,omitempty" yaml:"options"`
	// From binds the parameter to the form being filled in instead of an
	// input: "config.<field>" is a config field (the instance's URL), "name"
	// the instance's name as a slug (lowercase letters, digits and -).
	From string `json:"from,omitempty" yaml:"from"`
}

// RecipeOption is one choice of a parameter with options.
type RecipeOption struct {
	Label string `json:"label" yaml:"label"`
	Value string `json:"value" yaml:"value"`
}

// RecipeStep is one titled step.
type RecipeStep struct {
	Title  string        `json:"title"`
	Blocks []RecipeBlock `json:"blocks"`
}

// Recipe block kinds.
const (
	BlockText = "text"
	BlockList = "list"
	BlockCode = "code"
)

// RecipeBlock is a paragraph, a list or a command to copy.
type RecipeBlock struct {
	Kind    string   `json:"kind"`
	Text    string   `json:"text,omitempty"`
	Items   []string `json:"items,omitempty"`
	Ordered bool     `json:"ordered,omitempty"`
	Lang    string   `json:"lang,omitempty"` // sh, yaml, url, text
	Code    string   `json:"code,omitempty"`
}

// DiscoverHint recognises a service from one unauthenticated GET:
// <scheme>://<host>:<port><path> answers with a body containing Match
// (case-insensitively). A match proposes {"url": "<scheme>://<host>[:<port>]"},
// with tls mode tofu for https.
type DiscoverHint struct {
	Scheme string `json:"scheme" yaml:"scheme"`
	Port   int    `json:"port" yaml:"port"`
	Path   string `json:"path" yaml:"path"`
	Match  string `json:"match" yaml:"match"`
}

// DefaultParamPattern is a parameter's pattern when it doesn't give one: a
// name of letters, digits and . _ @ -, which is safe anywhere in a command.
const DefaultParamPattern = `^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`

var (
	placeholderRE = regexp.MustCompile(`\{\{([a-z][a-z0-9_]*)\}\}`)
	paramNameRE   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	fieldRE       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	orderedRE     = regexp.MustCompile(`^[0-9]+\. `)
)

// unsafeInCommand are characters no typed value may carry into a command:
// shell metacharacters, quotes, whitespace and globs.
const unsafeInCommand = " \t\n;&|$`'\"\\<>(){}*?!#~"

// Placeholders lists the parameter names a template uses, in order of first use.
func Placeholders(s string) []string {
	var out []string
	for _, m := range placeholderRE.FindAllStringSubmatch(s, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// Render fills a template with values; a placeholder without a value stays.
func Render(s string, values map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := values[m[2:len(m)-2]]; ok {
			return v
		}
		return m
	})
}

// templates is every template text in the recipe.
func (r Recipe) templates() []string {
	var out []string
	add := func(bs []RecipeBlock) {
		for _, b := range bs {
			out = append(out, b.Text, b.Code)
			out = append(out, b.Items...)
		}
	}
	for _, s := range r.Steps {
		out = append(out, s.Title)
		add(s.Blocks)
	}
	add(r.Notes)
	for _, k := range sortedKeys(r.Fills) {
		out = append(out, r.Fills[k])
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Check validates a recipe the way RegisterRecipe does: labels and steps
// present, every parameter safe and used, every placeholder declared.
func (r Recipe) Check() error {
	switch {
	case strings.TrimSpace(r.Title) == "":
		return errors.New("needs a title")
	case strings.TrimSpace(r.Summary) == "":
		return errors.New("needs a summary")
	case len(r.Steps) == 0:
		return errors.New("needs at least one step")
	}
	switch r.Access {
	case RecipeReadOnly, RecipeScoped, RecipeFull, RecipeNone:
	default:
		return fmt.Errorf("access %q isn't read-only, scoped, full or none", r.Access)
	}
	declared := map[string]bool{}
	for _, p := range r.Params {
		if err := p.check(); err != nil {
			return fmt.Errorf("param %q: %w", p.Name, err)
		}
		if declared[p.Name] {
			return fmt.Errorf("param %q is declared twice", p.Name)
		}
		declared[p.Name] = true
	}
	for i, s := range r.Steps {
		if strings.TrimSpace(s.Title) == "" || len(s.Blocks) == 0 {
			return fmt.Errorf("step %d needs a title and something under it", i+1)
		}
		for _, b := range s.Blocks {
			if err := b.check(); err != nil {
				return fmt.Errorf("step %q: %w", s.Title, err)
			}
		}
	}
	for _, b := range r.Notes {
		if err := b.check(); err != nil {
			return fmt.Errorf("notes: %w", err)
		}
	}
	for k := range r.Fills {
		if !fieldRE.MatchString(k) {
			return fmt.Errorf("fills: %q isn't a config field name", k)
		}
	}
	used := map[string]bool{}
	for _, t := range r.templates() {
		for _, name := range Placeholders(t) {
			if !declared[name] {
				return fmt.Errorf("{{%s}} isn't a declared param", name)
			}
			used[name] = true
		}
	}
	for _, p := range r.Params {
		if !used[p.Name] {
			return fmt.Errorf("param %q is never used", p.Name)
		}
	}
	for _, g := range r.Grants {
		if strings.TrimSpace(g) == "" {
			return errors.New("grants: an empty name")
		}
	}
	for _, h := range r.Discover {
		if err := h.check(); err != nil {
			return fmt.Errorf("discover: %w", err)
		}
	}
	return nil
}

func (p RecipeParam) check() error {
	if !paramNameRE.MatchString(p.Name) {
		return fmt.Errorf("the name must match %s", paramNameRE)
	}
	if strings.TrimSpace(p.Label) == "" {
		return errors.New("needs a label")
	}
	if p.From != "" {
		if p.From != "name" && (!strings.HasPrefix(p.From, "config.") || !fieldRE.MatchString(strings.TrimPrefix(p.From, "config."))) {
			return fmt.Errorf("from %q must be name or config.<field>", p.From)
		}
		if len(p.Options) > 0 {
			return errors.New("a param bound to the form has no options")
		}
	}
	if len(p.Options) > 0 {
		for _, o := range p.Options {
			if strings.TrimSpace(o.Label) == "" || o.Value == "" || strings.ContainsAny(o.Value, "\n`$\\\"'") {
				return fmt.Errorf("option %q needs a label and a plain value", o.Label)
			}
		}
		if !slices.ContainsFunc(p.Options, func(o RecipeOption) bool { return o.Value == p.Default }) {
			return fmt.Errorf("the default %q isn't one of the options", p.Default)
		}
		return nil
	}
	re, err := regexp.Compile(p.Pattern)
	if err != nil {
		return fmt.Errorf("pattern: %w", err)
	}
	if !strings.HasPrefix(p.Pattern, "^") || !strings.HasSuffix(p.Pattern, "$") {
		return errors.New("the pattern must be anchored, ^…$")
	}
	if !re.MatchString(p.Default) {
		return fmt.Errorf("the default %q doesn't match the pattern", p.Default)
	}
	// Whatever is typed, nothing that changes a command gets through.
	sample := p.Default
	for _, c := range unsafeInCommand {
		for _, v := range []string{sample + string(c), string(c) + sample, sample + string(c) + "x"} {
			if re.MatchString(v) {
				return fmt.Errorf("the pattern lets %q through, which can change a command", string(c))
			}
		}
	}
	return nil
}

func (b RecipeBlock) check() error {
	switch b.Kind {
	case BlockText:
		if strings.TrimSpace(b.Text) == "" {
			return errors.New("an empty paragraph")
		}
	case BlockList:
		if len(b.Items) == 0 {
			return errors.New("an empty list")
		}
	case BlockCode:
		if b.Code == "" || b.Lang == "" {
			return errors.New("a code block needs code and a language")
		}
	default:
		return fmt.Errorf("block kind %q isn't text, list or code", b.Kind)
	}
	return nil
}

func (h DiscoverHint) check() error {
	switch {
	case h.Scheme != "http" && h.Scheme != "https":
		return fmt.Errorf("scheme %q isn't http or https", h.Scheme)
	case h.Port < 1 || h.Port > 65535:
		return fmt.Errorf("port %d", h.Port)
	case !strings.HasPrefix(h.Path, "/"):
		return fmt.Errorf("path %q must start with /", h.Path)
	case strings.TrimSpace(h.Match) == "":
		return errors.New("needs a match")
	}
	return nil
}

// recipeFront is a recipe.md's front matter.
type recipeFront struct {
	Title    string            `yaml:"title"`
	Summary  string            `yaml:"summary"`
	Access   string            `yaml:"access"`
	Params   []RecipeParam     `yaml:"params"`
	Fills    map[string]string `yaml:"fills"`
	Grants   []string          `yaml:"grants"`
	Discover []DiscoverHint    `yaml:"discover"`
}

// ParseRecipe reads a recipe written as Markdown: YAML front matter between
// "---" lines (title, summary, access, params, fills, grants, discover),
// then one "## " section per step. A section titled "Notes" holds the notes.
// Under a heading: paragraphs, "- " or "1. " lists (continuation lines
// indented by two spaces), and fenced code blocks with a language. Nothing
// else: no other headings, no HTML, no links.
func ParseRecipe(md []byte) (Recipe, error) {
	s := strings.ReplaceAll(string(md), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return Recipe{}, errors.New("recipe: starts with front matter between --- lines")
	}
	end := strings.Index(s[4:], "\n---\n")
	if end < 0 {
		return Recipe{}, errors.New("recipe: the front matter has no closing ---")
	}
	var front recipeFront
	dec := yaml.NewDecoder(bytes.NewReader([]byte(s[4 : 4+end])))
	dec.KnownFields(true)
	if err := dec.Decode(&front); err != nil {
		return Recipe{}, fmt.Errorf("recipe: front matter: %w", err)
	}
	r := Recipe{
		Title: front.Title, Summary: front.Summary, Access: front.Access,
		Params: front.Params, Fills: front.Fills, Grants: front.Grants, Discover: front.Discover,
	}
	if err := parseRecipeBody(&r, s[4+end+5:]); err != nil {
		return Recipe{}, fmt.Errorf("recipe: %w", err)
	}
	for i := range r.Params {
		if r.Params[i].Pattern == "" && len(r.Params[i].Options) == 0 {
			r.Params[i].Pattern = DefaultParamPattern
		}
		if len(r.Params[i].Options) > 0 && r.Params[i].Default == "" {
			r.Params[i].Default = r.Params[i].Options[0].Value
		}
	}
	r.normalize()
	if err := r.Check(); err != nil {
		return Recipe{}, fmt.Errorf("recipe %q: %w", r.Title, err)
	}
	return r, nil
}

// normalize gives the wire shape fixed types: empty lists and maps, not null.
func (r *Recipe) normalize() {
	if r.Params == nil {
		r.Params = []RecipeParam{}
	}
	if r.Steps == nil {
		r.Steps = []RecipeStep{}
	}
	if r.Notes == nil {
		r.Notes = []RecipeBlock{}
	}
	if r.Fills == nil {
		r.Fills = map[string]string{}
	}
	if r.Grants == nil {
		r.Grants = []string{}
	}
}

func parseRecipeBody(r *Recipe, body string) error {
	var (
		section = -2 // -2: none yet, -1: notes, else the step index
		para    []string
		items   []string
		ordered bool
		inCode  bool
		lang    string
		code    []string
	)
	emit := func(b RecipeBlock) {
		if section == -1 {
			r.Notes = append(r.Notes, b)
		} else {
			r.Steps[section].Blocks = append(r.Steps[section].Blocks, b)
		}
	}
	flush := func() {
		if len(para) > 0 {
			emit(RecipeBlock{Kind: BlockText, Text: strings.Join(para, " ")})
			para = nil
		}
		if len(items) > 0 {
			emit(RecipeBlock{Kind: BlockList, Items: items, Ordered: ordered})
			items = nil
		}
	}
	for n, line := range strings.Split(body, "\n") {
		if inCode {
			if strings.TrimSpace(line) == "```" {
				emit(RecipeBlock{Kind: BlockCode, Lang: lang, Code: strings.Join(code, "\n")})
				inCode, code = false, nil
				continue
			}
			code = append(code, line)
			continue
		}
		trimmed := strings.TrimSpace(line)
		if section == -2 && trimmed != "" && !strings.HasPrefix(line, "## ") {
			return fmt.Errorf("line %d: everything goes under a ## heading", n+1)
		}
		switch {
		case strings.HasPrefix(line, "## "):
			flush()
			title := strings.TrimSpace(line[3:])
			if strings.EqualFold(title, "notes") {
				if section == -1 || len(r.Notes) > 0 {
					return fmt.Errorf("line %d: a second Notes section", n+1)
				}
				section = -1
				continue
			}
			if section == -1 {
				return fmt.Errorf("line %d: Notes come last", n+1)
			}
			r.Steps = append(r.Steps, RecipeStep{Title: title})
			section = len(r.Steps) - 1
		case strings.HasPrefix(line, "#"):
			return fmt.Errorf("line %d: only ## headings", n+1)
		case strings.HasPrefix(line, "```"):
			flush()
			inCode, lang = true, strings.TrimSpace(line[3:])
			if lang == "" {
				lang = "text"
			}
		case trimmed == "":
			flush()
		case strings.HasPrefix(line, "- "), orderedRE.MatchString(line):
			o := !strings.HasPrefix(line, "- ")
			if len(para) > 0 || (len(items) > 0 && o != ordered) {
				flush()
			}
			ordered = o
			text := line[2:]
			if o {
				text = orderedRE.ReplaceAllString(line, "")
			}
			items = append(items, strings.TrimSpace(text))
		case strings.HasPrefix(line, "  ") && len(items) > 0:
			items[len(items)-1] += " " + trimmed
		default:
			if len(items) > 0 {
				flush()
			}
			para = append(para, trimmed)
		}
	}
	if inCode {
		return errors.New("a code block isn't closed")
	}
	flush()
	return nil
}

// RegisterRecipe parses a recipe written as Markdown (see ParseRecipe) and
// adds it to Default for the integration id. It panics on a bad recipe or a
// second one for id, like Register: both are programming errors caught at
// startup, in tests. The integration may register before or after.
func RegisterRecipe(id string, md []byte) {
	r, err := ParseRecipe(md)
	if err != nil {
		panic(fmt.Errorf("integration %q: %w", id, err))
	}
	if err := Default.AddRecipe(id, r); err != nil {
		panic(err)
	}
}

// AddRecipe adds a recipe for the integration id after checking it.
func (r *Registry) AddRecipe(id string, rc Recipe) error {
	if !idRE.MatchString(id) {
		return fmt.Errorf("recipe: id %q must match %s", id, idRE)
	}
	rc.normalize()
	if err := rc.Check(); err != nil {
		return fmt.Errorf("integration %q: recipe: %w", id, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.recipes[id]; dup {
		return fmt.Errorf("%w: a recipe for %s", ErrDuplicate, id)
	}
	r.recipes[id] = rc
	return nil
}

// Recipe returns the recipe for the integration id, if it has one.
func (r *Registry) Recipe(id string) (Recipe, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rc, ok := r.recipes[id]
	return rc, ok
}
