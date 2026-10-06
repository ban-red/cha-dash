package nodeproto

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func newID(t *testing.T) Identity {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewIdentity(k)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// hub is a test hub: it accepts on /node and hands each channel (or the
// error) to accepted.
type hub struct {
	id       Identity
	srv      *httptest.Server
	accepted chan accepted
	maxFrame int
}

type accepted struct {
	ch   *Channel
	pins []string
	err  error
}

func newHub(t *testing.T, serve func(*Channel)) *hub {
	t.Helper()
	h := &hub{id: newID(t), accepted: make(chan accepted, 8)}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch, pins, err := Accept(w, r, AcceptOptions{Identity: func([]string) Identity { return h.id }, MaxFrame: h.maxFrame})
		h.accepted <- accepted{ch, pins, err}
		if err == nil && serve != nil {
			serve(ch)
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hub) url() string { return "ws" + strings.TrimPrefix(h.srv.URL, "http") + Path }

func (h *hub) next(t *testing.T) accepted {
	t.Helper()
	select {
	case a := <-h.accepted:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("the hub accepted nothing")
	}
	return accepted{}
}

// echo serves a session whose handler echoes, streams and blocks on request.
func echo(o SessionOptions) func(*Channel) {
	return func(ch *Channel) {
		o.Handler = func(ctx context.Context, method string, params json.RawMessage, emit func(json.RawMessage) error) (json.RawMessage, error) {
			switch method {
			case "echo":
				return params, nil
			case "stream":
				for i := range 5 {
					if err := emit(json.RawMessage(`{"i":` + string(rune('0'+i)) + `}`)); err != nil {
						return nil, err
					}
				}
				return json.RawMessage(`"done"`), nil
			case "block":
				<-ctx.Done()
				return nil, ctx.Err()
			case "refuse":
				return nil, Errorf(CodeForbidden, "the node's policy doesn't allow that")
			case "panic":
				panic("boom")
			case "plain":
				return nil, errors.New("stack_locked: web is deploying; try again in a minute")
			}
			return nil, Errorf(CodeNotFound, "no verb %q", method)
		}
		s := NewSession(ch, o)
		_ = s.Run(context.Background())
	}
}

func TestMutualAuthAndRPC(t *testing.T) {
	h := newHub(t, echo(SessionOptions{}))
	node := newID(t)
	ch, err := Dial(t.Context(), h.url(), DialOptions{Identity: node, Pins: []string{h.id.Pin}})
	if err != nil {
		t.Fatal(err)
	}
	a := h.next(t)
	if a.err != nil {
		t.Fatal(a.err)
	}
	if a.ch.PeerPin != node.Pin || ch.PeerPin != h.id.Pin {
		t.Fatalf("pins: hub saw %s, node saw %s", a.ch.PeerPin, ch.PeerPin)
	}
	if len(a.pins) != 1 || a.pins[0] != h.id.Pin {
		t.Fatalf("accepted pins = %v", a.pins)
	}
	if ch.Version != 1 || a.ch.Version != 1 {
		t.Fatalf("versions %d/%d", ch.Version, a.ch.Version)
	}
	if v := ch.tc.ConnectionState().Version; v != tls.VersionTLS13 {
		t.Fatalf("TLS version %x", v)
	}
	s := NewSession(ch, SessionOptions{})
	go s.Run(t.Context())

	big := bytes.Repeat([]byte("a"), 300<<10)
	payload, _ := json.Marshal(map[string]string{"big": string(big)})
	got, err := s.Call(t.Context(), "echo", json.RawMessage(payload), nil)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("echo: %v (%d bytes)", err, len(got))
	}
	var items []string
	got, err = s.Call(t.Context(), "stream", nil, func(b json.RawMessage) { items = append(items, string(b)) })
	if err != nil || string(got) != `"done"` || len(items) != 5 || items[0] != `{"i":0}` {
		t.Fatalf("stream: %v %s %v", err, got, items)
	}
	_, err = s.Call(t.Context(), "refuse", nil, nil)
	if CodeOf(err) != CodeForbidden || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("refuse: %v", err)
	}
	// A plain error's text crosses verbatim (stacks encode codes in it).
	_, err = s.Call(t.Context(), "plain", nil, nil)
	if err == nil || err.Error() != "stack_locked: web is deploying; try again in a minute" || CodeOf(err) != CodeFailed {
		t.Fatalf("plain error = %v", err)
	}
	_, err = s.Call(t.Context(), "panic", nil, nil)
	if CodeOf(err) != CodeFailed || strings.Contains(err.Error(), "boom") {
		t.Fatalf("panic: %v", err)
	}
	s.Close()
	<-s.Done()
	if _, err := s.Call(t.Context(), "echo", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestImpostorHub(t *testing.T) {
	// The node pins the real hub, but an impostor answers at the URL.
	real, impostor := newID(t), newHub(t, nil)
	_, err := Dial(t.Context(), impostor.url(), DialOptions{Identity: newID(t), Pins: []string{real.Pin}})
	if !errors.Is(err, ErrHubNotPinned) {
		t.Fatalf("dial = %v, want ErrHubNotPinned", err)
	}
	// The impostor never completes a handshake, so it can't read a frame.
	if a := impostor.next(t); a.err == nil {
		t.Fatal("the impostor got a channel")
	}
}

func TestNoClientCertificate(t *testing.T) {
	h := newHub(t, nil)
	ws, _, err := websocket.Dial(t.Context(), h.url(), nil)
	if err != nil {
		t.Fatal(err)
	}
	nc := websocket.NetConn(t.Context(), ws, websocket.MessageBinary)
	tc := tls.Client(nc, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, NextProtos: alpn(Versions)}) //nolint:gosec // the test plays an anonymous client
	_ = tc.HandshakeContext(t.Context())
	// TLS 1.3 clients finish before the server checks their certificate, so
	// the refusal shows on the hub and on the client's first read.
	if a := h.next(t); a.err == nil {
		t.Fatal("a client without a certificate got a channel")
	}
	_ = tc.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := tc.Read(make([]byte, 1)); err == nil {
		t.Fatal("read succeeded")
	}
}

func TestNoCommonVersion(t *testing.T) {
	h := newHub(t, nil)
	node := newID(t)
	ws, _, err := websocket.Dial(t.Context(), h.url(), nil)
	if err != nil {
		t.Fatal(err)
	}
	nc := websocket.NetConn(t.Context(), ws, websocket.MessageBinary)
	cfg := clientConfig(node, []string{h.id.Pin})
	cfg.NextProtos = []string{"chadash-node/99"}
	err = tls.Client(nc, cfg).HandshakeContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("handshake = %v, want no application protocol", err)
	}
	if a := h.next(t); a.err == nil {
		t.Fatal("the hub accepted a node with no version in common")
	}
}

