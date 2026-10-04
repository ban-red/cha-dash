package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"dash.cha.sh/spikes/traefik/labkit"
)

const fixturesDir = "../../testdata/integrations/unraid"

// Runs everywhere (CI included): the list-free invariants.
func TestFixturesLookSanitized(t *testing.T) {
	if err := labkit.SelfTest(); err != nil {
		t.Fatal(err) // the scanner must find its planted canary, or nothing below means anything
	}
	problems, err := labkit.CheckInvariants(fixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	// Every comment is redacted, and no 64-hex key-shaped value sits next to "api_key".
	apiKeyish := regexp.MustCompile(`(?i)api[_-]?key"?\s*[:=]\s*"?[0-9a-f]{64}`)
	_ = filepath.WalkDir(fixturesDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for n, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, `"comment": "`) && !strings.Contains(line, `"comment": "REDACTED"`) && !strings.Contains(line, `"comment": ""`) {
				t.Errorf("%s:%d: comment not redacted", p, n+1)
			}
			if apiKeyish.MatchString(line) {
				t.Errorf("%s:%d: something key-shaped next to api_key", p, n+1)
			}
		}
		return nil
	})
}

// Runs only where the private real-value list exists (it lives outside the repo):
//
//	LEAKCHECK_VALUES=<scratch>/unraid-leak-values.json go test -run Leak -v ./...
//
// The list holds every real value the sanitizer saw: the API key, disk serials and ids, the
// server id and every container/network id, addresses, MACs, domains, emails, the machine id,
// the system and VM UUIDs, label secrets, personal share names and every .env.local value.
// Values that are the same on every machine (the all-zero MAC, Docker's default gateways) are
// left off. Hits are reported by file, line and kind, never by value.
func TestLeakCheck(t *testing.T) {
	path := os.Getenv("LEAKCHECK_VALUES")
	if path == "" {
		t.Skip("LEAKCHECK_VALUES not set; the real-value list lives outside the repo")
	}
	vals, err := labkit.ReadLeakList(path)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, v := range vals {
		kinds[v.Kind]++
	}
	t.Logf("leak list: %d real values %v", len(vals), kinds)
	// A list without the key or the serials would make a clean result meaningless.
	for _, k := range []string{"api-key", "disk-serial", "disk-id", "id", "ipv4", "mac", "word"} {
		if kinds[k] == 0 {
			t.Fatalf("leak list has no %q values", k)
		}
	}

	// Sanity: plant the canary, every API key the list holds (the current one and any earlier
	// one) and one real value of each other critical kind in a temp file; the scanner must find
	// every one of them.
	dir := t.TempDir()
	planted := []labkit.LeakValue{{V: labkit.Canary, Kind: "canary"}}
	for _, v := range vals {
		if v.Kind == "api-key" {
			planted = append(planted, v)
		}
	}
	for _, k := range []string{"disk-serial", "disk-id", "ipv4", "mac"} {
		for _, v := range vals {
			if v.Kind == k {
				planted = append(planted, v)
				break
			}
		}
	}
	t.Logf("planted the canary and %d real values (%d API keys)", len(planted)-1, kinds["api-key"])
	var body strings.Builder
	for _, p := range planted {
		body.WriteString(`{"x": "` + p.V + `"}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "planted.json"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := labkit.ScanDir(dir, planted)
	if err != nil {
		t.Fatal(err)
	}
	hit := map[int]bool{}
	for _, f := range found {
		hit[f.Index] = true
	}
	for i, p := range planted {
		if !hit[i] {
			t.Fatalf("sanity check failed: the planted %s was not found", p.Kind)
		}
	}

	// The real scan: every integration fixture (this one; the Docker fixture of the same host,
	// which shares its container ids and share names; the others for good measure), and this
	// spike's own files (README, code, tests).
	for _, d := range []string{"../../testdata/integrations", "."} {
		found, err := labkit.ScanDir(d, vals)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range found {
			if strings.Contains(f.File, string(filepath.Separator)+"bin"+string(filepath.Separator)) {
				continue // local build output, git-ignored
			}
			t.Errorf("LEAK %s", f)
		}
	}
}
