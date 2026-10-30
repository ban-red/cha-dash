package stacks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/stackproto"
)

// stack.move brings a running project into the root, keeping its name so its
// volumes and networks stay: <root>/<name>/compose.yaml (and its .env),
// committed as an adopt with a Chadash-Moved-From trailer. Nothing runs:
// the first deploy recreates what the plan says. The old file is never
// touched, and an in-place registration for the name is dropped.
//
// The file comes from the node (from_path, which must be the file Docker or
// the registry says the project runs from) or from the hub (content, from an
// importer). A relative path in it would name something else once the file
// sits in the root, so each is made absolute against the directory it came
// from; with supplied content that directory isn't known, and a relative
// path refuses the move.

const trailerMovedFrom = "Chadash-Moved-From"

// movedFromSupplied is the trailer's value for content the hub supplied.
const movedFromSupplied = "content supplied by the hub"

func (m *Module) move(ctx context.Context, req stackproto.MoveRequest, _ emitter) (stackproto.MoveResult, error) {
	out := stackproto.MoveResult{Rewrites: []stackproto.Rewrite{}}
	if err := checkName(req.Stack); err != nil {
		return out, err
	}
	if (req.FromPath == "") == (req.Content == "") {
		return out, stackproto.Errorf(stackproto.CodeInvalid, "Send exactly one of from_path and content.")
	}
	if req.FromPath != "" && req.Env != "" {
		return out, stackproto.Errorf(stackproto.CodeInvalid, "env goes with supplied content only; with from_path the node reads the .env beside the file.")
	}
	unlock, err := m.lock(req.Stack)
	if err != nil {
		return out, err
	}
	defer unlock()
	s, err := m.resolve(req.Stack)
	if err != nil {
		return out, err
	}
	if s.managed && s.location == stackproto.LocationRoot {
		return out, stackproto.Errorf(stackproto.CodeExists, "%s is already managed by Cha Dash in the stacks root.", s.name)
	}
	dir := filepath.Join(m.root, s.name)
	if _, err := os.Lstat(dir); err == nil {
		return out, stackproto.Errorf(stackproto.CodeExists, "%s already exists. Moving never writes over a directory: adopt what's there in place, or move it out of the way first.", dir)
	}

	var content, env, from string
	if req.FromPath != "" {
		file, why, err := m.moveSource(ctx, s)
		if err != nil {
			return out, err
		}
		if why != "" {
			return out, stackproto.Errorf(stackproto.CodeUnadoptable, "%s", why)
		}
		if p := filepath.Clean(req.FromPath); p != file && p != filepath.Dir(file) {
			return out, stackproto.Errorf(stackproto.CodeInvalid, "%s isn't where %s runs from (%s).", req.FromPath, s.name, file)
		}
		if content, err = readLimited(file); err != nil {
			return out, err
		}
		if b, err := readLimited(filepath.Join(filepath.Dir(file), ".env")); err == nil {
			env = b
		} else if !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
		from = file
	} else {
		content, env = req.Content, req.Env
		if len(content) > maxFile || len(env) > maxFile {
			return out, stackproto.Errorf(stackproto.CodeInvalid, "The file is over %d KiB.", maxFile>>10)
		}
	}
	running, _ := m.containers(ctx, s.name)
	if why := m.protectedReason(s.name, []string{content}, running); why != "" {
		return out, stackproto.Errorf(stackproto.CodeProtected, "%s", why)
	}
	origDir := ""
	if from != "" {
		origDir = filepath.Dir(from)
	}
	rewrites, moved, err := rewritePaths(content, origDir)
	if err != nil {
		return out, err
	}
	out.Rewrites, out.Path = rewrites, dir

	// Stage the files where they'll live, so the loader sees the stack as
	// its first deploy will (its directory, its .env). A dry run, or a move
	// that's refused, takes them away again: the directory didn't exist.
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return out, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return out, stackproto.Errorf(stackproto.CodeExists, "%s already exists.", dir)
		}
		return out, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	target := stackRef{name: s.name, location: stackproto.LocationRoot, dir: dir, file: composeNames[0], tracked: s.name + "/" + composeNames[0]}
	if err := os.WriteFile(target.path(), []byte(moved), 0o644); err != nil {
		return out, err
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
			return out, err
		}
	}
	v := validate(ctx, target, moved, m.guard)
	for _, msg := range v.Messages {
		if msg.Severity == stackproto.SeverityError {
			text := msg.Message
			if msg.Line > 0 {
				text = fmt.Sprintf("Line %d: %s", msg.Line, text)
			}
			return out, stackproto.Errorf(stackproto.CodeInvalid, "%s", text)
		}
	}
	p, err := load(ctx, target, moved, loadOptions{})
	if err != nil {
		return out, stackproto.Errorf(stackproto.CodeInvalid, "%s", loadMessage(err))
	}
	refused, _, flagged := m.guard.check(p, dir)
	if len(refused) > 0 {
		return out, stackproto.Errorf(stackproto.CodeInvalid, "%s", refused[0].Message)
	}
	services, err := planServices(p, running)
	if err != nil {
		return out, err
	}
	out.Plan = planOf("", true, services)
	out.Plan.RootEquivalent = flagged
	if req.DryRun {
		return out, nil
	}

	c := change{writes: map[string][]byte{target.tracked: []byte(moved)}}
	if s.managed && s.location == stackproto.LocationExternal {
		reg, err := m.readRegistry()
		if err != nil {
			return out, err
		}
		delete(reg.Stacks, s.name)
		regBytes := encodeRegistry(reg)
		if err := m.writeRootFile(registryPath, regBytes); err != nil {
			return out, err
		}
		c.writes[registryPath] = regBytes
		c.drops = append(c.drops, s.tracked)
	}
	movedFrom := movedFromSupplied
	subject := fmt.Sprintf("Move %s into the stacks root", s.name)
	if from != "" {
		movedFrom = from
		subject = fmt.Sprintf("Move %s into the stacks root from %s", s.name, filepath.Dir(from))
	}
	h, err := m.repo.commit(c, subject+by(req.By, req.Via),
		[][2]string{{trailerKind, stackproto.KindAdopt}, {trailerStack, s.name}, {trailerActor, actorOf(req.By)}, {trailerMovedFrom, movedFrom}})
	if err != nil {
		return out, err
	}
	keep = true
	if c.drops != nil {
		_ = os.Remove(filepath.Join(m.root, filepath.FromSlash(s.tracked)))
		_ = os.Remove(filepath.Dir(filepath.Join(m.root, filepath.FromSlash(s.tracked))))
	}
	out.Commit = h.String()
	return out, nil
}

