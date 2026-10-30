package history

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	shapes := map[string]func(i int) float32{
		"flat":     func(int) float32 { return 42 },
		"steps":    func(i int) float32 { return float32(i / 10) },
		"noisy":    func(int) float32 { return rng.Float32() * 100 },
		"large":    func(i int) float32 { return 1.2e12 + float32(i)*4096 },
		"negative": func(i int) float32 { return -float32(i) * 0.5 },
		"extremes": func(i int) float32 {
			return []float32{0, float32(math.Inf(1)), math.MaxFloat32, math.SmallestNonzeroFloat32, -0}[i%5]
		},
	}
	for name, shape := range shapes {
		for _, size := range []int{1, 7, 24, 60, 366} {
			points := make([]Point, size)
			for i := range points {
				if rng.IntN(5) == 0 {
					continue // a gap
				}
				v := shape(i)
				p := Point{Avg: v, Min: v, Max: v, N: 1}
				if rng.IntN(2) == 0 {
					p = Point{Avg: v, Min: v - 1, Max: v * 2, N: uint32(rng.IntN(1000) + 1)}
				}
				points[i] = p
			}
			got, err := decode(encode(points), size)
			if err != nil {
				t.Fatalf("%s/%d: %v", name, size, err)
			}
			for i := range points {
				if !same(got[i], points[i]) {
					t.Fatalf("%s/%d: point %d = %+v, want %+v", name, size, i, got[i], points[i])
				}
			}
		}
	}
}

func same(a, b Point) bool {
	if a.N != b.N {
		return false
	}
	if a.N == 0 {
		return true
	}
	eq := func(x, y float32) bool { return math.Float32bits(x) == math.Float32bits(y) }
	return eq(a.Avg, b.Avg) && eq(a.Min, b.Min) && eq(a.Max, b.Max)
}

func TestCodecTrimsTrailingGapsAndRefusesJunk(t *testing.T) {
	points := make([]Point, 60)
	points[3] = Point{Avg: 1, Min: 1, Max: 1, N: 1}
	b := encode(points)
	// Version, count, one bitmap byte and one N, then 21 bits: a new window
	// (12) and 1.0's 7 meaningful bits for the average, 1 bit each for min and max.
	if len(b) != 7 {
		t.Fatalf("encoded %d bytes, want 7", len(b))
	}
	if got, _ := decode(encode(make([]Point, 24)), 24); got == nil || got[0].N != 0 {
		t.Fatalf("an empty chunk should decode to gaps, got %v", got)
	}
	for _, junk := range [][]byte{nil, {9, 0}, {codecVersion, 200, 1}, {codecVersion, 1, 1}, b[:4]} {
		if _, err := decode(junk, 60); err == nil {
			t.Errorf("decode(%v) succeeded", junk)
		}
	}
	if _, err := decode(b, 2); err == nil {
		t.Error("a chunk longer than its tier should fail")
	}
}

func TestFoldWeightsByCount(t *testing.T) {
	p := fold([]Point{{Avg: 10, Min: 5, Max: 20, N: 3}, {}, {Avg: 2, Min: 2, Max: 2, N: 1}})
	if p != (Point{Avg: 8, Min: 2, Max: 20, N: 4}) {
		t.Fatalf("fold = %+v", p)
	}
	if fold([]Point{{}, {}}).N != 0 {
		t.Fatal("folding gaps should give a gap")
	}
	var q Point
	for _, v := range []float32{4, 8, 0} {
		q.add(v)
	}
	if q != (Point{Avg: 4, Min: 0, Max: 8, N: 3}) {
		t.Fatalf("add = %+v", q)
	}
}
