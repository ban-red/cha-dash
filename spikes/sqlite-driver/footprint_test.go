package sqlitedriver

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Footprint is what one child process measured for one variant.
type Footprint struct {
	Variant string

	FirstQuery time.Duration // process's first connection: open + SELECT 1
	OpenTotal  time.Duration // both pools, journal check, ping
	Migrate    time.Duration // the hub's migrations on an empty file
	Workload   time.Duration // the fixed workload below

	RSSIdleMiB   float64 // after open, migrate and seed: the hub at rest
	RSSWarmMiB   float64 // after the workload, every reader warmed with the 1 MiB layout, GC
	RSSClosedMiB float64 // then with both pools closed, GC: Warm − Closed is what the connections hold
	MaxRSSMiB    float64 // getrusage high-water mark over the whole run
	HeapSysMiB   float64 // runtime.MemStats.HeapSys at the end
	GoSysMiB     float64 // runtime.MemStats.Sys at the end
	TotalAllocM  float64 // MiB allocated on the Go heap over the run
	NumGC        uint32

	Mixed MixedResult

	InitClock map[string]float64 `json:",omitempty"` // package init ms, filled in by the parent
	InitBytes map[string]float64 `json:",omitempty"`
}

// TestFootprint runs the workload once per variant, each in a fresh child
// process, so peak RSS and first-query latency belong to one driver. Both
// drivers are linked into the test binary; untouched code isn't resident,
// and the "none" child measures the floor. Run with SPIKE_FOOTPRINT=1.
func TestFootprint(t *testing.T) {
	if os.Getenv("SPIKE_FOOTPRINT") == "" {
		t.Skip("set SPIKE_FOOTPRINT=1")
	}
	names := []string{"none"}
	for _, v := range Variants() {
		names = append(names, v.Name)
	}
	rounds, _ := strconv.Atoi(os.Getenv("SPIKE_ROUNDS"))
	rounds = max(rounds, 1)
	var all []Footprint
	for r := range rounds {
		for _, name := range names {
			fp, err := runChild(name)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			t.Logf("round %d %s: first query %v, RSS idle %.1f MiB, peak %.1f MiB", r+1, name, fp.FirstQuery, fp.RSSIdleMiB, fp.MaxRSSMiB)
			all = append(all, fp)
		}
	}
	if p := os.Getenv("SPIKE_FOOTPRINT_OUT"); p != "" {
		out, _ := json.MarshalIndent(all, "", "  ")
		if err := os.WriteFile(p, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Printf("\nMedians of %d round(s):\n\n", rounds)
	printTable(names, all)
}

// printTable prints per-variant medians as a Markdown table.
func printTable(names []string, all []Footprint) {
	rows := []struct {
		label string
		get   func(Footprint) float64
		unit  string
	}{
		{"first query, cold (ms)", func(f Footprint) float64 { return ms(f.FirstQuery) }, "%.2f"},
		{"open both pools (ms)", func(f Footprint) float64 { return ms(f.OpenTotal) }, "%.2f"},
		{"migrate v0 schema (ms)", func(f Footprint) float64 { return ms(f.Migrate) }, "%.2f"},
		{"package init (ms)", func(f Footprint) float64 { return f.InitClock[driverOf(f.Variant)] }, "%.2f"},
		{"package init heap (KiB)", func(f Footprint) float64 { return f.InitBytes[driverOf(f.Variant)] / 1024 }, "%.0f"},
		{"fixed workload (ms)", func(f Footprint) float64 { return ms(f.Workload) }, "%.0f"},
		{"RSS idle, seeded (MiB)", func(f Footprint) float64 { return f.RSSIdleMiB }, "%.1f"},
		{"RSS warm, after workload (MiB)", func(f Footprint) float64 { return f.RSSWarmMiB }, "%.1f"},
		{"held by open connections (MiB)", func(f Footprint) float64 { return f.RSSWarmMiB - f.RSSClosedMiB }, "%.1f"},
		{"peak RSS, getrusage (MiB)", func(f Footprint) float64 { return f.MaxRSSMiB }, "%.1f"},
		{"Go heap Sys at end (MiB)", func(f Footprint) float64 { return f.HeapSysMiB }, "%.1f"},
		{"mixed: reads/s", func(f Footprint) float64 { return float64(f.Mixed.Reads) / f.Mixed.Duration.Seconds() }, "%.0f"},
		{"mixed: read p50 (µs)", func(f Footprint) float64 { return us(f.Mixed.ReadP50) }, "%.1f"},
		{"mixed: read p99 (µs)", func(f Footprint) float64 { return us(f.Mixed.ReadP99) }, "%.1f"},
		{"mixed: read max (ms)", func(f Footprint) float64 { return ms(f.Mixed.ReadMax) }, "%.1f"},
		{"mixed: write rounds/s", func(f Footprint) float64 { return float64(f.Mixed.Writes) / f.Mixed.Duration.Seconds() }, "%.0f"},
		{"mixed: write round p50 (ms)", func(f Footprint) float64 { return ms(f.Mixed.WriteP50) }, "%.1f"},
		{"mixed: errors", func(f Footprint) float64 { return float64(f.Mixed.Errors) }, "%.0f"},
	}
	fmt.Printf("| | %s |\n|---|%s\n", strings.Join(names, " | "), strings.Repeat("---|", len(names)))
	for _, r := range rows {
		cells := make([]string, len(names))
		for i, n := range names {
			var xs []float64
			for _, f := range all {
				if f.Variant == n {
					xs = append(xs, r.get(f))
				}
			}
			cells[i] = fmt.Sprintf(r.unit, median(xs))
		}
		fmt.Printf("| %s | %s |\n", r.label, strings.Join(cells, " | "))
	}
}

func driverOf(variant string) string {
	if variant == "none" {
		return ""
	}
	d, _, _ := strings.Cut(variant, "+")
	return d
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	slices.Sort(xs)
	if len(xs)%2 == 1 {
		return xs[len(xs)/2]
	}
	return (xs[len(xs)/2-1] + xs[len(xs)/2]) / 2
}

func ms(d time.Duration) float64 { return float64(d) / 1e6 }
func us(d time.Duration) float64 { return float64(d) / 1e3 }

var initRE = regexp.MustCompile(`^init (\S+) @[\d.]+ ms, ([\d.]+) ms clock, (\d+) bytes`)

func runChild(name string) (Footprint, error) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestFootprintChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "SPIKE_CHILD="+name, "GODEBUG=inittrace=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return Footprint{}, fmt.Errorf("%v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	var fp Footprint
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if j, ok := strings.CutPrefix(sc.Text(), "FOOTPRINT "); ok {
			if err := json.Unmarshal([]byte(j), &fp); err != nil {
				return fp, err
			}
		}
	}
	if fp.Variant == "" {
		return fp, fmt.Errorf("no FOOTPRINT line:\n%s", stdout.String())
	}
	// Package init cost, grouped by driver.
	fp.InitClock, fp.InitBytes = map[string]float64{}, map[string]float64{}
	sc = bufio.NewScanner(&stderr)
	for sc.Scan() {
		m := initRE.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		group := ""
		switch {
		case strings.HasPrefix(m[1], "modernc.org/"):
			group = "modernc"
		case strings.HasPrefix(m[1], "github.com/ncruces/"), strings.HasPrefix(m[1], "lukechampine.com/"):
			group = "ncruces"
		default:
			continue
		}
		clock, _ := strconv.ParseFloat(m[2], 64)
		b, _ := strconv.ParseFloat(m[3], 64)
		fp.InitClock[group] += clock
		fp.InitBytes[group] += b
	}
	return fp, nil
}

