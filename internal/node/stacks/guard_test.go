package stacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/stackproto"
)

func codeOf(err error) string {
	if e := stackproto.ErrorFrom(err); e != nil {
		return e.Code
	}
	return ""
}

func (n *testNode) validate(content string) stackproto.ValidateResponse {
	n.t.Helper()
	var res stackproto.ValidateResponse
	n.must("stack.validate", stackproto.ValidateRequest{Stack: "media", Content: content}, &res)
	return res
}

func hasMessage(res stackproto.ValidateResponse, severity, sub string) bool {
	return slices.ContainsFunc(res.Messages, func(m stackproto.Message) bool {
		return m.Severity == severity && strings.Contains(m.Message, sub)
	})
}

func svc(extra string) string {
	return "services:\n  web:\n    image: nginx:1.27\n" + extra
}

// TestGuardAlwaysRefuses: whatever the policy, a stack can't reach the
// node's own keys to the host (review finding 2).
func TestGuardAlwaysRefuses(t *testing.T) {
	outside := t.TempDir()
	leak := filepath.Join(outside, "node.key")
	if err := os.WriteFile(leak, []byte("NODE_KEY=leaked-private-key-material\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := newTestNode(t, func(o *Options) {
		o.Guard = Guard{PrivatePaths: []string{"/var/lib/docker/volumes/chadash_node/_data", outside}, PrivateVolumes: []string{"chadash_node", "chadash_pairing"}}
	})
	cases := map[string]string{
		"socket":          svc("    volumes: [/var/run/docker.sock:/var/run/docker.sock]\n"),
		"root":            svc("    volumes: [/:/host:ro]\n"),
		"var run":         svc("    volumes: [/var/run:/run/host]\n"),
		"node dir":        svc("    volumes: [/etc/chadash-node/identity:/id]\n"),
		"pairing":         svc("    volumes: [/pairing:/p:ro]\n"),
		"volume source":   svc("    volumes: [/var/lib/docker/volumes:/v]\n"),
		"node volume":     svc("    volumes: [id:/id]\nvolumes:\n  id:\n    external: true\n    name: chadash_node\n"),
		"local bind":      svc("    volumes: [d:/d]\nvolumes:\n  d:\n    driver_opts: {type: none, o: bind, device: /etc/chadash-node}\n"),
		"secret file":     svc("    secrets: [k]\nsecrets:\n  k:\n    file: /pairing/token\n"),
		"volumes_from":    svc("    volumes_from: [\"container:chadash-node\"]\n"),
		"env_file out":    svc("    env_file: [" + leak + "]\n"),
		"env_file up":     svc("    env_file: [../../etc/passwd]\n"),
		"label_file":      svc("    label_file: [" + leak + "]\n"),
		"include":         "include: [" + filepath.Join(outside, "x.yaml") + "]\n" + svc(""),
		"extends file":    "services:\n  web:\n    image: nginx:1.27\n    extends: {file: " + filepath.Join(outside, "base.yaml") + ", service: base}\n",
		"socket by alias": svc("    volumes:\n      - type: bind\n        source: /run/docker.sock\n        target: /s\n"),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			res := n.validate(content)
			if res.OK {
				t.Fatalf("allowed: %+v", res.Messages)
			}
			for _, m := range res.Messages {
				if strings.Contains(m.Message, "leaked-private-key-material") {
					t.Fatalf("the loader read the file it should have refused: %s", m.Message)
				}
			}
			// The deploy refuses it too, before anything runs.
			err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "media", Content: content}, nil)
			if codeOf(err) != stackproto.CodeInvalid {
				t.Fatalf("deploy = %v, want invalid", err)
			}
			if n.docker.Called("up") != 0 {
				t.Fatal("a refused file reached Docker")
			}
		})
	}
	// Files next to the compose file are fine.
	if err := os.MkdirAll(filepath.Join(n.root, "media"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.root, "media", "app.env"), []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := n.validate(svc("    env_file: [app.env]\n    volumes: [./data:/data]\n")); !res.OK {
		t.Fatalf("an env_file and a bind in the stack's directory: %+v", res.Messages)
	}
}

