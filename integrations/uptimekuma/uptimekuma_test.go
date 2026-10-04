package uptimekuma

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// The fixtures are synthetic (testdata/integrations/uptimekuma/README.md),
// built from Uptime Kuma 2's source.
var fixtures = filepath.Join("..", "..", "testdata", "integrations", "uptimekuma")

const apiKey = "uk1_test-key-0123456789"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fake is an Uptime Kuma with the status page "current".
type fake struct {
	*httptest.Server
	t *testing.T

	mu     sync.Mutex
	files  map[string][]byte
	status map[string]int // path → status to answer instead
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, status: map[string]int{}, files: map[string][]byte{
		"/api/status-page/current":           fixture(t, "status_page.json"),
		"/api/status-page/heartbeat/current": fixture(t, "heartbeat.json"),
		"/metrics":                           fixture(t, "metrics.txt"),
	}}
	f.Server = httptest.NewServer(f)
	t.Cleanup(f.Close)
	return f
}

func (f *fake) set(path string, status int, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[path] = status
	if body != nil {
		f.files[path] = body
	}
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.String(), apiKey) {
		f.t.Errorf("the API key is in the URL: %s", r.URL)
	}
	if r.Method != http.MethodGet {
		f.t.Errorf("%s %s: the integration only reads", r.Method, r.URL.Path)
	}
	f.mu.Lock()
	body, ok := f.files[r.URL.Path]
	status := f.status[r.URL.Path]
	f.mu.Unlock()
	switch {
	case r.URL.Path == "/metrics":
		if user, pass, ok := r.BasicAuth(); !ok || user != "" || pass != apiKey {
			w.Header().Set("WWW-Authenticate", `Basic realm="Uptime Kuma"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	case strings.HasPrefix(r.URL.Path, "/api/status-page/"):
		if r.Header.Get("Authorization") != "" {
			f.t.Errorf("%s: the status page is public; no credential belongs there", r.URL.Path)
		}
		if !ok {
			// Uptime Kuma's sendHttpError for an unknown slug.
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"status":"fail","msg":"Status Page Not Found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	default:
		f.t.Errorf("unexpected request: %s", r.URL)
		http.NotFound(w, r)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
	}
	w.Write(body)
}

func connect(t *testing.T, h *integrationtest.Harness, f *fake, key string) integration.Instance {
	t.Helper()
	var secrets map[string]string
	if key != "" {
		secrets = map[string]string{"key": key}
	}
	return h.Connect(Integration{}, map[string]any{"url": f.URL, "slug": "current"}, secrets)
}

func mustSync(t *testing.T, inst integration.Instance) {
	t.Helper()
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Uptime Kuma" || !slices.Equal(m.SecretFields(), []string{"key"}) || m.Poll.Interval.Seconds() != 60 {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidate(t *testing.T) {
	// The Homepage importer's shape, with and without the key.
	imported := map[string]any{"slug": "current", "tls": map[string]any{"mode": "verify"}, "url": "https://uptime.lab.example"}
	if errs := integrationtest.Validate(Integration{}, imported, map[string]string{"key": "x"}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, imported, nil); len(errs) != 0 {
		t.Fatalf("without a key: %v", errs)
	}
	cases := map[string]struct {
		config map[string]any
		path   string
	}{
		"no slug":   {map[string]any{"url": "https://a.lab.example"}, "/slug"},
		"bad slug":  {map[string]any{"url": "https://a.lab.example", "slug": "../admin"}, "/slug"},
		"no url":    {map[string]any{"slug": "current"}, "/url"},
		"ftp url":   {map[string]any{"url": "ftp://a.lab.example", "slug": "current"}, "/url"},
		"extra key": {map[string]any{"url": "https://a.lab.example", "slug": "x", "token": "y"}, "/token"},
	}
	for name, c := range cases {
		errs := integrationtest.Validate(Integration{}, c.config, nil)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%s: %v, want an error at %s", name, errs, c.path)
		}
	}
}

func TestStatusPage(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)
	mustSync(t, connect(t, h, f, ""))

	app, ok := h.Resource("uptimekuma:i_test/app")
	if !ok || app.Kind != "app" || app.Name != "Test" || app.State != "running" || app.Health != integration.HealthOK || app.Source != "integration:i_test" {
		t.Fatalf("app = %+v", app)
	}
	for k, want := range map[string]any{"type": "uptimekuma", "status_page": "current", "title": "Lab status", "monitors": 9.0, "url": f.URL} {
		if app.Attrs[k] != want {
			t.Errorf("app.attrs.%s = %v, want %v", k, app.Attrs[k], want)
		}
	}
	if links := app.Attrs["links"].([]any); len(links) != 2 || links[1].(map[string]any)["url"] != f.URL+"/status/current" {
		t.Errorf("links = %v", links)
	}
	// qBittorrent is down, Mealie is pending (in neither count).
	if se := h.Series("uptimekuma:i_test/app", "monitors.up"); se["value"] != 7.0 || se["max"] != 9.0 {
		t.Errorf("monitors.up = %v", se)
	}
	if se := h.Series("uptimekuma:i_test/app", "monitors.down"); se["value"] != 1.0 {
		t.Errorf("monitors.down = %v", se)
	}

	ha, ok := h.Resource("uptimekuma:i_test/monitor/1")
	if !ok || ha.Kind != "check" || ha.Name != "Home Assistant" || ha.State != "online" || ha.Health != integration.HealthOK {
		t.Fatalf("home assistant = %+v", ha)
	}
	for k, want := range map[string]any{"check": "http", "monitor_id": "1", "group": "Home", "target": "https://home.lab.example", "uptime_24h": 100.0, "cert_days": 61.0} {
		if ha.Attrs[k] != want {
			t.Errorf("home assistant.attrs.%s = %v, want %v", k, ha.Attrs[k], want)
		}
	}
	if se := h.Series("uptimekuma:i_test/monitor/1", "http.ms"); se["value"] != 41.0 || se["warn"] != 800.0 {
		t.Errorf("http.ms = %v", se)
	}
	qb, _ := h.Resource("uptimekuma:i_test/monitor/8")
	if qb.State != "error" || qb.Health != integration.HealthDown || qb.Attrs["health.since"] != "2026-10-03T14:03:33Z" ||
		qb.Attrs["uptime_24h"] != 99.7 || qb.Attrs["target"] != nil {
		t.Errorf("qbittorrent = %+v", qb)
	}
	if se := h.Series("uptimekuma:i_test/monitor/8", "tcp.ms"); se == nil || se["value"] != nil {
		t.Errorf("a down monitor's latency is unknown: %v", se)
	}
	if m, _ := h.Resource("uptimekuma:i_test/monitor/5"); m.Health != integration.HealthAttention || m.Attrs["health.reason"] != "Failing; Uptime Kuma is retrying" {
		t.Errorf("mealie = %+v", m)
	}
	if m, _ := h.Resource("uptimekuma:i_test/monitor/2"); m.Attrs["cert_days"] != nil || h.Series(string(m.ID), "ping.ms")["value"] != 1.0 {
		t.Errorf("opnsense = %+v", m)
	}
	rels := h.Graph().Relations
	for _, k := range []string{"uptimekuma:i_test/monitor/3 part_of uptimekuma:i_test/app", "integration:i_test monitors uptimekuma:i_test/monitor/3", "integration:i_test monitors uptimekuma:i_test/app"} {
		if rels[k] == nil {
			t.Errorf("missing relation %s", k)
		}
	}
}

func TestMetrics(t *testing.T) {
	check := func(t *testing.T, metrics []byte) {
		f := newFake(t)
		f.set("/metrics", 0, metrics)
		h := integrationtest.New(t)
		mustSync(t, connect(t, h, f, apiKey))
		// /metrics is live: Mealie has recovered since the heartbeats' cache.
		if se := h.Series("uptimekuma:i_test/app", "monitors.up"); se["value"] != 8.0 {
			t.Errorf("monitors.up = %v", se)
		}
		if se := h.Series("uptimekuma:i_test/monitor/1", "http.ms"); se["value"] != 42.0 {
			t.Errorf("http.ms from /metrics = %v", se)
		}
		qb, _ := h.Resource("uptimekuma:i_test/monitor/8")
		if qb.Attrs["target"] != "10.0.11.30:8080" || qb.Attrs["ip"] != "10.0.11.30" {
			t.Errorf("qbittorrent = %+v", qb.Attrs)
		}
		if im, _ := h.Resource("uptimekuma:i_test/monitor/4"); im.Attrs["target"] != "https://photos.lab.example" || im.Attrs["links"] == nil {
			t.Errorf("immich = %+v", im.Attrs)
		}
		if _, ok := h.Resource("uptimekuma:i_test/monitor/10"); ok {
			t.Error("a monitor that isn't on the status page isn't published")
		}
	}
	t.Run("uptime kuma 2", func(t *testing.T) { check(t, fixture(t, "metrics.txt")) })
	// Uptime Kuma 1.x has no monitor_id label: monitors match by name.
	t.Run("uptime kuma 1", func(t *testing.T) {
		check(t, regexp.MustCompile(`monitor_id="\d+",`).ReplaceAll(fixture(t, "metrics.txt"), nil))
	})
}

func TestErrors(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)

	unknown := h.Connect(Integration{}, map[string]any{"url": f.URL, "slug": "nope"}, nil)
	err := unknown.Sync(t.Context(), integrationtest.Watched)
	if code, class, _ := integration.Classify(err); code != integration.CodeInvalidConfig || class != integration.Permanent || integration.FieldOf(err) != "slug" {
		t.Fatalf("unknown slug = %v (%s)", err, code)
	}

	bad := connect(t, h, f, "wrong")
	err = bad.Sync(t.Context(), integrationtest.Watched)
	if code, _, _ := integration.Classify(err); code != integration.CodeAuth || integration.FieldOf(err) != "key" {
		t.Fatalf("wrong key = %v", err)
	}
	if strings.Contains(h.Logs(), "wrong") {
		t.Fatal("the key leaked into the logs")
	}

	// Uptime Kuma answers 403 for its own errors on public endpoints: not
	// an auth failure.
	f.set("/api/status-page/heartbeat/current", http.StatusForbidden, []byte(`{"status":"fail","msg":"Cannot read properties of null"}`))
	err = connect(t, h, f, "").Sync(t.Context(), integrationtest.Watched)
	if code, class, msg := integration.Classify(err); code != integration.CodeUpstream || class != integration.Transient || !strings.Contains(msg, "Cannot read properties") {
		t.Fatalf("403 on the status page = %v", err)
	}

	f2 := newFake(t)
	f2.set("/api/status-page/current", http.StatusOK, []byte("<html>Uptime Kuma</html>"))
	var re *integration.RepairError
	if err := connect(t, h, f2, "").Sync(t.Context(), integrationtest.Watched); !errors.As(err, &re) || re.Code != integration.CodeAPIChanged {
		t.Fatalf("HTML = %v", err)
	}
}

func TestTest(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)
	res := connect(t, h, f, "").Test(t.Context())
	if !res.OK || len(res.Grants) != 1 || res.Grants[0].CanWrite() || !strings.Contains(res.Grants[0].Detail, "9 monitors") {
		t.Fatalf("test = %+v", res)
	}
	res = connect(t, h, f, apiKey).Test(t.Context())
	if !res.OK || len(res.Grants) != 2 || res.Grants[1].Name != "metrics" || res.Grants[1].CanWrite() {
		t.Fatalf("test with a key = %+v", res)
	}
	if _, ok := h.Resource("uptimekuma:i_test/app"); ok {
		t.Fatal("Test must not emit facts")
	}
	if res := connect(t, h, f, "wrong").Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("test with a wrong key = %+v", res)
	}
}

func TestParseSample(t *testing.T) {
	name, labels, v, err := parseSample(`monitor_status{monitor_name="Say \"hi\" \\ there",monitor_type="http",} 2 1696000000000`)
	if err != nil || name != "monitor_status" || labels["monitor_name"] != `Say "hi" \ there` || labels["monitor_type"] != "http" || v != 2 {
		t.Fatalf("%q %v %v %v", name, labels, v, err)
	}
	for _, bad := range []string{`monitor_status{monitor_name="x} 1`, `monitor_status{monitor_name=x} 1`, `monitor_status`, `monitor_status{} abc`} {
		if _, _, _, err := parseSample(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}
