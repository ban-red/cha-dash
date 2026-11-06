package main

import (
	"slices"
	"sort"
	"strings"
	"testing"

	hubconfig "dash.cha.sh/internal/config"
)

// The published config schema's node section is what loadConfig reads: the
// same environment variables, and the same defaults where it has one.
func TestConfigSchema(t *testing.T) {
	asked := map[string]bool{}
	c, err := loadConfig(func(k string) string {
		asked[k] = true
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range asked {
		got = append(got, k)
	}
	sort.Strings(got)
	if want := hubconfig.Envs(hubconfig.NodeOptions); !slices.Equal(got, want) {
		t.Fatalf("loadConfig reads %v,\nthe schema lists %v", got, want)
	}
	defaults := map[string]string{
		"CHADASH_NODE_HUB": c.HubURL, "CHADASH_NODE_NAME": c.Name, "CHADASH_NODE_DIR": c.Dir,
		"CHADASH_NODE_PAIRING_FILE": c.PairingFile, "CHADASH_NODE_DOCKER_HOST": c.DockerHost,
		"CHADASH_NODE_HEALTH_FILE": c.HealthFile, "CHADASH_NODE_LOG_LEVEL": strings.ToLower(c.LogLevel.String()),
		"CHADASH_NODE_LOG_FORMAT": c.LogFormat,
	}
	for _, o := range hubconfig.NodeOptions {
		if d, ok := defaults[o.Env]; ok && d != o.Default {
			t.Errorf("%s: loadConfig's default %q, the schema's %q", o.Env, d, o.Default)
		}
	}
}
