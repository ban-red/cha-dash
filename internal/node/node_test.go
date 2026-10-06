package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/nodeproto"
)

func TestDefaultPolicy(t *testing.T) {
	p, missing, err := LoadPolicy(filepath.Join(t.TempDir(), "policy.yaml"))
	if err != nil || !missing {
		t.Fatalf("missing file: %v %v", missing, err)
	}
	for perm, want := range map[string]bool{
		"docker.read": true, "docker.logs": true, "docker.lifecycle": true, "docker.update": true,
		"docker.remove": false, "docker.exec": false,
		"stacks.read": true, "stacks.deploy": true, "stacks.adopt": true,
		"host.metrics": false, "host.smart": false, "docker.root": false, "nonsense": false, "": false,
	} {
		if p.Allows(perm) != want {
			t.Errorf("default Allows(%q) = %v", perm, !want)
		}
	}
	if !p.RedactEnv() || !p.PinRotation() || p.Stacks.Root != "/opt/stacks" {
		t.Fatalf("defaults: %+v", p)
	}
	for _, name := range []string{"chadash", "chadash-node", "/chadash"} {
		if !p.Protected(name, "") {
			t.Errorf("%s isn't protected", name)
		}
	}
	if !p.Protected("chadash-chadash-1", "chadash") || p.Protected("sonarr", "sonarr") {
		t.Fatal("protect by service")
	}
}

