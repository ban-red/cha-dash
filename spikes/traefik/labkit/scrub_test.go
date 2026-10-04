package labkit

import (
	"strings"
	"testing"
)

func newTestScrubber(t *testing.T) *Scrubber {
	t.Helper()
	st, err := LoadState(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	s := NewScrubber(st)
	s.AddLAN("192.0.2.120")
	return s
}

func TestScrubDomainsAndAddresses(t *testing.T) {
	s := newTestScrubber(t)
	s.LearnString("Host(`sonarr.home.test-domain.net`) && PathPrefix(`/sonarr`)")
	s.LearnString("Host(`photos.second.org`)")
	s.LearnString("Host(`nas.box.lan`) || Host(`printer.local`)")
	s.LearnString("HostRegexp(`^(.+\\.)?third\\.red$`)")
	s.Finish()
	for in, want := range map[string]string{
		"Host(`nas.box.lan`) || Host(`printer.local`)": "Host(`nas.home1.lan`) || Host(`host1.local`)",
		"HostRegexp(`^(.+\\.)?third\\.red$`)":          "HostRegexp(`^(.+\\.)?lab3\\.example$`)",
	} {
		if got := s.String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
	cases := map[string]string{
		"Host(`sonarr.home.test-domain.net`) && PathPrefix(`/sonarr`)": "Host(`sonarr.home.lab.example`) && PathPrefix(`/sonarr`)",
		"Host(`photos.second.org`)":                                    "Host(`photos.family.example`)",
		"router-test-domain-net":                                       "router-lab-example",
		"http://192.0.2.120:8989":                                      "http://10.0.11.120:8989",
		"http://172.18.0.5:80":                                         "http://10.102.0.5:80",
		"172.17.0.0/16":                                                "10.101.0.0/16",
		"0.0.0.0:2375":                                                 "0.0.0.0:2375",
		"127.0.0.1":                                                    "127.0.0.1",
		"8.8.8.8":                                                      "203.0.113.1",
		"02:42:AC:11:00:02":                                            "02:42:0a:00:00:01",
		"https://user:pw@sonarr.second.org/x":                          "https://redacted:redacted@sonarr.family.example/x",
		"mail me@test-domain.net":                                      "mail redacted@example.com",
		"lscr.io/linuxserver/sonarr:4.0.14.2939-ls283":                 "lscr.io/linuxserver/sonarr:4.0.14.2939-ls283",
	}
	for in, want := range cases {
		if got := s.String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScrubIDsAndSecrets(t *testing.T) {
	s := newTestScrubber(t)
	id := strings.Repeat("ab12", 16)
	s.LearnString(id)
	s.Finish()
	fake := s.ID(id)
	if got := s.String("/var/lib/docker/containers/" + id + "/" + id + "-json.log"); got != "/var/lib/docker/containers/"+fake+"/"+fake+"-json.log" {
		t.Errorf("id path not rehashed consistently: %q", got)
	}
	if got := s.String(id[:12]); got != fake[:12] {
		t.Errorf("short id = %q, want %q", got, fake[:12])
	}
	key := "0123456789abcdef0123456789abcdef"
	fk := s.FakeSecret(key)
	if fk == key || len(fk) != len(key) || !hexish.MatchString(fk) {
		t.Errorf("FakeSecret(hex) = %q: must differ and stay 32-hex", fk)
	}
	if fk := s.FakeSecret("uk2_AbCdEf123456"); !strings.HasPrefix(fk, "uk2_") || fk == "uk2_AbCdEf123456" {
		t.Errorf("FakeSecret kept no prefix or didn't change: %q", fk)
	}
	if fk := s.FakeSecret("bob:$apr1$abcd$efghijklmnop"); !strings.HasPrefix(fk, "admin:$apr1$") {
		t.Errorf("htpasswd fake = %q", fk)
	}
	tree := s.Tree(map[string]any{"basicAuth": map[string]any{"users": []any{"bob:$apr1$abcd$efgh"}}, "apiKey": "s3cr3tvalue"}).(map[string]any)
	if tree["apiKey"] == "s3cr3tvalue" {
		t.Error("secret-looking key kept its value")
	}
}

func TestLeakCheckSelfTest(t *testing.T) {
	if err := SelfTest(); err != nil {
		t.Fatal(err)
	}
}
