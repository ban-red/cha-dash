//go:build footprint

// Package footprint measures the hub's memory and CPU in-process, for the
// hardware-floor decision (ADR-0016). It boots the hub's real components
// (store, vault, auth, graph, topics, realtime, server) on httptest, with or
// without the Demo Home driver, connects WebSocket clients and drives the
// simulated day through its incidents.
//
// Each scenario runs in a fresh child process (the test binary re-executes
// itself), so peak RSS and GC settings don't leak between scenarios.
//
//	go test -tags footprint -run '^TestFootprint$' -timeout 30m -v ./spikes/footprint/
//
// FOOTPRINT_ONLY=<regexp> runs only the scenarios whose name matches it;
// FOOTPRINT_HOURS=<n> changes how many simulated hours the demo scenarios run.
// Nothing here talks to anything but loopback.
package footprint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/argon2"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/server"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

// Pacing. One demo tick is 30 simulated seconds. Real time only has to be long
// enough for each tick's diffs to leave before the next tick, so every tick
// costs what it costs in real time (one immediate diff plus one coalesced diff
// per subscription), just without the 30 s of idling in between. The hub's
// 250 ms coalescing window is shortened to match; it doesn't change the work
// done per tick, only how long the hub waits before doing it.
const (
	tickPause = 60 * time.Millisecond
	coalesce  = 20 * time.Millisecond
	demoStart = 13 * time.Hour // before plex/host-memory/qbit open; every fix lands by 20:00
	demoHours = 7.5            // 13:00 → 20:30
)

type scenario struct {
	Name     string            `json:"name"`
	Baseline bool              `json:"baseline,omitempty"` // boot nothing: the process floor on this OS
	Argon2   bool              `json:"argon2,omitempty"`   // baseline plus one argon2id hash, to see how the OS counts freed memory
	Demo     bool              `json:"demo"`
	Clients  int               `json:"clients"`
	Hours    float64           `json:"hours,omitempty"`    // simulated hours (demo)
	IdleFor  time.Duration     `json:"idle_for,omitempty"` // real time (no demo)
	Env      map[string]string `json:"env,omitempty"`      // GOGC, GOMEMLIMIT for the child
}

func scenarios() []scenario {
	hours := demoHours
	if h, err := strconv.ParseFloat(os.Getenv("FOOTPRINT_HOURS"), 64); err == nil && h > 0 {
		hours = h
	}
	list := []scenario{
		{Name: "process baseline (nothing booted)", Baseline: true},
		{Name: "baseline + one argon2id hash", Baseline: true, Argon2: true},
		{Name: "empty hub, 0 clients", IdleFor: time.Minute},
		{Name: "empty hub, 1 client", Clients: 1, IdleFor: time.Minute},
	}
	for _, n := range []int{0, 1, 5, 20} {
		list = append(list, scenario{Name: fmt.Sprintf("demo, %d clients", n), Demo: true, Clients: n, Hours: hours})
	}
	list = append(list,
		scenario{Name: "demo, 20 clients, GOGC=50", Demo: true, Clients: 20, Hours: hours, Env: map[string]string{"GOGC": "50"}},
		scenario{Name: "demo, 20 clients, GOMEMLIMIT=32MiB", Demo: true, Clients: 20, Hours: hours, Env: map[string]string{"GOMEMLIMIT": "32MiB"}},
	)
	return list
}

// sample is the process's memory at one moment.
type sample struct {
	HeapLive   uint64 `json:"heap_live"`   // live heap after the last GC
	HeapInuse  uint64 `json:"heap_inuse"`  // heap spans in use (objects + fragmentation)
	GoResident uint64 `json:"go_resident"` // memory the Go runtime holds from the OS, minus what it returned
	RSS        uint64 `json:"rss"`         // resident set size, from the OS
	Footprint  uint64 `json:"footprint"`   // dirty memory the OS charges the process (stage samples only)
	Goroutines int    `json:"goroutines"`
}