// moveSource is the compose file a project runs from, as the node knows
// it: the registry's, for a stack adopted in place, or the one Docker's
// labels name. why says why it can't be moved from there.
func (m *Module) moveSource(ctx context.Context, s stackRef) (file, why string, err error) {
	if s.managed && s.location == stackproto.LocationExternal {
		f := s.path()
		if !m.readableExternal(f) {
			return "", "Its compose file isn't one the node will read: it must be a .yaml or .yml file outside the node's own directories.", nil
		}
		if st, err := os.Stat(f); err != nil || !st.Mode().IsRegular() {
			return "", fmt.Sprintf("The node can't see %s any more.", f), nil
		}
		return f, "", nil
	}
	ps, err := m.docker.Projects(ctx)
	if err != nil {
		return "", "", fmt.Errorf("list compose projects: %w", err)
	}
	i := slices.IndexFunc(ps, func(p Project) bool { return p.Name == s.name })
	if i < 0 {
		return "", "", stackproto.Errorf(stackproto.CodeNotFound, "There's no compose project called %s on this node.", s.name)
	}
	if why := m.unadoptable(ps[i]); why != "" {
		return "", why, nil
	}
	return ps[i].ConfigFiles[0], "", nil
}

// moveRefusal says why a running project stack.detect found can't be
// moved into the root, or "".
func (m *Module) moveRefusal(p Project, adoptRefusal string) string {
	if adoptRefusal != "" {
		return adoptRefusal
	}
	if _, err := os.Lstat(filepath.Join(m.root, p.Name)); err == nil {
		return fmt.Sprintf("%s already exists in the stacks root.", p.Name)
	}
	return ""
}

