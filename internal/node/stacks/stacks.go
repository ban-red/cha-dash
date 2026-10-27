// Package stacks is the node's stacks module: Cha Dash owns compose stacks
// on the host the node runs on (ADR-0006), and deploys them with the
// Compose SDK and go-git (ADR-0020).
//
// The stacks root (default /opt/stacks) is Dockge's layout,
// <root>/<stack>/compose.yaml plus an optional .env, and a git repository.
// Every change Cha Dash makes is a commit; a deploy goes through seven
// steps (validate, plan, preflight, commit, pull and up, health gate, keep
// or roll back), and a failed one rolls back on its own as a new commit
// pinned to the image digests the previous version last ran. Secrets are
// ${secret:name} references, resolved in memory from values the hub sends
// with the request and never written to disk or git.
//
// The module talks to Docker only through the Docker interface; sdk.go
// implements it with the Compose SDK, and tests use a fake. Only the node
// links this package: the hub speaks to it through internal/stackproto.
// See README.md.
package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/stackproto"
)

// Defaults.
const (
	DefaultRoot         = "/opt/stacks"
	DefaultWait         = 2 * time.Minute
	DefaultSoak         = 20 * time.Second
	DefaultRollbackSoak = 5 * time.Second
	DefaultPoll         = 2 * time.Second
	historyLimit        = 50
)

// DefaultProtectImages mark Cha Dash's own stack: a project running (or
// declaring) one of these images can be viewed and diffed, not deployed,
// until self-update lands (v0.3).
var DefaultProtectImages = []string{"ghcr.io/ban-red/chadash"}

