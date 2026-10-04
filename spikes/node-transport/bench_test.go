package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// A full reconnect of an approved node over loopback: WebSocket upgrade,
// handshake, registry lookup, hello and welcome. Teardown isn't timed.
func BenchmarkReconnect(b *testing.B) {
	for _, d := range designs {
		b.Run(d.name, func(b *testing.B) {
			e := newEnv(b, d)
			n, _, c := e.enroll(e.url, nil)
			c.close()
			b.ResetTimer()
			for range b.N {
				ctx, cancel := context.WithCancel(e.ctx)
				s, err := n.Dial(ctx)
				if err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				cancel()
				<-s.Done()
				b.StartTimer()
			}
		})
	}
	// The floor: the same upgrade and one message each way, no inner handshake.
	b.Run("plain-ws", func(b *testing.B) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer ws.CloseNow()
			if _, m, err := ws.Read(r.Context()); err == nil {
				ws.Write(r.Context(), websocket.MessageText, m)
				ws.Read(r.Context())
			}
		}))
		defer srv.Close()
		url := "ws" + strings.TrimPrefix(srv.URL, "http")
		ctx := context.Background()
		b.ResetTimer()
		for range b.N {
			ws, _, err := websocket.Dial(ctx, url, nil)
			if err != nil {
				b.Fatal(err)
			}
			ws.Write(ctx, websocket.MessageText, []byte(`{"op":"hello"}`))
			if _, _, err := ws.Read(ctx); err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			ws.Close(websocket.StatusNormalClosure, "")
			b.StartTimer()
		}
	})
}

// One request/response on an established session.
func BenchmarkCall(b *testing.B) {
	for _, d := range designs {
		b.Run(d.name, func(b *testing.B) {
			e := newEnv(b, d)
			n, _, c := e.enroll(e.url, nil)
			defer c.close()
			b.ResetTimer()
			for range b.N {
				if _, err := e.hub.Call(e.ctx, n.ID(), "ping", nil, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// A 1 MiB echo through the RPC layer: throughput, including JSON.
func BenchmarkEcho1MiB(b *testing.B) {
	payload := strings.Repeat("x", 1<<20)
	for _, d := range designs {
		b.Run(d.name, func(b *testing.B) {
			e := newEnv(b, d)
			n, _, c := e.enroll(e.url, nil)
			defer c.close()
			b.SetBytes(2 << 20)
			b.ResetTimer()
			for range b.N {
				if _, err := e.hub.Call(e.ctx, n.ID(), "echo", payload, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