// readLimited reads a file of at most maxFile bytes.
func readLimited(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxFile {
		return "", stackproto.Errorf(stackproto.CodeInvalid, "%s is over %d KiB.", path, maxFile>>10)
	}
	return string(b), nil
}

// movedFrom is where the stack was moved from, while a file is still there:
// the Chadash-Moved-From trailer of its newest adopt commit.
func movedFrom(hist []stackproto.Commit, messages map[string]string) string {
	for _, c := range hist {
		if c.Kind != stackproto.KindAdopt {
			continue
		}
		from := trailer(messages[c.Commit], trailerMovedFrom)
		if !filepath.IsAbs(from) {
			return ""
		}
		if st, err := os.Stat(from); err == nil && st.Mode().IsRegular() {
			return from
		}
		return ""
	}
	return ""
}

// ── rewriting relative paths ─────────────────────────────────────────────

// pathRef is one scalar in a compose file that names a host path.
type pathRef struct {
	node *yaml.Node
	// prefix is the part of the scalar that is the path (a short volume's
	// source); the rest stays as written.
	prefix string
	what   string // "the bind source", "env_file", … for a refusal
	// keep is a relative path that stays relative: an env_file naming the
	// .env that moves with the file.
	keep bool
}

// rewritePaths makes each relative host path in content absolute against
// dir, the directory the file came from: bind sources (short and long
// syntax), env_file, build.context, and the files of top-level configs and
// secrets. Only those scalars change; the rest of the file stays as
// written. With dir "" (supplied content), a relative path refuses the
// move and names its line.
func rewritePaths(content, dir string) ([]stackproto.Rewrite, string, error) {
	rewrites := []stackproto.Rewrite{}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, "", stackproto.Errorf(stackproto.CodeInvalid, "%s", yamlMessage(err))
	}
	refs := relativePaths(&doc)
	if len(refs) == 0 {
		return rewrites, content, nil
	}
	if dir == "" {
		var named []string
		for _, r := range refs {
			if !r.keep {
				named = append(named, fmt.Sprintf("line %d (%s %s)", r.node.Line, r.what, r.prefix))
			}
		}
		if len(named) > 0 {
			return nil, "", stackproto.Errorf(stackproto.CodeInvalid,
				"The file uses relative paths, and where it came from isn't known, so they can't be made absolute: %s. Make them absolute, or move it from its file on the node.",
				strings.Join(named, ", "))
		}
		return rewrites, content, nil
	}
	lines := strings.SplitAfter(content, "\n")
	// Right to left on each line, so a rewrite doesn't shift the next one.
	slices.SortFunc(refs, func(a, b pathRef) int {
		if a.node.Line != b.node.Line {
			return a.node.Line - b.node.Line
		}
		return b.node.Column - a.node.Column
	})
	for _, r := range refs {
		if r.keep {
			continue
		}
		abs := filepath.Join(dir, r.prefix)
		old := r.node.Value
		updated := abs + old[len(r.prefix):]
		ln, col := r.node.Line-1, r.node.Column-1
		if ln < 0 || ln >= len(lines) || col < 0 || col > len(lines[ln]) {
			return nil, "", stackproto.Errorf(stackproto.CodeInvalid, "Line %d's path (%s) couldn't be rewritten.", r.node.Line, old)
		}
		line, rest := lines[ln], lines[ln][col:]
		switch {
		case r.node.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0 && len(rest) > 0 && (rest[0] == '"' || rest[0] == '\'') && strings.HasPrefix(rest[1:], old+rest[:1]):
			q := rest[:1]
			lines[ln] = line[:col] + q + updated + q + rest[len(old)+2:]
		case r.node.Style == 0 && strings.HasPrefix(rest, old):
			lines[ln] = line[:col] + updated + rest[len(old):]
		default:
			return nil, "", stackproto.Errorf(stackproto.CodeInvalid, "Line %d's path (%s) isn't written in a way the node can rewrite; make it absolute.", r.node.Line, old)
		}
		rewrites = append(rewrites, stackproto.Rewrite{Line: r.node.Line, From: old, To: updated})
	}
	slices.SortFunc(rewrites, func(a, b stackproto.Rewrite) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.From, b.From)
	})
	return rewrites, strings.Join(lines, ""), nil
}

