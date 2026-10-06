package node

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"dash.cha.sh/internal/nodeproto"
)

// DefaultPolicyPath is where the policy file lives on the host.
const DefaultPolicyPath = "/etc/chadash-node/policy.yaml"

// Policy is the host owner's local policy (/etc/chadash-node/policy.yaml).
// The node enforces it on every verb, and nothing the hub sends can widen
// it: the hub can only ask, and the node checks.
//
// Every verb names one permission, "<section>.<grant>". The keys:
//
//	docker.read       list and inspect containers, engine version and info, events, stats
//	docker.logs       container logs, streamed
//	docker.lifecycle  start, stop, restart
//	docker.update     pull an image, recreate a container from it
//	docker.remove     remove a container (not by default)
//	docker.exec       exec into a container (not by default; also needs exec: true)
//	stacks.read       list stacks, read compose files, diff, history
//	stacks.deploy     deploy and roll back a stack
//	stacks.adopt      adopt a running compose project in place
//	host.metrics      host CPU, memory, disk (later)
//	host.smart        SMART detail (later; needs CAP_SYS_RAWIO)
//
// Write permissions (everything but docker.read, docker.logs, stacks.read
// and host.*) are refused for protected containers whatever the allow lists
// say: see Protected.
type Policy struct {
	Hub    HubPolicy    `yaml:"hub"`
	Docker DockerPolicy `yaml:"docker"`
	Stacks StacksPolicy `yaml:"stacks"`
	Host   HostPolicy   `yaml:"host"`
	// Exec is the master switch for exec, off by default: docker.exec needs
	// it and "exec" in docker.allow.
	Exec  bool        `yaml:"exec"`
	Relay RelayPolicy `yaml:"relay"`
}

// HubPolicy fixes which hub the node talks to.
type HubPolicy struct {
	// URL overrides CHADASH_NODE_HUB.
	URL string `yaml:"url"`
	// Pin fixes the hub's key ("ed25519:…"). When set, it's the only hub
	// key the node trusts, and pairing tokens can't add another.
	Pin string `yaml:"pin"`
	// PinRotation lets the hub push its next key to the node (default
	// true). False means a hub key rotation needs the owner to edit Pin.
	PinRotation *bool `yaml:"pin_rotation"`
}

// DockerPolicy is the docker section.
type DockerPolicy struct {
	Allow []string `yaml:"allow"`
	// Protect lists containers no write verb may touch, by container name
	// or compose service name. The hub and the node (AlwaysProtected) and
	// the node's own container are protected whatever it says.
	Protect []string `yaml:"protect"`
	// RedactEnv keeps containers' environment values on the node (default
	// true): inspect returns the names with "[redacted]" values.
	RedactEnv *bool `yaml:"redact_env"`
}

// StacksPolicy is the stacks section (the stacks module reads it).
type StacksPolicy struct {
	Root  string   `yaml:"root"`
	Allow []string `yaml:"allow"`
}

// HostPolicy is the host section.
type HostPolicy struct {
	Metrics bool `yaml:"metrics"`
	Smart   bool `yaml:"smart"`
}

// RelayPolicy is the relay section: LAN-only APIs the hub may reach through
// the node. Nothing uses it yet; an empty list is the only valid value.
type RelayPolicy struct {
	Allow []string `yaml:"allow"`
}

// grants are the valid entries of each section's allow list.
var grants = map[string][]string{
	"docker": {"read", "logs", "lifecycle", "update", "exec", "remove"},
	"stacks": {"read", "deploy", "adopt"},
	"host":   {"metrics", "smart"},
}

// readOnly are the permissions that never change anything.
var readOnly = []string{"docker.read", "docker.logs", "stacks.read", "host.metrics", "host.smart"}

// DefaultPolicy is what a node without a policy file runs: Docker reads,
// logs, lifecycle and updates; stacks read, deploy and adopt; the hub and
// the node itself protected; exec and remove off; env redacted.
func DefaultPolicy() Policy {
	t := true
	return Policy{
		Hub: HubPolicy{PinRotation: &t},
		Docker: DockerPolicy{
			Allow:     []string{"read", "logs", "lifecycle", "update"},
			Protect:   []string{},
			RedactEnv: &t,
		},
		Stacks: StacksPolicy{Root: "/opt/stacks", Allow: []string{"read", "deploy", "adopt"}},
	}
}

