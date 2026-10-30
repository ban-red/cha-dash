package node

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
//	stacks.deploy     deploy and roll back a stack (root on this host: see StacksPolicy)
//	stacks.adopt      adopt a running compose project in place, or move it into the root
//	stacks.sync       check and fetch a git remote a stack follows (limited by stacks.remotes)
//	stacks.selfupdate update the hub's own container (not by default; the hub's host only)
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
//
// stacks.deploy runs compose files the hub sends, and a compose file can
// mount the host's disk, run privileged or join the host's namespaces: an
// admin who can deploy is root on this host, by design. Some things are
// refused whatever the policy says (the Docker socket, "/", the node's own
// directories and volumes, the pairing volume, env_file outside the stack's
// directory, include and extends from other files). Deny narrows the rest:
//
//	privileged       privileged: true, or security_opt that turns confinement off
//	host_namespaces  network_mode, pid, ipc, uts, userns_mode or cgroup "host" (or another container's)
//	devices          devices, device_cgroup_rules, gpus
//	cap_add          any cap_add
//	host_binds       host paths outside bind_roots (bind mounts, local-driver volumes, file secrets and configs)
//
// Nothing is denied by default, so stacks that need a VPN's NET_ADMIN or a
// media library's bind mount work; plans flag each of these as
// root-equivalent instead. Removing deploy from allow turns deploying off.
type StacksPolicy struct {
	Root  string   `yaml:"root"`
	Allow []string `yaml:"allow"`
	// Deny lists compose features a deploy may not use (StackDenials).
	Deny []string `yaml:"deny"`
	// BindRoots are the host paths binds may use when host_binds is
	// denied: every host path a stack names must be inside one of them.
	BindRoots []string `yaml:"bind_roots"`
	// Remotes are the git hosts stacks.sync may reach, as host names or
	// host:port ("git.lab.example", "git.lab.example:2222"): no scheme, no
	// path, no user. Empty means any.
	Remotes []string `yaml:"remotes"`
}

// StackDenials are the valid entries of stacks.deny.
var StackDenials = []string{"privileged", "host_namespaces", "devices", "cap_add", "host_binds"}

// Denies reports whether stacks.deny lists feature.
func (s StacksPolicy) Denies(feature string) bool { return slices.Contains(s.Deny, feature) }

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
	"stacks": {"read", "deploy", "adopt", "sync", "selfupdate"},
	"host":   {"metrics", "smart"},
}

// readOnly are the permissions that never change anything.
var readOnly = []string{"docker.read", "docker.logs", "stacks.read", "host.metrics", "host.smart"}

// DefaultPolicy is what a node without a policy file runs: Docker reads,
// logs, lifecycle and updates; stacks read, deploy, adopt and sync, from
// any remote; the hub and the node itself protected; exec, remove and
// selfupdate off; env redacted.
func DefaultPolicy() Policy {
	t := true
	return Policy{
		Hub: HubPolicy{PinRotation: &t},
		Docker: DockerPolicy{
			Allow:     []string{"read", "logs", "lifecycle", "update"},
			Protect:   []string{},
			RedactEnv: &t,
		},
		Stacks: StacksPolicy{Root: "/opt/stacks", Allow: []string{"read", "deploy", "adopt", "sync"}},
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
	for _, d := range p.Stacks.Deny {
		if !slices.Contains(StackDenials, d) {
			errs = append(errs, fmt.Errorf("policy: stacks.deny: unknown feature %q (want one of %s)", d, strings.Join(StackDenials, ", ")))
		}
	}
	for _, r := range p.Stacks.BindRoots {
		if !filepath.IsAbs(r) || filepath.Clean(r) == "/" {
			errs = append(errs, fmt.Errorf("policy: stacks.bind_roots: %q must be an absolute path other than /", r))
		}
	}
	if len(p.Stacks.BindRoots) > 0 && !p.Stacks.Denies("host_binds") {
		errs = append(errs, errors.New("policy: stacks.bind_roots only means something with host_binds in stacks.deny"))
	}
	for _, r := range p.Stacks.Remotes {
		if err := validRemote(r); err != nil {
			errs = append(errs, fmt.Errorf("policy: stacks.remotes: %q %w", r, err))
		}
	}
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

// hostLabelRE is one label of a DNS name.
var hostLabelRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// validRemote checks one stacks.remotes entry: a host name or IP address,
// optionally with a port ("[::1]:22" for IPv6), and nothing else.
func validRemote(r string) error {
	switch {
	case r == "":
		return errors.New("is empty")
	case strings.Contains(r, "://"):
		return errors.New("has a scheme; give the host only")
	case strings.Contains(r, "@"):
		return errors.New("has a user; give the host only")
	case strings.ContainsAny(r, "/?#"):
		return errors.New("has a path; give the host only")
	case strings.ContainsAny(r, " \t\r\n"):
		return errors.New("has a space")
	}
	host := r
	if strings.HasPrefix(r, "[") || strings.Count(r, ":") == 1 {
		if strings.HasPrefix(r, "[") && strings.HasSuffix(r, "]") {
			host = r[1 : len(r)-1]
		} else {
			h, port, err := net.SplitHostPort(r)
			if err != nil {
				return errors.New("isn't host or host:port")
			}
			if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
				return errors.New("has a bad port")
			}
			host = h
		}
		if strings.HasPrefix(r, "[") && !strings.Contains(host, ":") {
			return errors.New("isn't host or host:port")
		}
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	if strings.Contains(host, ":") {
		return errors.New("isn't host or host:port (bracket an IPv6 address)")
	}
	if len(host) > 253 {
		return errors.New("is too long")
	}
	for label := range strings.SplitSeq(strings.TrimSuffix(host, "."), ".") {
		if !hostLabelRE.MatchString(label) {
			return errors.New("isn't a host name")
		}
	}
	return nil
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
