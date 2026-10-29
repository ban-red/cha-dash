package homarr

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/store"
)

type catalog map[string][2][]string

func (c catalog) Integration(id string) ([]string, []string, bool) {
	f, ok := c[id]
	return f[0], f[1], ok
}

var testCatalog = catalog{
	"sonarr":      {{"api_key"}, {"url", "api_key"}},
	"radarr":      {{"api_key"}, {"url", "api_key"}},
	"qbittorrent": {{"password"}, {"url", "username", "password"}},
	"pihole":      {{"password"}, {"url"}},
	"checks":      {nil, {"targets"}},
}

// backupZip builds the fixture's db.sqlite and zips it.
func backupZip(t *testing.T) []byte {
	t.Helper()
	sqlText, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "importers", "homarr", "backup.sql"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "db.sqlite")
	st, err := store.Open(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(t.Context(), string(sqlText)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	db, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string][]byte{"db.sqlite": db, "metadata.json": []byte(`{"version":"2.0.0"}`)} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBuildMapsTheBusiestBoard(t *testing.T) {
	res, secrets, err := Build(backupZip(t), testCatalog)
	if err != nil {
		t.Fatal(err)
	}
	p := &res.Plan
	if p.Board.Name != "home" || len(secrets) != 0 {
		t.Fatalf("board %q, secrets %d", p.Board.Name, len(secrets))
	}
	var titles []string
	for _, s := range p.Board.Layout.Sections {
		t := ""
		if s.Title != nil {
			t = *s.Title
		}
		titles = append(titles, t)
	}
	if !slices.Equal(titles, []string{"", "Media", "Network"}) {
		t.Fatalf("sections = %q", titles)
	}
	tiles := map[string]importer.Tile{}
	for _, t := range p.Tiles {
		tiles[t.Name] = t
	}
	if tiles["Clock"].Widget != "clock" || tiles["Sonarr"].Instance == "" || tiles["Wiki"].Check == "" || tiles["Plex"].Check == "" {
		t.Fatalf("tiles = %+v", tiles)
	}
	if cal := tiles["Up next"]; cal.Widget != "calendar" || len(cal.Sources) != 2 {
		t.Fatalf("calendar = %+v", cal)
	}
	if dl := tiles["qBittorrent"]; dl.Widget != "downloads" || dl.Instance == "" {
		t.Fatalf("downloads = %+v", dl)
	}
	insts := map[string]importer.Instance{}
	for _, in := range p.Instances {
		insts[in.Integration] = in
	}
	if !slices.Equal(insts["sonarr"].MissingSecrets, []string{"api_key"}) || len(insts["pihole"].MissingSecrets) != 0 {
		t.Fatalf("missing = %v / %v", insts["sonarr"].MissingSecrets, insts["pihole"].MissingSecrets)
	}
	if insts["qbittorrent"].Config["url"] != "http://10.20.0.5:8080" {
		t.Fatalf("qbittorrent = %+v", insts["qbittorrent"])
	}
	kinds := map[string]bool{}
	for _, u := range res.Unmapped {
		kinds[u.Type] = true
	}
	if !kinds["notebook"] || !kinds["github"] || !kinds["qbittorrent"] {
		t.Fatalf("unmapped = %+v", res.Unmapped)
	}
	if len(p.Ignored) != 1 || !strings.Contains(p.Ignored[0].Key, "spare") {
		t.Fatalf("ignored = %+v", p.Ignored)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "private notes") || strings.Contains(string(b), "ZmFrZS1lbmNyeXB0ZWQ") {
		t.Fatal("item text or an encrypted secret leaked into the plan")
	}

	again, _, err := Build(backupZip(t), testCatalog)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatal("the same backup built a different plan")
	}
}

func TestBuildRefusesOtherZips(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("services.yaml")
	_, _ = w.Write([]byte("- A: []"))
	_ = zw.Close()
	if _, _, err := Build(buf.Bytes(), testCatalog); err != ErrNotABackup {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := Build([]byte("not a zip"), testCatalog); err == nil {
		t.Fatal("a non-zip built")
	}
}
