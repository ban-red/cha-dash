package correlate

import (
	"testing"

	"dash.cha.sh/internal/integration"
)

func use(role, url, name string) map[string]any {
	return map[string]any{"role": role, "url": url, "name": name}
}

// Sonarr says what it uses by address; the correlator finds what answers
// there: an app's URL, a route, or a container's published port.
func TestUsesJoinsByAddress(t *testing.T) {
	h := jupiter(t).
		add("docker:i_d/container/qbittorrent", "container", "qbittorrent", "container_id", "cc"+fullID[2:12],
			"published", []string{"18080->8080/tcp"}).
		rel("docker:i_d/container/qbittorrent", "runs_on", "docker:i_d/engine").
		add("docker:i_d/container/mosquitto", "container", "mosquitto", "container_id", "dd"+fullID[2:12],
			"published", []string{"11883->1883/tcp"}).
		rel("docker:i_d/container/mosquitto", "runs_on", "docker:i_d/engine").
		add("traefik:i_t/route/prowlarr", "route", "prowlarr", "url", "https://prowlarr.lab.example").
		add("traefik:i_t/route/bazarr", "route", "bazarr", "url", "https://bazarr.lab.example").
		add("prowlarr:i_p/app", "app", "Prowlarr", "type", "prowlarr", "url", "https://prowlarr.lab.example").
		add("radarr:i_r/app", "app", "Radarr", "type", "radarr", "url", "http://10.0.11.120:17878").
		add("sonarr:i_s/app", "app", "Sonarr", "type", "sonarr", "url", "http://10.0.11.120:18989", "uses", []any{
			use("download client", "http://10.0.11.120:18080", "qBittorrent"),
			use("indexer", "https://prowlarr.lab.example/1/api", "Prowlarr"),
			use("subtitles", "https://bazarr.lab.example", ""),
			use("sibling", "http://10.0.11.120:17878/", "Radarr"),
		}).
		add("frigate:i_f/app", "app", "Frigate", "type", "frigate", "url", "http://10.0.11.20:5000", "uses", []any{
			use("MQTT broker", "mqtt://10.0.11.120:11883", "mosquitto"),
		})
	r := h.run()
	// A container with no app in it: the container.
	mustLink(t, r, "sonarr:i_s/app", DependsOn, "docker:i_d/container/qbittorrent", Strong, "uses")
	// A route the correlator joined to an app: the app behind it.
	mustLink(t, r, "sonarr:i_s/app", DependsOn, "prowlarr:i_p/app", Strong, "uses")
	// A route to nothing it knows: the route.
	mustLink(t, r, "sonarr:i_s/app", DependsOn, "traefik:i_t/route/bazarr", Strong, "uses")
	// An app's own URL, trailing slash and all: the app, not its container.
	mustLink(t, r, "sonarr:i_s/app", DependsOn, "radarr:i_r/app", Strong, "uses")
	noLink(t, r, "sonarr:i_s/app", DependsOn, "docker:i_d/container/radarr")
	// Not http: joined by host and port.
	mustLink(t, r, "frigate:i_f/app", DependsOn, "docker:i_d/container/mosquitto", Strong, "uses")
	l, _ := hasLink(r, "sonarr:i_s/app", DependsOn, "docker:i_d/container/qbittorrent")
	if want := "Sonarr uses qBittorrent as its download client, at 10.0.11.120:18080, which qbittorrent publishes."; l.Evidence != want {
		t.Fatalf("evidence %q, want %q", l.Evidence, want)
	}
}

