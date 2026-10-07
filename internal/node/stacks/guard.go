package stacks

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"dash.cha.sh/internal/stackproto"
)

// Deploying a compose file is root on the node's host: a file can mount the
// host's disk, run privileged or share the host's namespaces, and the node
// runs what the hub sends (ADR-0006). The guard is what the node holds back
// anyway.
//
// Always refused, under any policy: anything that would hand a stack the
// node's own keys to the host. That's binding the Docker socket, "/", the
// node's own directories and volumes or the pairing volume (or any path
// that contains one of them); volumes_from another container (the node's
// identity volume among them); and, checked while loading, env_file and
// label_file outside the stack's directory, include, and extends from
// another file.
//
// Refused when the policy's stacks.deny lists them, and otherwise flagged
// as root-equivalent in validation and the plan: privileged containers and
// switched-off confinement, the host's namespaces, devices, cap_add, and
// host paths outside the stack's directory (or outside stacks.bind_roots).

// Guard configures the checks.
type Guard struct {
	// PrivatePaths are paths no stack may bind or name, as the host sees
	// them and as the node does (when the node runs in a container, the two
	// differ; both are listed). AlwaysPrivate is added to them.
	PrivatePaths []string
	// PrivateVolumes are Docker volumes no stack may mount: the node's own.
	PrivateVolumes []string
	// Deny and BindRoots are the policy's stacks.deny and stacks.bind_roots.
	Deny      []string
	BindRoots []string
}

// AlwaysPrivate are refused on every node: the Docker socket's usual
// paths, and the node's directory and pairing volume as compose.yaml mounts
// them (on a node that runs as a binary, they're host paths too).
var AlwaysPrivate = []string{"/var/run/docker.sock", "/run/docker.sock", "/etc/chadash-node", "/pairing"}

// Features a policy can deny (node.StackDenials).
const (
	featPrivileged = "privileged"
	featNamespaces = "host_namespaces"
	featDevices    = "devices"
	featCapAdd     = "cap_add"
	featHostBinds  = "host_binds"
)

// finding is one thing the guard noticed. Feature "" is always refused.
type finding struct {
	service string
	feature string
	what    string // "binds /mnt/media", for "<service> <what>"
}

// check looks at a loaded project. Refusals are errors: always-refused
// findings and denied features. Warnings are the rest, each root-equivalent
// on the host; flagged says the same briefly ("web runs privileged"), for
// the plan.
func (g Guard) check(p *types.Project, stackDir string) (refused, warnings []stackproto.Message, flagged []string) {
	for _, f := range g.inspect(p, stackDir) {
		subject := f.service
		if subject == "" {
			subject = "The file"
		}
		switch {
		case f.feature == "":
			refused = append(refused, stackproto.Message{Severity: stackproto.SeverityError, Service: f.service,
				Message: fmt.Sprintf("%s %s. The node never allows that, whatever its policy says: it would hand the stack the node's own keys to this host.", subject, f.what)})
		case slices.Contains(g.Deny, f.feature):
			refused = append(refused, stackproto.Message{Severity: stackproto.SeverityError, Service: f.service,
				Message: fmt.Sprintf("%s %s, which this node's policy denies (stacks.deny: %s).", subject, f.what, f.feature)})
		default:
			flagged = append(flagged, subject+" "+f.what)
			warnings = append(warnings, stackproto.Message{Severity: stackproto.SeverityWarning, Service: f.service,
				Message: fmt.Sprintf("%s %s: root-equivalent on this host. Whoever can deploy this file controls the host; a node's policy can deny it (stacks.deny: %s).", subject, f.what, f.feature)})
		}
	}
	return refused, warnings, flagged
}

