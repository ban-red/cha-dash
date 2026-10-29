// Package leads turns the services Settings → Bring in → Discover found into
// a plan, so they go through the same Review sheet, History and Revert as an
// import: each lead is a connection, and a tile in a "Discovered" section.
// The web sends what it found (an integration and the address it reads);
// nothing here touches the network.
package leads

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/importer/importhttp"
)

// Source names this importer: /api/import/discovery.
const Source = "discovery"

// MaxLeads bounds one review.
const MaxLeads = 50

// Lead is one service to connect.
type Lead struct {
	Integration string `json:"integration"`
	Name        string `json:"name"`
	// URL is where it answers; Endpoint is Docker's tcp:// or unix:// address.
	URL      string `json:"url,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	// Href is the link the tile opens, when it differs from URL.
	Href string `json:"href,omitempty"`
}

// tileFor is the catalog widget a lead's tile defaults to.
var tileFor = map[string]string{
	"sonarr": "arr", "radarr": "arr", "lidarr": "arr",
	"plex": "plex", "jellyfin": "plex",
	"qbittorrent": "downloads", "transmission": "downloads", "nzbget": "downloads", "sabnzbd": "downloads",
	"homeassistant": "homeassistant", "frigate": "cameras", "uptimekuma": "uptimekuma", "traefik": "traefik",
	"docker": "containers",
}

// Build maps leads (JSON) with cat (the hub's integrations).
func Build(src []byte, cat importer.Catalog) (*importer.Result, importer.Secrets, error) {
	var leads []Lead
	if err := json.Unmarshal(src, &leads); err != nil {
		return nil, nil, errors.New("leads: not a list of leads")
	}
	if len(leads) == 0 || len(leads) > MaxLeads {
		return nil, nil, fmt.Errorf("leads: send 1 to %d", MaxLeads)
	}
	k := importer.NewKit(Source)
	sec := k.Section("discovered", "Discovered", 8)
	for i, l := range leads {
		secret, required, ok := cat.Integration(l.Integration)
		if !ok {
			return nil, nil, fmt.Errorf("leads: lead %d: %q isn't an integration this hub has", i+1, l.Integration)
		}
		cfg := map[string]any{}
		key := l.URL
		switch {
		case l.Endpoint != "":
			if !strings.HasPrefix(l.Endpoint, "tcp://") && !strings.HasPrefix(l.Endpoint, "unix:///") && !strings.HasPrefix(l.Endpoint, "node://") {
				return nil, nil, fmt.Errorf("leads: lead %d: the endpoint isn't tcp://, unix:// or node://", i+1)
			}
			cfg["endpoint"], key = l.Endpoint, l.Endpoint
		case l.URL != "":
			u, err := url.Parse(l.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return nil, nil, fmt.Errorf("leads: lead %d: the address isn't http(s)", i+1)
			}
			cfg["url"], cfg["tls"] = l.URL, importer.TLSFor(l.URL)
		default:
			return nil, nil, fmt.Errorf("leads: lead %d has no address", i+1)
		}
		name := strings.TrimSpace(l.Name)
		if name == "" {
			name = l.Integration
		}
		origin := importer.Origin{File: "discovery", Group: "Discovered", Entry: name}
		inst := k.Instance(importer.Instance{ID: k.ID("i_", l.Integration, key), Integration: l.Integration, Name: name, Config: cfg, Origin: origin})
		for _, f := range secret {
			if slices.Contains(required, f) {
				k.Missing(inst, f)
			}
		}
		t := importer.Tile{Widget: tileFor[l.Integration], Name: name, Instance: inst.ID, Origin: origin}
		if t.Widget == "" {
			t.Widget = "service"
		}
		if h := l.Href; strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") {
			t.Href = h
		} else if l.URL != "" {
			t.Href = l.URL
		}
		k.Tile(sec, t, "")
	}
	return k.Finish("discovered", "Discovered", "Discovered")
}

// ImportSource is the discovery source for the shared import API:
// POST /api/import/discovery takes {files: {leads_json}}. No dropped file is
// ever detected as one.
func ImportSource(cat importer.Catalog) importhttp.Source {
	return importhttp.Source{
		ID: Source, Name: "Discovery", MaxBytes: 256 << 10,
		Roles:  []importhttp.Role{{Key: "leads_json", File: "leads.json", Required: true}},
		Detect: func(string, []byte) (string, bool) { return "", false },
		Build: func(files map[string][]byte) (*importer.Result, importer.Secrets, error) {
			return Build(files["leads_json"], cat)
		},
	}
}