type result struct {
	Scenario   scenario      `json:"scenario"`
	Boot       sample        `json:"boot"`      // after boot, before sign-in (GC first)
	SignIn     *sample       `json:"sign_in"`   // after first-run setup, before any socket (GC first); nil without clients
	Ready      sample        `json:"ready"`     // clients connected, snapshots received (GC first)
	End        sample        `json:"end"`       // after the run (GC first)
	Floor      sample        `json:"floor"`     // after End, with free memory returned to the OS (debug.FreeOSMemory)
	PeakHeap   uint64        `json:"peak_heap"` // max heap in use over the run's periodic samples
	PeakRSS    uint64        `json:"peak_rss"`  // max RSS over the run's periodic samples
	MaxRSS     uint64        `json:"max_rss"`   // getrusage high-water mark, sign-in included
	PeakFoot   uint64        `json:"peak_foot"` // the OS's footprint high-water mark, where it has one
	CPU        time.Duration `json:"cpu"`       // user+system CPU during the run
	Minutes    float64       `json:"minutes"`   // simulated (demo) or real (idle) minutes covered
	Frames     int64         `json:"frames"`    // frames received by all clients during the run
	Bytes      int64         `json:"bytes"`
	Resyncs    int64         `json:"resyncs"` // snapshots beyond each client's first: should be 0
	GCs        uint64        `json:"gcs"`
	GoMaxProcs int           `json:"gomaxprocs"`
	Steps      []namedSample `json:"steps,omitempty"` // boot, one component at a time (GC first)
}

type namedSample struct {
	Name string `json:"name"`
	sample
}

const resultPrefix = "FOOTPRINT_RESULT "

// TestFootprint runs every scenario in its own child process and prints the
// results as a Markdown table.
func TestFootprint(t *testing.T) {
	if os.Getenv("FOOTPRINT_SCENARIO") != "" {
		t.Skip("child process")
	}
	only, err := regexp.Compile(os.Getenv("FOOTPRINT_ONLY"))
	if err != nil {
		t.Fatal(err)
	}
	var results []result
	for _, sc := range scenarios() {
		if !only.MatchString(sc.Name) {
			continue
		}
		spec, _ := json.Marshal(sc)
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestScenario$", "-test.v", "-test.timeout=20m")
		cmd.Env = append(os.Environ(), "FOOTPRINT_SCENARIO="+string(spec))
		for k, v := range sc.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		start := time.Now()
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v\n%s", sc.Name, err, out.String())
		}
		var r result
		found := false
		for line := range strings.SplitSeq(out.String(), "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), resultPrefix); ok {
				if err := json.Unmarshal([]byte(rest), &r); err != nil {
					t.Fatal(err)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: no result\n%s", sc.Name, out.String())
		}
		if os.Getenv("FOOTPRINT_VERBOSE") != "" {
			t.Logf("%s output:\n%s", sc.Name, out.String())
		}
		t.Logf("%s: done in %s", sc.Name, time.Since(start).Round(time.Second))
		results = append(results, r)
	}
	fmt.Println(table(results))
	checkBudgets(t, results)
}

func mib(b uint64) string { return fmt.Sprintf("%.1f", float64(b)/(1<<20)) }

