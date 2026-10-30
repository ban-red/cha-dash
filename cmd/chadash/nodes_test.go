package main

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/nodeproto"
	"dash.cha.sh/internal/nodes"
)

// TestDockerOnApprove: approving a node whose join token asked for it adds
// one Docker integration reading through it (node://<name>), as the admin
// who approved; approving can't run twice; a token with add_docker off adds
// nothing.
func TestDockerOnApprove(t *testing.T) {
	a, err := newApp(t.Context(), testConfig(t, t.TempDir()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	w := a.store.Writer()
	if _, err := w.Exec(`INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_ada', 'ada', 'admin', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	pending := func(id, name string, addDocker int) {
		t.Helper()
		k, _ := nodeproto.GenerateKey()
		if _, err := w.Exec(`INSERT INTO nodes (id, name, public_key, state, enrolled_via, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', 'token', 1, 1)`, id, name, nodeproto.PinOf(k)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Exec(`INSERT INTO node_join_tokens (id, hash, name, add_docker, created_by, created_at, expires_at, used_at, node_id)
			VALUES (?, ?, ?, ?, 'u_ada', 1, 2, 1, ?)`, "njt_"+id, "hash-"+id, name, addDocker, id); err != nil {
			t.Fatal(err)
		}
	}
	pending("n_far", "far", 1)
	pending("n_quiet", "quiet", 0)
	ada := audit.Actor{Kind: "user", ID: "u_ada", Name: "ada"}

	dockerFor := func(name string) []string {
		var ids []string
		for _, in := range a.integrations.List() {
			if ep, _ := in.Config["endpoint"].(string); in.Integration == "docker" && ep == "node://"+name {
				ids = append(ids, in.ID)
			}
		}
		return ids
	}
	if _, err := a.nodes.Approve(t.Context(), "n_far", ada); err != nil {
		t.Fatal(err)
	}
	ids := dockerFor("far")
	if len(ids) != 1 {
		t.Fatalf("docker instances for far: %v", ids)
	}
	if in, err := a.integrations.Get(ids[0]); err != nil || in.Name != "far" {
		t.Fatalf("instance %+v %v", in, err)
	}
	page, err := audit.New(a.store).Query(t.Context(), audit.Filter{Action: "settings.integrations.instance.create"})
	if err != nil || len(page.Events) != 1 || page.Events[0].Actor.ID != "u_ada" || page.Events[0].Target != "integration:"+ids[0] {
		t.Fatalf("audit %+v %v", page.Events, err)
	}
	if _, err := a.nodes.Approve(t.Context(), "n_far", ada); !errors.Is(err, nodes.ErrNotPending) {
		t.Fatalf("approving twice: %v", err)
	}
	if len(dockerFor("far")) != 1 {
		t.Fatal("a second approve added another instance")
	}

	if _, err := a.nodes.Approve(t.Context(), "n_quiet", ada); err != nil {
		t.Fatal(err)
	}
	if ids := dockerFor("quiet"); len(ids) != 0 {
		t.Fatalf("add_docker off, but: %v", ids)
	}
}
