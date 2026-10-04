// Package testnet builds in-process stand-ins for what sits between a node and
// the hub: a reverse proxy that terminates TLS, a TCP passthrough, and a
// WebSocket relay that can watch, tamper with and replay messages. Everything
// listens on loopback and lives as long as the test.
package testnet

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TLSProxy terminates TLS with its own certificate and forwards plain HTTP to
// backend, as Traefik or Caddy do in front of the hub. WebSocket upgrades pass
// through. The returned client trusts the proxy's certificate, like a node
// trusting a public CA.
func TLSProxy(t testing.TB, backend string) (wssURL string, client *http.Client) {
	u, err := url.Parse(backend)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(u))
	t.Cleanup(srv.Close)
	return "wss" + strings.TrimPrefix(srv.URL, "https") + "/node", srv.Client()
}

// TLSReencryptProxy terminates TLS and opens a new TLS connection to an HTTPS
// backend, which is what a proxy does when the backend speaks TLS too. The
// proxy has no client certificate to give.
func TLSReencryptProxy(t testing.TB, backend string, backendTransport *http.Transport) (httpsURL string, client *http.Client) {
	u, err := url.Parse(backend)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.Transport = backendTransport
	srv := httptest.NewTLSServer(rp)
	t.Cleanup(srv.Close)
	return srv.URL, srv.Client()
}

// TCPPassthrough copies bytes to backendAddr without looking at them, like a
// TCP router with TLS passthrough.
func TCPPassthrough(t testing.TB, backendAddr string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b, err := net.Dial("tcp", backendAddr)
				if err != nil {
					return
				}
				defer b.Close()
				go io.Copy(b, c)
				io.Copy(c, b)
			}()
		}
	}()
	return ln.Addr().String()
}

// Relay sits between node and hub at the WebSocket message level: exactly
// where a TLS-terminating proxy sees plaintext WebSocket frames. It records
// every message, and Tap may rewrite or drop them (return nil).
type Relay struct {
	URL string
	Tap func(toHub bool, msg []byte) []byte

	mu    sync.Mutex
	all   [][]byte
	toHub [][]byte // node → hub messages of the latest connection
}

func NewRelay(t testing.TB, backendWS string, headers ...string) *Relay {
	r := &Relay{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		in, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		h := http.Header{}
		for _, k := range headers {
			h.Set(k, req.Header.Get(k))
		}
		out, _, err := websocket.Dial(req.Context(), backendWS, &websocket.DialOptions{HTTPHeader: h})
		if err != nil {
			in.Close(websocket.StatusInternalError, "backend")
			return
		}
		in.SetReadLimit(-1)
		out.SetReadLimit(-1)
		r.mu.Lock()
		r.toHub = nil
		r.mu.Unlock()
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()
		go func() { r.pump(ctx, in, out, true); cancel() }()
		r.pump(ctx, out, in, false)
		in.CloseNow()
		out.CloseNow()
	}))
	t.Cleanup(srv.Close)
	r.URL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/node"
	return r
}

func (r *Relay) pump(ctx context.Context, src, dst *websocket.Conn, toHub bool) {
	for {
		typ, msg, err := src.Read(ctx)
		if err != nil {
			return
		}
		r.mu.Lock()
		r.all = append(r.all, msg)
		if toHub {
			r.toHub = append(r.toHub, msg)
		}
		tap := r.Tap
		r.mu.Unlock()
		if tap != nil {
			if msg = tap(toHub, msg); msg == nil {
				continue
			}
		}
		if dst.Write(ctx, typ, msg) != nil {
			return
		}
	}
}

// Seen returns every message the relay carried, both directions.
func (r *Relay) Seen() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.all...)
}

// ToHub returns the node → hub messages of the latest connection.
func (r *Relay) ToHub() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.toHub...)
}

// Replay dials url and sends msgs in order, the way an attacker replays a
// recorded session, then reads until the far end gives up.
func Replay(ctx context.Context, url string, h http.Header, msgs [][]byte) {
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(-1)
	go func() {
		for _, m := range msgs {
			if ws.Write(ctx, websocket.MessageBinary, m) != nil {
				return
			}
			time.Sleep(10 * time.Millisecond) // let the far end answer each flight
		}
	}()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
	}
}
