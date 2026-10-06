package stacks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"dash.cha.sh/internal/stackproto"
)

// The stacks root is Dockge's layout: <root>/<stack>/compose.yaml, with an
// optional .env next to it. A stack adopted in place outside the root is
// listed in .chadash/stacks.json, and a shadow copy of its file is what the
// repository versions (.chadash/shadow/<stack>/<file>); its real file stays
// where it is.

// composeNames are the file names a stack directory may use, in the order
// Compose itself prefers them.
var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

const (
	metaDir      = ".chadash"
	registryPath = metaDir + "/stacks.json"
	shadowDir    = metaDir + "/shadow"
)

// nameRE is a compose project name: what a stack can be called.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func checkName(name string) error {
	if !nameRE.MatchString(name) {
		return stackproto.Errorf(stackproto.CodeInvalid, "%q can't be a stack name: use lowercase letters, digits, - and _, starting with a letter or digit.", name)
	}
	return nil
}

// stackRef is where a stack's files are.
type stackRef struct {
	name     string
	location string // stackproto.LocationRoot or LocationExternal
	dir      string // absolute
	file     string // the compose file's name in dir
	tracked  string // the versioned copy's path in the repo
	managed  bool   // in the repo (root) or the registry (external)
	exists   bool   // the compose file is on disk
}

func (s stackRef) path() string { return filepath.Join(s.dir, s.file) }

// registry lists the stacks adopted in place outside the root.
type registry struct {
	Stacks map[string]external `json:"stacks"`
}

type external struct {
	File string `json:"file"` // the compose file's absolute path
}

func (m *Module) readRegistry() (registry, error) {
	reg := registry{Stacks: map[string]external{}}
	b, err := os.ReadFile(filepath.Join(m.root, registryPath))
	if errors.Is(err, os.ErrNotExist) {
		return reg, nil
	}
	if err != nil {
		return reg, err
	}
	if err := json.Unmarshal(b, &reg); err != nil {
		return reg, err
	}
	if reg.Stacks == nil {
		reg.Stacks = map[string]external{}
	}
	return reg, nil
}

func encodeRegistry(reg registry) []byte {
	b, _ := json.MarshalIndent(reg, "", "  ")
	return append(b, '\n')
}

// resolve finds a stack by name: in the registry, then in the root. A name
// that is neither is a new stack in the root (exists false).
func (m *Module) resolve(name string) (stackRef, error) {
	if err := checkName(name); err != nil {
		return stackRef{}, err
	}
	reg, err := m.readRegistry()
	if err != nil {
		return stackRef{}, err
	}
	if ext, ok := reg.Stacks[name]; ok {
		s := stackRef{
			name: name, location: stackproto.LocationExternal, managed: true,
			dir: filepath.Dir(ext.File), file: filepath.Base(ext.File),
		}
		s.tracked = shadowDir + "/" + name + "/" + s.file
		_, err := os.Stat(s.path())
		s.exists = err == nil
		return s, nil
	}
	s := stackRef{name: name, location: stackproto.LocationRoot, dir: filepath.Join(m.root, name), file: composeNames[0]}
	for _, f := range composeNames {
		if st, err := os.Stat(filepath.Join(s.dir, f)); err == nil && st.Mode().IsRegular() {
			s.file, s.exists = f, true
			break
		}
	}
	s.tracked = name + "/" + s.file
	tracked, err := m.repo.tracked()
	if err != nil {
		return stackRef{}, err
	}
	s.managed = tracked[s.tracked]
	return s, nil
}

// rootStacks lists the stack directories in the root: name → ref.
func (m *Module) rootStacks() (map[string]stackRef, error) {
	entries, err := os.ReadDir(m.root)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]stackRef{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]stackRef{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || !nameRE.MatchString(e.Name()) {
			continue
		}
		s, err := m.resolve(e.Name())
		if err != nil {
			return nil, err
		}
		if s.location == stackproto.LocationRoot && (s.exists || s.managed) {
			out[s.name] = s
		}
	}
	return out, nil
}

// managedStacks lists every managed stack, by name.
func (m *Module) managedStacks() ([]stackRef, error) {
	root, err := m.rootStacks()
	if err != nil {
		return nil, err
	}
	reg, err := m.readRegistry()
	if err != nil {
		return nil, err
	}
	var out []stackRef
	for _, s := range root {
		if s.managed {
			out = append(out, s)
		}
	}
	for name := range reg.Stacks {
		if _, clash := root[name]; clash {
			continue
		}
		s, err := m.resolve(name)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b stackRef) int { return strings.Compare(a.name, b.name) })
	return out, nil
}

// relToRoot is path relative to the root, or "" when it's outside it.
func (m *Module) relToRoot(path string) string {
	rel, err := filepath.Rel(m.root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}
