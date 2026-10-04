package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"dash.cha.sh/spikes/traefik/labkit"
)

const fixtures = "../../testdata/integrations/traefik"

// Runs everywhere (CI included): no address outside the fake ranges, no real
// MAC, no Host() rule outside the fake domains, no basic-auth hash that isn't
// a fake.
func TestFixturesLookSanitized(t *testing.T) {
	problems, err := labkit.CheckInvariants(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	var mws []struct {
		BasicAuth *struct{ Users []string } `json:"basicAuth"`
	}
	if err := readJSON(fixtures+"/http-middlewares.json", &mws); err != nil {
		t.Fatal(err)
	}
	for _, m := range mws {
		if m.BasicAuth == nil {
			continue
		}
		for _, u := range m.BasicAuth.Users {
			if !strings.HasPrefix(u, "admin:$") {
				t.Errorf("basicAuth user entry is not a sanitized fake")
			}
		}
	}
}

// Runs only where the private real-value list exists (the maintainer's
// machine): LEAKCHECK_VALUES=/path/outside/the/repo/leak-values.json go test ./...
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
	if len(vals) == 0 {
		t.Fatal("empty leak list")
	}
	found, err := labkit.ScanDir(fixtures, vals)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Error(f) // file:line and kind only, never the value
	}
}

// The correlation the README documents still holds on the fixtures.
func TestCorrelation(t *testing.T) {
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := analyze(fixtures, "../../testdata/integrations/docker/jupiter")
	w.Close()
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	for _, want := range []string{
		"| `sonarr@file` | https://jupiter.lab.example/sonarr | →https auth:authelia | `http://10.0.11.120:7780/sonarr` | jupiter → nginxproxy",
		"| `bazarr@file` | https://jupiter.lab.example/bazarr | →https auth:authelia | `http://10.0.11.120:6767` | jupiter → bazarr",
		"| `plex@file` | https://plex.lab.example | →https | `http://10.0.11.120:32400` | jupiter → Plex-Current (host network)",
		"| `romm@file` | https://roms.lab.example | →https | `http://10.0.11.120:8100` | jupiter → RomM [exited]",
		"| `linkding@docker` | https://links.lab.example | →https | `http://10.106.0.5:9090` | container on Traefik's own Docker host",
		"- path-based: 9",
		"- provider:file: 56",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("analysis lost a documented row:\n%s", want)
		}
	}
}

func TestOverviewCounts(t *testing.T) {
	var o struct {
		HTTP struct {
			Routers, Services, Middlewares struct{ Total int }
		} `json:"http"`
		Providers []string
	}
	b, err := os.ReadFile(fixtures + "/overview.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatal(err)
	}
	// The same numbers Homepage's Traefik widget showed (05-dogfood-home.md).
	if o.HTTP.Routers.Total != 61 || o.HTTP.Services.Total != 56 || o.HTTP.Middlewares.Total != 7 {
		t.Errorf("overview = %d/%d/%d, want 61/56/7", o.HTTP.Routers.Total, o.HTTP.Services.Total, o.HTTP.Middlewares.Total)
	}
}
