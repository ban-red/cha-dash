package stacks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/stackproto"
)

// maxFile bounds a compose file.
const maxFile = 256 << 10

// Secret references are ${secret:name}. Compose's own interpolation would
// reject the colon, so the module substitutes them itself, inside each
// string value after the YAML is parsed (never in the text, so a value
// can't inject YAML), with any "$" in the value escaped so Compose doesn't
// interpolate the secret's contents. "$${secret:x}" is a literal, as in
// Compose.

// secretNameRE is a reference's name. The hub reads it from its vault as
// stacks/<name>.
var secretNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const secretOpen = "${secret:"

// placeholder stands in for secrets when validating, so the loader sees a
// plausible value without the node ever holding the real one.
const placeholder = "chadash-secret-placeholder"

// substituteSecrets replaces each reference in s with lookup(name); a
// lookup that fails is an error naming the reference. It returns the names
// it saw, too.
func substituteSecrets(s string, lookup func(name string) (string, bool)) (string, []string, error) {
	if !strings.Contains(s, secretOpen) {
		return s, nil, nil
	}
	var b strings.Builder
	var names []string
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if strings.HasPrefix(s[i:], "$$") {
			b.WriteString("$$") // Compose's escape; it turns it into "$" later
			i += 2
			continue
		}
		if !strings.HasPrefix(s[i:], secretOpen) {
			b.WriteByte('$')
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			return "", nil, fmt.Errorf("a ${secret:…} reference isn't closed with }")
		}
		name := s[i+len(secretOpen) : i+end]
		if !secretNameRE.MatchString(name) {
			return "", nil, fmt.Errorf("${secret:%s} isn't a valid secret name: use letters, digits, '.', '_' and '-', up to 64", clipName(name))
		}
		v, ok := lookup(name)
		if !ok {
			return "", nil, fmt.Errorf("the secret %q wasn't provided", name)
		}
		names = append(names, name)
		b.WriteString(strings.ReplaceAll(v, "$", "$$"))
		i += end + 1
	}
	return b.String(), names, nil
}

