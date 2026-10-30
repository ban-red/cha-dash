package yamlboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/importer"
)

type catalog map[string][2][]string

func (c catalog) Integration(id string) ([]string, []string, bool) {
	f, ok := c[id]
	return f[0], f[1], ok
}

var testCatalog = catalog{
	"sonarr": {{"api_key"}, {"url", "api_key"}},
	"plex":   {{"token"}, {"url", "token"}},
	"checks": {nil, {"targets"}},
}

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "importers", "yaml", "board.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildTheFixture(t *testing.T) {
	res, secrets, err := Build(fixture(t), testCatalog)
	if err != nil {
		t.Fatal(err)
	}
	p := &res.Plan
	if p.Board.Name != "Home" || len(p.Board.Layout.Sections) != 2 || p.Board.Layout.Sections[0].Span != 8 {
		t.Fatalf("board = %+v", p.Board)
	}
	byName := map[string]importer.Tile{}
	for _, t := range p.Tiles {
		byName[t.Name] = t
	}
	sonarr, again := byName["Sonarr"], byName["Sonarr again"]
	if sonarr.Widget != "arr" || sonarr.Size != "wide" || again.Widget != "service" || sonarr.Instance != again.Instance {
		t.Fatalf("sonarr %+v, again %+v", sonarr, again)
	}
	if byName["Wiki"].Check == "" || byName["Notes"].Check != "" || byName["Printer"].Check == "" {
		t.Fatalf("checks: %+v", byName)
	}
	insts := map[string]importer.Instance{}
	for _, in := range p.Instances {
		insts[in.Integration] = in
	}
	if len(p.Instances) != 3 || insts["sonarr"].Config["api_key"] != "${secret:instance/"+insts["sonarr"].ID+"/api_key}" {
		t.Fatalf("instances = %+v", p.Instances)
	}
	if !slices.Equal(insts["plex"].MissingSecrets, []string{"token"}) {
		t.Fatalf("plex = %+v", insts["plex"])
	}
	if v := secrets["instance/"+insts["sonarr"].ID+"/api_key"]; v.Reveal() != "0123456789abcdef0123456789abcdef" {
		t.Fatal("the key lost its text")
	}
	var codes []string
	for _, h := range res.Hints {
		codes = append(codes, h.Code)
	}
	if !slices.Contains(codes, "secret_in_file") || !slices.Contains(codes, "missing_secret") {
		t.Fatalf("hints = %v", codes)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "0123456789abcdef") {
		t.Fatal("the key leaked into the plan")
	}
}

func TestBuildNamesTheLine(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"apiVersion: chadash/v1alpha1\nkind: Board\nname: X\nsections:\n  - title: A\n    colour: red\n", "line 6"},
		{"apiVersion: chadash/v1alpha1\nkind: Home\nname: X\nsections: []\n", "kind must be Board"},
		{"apiVersion: chadash/v1alpha1\nkind: Board\nname: X\nsections:\n  - tiles:\n      - name: A\n        connect: nope\n", "line 7"},
		{"apiVersion: chadash/v1alpha1\nkind: Board\nname: X\nsections:\n  - tiles:\n      - name: A\n        href: ftp://x.example\n", "http(s)"},
		{"a: &x [1]\nb: *x\n", "aliases"},
		{"apiVersion: chadash/v1alpha1\nkind: Board\nname: X\nsections:\n  - tiles: [{name: A, connect: sonarr, api_key: SECRETVALUE, size: [}]\n", "not valid YAML"},
	} {
		_, _, err := Build([]byte(tc.src), testCatalog)
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "SECRETVALUE") {
			t.Errorf("%q: err = %v, want %q", tc.src, err, tc.want)
		}
	}
}

// A file's tls never reaches a connection: TLS comes from the URL.
func TestBuildIgnoresTLSFromTheFile(t *testing.T) {
	src := "apiVersion: chadash/v1alpha1\nkind: Board\nname: X\nsections:\n  - tiles:\n      - name: A\n        connect: sonarr\n        url: https://sonarr.example\n        tls: {mode: insecure}\n"
	res, _, err := Build([]byte(src), testCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plan.Instances) != 1 {
		t.Fatalf("instances = %+v", res.Plan.Instances)
	}
	if tls, _ := json.Marshal(res.Plan.Instances[0].Config["tls"]); strings.Contains(string(tls), "insecure") {
		t.Fatalf("tls = %s", tls)
	}
	unmapped := false
	for _, u := range res.Unmapped {
		unmapped = unmapped || slices.Contains(u.Fields, "tls")
	}
	if !unmapped {
		t.Fatal("tls isn't listed as left out")
	}
}
