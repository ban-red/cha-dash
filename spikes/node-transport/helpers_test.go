// The scenario suite: every test runs against both designs through the same
// core, so a difference in outcome is a difference in the design.
package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/spikes/node-transport/core"
	"dash.cha.sh/spikes/node-transport/mtlsws"
	"dash.cha.sh/spikes/node-transport/noisews"
	"dash.cha.sh/spikes/node-transport/pinnedtls"
)

type design struct {
	name  string
	hub   func() (core.HubCrypto, error)
	node  func() (core.NodeCrypto, error)
	key   func(core.NodeCrypto) []byte
	clone func(core.NodeCrypto) core.NodeCrypto
	// stolen is an attacker holding the victim's public identity (its
	// certificate or its public key) but not its private key.
	stolen func(victim core.NodeCrypto) core.NodeCrypto
}

var designs = []design{
	{
		name:  "mtls",
		hub:   func() (core.HubCrypto, error) { return mtlsws.NewHub() },
		node:  func() (core.NodeCrypto, error) { return mtlsws.NewNode() },
		key:   func(n core.NodeCrypto) []byte { return n.(*mtlsws.Node).Key() },
		clone: func(n core.NodeCrypto) core.NodeCrypto { return n.(*mtlsws.Node).Clone() },
		stolen: func(v core.NodeCrypto) core.NodeCrypto {
			a, _ := mtlsws.NewNode()
			for _, p := range v.Pins() {
				a.AddPin(p)
			}
			a.UnsafeSetCert(v.(*mtlsws.Node).Cert())
			return a
		},
	},
	{
		name:   "pinned-tls",
		hub:    func() (core.HubCrypto, error) { return pinnedtls.NewHub() },
		node:   func() (core.NodeCrypto, error) { return pinnedtls.NewNode() },
		key:    func(n core.NodeCrypto) []byte { return n.(*pinnedtls.Node).Key() },
		clone:  func(n core.NodeCrypto) core.NodeCrypto { return n.(*pinnedtls.Node).Clone() },
		stolen: func(v core.NodeCrypto) core.NodeCrypto { return pinnedtls.UnsafeImpersonate(v.(*pinnedtls.Node)) },
	},
	{
		name:   "noise",
		hub:    func() (core.HubCrypto, error) { return noisews.NewHub() },
		node:   func() (core.NodeCrypto, error) { return noisews.NewNode() },
		key:    func(n core.NodeCrypto) []byte { return n.(*noisews.Node).Key() },
		clone:  func(n core.NodeCrypto) core.NodeCrypto { return n.(*noisews.Node).Clone() },
		stolen: func(v core.NodeCrypto) core.NodeCrypto { return noisews.UnsafeImpersonate(v.(*noisews.Node)) },
	},
}

func each(t *testing.T, f func(t *testing.T, d design)) {
	for _, d := range designs {
		t.Run(d.name, func(t *testing.T) { f(t, d) })
	}
}

// events records what the hub did, so tests can wait for it.
type events struct {
	mu   sync.Mutex
	list []core.Event
	ch   chan struct{}
}

func (e *events) add(ev core.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
	close(e.ch)
	e.ch = make(chan struct{})
}

func (e *events) count(kind string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, ev := range e.list {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

// waitN waits until the hub has emitted n events of kind and returns the nth.
func (e *events) waitN(t testing.TB, kind string, n int) core.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		e.mu.Lock()
		seen := 0
		for _, ev := range e.list {
			if ev.Kind == kind {
				if seen++; seen == n {
					e.mu.Unlock()
					return ev
				}
			}
		}
		ch := e.ch
		e.mu.Unlock()
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("timed out waiting for %d× %s", n, kind)
		}
	}
}

type env struct {
	t   testing.TB
	d   design
	hc  core.HubCrypto
	hub *core.Hub
	ev  *events
	url string // ws://127.0.0.1:…/node, the compose-network case
	ctx context.Context
}

func newEnv(t testing.TB, d design) *env {
	hc, err := d.hub()
	if err != nil {
		t.Fatal(err)
	}
	hub := core.NewHub(hc)
	hub.HandshakeTimeout = 2 * time.Second
	ev := &events{ch: make(chan struct{})}
	hub.OnEvent = ev.add
	mux := http.NewServeMux()
	mux.Handle("/node", hub)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return &env{t: t, d: d, hc: hc, hub: hub, ev: ev, url: "ws" + strings.TrimPrefix(srv.URL, "http") + "/node", ctx: ctx}
}

func (e *env) newNode(nc core.NodeCrypto, url, token string, client *http.Client) *core.Node {
	e.t.Helper()
	n, err := core.NewNode(nc, url, token)
	if err != nil {
		e.t.Fatal(err)
	}
	n.Handler = nodeVerbs
	n.HTTPClient = client
	return n
}

// conn is one live node session.
type conn struct {
	s     *core.Session
	close func()
}

// dial connects until the hub welcomes the node, following ErrReconnect, and
// reports how many reconnects the hub asked for.
func (e *env) dial(n *core.Node) (conn, int) {
	e.t.Helper()
	for i := range 3 {
		ctx, cancel := context.WithCancel(e.ctx)
		s, err := n.Dial(ctx)
		if errors.Is(err, core.ErrReconnect) {
			cancel()
			continue
		}
		if err != nil {
			cancel()
			e.t.Fatalf("dial: %v", err)
		}
		return conn{s: s, close: func() { cancel(); <-s.Done() }}, i
	}
	e.t.Fatal("too many reconnects")
	return conn{}, 0
}

// enroll walks a new node through token → pending → approval → session.
func (e *env) enroll(url string, client *http.Client) (*core.Node, core.NodeCrypto, conn) {
	e.t.Helper()
	nc, err := e.d.node()
	if err != nil {
		e.t.Fatal(err)
	}
	n := e.newNode(nc, url, e.hub.JoinToken(15*time.Minute), client)
	before := e.ev.count("pending")
	errc := make(chan error, 1)
	go func() { _, err := n.Dial(e.ctx); errc <- err }()
	pending := e.ev.waitN(e.t, "pending", before+1)
	if err := e.hub.Approve(pending.Node); err != nil {
		e.t.Fatal(err)
	}
	if err := <-errc; !errors.Is(err, core.ErrReconnect) {
		e.t.Fatalf("after approval: %v", err)
	}
	c, _ := e.dial(n)
	return n, nc, c
}

func (e *env) ping(id string) error {
	ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
	defer cancel()
	res, err := e.hub.Call(ctx, id, "ping", nil, nil)
	if err == nil && string(res) != `{"pong":true}` {
		err = fmt.Errorf("ping answered %s", res)
	}
	return err
}

// nodeVerbs stands in for the node's capability verbs.
func nodeVerbs(ctx context.Context, method string, params json.RawMessage, emit func(any) error) (any, error) {
	switch method {
	case "ping":
		return map[string]bool{"pong": true}, nil
	case "echo":
		return params, nil
	case "logs.tail":
		var p struct {
			Lines int `json:"lines"`
		}
		json.Unmarshal(params, &p)
		for i := range p.Lines {
			if err := emit(fmt.Sprintf("line %d", i)); err != nil {
				return nil, err
			}
		}
		return map[string]int{"lines": p.Lines}, nil
	}
	return nil, errors.New("unknown verb")
}

func refusedWith(err error, reason string) bool {
	var r *core.RefusedError
	return errors.As(err, &r) && strings.Contains(r.Reason, reason)
}