func table(rs []result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s/%s, GOMAXPROCS %d, %s\n\n", runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), runtime.Version())
	b.WriteString("| Scenario | RSS boot | RSS signed in | RSS ready | RSS peak (run) | RSS end | Max RSS | Footprint boot | Footprint ready | Footprint end | Footprint floor | Footprint peak |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range rs {
		signIn := "—"
		if r.SignIn != nil {
			signIn = mib(r.SignIn.RSS)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			r.Scenario.Name, mib(r.Boot.RSS), signIn, mib(r.Ready.RSS), mib(r.PeakRSS), mib(r.End.RSS), mib(r.MaxRSS),
			mib(r.Boot.Footprint), mib(r.Ready.Footprint), mib(r.End.Footprint), mib(r.Floor.Footprint), mib(r.PeakFoot))
	}
	b.WriteString("\n| Scenario | Heap live end | Peak heap in use | Go-held end | Go-held floor | Goroutines end | CPU / minute | Frames | MiB sent | Resyncs | GCs |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range rs {
		perMin := "—"
		if r.Minutes > 0 {
			perMin = time.Duration(float64(r.CPU) / r.Minutes).Round(10 * time.Microsecond).String()
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %d | %s | %d | %s | %d | %d |\n",
			r.Scenario.Name, mib(r.End.HeapLive), mib(r.PeakHeap), mib(r.End.GoResident), mib(r.Floor.GoResident), r.End.Goroutines,
			perMin, r.Frames, mib(uint64(r.Bytes)), r.Resyncs, r.GCs)
	}
	b.WriteString("\nBoot, one component at a time: footprint / RSS / Go-held\n\n")
	b.WriteString("| Scenario | process | + store | + vault | + auth | + graph | + server |\n|---|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range rs {
		if len(r.Steps) == 0 {
			continue
		}
		fmt.Fprintf(&b, "| %s |", r.Scenario.Name)
		for _, s := range append(r.Steps, namedSample{"server", r.Boot}) {
			fmt.Fprintf(&b, " %s / %s / %s |", mib(s.Footprint), mib(s.RSS), mib(s.GoResident))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nMemory in MiB. CPU / minute is per simulated minute for demo rows, per real minute for empty-hub rows.\n")
	return b.String()
}

// BenchmarkArgon2id is one password hash at the hub's cost (auth's
// defaultParams: 19 MiB, t=2, p=1), which is what a sign-in costs.
func BenchmarkArgon2id(b *testing.B) {
	salt := make([]byte, 16)
	for b.Loop() {
		argon2.IDKey([]byte("correct horse battery"), salt, 2, 19*1024, 1, 32)
	}
}

// TestScenario is the child: it runs one scenario and prints its result.
func TestScenario(t *testing.T) {
	spec := os.Getenv("FOOTPRINT_SCENARIO")
	if spec == "" {
		t.Skip("run through TestFootprint")
	}
	var sc scenario
	if err := json.Unmarshal([]byte(spec), &sc); err != nil {
		t.Fatal(err)
	}
	r := run(t, sc)
	out, _ := json.Marshal(r)
	fmt.Println(resultPrefix + string(out))
}

// fakeClock is the demo's real-time clock, advanced by the test.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// run boots the hub the way cmd/chadash's newApp does, then runs sc.
func run(t *testing.T, sc scenario) result {
	if sc.Baseline {
		time.Sleep(time.Second)
		r := result{Scenario: sc, GoMaxProcs: runtime.GOMAXPROCS(0), Boot: measure(true)}
		r.Ready = r.Boot
		if sc.Argon2 {
			// One password hash at the hub's cost (auth.defaultParams), then
			// give the memory back: does the OS stop charging for it?
			argon2.IDKey([]byte("calibration"), make([]byte, 16), 2, 19*1024, 1, 32)
			r.Ready = measure(true)
		}
		r.End = r.Ready
		r.MaxRSS = maxRSS()
		_, r.PeakFoot = footprint()
		r.PeakRSS, r.PeakHeap = r.Ready.RSS, r.Ready.HeapInuse
		debug.FreeOSMemory()
		time.Sleep(time.Second)
		r.Floor = measure(true)
		return r
	}
	ctx := t.Context()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	r := result{Scenario: sc, GoMaxProcs: runtime.GOMAXPROCS(0)}
	step := func(name string) { r.Steps = append(r.Steps, namedSample{name, measure(true)}) }
	step("process")

	st, err := store.Open(ctx, filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	step("store")
	if _, err := vault.Open(ctx, st, vault.KeyConfig{Path: filepath.Join(dir, "secret.key"), Generate: true}, log); err != nil {
		t.Fatal(err)
	}
	step("vault")
	authSvc, err := auth.New(ctx, st, log, httpx.NewProxies(nil))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // auth.New computes its timing-equaliser hash (argon2id) in the background
	step("auth")

	g := graph.New()
	clock := &fakeClock{t: time.Now()}
	tp := topics.New(g, clock.Now())
	bg, stop := context.WithCancel(ctx)
	defer stop()
	var drv *demo.Driver
	if sc.Demo {
		drv, err = demo.New(tp, demo.Options{Seed: 1, At: demoStart, Clock: clock.Now, Log: log})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := g.Update(func(tx *graph.Tx) error {
			return tx.Upsert(graph.Resource{ID: "chadash:hub", Kind: "app", Name: "Cha Dash", State: "running", Health: graph.HealthOK})
		}); err != nil {
			t.Fatal(err)
		}
		go tp.RunClock(bg, 30*time.Second, time.Now)
	}
	step("graph")

	hub := realtime.New(tp.Source(), realtime.Options{Coalesce: coalesce, Server: "chadash/footprint", Log: log})
	h, err := server.New(server.Deps{Log: log, Store: st, Auth: authSvc, Realtime: hub, Proxies: httpx.NewProxies(nil), Demo: drv})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer func() {
		hub.Close()
		srv.Close()
	}()

	time.Sleep(time.Second)
	r.Boot = measure(true)

	var clients []*client
	if sc.Clients > 0 {
		jar := signIn(t, srv.URL)
		s := measure(true)
		r.SignIn = &s
		for range sc.Clients {
			c, err := dial(ctx, srv.URL, jar)
			if err != nil {
				t.Fatal(err)
			}
			defer c.conn.CloseNow()
			clients = append(clients, c)
		}
		for _, c := range clients {
			select {
			case <-c.snapped:
			case <-time.After(10 * time.Second):
				t.Fatal("no snapshot")
			}
		}
	}
	time.Sleep(time.Second)
	r.Ready = measure(true)
	frames0, bytes0 := totals(clients)
	gc0 := gcCount()

	cpu0 := cpuTime()
	var overhead time.Duration // sampling forks ps; that CPU isn't the hub's
	track := func() {
		c := cpuTime()
		s := measure(false)
		r.PeakHeap = max(r.PeakHeap, s.HeapInuse)
		r.PeakRSS = max(r.PeakRSS, s.RSS)
		overhead += cpuTime() - c
	}
	if sc.Demo {
		ticks := int(sc.Hours * 120) // 30 s per tick
		for i := range ticks {
			clock.Advance(30 * time.Second)
			drv.Step()
			time.Sleep(tickPause)
			if i%20 == 0 {
				track()
			}
		}
		r.Minutes = float64(ticks) / 2
		// Sanity: the day moved through its incidents.
		if got := drv.Now(); got.Sub(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)) < time.Duration(float64(demoStart)+sc.Hours*float64(time.Hour))-time.Minute {
			t.Fatalf("simulated clock at %s", got)
		}
	} else {
		start := time.Now()
		for time.Since(start) < sc.IdleFor {
			time.Sleep(5 * time.Second)
			track()
		}
		r.Minutes = time.Since(start).Minutes()
	}
	r.CPU = cpuTime() - cpu0 - overhead
	time.Sleep(200 * time.Millisecond) // let the last diffs land
	f1, b1 := totals(clients)
	r.Frames, r.Bytes = f1-frames0, b1-bytes0
	for _, c := range clients {
		r.Resyncs += c.snaps.Load() - 1
	}
	r.GCs = gcCount() - gc0
	r.End = measure(true)
	r.MaxRSS = maxRSS()
	_, r.PeakFoot = footprint()
	debug.FreeOSMemory()
	r.Floor = measure(true)
	if os.Getenv("FOOTPRINT_VERBOSE") != "" {
		fmt.Println(breakdown())
	}
	return r
}

// signIn completes first-run setup.
func signIn(t *testing.T, base string) http.CookieJar {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	body, _ := json.Marshal(map[string]string{"username": "owner", "password": "correct horse battery"})
	resp, err := c.Post(base+"/api/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	return jar
}

// client is a browser tab subscribed to every topic. It counts frames and
// throws them away, so its own memory stays small.
type client struct {
	conn    *websocket.Conn
	frames  atomic.Int64
	bytes   atomic.Int64
	snaps   atomic.Int64
	snapped chan struct{}
}

func dial(ctx context.Context, base string, jar http.CookieJar) (*client, error) {
	u := "ws" + strings.TrimPrefix(base, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: &http.Client{Jar: jar}})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(-1)
	send := func(v any) error {
		b, _ := json.Marshal(v)
		return conn.Write(ctx, websocket.MessageText, b)
	}
	if err := send(map[string]any{"op": "hello", "v": 1}); err != nil {
		return nil, err
	}
	if _, _, err := conn.Read(ctx); err != nil { // the hub's hello
		return nil, err
	}
	if err := send(map[string]any{"op": "sub", "id": 1, "topics": []string{topics.Graph, topics.Metrics, topics.Inbox}}); err != nil {
		return nil, err
	}
	c := &client{conn: conn, snapped: make(chan struct{})}
	go c.read(ctx)
	return c, nil
}

func (c *client) read(ctx context.Context) {
	head := make([]byte, 12) // {"op":"snap"
	for {
		_, r, err := c.conn.Reader(ctx)
		if err != nil {
			return
		}
		n, _ := io.ReadFull(r, head)
		rest, _ := io.Copy(io.Discard, r)
		c.frames.Add(1)
		c.bytes.Add(int64(n) + rest)
		if bytes.Equal(head[:n], []byte(`{"op":"snap"`)) {
			if c.snaps.Add(1) == 1 {
				close(c.snapped)
			}
		}
	}
}

func totals(cs []*client) (frames, bytes int64) {
	for _, c := range cs {
		frames += c.frames.Load()
		bytes += c.bytes.Load()
	}
	return frames, bytes
}

var memMetrics = []string{
	"/gc/heap/live:bytes",
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/heap/unused:bytes",
	"/memory/classes/total:bytes",
	"/memory/classes/heap/released:bytes",
	"/sched/goroutines:goroutines",
	"/gc/cycles/total:gc-cycles",
}

func readMetrics() map[string]uint64 {
	s := make([]metrics.Sample, len(memMetrics))
	for i, name := range memMetrics {
		s[i].Name = name
	}
	metrics.Read(s)
	out := map[string]uint64{}
	for _, m := range s {
		if m.Value.Kind() == metrics.KindUint64 {
			out[m.Name] = m.Value.Uint64()
		}
	}
	return out
}

// measure samples the process. A stage sample (stage true) runs a GC first
// and asks the OS for the footprint too, which is slow on macOS; periodic
// samples during a run do neither.
func measure(stage bool) sample {
	if stage {
		runtime.GC()
	}
	m := readMetrics()
	s := sample{
		HeapLive:   m["/gc/heap/live:bytes"],
		HeapInuse:  m["/memory/classes/heap/objects:bytes"] + m["/memory/classes/heap/unused:bytes"],
		GoResident: m["/memory/classes/total:bytes"] - m["/memory/classes/heap/released:bytes"],
		RSS:        rss(),
		Goroutines: int(m["/sched/goroutines:goroutines"]),
	}
	if stage {
		s.Footprint, _ = footprint()
	}
	return s
}

func gcCount() uint64 { return readMetrics()["/gc/cycles/total:gc-cycles"] }