func TestParsePolicy(t *testing.T) {
	k, _ := nodeproto.GenerateKey()
	pin := nodeproto.PinOf(k)
	p, err := ParsePolicy([]byte(`
hub: { url: wss://dash.lab.example/node, pin: "` + pin + `" }
docker:
  allow: [read, logs, lifecycle, update, remove, exec]
  protect: [vaultwarden]
  redact_env: false
stacks: { allow: [read] }
exec: false
`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Allows("docker.exec") {
		t.Error("exec allowed without exec: true")
	}
	if !p.Allows("docker.remove") || p.Allows("stacks.deploy") || p.RedactEnv() || p.PinRotation() {
		t.Errorf("parsed %+v", p)
	}
	if !p.Protected("vaultwarden", "") || !p.Protected("chadash", "") {
		t.Error("protect list")
	}
	if p.Stacks.Root != "/opt/stacks" {
		t.Errorf("root %q", p.Stacks.Root)
	}
	p, err = ParsePolicy([]byte("docker: { allow: [read, exec] }\nexec: true\n"))
	if err != nil || !p.Allows("docker.exec") || p.Allows("docker.logs") {
		t.Fatalf("exec on: %v %+v", err, p)
	}
	p, err = ParsePolicy([]byte("docker: { allow: [] }\n"))
	if err != nil || p.Allows("docker.read") {
		t.Fatalf("empty allow: %v", err)
	}
	for name, doc := range map[string]string{
		"unknown key":     "dockr: {}\n",
		"unknown field":   "docker: { alow: [read] }\n",
		"unknown grant":   "docker: { allow: [root] }\n",
		"stacks grant":    "stacks: { allow: [read, delete] }\n",
		"bad pin":         "hub: { pin: abc }\n",
		"relay":           "relay: { allow: [192.0.2.1] }\n",
		"empty protected": "docker: { protect: [''] }\n",
		"not yaml":        "docker: [",
	} {
		if _, err := ParsePolicy([]byte(doc)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	path := filepath.Join(t.TempDir(), "policy.yaml")
	os.WriteFile(path, []byte("docker: { allow: [rad] }\n"), 0o600)
	if _, missing, err := LoadPolicy(path); err == nil || missing {
		t.Fatal("a bad file loaded")
	}
}

type fakeModule struct{ verbs []Verb }

func (fakeModule) Name() string    { return "fake" }
func (m fakeModule) Verbs() []Verb { return m.verbs }

func handler(name string) func(context.Context, json.RawMessage, func(json.RawMessage)) (json.RawMessage, error) {
	return func(_ context.Context, req json.RawMessage, progress func(json.RawMessage)) (json.RawMessage, error) {
		progress(json.RawMessage(`"working"`))
		return json.RawMessage(`"` + name + `"`), nil
	}
}

// TestEveryWriteVerbRefused builds one verb per permission and checks the
// dispatcher refuses each, before its handler runs, under a read-only
// policy, and runs each under a policy that allows everything.
func TestEveryWriteVerbRefused(t *testing.T) {
	var verbs []Verb
	perms := []string{}
	for section, gs := range grants {
		for _, g := range gs {
			perm := section + "." + g
			perms = append(perms, perm)
			verbs = append(verbs, Verb{Name: "v." + perm, Policy: perm, Handle: handler(perm)})
		}
	}
	slices.Sort(perms)
	readOnlyPolicy, err := ParsePolicy([]byte("docker: { allow: [read] }\nstacks: { allow: [read] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDispatcher(readOnlyPolicy, fakeModule{verbs})
	if err != nil {
		t.Fatal(err)
	}
	for _, perm := range perms {
		_, err := d.Call(t.Context(), "v."+perm, nil, nil)
		allowed := perm == "docker.read" || perm == "stacks.read"
		switch {
		case allowed && err != nil:
			t.Errorf("%s refused: %v", perm, err)
		case !allowed && nodeproto.CodeOf(err) != nodeproto.CodeForbidden:
			t.Errorf("%s = %v, want forbidden", perm, err)
		case !allowed && !strings.Contains(err.Error(), perm):
			t.Errorf("%s: the refusal doesn't name the permission: %v", perm, err)
		}
		if IsWrite(perm) && allowed {
			t.Errorf("%s is a write the read-only policy allowed", perm)
		}
	}
	info := d.Info()
	if len(info) != len(perms) {
		t.Fatalf("info lists %d verbs", len(info))
	}
	all, _ := ParsePolicy([]byte("docker: { allow: [read, logs, lifecycle, update, exec, remove] }\nexec: true\nhost: { metrics: true, smart: true }\n"))
	d, _ = NewDispatcher(all, fakeModule{verbs})
	for _, perm := range perms {
		var items []string
		res, err := d.Call(t.Context(), "v."+perm, nil, func(b json.RawMessage) { items = append(items, string(b)) })
		if err != nil || string(res) != `"`+perm+`"` || len(items) != 1 {
			t.Errorf("%s under allow-all: %s %v %v", perm, res, err, items)
		}
	}
	if _, err := d.Call(t.Context(), "nope", nil, nil); nodeproto.CodeOf(err) != nodeproto.CodeNotFound {
		t.Fatalf("unknown verb = %v", err)
	}
}

func TestDispatcherChecksVerbs(t *testing.T) {
	ok := Verb{Name: "container.list", Policy: "docker.read", Handle: handler("x")}
	for name, verbs := range map[string][]Verb{
		"unknown permission": {{Name: "x", Policy: "docker.root", Handle: handler("x")}},
		"no handler":         {{Name: "x", Policy: "docker.read"}},
		"duplicate":          {ok, ok},
		"built-in":           {{Name: nodeproto.VerbHubPin, Policy: "docker.read", Handle: handler("x")}},
	} {
		if _, err := NewDispatcher(DefaultPolicy(), fakeModule{verbs}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestIdentityStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "identity")
	s, err := OpenIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := s.Current().Pin
	if fi, err := os.Stat(filepath.Join(dir, keyFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode())
	}
	k, _ := nodeproto.GenerateKey()
	if err := s.Update(func(st *State) { st.Node = "n_1" }); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		k, _ = nodeproto.GenerateKey()
		if err := s.AddHubPin(nodeproto.PinOf(k)); err != nil {
			t.Fatal(err)
		}
	}
	if pins := s.State().HubPins; len(pins) != maxHubPins || pins[len(pins)-1] != nodeproto.PinOf(k) {
		t.Fatalf("pins %v", pins)
	}
	if s.AddHubPin("nope") == nil {
		t.Fatal("bad pin added")
	}

	// A restart reloads the same identity.
	s2, err := OpenIdentity(dir)
	if err != nil || s2.Current().Pin != first || s2.State().Node != "n_1" {
		t.Fatalf("reload: %v", err)
	}
	// Rotation: begin, crash (reload), then commit.
	next, err := s2.BeginRotate()
	if err != nil {
		t.Fatal(err)
	}
	s3, _ := OpenIdentity(dir)
	if n, ok := s3.Next(); !ok || n.Pin != next || s3.Current().Pin != first {
		t.Fatal("the next key didn't survive a restart")
	}
	if err := s3.CommitRotate(); err != nil || s3.Current().Pin != next {
		t.Fatalf("commit: %v", err)
	}
	if _, ok := s3.Next(); ok {
		t.Fatal("still rotating")
	}
	s4, _ := OpenIdentity(dir)
	if s4.Current().Pin != next {
		t.Fatal("the commit didn't stick")
	}
	if _, err := s4.BeginRotate(); err != nil {
		t.Fatal(err)
	}
	if err := s4.AbortRotate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, nextFile)); !os.IsNotExist(err) {
		t.Fatal("abort left the next key")
	}
}

func TestHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health")
	if _, err := CheckHealth(path, time.Now()); err == nil {
		t.Fatal("healthy without a file")
	}
	h := NewHealth(path)
	h.Set(StateConnected, "n_1")
	r, err := CheckHealth(path, time.Now())
	if err != nil || r.State != StateConnected || r.Detail != "n_1" {
		t.Fatalf("report %+v %v", r, err)
	}
	if _, err := CheckHealth(path, time.Now().Add(2*HealthStale)); err == nil {
		t.Fatal("a stale file is healthy")
	}
	var nilHealth *Health
	nilHealth.Set(StateConnected, "") // no panic
}