// TestGuardFlagsAndDenies: privileged, host namespaces, devices, cap_add and
// host binds are allowed by default but flagged as root-equivalent in
// validation and the plan; stacks.deny refuses them (review finding 2).
func TestGuardFlagsAndDenies(t *testing.T) {
	risky := svc("    privileged: true\n    network_mode: host\n    cap_add: [NET_ADMIN]\n    devices: [/dev/net/tun:/dev/net/tun]\n    volumes: [/mnt/media:/media:ro]\n")
	n := newTestNode(t)
	res := n.validate(risky)
	if !res.OK {
		t.Fatalf("the default policy refused it: %+v", res.Messages)
	}
	for _, want := range []string{"runs privileged", "host's namespace", "adds capabilities (NET_ADMIN)", "host devices", "host path /mnt/media"} {
		if !hasMessage(res, stackproto.SeverityWarning, want) {
			t.Errorf("no root-equivalent warning for %q in %+v", want, res.Messages)
		}
	}
	var plan stackproto.Plan
	n.must("stack.plan", stackproto.PlanRequest{Stack: "media", Content: risky}, &plan)
	if len(plan.RootEquivalent) != 5 || !strings.HasPrefix(plan.RootEquivalent[0], "web ") {
		t.Fatalf("plan flags %q", plan.RootEquivalent)
	}
	if r := n.deploy("media", risky, nil); r.Outcome != stackproto.OutcomeDeployed || len(r.Plan.RootEquivalent) != 5 {
		t.Fatalf("deploy %+v", r)
	}

	// The policy denies them one by one.
	locked := newTestNode(t, func(o *Options) {
		o.Guard = Guard{Deny: []string{"privileged", "host_namespaces", "devices", "cap_add", "host_binds"}, BindRoots: []string{"/mnt/media"}}
	})
	for feature, content := range map[string]string{
		"privileged":      svc("    privileged: true\n"),
		"unconfined":      svc("    security_opt: [\"seccomp=unconfined\"]\n"),
		"host_namespaces": svc("    pid: host\n"),
		"devices":         svc("    devices: [/dev/sda:/dev/sda]\n"),
		"cap_add":         svc("    cap_add: [SYS_ADMIN]\n"),
		"host_binds":      svc("    volumes: [/srv/backups:/backups]\n"),
	} {
		res := locked.validate(content)
		want := "stacks.deny: " + feature
		if feature == "unconfined" {
			want = "stacks.deny: privileged"
		}
		if res.OK || !hasMessage(res, stackproto.SeverityError, want) {
			t.Errorf("%s: %+v", feature, res.Messages)
		}
		if err := locked.call("stack.plan", stackproto.PlanRequest{Stack: "media", Content: content}, nil); codeOf(err) != stackproto.CodeInvalid {
			t.Errorf("%s: plan = %v", feature, err)
		}
	}
	// Under a bind root (or in the stack's directory) is fine.
	if res := locked.validate(svc("    volumes: [/mnt/media/films:/films, ./cache:/cache]\n")); !res.OK {
		t.Fatalf("binds under bind_roots: %+v", res.Messages)
	}
}

// TestGuardSeesSecretValues: validation sees placeholders, so the deploy
// checks again with the real values (defence in depth).
func TestGuardSeesSecretValues(t *testing.T) {
	n := newTestNode(t)
	content := svc("    volumes: [\"${secret:dir}:/x\"]\n")
	if err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "media", Content: content, Secrets: stackproto.Secrets{"dir": "/etc/chadash-node"}}, nil); codeOf(err) != stackproto.CodeInvalid {
		t.Fatalf("a secret that names a private path = %v", err)
	}
	if n.docker.Called("up") != 0 {
		t.Fatal("it reached Docker")
	}
}

