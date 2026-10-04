package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env.local")
	content := "# comment\n" +
		"UNRAID_JUPITER_URL=http://192.168.77.14/\n" +
		"UNRAID_JUPITER_API_KEY=\"abc123\"\n" +
		"lower_case=ignored\n" +
		"  INDENTED=ignored\n" +
		"export EXPORTED=ignored\n" +
		"PVE_X_URL='https://pve.example:8006'\r\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	vals, err := loadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"UNRAID_JUPITER_URL":     "http://192.168.77.14/",
		"UNRAID_JUPITER_API_KEY": "abc123",
		"PVE_X_URL":              "https://pve.example:8006",
	}
	if len(vals) != len(want) {
		t.Errorf("got %d keys, want %d", len(vals), len(want))
	}
	for k, v := range want {
		if vals[k] != v {
			t.Errorf("%s: got %q, want %q", k, vals[k], v)
		}
	}

	cfg, err := loadInstance(p, "jupiter")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "http://192.168.77.14" || cfg.APIKey != "abc123" || cfg.Name != "JUPITER" {
		t.Errorf("unexpected instance config (URL trimmed, key read): %+v", cfg)
	}
	if _, err := loadInstance(p, "saturn"); err == nil {
		t.Error("missing instance should fail")
	}
}

func TestFindEnvFileWalksUp(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, ".claude", "worktrees", "agent-x", "spikes", "unraid")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".env.local")
	if err := os.WriteFile(want, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := findEnvFile(deep)
	if err != nil || got != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
}
