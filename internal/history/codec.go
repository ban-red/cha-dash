package history

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// Point is one slot of a tier: the samples that landed in it, summarised.
// N = 0 is a gap; the other fields are then meaningless.
type Point struct {
	Avg, Min, Max float32
	N             uint32
}

// add folds a sample into p.
func (p *Point) add(v float32) {
	if p.N == 0 {
		*p = Point{Avg: v, Min: v, Max: v, N: 1}
		return
	}
	p.Avg = float32((float64(p.Avg)*float64(p.N) + float64(v)) / float64(p.N+1))
	p.Min, p.Max = min(p.Min, v), max(p.Max, v)
	p.N++
}

// fold summarises points into one, weighting each average by its count, so
// a coarse point is the exact average of the samples behind it.
func fold(points []Point) Point {
	var out Point
	var sum float64
	for _, p := range points {
		if p.N == 0 {
			continue
		}
		if out.N == 0 {
			out.Min, out.Max = p.Min, p.Max
		}
		out.Min, out.Max = min(out.Min, p.Min), max(out.Max, p.Max)
		out.N += p.N
		sum += float64(p.Avg) * float64(p.N)
	}
	if out.N > 0 {
		out.Avg = float32(sum / float64(out.N))
	}
	return out
}

// codecVersion is a chunk's first byte.
const codecVersion = 1

// errChunk is what decode returns for bytes it can't read.
var errChunk = errors.New("history: malformed chunk")

// encode packs a chunk's points. Trailing gaps are left out. The layout:
//
//	version byte
//	uvarint   count (points up to the last one present)
//	bitmap    ⌈count/8⌉ bytes, bit i set when point i is present
//	uvarints  N of each present point
//	bits      avg, min and max of each present point, XOR-encoded
//
// The averages are XORed against the previous average, and min and max
// against the same point's average, Gorilla-style: an unchanged value costs
// one bit, and a point from a single sample (min = max = avg) two more.
func encode(points []Point) []byte {
	count := len(points)
	for count > 0 && points[count-1].N == 0 {
		count--
	}
	out := make([]byte, 0, 1+binary.MaxVarintLen32+(count+7)/8+count*8)
	out = append(out, codecVersion)
	out = binary.AppendUvarint(out, uint64(count))
	bitmap := make([]byte, (count+7)/8)
	for i, p := range points[:count] {
		if p.N > 0 {
			bitmap[i/8] |= 1 << (i % 8)
		}
	}
	out = append(out, bitmap...)
	for _, p := range points[:count] {
		if p.N > 0 {
			out = binary.AppendUvarint(out, uint64(p.N))
		}
	}
	w := bitWriter{buf: out}
	var avg, lo, hi xorState
	for _, p := range points[:count] {
		if p.N == 0 {
			continue
		}
		a := math.Float32bits(p.Avg)
		avg.put(&w, a, avg.prev)
		avg.prev = a
		lo.put(&w, math.Float32bits(p.Min), a)
		hi.put(&w, math.Float32bits(p.Max), a)
	}
	return w.bytes()
}

// decode unpacks a chunk of size points.
func decode(b []byte, size int) ([]Point, error) {
	if len(b) < 2 || b[0] != codecVersion {
		return nil, errChunk
	}
	b = b[1:]
	c, n := binary.Uvarint(b)
	if n <= 0 || c > uint64(size) {
		return nil, fmt.Errorf("%w: %d points in a chunk of %d", errChunk, c, size)
	}
	b = b[n:]
	count := int(c)
	nb := (count + 7) / 8
	if len(b) < nb {
		return nil, errChunk
	}
	bitmap := b[:nb]
	b = b[nb:]
	points := make([]Point, size)
	for i := range count {
		if bitmap[i/8]&(1<<(i%8)) == 0 {
			continue
		}
		v, n := binary.Uvarint(b)
		if n <= 0 || v == 0 || v > math.MaxUint32 {
			return nil, errChunk
		}
		points[i].N = uint32(v)
		b = b[n:]
	}
	r := bitReader{buf: b}
	var avg, lo, hi xorState
	for i := range count {
		p := &points[i]
		if p.N == 0 {
			continue
		}
		a := avg.get(&r, avg.prev)
		avg.prev = a
		p.Avg = math.Float32frombits(a)
		p.Min = math.Float32frombits(lo.get(&r, a))
		p.Max = math.Float32frombits(hi.get(&r, a))
	}
	if r.err {
		return nil, errChunk
	}
	return points, nil
}

// xorState is one XOR stream: the previous value (for streams XORed against
// their own past) and the window of meaningful bits last written.
type xorState struct {
	prev        uint32
	lead, trail uint8
	window      bool
}

// put writes v XORed against ref:
//
//	0                     v = ref
//	1 0 bits              the meaningful bits fit the previous window
//	1 1 lead(5) len-1(5)  a new window, then its bits
func (s *xorState) put(w *bitWriter, v, ref uint32) {
	x := v ^ ref
	if x == 0 {
		w.write(0, 1)
		return
	}
	lead, trail := uint8(min(bits.LeadingZeros32(x), 31)), uint8(bits.TrailingZeros32(x))
	if s.window && lead >= s.lead && trail >= s.trail {
		w.write(0b10, 2)
		w.write(uint64(x>>s.trail), 32-s.lead-s.trail)
		return
	}
	sig := 32 - lead - trail
	w.write(0b11, 2)
	w.write(uint64(lead), 5)
	w.write(uint64(sig-1), 5)
	w.write(uint64(x>>trail), sig)
	s.lead, s.trail, s.window = lead, trail, true
}

func (s *xorState) get(r *bitReader, ref uint32) uint32 {
	if r.read(1) == 0 {
		return ref
	}
	if r.read(1) == 0 {
		if !s.window {
			r.err = true
			return ref
		}
		return ref ^ uint32(r.read(32-s.lead-s.trail))<<s.trail
	}
	lead := uint8(r.read(5))
	sig := uint8(r.read(5)) + 1
	if int(lead)+int(sig) > 32 {
		r.err = true
		return ref
	}
	s.lead, s.trail, s.window = lead, 32-lead-sig, true
	return ref ^ uint32(r.read(sig))<<s.trail
}

type bitWriter struct {
	buf  []byte
	free uint8 // unused low bits of the last byte
}

func (w *bitWriter) write(v uint64, n uint8) {
	for n > 0 {
		if w.free == 0 {
			w.buf = append(w.buf, 0)
			w.free = 8
		}
		take := min(n, w.free)
		chunk := byte(v>>(n-take)) & (1<<take - 1)
		w.buf[len(w.buf)-1] |= chunk << (w.free - take)
		w.free -= take
		n -= take
	}
}

func (w *bitWriter) bytes() []byte { return w.buf }

type bitReader struct {
	buf []byte
	pos uint // bit position
	err bool
}

func (r *bitReader) read(n uint8) uint64 {
	var v uint64
	for n > 0 {
		i := r.pos / 8
		if i >= uint(len(r.buf)) {
			r.err = true
			return 0
		}
		left := uint8(8 - r.pos%8)
		take := min(n, left)
		bits := uint64(r.buf[i]>>(left-take)) & (1<<take - 1)
		v = v<<take | bits
		r.pos += uint(take)
		n -= take
	}
	return v
}