// TestDeployRespectsDockerProtect: a deploy or plan that would recreate or
// remove a container docker.protect names is refused (review finding 5).
func TestDeployRespectsDockerProtect(t *testing.T) {
	protect := false
	n := newTestNode(t, func(o *Options) {
		o.ProtectContainer = func(name, service string) bool { return protect && name == "media-web-1" }
	})
	if r := n.deploy("media", v1, nil); r.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("creating it: %+v", r)
	}
	if r := n.deploy("media", v2Good, nil); r.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("updating it: %+v", r)
	}
	protect = true
	v3 := strings.Replace(v1, "nginx:1.27", "nginx:1.29", 1)
	err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "media", Content: v3}, nil)
	if codeOf(err) != stackproto.CodeProtected || !strings.Contains(err.Error(), "media-web-1") {
		t.Fatalf("recreating a protected container = %v", err)
	}
	if err := n.call("stack.plan", stackproto.PlanRequest{Stack: "media", Content: v3}, nil); codeOf(err) != stackproto.CodeProtected {
		t.Fatalf("planning it = %v", err)
	}
	var st stackproto.Stack
	n.must("stack.get", stackproto.GetRequest{Stack: "media"}, &st)
	if len(st.History) < 2 {
		t.Fatalf("history %+v", st.History)
	}
	if err := n.call("stack.rollback", stackproto.RollbackRequest{Stack: "media", Commit: st.History[1].Commit}, nil); codeOf(err) != stackproto.CodeProtected {
		t.Fatalf("a rollback over a protected container = %v", err)
	}
	if n.docker.Running("media")["web"] != "nginx:1.28" {
		t.Fatalf("running %v", n.docker.Running("media"))
	}
}

func TestDeployRespectsDockerProtectOnRemove(t *testing.T) {
	n := newTestNode(t, func(o *Options) {
		o.ProtectContainer = func(name, service string) bool { return name == "media-web-1" }
	})
	if r := n.deploy("media", v1, nil); r.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("creating it: %+v", r)
	}
	removed := "services:\n  other:\n    image: nginx:1.27\n"
	if err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "media", Content: removed}, nil); codeOf(err) != stackproto.CodeProtected {
		t.Fatalf("removing a protected container = %v", err)
	}
	// Leaving it alone is fine.
	added := strings.Replace(v1, "x-chadash:", "  sidecar:\n    image: busybox:1.37\nx-chadash:", 1)
	if r := n.deploy("media", added, nil); r.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("adding a service beside a protected one: %+v", r)
	}
}

// TestSelfProjectIsAskedAgain: a node that couldn't tell its own project at
// start learns it later, and its stack turns view-only (review finding 5).
func TestSelfProjectIsAskedAgain(t *testing.T) {
	calls := 0
	n := newTestNode(t, func(o *Options) {
		o.SelfProject = func(context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.New("docker is starting")
			}
			return "media", nil
		}
	})
	if r := n.deploy("media", v1, nil); r.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("before the node knows: %+v", r)
	}
	// Within the minute it doesn't ask again.
	n.m.selfProjectName()
	if calls != 1 {
		t.Fatalf("asked %d times within a minute", calls)
	}
	n.mu.Lock()
	n.clock = n.clock.Add(time.Minute)
	n.mu.Unlock()
	if err := n.call("stack.deploy", stackproto.DeployRequest{Stack: "media", Content: v2Good}, nil); codeOf(err) != stackproto.CodeProtected {
		t.Fatalf("after it learns = %v", err)
	}
	n.m.selfProjectName()
	if calls != 2 {
		t.Fatalf("asked %d times after it knew", calls)
	}
}

// TestAdoptRefusesPrivateFiles: a running project whose labels point at
// the node's own files is neither read nor adopted (review finding 2, L5).
func TestAdoptRefusesPrivateFiles(t *testing.T) {
	private := t.TempDir()
	key := filepath.Join(private, "identity", "compose.yaml")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("services:\n  x:\n    image: nginx:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "node.key")
	if err := os.WriteFile(other, []byte("not yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := newTestNode(t, func(o *Options) { o.PrivateDirs = []string{private} })
	n.docker.AddProject("sneaky", []string{key})
	n.docker.AddProject("keyfile", []string{other})
	n.docker.AddProject("token", []string{"/pairing/token.yaml"})
	var det stackproto.DetectResponse
	n.must("stack.detect", nil, &det)
	for _, d := range det.Projects {
		if d.Adoptable {
			t.Errorf("%s is adoptable", d.Name)
		}
	}
	for _, name := range []string{"sneaky", "keyfile", "token"} {
		if err := n.call("stack.get", stackproto.GetRequest{Stack: name}, nil); codeOf(err) != stackproto.CodeNotFound {
			t.Errorf("get %s = %v", name, err)
		}
		if err := n.call("stack.adopt", stackproto.AdoptRequest{Stack: name}, nil); codeOf(err) != stackproto.CodeUnadoptable {
			t.Errorf("adopt %s = %v", name, err)
		}
	}
}
