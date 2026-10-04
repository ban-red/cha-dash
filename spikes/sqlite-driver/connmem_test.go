package sqlitedriver

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ConnMem is what one connection-memory child measured.
type ConnMem struct {
	Variant  string
	DocBytes int
	Base     float64 // RSS, MiB: open, migrated, seeded
	Warm     float64 // after 10 saves of the document and a load on each reader
	Closed   float64 // after the idle readers are closed (SetMaxIdleConns(0))
}

// TestConnMemory asks how much memory the pool keeps after handling large
// layouts, and whether closing idle readers gives it back. Wasm memory
// (ncruces) grows per connection and never shrinks until the connection
// closes. Run with SPIKE_CONNMEM=1.
func TestConnMemory(t *testing.T) {
	if os.Getenv("SPIKE_CONNMEM") == "" {
		t.Skip("set SPIKE_CONNMEM=1")
	}
	rounds, _ := strconv.Atoi(os.Getenv("SPIKE_ROUNDS"))
	rounds = max(rounds, 1)
	fmt.Printf("\nMedians of %d round(s), RSS in MiB:\n\n", rounds)
	fmt.Println("| variant | layout | seeded | warm | readers closed | growth (warm − seeded) | held by 4 idle readers |")
	fmt.Println("|---|---|---|---|---|---|---|")
	for _, size := range []int{64 << 10, 1 << 20} {
		for _, name := range []string{"modernc", "ncruces"} {
			var base, warm, closed []float64
			for range rounds {
				cmd := exec.Command(os.Args[0], "-test.run=^TestConnMemoryChild$", "-test.count=1")
				cmd.Env = append(os.Environ(), fmt.Sprintf("SPIKE_CONNMEM_CHILD=%s:%d", name, size))
				var out bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &out
				if err := cmd.Run(); err != nil {
					t.Fatalf("%v\n%s", err, out.String())
				}
				var m ConnMem
				for _, line := range strings.Split(out.String(), "\n") {
					if j, ok := strings.CutPrefix(line, "CONNMEM "); ok {
						json.Unmarshal([]byte(j), &m)
					}
				}
				base, warm, closed = append(base, m.Base), append(warm, m.Warm), append(closed, m.Closed)
			}
			b, w, c := median(base), median(warm), median(closed)
			fmt.Printf("| %s | %d KiB | %.1f | %.1f | %.1f | %+.1f | %.1f |\n", name, size>>10, b, w, c, w-b, w-c)
		}
	}
}

func TestConnMemoryChild(t *testing.T) {
	spec := os.Getenv("SPIKE_CONNMEM_CHILD")
	if spec == "" {
		t.Skip("child process only")
	}
	name, sizeStr, _ := strings.Cut(spec, ":")
	size, _ := strconv.Atoi(sizeStr)
	v, err := Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	st, err := Open(ctx, v, filepath.Join(t.TempDir(), "chadash.db"), testKEK)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fx, err := Seed(ctx, st, 10, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Touch every reader once so all five connections exist.
	var conns []*sql.Conn
	for range ReadPoolSize {
		c, err := st.R.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		c.Close()
	}
	settle()
	m := ConnMem{Variant: name, DocBytes: size, Base: currentRSSMiB(t)}

	doc := LayoutDoc(size, 1)
	var rev int64
	for range 10 {
		if rev, err = SaveLayout(ctx, st, fx.BoardID, rev, doc, fx.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	conns = conns[:0]
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
	doc = ""
	settle()
	m.Warm = currentRSSMiB(t)

	st.R.SetMaxIdleConns(0) // what SetConnMaxIdleTime would do after the timeout
	settle()
	m.Closed = currentRSSMiB(t)

	j, _ := json.Marshal(m)
	fmt.Println("CONNMEM " + string(j))
}
