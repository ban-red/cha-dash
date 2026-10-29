package homepage

import (
	"slices"
	"testing"

	"dash.cha.sh/internal/importer"
)

func TestDetectApp(t *testing.T) {
	for _, tc := range []struct {
		icon, name, key, want string
	}{
		{"https://api.iconify.design/simple-icons:unraid.svg?color=%23888888", "Jupiter Unraid", "Jupiter", "unraid"},
		{"linkding.png", "", "Linkding", "linkding"},
		{"mdi-plex", "", "Media", "plex"},
		{"paperless-ngx.svg", "", "Docs", "paperlessngx"},
		{"speedtest-tracker-light.png", "", "Speed", "speedtest"},
		{"", "Home Assistant", "HA", "homeassistant"},
		{"", "", "Sonarr", "sonarr"},
		// Not exact: a different app, or no app at all.
		{"https://api.iconify.design/logos:cockpit.svg", "", "JupiterNasCockpit", ""},
		{"", "Proxmox Backup Notes", "Notes", ""},
		{"data:image/svg+xml;base64,PHN2Zz4=", "Wiki", "Wiki", ""},
		{"", "", "", ""},
	} {
		if got := detectApp(importer.Tile{Icon: tc.icon, Name: tc.name}, tc.key); got != tc.want {
			t.Errorf("detectApp(icon %q, name %q, key %q) = %q, want %q", tc.icon, tc.name, tc.key, got, tc.want)
		}
	}
}

func TestWidgetlessEntryBecomesItsIntegration(t *testing.T) {
	res, _ := build(t, Input{Services: `
- Servers:
    - Jupiter:
        name: Jupiter Unraid
        href: https://jupiter.example/
        icon: simple-icons:unraid.svg
        siteMonitor: http://10.0.11.120
    - Cockpit:
        href: https://nas.example:9090/
        icon: logos:cockpit.svg
    - Docs:
        icon: paperless-ngx.png
`})
	p := res.Plan
	var unraid *importer.Instance
	for i := range p.Instances {
		if p.Instances[i].Integration == "unraid" {
			unraid = &p.Instances[i]
		}
	}
	if unraid == nil {
		t.Fatalf("no unraid instance in %v", p.Instances)
	}
	if unraid.Config["url"] != "https://jupiter.example/" || !slices.Equal(unraid.MissingSecrets, []string{"api_key"}) {
		t.Errorf("unraid = %+v", unraid)
	}
	for _, tl := range p.Tiles {
		switch tl.Name {
		case "Jupiter Unraid":
			if tl.Instance != unraid.ID || tl.Check != "Jupiter Unraid" {
				t.Errorf("Jupiter tile = %+v, want the unraid instance and its siteMonitor check", tl)
			}
		case "Cockpit":
			// No integration for it: it stays a check in the shared instance.
			if tl.Check != "Cockpit" || tl.Instance == unraid.ID {
				t.Errorf("Cockpit tile = %+v", tl)
			}
		case "Docs":
			// Recognised, but with no URL there's nothing to connect to.
			if tl.Instance != "" {
				t.Errorf("Docs tile = %+v, want no instance", tl)
			}
		}
	}
	var hint *importer.Hint
	for i := range res.Hints {
		if res.Hints[i].Code == "detected_integration" {
			hint = &res.Hints[i]
		}
		if res.Hints[i].Code == "missing_secret" {
			t.Errorf("a detected instance got the Homepage-file hint: %+v", res.Hints[i])
		}
	}
	if hint == nil || hint.Inbox {
		t.Errorf("hint = %+v, want a quiet detected_integration", hint)
	}
}
