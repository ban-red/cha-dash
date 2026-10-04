//go:build live

package liveverify

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"

	_ "dash.cha.sh/integrations/checks"
	_ "dash.cha.sh/integrations/docker"
	_ "dash.cha.sh/integrations/frigate"
	_ "dash.cha.sh/integrations/opnsense"
	_ "dash.cha.sh/integrations/proxmox"
	_ "dash.cha.sh/integrations/traefik"
	_ "dash.cha.sh/integrations/unraid"
)

var (
	envFile   = flag.String("env", "../../.env.local", "the env file with the lab's credentials")
	only      = flag.String("only", "", "comma-separated instance labels to run (default: all), e.g. proxmox/pluto,traefik")
	gap       = flag.Duration("gap", 5*time.Second, "pause between the two syncs")
	subscribe = flag.Duration("subscribe", 20*time.Second, "how long to hold a push stream open (OPNsense, Frigate, Docker); 0 skips")
	tlsMode   = flag.String("tls", "tofu", "TLS mode for https sources: verify, tofu or insecure")
	rawDir    = flag.String("raw", "", "where the full results go (default ~/.local/share/chadash-spikes/live-verify/<time>)")
	from      = flag.String("from", "", "summarize a saved run's raw results again, offline (TestFrom)")
)

// target is one instance to verify.
type target struct {
	label   string // printed; derived from env key names, not values
	integ   string // integration ID
	config  map[string]any
	secrets map[string]string
}

func TestLive(t *testing.T) {
	if os.Getenv("CHADASH_LIVE") != "1" {
		t.Skip("set CHADASH_LIVE=1 to run against the lab (read-only)")
	}
	env, err := readEnv(*envFile)
	if err != nil {
		t.Fatal(err)
	}
	targets := targetsFrom(env, *tlsMode)
	if *only != "" {
		want := strings.Split(*only, ",")
		targets = slices.DeleteFunc(targets, func(x target) bool { return !slices.Contains(want, x.label) })
	}
	if len(targets) == 0 {
		t.Fatal("no instances found in the env file")
	}
	dir := *rawDir
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share", "chadash-spikes", "live-verify", time.Now().Format("20060102-150405"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("live-verify: %d instances · tls %s · raw results in %s\n\n", len(targets), *tlsMode, dir)
	for _, x := range targets {
		t.Run(strings.ReplaceAll(x.label, "/", "_"), func(t *testing.T) { verify(t, x, dir) })
	}
}

