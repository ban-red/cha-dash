package integration

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Registry holds the integrations the hub can run. Integrations add
// themselves to Default from init (the Telegraf pattern); cmd/chadash
// imports them for the side effect.
type Registry struct {
	mu sync.RWMutex
	m  map[string]entry
}

// entry keeps the manifest checked at registration: Check compiles the
// schema's patterns, and Manifest() may build a fresh schema on every call.
type entry struct {
	integ    Integration
	manifest Manifest
}

// Default is the hub's registry.
var Default = NewRegistry()

// Register adds i to Default and panics if its manifest is invalid or its ID
// is taken: both are programming errors that must fail at startup, in tests.
func Register(i Integration) {
	if err := Default.Register(i); err != nil {
		panic(err)
	}
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[string]entry{}} }

// ErrDuplicate is returned by Register for an ID that's already registered.
var ErrDuplicate = errors.New("integration: already registered")

// Register validates i's manifest and adds it.
func (r *Registry) Register(i Integration) error {
	m := i.Manifest()
	if err := ValidateManifest(m); err != nil {
		return err
	}
	if _, ok := i.(Authorizer); ok != (len(m.AuthorizeFields()) > 0) {
		return fmt.Errorf("integration %q: a field marked x-chadash-authorize and implementing Authorizer go together", m.ID)
	}
	if m.Engine == "" {
		m.Engine = EngineGo
	}
	m.Poll = m.Poll.WithDefaults()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.m[m.ID]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicate, m.ID)
	}
	r.m[m.ID] = entry{integ: i, manifest: m}
	return nil
}

// Get returns the integration registered under id and its manifest, as
// checked at registration. Use this manifest, not i.Manifest(), to validate.
func (r *Registry) Get(id string) (Integration, Manifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.m[id]
	return e.integ, e.manifest, ok
}

// Manifests returns every registered manifest, sorted by ID.
func (r *Registry) Manifests() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Manifest, 0, len(r.m))
	for _, e := range r.m {
		out = append(out, e.manifest)
	}
	slices.SortFunc(out, func(a, b Manifest) int { return strings.Compare(a.ID, b.ID) })
	return out
}

var (
	idRE  = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)
	capRE = regexp.MustCompile(`^[a-z]+(\.[a-z][a-z0-9_]*)*$`)
)

// ValidateManifest checks a manifest the way Register does.
func ValidateManifest(m Manifest) error {
	fail := func(format string, a ...any) error {
		return fmt.Errorf("integration %q: %s", m.ID, fmt.Sprintf(format, a...))
	}
	switch {
	case !idRE.MatchString(m.ID):
		return fail("id must match %s", idRE)
	case strings.TrimSpace(m.Name) == "":
		return fail("needs a name")
	case m.Version == "":
		return fail("needs a version")
	case m.ConfigSchema == nil:
		return fail("needs a config schema")
	}
	switch m.Tier {
	case Bronze, Silver, Gold:
	default:
		return fail("tier %q isn't bronze, silver or gold", m.Tier)
	}
	switch m.Engine {
	case "", EngineGo, EngineSpec:
	default:
		return fail("engine %q isn't go or spec", m.Engine)
	}
	for _, c := range m.Capabilities {
		if !capRE.MatchString(string(c)) {
			return fail("capability %q must match %s", c, capRE)
		}
	}
	if err := m.ConfigSchema.Check(); err != nil {
		return fail("%v", err)
	}
	if tls := m.ConfigSchema.Property("tls"); tls != nil && tls.Type != "object" {
		return fail("the tls property must be TLSSchema()")
	}
	return nil
}