// relay sits where a reverse proxy would: it carries WebSocket messages
// between the node and the hub and records what the node sends.
type relay struct {
	mu       sync.Mutex
	fromNode [][]byte
	sawText  bytes.Buffer
}

func (rl *relay) handler(t *testing.T, target string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		out, _, err := websocket.Dial(r.Context(), target, &websocket.DialOptions{HTTPHeader: http.Header{PinsHeader: r.Header[PinsHeader]}})
		if err != nil {
			in.Close(websocket.StatusInternalError, "")
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		pipe := func(dst, src *websocket.Conn, record bool) {
			defer cancel()
			for {
				typ, b, err := src.Read(ctx)
				if err != nil {
					return
				}
				if record {
					rl.mu.Lock()
					rl.fromNode = append(rl.fromNode, b)
					rl.mu.Unlock()
				}
				rl.mu.Lock()
				rl.sawText.Write(b)
				rl.mu.Unlock()
				if dst.Write(ctx, typ, b) != nil {
					return
				}
			}
		}
		go pipe(out, in, true)
		pipe(in, out, false)
		in.CloseNow()
		out.CloseNow()
	})
}

func TestReplayAndCiphertextOnly(t *testing.T) {
	h := newHub(t, echo(SessionOptions{}))
	rl := &relay{}
	proxy := httptest.NewServer(rl.handler(t, h.url()))
	defer proxy.Close()
	node := newID(t)
	ch, err := Dial(t.Context(), "ws"+strings.TrimPrefix(proxy.URL, "http")+Path, DialOptions{Identity: node, Pins: []string{h.id.Pin}})
	if err != nil {
		t.Fatal(err)
	}
	if a := h.next(t); a.err != nil {
		t.Fatal(a.err)
	}
	s := NewSession(ch, SessionOptions{})
	go s.Run(t.Context())
	if _, err := s.Call(t.Context(), "echo", map[string]string{"secret": "pairing-plaintext-marker"}, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	<-s.Done()

	rl.mu.Lock()
	recorded := rl.fromNode
	seen := rl.sawText.String()
	rl.mu.Unlock()
	for _, leak := range []string{"pairing-plaintext-marker", `"op"`, "echo"} {
		if strings.Contains(seen, leak) {
			t.Errorf("the relay saw %q in clear", leak)
		}
	}

	// Replaying the node's recorded messages opens no channel: the hub's
	// fresh randomness makes the old Finished worthless.
	ws, _, err := websocket.Dial(t.Context(), h.url(), &websocket.DialOptions{HTTPHeader: http.Header{PinsHeader: {h.id.Pin}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range recorded {
		if err := ws.Write(t.Context(), websocket.MessageBinary, b); err != nil {
			break
		}
	}
	if a := h.next(t); a.err == nil {
		t.Fatal("a replayed handshake opened a channel")
	}
	ws.CloseNow()
}

func TestCancelReachesTheHandler(t *testing.T) {
	cancelled := make(chan struct{})
	h := newHub(t, func(ch *Channel) {
		s := NewSession(ch, SessionOptions{Handler: func(ctx context.Context, _ string, _ json.RawMessage, emit func(json.RawMessage) error) (json.RawMessage, error) {
			_ = emit(json.RawMessage(`"line"`))
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		}})
		_ = s.Run(context.Background())
	})
	ch, err := Dial(t.Context(), h.url(), DialOptions{Identity: newID(t), Pins: []string{h.id.Pin}})
	if err != nil {
		t.Fatal(err)
	}
	h.next(t)
	s := NewSession(ch, SessionOptions{})
	go s.Run(t.Context())
	ctx, cancel := context.WithCancel(t.Context())
	got := make(chan error, 1)
	go func() {
		_, err := s.Call(ctx, "logs", nil, func(json.RawMessage) { cancel() })
		got <- err
	}()
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("call = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never saw the cancel")
	}
}

func TestKeepAlive(t *testing.T) {
	o := SessionOptions{KeepAlive: 20 * time.Millisecond, IdleTimeout: 120 * time.Millisecond}
	h := newHub(t, echo(o))
	ch, err := Dial(t.Context(), h.url(), DialOptions{Identity: newID(t), Pins: []string{h.id.Pin}})
	if err != nil {
		t.Fatal(err)
	}
	h.next(t)
	s := NewSession(ch, o)
	go s.Run(t.Context())
	// Quiet for several idle timeouts: the pings keep it open.
	time.Sleep(500 * time.Millisecond)
	select {
	case <-s.Done():
		t.Fatalf("a quiet session ended: %v", s.Err())
	default:
	}
	if _, err := s.Call(t.Context(), "echo", "1", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// A hub that stops answering (it holds the channel but never reads) is
	// noticed within the idle timeout.
	mute := newHub(t, func(ch *Channel) { time.Sleep(5 * time.Second); ch.Close() })
	ch, err = Dial(t.Context(), mute.url(), DialOptions{Identity: newID(t), Pins: []string{mute.id.Pin}})
	if err != nil {
		t.Fatal(err)
	}
	mute.next(t)
	s = NewSession(ch, o)
	start := time.Now()
	err = s.Run(t.Context())
	if !errors.Is(err, ErrIdle) {
		t.Fatalf("run = %v, want ErrIdle", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %s to notice", d)
	}
}

func TestSizeLimits(t *testing.T) {
	// The hub accepts small frames only; the node may send large ones.
	var hubSession *Session
	ready := make(chan struct{})
	h := newHub(t, func(ch *Channel) {
		hubSession = NewSession(ch, SessionOptions{})
		close(ready)
		_ = hubSession.Run(context.Background())
	})
	h.maxFrame = MinMaxFrame
	ch, err := Dial(t.Context(), h.url(), DialOptions{Identity: newID(t), Pins: []string{h.id.Pin}, MaxFrame: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	h.next(t)
	<-ready
	if hubSession.Channel().MaxFrame() != MinMaxFrame {
		t.Fatalf("hub max frame %d", hubSession.Channel().MaxFrame())
	}
	// The hub refuses to send what's over its own limit.
	if _, err := hubSession.Call(t.Context(), "x", strings.Repeat("a", MinMaxFrame), nil); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized call = %v", err)
	}
	// A peer that sends more than the limit ends the session.
	s := NewSession(ch, SessionOptions{})
	go s.Run(t.Context())
	_, _ = s.Call(t.Context(), "x", strings.Repeat("a", 2*MinMaxFrame), nil)
	select {
	case <-hubSession.Done():
		if !errors.Is(hubSession.Err(), ErrFrameTooLarge) {
			t.Fatalf("hub session ended with %v", hubSession.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hub kept a session that sent an oversized frame")
	}
}

func TestBusy(t *testing.T) {
	h := newHub(t, echo(SessionOptions{MaxInFlight: 1}))
	ch, err := Dial(t.Context(), h.url(), DialOptions{Identity: newID(t), Pins: []string{h.id.Pin}})
	if err != nil {
		t.Fatal(err)
	}
	h.next(t)
	s := NewSession(ch, SessionOptions{})
	go s.Run(t.Context())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	blocked := make(chan error, 1)
	go func() { _, err := s.Call(ctx, "block", nil, nil); blocked <- err }()
	time.Sleep(200 * time.Millisecond) // the block is being served
	if _, err := s.Call(t.Context(), "echo", "1", nil); CodeOf(err) != CodeBusy {
		t.Fatalf("second call = %v, want busy", err)
	}
	cancel()
	<-blocked
	// The cancel freed the slot.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := s.Call(t.Context(), "echo", "1", nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still busy: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestKeys(t *testing.T) {
	k, _ := GenerateKey()
	pin := PinOf(k)
	pub, err := ParsePin(pin)
	if err != nil || Pin(pub) != pin || !ValidPin(pin) {
		t.Fatalf("pin round trip: %v", err)
	}
	for _, bad := range []string{"", "ed25519:", "ed25519:abc", "rsa:" + strings.TrimPrefix(pin, "ed25519:")} {
		if ValidPin(bad) {
			t.Errorf("%q parsed", bad)
		}
	}
	if fp := Fingerprint(pin); len(fp) != 39 || strings.Count(fp, ":") != 7 {
		t.Fatalf("fingerprint %q", fp)
	}
	path := filepath.Join(t.TempDir(), "node.key")
	if err := WriteKeyFile(path, k); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi.Mode(), err)
	}
	k2, err := ReadKeyFile(path)
	if err != nil || !k2.Equal(k) {
		t.Fatalf("read back: %v", err)
	}
	if _, err := ReadKeyFile(filepath.Join(t.TempDir(), "none")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := DecodeKey([]byte("not a key")); err == nil {
		t.Fatal("decoded garbage")
	}
}
