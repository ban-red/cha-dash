package spec

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// semantics is a spec whose attrs show what v1beta1 changed: a config
// default, the clock and findIndex with no match.
const semantics = `apiVersion: chadash/v1beta1
kind: Integration
meta: { id: widgets, name: Widgets, icon: widgets, version: 0.1.0, tier: bronze, capabilities: [primitives.stat] }
config:
  warn: { type: integer, title: Warn, default: 5 }
  site: { type: string, title: Site, default: main }
  zone: { type: string, title: Zone }
requests:
  info:
    path: /api/{{ config.site }}/info
    query: { zone: "{{ config.zone }}" }
    extract: { list: list }
resources:
  - id: app
    kind: app
    attrs:
      warn: config.warn
      double: config.warn * 2
      clock: now().Format("2006-01-02T15:04:05Z07:00")
      first: 'findIndex(info.list, # > 9)'
      last: 'findLastIndex(info.list, # > 9)'
      found: 'findIndex(info.list, # > 1)'
`

func syncSemantics(t *testing.T, version string, config map[string]any) (map[string]any, *integrationtest.Harness, []string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"list": [1, 2, 3]}`))
	}))
	t.Cleanup(srv.Close)
	src := strings.Replace(semantics, "chadash/v1beta1", version, 1)
	if version == V1Alpha1 {
		src = strings.Replace(src, "      double: config.warn * 2\n", "", 1) // null * 2 fails in v1alpha1
	}
	s := mustParse(t, src)
	h := integrationtest.New(t)
	cfg := map[string]any{"url": srv.URL}
	for k, v := range config {
		cfg[k] = v
	}
	if err := h.Connect(s, cfg, nil).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, ok := h.Resource("widgets:i_test/app")
	if !ok {
		t.Fatal("no app")
	}
	return r.Attrs, h, paths
}

// v1beta1: a default is the value when unset, in expressions and
// templates; now() is the framework's clock; findIndex is -1 on no match.
func TestV1Beta1Semantics(t *testing.T) {
	attrs, h, paths := syncSemantics(t, V1Beta1, nil)
	if attrs["warn"] != 5.0 || attrs["double"] != 10.0 {
		t.Errorf("defaults: warn %v, double %v", attrs["warn"], attrs["double"])
	}
	if attrs["clock"] != h.Now().Format(time.RFC3339) {
		t.Errorf("clock %v, the harness says %v", attrs["clock"], h.Now())
	}
	if attrs["first"] != -1.0 || attrs["last"] != -1.0 || attrs["found"] != 1.0 {
		t.Errorf("findIndex: first %v, last %v, found %v", attrs["first"], attrs["last"], attrs["found"])
	}
	if len(paths) != 1 || paths[0] != "/api/main/info?zone=" {
		t.Errorf("requests %v: the default fills a template, and an unset field is empty", paths)
	}
	// The owner's value wins over the default.
	if attrs, _, paths := syncSemantics(t, V1Beta1, map[string]any{"warn": 7, "site": "lab"}); attrs["warn"] != 7.0 || paths[0] != "/api/lab/info?zone=" {
		t.Errorf("set: %v %v", attrs["warn"], paths)
	}

	// The clock moves with the harness.
	s := mustParse(t, semantics)
	h2 := integrationtest.New(t)
	h2.Advance(36 * time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"list": []}`)) }))
	defer srv.Close()
	if err := h2.Connect(s, map[string]any{"url": srv.URL}, nil).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if r, _ := h2.Resource("widgets:i_test/app"); r.Attrs["clock"] != "2026-10-05T02:05:00Z" {
		t.Errorf("advanced clock %v", r.Attrs["clock"])
	}
}

// v1alpha1 keeps its semantics: no defaults, the wall clock, nil from
// findIndex, and "<nil>" for an unset template.
func TestV1Alpha1Semantics(t *testing.T) {
	attrs, h, paths := syncSemantics(t, V1Alpha1, nil)
	if _, ok := attrs["warn"]; ok {
		t.Errorf("v1alpha1 applied a default: %v", attrs["warn"])
	}
	if c, _ := attrs["clock"].(string); c == h.Now().Format(time.RFC3339) || c[:4] != time.Now().UTC().Format("2006") {
		t.Errorf("v1alpha1's clock %v isn't the wall clock", c)
	}
	if _, ok := attrs["first"]; ok || attrs["found"] != 1.0 {
		t.Errorf("findIndex: %v", attrs)
	}
	if paths[0] != "/api/%3Cnil%3E/info?zone=%3Cnil%3E" {
		t.Errorf("requests %v", paths)
	}
}

// v1beta1 reserves now; v1alpha1 doesn't.
func TestV1Beta1ReservesNow(t *testing.T) {
	src := strings.Replace(semantics, "  info:\n", "  now:\n", 1)
	src = strings.ReplaceAll(src, "info.list", "now.list")
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "now is the clock") {
		t.Fatalf("a request called now: %v", err)
	}
	alpha := strings.Replace(src, "chadash/v1beta1", V1Alpha1, 1)
	alpha = strings.Replace(alpha, "      double: config.warn * 2\n", "", 1)
	alpha = strings.Replace(alpha, `      clock: now().Format("2006-01-02T15:04:05Z07:00")`+"\n", "", 1)
	if _, err := Parse([]byte(alpha)); err != nil {
		t.Fatalf("v1alpha1 with a request called now: %v", err)
	}
	if _, err := Parse([]byte(strings.Replace(semantics, "chadash/v1beta1", "chadash/v1", 1))); err == nil || !strings.Contains(err.Error(), "chadash/v1beta1") {
		t.Fatalf("an unknown version: %v", err)
	}
}

// Each version publishes its own schema, the same shape.
func TestSchemaFor(t *testing.T) {
	for _, v := range Versions {
		b := SchemaFor(v)
		if !strings.Contains(string(b), `"const": "`+v+`"`) {
			t.Errorf("%s: %.200s", v, b)
		}
	}
	if string(Schema()) != string(SchemaFor(V1Beta1)) || SchemaFor("chadash/v2") != nil {
		t.Fatal("Schema is v1beta1's; an unknown version has none")
	}
	if s := mustParse(t, semantics); s.APIVersion() != V1Beta1 || s.Manifest().Engine != integration.EngineSpec {
		t.Fatal(s.APIVersion())
	}
}
