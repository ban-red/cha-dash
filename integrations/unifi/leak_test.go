package unifi

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The fixtures keep to the scheme in testdata/integrations/unifi/README.md:
// LAN 10.0.40.x, WAN 192.0.2.x (TEST-NET-1), a /24 netmask, MACs 00:00:5e:00:53:xx
// (RFC 7042's documentation range), placeholder UUIDs and *.example names.
var (
	leakIPv4   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	leakMAC    = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b`)
	leakUUID   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	leakDomain = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.(?:lan|local|home|internal|com|net|org|de|uk|io|dev|app|cloud|example)\b`)
	// Firmware versions (4.0.21.27000) look like addresses; they're dropped
	// before the address check.
	versions = regexp.MustCompile(`"[a-z_]*(?:version|firmware)": ?"[^"]*"`)
)

func TestFixturesKeepTheScheme(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtures, "*.json"))
	if err != nil || len(files) < 10 {
		t.Fatalf("%d fixtures, %v", len(files), err)
	}
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s, name := string(b), filepath.Base(p)
		for _, ip := range leakIPv4.FindAllString(versions.ReplaceAllString(s, ""), -1) {
			if !strings.HasPrefix(ip, "10.0.40.") && !strings.HasPrefix(ip, "192.0.2.") && ip != "255.255.255.0" {
				t.Errorf("%s: address outside the scheme: %s", name, ip)
			}
		}
		for _, mac := range leakMAC.FindAllString(s, -1) {
			if !strings.HasPrefix(strings.ToLower(mac), "00:00:5e:00:53:") {
				t.Errorf("%s: MAC outside the scheme: %s", name, mac)
			}
		}
		for _, u := range leakUUID.FindAllString(s, -1) {
			if !strings.HasPrefix(u, "00000000-0000-4000-8000-") {
				t.Errorf("%s: UUID outside the scheme: %s", name, u)
			}
		}
		for _, d := range leakDomain.FindAllString(s, -1) {
			if !strings.HasSuffix(strings.ToLower(d), ".example") {
				t.Errorf("%s: domain outside the scheme: %s", name, d)
			}
		}
	}
}
