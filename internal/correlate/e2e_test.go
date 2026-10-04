package correlate_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/integrations/docker"
	"dash.cha.sh/integrations/traefik"
	"dash.cha.sh/integrations/unraid"
	"dash.cha.sh/internal/correlate"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/topics"
)

// The spike captures of the dogfood home: jupiter's Docker engine (behind
// a Tecnativa socket proxy), the Unraid API on the same server, and the
// home's Traefik. The Docker and Unraid captures list the same 74
// containers with the same (fake) IDs.
const (
	dockerFixtures  = "../../testdata/integrations/docker/jupiter"
	unraidFixtures  = "../../testdata/integrations/unraid/jupiter"
	traefikFixtures = "../../testdata/integrations/traefik"
)

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeDocker answers like the capture's proxy: the engine's list and
// inspects, and a refusal for everything else.
func fakeDocker(t *testing.T) string {
	inspects := map[string][]byte{}
	files, _ := filepath.Glob(filepath.Join(dockerFixtures, "inspect", "*.json"))
	for _, f := range files {
		b := readFixture(t, f)
		var m struct {
			ID string `json:"Id"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		inspects[m.ID] = b
	}
	list := readFixture(t, filepath.Join(dockerFixtures, "containers.json"))
	version := readFixture(t, filepath.Join(dockerFixtures, "version.json"))
	inspectPath := regexp.MustCompile(`^/v1\.\d+/containers/([0-9a-f]{64})/json$`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("the Docker integration sent %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch p := r.URL.Path; {
		case p == "/_ping":
			w.Header().Set("Api-Version", "1.54")
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "OK")
		case p == "/version":
			w.Write(version)
		case strings.HasSuffix(p, "/containers/json"):
			w.Write(list)
		case inspectPath.MatchString(p) && inspects[inspectPath.FindStringSubmatch(p)[1]] != nil:
			w.Write(inspects[inspectPath.FindStringSubmatch(p)[1]])
		default:
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "<html><body><h1>403 Forbidden</h1></body></html>")
		}
	}))
	t.Cleanup(srv.Close)
	return "tcp://" + strings.TrimPrefix(srv.URL, "http://")
}

// fakeUnraid answers each GraphQL operation with its capture, on an API
// version the integration accepts.
func fakeUnraid(t *testing.T) string {
	files := map[string]string{
		"ChadashVersion": "info.json", "ChadashInfo": "info.json", "ChadashNetwork": "network.json", "ChadashMetrics": "metrics.json",
		"ChadashArray": "array.json", "ChadashDocker": "docker.json", "ChadashShares": "shares.json", "ChadashMe": "me.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OperationName string `json:"operationName"`
		}
		if r.Method != http.MethodPost || r.Header.Get("x-api-key") != "fake-unraid-key" || json.NewDecoder(r.Body).Decode(&req) != nil || files[req.OperationName] == "" {
			http.NotFound(w, r)
			return
		}
		b := readFixture(t, filepath.Join(unraidFixtures, files[req.OperationName]))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(strings.Replace(string(b), `"4.37.2+a00c99a5"`, `"4.37.5+0123abcd"`, 1)))
	}))
	t.Cleanup(srv.Close)
	// localhost, so the server's LAN address comes from its interfaces.
	return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
}

// fakeTraefik serves the capture's API, every list on one page.
func fakeTraefik(t *testing.T) string {
	files := map[string]string{
		"/api/version": "version.json", "/api/overview": "overview.json", "/api/entrypoints": "entrypoints.json",
		"/api/http/routers": "http-routers.json", "/api/http/services": "http-services.json", "/api/http/middlewares": "http-middlewares.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Next-Page", "1")
		w.Write(readFixture(t, filepath.Join(traefikFixtures, name)))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// harness runs an integration instance into the shared topic set, as the
// hub's instances all write into one.
func harness(t *testing.T, set *topics.Set, id, name string) *integrationtest.Harness {
	h := integrationtest.New(t)
	h.ID, h.Set = id, set
	h.Emitter = runtime.NewEmitter(set, id, h.Now)
	if err := h.Emitter.PublishSelf(integration.Resource{Kind: "integration", Name: name, State: "connected",
		Attrs: map[string]any{"status": "ok"}, Health: integration.Health{Status: integration.HealthOK}}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDockerUnraidTraefikFixtures(t *testing.T) {
	set := topics.New(graph.New(), time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC))
	ctx := t.Context()
	for _, inst := range []integration.Instance{
		harness(t, set, "i_docker", "Jupiter Docker").Connect(docker.Integration{}, map[string]any{"endpoint": fakeDocker(t)}, nil),
		harness(t, set, "i_unraid", "Jupiter").Connect(unraid.Integration{}, map[string]any{"url": fakeUnraid(t), "tls": map[string]any{"mode": "verify"}},
			map[string]string{"api_key": "fake-unraid-key"}),
		harness(t, set, "i_traefik", "Traefik").Connect(traefik.Integration{}, map[string]any{"url": fakeTraefik(t)}, nil),
	} {
		if err := inst.Sync(ctx, integrationtest.Watched); err != nil {
			t.Fatal(err)
		}
	}
	_, before := set.Graph.Snapshot(nil)
	containers := 0
	for _, r := range before.Resources {
		if r.Kind == "container" {
			containers++
		}
	}
	if containers != 148 {
		t.Fatalf("%d containers before correlating, want 74 from each source", containers)
	}

	c := correlate.New(correlate.Options{Topics: set})
	if err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	_, doc := set.Graph.Snapshot(nil)
	has := func(from string, typ graph.RelType, to string) bool {
		rel, ok := doc.Relations[graph.Relation{From: graph.RID(from), Type: typ, To: graph.RID(to)}.Key()]
		return ok && rel.Source == correlate.Source
	}

	// One of each container: every Unraid container is same_as Docker's.
	merged := 0
	for _, rel := range doc.Relations {
		if rel.Type == correlate.SameAs && strings.HasPrefix(string(rel.From), "unraid:i_unraid/container/") &&
			strings.HasPrefix(string(rel.To), "docker:i_docker/container/") {
			merged++
			if path(rel.From) != path(rel.To) {
				t.Errorf("%s merged with %s", rel.From, rel.To)
			}
		}
	}
	if merged != 74 {
		t.Fatalf("%d containers merged, want 74", merged)
	}
	if n := len(correlate.Distinct(doc, "container")); n != 74 {
		t.Fatalf("%d distinct containers, want 74", n)
	}
	// Both sources stay, each with its own facts.
	members := correlate.Members(doc, "unraid:i_unraid/container/sonarr")
	if len(members) != 2 || members[0] != "docker:i_docker/container/sonarr" || members[1] != "unraid:i_unraid/container/sonarr" {
		t.Fatalf("members = %v", members)
	}
	if u := doc.Resources["unraid:i_unraid/container/sonarr"]; u == nil || u.Attrs["autostart"] == nil {
		t.Fatalf("Unraid's container lost its facts: %+v", u)
	}

	// The engine is reached by its proxy's loopback address in the test,
	// but its containers are the ones Unraid reports, so it runs on
	// jupiter, and stops with jupiter's array.
	if !has("docker:i_docker/engine", correlate.RunsOn, "unraid:i_unraid/host") {
		t.Fatal("the engine isn't placed on jupiter")
	}
	if !has("docker:i_docker/engine", correlate.DependsOn, "unraid:i_unraid/array") {
		t.Fatal("the engine doesn't depend on the array")
	}

	// Traefik's backends on jupiter's address are the containers'
	// published ports.
	for route, cont := range map[string]string{
		"bazarr@file": "bazarr", "immich@file": "immich_server", "prowlarr@file": "binhex-prowlarr",
		"sonarr@file": "nginxproxy", // the nginx-proxy hop
	} {
		if !has("traefik:i_traefik/route/"+route, correlate.RoutesTo, "docker:i_docker/container/"+cont) {
			t.Errorf("route %s doesn't go to %s", route, cont)
		}
	}
	routed := 0
	for _, rel := range doc.Relations {
		if rel.Type == correlate.RoutesTo && rel.Source == correlate.Source {
			routed++
		}
	}
	if routed < 10 {
		t.Errorf("only %d routes reach a container", routed)
	}

	// Immich's compose file says what it needs.
	for _, l := range c.Links() {
		if l.Type == correlate.DependsOn && l.From == "docker:i_docker/container/immich_server" && l.Rule != "compose" {
			t.Errorf("immich dependency by %s", l.Rule)
		}
	}
	deps := 0
	for _, l := range c.Links() {
		if l.Type == correlate.DependsOn && l.From == "docker:i_docker/container/immich_server" {
			deps++
		}
	}
	if deps != 2 {
		t.Errorf("immich_server depends on %d containers, want its database and redis", deps)
	}

	// Nothing was rewritten: every source resource is as it was.
	for id, r := range before.Resources {
		if after := doc.Resources[id]; after == nil || after != r {
			t.Fatalf("resource %s changed", id)
		}
	}
	for k, rel := range before.Relations {
		if after := doc.Relations[k]; after == nil || *after != *rel {
			t.Fatalf("relation %s changed", k)
		}
	}
}

// path is a resource ID's local name: "sonarr" for …/container/sonarr.
func path(id graph.RID) string { return string(id)[strings.LastIndexByte(string(id), '/')+1:] }
