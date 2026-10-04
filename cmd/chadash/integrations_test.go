package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
)

func TestIntegrationsLinkedIn(t *testing.T) {
	// Every v0.1 integration ID from 07 · Integration IDs, plus the sample spec.
	for _, id := range []string{
		"checks", "proxmox", "pbs", "docker", "traefik", "unraid", "opnsense", "frigate",
		"uptimekuma", "homeassistant", "plex", "sonarr", "radarr", "prowlarr", "qbittorrent", "immich",
		"tautulli", "bazarr", "mealie", "linkding", "karakeep", "seerr",
	} {
		if _, _, ok := integration.Default.Get(id); !ok {
			t.Errorf("%s isn't registered in the hub", id)
		}
	}
}

func TestIntegrationsMode(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	targets := map[string]any{"targets": []any{map[string]any{"name": "Web", "kind": "http", "url": up.URL}}}
	rid := func(id string) graph.RID { return graph.RID("check:" + id + "/web") }

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	// Outside demo mode, instances feed the graph everyone sees.
	a, err := newApp(t.Context(), testConfig(t, t.TempDir()), quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if a.integrationTopics != a.topics {
		t.Fatal("outside demo mode, instances write to the served topics")
	}
	id, err := a.integrations.Create(t.Context(), runtime.CreateRequest{Integration: "checks", Name: "Checks", Config: targets})
	if err != nil {
		t.Fatal(err)
	}
	waitFor("the check in the graph", func() bool { _, ok := a.graph.Get(rid(id)); return ok })

	// In demo mode they run, but into a private graph: the Demo Home stays
	// untouched.
	cfg := testConfig(t, t.TempDir())
	cfg.Demo, cfg.DemoAt, cfg.DemoSeed = true, config.DefaultDemoAt, 1
	d, err := newApp(t.Context(), cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	id, err = d.integrations.Create(t.Context(), runtime.CreateRequest{Integration: "checks", Name: "Checks", Config: targets})
	if err != nil {
		t.Fatal(err)
	}
	waitFor("the check in the sandbox", func() bool { _, ok := d.integrationTopics.Graph.Get(rid(id)); return ok })
	if _, ok := d.graph.Get(rid(id)); ok {
		t.Fatal("a real instance reached the Demo Home")
	}
	if _, ok := d.graph.Get(graph.RID(runtime.SelfID(id))); ok {
		t.Fatal("a real instance's own resource reached the Demo Home")
	}
	// The status turns ok just after the sync that published the check.
	waitFor("the sandboxed instance to report ok", func() bool { v, _ := d.integrations.Get(id); return v.Status == runtime.StatusOK })
}
