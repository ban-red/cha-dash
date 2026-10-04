package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dash.cha.sh/spikes/traefik/labkit"
)

const fixtures = "../../testdata/integrations/docker"

// Runs everywhere (CI included).
func TestFixturesLookSanitized(t *testing.T) {
	problems, err := labkit.CheckInvariants(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	files, _ := filepath.Glob(filepath.Join(fixtures, "jupiter", "inspect", "*.json"))
	if len(files) != 74 {
		t.Errorf("%d inspect fixtures, want 74", len(files))
	}
	for _, f := range files {
		var c struct {
			Config struct {
				Env    []string
				Labels map[string]string
			}
			State struct {
				Health *struct{ Log []struct{ Output string } }
			}
		}
		if err := readJSON(f, &c); err != nil {
			t.Fatal(err)
		}
		for _, e := range c.Config.Env {
			if _, v, _ := strings.Cut(e, "="); v != "REDACTED" {
				t.Errorf("%s: env value not redacted", filepath.Base(f))
			}
		}
		if c.State.Health != nil {
			for _, l := range c.State.Health.Log {
				if l.Output != "" && l.Output != "REDACTED" {
					t.Errorf("%s: healthcheck output not redacted", filepath.Base(f))
				}
			}
		}
	}
}

// Runs only where the private real-value list exists:
// LEAKCHECK_VALUES=/path/outside/the/repo/leak-values.json go test ./...
func TestFixturesHaveNoRealValues(t *testing.T) {
	if err := labkit.SelfTest(); err != nil {
		t.Fatal(err) // the scanner must find a planted known test string
	}
	path := os.Getenv("LEAKCHECK_VALUES")
	if path == "" {
		t.Skip("LEAKCHECK_VALUES not set: the real-value list lives outside the repo")
	}
	vals, err := labkit.ReadLeakList(path)
	if err != nil {
		t.Fatal(err)
	}
	found, err := labkit.ScanDir(fixtures, vals)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Error(f)
	}
}

// The probe classifies jupiter's endpoint as a filtering proxy, and the
// statuses the README documents still hold.
func TestProbeFixture(t *testing.T) {
	var rows []ProbeRow
	if err := readJSON(filepath.Join(fixtures, "jupiter", "probe.json"), &rows); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"/_ping": 200, "/v{api}/info": 403, "/v{api}/containers/{id}/json": 200, "/v{api}/images/json": 403,
		"/v{api}/networks": 403, "/v{api}/volumes": 403, "/v{api}/secrets": 403, "/v{api}/swarm": 403,
		"/v{api}/containers/{id}/logs?stdout=0&stderr=0&tail=0": 400,
	}
	for _, r := range rows {
		if w, ok := want[r.Path]; ok && r.Status != w {
			t.Errorf("%s %s = %d, want %d", r.Method, r.Path, r.Status, w)
		}
	}
	var meta struct {
		Reachable bool
		Error     string
	}
	if err := readJSON(filepath.Join(fixtures, "pvedocker", "capture.json"), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Reachable || meta.Error == "" {
		t.Error("pvedocker should be recorded as unreachable with an error kind")
	}
}

func TestEventsFixtureShape(t *testing.T) {
	var evs []map[string]any
	if err := readJSON(filepath.Join(fixtures, "jupiter", "events-live.json"), &evs); err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatal("no live events")
	}
	for _, e := range evs {
		a, _ := e["Action"].(string)
		// exec_create/exec_start carry the command line ("exec_start: sh -c …"); exec_die doesn't.
		if verb, _, ok := strings.Cut(a, ": "); ok && a != verb+": REDACTED" {
			t.Errorf("exec event keeps its command line: %q", a)
		}
	}
}
