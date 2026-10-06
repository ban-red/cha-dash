package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/node"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfig(t *testing.T) {
	c, err := loadConfig(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.HubURL != defaultHub || c.Name != "local" || c.PolicyPath != "/etc/chadash-node/policy.yaml" || c.PairingFile != "/pairing/token" || c.DockerHost != "unix:///var/run/docker.sock" {
		t.Fatalf("defaults %+v", c)
	}
	for k, v := range map[string]string{
		"CHADASH_NODE_HUB": "http://chadash:3696/node", "CHADASH_NODE_NAME": "bad name",
		"CHADASH_NODE_LOG_LEVEL": "loud", "CHADASH_NODE_LOG_FORMAT": "xml", "CHADASH_NODE_DOCKER_HOST": "/var/run/docker.sock",
	} {
		if _, err := loadConfig(envOf(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
	c, _ = loadConfig(envOf(map[string]string{"CHADASH_NODE_DIR": "/srv/node/", "CHADASH_NODE_HUB": "wss://dash.lab.example/node"}))
	if c.PolicyPath != "/srv/node/policy.yaml" || c.identityDir() != "/srv/node/identity" {
		t.Fatalf("dir %+v", c)
	}
}

func TestCommands(t *testing.T) {
	dir := t.TempDir()
	env := envOf(map[string]string{"CHADASH_NODE_DIR": dir, "CHADASH_NODE_HEALTH_FILE": filepath.Join(dir, "health")})
	var out, errb bytes.Buffer
	if code := run(t.Context(), []string{"version"}, env, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "chadash-node ") {
		t.Fatalf("version: %d %q", code, out.String())
	}
	out.Reset()
	if code := run(t.Context(), []string{"show-key"}, env, &out, &errb); code != 1 {
		t.Fatalf("show-key before keygen: %d", code)
	}
	if code := run(t.Context(), []string{"keygen"}, env, &out, &errb); code != 0 || !strings.Contains(out.String(), "pin:         ed25519:") {
		t.Fatalf("keygen: %d %q %q", code, out.String(), errb.String())
	}
	first := out.String()
	out.Reset()
	if code := run(t.Context(), []string{"show-key"}, env, &out, &errb); code != 0 || out.String() != first {
		t.Fatalf("show-key: %q, want %q", out.String(), first)
	}
	if strings.Contains(first, "PRIVATE") {
		t.Fatal("show-key printed the private key")
	}
	if code := run(t.Context(), []string{"healthcheck"}, env, &out, &errb); code != 1 {
		t.Fatalf("healthcheck without a running node: %d", code)
	}
	node.NewHealth(filepath.Join(dir, "health")).Set(node.StateConnecting, "")
	out.Reset()
	if code := run(t.Context(), []string{"healthcheck"}, env, &out, &errb); code != 0 || !strings.Contains(out.String(), "connecting") {
		t.Fatalf("healthcheck: %d %q", code, out.String())
	}
	if code := run(t.Context(), []string{"frobnicate"}, env, &out, &errb); code != 2 {
		t.Fatalf("unknown command: %d", code)
	}
}

// TestServeRefusesABadPolicy: a policy file that doesn't parse stops the
// node before it dials anything.
func TestServeRefusesABadPolicy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte("docker: { allow: [everything] }\n"), 0o600)
	var errb bytes.Buffer
	env := envOf(map[string]string{"CHADASH_NODE_DIR": dir, "CHADASH_NODE_HEALTH_FILE": filepath.Join(dir, "h")})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if code := run(ctx, []string{"serve"}, env, &errb, &errb); code != 1 || !strings.Contains(errb.String(), "unknown grant") {
		t.Fatalf("serve with a bad policy: %d %s", code, errb.String())
	}
}
