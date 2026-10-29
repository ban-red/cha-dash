package sim

import (
	"bytes"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the JSON snapshots in sim/snapshots")

// snapshotSeed is the seed of the committed snapshots.
const snapshotSeed = 1

// snapshotBudget keeps the committed snapshots small enough to load in a
// prototype without thinking about it.
const snapshotBudget = 1 << 20

// TestSnapshots is the golden test for sim/snapshots. Regenerate with:
//
//	go test ./sim -run TestSnapshots -update
func TestSnapshots(t *testing.T) {
	files := map[string][]byte{}

	s := mustNew(t, snapshotSeed)
	files["healthy.json"] = EncodeState(s.Snapshot())
	files["timeline.json"] = EncodeTimeline(s.Record(24*time.Hour, 30*time.Minute))

	s = mustNew(t, snapshotSeed)
	s.Advance(DefaultAllOpen)
	files["incidents.json"] = EncodeState(s.Snapshot())

	total := 0
	for _, name := range slices.Sorted(maps.Keys(files)) {
		got := files[name]
		total += len(got)
		path := filepath.Join("snapshots", name)
		if *update {
			if err := os.MkdirAll("snapshots", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s (%d bytes)", path, len(got))
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run: go test ./sim -run TestSnapshots -update)", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is out of date (run: go test ./sim -run TestSnapshots -update)", path)
		}
	}
	if total > snapshotBudget {
		t.Errorf("snapshots total %d bytes, over the %d-byte budget", total, snapshotBudget)
	}
}
