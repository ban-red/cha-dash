package main

import (
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fixturesDir is the sanitized fixture tree. PVE_FIXTURES overrides it (to
// check a scratch output before it lands in the repo).
func fixturesDir() string {
	if d := os.Getenv("PVE_FIXTURES"); d != "" {
		return d
	}
	return "../../testdata/integrations/proxmox"
}

// TestScannerFindsCanary is the leak check's sanity check: a planted known
// string must be found, at the right file and line, and a clean file must not.
func TestScannerFindsCanary(t *testing.T) {
	s := newScanner([]Needle{{Value: leakCanary, Class: "canary"}, {Value: "secret-host", Class: "hostname", Bounded: true}})
	if err := selfTest(s); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.json"), []byte("{\n\"x\": 1,\n\"y\": \"SECRET-HOST.lan\"\n}"), 0o600)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte("{\"x\": \"not-secret-hostname\"}"), 0o600) // bounded: no match inside a longer word
	got, files, err := s.scanPaths([]string{dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || len(got) != 1 || filepath.Base(got[0].File) != "a.json" || got[0].Line != 3 {
		t.Fatalf("want one hit at a.json:3 (case-insensitive), got %+v over %d files", got, files)
	}
}

// TestMapperIsStable checks the replacement engine on synthetic values: fakes
// keep their format, are stable, and are never re-mapped as real values.
func TestMapperIsStable(t *testing.T) {
	m := newMapper(nil)
	m.lan16["192.168"] = true
	in := `node secretbox at 192.168.7.20/24 mac 3c:7c:3f:12:34:56 fp ` + strings.Repeat("ab:", 31) + `cd uuid 0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0 mail alice@example.net docker 172.18.0.1`
	m.force("secretbox", "pluto", "node")
	out := m.str(in)
	for _, bad := range []string{"secretbox", "192.168.7.20", "3c:7c:3f", "ab:ab:ab", "0f1e2d3c", "alice", "example.net"} {
		if strings.Contains(strings.ToLower(out), bad) {
			t.Errorf("%q survived: %s", bad, out)
		}
	}
	for _, want := range []string{"pluto", "10.0.11.20/24", "BC:24:11:00:00:01", "00000000-0000-4000-8000-000000000001", "admin@lab.example", "172.18.0.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in %s", want, out)
		}
	}
	if again := m.str(out); again != out {
		t.Errorf("not idempotent:\n%s\n%s", out, again)
	}
}

var (
	genIP4    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	genDomain = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.(?:lan|local|localdomain|home|internal|arpa|com|net|org|de|uk|nl|fr|eu|me|dev|app|cloud|xyz|tech|site|online|io)\b`)
	okDomain  = regexp.MustCompile(`(?i)(\.lab\.example|\.example|proxmox\.com|debian\.org)$`)
)

// TestFixturesHaveNoGenericLeaks needs no real values: it rejects any address,
// MAC, fingerprint, UUID, e-mail or domain in the fixtures that isn't in the
// sanitized scheme. It runs in CI.
func TestFixturesHaveNoGenericLeaks(t *testing.T) {
	root := fixturesDir()
	if _, err := os.Stat(root); err != nil {
		t.Skip("no fixtures at", root)
	}
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".json" {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		s := string(b)
		rel, _ := filepath.Rel(root, p)
		for _, ip := range genIP4.FindAllString(s, -1) {
			v := net.ParseIP(ip).To4()
			if v == nil {
				continue
			}
			ok := v[0] == 10 && v[1] == 0 || v[0] == 127 || v[0] == 0 || v[0] == 255 ||
				v[0] == 172 && v[1] >= 17 && v[1] <= 31 || ip == "1.1.1.1" || ip == "8.8.8.8" || ip == "9.9.9.9"
			if !ok {
				t.Errorf("%s: IPv4 outside the scheme: %s", rel, ip)
			}
		}
		for _, mac := range reMAC.FindAllString(s, -1) {
			u := strings.ToUpper(strings.ReplaceAll(mac, "-", ":"))
			if !strings.HasPrefix(u, "BC:24:11:00:00:") && u != "00:00:00:00:00:00" && !strings.HasPrefix(u, "00:00:00:00:00:") {
				t.Errorf("%s: MAC outside the scheme: %s", rel, mac)
			}
		}
		for _, f := range reFP.FindAllString(s, -1) {
			if !strings.HasPrefix(f, strings.Repeat("00:", 30)) {
				t.Errorf("%s: fingerprint outside the scheme", rel)
			}
		}
		for _, u := range reUUID.FindAllString(s, -1) {
			if !strings.HasPrefix(u, "00000000-0000-4000-8000-") {
				t.Errorf("%s: UUID outside the scheme: %s", rel, u)
			}
		}
		for _, e := range reEmail.FindAllString(s, -1) {
			if !strings.HasSuffix(strings.ToLower(e), "@lab.example") {
				t.Errorf("%s: e-mail outside the scheme", rel)
			}
		}
		for _, dm := range genDomain.FindAllString(s, -1) {
			if !okDomain.MatchString(dm) {
				t.Errorf("%s: domain outside the scheme: %s", rel, dm)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no fixture files scanned")
	}
}

// TestFixturesHaveNoRealValues is the full leak check: it derives every real
// value from the env file and the raw captures (in memory only) and scans the
// fixtures and this spike for them. It needs the maintainer's lab data, so it
// runs only when PVE_ENV and PVE_RAW are set:
//
//	PVE_ENV=/path/to/.env.local PVE_RAW=/path/to/raw go test -run RealValues
func TestFixturesHaveNoRealValues(t *testing.T) {
	env, raw := os.Getenv("PVE_ENV"), os.Getenv("PVE_RAW")
	if env == "" || raw == "" {
		t.Skip("PVE_ENV and PVE_RAW not set")
	}
	ins, err := loadInstances(env)
	if err != nil {
		t.Fatal(err)
	}
	n, err := leakcheckCmd(ins, raw, []string{fixturesDir(), "."})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d leaks", n)
	}
}