func TestFootprintChild(t *testing.T) {
	name := os.Getenv("SPIKE_CHILD")
	if name == "" {
		t.Skip("child process only")
	}
	fp := Footprint{Variant: name}
	defer func() {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		fp.HeapSysMiB = mib(ms.HeapSys)
		fp.GoSysMiB = mib(ms.Sys)
		fp.TotalAllocM = mib(ms.TotalAlloc)
		fp.NumGC = ms.NumGC
		fp.MaxRSSMiB = maxRSSMiB()
		j, _ := json.Marshal(fp)
		fmt.Println("FOOTPRINT " + string(j))
	}()
	if name == "none" {
		fp.RSSIdleMiB = currentRSSMiB(t)
		settle()
		fp.RSSWarmMiB = currentRSSMiB(t)
		fp.RSSClosedMiB = fp.RSSWarmMiB
		return
	}
	v, err := Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "chadash.db")

	start := time.Now()
	db, err := v.Open(path, false, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatal(err)
	}
	fp.FirstQuery = time.Since(start)
	db.Close()

	start = time.Now()
	st, err := Open(ctx, v, path, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fp.OpenTotal = time.Since(start)
	start = time.Now()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fp.Migrate = time.Since(start)
	fx, err := Seed(ctx, st, 10, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := LookupSession(ctx, st, fx.TokenHashes[0]); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	fp.RSSIdleMiB = currentRSSMiB(t)

	// The fixed workload: about what a busy week of edits and polling
	// does, compressed.
	start = time.Now()
	var rev int64
	doc := LayoutDoc(layoutSize, 1)
	for range 100 {
		if rev, err = SaveLayout(ctx, st, fx.BoardID, rev, doc, fx.OwnerID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadLayout(ctx, st, fx.BoardID); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 10_000 {
		if err := LookupSession(ctx, st, fx.TokenHashes[i%len(fx.TokenHashes)]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2_000 {
		if err := AppendAudit(ctx, st, i); err != nil {
			t.Fatal(err)
		}
	}
	for range 50 {
		if err := InsertMetrics(ctx, st, 300); err != nil {
			t.Fatal(err)
		}
	}
	fp.Workload = time.Since(start)
	fp.Mixed = Mixed(ctx, st, fx, 3*time.Second, ReadPoolSize, layoutSize)
	if fp.Mixed.Errors > 0 {
		t.Errorf("mixed phase: %d errors, first: %v", fp.Mixed.Errors, fp.Mixed.FirstErr)
	}

	// Warm every reader with the big layout, as a day of first paints
	// would, then see what the open connections hold.
	var conns []*sql.Conn
	for range ReadPoolSize {
		c, err := st.R.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var s string
		if err := c.QueryRowContext(ctx, `SELECT document FROM layouts ORDER BY revision DESC LIMIT 1`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		c.Close()
	}
	settle()
	fp.RSSWarmMiB = currentRSSMiB(t)
	st.Close()
	settle()
	fp.RSSClosedMiB = currentRSSMiB(t)
}

func settle() {
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
	time.Sleep(100 * time.Millisecond)
}

func mib(b uint64) float64 { return float64(b) / (1 << 20) }

// maxRSSMiB is getrusage's high-water mark: bytes on macOS, KiB on Linux.
func maxRSSMiB() float64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	if runtime.GOOS == "darwin" {
		return float64(ru.Maxrss) / (1 << 20)
	}
	return float64(ru.Maxrss) / 1024
}

// currentRSSMiB asks ps, which works on both macOS and Linux without cgo.
func currentRSSMiB(t *testing.T) float64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	kib, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatal(err)
	}
	return kib / 1024
}
