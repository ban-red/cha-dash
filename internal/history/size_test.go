package history

import (
	"testing"
	"time"

	"dash.cha.sh/sim"
)

// TestChunkSizeOnDemoHome encodes 48 hours of the Demo Home, sampled every
// 30 seconds, into 1-minute and 15-minute chunks, and checks the bytes a
// point against the budget 13 · History assumes (8). Run with -v for the
// numbers.
func TestChunkSizeOnDemoHome(t *testing.T) {
	if testing.Short() {
		t.Skip("simulates 48 hours")
	}
	s, err := sim.New(1, sim.WithSampleInterval(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	start := s.Now().UnixMilli()
	minutes := map[string][]Point{} // series → 1-minute points over 48 hours
	const span = 48 * 60
	for _, ev := range s.Advance(48 * time.Hour) {
		if ev.Type != sim.EventMetrics {
			continue
		}
		i := int((ev.At.UnixMilli() - start) / time.Minute.Milliseconds())
		if i >= span {
			continue
		}
		for _, smp := range ev.Samples {
			if smp.Value.Valid() {
				key := smp.Resource + "#" + smp.Metric
				if minutes[key] == nil {
					minutes[key] = make([]Point, span)
				}
				minutes[key][i].add(float32(smp.Value))
			}
		}
	}

	measure := func(name string, perSeries func([]Point) [][]Point) float64 {
		var bytes, points, chunks int
		for _, pts := range minutes {
			for _, c := range perSeries(pts) {
				bytes += len(encode(c))
				chunks++
				for _, p := range c {
					if p.N > 0 {
						points++
					}
				}
			}
		}
		per := float64(bytes) / float64(points)
		t.Logf("%s: %d series, %d chunks, %d points, %d bytes: %.2f bytes a point", name, len(minutes), chunks, points, bytes, per)
		return per
	}
	split := func(pts []Point, n int) [][]Point {
		var out [][]Point
		for i := 0; i < len(pts); i += n {
			out = append(out, pts[i:min(i+n, len(pts))])
		}
		return out
	}
	m := measure("1m tier, 1-hour chunks", func(pts []Point) [][]Point { return split(pts, Minute.size()) })
	q := measure("15m tier, 6-hour chunks", func(pts []Point) [][]Point {
		quarters := make([]Point, 0, len(pts)/15)
		for i := 0; i < len(pts); i += 15 {
			quarters = append(quarters, fold(pts[i:i+15]))
		}
		return split(quarters, Quarter.size())
	})
	if m > 8 || q > 8 {
		t.Errorf("more than the 8 bytes a point 13 · History budgets for")
	}
}
