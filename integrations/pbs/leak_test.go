package pbs

import (
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The generic leak check from spike 4.1 (spikes/pve/leak_test.go), as the
// Proxmox integration runs it. The synthetic fixture uses the Proxmox
// fixtures' scheme (testdata/integrations/pbs/README.md), so it must pass
// the same check: no address, MAC, fingerprint, UUID, e-mail or domain
// outside it.
var (
	leakIPv4   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	leakMAC    = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b`)
	leakFP     = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}:){31}[0-9a-f]{2}\b`)
	leakUUID   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	leakEmail  = regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,}\b`)
	leakDomain = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.(?:lan|local|localdomain|home|internal|arpa|com|net|org|de|uk|nl|fr|eu|me|dev|app|cloud|xyz|tech|site|online|io)\b`)
	okDomain   = regexp.MustCompile(`(?i)(\.lab\.example|\.example|proxmox\.com|debian\.org)$`)
)

func TestFixturesHaveNoGenericLeaks(t *testing.T) {
	n := 0
	err := filepath.WalkDir(fixtures, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".json" {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		s := string(b)
		rel, _ := filepath.Rel(fixtures, p)
		for _, ip := range leakIPv4.FindAllString(s, -1) {
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
		for _, mac := range leakMAC.FindAllString(s, -1) {
			u := strings.ToUpper(strings.ReplaceAll(mac, "-", ":"))
			if !strings.HasPrefix(u, "BC:24:11:00:00:") && !strings.HasPrefix(u, "00:00:00:00:00:") {
				t.Errorf("%s: MAC outside the scheme: %s", rel, mac)
			}
		}
		for _, f := range leakFP.FindAllString(s, -1) {
			if !strings.HasPrefix(f, strings.Repeat("00:", 30)) {
				t.Errorf("%s: fingerprint outside the scheme", rel)
			}
		}
		for _, u := range leakUUID.FindAllString(s, -1) {
			if !strings.HasPrefix(u, "00000000-0000-4000-8000-") {
				t.Errorf("%s: UUID outside the scheme: %s", rel, u)
			}
		}
		for _, e := range leakEmail.FindAllString(s, -1) {
			if !strings.HasSuffix(strings.ToLower(e), "@lab.example") {
				t.Errorf("%s: e-mail outside the scheme", rel)
			}
		}
		for _, dm := range leakDomain.FindAllString(s, -1) {
			if !okDomain.MatchString(dm) {
				t.Errorf("%s: domain outside the scheme: %s", rel, dm)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 9 {
		t.Fatalf("scanned %d fixture files; the tree has 10", n)
	}
}