// LoadPolicy reads the policy file. A missing file is the default policy
// (and missing reports true); a file that exists but doesn't parse, or has
// an unknown key or grant, is an error: the node refuses to start rather
// than guess what the owner meant.
func LoadPolicy(path string) (p Policy, missing bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultPolicy(), true, nil
	}
	if err != nil {
		return Policy{}, false, err
	}
	p, err = ParsePolicy(b)
	return p, false, err
}

// ParsePolicy reads a policy document. A field left out keeps its default;
// one that's present replaces it, so "docker: {allow: [read]}" really means
// read only, and "allow: []" means nothing.
func ParsePolicy(b []byte) (Policy, error) {
	def := DefaultPolicy()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var p Policy
	if err := dec.Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		return Policy{}, fmt.Errorf("policy: %w", err)
	}
	if p.Hub.PinRotation == nil {
		p.Hub.PinRotation = def.Hub.PinRotation
	}
	if p.Docker.Allow == nil {
		p.Docker.Allow = def.Docker.Allow
	}
	if p.Docker.Protect == nil {
		p.Docker.Protect = def.Docker.Protect
	}
	if p.Docker.RedactEnv == nil {
		p.Docker.RedactEnv = def.Docker.RedactEnv
	}
	if p.Stacks.Root == "" {
		p.Stacks.Root = def.Stacks.Root
	}
	if p.Stacks.Allow == nil {
		p.Stacks.Allow = def.Stacks.Allow
	}
	return p, p.Validate()
}

// Validate checks the allow lists and the pin.
func (p Policy) Validate() error {
	var errs []error
	check := func(section string, allow []string) {
		for _, g := range allow {
			if !slices.Contains(grants[section], g) {
				errs = append(errs, fmt.Errorf("policy: %s.allow: unknown grant %q (want one of %s)", section, g, strings.Join(grants[section], ", ")))
			}
		}
	}
	check("docker", p.Docker.Allow)
	check("stacks", p.Stacks.Allow)
	if p.Hub.Pin != "" && !nodeproto.ValidPin(p.Hub.Pin) {
		errs = append(errs, errors.New("policy: hub.pin must be ed25519:<base64url key>"))
	}
	if len(p.Relay.Allow) > 0 {
		errs = append(errs, errors.New("policy: relay isn't supported yet; leave relay.allow empty"))
	}
	for _, n := range p.Docker.Protect {
		if strings.TrimSpace(n) == "" {
			errs = append(errs, errors.New("policy: docker.protect has an empty name"))
		}
	}
	return errors.Join(errs...)
}

// ValidPermission reports whether perm is a permission key ("docker.logs").
func ValidPermission(perm string) bool {
	section, grant, ok := strings.Cut(perm, ".")
	return ok && slices.Contains(grants[section], grant)
}

// Allows reports whether the policy grants perm. Unknown permissions are
// refused.
func (p Policy) Allows(perm string) bool {
	section, grant, ok := strings.Cut(perm, ".")
	if !ok || !slices.Contains(grants[section], grant) {
		return false
	}
	switch section {
	case "docker":
		if grant == "exec" && !p.Exec {
			return false
		}
		return slices.Contains(p.Docker.Allow, grant)
	case "stacks":
		return slices.Contains(p.Stacks.Allow, grant)
	case "host":
		return grant == "metrics" && p.Host.Metrics || grant == "smart" && p.Host.Smart
	}
	return false
}

// IsWrite reports whether perm can change anything.
func IsWrite(perm string) bool { return !slices.Contains(readOnly, perm) }

// RedactEnv reports whether inspect output leaves with the environment's
// values removed.
func (p Policy) RedactEnv() bool { return p.Docker.RedactEnv == nil || *p.Docker.RedactEnv }

// PinRotation reports whether the hub may push its next key.
func (p Policy) PinRotation() bool {
	return p.Hub.Pin == "" && (p.Hub.PinRotation == nil || *p.Hub.PinRotation)
}

// AlwaysProtected are protected under any policy until self-update lands
// (v0.3): Cha Dash never restarts, recreates or removes itself.
var AlwaysProtected = []string{"chadash", "chadash-node"}

// Protected reports whether a container is protected: its name or its
// compose service matches AlwaysProtected or an entry in docker.protect.
func (p Policy) Protected(name, service string) bool {
	name = strings.TrimPrefix(name, "/")
	for _, n := range slices.Concat(AlwaysProtected, p.Docker.Protect) {
		if n == name || service != "" && n == service {
			return true
		}
	}
	return false
}