func verify(t *testing.T, x target, dir string) {
	integ, _, ok := integration.Default.Get(x.integ)
	if !ok {
		t.Fatalf("%s isn't registered", x.integ)
	}
	h := integrationtest.New(t)
	h.Advance(time.Since(h.Now())) // the harness starts at the Demo Home's moment; run on the real clock
	raw := map[string]any{"label": x.label, "integration": x.integ}
	out := &strings.Builder{}
	fmt.Fprintf(out, "── %s (%s)\n", x.label, x.integ)

	if errs := integrationtest.Validate(integ, x.config, x.secrets); len(errs) > 0 {
		fmt.Fprintf(out, "   config:    INVALID %v\n", fieldNames(errs))
		fmt.Println(out.String())
		return
	}
	inst := h.Connect(integ, x.config, x.secrets)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	start := time.Now()
	res := inst.Test(ctx)
	raw["test"] = res
	raw["test_ms"] = time.Since(start).Milliseconds()
	fmt.Fprintf(out, "   test:      ok=%v version=%q %s%s\n", res.OK, res.Version, ms(time.Since(start)), failure(res.ErrorCode))
	fmt.Fprintf(out, "   grants:    %s\n", grantSummary(res.Grants))
	fmt.Fprintf(out, "   warnings:  %s\n", warningCodes(res.Warnings))

	for i := 1; i <= 2; i++ {
		if i == 2 {
			time.Sleep(*gap)
			h.Advance(*gap)
		}
		start := time.Now()
		err := inst.Sync(ctx, integrationtest.Watched)
		code, class := "", ""
		if err != nil {
			var c integration.Class
			var msg string
			code, c, msg = integration.Classify(err)
			class = className(c)
			raw[fmt.Sprintf("sync%d_error", i)] = msg // the message may name hosts: raw results only
		}
		took := time.Since(start)
		raw[fmt.Sprintf("sync%d_ms", i)] = took.Milliseconds()
		raw[fmt.Sprintf("sync%d_code", i)] = strings.TrimSpace(code + " " + class)
		fmt.Fprintf(out, "   sync %d:    %s%s\n", i, ms(took), failure(strings.TrimSpace(code+" "+class)))
	}

	if sub, ok := inst.(integration.Subscriber); ok && *subscribe > 0 {
		sctx, scancel := context.WithTimeout(ctx, *subscribe)
		start := time.Now()
		err := sub.Subscribe(sctx, h.Emitter)
		scancel()
		note := ""
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			code, class, msg := integration.Classify(err)
			note = failure(strings.TrimSpace(code + " " + className(class)))
			raw["subscribe_error"] = msg
		}
		fmt.Fprintf(out, "   subscribe: held %s%s\n", time.Since(start).Round(time.Second), note)
	}

	doc := h.Graph()
	raw["graph"] = doc
	kinds, rels := map[string]int{}, map[string]int{}
	health := map[string]int{}
	for _, r := range doc.Resources {
		kinds[string(r.Kind)]++
		if r.Health != "" {
			health[string(r.Health)]++
		}
	}
	for _, r := range doc.Relations {
		rels[string(r.Type)]++
	}
	fmt.Fprintf(out, "   resources: %s\n", counts(kinds))
	fmt.Fprintf(out, "   health:    %s\n", counts(health))
	fmt.Fprintf(out, "   relations: %s\n", counts(rels))

	// Snapshots hold json.RawMessage collections; decode them once.
	_, msnap := h.Set.Metrics.Snapshot()
	metrics := decoded(msnap)
	raw["metrics"] = metrics
	fmt.Fprintf(out, "   metrics:   %s\n", metricSummary(metrics))

	_, isnap := h.Set.Inbox.Snapshot()
	inbox := decoded(isnap)
	raw["inbox"] = inbox
	fmt.Fprintf(out, "   incidents: %s\n", incidentSummary(inbox))
	raw["stored_config_keys"] = keys(h.StoredConfig())
	raw["logs"] = h.Logs()

	name := strings.ReplaceAll(x.label, "/", "_") + ".json"
	if b, err := json.MarshalIndent(raw, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, name), b, 0o600)
	}
	fmt.Println(out.String())
}

// TestSummaries checks the summary code on a local check, offline: one
// target up (a latency series), one down (fail_after 1, an incident).
func TestSummaries(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	integ, _, _ := integration.Default.Get("checks")
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"targets": []any{
		map[string]any{"name": "Up", "kind": "http", "url": up.URL},
		map[string]any{"name": "Down", "kind": "tcp", "host": "127.0.0.1", "port": 1, "fail_after": 1},
	}}, nil)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	_, msnap := h.Set.Metrics.Snapshot()
	_, isnap := h.Set.Inbox.Snapshot()
	m, in := metricSummary(decoded(msnap)), incidentSummary(decoded(isnap))
	if !strings.Contains(m, "http.ms 1") {
		t.Errorf("metrics = %s", m)
	}
	if in == "none" {
		t.Errorf("incidents = %s", in)
	}
	t.Logf("metrics: %s\nincidents: %s", m, in)
}

