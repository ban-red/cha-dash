package nodeproto

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Size limits. A frame is one JSON message on the channel; streams send
// many small ones rather than one large one.
const (
	// DefaultMaxFrame bounds one frame either way. An inspect of a busy host
	// or a compose file fits with room to spare; logs arrive as many frames.
	DefaultMaxFrame = 4 << 20
	// MinMaxFrame is the smallest limit a Channel accepts.
	MinMaxFrame = 4 << 10
	// HandshakeMaxFrame bounds frames before the peer is known: the hub
	// reads a node's hello under it, and only an admitted node's channel
	// gets the session's limit (SetMaxFrame).
	HandshakeMaxFrame = 64 << 10
	// readChunk is how much Recv reads at a time: a frame's buffer grows
	// with the bytes that actually arrive, never to the size a peer merely
	// declares.
	readChunk = 64 << 10
)

// ErrFrameTooLarge is a frame over the channel's limit: refused when
// sending, and fatal to the session when received.
var ErrFrameTooLarge = errors.New("nodeproto: frame too large")

// Channel is an authenticated, encrypted, ordered message pipe between the
// hub and one node: length-prefixed frames on TLS 1.3, which runs inside
// the WebSocket.
type Channel struct {
	tc       *tls.Conn
	ws       *websocket.Conn
	maxFrame atomic.Int64
	// Version is the protocol version ALPN settled on.
	Version int
	// PeerPin is the other side's key, proved in the handshake.
	PeerPin string

	wmu sync.Mutex
	rmu sync.Mutex
}

func newChannel(tc *tls.Conn, ws *websocket.Conn, maxFrame int) (*Channel, error) {
	cs := tc.ConnectionState()
	v, err := versionOf(cs.NegotiatedProtocol)
	if err != nil {
		return nil, err
	}
	pin, err := peerPin(cs)
	if err != nil {
		return nil, err
	}
	if maxFrame <= 0 {
		maxFrame = DefaultMaxFrame
	}
	c := &Channel{tc: tc, ws: ws, Version: v, PeerPin: pin}
	c.maxFrame.Store(int64(max(maxFrame, MinMaxFrame)))
	return c, nil
}

// MaxFrame is the channel's frame limit.
func (c *Channel) MaxFrame() int { return int(c.maxFrame.Load()) }

// SetMaxFrame changes the frame limit (DefaultMaxFrame when n <= 0, never
// below MinMaxFrame). The hub reads the hello under HandshakeMaxFrame and
// raises the limit once the node is admitted.
func (c *Channel) SetMaxFrame(n int) {
	if n <= 0 {
		n = DefaultMaxFrame
	}
	c.maxFrame.Store(int64(max(n, MinMaxFrame)))
}

// Send writes one frame. It gives up when ctx ends or after timeout (0: no
// timeout of its own), and the channel is unusable after a failed write.
func (c *Channel) Send(ctx context.Context, msg []byte, timeout time.Duration) error {
	if limit := c.MaxFrame(); len(msg) > limit {
		return fmt.Errorf("%w: %d bytes, the limit is %d", ErrFrameTooLarge, len(msg), limit)
	}
	buf := make([]byte, 4+len(msg))
	binary.BigEndian.PutUint32(buf, uint32(len(msg)))
	copy(buf[4:], msg)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	_ = c.tc.SetWriteDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = c.tc.SetWriteDeadline(time.Unix(1, 0)) })
	defer stop()
	_, err := c.tc.Write(buf)
	return err
}

// Recv reads one frame, waiting at most idle (0: forever) for it to start
// arriving, or until ctx ends.
func (c *Channel) Recv(ctx context.Context, idle time.Duration) ([]byte, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	var deadline time.Time
	if idle > 0 {
		deadline = time.Now().Add(idle)
	}
	_ = c.tc.SetReadDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = c.tc.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()
	var hdr [4]byte
	if _, err := io.ReadFull(c.tc, hdr[:]); err != nil {
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case idle > 0 && !time.Now().Before(deadline):
			return nil, ErrIdle
		}
		return nil, err
	}
	size := binary.BigEndian.Uint32(hdr[:])
	if limit := c.MaxFrame(); int64(size) > int64(limit) {
		return nil, fmt.Errorf("%w: the peer sent %d bytes, the limit is %d", ErrFrameTooLarge, size, limit)
	}
	// Grow with what arrives: a peer that declares 4 MiB and sends nothing
	// holds one chunk (64 KiB), not the whole frame.
	buf := make([]byte, 0, min(int(size), readChunk))
	for len(buf) < int(size) {
		n := min(int(size)-len(buf), readChunk)
		buf = slices.Grow(buf, n)
		if _, err := io.ReadFull(c.tc, buf[len(buf):len(buf)+n]); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		buf = buf[:len(buf)+n]
	}
	return buf, nil
}

// Close drops the connection at once. Frames are length-prefixed, so a
// truncated one is never mistaken for a whole one, and waiting for the
// peer's close handshake would only hold a dead connection open longer.
func (c *Channel) Close() error { return c.ws.CloseNow() }
