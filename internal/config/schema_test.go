package config

import (
	"encoding/json"
	"flag"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The schema is the hub's FlagSet: every flag is in it with its env and
// default, and every option with a flag is a flag.
func TestSchemaMatchesFlags(t *testing.T) {
	fs := flag.NewFlagSet("chadash", flag.ContinueOnError)
	Bind(fs, func(string) string { return "" })
	envOf := regexp.MustCompile(`\(env ([A-Z_]+)\)`)
	byFlag := map[string]Option{}
	for _, o := range HubOptions {
		if o.Flag != "" {
			byFlag[o.Flag] = o
		}
	}
	seen := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		o, ok := byFlag[f.Name]
		if !ok {
			t.Errorf("--%s isn't in HubOptions", f.Name)
			return
		}
		seen[f.Name] = true
		if m := envOf.FindStringSubmatch(f.Usage); m == nil || m[1] != o.Env {
			t.Errorf("--%s: its usage names env %v, the schema %s", f.Name, m, o.Env)
		}
		if f.DefValue != o.Default && !(f.DefValue == "false" && o.Default == "") {
			t.Errorf("--%s: default %q, the schema %q", f.Name, f.DefValue, o.Default)
		}
		if o.Secret {
			t.Errorf("--%s: a secret is never a flag (it would show in the process list)", f.Name)
		}
	})
	for name := range byFlag {
		if !seen[name] {
			t.Errorf("HubOptions has --%s, which the hub doesn't take", name)
		}
	}
}

func TestSchemaDocument(t *testing.T) {
	var doc struct {
		ID         string `json:"$id"`
		Version    int    `json:"x-chadash-config-version"`
		Properties map[string]struct {
			Properties map[string]map[string]any `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(Schema(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != SchemaVersion || !strings.HasSuffix(doc.ID, "/v1.json") {
		t.Fatalf("version %d, id %s", doc.Version, doc.ID)
	}
	hub := doc.Properties["hub"].Properties
	if hub["CHADASH_DEMO"]["default"] != false || hub["CHADASH_DEMO_SEED"]["default"] != float64(1) || hub["CHADASH_METRICS_TOKEN"]["writeOnly"] != true {
		t.Fatalf("hub: %v %v %v", hub["CHADASH_DEMO"], hub["CHADASH_DEMO_SEED"], hub["CHADASH_METRICS_TOKEN"])
	}
	for _, opts := range [][]Option{HubOptions, NodeOptions} {
		envs := Envs(opts)
		if len(slices.Compact(slices.Clone(envs))) != len(envs) {
			t.Fatalf("an env var is listed twice: %v", envs)
		}
		for _, o := range opts {
			if o.Doc == "" || (o.Type != "string" && o.Type != "boolean" && o.Type != "integer") {
				t.Errorf("%s: %+v", o.Env, o)
			}
		}
	}
	if len(doc.Properties["node"].Properties) != len(NodeOptions) {
		t.Fatal("node section")
	}
}