// relativePaths finds the scalars that name relative host paths.
func relativePaths(doc *yaml.Node) []pathRef {
	var out []pathRef
	add := func(n *yaml.Node, prefix, what string) {
		if n == nil || n.Kind != yaml.ScalarNode || !relativePath(prefix) {
			return
		}
		out = append(out, pathRef{node: n, prefix: prefix, what: what})
	}
	if svcs := mapValue(doc, "services"); svcs != nil && svcs.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(svcs.Content); i += 2 {
			svc := svcs.Content[i+1]
			if svc.Kind != yaml.MappingNode {
				continue
			}
			if vols := childValue(svc, "volumes"); vols != nil && vols.Kind == yaml.SequenceNode {
				for _, v := range vols.Content {
					switch v.Kind {
					case yaml.ScalarNode:
						src, _, ok := strings.Cut(v.Value, ":")
						// A short volume is a bind only when its source looks like a
						// path; anything else names a volume.
						if ok && (src == "." || src == ".." || strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../")) {
							add(v, src, "the bind source")
						}
					case yaml.MappingNode:
						if t := childValue(v, "type"); t != nil && t.Value == "bind" {
							if src := childValue(v, "source"); src != nil {
								add(src, src.Value, "the bind source")
							}
						}
					}
				}
			}
			if ef := childValue(svc, "env_file"); ef != nil {
				var nodes []*yaml.Node
				switch ef.Kind {
				case yaml.ScalarNode:
					nodes = append(nodes, ef)
				case yaml.SequenceNode:
					for _, e := range ef.Content {
						if e.Kind == yaml.MappingNode {
							e = childValue(e, "path")
						}
						if e != nil {
							nodes = append(nodes, e)
						}
					}
				}
				for _, n := range nodes {
					if n.Kind != yaml.ScalarNode || !relativePath(n.Value) {
						continue
					}
					// The .env beside the file moves with it, so a reference to
					// it stays relative and keeps working (and stays inside the
					// stack's directory, where the node reads env files).
					keep := filepath.Clean(n.Value) == ".env"
					out = append(out, pathRef{node: n, prefix: n.Value, what: "env_file", keep: keep})
				}
			}
			if b := childValue(svc, "build"); b != nil {
				switch b.Kind {
				case yaml.ScalarNode:
					if !remoteContext(b.Value) {
						add(b, b.Value, "build")
					}
				case yaml.MappingNode:
					if c := childValue(b, "context"); c != nil && !remoteContext(c.Value) {
						add(c, c.Value, "build.context")
					}
				}
			}
		}
	}
	for _, top := range []string{"configs", "secrets"} {
		m := mapValue(doc, top)
		if m == nil || m.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			if e := m.Content[i+1]; e.Kind == yaml.MappingNode {
				if f := childValue(e, "file"); f != nil {
					add(f, f.Value, top+"."+m.Content[i].Value+".file")
				}
			}
		}
	}
	return out
}

// relativePath reports whether p is a relative host path the move can
// rewrite: not absolute, not the home directory, not interpolated.
func relativePath(p string) bool {
	return p != "" && !filepath.IsAbs(p) && !strings.HasPrefix(p, "~") && !strings.Contains(p, "$")
}

// remoteContext reports whether a build context is a URL (git, http)
// rather than a directory.
func remoteContext(c string) bool {
	return strings.Contains(c, "://") || strings.HasPrefix(c, "git@") || strings.HasPrefix(c, "github.com/")
}