func (g Guard) private() []string {
	out := []string{}
	for _, p := range slices.Concat(AlwaysPrivate, g.PrivatePaths) {
		if p != "" && filepath.IsAbs(p) {
			out = append(out, filepath.Clean(p))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// touches reports whether a host path is, is inside, or contains a private
// one: binding "/" or "/var/run" exposes the socket as surely as binding it.
func (g Guard) touches(path string) (string, bool) {
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	path = filepath.Clean(path)
	for _, p := range g.private() {
		if within(path, p) || within(p, path) {
			return p, true
		}
	}
	return "", false
}

func (g Guard) inspect(p *types.Project, stackDir string) []finding {
	var out []finding
	add := func(svc, feature, format string, args ...any) {
		out = append(out, finding{service: svc, feature: feature, what: fmt.Sprintf(format, args...)})
	}
	// hostPath files a host path: always refused if it touches a private
	// path, else a host bind unless it's in the stack's own directory or
	// (with host_binds denied) under a bind root.
	hostPath := func(svc, how, path string) {
		if path == "" {
			return
		}
		if !filepath.IsAbs(path) {
			add(svc, featHostBinds, "%s %s, a host path that isn't absolute", how, path)
			return
		}
		if priv, ok := g.touches(path); ok {
			if priv == filepath.Clean(path) {
				add(svc, "", "%s %s, one of the node's private paths", how, path)
			} else {
				add(svc, "", "%s %s, which is or contains the node's private path %s", how, path, priv)
			}
			return
		}
		if stackDir != "" && within(filepath.Clean(path), stackDir) {
			return
		}
		if slices.Contains(g.Deny, featHostBinds) && slices.ContainsFunc(g.BindRoots, func(r string) bool { return within(filepath.Clean(path), filepath.Clean(r)) }) {
			return
		}
		add(svc, featHostBinds, "%s the host path %s", how, path)
	}
	volumeName := func(key string) string {
		if v, ok := p.Volumes[key]; ok && v.Name != "" {
			return v.Name
		}
		return key
	}

	// Top-level volumes, secrets and configs that reach the host.
	for _, key := range sortedKeys(p.Volumes) {
		v := p.Volumes[key]
		if slices.Contains(g.PrivateVolumes, v.Name) || slices.Contains(g.PrivateVolumes, key) && bool(v.External) {
			add("", "", "declares the volume %s, which is the node's own", v.Name)
			continue
		}
		if (v.Driver == "" || v.Driver == "local") && v.DriverOpts["device"] != "" &&
			(v.DriverOpts["type"] == "none" || strings.Contains(v.DriverOpts["o"], "bind")) {
			hostPath("", "declares the volume "+key+" on", v.DriverOpts["device"])
		}
	}
	for _, key := range sortedKeys(p.Secrets) {
		hostPath("", "reads the secret "+key+" from", p.Secrets[key].File)
	}
	for _, key := range sortedKeys(p.Configs) {
		hostPath("", "reads the config "+key+" from", p.Configs[key].File)
	}

	for _, name := range sortedKeys(p.Services) {
		svc := p.Services[name]
		for _, v := range svc.Volumes {
			switch v.Type {
			case types.VolumeTypeBind:
				hostPath(name, "binds", v.Source)
			case types.VolumeTypeVolume:
				if v.Source != "" && slices.Contains(g.PrivateVolumes, volumeName(v.Source)) {
					add(name, "", "mounts the volume %s, which is the node's own", volumeName(v.Source))
				}
			}
		}
		for _, from := range svc.VolumesFrom {
			if c, ok := strings.CutPrefix(from, "container:"); ok {
				add(name, "", "takes the volumes of the container %s (volumes_from), which could be the node's own", strings.Split(c, ":")[0])
			}
		}
		if svc.Privileged {
			add(name, featPrivileged, "runs privileged")
		}
		for _, o := range svc.SecurityOpt {
			lo := strings.ToLower(strings.ReplaceAll(o, "=", ":"))
			if strings.HasSuffix(lo, ":unconfined") || lo == "label:disable" || strings.HasPrefix(lo, "systempaths:unconfined") {
				add(name, featPrivileged, "turns off confinement (security_opt %s)", o)
			}
		}
		for _, ns := range []struct{ key, val string }{
			{"network_mode", svc.NetworkMode}, {"pid", svc.Pid}, {"ipc", svc.Ipc}, {"uts", svc.Uts},
			{"userns_mode", svc.UserNSMode}, {"cgroup", svc.Cgroup},
		} {
			switch {
			case ns.val == "host":
				add(name, featNamespaces, "shares the host's namespace (%s: host)", ns.key)
			case strings.HasPrefix(ns.val, "container:"):
				add(name, featNamespaces, "joins another container's namespace (%s: %s)", ns.key, ns.val)
			}
		}
		if len(svc.Devices) > 0 || len(svc.DeviceCgroupRules) > 0 || len(svc.Gpus) > 0 {
			var ds []string
			for _, d := range svc.Devices {
				ds = append(ds, d.Source)
			}
			if len(ds) == 0 {
				ds = []string{"device rules or GPUs"}
			}
			add(name, featDevices, "gets host devices (%s)", strings.Join(ds, ", "))
		}
		if len(svc.CapAdd) > 0 {
			add(name, featCapAdd, "adds capabilities (%s)", strings.Join(svc.CapAdd, ", "))
		}
	}
	return out
}

// checkFileRefs refuses references the loader would read from the node's
// own filesystem outside the stack's directory, before the loader reads
// them: env_file and label_file (the node reads those and passes their
// contents to the container), include, and extends from another file.
func checkFileRefs(p *types.Project, stackDir string, hasInclude bool) error {
	if hasInclude {
		return fmt.Errorf("include isn't supported: the node deploys one file per stack")
	}
	inDir := func(path string) bool {
		if !filepath.IsAbs(path) {
			path = filepath.Join(stackDir, path)
		}
		path = filepath.Clean(path)
		if !within(path, stackDir) {
			return false
		}
		// A symlink in the directory mustn't lead out of it either.
		if real, err := filepath.EvalSymlinks(path); err == nil {
			if dir, err := filepath.EvalSymlinks(stackDir); err == nil && !within(real, dir) {
				return false
			}
		}
		return true
	}
	for _, name := range sortedKeys(p.Services) {
		svc := p.Services[name]
		if svc.Extends != nil && svc.Extends.File != "" {
			return fmt.Errorf("the service %s extends a service in %s; the node deploys one file per stack, so extend services in this file only", name, svc.Extends.File)
		}
		for _, f := range svc.EnvFiles {
			if !inDir(f.Path) {
				return fmt.Errorf("the service %s has env_file %s, outside the stack's directory; the node only reads env files next to the compose file", name, f.Path)
			}
		}
		for _, f := range svc.LabelFiles {
			if !inDir(f) {
				return fmt.Errorf("the service %s has label_file %s, outside the stack's directory; the node only reads label files next to the compose file", name, f)
			}
		}
	}
	return nil
}