// TestFrom summarizes a saved run again, offline: -args -from <dir>.
func TestFrom(t *testing.T) {
	if *from == "" {
		t.Skip("pass -args -from <raw results dir> to summarize a saved run")
	}
	files, err := filepath.Glob(filepath.Join(*from, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no results in %s", *from)
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
		fmt.Println(summarizeSaved(raw))
	}
}

// summarizeSaved prints what verify prints, from the saved JSON.
func summarizeSaved(raw map[string]any) string {
	out := &strings.Builder{}
	fmt.Fprintf(out, "── %v (%v)\n", raw["label"], raw["integration"])
	if test, ok := raw["test"].(map[string]any); ok {
		code, _ := test["ErrorCode"].(string)
		fmt.Fprintf(out, "   test:      ok=%v version=%q%s\n", test["OK"], test["Version"], failure(code))
		var gs []integration.Grant
		var ws []integration.Warning
		b, _ := json.Marshal(test["Grants"])
		_ = json.Unmarshal(b, &gs)
		b, _ = json.Marshal(test["Warnings"])
		_ = json.Unmarshal(b, &ws)
		fmt.Fprintf(out, "   grants:    %s\n", grantSummary(gs))
		fmt.Fprintf(out, "   warnings:  %s\n", warningCodes(ws))
	}
	for i := 1; i <= 2; i++ {
		if c, ok := raw[fmt.Sprintf("sync%d_code", i)].(string); ok {
			fmt.Fprintf(out, "   sync %d:    %vms%s\n", i, raw[fmt.Sprintf("sync%d_ms", i)], failure(c))
		} else if _, failed := raw[fmt.Sprintf("sync%d_error", i)]; failed {
			fmt.Fprintf(out, "   sync %d:    FAILED (see the raw file)\n", i)
		}
	}
	kinds, health, rels := map[string]int{}, map[string]int{}, map[string]int{}
	g, _ := raw["graph"].(map[string]any)
	res, _ := g["resources"].(map[string]any)
	for _, v := range res {
		r, _ := v.(map[string]any)
		kinds[fmt.Sprint(r["kind"])]++
		if h, _ := r["health"].(string); h != "" {
			health[h]++
		}
	}
	relations, _ := g["relations"].(map[string]any)
	for _, v := range relations {
		r, _ := v.(map[string]any)
		rels[fmt.Sprint(r["type"])]++
	}
	fmt.Fprintf(out, "   resources: %s\n", counts(kinds))
	fmt.Fprintf(out, "   health:    %s\n", counts(health))
	fmt.Fprintf(out, "   relations: %s\n", counts(rels))
	m, _ := raw["metrics"].(map[string]any)
	fmt.Fprintf(out, "   metrics:   %s\n", metricSummary(m))
	in, _ := raw["inbox"].(map[string]any)
	fmt.Fprintf(out, "   incidents: %s\n", incidentSummary(in))
	return out.String()
}

// decoded turns a livedoc snapshot (collections of json.RawMessage) into
// plain maps.
func decoded(snap map[string]any) map[string]any {
	b, err := json.Marshal(snap)
	if err != nil {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// ── Targets from the env file ────────────────────────────────────────────────

func targetsFrom(env map[string]string, mode string) []target {
	var out []target
	tlsFor := func(raw string) map[string]any {
		if strings.HasPrefix(raw, "https://") {
			return map[string]any{"mode": mode}
		}
		return nil
	}
	add := func(label, integ, rawURL string, config map[string]any, secrets map[string]string) {
		if t := tlsFor(rawURL); t != nil {
			config["tls"] = t
		}
		out = append(out, target{label: label, integ: integ, config: config, secrets: secrets})
	}
	for _, name := range envNames(env, `^PVE_(.+)_URL$`) {
		p := "PVE_" + name + "_"
		add("proxmox/"+strings.ToLower(name), "proxmox", env[p+"URL"],
			map[string]any{"url": env[p+"URL"], "token_id": env[p+"TOKEN_ID"]},
			map[string]string{"token_secret": env[p+"TOKEN_SECRET"]})
	}
	for _, name := range envNames(env, `^DOCKER_(.+)_URL$`) {
		endpoint, tls := dockerEndpoint(env["DOCKER_"+name+"_URL"])
		c := map[string]any{"endpoint": endpoint}
		if tls {
			c["tls"] = map[string]any{"mode": mode}
		}
		out = append(out, target{label: "docker/" + strings.ToLower(name), integ: "docker", config: c, secrets: map[string]string{}})
	}
	if u := env["TRAEFIK_URL"]; u != "" {
		c, s := map[string]any{"url": u}, map[string]string{}
		if env["TRAEFIK_USERNAME"] != "" {
			c["username"] = env["TRAEFIK_USERNAME"]
			s["password"] = env["TRAEFIK_PASSWORD"]
		}
		add("traefik", "traefik", u, c, s)
	}
	if u := env["OPNSENSE_URL"]; u != "" {
		add("opnsense", "opnsense", u, map[string]any{"url": u},
			map[string]string{"key": env["OPNSENSE_KEY"], "secret": env["OPNSENSE_SECRET"]})
	}
	if u := env["FRIGATE_URL"]; u != "" {
		add("frigate", "frigate", u, map[string]any{"url": u}, map[string]string{})
	}
	for _, name := range envNames(env, `^UNRAID_(.+)_URL$`) {
		u := env["UNRAID_"+name+"_URL"]
		add("unraid/"+strings.ToLower(name), "unraid", u, map[string]any{"url": u},
			map[string]string{"api_key": env["UNRAID_"+name+"_API_KEY"]})
	}
	return out
}

// dockerEndpoint turns a URL as the env file may hold it into the
// integration's endpoint form: tcp://host:port, or unix://path.
func dockerEndpoint(raw string) (endpoint string, tls bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "tcp://" + raw, false
	}
	switch u.Scheme {
	case "http":
		return "tcp://" + u.Host, false
	case "https":
		return "tcp://" + u.Host, true
	}
	return raw, false
}

func envNames(env map[string]string, pattern string) []string {
	re := regexp.MustCompile(pattern)
	var out []string
	for k := range env {
		if m := re.FindStringSubmatch(k); m != nil {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

func readEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	key := regexp.MustCompile(`^[A-Z0-9_]+=`)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !key.MatchString(line) {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		env[k] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return env, sc.Err()
}

// ── Sanitized summaries: shapes and counts, never names, hosts or values ──

func className(c integration.Class) string {
	switch c {
	case integration.Transient:
		return "(transient)"
	case integration.NeedsRepair:
		return "(needs repair)"
	case integration.Permanent:
		return "(permanent)"
	}
	return fmt.Sprintf("(class %d)", c)
}

func ms(d time.Duration) string { return fmt.Sprintf("%dms", d.Milliseconds()) }

func failure(code string) string {
	if code == "" {
		return ""
	}
	return " FAILED " + code
}

func fieldNames(errs []integration.FieldError) []string {
	var out []string
	for _, e := range errs {
		out = append(out, e.Path)
	}
	return out
}

// grantSummary keeps a grant's name only up to its first path segment, so
// permission paths that name nodes or guests don't print.
func grantSummary(gs []integration.Grant) string {
	if len(gs) == 0 {
		return "none reported"
	}
	writes := 0
	names := map[string]int{}
	for _, g := range gs {
		if g.CanWrite() {
			writes++
		}
		n := g.Name
		if strings.HasPrefix(n, "/") {
			parts := strings.SplitN(strings.TrimPrefix(n, "/"), "/", 2)
			n = "/" + parts[0]
			if len(parts) > 1 {
				n += "/…"
			}
		}
		names[n+" ("+g.Access+")"]++
	}
	return fmt.Sprintf("%d, %d can write · %s", len(gs), writes, counts(names))
}

func warningCodes(ws []integration.Warning) string {
	if len(ws) == 0 {
		return "none"
	}
	var out []string
	for _, w := range ws {
		out = append(out, w.Code+"/"+w.Severity)
	}
	return strings.Join(out, ", ")
}

func counts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	var out []string
	for k, v := range m {
		out = append(out, fmt.Sprintf("%s %d", k, v))
	}
	sort.Strings(out)
	return strings.Join(out, " · ")
}

// metricSummary lists each metric key with how many series have a value,
// how many are unknown, and the range of known values.
func metricSummary(snap map[string]any) string {
	series, _ := snap["series"].(map[string]any)
	type agg struct {
		known, unknown int
		min, max       float64
	}
	byKey := map[string]*agg{}
	for key, v := range series {
		_, metric, _ := strings.Cut(key, "#")
		a := byKey[metric]
		if a == nil {
			a = &agg{}
			byKey[metric] = a
		}
		s, _ := v.(map[string]any)
		f, ok := s["value"].(float64)
		if !ok {
			a.unknown++
			continue
		}
		if a.known == 0 || f < a.min {
			a.min = f
		}
		if a.known == 0 || f > a.max {
			a.max = f
		}
		a.known++
	}
	if len(byKey) == 0 {
		return "none"
	}
	var out []string
	for k, a := range byKey {
		s := fmt.Sprintf("%s %d", k, a.known)
		if a.known > 0 {
			s += fmt.Sprintf(" [%g…%g]", round(a.min), round(a.max))
		}
		if a.unknown > 0 {
			s += fmt.Sprintf(" +%d unknown", a.unknown)
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return "\n              " + strings.Join(out, "\n              ")
}

func round(f float64) float64 { return float64(int64(f*10)) / 10 }

func incidentSummary(snap map[string]any) string {
	incs, _ := snap["incidents"].(map[string]any)
	m := map[string]int{}
	for _, v := range incs {
		in, _ := v.(map[string]any)
		label := fmt.Sprint(in["class"], ":", in["scenario"], "/", in["severity"])
		if in["status"] == "resolved" {
			label += " (resolved)"
		}
		m[label]++
	}
	return counts(m)
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