// Options configure the module.
type Options struct {
	// Root is the stacks root. Default DefaultRoot.
	Root   string
	Docker Docker
	// Protected names projects that are view-only, such as the compose
	// project the node itself runs in. ProtectImages adds any project
	// with a matching image (a prefix); nil means DefaultProtectImages.
	Protected     []string
	ProtectImages []string
	// SelfProject names the compose project the node runs in, which is
	// view-only like Protected. It's asked again (at most once a minute)
	// until it answers, so a Docker hiccup at start doesn't leave Cha Dash's
	// own stack deployable. Nil: none.
	SelfProject func(ctx context.Context) (string, error)
	// ProtectContainer reports whether a container is protected by the
	// node's policy (docker.protect, and the hub and the node always): a
	// deploy or rollback that would recreate or remove one is refused. Nil:
	// node.AlwaysProtected only.
	ProtectContainer func(name, service string) bool
	// Guard is what the node refuses or flags in a compose file (guard.go).
	Guard Guard
	// PrivateDirs are the node's own directories on its filesystem (its
	// identity, the pairing volume): a running project whose compose file
	// is under one of them is never read or adopted.
	PrivateDirs []string
	// Wait bounds up --wait; Soak is how long every container must then
	// stay up; RollbackSoak is the same after an automatic rollback (the
	// previous version has proven itself, so it's shorter). Poll is how
	// often the soak looks. A stack's x-chadash overrides the first three.
	Wait, Soak, RollbackSoak, Poll time.Duration
	Log                            *slog.Logger
	// Now and Sleep are for tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Module is the stacks module. It's safe for concurrent use: operations on
// one stack take its lock, and the repository serialises its own writes.
type Module struct {
	root          string
	docker        Docker
	repo          *repo
	protected     []string
	protectImages []string
	selfProject   func(ctx context.Context) (string, error)
	protect       func(name, service string) bool
	guard         Guard
	privateDirs   []string
	wait, soak    time.Duration
	rollbackSoak  time.Duration
	poll          time.Duration
	log           *slog.Logger
	now           func() time.Time
	sleep         func(ctx context.Context, d time.Duration) error

	mu    sync.Mutex
	locks map[string]*sync.Mutex

	selfMu   sync.Mutex
	self     string    // the node's compose project, once known
	selfNext time.Time // when to ask again
}

var _ node.Module = (*Module)(nil)

// New returns the module. It opens the stacks root's repository if there
// is one; the first commit creates it.
func New(o Options) (*Module, error) {
	if o.Docker == nil {
		return nil, errors.New("stacks: Docker is required")
	}
	m := &Module{
		root: o.Root, docker: o.Docker, protected: o.Protected, protectImages: o.ProtectImages,
		selfProject: o.SelfProject, protect: o.ProtectContainer, guard: o.Guard, privateDirs: o.PrivateDirs,
		wait: o.Wait, soak: o.Soak, rollbackSoak: o.RollbackSoak, poll: o.Poll,
		log: o.Log, now: o.Now, sleep: o.Sleep, locks: map[string]*sync.Mutex{},
	}
	if m.root == "" {
		m.root = DefaultRoot
	}
	root, err := filepath.Abs(m.root)
	if err != nil {
		return nil, err
	}
	m.root = root
	if m.protectImages == nil {
		m.protectImages = DefaultProtectImages
	}
	if m.protect == nil {
		m.protect = func(name, service string) bool {
			return node.Policy{}.Protected(name, service)
		}
	}
	if m.wait <= 0 {
		m.wait = DefaultWait
	}
	if m.soak < 0 {
		m.soak = 0
	} else if m.soak == 0 {
		m.soak = DefaultSoak
	}
	if m.rollbackSoak <= 0 {
		m.rollbackSoak = DefaultRollbackSoak
	}
	if m.poll <= 0 {
		m.poll = DefaultPoll
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.sleep == nil {
		m.sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if m.repo, err = openRepo(m.root, m.now); err != nil {
		return nil, err
	}
	return m, nil
}

// Name implements node.Module.
func (m *Module) Name() string { return "stacks" }

// Verbs implements node.Module.
func (m *Module) Verbs() []node.Verb {
	return []node.Verb{
		verb(stackproto.VerbList, stackproto.PermRead, m.list),
		verb(stackproto.VerbDetect, stackproto.PermRead, m.detect),
		verb(stackproto.VerbGet, stackproto.PermRead, m.get),
		verb(stackproto.VerbValidate, stackproto.PermRead, m.validate),
		verb(stackproto.VerbPlan, stackproto.PermRead, m.plan),
		verb(stackproto.VerbDeploy, stackproto.PermDeploy, m.deploy),
		verb(stackproto.VerbRollback, stackproto.PermDeploy, m.rollback),
		verb(stackproto.VerbAdopt, stackproto.PermAdopt, m.adopt),
		verb(stackproto.VerbUnadopt, stackproto.PermAdopt, m.unadopt),
	}
}

// emitter sends progress to the caller, scrubbed of secret values.
type emitter struct {
	fn      func(json.RawMessage)
	secrets map[string]string
}

func (e emitter) send(p stackproto.Progress) {
	if e.fn == nil {
		return
	}
	p.Step, p.Line = scrub(p.Step, e.secrets), scrub(p.Line, e.secrets)
	b, _ := json.Marshal(p)
	e.fn(b)
}

func (e emitter) step(label string) { e.send(stackproto.Progress{Step: label}) }
func (e emitter) line(format string, args ...any) {
	e.send(stackproto.Progress{Line: fmt.Sprintf(format, args...)})
}
func (e emitter) fraction(f float64)                      { e.send(stackproto.Progress{Fraction: &f}) }
func (e emitter) withSecrets(s map[string]string) emitter { e.secrets = s; return e }

func verb[Req, Resp any](name, perm string, fn func(context.Context, Req, emitter) (Resp, error)) node.Verb {
	return node.Verb{Name: name, Policy: perm, Handle: func(ctx context.Context, raw json.RawMessage, progress func(json.RawMessage)) (json.RawMessage, error) {
		var req Req
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, stackproto.Errorf(stackproto.CodeInvalid, "The request doesn't decode: %v.", err)
			}
		}
		resp, err := fn(ctx, req, emitter{fn: progress})
		if err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	}}
}

// scrub replaces every secret value in s with [redacted]. Values shorter
// than four characters are left alone: redacting them would mangle every
// message, and they aren't secrets worth the name.
func scrub(s string, secrets map[string]string) string {
	for _, v := range secrets {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

// lock takes the stack's lock, or refuses: one operation per stack.
func (m *Module) lock(name string) (func(), error) {
	m.mu.Lock()
	l := m.locks[name]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[name] = l
	}
	m.mu.Unlock()
	if !l.TryLock() {
		return nil, stackproto.Errorf(stackproto.CodeBusy, "Another operation is running on %s; try again when it finishes.", name)
	}
	return l.Unlock, nil
}

// ── status ───────────────────────────────────────────────────────────────

func status(cs []Container) (string, int, int) {
	services := map[string]bool{}
	running := 0
	for _, c := range cs {
		services[c.Service] = true
		if c.State == "running" {
			running++
		}
	}
	switch {
	case len(cs) == 0:
		return stackproto.StatusDown, 0, 0
	case running == len(cs):
		return stackproto.StatusRunning, len(services), running
	case running == 0:
		return stackproto.StatusStopped, len(services), 0
	}
	return stackproto.StatusPartial, len(services), running
}

func healthOf(c Container) string {
	switch c.Health {
	case "healthy", "unhealthy", "starting":
		return c.Health
	}
	return "none"
}

// protectedReason says why a stack is view-only, or "".
func (m *Module) protectedReason(name string, contents []string, cs []Container) string {
	const why = "This is Cha Dash's own stack. It can be viewed and diffed here, but deploying it from Cha Dash waits for self-update (v0.3)."
	if slices.Contains(m.protected, name) || (name != "" && m.selfProjectName() == name) {
		return why
	}
	match := func(img string) bool {
		for _, p := range m.protectImages {
			if p != "" && strings.HasPrefix(img, p) {
				return true
			}
		}
		return false
	}
	for _, c := range cs {
		if match(c.Image) {
			return why
		}
	}
	for _, content := range contents {
		for _, img := range imagesOf(content) {
			if match(img) {
				return why
			}
		}
	}
	return ""
}

// selfProjectName is the node's own compose project, asking Docker again
// (at most once a minute) until it knows.
func (m *Module) selfProjectName() string {
	if m.selfProject == nil {
		return ""
	}
	m.selfMu.Lock()
	defer m.selfMu.Unlock()
	if m.self != "" || m.now().Before(m.selfNext) {
		return m.self
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := m.selfProject(ctx)
	if err != nil || p == "" {
		m.selfNext = m.now().Add(time.Minute)
		if err != nil {
			m.log.Warn("stacks: couldn't tell which compose project the node runs in; asking again in a minute", "err", err)
		}
		return ""
	}
	m.self = p
	return p
}

// protectedTouched names the protected containers a plan would recreate or
// remove, or "": docker.protect holds for stack verbs as for container ones.
func (m *Module) protectedTouched(services []stackproto.ServicePlan, running []Container) string {
	touched := map[string]bool{}
	for _, sp := range services {
		if sp.Action == stackproto.ActionRecreate || sp.Action == stackproto.ActionRemove {
			touched[sp.Name] = true
		}
	}
	var hit []string
	for _, c := range running {
		if touched[c.Service] && m.protect(c.Name, c.Service) {
			hit = append(hit, strings.TrimPrefix(c.Name, "/"))
		}
	}
	if len(hit) == 0 {
		return ""
	}
	slices.Sort(hit)
	return fmt.Sprintf("This would recreate or remove %s, which this node's policy protects (docker.protect), so the node refuses it.", strings.Join(slices.Compact(hit), ", "))
}

// privatePath reports whether a file on the node's filesystem is in one of
// its private directories.
func (m *Module) privatePath(path string) bool {
	path = filepath.Clean(path)
	real, err := filepath.EvalSymlinks(path)
	for _, d := range slices.Concat(m.privateDirs, AlwaysPrivate) {
		if d == "" {
			continue
		}
		d = filepath.Clean(d)
		if within(path, d) || err == nil && within(real, d) {
			return true
		}
	}
	return false
}

// readableExternal reports whether the node may read a running project's
// compose file: an absolute .yaml or .yml path outside its own directories.
func (m *Module) readableExternal(f string) bool {
	return filepath.IsAbs(f) && composeFileName(f) && !m.privatePath(f)
}

// composeFileName reports whether a path looks like a compose file. A
// running project's labels name its file, and anything that can start a
// container can set them: the node reads only YAML files it may show.
func composeFileName(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

// imagesOf reads each service's image from a file's text, without loading
// it: service → image.
func imagesOf(content string) map[string]string {
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	out := map[string]string{}
	if yaml.Unmarshal([]byte(content), &doc) != nil {
		return out
	}
	for name, s := range doc.Services {
		if s.Image != "" {
			out[name] = s.Image
		}
	}
	return out
}

// containers asks Docker for a project's containers; a Docker failure is
// logged and reads as none, so the files can still be shown.
func (m *Module) containers(ctx context.Context, name string) ([]Container, bool) {
	cs, err := m.docker.Containers(ctx, name)
	if err != nil {
		m.log.Warn("stacks: couldn't list containers", "stack", name, "err", err)
		return nil, false
	}
	return cs, true
}

// ── stack.list ───────────────────────────────────────────────────────────

func (m *Module) list(ctx context.Context, _ stackproto.ListRequest, _ emitter) (stackproto.ListResponse, error) {
	refs, err := m.managedStacks()
	if err != nil {
		return stackproto.ListResponse{}, err
	}
	out := stackproto.ListResponse{Root: m.root, Stacks: []stackproto.Summary{}}
	for _, s := range refs {
		sum, err := m.summary(ctx, s)
		if err != nil {
			return out, err
		}
		out.Stacks = append(out.Stacks, sum)
	}
	return out, nil
}

func (m *Module) summary(ctx context.Context, s stackRef) (stackproto.Summary, error) {
	sum := stackproto.Summary{Name: s.name, Location: s.location, Path: s.dir, File: s.file}
	cs, ok := m.containers(ctx, s.name)
	sum.Status, sum.Services, sum.Running = status(cs)
	if !ok {
		sum.Status = stackproto.StatusUnknown
	}
	committed, tracked, err := m.repo.file(nil, s.tracked)
	if err != nil {
		return sum, err
	}
	if tracked {
		if disk, err := os.ReadFile(s.path()); err != nil || string(disk) != committed {
			sum.Drift = true
		}
		hist, err := m.repo.history(s.name, s.tracked, 1)
		if err != nil {
			return sum, err
		}
		if len(hist) > 0 {
			c := toCommit(hist[0])
			sum.Head = &c
		}
	}
	sum.Protected = m.protectedReason(s.name, []string{committed}, cs) != ""
	return sum, nil
}

// ── stack.detect ─────────────────────────────────────────────────────────

func (m *Module) detect(ctx context.Context, _ stackproto.ListRequest, _ emitter) (stackproto.DetectResponse, error) {
	out := stackproto.DetectResponse{Projects: []stackproto.Detected{}}
	managed, err := m.managedStacks()
	if err != nil {
		return out, err
	}
	isManaged := map[string]bool{}
	for _, s := range managed {
		isManaged[s.name] = true
	}
	seen := map[string]bool{}
	projects, err := m.docker.Projects(ctx)
	if err != nil {
		return out, fmt.Errorf("list compose projects: %w", err)
	}
	for _, p := range projects {
		if isManaged[p.Name] {
			continue
		}
		seen[p.Name] = true
		cs, _ := m.containers(ctx, p.Name)
		d := stackproto.Detected{Name: p.Name, ConfigFiles: p.ConfigFiles, Path: p.WorkingDir}
		d.Status, d.Services, d.Running = status(cs)
		if d.ConfigFiles == nil {
			d.ConfigFiles = []string{}
		}
		if d.Path == "" && len(p.ConfigFiles) > 0 {
			d.Path = filepath.Dir(p.ConfigFiles[0])
		}
		d.Reason = m.unadoptable(p)
		d.Adoptable = d.Reason == ""
		out.Projects = append(out.Projects, d)
	}
	root, err := m.rootStacks()
	if err != nil {
		return out, err
	}
	for _, name := range sortedKeys(root) {
		s := root[name]
		if s.managed || seen[name] {
			continue
		}
		out.Projects = append(out.Projects, stackproto.Detected{
			Name: name, Status: stackproto.StatusDown, Path: s.dir,
			ConfigFiles: []string{s.path()}, Adoptable: true,
		})
	}
	slices.SortFunc(out.Projects, func(a, b stackproto.Detected) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// unadoptable says why a running project can't be adopted in place, or "".
func (m *Module) unadoptable(p Project) string {
	switch {
	case !nameRE.MatchString(p.Name):
		return "Its project name isn't one Cha Dash can manage."
	case len(p.ConfigFiles) == 0:
		return "Docker doesn't say which compose file started it."
	case len(p.ConfigFiles) > 1:
		return fmt.Sprintf("It uses %d compose files (overrides); the node manages stacks with exactly one.", len(p.ConfigFiles))
	}
	f := p.ConfigFiles[0]
	if !m.readableExternal(f) {
		return "Its compose file isn't one the node will read: it must be a .yaml or .yml file outside the node's own directories."
	}
	if st, err := os.Stat(f); err != nil || !st.Mode().IsRegular() {
		return fmt.Sprintf("The node can't see %s. Mount its directory into chadash-node to adopt it.", f)
	}
	if rel := m.relToRoot(f); rel != "" {
		dir, file, _ := strings.Cut(rel, "/")
		if dir != p.Name || strings.Contains(file, "/") || !slices.Contains(composeNames, file) {
			return "It lives inside the stacks root, but not at <root>/" + p.Name + "/compose.yaml, so it would clash with the root's layout."
		}
	}
	return ""
}

// ── stack.get ────────────────────────────────────────────────────────────

func (m *Module) get(ctx context.Context, req stackproto.GetRequest, _ emitter) (stackproto.Stack, error) {
	s, err := m.resolve(req.Stack)
	if err != nil {
		return stackproto.Stack{}, err
	}
	if !s.managed && !s.exists {
		// A running project outside the root that nobody adopted yet: show
		// it, read-only, so it can be looked at before adopting.
		if ext, ok := m.detectedExternal(ctx, req.Stack); ok {
			s = ext
		} else {
			return stackproto.Stack{}, stackproto.Errorf(stackproto.CodeNotFound, "There's no stack called %s on this node.", req.Stack)
		}
	}
	out := stackproto.Stack{Managed: s.managed, Meta: map[string]any{}, Containers: []stackproto.Service{}, History: []stackproto.Commit{}}
	sum, err := m.summary(ctx, s)
	if err != nil {
		return out, err
	}
	out.Summary = sum
	if s.managed {
		out.Content, _, err = m.repo.file(nil, s.tracked)
		if err != nil {
			return out, err
		}
		hist, err := m.repo.history(s.name, s.tracked, historyLimit)
		if err != nil {
			return out, err
		}
		for _, c := range hist {
			out.History = append(out.History, toCommit(c))
		}
	} else {
		b, err := os.ReadFile(s.path())
		if err != nil {
			return out, err
		}
		if len(b) > maxFile {
			return out, stackproto.Errorf(stackproto.CodeInvalid, "%s is over %d KiB.", s.path(), maxFile>>10)
		}
		out.Content = string(b)
	}
	if req.At != "" {
		c, err := m.repo.commitByHash(req.At)
		if err != nil {
			return out, err
		}
		content, ok, err := m.repo.file(c, s.tracked)
		if err != nil {
			return out, err
		}
		if !ok {
			return out, stackproto.Errorf(stackproto.CodeNotFound, "%s didn't exist at %s.", s.name, c.Hash.String()[:7])
		}
		out.Content = content
	}
	out.Env = envInfo(s.dir)
	out.Meta = metaOf(out.Content).raw
	out.Secrets = secretRefs(out.Content)
	cs, _ := m.containers(ctx, s.name)
	slices.SortFunc(cs, func(a, b Container) int { return strings.Compare(a.Service+a.Name, b.Service+b.Name) })
	for _, c := range cs {
		out.Containers = append(out.Containers, stackproto.Service{Name: c.Service, Image: c.Image, State: c.State, Health: healthOf(c), Restarts: c.RestartCount})
	}
	out.ProtectedReason = m.protectedReason(s.name, []string{out.Content}, cs)
	return out, nil
}

// detectedExternal finds an unadopted running project outside the root.
func (m *Module) detectedExternal(ctx context.Context, name string) (stackRef, bool) {
	ps, err := m.docker.Projects(ctx)
	if err != nil {
		return stackRef{}, false
	}
	for _, p := range ps {
		if p.Name == name && len(p.ConfigFiles) == 1 {
			f := p.ConfigFiles[0]
			if !m.readableExternal(f) {
				return stackRef{}, false
			}
			if _, err := os.Stat(f); err != nil {
				return stackRef{}, false
			}
			return stackRef{name: name, location: stackproto.LocationExternal, dir: filepath.Dir(f), file: filepath.Base(f), exists: true}, true
		}
	}
	return stackRef{}, false
}

// ── stack.validate and stack.plan ────────────────────────────────────────

func (m *Module) validate(ctx context.Context, req stackproto.ValidateRequest, _ emitter) (stackproto.ValidateResponse, error) {
	s, err := m.resolve(req.Stack)
	if err != nil {
		return stackproto.ValidateResponse{}, err
	}
	return validate(ctx, s, req.Content, m.guard), nil
}

func (m *Module) plan(ctx context.Context, req stackproto.PlanRequest, _ emitter) (stackproto.Plan, error) {
	s, err := m.resolve(req.Stack)
	if err != nil {
		return stackproto.Plan{}, err
	}
	p, err := load(ctx, s, req.Content, loadOptions{secrets: secretsOrEmpty(req.Secrets)})
	if err != nil {
		return stackproto.Plan{}, stackproto.Errorf(stackproto.CodeInvalid, "%s", scrub(loadMessage(err), req.Secrets))
	}
	committed, _, err := m.repo.file(nil, s.tracked)
	if err != nil {
		return stackproto.Plan{}, err
	}
	head, err := m.headOf(s)
	if err != nil {
		return stackproto.Plan{}, err
	}
	refused, _, flagged := m.guard.check(p, s.dir)
	if len(refused) > 0 {
		return stackproto.Plan{}, stackproto.Errorf(stackproto.CodeInvalid, "%s", scrub(refused[0].Message, req.Secrets))
	}
	cs, _ := m.containers(ctx, s.name)
	services, err := planServices(p, cs)
	if err != nil {
		return stackproto.Plan{}, err
	}
	if why := m.protectedTouched(services, cs); why != "" {
		return stackproto.Plan{}, stackproto.Errorf(stackproto.CodeProtected, "%s", why)
	}
	pl := planOf(head, req.Content != committed, services)
	pl.RootEquivalent = scrubAll(flagged, req.Secrets)
	return pl, nil
}

// scrubAll scrubs secret values out of each line.
func scrubAll(lines []string, secrets map[string]string) []string {
	var out []string
	for _, l := range lines {
		out = append(out, scrub(l, secrets))
	}
	return out
}

func secretsOrEmpty(s stackproto.Secrets) map[string]string {
	if s == nil {
		return map[string]string{}
	}
	return s
}

// headOf is the last commit that touched the stack, or "".
func (m *Module) headOf(s stackRef) (string, error) {
	hist, err := m.repo.history(s.name, s.tracked, 1)
	if err != nil || len(hist) == 0 {
		return "", err
	}
	return hist[0].Hash.String(), nil
}

// ── stack.adopt and stack.unadopt ────────────────────────────────────────

func (m *Module) adopt(ctx context.Context, req stackproto.AdoptRequest, _ emitter) (stackproto.AdoptResult, error) {
	unlock, err := m.lock(req.Stack)
	if err != nil {
		return stackproto.AdoptResult{}, err
	}
	defer unlock()
	s, err := m.resolve(req.Stack)
	if err != nil {
		return stackproto.AdoptResult{}, err
	}
	trailers := func(kind string) [][2]string {
		return [][2]string{{trailerKind, kind}, {trailerStack, s.name}, {trailerActor, actorOf(req.By)}}
	}
	if s.managed {
		committed, _, err := m.repo.file(nil, s.tracked)
		if err != nil {
			return stackproto.AdoptResult{}, err
		}
		disk, err := os.ReadFile(s.path())
		if err != nil {
			return stackproto.AdoptResult{}, stackproto.Errorf(stackproto.CodeInvalid, "%s is missing on disk, so there are no edits to accept.", s.path())
		}
		if string(disk) == committed {
			return stackproto.AdoptResult{}, stackproto.Errorf(stackproto.CodeExists, "%s is already managed by Cha Dash.", s.name)
		}
		h, err := m.repo.commit(change{writes: map[string][]byte{s.tracked: disk}},
			fmt.Sprintf("Accept edits to %s made outside Cha Dash%s", s.name, by(req.By, req.Via)), trailers(stackproto.KindAccept))
		if err != nil {
			return stackproto.AdoptResult{}, err
		}
		return stackproto.AdoptResult{Commit: h.String(), Kind: stackproto.KindAccept, Location: s.location, Path: s.dir}, nil
	}
	if s.exists {
		// A directory in the root nobody adopted: version it where it is.
		disk, err := os.ReadFile(s.path())
		if err != nil {
			return stackproto.AdoptResult{}, err
		}
		h, err := m.repo.commit(change{writes: map[string][]byte{s.tracked: disk}},
			fmt.Sprintf("Adopt %s in place%s", s.name, by(req.By, req.Via)), trailers(stackproto.KindAdopt))
		if err != nil {
			return stackproto.AdoptResult{}, err
		}
		return stackproto.AdoptResult{Commit: h.String(), Kind: stackproto.KindAdopt, Location: s.location, Path: s.dir}, nil
	}
	// A running project elsewhere: register its path and start a shadow
	// history. Nothing moves and nothing restarts.
	ps, err := m.docker.Projects(ctx)
	if err != nil {
		return stackproto.AdoptResult{}, fmt.Errorf("list compose projects: %w", err)
	}
	idx := slices.IndexFunc(ps, func(p Project) bool { return p.Name == s.name })
	if idx < 0 {
		return stackproto.AdoptResult{}, stackproto.Errorf(stackproto.CodeNotFound, "There's no compose project called %s on this node.", s.name)
	}
	if why := m.unadoptable(ps[idx]); why != "" {
		return stackproto.AdoptResult{}, stackproto.Errorf(stackproto.CodeUnadoptable, "%s", why)
	}
	file := ps[idx].ConfigFiles[0]
	disk, err := os.ReadFile(file)
	if err != nil {
		return stackproto.AdoptResult{}, err
	}
	reg, err := m.readRegistry()
	if err != nil {
		return stackproto.AdoptResult{}, err
	}
	reg.Stacks[s.name] = external{File: file}
	shadow := shadowDir + "/" + s.name + "/" + filepath.Base(file)
	regBytes := encodeRegistry(reg)
	if err := m.writeRootFile(registryPath, regBytes); err != nil {
		return stackproto.AdoptResult{}, err
	}
	if err := m.writeRootFile(shadow, disk); err != nil {
		return stackproto.AdoptResult{}, err
	}
	h, err := m.repo.commit(change{writes: map[string][]byte{registryPath: regBytes, shadow: disk}},
		fmt.Sprintf("Adopt %s in place at %s%s", s.name, filepath.Dir(file), by(req.By, req.Via)), trailers(stackproto.KindAdopt))
	if err != nil {
		return stackproto.AdoptResult{}, err
	}
	return stackproto.AdoptResult{Commit: h.String(), Kind: stackproto.KindAdopt, Location: stackproto.LocationExternal, Path: filepath.Dir(file)}, nil
}

func (m *Module) unadopt(_ context.Context, req stackproto.UnadoptRequest, _ emitter) (stackproto.UnadoptResult, error) {
	unlock, err := m.lock(req.Stack)
	if err != nil {
		return stackproto.UnadoptResult{}, err
	}
	defer unlock()
	s, err := m.resolve(req.Stack)
	if err != nil {
		return stackproto.UnadoptResult{}, err
	}
	if !s.managed {
		return stackproto.UnadoptResult{}, stackproto.Errorf(stackproto.CodeNotManaged, "%s isn't managed by Cha Dash.", s.name)
	}
	c := change{drops: []string{s.tracked}, writes: map[string][]byte{}}
	if s.location == stackproto.LocationExternal {
		reg, err := m.readRegistry()
		if err != nil {
			return stackproto.UnadoptResult{}, err
		}
		delete(reg.Stacks, s.name)
		regBytes := encodeRegistry(reg)
		if err := m.writeRootFile(registryPath, regBytes); err != nil {
			return stackproto.UnadoptResult{}, err
		}
		c.writes[registryPath] = regBytes
		_ = os.Remove(filepath.Join(m.root, filepath.FromSlash(s.tracked)))
		_ = os.Remove(filepath.Dir(filepath.Join(m.root, filepath.FromSlash(s.tracked))))
	}
	h, err := m.repo.commit(c, fmt.Sprintf("Un-adopt %s: its files and containers stay as they are%s", s.name, by(req.By, req.Via)),
		[][2]string{{trailerKind, stackproto.KindUnadopt}, {trailerStack, s.name}, {trailerActor, actorOf(req.By)}})
	if err != nil {
		return stackproto.UnadoptResult{}, err
	}
	return stackproto.UnadoptResult{Commit: h.String()}, nil
}

// writeRootFile writes a file of the module's own under the root. It goes
// through os.Root, so neither a ".." nor a symlink can take it outside.
func (m *Module) writeRootFile(rel string, b []byte) error {
	r, err := os.OpenRoot(m.root)
	if err != nil {
		return err
	}
	defer r.Close()
	name := filepath.FromSlash(rel)
	if err := r.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	return r.WriteFile(name, b, 0o644)
}

// by is " · by sam via web" for a commit subject.
func by(who, via string) string {
	if who == "" {
		return ""
	}
	if via == "" {
		return " · by " + oneLine(who)
	}
	return " · by " + oneLine(who) + " via " + oneLine(via)
}

func actorOf(who string) string {
	if who == "" {
		return "unknown"
	}
	return who
}
