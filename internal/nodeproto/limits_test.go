package nodeproto

import (
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRecvGrowsWithTheData: a peer that declares a large frame and sends
// a few bytes makes the other side buffer what arrived, not what was
// declared (review finding 6: the 4 MiB frame was allocated up front).
func TestRecvGrowsWithTheData(t *testing.T) {
	type result struct {
		err   error
		bytes uint64
	}
	got := make(chan result, 1)
	h := newHub(t, func(ch *Channel) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, err := ch.Recv(t.Context(), 5*time.Second)
		runtime.ReadMemStats(&after)
		got <- result{err, after.TotalAlloc - before.TotalAlloc}
	})
	ch, err := Dial(t.Context(), h.url(), DialOptions{Identity: newID(t), Pins: []string{h.id.Pin}})
	if err != nil {
		t.Fatal(err)
	}
	h.next(t)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], DefaultMaxFrame-1)
	if _, err := ch.tc.Write(append(hdr[:], "only these"...)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	ch.Close()
	select {
	case r := <-got:
		if r.err == nil {
			t.Fatalf("a truncated frame read as whole: %v", r.err)
		}
		if r.bytes > 1<<20 {
			t.Fatalf("receiving a truncated %d-byte frame allocated %d bytes", DefaultMaxFrame-1, r.bytes)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Recv never returned")
	}
}

// TestHandshakeFrameLimit: the hub reads the hello under HandshakeMaxFrame
// and raises the limit for an admitted node (SetMaxFrame).
func TestHandshakeFrameLimit(t *testing.T) {
	errs := make(chan error, 1)
	h := newHub(t, func(ch *Channel) {
		_, err := ch.Recv(t.Context(), 5*time.Second)
		errs <- err
	})
	h.maxFrame = HandshakeMaxFrame
	ch, err := Dial(t.Context(), h.url(), DialOptions{Identity: newID(t), Pins: []string{h.id.Pin}})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	h.next(t)
	big := []byte(`"` + strings.Repeat("a", HandshakeMaxFrame) + `"`)
	if err := ch.Send(t.Context(), big, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("an oversized hello = %v, want ErrFrameTooLarge", err)
	}
}

func TestSetMaxFrameRaisesTheLimit(t *testing.T) {
	errs := make(chan error, 1)
	h := newHub(t, func(ch *Channel) {
		ch.SetMaxFrame(DefaultMaxFrame)
		_, err := ch.Recv(t.Context(), 5*time.Second)
		errs <- err
	})
	h.maxFrame = HandshakeMaxFrame
	ch, err := Dial(t.Context(), h.url(), DialOptions{Identity: newID(t), Pins: []string{h.id.Pin}})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	h.next(t)
	time.Sleep(50 * time.Millisecond) // let the hub raise its limit first
	if err := ch.Send(t.Context(), []byte(`"`+strings.Repeat("a", 2*HandshakeMaxFrame)+`"`), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("a frame under the raised limit = %v", err)
	}
	if ch.SetMaxFrame(0); ch.MaxFrame() != DefaultMaxFrame {
		t.Fatalf("SetMaxFrame(0) = %d, want the default", ch.MaxFrame())
	}
}
