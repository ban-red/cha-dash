package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/correlate"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
)

func TestIntegrationsLinkedIn(t *testing.T) {
	// Every v0.1 integration ID from 07 · Integration IDs, plus the sample
	// spec, the breadth integrations and the weather source.
	for _, id := range []string{
		"checks", "proxmox", "pbs", "docker", "traefik", "unraid", "opnsense", "frigate",
		"uptimekuma", "homeassistant", "plex", "sonarr", "radarr", "prowlarr", "qbittorrent", "immich",
		"tautulli", "bazarr", "mealie", "linkding", "karakeep", "seerr",
		"jellyfin", "pihole", "adguard", "sabnzbd", "weather",
		// general calendars (v0.2)
		"ical",
		// wave 8 breadth (v0.2)
		"lidarr", "transmission", "nzbget", "nextcloud", "audiobookshelf", "paperlessngx", "speedtesttracker",
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

// The correlator runs over what the instances publish: two check instances
// of one URL are one check. Its API is mounted behind sign-in.
func TestCorrelatorWired(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	targets := map[string]any{"targets": []any{map[string]any{"name": "Web", "kind": "http", "url": up.URL}}}
	a, err := newApp(t.Context(), testConfig(t, t.TempDir()), quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	var ids []string
	for _, name := range []string{"Checks", "More checks"} {
		id, err := a.integrations.Create(t.Context(), runtime.CreateRequest{Integration: "checks", Name: name, Config: targets})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	key := graph.Relation{From: graph.RID("check:" + ids[1] + "/web"), Type: correlate.SameAs, To: graph.RID("check:" + ids[0] + "/web")}.Key()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		_, doc := a.graph.Snapshot(nil)
		if rel, ok := doc.Relations[key]; ok && rel.Source == correlate.Source {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the correlator never merged the two checks")
		}
	}

	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/correlate/suggestions", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/correlate/suggestions without a session = %d", rec.Code)
	}
}

// Every integration sits in a category, so the Add list can group them.
func TestEveryIntegrationHasACategory(t *testing.T) {
	for _, m := range integration.Default.Manifests() {
		if m.Category == "" {
			t.Errorf("%s has no category (Manifest.Category, or meta.category in its spec)", m.ID)
		}
	}
}
