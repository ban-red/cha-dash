package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
	"dash.cha.sh/internal/nodes"
	"dash.cha.sh/internal/vault"
)

type listVerb struct{}

func (listVerb) Name() string { return "test" }
func (listVerb) Verbs() []node.Verb {
	return []node.Verb{{Name: "container.list", Policy: "docker.read", Handle: func(context.Context, json.RawMessage, func(json.RawMessage)) (json.RawMessage, error) {
		return json.RawMessage(`[]`), nil
	}}}
}

// TestNodeChannelThroughTheHub pairs a node with the full handler, through
// Guard and every middleware (security headers, the cross-origin check,
// compression, the access log), and checks the nodes API's roles.
func TestNodeChannelThroughTheHub(t *testing.T) {
	pairing := filepath.Join(t.TempDir(), "pairing")
	var hub *nodes.Hub
	e := newEnvWith(t, func(d *Deps) {
		v, err := vault.New(d.Store, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		hub, err = nodes.New(t.Context(), nodes.Options{Store: d.Store, Vault: v, PairingFile: pairing, Log: d.Log})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(hub.Close)
		d.Nodes = hub
	})
	tok, err := os.ReadFile(pairing)
	if err != nil {
		t.Fatal(err)
	}
	ident, err := node.OpenIdentity(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := node.NewDispatcher(node.DefaultPolicy(), listVerb{})
	c, err := node.NewClient(node.Config{HubURL: "ws" + strings.TrimPrefix(e.srv.URL, "http") + nodeproto.Path, Name: "local",
		Identity: ident, Dispatcher: d, Pairing: strings.TrimSpace(string(tok))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	for !hub.Connected("local") {
		if time.Now().After(deadline) {
			t.Fatal("the node never connected through the hub's handler")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if res, err := hub.Call(t.Context(), "local", "container.list", nil, nil); err != nil || string(res) != "[]" {
		t.Fatalf("call: %s %v", res, err)
	}

	owner := signIn(t, e)
	status, _, b := e.call(t, owner, "GET", "/api/nodes", nil)
	if status != http.StatusOK || !strings.Contains(string(b), `"name":"local"`) || !strings.Contains(string(b), `"connected":true`) {
		t.Fatalf("GET /api/nodes: %d %s", status, b)
	}
	_, viewer := addUser(t, e, owner, "vic", auth.RoleViewer)
	if status, code, _ := e.call(t, viewer, "GET", "/api/nodes", nil); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("viewer: %d %s", status, code)
	}
	if status, _, _ := e.call(t, &http.Client{}, "GET", "/api/nodes", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", status)
	}
	for _, leak := range []string{strings.TrimSpace(string(tok))[4:24]} {
		if strings.Contains(string(b), leak) {
			t.Fatal("the nodes API leaks the pairing secret")
		}
	}
}
