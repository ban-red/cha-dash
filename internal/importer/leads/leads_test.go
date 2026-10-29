package leads

import (
	"slices"
	"strings"
	"testing"
)

type catalog map[string][2][]string

func (c catalog) Integration(id string) ([]string, []string, bool) {
	f, ok := c[id]
	return f[0], f[1], ok
}

var cat = catalog{"sonarr": {{"api_key"}, {"url", "api_key"}}, "docker": {nil, {"endpoint"}}, "checks": {nil, nil}}

func TestBuild(t *testing.T) {
	res, secrets, err := Build([]byte(`[
		{"integration":"sonarr","name":"Sonarr","url":"http://10.20.0.5:8989","href":"https://sonarr.home.example"},
		{"integration":"docker","name":"Docker on nas","endpoint":"tcp://nas.lab.example:2375"}
	]`), cat)
	if err != nil {
		t.Fatal(err)
	}
	p := &res.Plan
	if len(p.Instances) != 2 || len(p.Tiles) != 2 || len(secrets) != 0 || p.Board.Name != "Discovered" {
		t.Fatalf("plan = %+v", p)
	}
	if !slices.Equal(p.Instances[0].MissingSecrets, []string{"api_key"}) || p.Tiles[0].Widget != "arr" || p.Tiles[0].Href != "https://sonarr.home.example" {
		t.Fatalf("sonarr = %+v / %+v", p.Instances[0], p.Tiles[0])
	}
	if p.Instances[1].Config["endpoint"] != "tcp://nas.lab.example:2375" || p.Tiles[1].Widget != "containers" {
		t.Fatalf("docker = %+v / %+v", p.Instances[1], p.Tiles[1])
	}
	for _, bad := range []string{`[]`, `{}`, `[{"integration":"nope","url":"http://a.example"}]`, `[{"integration":"sonarr","url":"ftp://a.example"}]`, `[{"integration":"sonarr"}]`, `[{"integration":"docker","endpoint":"http://x"}]`} {
		if _, _, err := Build([]byte(bad), cat); err == nil || strings.Contains(err.Error(), "a.example") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