// A container that runs one app stands for the app.
func TestUsesPrefersTheAppInAContainer(t *testing.T) {
	h := jupiter(t).
		add("traefik:i_t/route/sonarr", "route", "sonarr", "url", "https://sonarr.lab.example", "upstream_addr", "10.0.11.120:18989").
		add("sonarr:i_s/app", "app", "Sonarr", "type", "sonarr", "url", "https://sonarr.lab.example").
		add("seerr:i_e/app", "app", "Seerr", "type", "seerr", "url", "http://10.0.11.120:5055", "uses", []any{
			use("TV", "http://10.0.11.120:18989", "Sonarr"),
		})
	r := h.run()
	mustLink(t, r, "sonarr:i_s/app", RunsOn, "docker:i_d/container/sonarr", Strong, "route")
	mustLink(t, r, "seerr:i_e/app", DependsOn, "sonarr:i_s/app", Strong, "uses")
}

// A name alone is a suggestion, and only when one app has it. A URL with
// a user in it is never read as an address.
func TestUsesByNameIsASuggestion(t *testing.T) {
	h := newHome(t).
		add("plex:i_x/app", "app", "Plex", "type", "plex", "url", "http://10.0.11.30:32400").
		add("sonarr:i_s/app", "app", "Sonarr", "type", "sonarr").
		add("sonarr:i_t/app", "app", "Sonarr 4K", "type", "sonarr").
		add("tautulli:i_a/app", "app", "Tautulli", "type", "tautulli", "uses", []any{
			use("media server", "", "Plex"),
			use("", "http://admin:hunter2@10.0.11.99:8989", "sonarr"),
			map[string]any{"role": "nothing"},
		})
	r := h.run()
	noLink(t, r, "tautulli:i_a/app", DependsOn, "plex:i_x/app")
	if !hasSuggestion(r, "tautulli:i_a/app", DependsOn, "plex:i_x/app") {
		t.Fatalf("no suggestion; suggestions:\n%s", dump(r.Suggestions))
	}
	// Two apps of type sonarr: no suggestion at all.
	for _, l := range r.Suggestions {
		if l.From == "tautulli:i_a/app" && l.To != "plex:i_x/app" {
			t.Fatalf("unexpected suggestion %s", l.Key())
		}
	}
}

// The owner's rejection holds, and a confirmation of a suggestion is
// published.
func TestUsesDecisions(t *testing.T) {
	h := newHome(t).
		add("plex:i_x/app", "app", "Plex", "type", "plex", "url", "http://10.0.11.30:32400").
		add("tautulli:i_a/app", "app", "Tautulli", "type", "tautulli", "uses", []any{
			use("media server", "http://10.0.11.30:32400", "Plex"),
		}).
		add("overseerr:i_o/app", "app", "Overseerr", "type", "seerr", "uses", []any{use("media server", "", "Plex")})
	r := h.run(
		Decision{From: "tautulli:i_a/app", Type: DependsOn, To: "plex:i_x/app", Verdict: Reject},
		Decision{From: "overseerr:i_o/app", Type: DependsOn, To: "plex:i_x/app", Verdict: Confirm, Rule: "uses"},
	)
	noLink(t, r, "tautulli:i_a/app", DependsOn, "plex:i_x/app")
	mustLink(t, r, "overseerr:i_o/app", DependsOn, "plex:i_x/app", Confirmed, "uses")
}

func TestUsesOf(t *testing.T) {
	h := newHome(t).add("x:i_x/app", "app", "X", "uses", []integration.Use{{Role: "a", URL: "http://a.example"}, {Role: "empty"}})
	us := usesOf(h.doc.Resources["x:i_x/app"])
	if len(us) != 1 || us[0].URL != "http://a.example" {
		t.Fatalf("usesOf = %+v", us)
	}
	for _, s := range []string{"", "mqtt://broker.lab.example", "mqtt://u:p@broker.lab.example:1883", "broker:1883"} {
		if _, ok := parseUse(s); ok {
			t.Fatalf("parseUse(%q) ok", s)
		}
	}
	if u, ok := parseUse("MQTT://Broker.lab.example.:1883/x"); !ok || u.key() != "broker.lab.example:1883" {
		t.Fatalf("parseUse = %+v, %v", u, ok)
	}
}
