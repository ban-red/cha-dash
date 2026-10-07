package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/store"
)

// openSocket opens /ws as c and says hello.
func openSocket(t *testing.T, e *env, c *http.Client) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", &websocket.DialOptions{HTTPClient: c})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	b, _ := json.Marshal(map[string]any{"op": "hello", "v": 1})
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Read(ctx); err != nil { // hello back
		t.Fatal(err)
	}
	return ws
}

// closedWith reads until ws closes and returns its status; -1 if it's
// still open after a few seconds.
func closedWith(t *testing.T, ws *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return -1
			}
			return websocket.CloseStatus(err)
		}
	}
}

// stillOpen checks ws answers a ping.
func stillOpen(t *testing.T, ws *websocket.Conn, who string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	b, _ := json.Marshal(map[string]any{"op": "ping"})
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("%s: %v", who, err)
	}
	for {
		_, msg, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("%s's socket closed: %v", who, err)
		}
		if strings.Contains(string(msg), `"pong"`) {
			return
		}
	}
}

// An open socket keeps the identity it opened with, so whatever ends or
// changes that identity closes it: signing out, being disabled or
// demoted, and the session expiring (found by the periodic recheck).
func TestSocketsCloseWhenTheirSessionEnds(t *testing.T) {
	var st *store.Store
	e := newEnvWith(t, func(d *Deps) { st = d.Store })
	owner := signIn(t, e)
	ownerWS := openSocket(t, e, owner)

	t.Run("demote", func(t *testing.T) {
		id, c := addUser(t, e, owner, "demoted", auth.RoleOperator)
		ws := openSocket(t, e, c)
		if status, _, b := e.call(t, owner, "PUT", "/api/users/"+id, map[string]string{"role": auth.RoleViewer}); status != http.StatusOK {
			t.Fatalf("demote: %d %s", status, b)
		}
		if s := closedWith(t, ws); s != websocket.StatusPolicyViolation {
			t.Fatalf("close status = %v", s)
		}
		// Reconnecting works, as the new role.
		stillOpen(t, openSocket(t, e, c), "demoted, reconnected")
	})
	t.Run("disable", func(t *testing.T) {
		id, c := addUser(t, e, owner, "disabled", auth.RoleOperator)
		ws := openSocket(t, e, c)
		if status, _, b := e.call(t, owner, "PUT", "/api/users/"+id, map[string]bool{"disabled": true}); status != http.StatusOK {
			t.Fatalf("disable: %d %s", status, b)
		}
		if s := closedWith(t, ws); s != websocket.StatusPolicyViolation {
			t.Fatalf("close status = %v", s)
		}
	})
	t.Run("logout", func(t *testing.T) {
		_, c := addUser(t, e, owner, "leaver", auth.RoleViewer)
		other := login(t, e, "leaver") // a second session of the same user
		ws, otherWS := openSocket(t, e, c), openSocket(t, e, other)
		if status, _, b := e.call(t, c, "POST", "/api/auth/logout", nil); status != http.StatusNoContent {
			t.Fatalf("logout: %d %s", status, b)
		}
		if s := closedWith(t, ws); s != websocket.StatusPolicyViolation {
			t.Fatalf("close status = %v", s)
		}
		stillOpen(t, otherWS, "the other session")
	})
	t.Run("expired", func(t *testing.T) {
		_, c := addUser(t, e, owner, "lapsed", auth.RoleViewer)
		ws := openSocket(t, e, c)
		// No event: the session just lapses (the clock moved, here the row).
		if _, err := st.Writer().Exec(`UPDATE sessions SET expires_at = 1 WHERE user_id = (SELECT id FROM users WHERE username = 'lapsed')`); err != nil {
			t.Fatal(err)
		}
		if s := closedWith(t, ws); s != websocket.StatusPolicyViolation {
			t.Fatalf("close status = %v", s)
		}
	})
	stillOpen(t, ownerWS, "owner")
}

// Sign-outs and account changes close sockets at once, without waiting
// for the recheck (here there's none).
func TestSocketsCloseAtOnceOnAuthEvents(t *testing.T) {
	e := newEnvWith(t, func(d *Deps) {
		hub := realtime.New(realtime.GraphSource{Graph: graph.New()}, realtime.Options{Log: d.Log})
		t.Cleanup(hub.Close)
		d.Realtime = hub
	})
	owner := signIn(t, e)
	id, c := addUser(t, e, owner, "sam", auth.RoleAdmin)
	ws := openSocket(t, e, c)
	if status, _, b := e.call(t, owner, "PUT", "/api/users/"+id, map[string]string{"role": auth.RoleHousehold}); status != http.StatusOK {
		t.Fatalf("demote: %d %s", status, b)
	}
	if s := closedWith(t, ws); s != websocket.StatusPolicyViolation {
		t.Fatalf("demote: close status = %v", s)
	}
	ws = openSocket(t, e, c)
	if status, _, b := e.call(t, c, "POST", "/api/auth/logout", nil); status != http.StatusNoContent {
		t.Fatalf("logout: %d %s", status, b)
	}
	if s := closedWith(t, ws); s != websocket.StatusPolicyViolation {
		t.Fatalf("logout: close status = %v", s)
	}
}
