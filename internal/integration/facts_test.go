package integration

import (
	"encoding/json"
	"testing"
)

// UseURL keeps the address and drops everything that could carry a
// credential.
func TestUseURL(t *testing.T) {
	for _, c := range []struct {
		scheme, host string
		port         int
		path, want   string
	}{
		{"http", "10.0.11.120", 8080, "", "http://10.0.11.120:8080"},
		{"HTTPS", "qbit.lab.example", 0, "/base/", "https://qbit.lab.example/base"},
		{"mqtt", "fd00::5", 1883, "", "mqtt://[fd00::5]:1883"},
		{"http", "[fd00::5]", 80, "x", "http://[fd00::5]:80/x"},
		{"http", "user:pass@host.example", 80, "", ""},
		{"http", "", 80, "", ""},
		{"", "host.example", 80, "", ""},
		{"http", "host.example", 70000, "/a?apikey=x#y", "http://host.example/a"},
	} {
		if got := UseURL(c.scheme, c.host, c.port, c.path); got != c.want {
			t.Errorf("UseURL(%q, %q, %d, %q) = %q, want %q", c.scheme, c.host, c.port, c.path, got, c.want)
		}
	}
}

// CleanUses keeps role, url and name only, whatever a spec or an
// integration sent: a password in a field or in the URL never survives.
func TestCleanUses(t *testing.T) {
	var in any
	if err := json.Unmarshal([]byte(`[
		{"role": "download client", "url": "http://admin:hunter2@10.0.11.120:8080/qbit?apikey=abc#x", "name": "qBittorrent", "password": "hunter2", "api_key": "abc"},
		{"role": "download client", "url": "http://admin:hunter2@10.0.11.120:8080/qbit?apikey=abc#x", "name": "qBittorrent"},
		{"role": "indexer", "url": "not a url"},
		{"role": "nothing"},
		"a string",
		{"role": "  media\n server ", "name": "Plex"}
	]`), &in); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(CleanUses(in))
	want := `[{"name":"qBittorrent","role":"download client","url":"http://10.0.11.120:8080/qbit"},{"name":"Plex","role":"media server"}]`
	if string(b) != want {
		t.Fatalf("CleanUses = %s, want %s", b, want)
	}
	many := make([]any, 40)
	for i := range many {
		many[i] = map[string]any{"role": "indexer", "url": UseURL("http", "idx.example", 9000+i, "")}
	}
	if n := len(CleanUses(many)); n != MaxUses {
		t.Fatalf("kept %d, want %d", n, MaxUses)
	}
}
