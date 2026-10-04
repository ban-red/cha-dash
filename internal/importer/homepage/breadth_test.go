package homepage

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/importer"
)

// v0.2's breadth widgets map onto their integrations, with the config keys
// each integration's TestValidate checks.
func TestBreadthWidgets(t *testing.T) {
	src := `
- Media:
  - Overseerr:
      href: https://requests.home.arpa
      widget: {type: overseerr, url: "https://requests.home.arpa", key: FAKE-overseerr-key}
  - Jellyseerr:
      widget: {type: jellyseerr, url: "https://jellyseerr.home.arpa", key: FAKE-jellyseerr-key}
  - Lidarr:
      widget: {type: lidarr, url: "https://lidarr.home.arpa", key: FAKE-lidarr-key, enableQueue: true}
  - Audiobookshelf:
      widget: {type: audiobookshelf, url: "https://audiobooks.home.arpa", key: FAKE-abs-key}
- Downloads:
  - Transmission:
      widget: {type: transmission, url: "http://nas.home.arpa:9091", username: chadash, password: FAKE-transmission-pw, rpcUrl: /transmission/}
  - NZBGet:
      widget: {type: nzbget, url: "http://nas.home.arpa:6789", username: chadash, password: FAKE-nzbget-pw}
- Apps:
  - Nextcloud:
      widget: {type: nextcloud, url: "https://nextcloud.home.arpa", key: FAKE-nc-token}
  - Nextcloud admin:
      widget: {type: nextcloud, url: "https://cloud.home.arpa", username: admin, password: FAKE-nc-app-pw}
  - Paperless:
      widget: {type: paperlessngx, url: "https://paperless.home.arpa", key: FAKE-paperless-token}
  - Paperless by password:
      widget: {type: paperlessngx, url: "https://docs.home.arpa", username: admin, password: FAKE-paperless-pw}
  - Speedtest:
      widget: {type: speedtest, url: "https://speedtest.home.arpa", version: 2, key: FAKE-speedtest-token, bitsPerSecond: true}
- Data:
  - "":
      widget:
        type: calendar
        integrations:
          - {type: lidarr, service_group: Media, service_name: Lidarr}
`
	res, secrets := build(t, Input{Services: src})
	byName := instancesByName(res.Plan)
	want := []struct {
		name, integration string
		config            map[string]any // non-secret keys besides url and tls
		secrets, missing  []string
		tile              string
	}{
		{"Overseerr", "seerr", nil, []string{"api_key"}, nil, "service"},
		{"Jellyseerr", "seerr", nil, []string{"api_key"}, nil, "service"},
		{"Lidarr", "lidarr", nil, []string{"api_key"}, nil, "arr"},
		{"Audiobookshelf", "audiobookshelf", nil, []string{"api_key"}, nil, "service"},
		{"Transmission", "transmission", map[string]any{"username": "chadash", "rpc_path": "/transmission/"}, []string{"password"}, nil, "downloads"},
		// A second downloads singleton falls back to the service tile.
		{"NZBGet", "nzbget", map[string]any{"username": "chadash"}, []string{"password"}, nil, "service"},
		{"Nextcloud", "nextcloud", nil, []string{"token"}, nil, "service"},
		{"Nextcloud admin", "nextcloud", map[string]any{"username": "admin"}, []string{"password"}, nil, "service"},
		{"Paperless", "paperlessngx", nil, []string{"token"}, nil, "service"},
		{"Paperless by password", "paperlessngx", nil, nil, []string{"token"}, "service"},
		{"Speedtest", "speedtesttracker", nil, []string{"token"}, nil, "service"},
	}
	if len(res.Plan.Instances) != len(want) {
		t.Fatalf("instances = %v", instanceNames(res.Plan))
	}
	for _, w := range want {
		in, ok := byName[w.name]
		if !ok || in.Integration != w.integration {
			t.Errorf("%s: %+v", w.name, in)
			continue
		}
		for k, v := range w.config {
			if in.Config[k] != v {
				t.Errorf("%s: config %s = %v, want %v", w.name, k, in.Config[k], v)
			}
		}
		for k := range in.Config {
			if _, listed := w.config[k]; !listed && k != "url" && k != "tls" && !slices.Contains(w.secrets, k) {
				t.Errorf("%s: unexpected config %s", w.name, k)
			}
		}
		if !slices.Equal(in.SecretFields, orEmpty(w.secrets)) || !slices.Equal(in.MissingSecrets, w.missing) {
			t.Errorf("%s: secrets %v missing %v", w.name, in.SecretFields, in.MissingSecrets)
		}
		if tl := tileFor(res.Plan, in.ID); tl.Widget != w.tile {
			t.Errorf("%s: tile %s, want %s", w.name, tl.Widget, w.tile)
		}
	}
	if tl := tileFor(res.Plan, byName["Lidarr"].ID); tl.ItemID != "arr@lidarr:"+byName["Lidarr"].ID+"/app" || tl.Options["queue"] != true {
		t.Errorf("lidarr tile = %+v", tl)
	}
	// The calendar reads Lidarr through media.calendar.
	var cal importer.Tile
	for _, tl := range res.Plan.Tiles {
		if tl.Widget == "calendar" {
			cal = tl
		}
	}
	if len(cal.Sources) != 1 || cal.Sources[0].Instance != byName["Lidarr"].ID || cal.Sources[0].Capability != "media.calendar" {
		t.Errorf("calendar sources = %+v", cal.Sources)
	}
	// Paperless's username and password aren't moved (the spec reads with
	// a token); Speedtest's bitsPerSecond is ignored. Nothing is unmapped.
	if len(res.Unmapped) != 0 {
		t.Errorf("unmapped = %+v", res.Unmapped)
	}
	var ignored []string
	for _, ig := range res.Plan.Ignored {
		ignored = append(ignored, ig.Key)
	}
	for _, k := range []string{"widget.username", "widget.password", "widget.bitsPerSecond"} {
		if !slices.Contains(ignored, k) {
			t.Errorf("%s isn't listed as ignored: %v", k, ignored)
		}
	}
	if len(secrets) != 10 {
		t.Errorf("%d secrets moved", len(secrets))
	}
	j, _ := json.Marshal(res)
	assertNoLeak(t, "the plan", string(j), []string{"FAKE-overseerr-key", "FAKE-transmission-pw", "FAKE-nc-token", "FAKE-paperless-pw", "FAKE-speedtest-token"})
	if strings.Contains(string(j), "FAKE-paperless-pw") {
		t.Error("an ignored password reached the plan")
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