func clipName(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

// secretRefs lists the references in a file's text, sorted and unique. It
// only looks: syntax errors come from the loader.
func secretRefs(content string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(content[i:], secretOpen)
		if j < 0 {
			break
		}
		at := i + j
		i = at + len(secretOpen)
		if at > 0 && content[at-1] == '$' && (at < 2 || content[at-2] != '$') {
			continue // $${secret:…} is a literal
		}
		end := strings.IndexByte(content[i:], '}')
		if end < 0 {
			break
		}
		if name := content[i : i+end]; secretNameRE.MatchString(name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// loadOptions say how to load a stack's file.
type loadOptions struct {
	// secrets are the values to substitute; nil means placeholders
	// (validation).
	secrets map[string]string
}

// load parses content as the stack's compose file, as Compose would from
// the stack's directory under the stack's name, with its .env, without
// writing anything to disk. Secrets are substituted in memory.
func load(ctx context.Context, s stackRef, content string, o loadOptions) (*types.Project, error) {
	if len(content) > maxFile {
		return nil, fmt.Errorf("the file is over %d KiB", maxFile>>10)
	}
	env, err := dotEnv(s.dir)
	if err != nil {
		return nil, err
	}
	lookup := func(name string) (string, bool) {
		if o.secrets == nil {
			return placeholder, true
		}
		v, ok := o.secrets[name]
		return v, ok
	}
	var subErr error
	details := func() types.ConfigDetails {
		return types.ConfigDetails{
			WorkingDir:  s.dir,
			ConfigFiles: []types.ConfigFile{{Filename: s.path(), Content: []byte(content)}},
			Environment: maps.Clone(env),
		}
	}
	common := func(lo *loader.Options) {
		lo.SetProjectName(s.name, true)
		next := lo.Interpolate.Substitute
		if next == nil {
			next = template.Substitute
		}
		lo.Interpolate.Substitute = func(v string, m template.Mapping) (string, error) {
			out, _, err := substituteSecrets(v, lookup)
			if err != nil {
				if subErr == nil {
					subErr = err
				}
				return "", err
			}
			return next(out, m)
		}
	}
	// First pass: the model without reading any other file (no env_file,
	// label_file, include or extends), so references that leave the
	// stack's directory are refused before the loader opens them (guard.go).
	pre, err := loader.LoadWithContext(ctx, details(), func(lo *loader.Options) {
		common(lo)
		lo.SkipInclude, lo.SkipExtends = true, true
		lo.SkipResolveEnvironment, lo.SkipResolveLabels = true, true
		lo.SkipConsistencyCheck = true
	})
	if subErr != nil {
		return nil, subErr
	}
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	hasInclude := yaml.Unmarshal([]byte(content), &doc) == nil && hasKey(&doc, "include")
	if err := checkFileRefs(pre, s.dir, hasInclude); err != nil {
		return nil, err
	}
	p, err := loader.LoadWithContext(ctx, details(), func(lo *loader.Options) {
		common(lo)
		lo.SkipResolveEnvironment = false
	})
	if subErr != nil {
		return nil, subErr
	}
	if err != nil {
		return nil, err
	}
	if len(p.ComposeFiles) == 0 {
		p.ComposeFiles = []string{s.path()}
	}
	// The labels Compose's own loader adds, so containers created from this
	// project carry the same ones the CLI would stamp.
	for name, svc := range p.Services {
		svc.CustomLabels = map[string]string{
			api.ProjectLabel:     p.Name,
			api.ServiceLabel:     name,
			api.VersionLabel:     api.ComposeVersion,
			api.WorkingDirLabel:  p.WorkingDir,
			api.ConfigFilesLabel: strings.Join(p.ComposeFiles, ","),
			api.OneoffLabel:      "False",
		}
		p.Services[name] = svc
	}
	return p, nil
}

// dotEnv reads the .env next to the file, if there is one. The node's own
// environment is deliberately left out, so a stack renders the same
// whoever runs the node.
//
// It opens the file with O_NONBLOCK and reads it only if what it opened is
// a regular file, so a FIFO or a device named .env can't hang a deploy or a
// plan while it holds the stack's lock. Unlike stack.move's reads it still
// follows a symlink, as Compose does: a stack's own .env may be one.
func dotEnv(dir string) (map[string]string, error) {
	path := filepath.Join(dir, ".env")
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("the .env next to the file isn't a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFile {
		return nil, fmt.Errorf("the .env next to the file is over %d KiB", maxFile>>10)
	}
	env, err := dotenv.ParseWithLookup(bytes.NewReader(b), nil)
	if err != nil {
		return nil, fmt.Errorf("the .env next to the file doesn't parse: %w", err)
	}
	// As dotenv.Read: a key starting with a digit isn't a variable.
	maps.DeleteFunc(env, func(k, _ string) bool {
		k = strings.TrimSpace(k)
		return k != "" && k[0] >= '0' && k[0] <= '9'
	})
	return env, nil
}

// envInfo describes the .env: present, and its keys (never values).
func envInfo(dir string) stackproto.EnvInfo {
	env, err := dotEnv(dir)
	if err != nil || len(env) == 0 {
		_, statErr := os.Stat(filepath.Join(dir, ".env"))
		return stackproto.EnvInfo{Present: statErr == nil, Keys: []string{}}
	}
	keys := mapsKeys(env)
	slices.Sort(keys)
	return stackproto.EnvInfo{Present: true, Keys: keys}
}

// ── validation ───────────────────────────────────────────────────────────

// validate checks a proposed file: it must parse and load as compose, its
// secret references must be well-formed, x-chadash must make sense, and the
// guard must allow what it does on the host (refusals are errors, the
// root-equivalent rest warnings). It also warns about what the deploy
// pipeline handles poorly.
func validate(ctx context.Context, s stackRef, content string, g Guard) stackproto.ValidateResponse {
	out := stackproto.ValidateResponse{Messages: []stackproto.Message{}, Secrets: secretRefs(content), Meta: map[string]any{}}
	add := func(sev, msg string, line int, svc string) {
		out.Messages = append(out.Messages, stackproto.Message{Severity: sev, Message: msg, Line: line, Service: svc})
	}
	if strings.TrimSpace(content) == "" {
		add(stackproto.SeverityError, "The file is empty.", 0, "")
		return out
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		add(stackproto.SeverityError, yamlMessage(err), yamlLine(err), "")
		return out
	}
	if _, _, err := substituteSecrets(content, func(string) (string, bool) { return placeholder, true }); err != nil {
		add(stackproto.SeverityError, sentence(err.Error()), lineOf(content, secretOpen), "")
	}
	meta, msgs := parseMeta(&doc)
	out.Meta = meta.raw
	out.Messages = append(out.Messages, msgs...)

	p, err := load(ctx, s, content, loadOptions{})
	if err != nil {
		add(stackproto.SeverityError, loadMessage(err), 0, "")
		return out
	}
	if len(p.Services) == 0 {
		add(stackproto.SeverityError, "The file has no services.", 0, "")
	}
	lines := serviceLines(&doc)
	refused, flagged, _ := g.check(p, s.dir)
	for _, m := range slices.Concat(refused, flagged) {
		m.Line = lines[m.Service]
		out.Messages = append(out.Messages, m)
	}
	for _, name := range sortedKeys(p.Services) {
		svc := p.Services[name]
		line := lines[name]
		switch {
		case svc.Build != nil && svc.Image == "":
			add(stackproto.SeverityError, fmt.Sprintf("%s builds its image; the node deploys images only, so give it an image to pull.", name), line, name)
		case svc.Image == "":
			add(stackproto.SeverityError, fmt.Sprintf("%s has no image.", name), line, name)
		case !strings.Contains(lastSegment(svc.Image), ":") && !strings.Contains(svc.Image, "@"):
			add(stackproto.SeverityWarning, fmt.Sprintf("%s's image %s has no tag, so it means :latest and can change under you. Pin a version.", name, svc.Image), line, name)
		case strings.HasSuffix(svc.Image, ":latest"):
			add(stackproto.SeverityWarning, fmt.Sprintf("%s uses :latest, which can change under you. Pin a version.", name), line, name)
		}
		if svc.HealthCheck == nil || svc.HealthCheck.Disable {
			add(stackproto.SeverityInfo, fmt.Sprintf("%s has no healthcheck, so the health gate can only check it keeps running.", name), line, name)
		} else if iv := svc.HealthCheck.Interval; iv != nil && time.Duration(*iv) > 30*time.Second {
			add(stackproto.SeverityInfo, fmt.Sprintf("%s's healthcheck runs every %s, so a failing deploy takes a while to notice. A start_interval helps.", name, time.Duration(*iv)), line, name)
		}
		for key, val := range svc.Environment {
			if val != nil && looksSecret(key) && *val != "" && *val != placeholder && !strings.Contains(*val, placeholder) {
				add(stackproto.SeverityWarning, fmt.Sprintf("%s's %s looks like a secret in plain text. Use ${secret:…} so it stays out of the file and its history.", name, key), line, name)
			}
		}
	}
	if len(p.Profiles) > 0 {
		add(stackproto.SeverityWarning, "profiles aren't supported by the node yet; deploys use the file as it is.", 0, "")
	}
	out.OK = !slices.ContainsFunc(out.Messages, func(m stackproto.Message) bool { return m.Severity == stackproto.SeverityError })
	return out
}

func lastSegment(image string) string {
	if i := strings.LastIndexByte(image, '/'); i >= 0 {
		return image[i+1:]
	}
	return image
}

func looksSecret(key string) bool {
	k := strings.ToUpper(key)
	for _, w := range []string{"PASSWORD", "PASSWD", "SECRET", "TOKEN", "API_KEY", "APIKEY", "PRIVATE_KEY"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

var yamlLineRE = regexp.MustCompile(`line (\d+)`)

func yamlLine(err error) int {
	if m := yamlLineRE.FindStringSubmatch(err.Error()); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func yamlMessage(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	return sentence("The YAML doesn't parse: " + msg)
}

// loadMessage turns a loader error into a sentence. The loader never sees
// secret values here (validation uses placeholders).
func loadMessage(err error) string {
	return sentence(strings.ReplaceAll(err.Error(), "\n", "; "))
}

func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// lineOf is the 1-based line of the first occurrence of sub, or 0.
func lineOf(content, sub string) int {
	before, _, ok := strings.Cut(content, sub)
	if !ok {
		return 0
	}
	return strings.Count(before, "\n") + 1
}

// serviceLines maps each service to the line its key is on.
func serviceLines(doc *yaml.Node) map[string]int {
	out := map[string]int{}
	if svcs := mapValue(doc, "services"); svcs != nil && svcs.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(svcs.Content); i += 2 {
			out[svcs.Content[i].Value] = svcs.Content[i].Line
		}
	}
	return out
}

// mapValue is the value of a top-level key, or nil.
func mapValue(doc *yaml.Node, key string) *yaml.Node {
	n := doc
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func hasKey(doc *yaml.Node, key string) bool { return mapValue(doc, key) != nil }

// ── x-chadash ────────────────────────────────────────────────────────────

// meta is a stack's x-chadash block: Cha Dash's settings for the stack,
// inside the file so it stays valid compose.
//
//	x-chadash:
//	  soak: 20s           # how long every container must stay up after a deploy
//	  rollback_soak: 5s   # the same after an automatic rollback
//	  wait: 2m            # how long up --wait may take
//	  description: Media server
//	  tags: [media]
//	  household: false    # whether household accounts see it
type meta struct {
	raw          map[string]any
	soak         time.Duration
	rollbackSoak time.Duration
	wait         time.Duration
}

var metaKeys = map[string]string{
	"soak": "duration", "rollback_soak": "duration", "wait": "duration",
	"description": "string", "icon": "string", "tags": "list", "household": "bool",
}

func parseMeta(doc *yaml.Node) (meta, []stackproto.Message) {
	m := meta{raw: map[string]any{}}
	var msgs []stackproto.Message
	n := mapValue(doc, "x-chadash")
	if n == nil {
		return m, nil
	}
	warn := func(line int, format string, args ...any) {
		msgs = append(msgs, stackproto.Message{Severity: stackproto.SeverityWarning, Line: line, Message: fmt.Sprintf(format, args...)})
	}
	if n.Kind != yaml.MappingNode {
		warn(n.Line, "x-chadash should be a mapping of settings.")
		return m, msgs
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		kind, known := metaKeys[k.Value]
		if !known {
			warn(k.Line, "x-chadash.%s isn't a setting Cha Dash knows; it's ignored.", k.Value)
			continue
		}
		switch kind {
		case "duration":
			d, err := time.ParseDuration(v.Value)
			if err != nil || d < 0 || d > time.Hour {
				warn(v.Line, "x-chadash.%s should be a duration like 20s or 2m, at most an hour.", k.Value)
				continue
			}
			m.raw[k.Value] = d.String()
			switch k.Value {
			case "soak":
				m.soak = d
			case "rollback_soak":
				m.rollbackSoak = d
			case "wait":
				m.wait = d
			}
		case "bool":
			var b bool
			if err := v.Decode(&b); err != nil {
				warn(v.Line, "x-chadash.%s should be true or false.", k.Value)
				continue
			}
			m.raw[k.Value] = b
		case "list":
			var l []string
			if err := v.Decode(&l); err != nil {
				warn(v.Line, "x-chadash.%s should be a list of words.", k.Value)
				continue
			}
			m.raw[k.Value] = l
		default:
			m.raw[k.Value] = v.Value
		}
	}
	return m, msgs
}

// metaOf parses x-chadash from a file's text, ignoring problems.
func metaOf(content string) meta {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(content), &doc) != nil {
		return meta{raw: map[string]any{}}
	}
	m, _ := parseMeta(&doc)
	return m
}
