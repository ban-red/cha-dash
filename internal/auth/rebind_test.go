package auth

import (
	"net/http/httptest"
	"testing"
)

func TestRebindSafe(t *testing.T) {
	s := &Service{publicURL: "https://dash.example.com", publicRP: relyingParty{ID: "dash.example.com"}}
	for host, want := range map[string]bool{
		"192.168.1.20:3696":        true,
		"[fd00::20]:3696":          true,
		"localhost:3697":           true,
		"hub:3696":                 true,
		"hub.lan:3696":             true,
		"hub.home.arpa":            true,
		"Dash.Example.Com.":        true,
		"dash.attacker.example":    false,
		"192.168.1.20.nip.io":      false,
		"dash.example.com.evil.io": false,
	} {
		r := httptest.NewRequest("POST", "/api/setup", nil)
		r.Host = host
		if got := s.rebindSafe(r); got != want {
			t.Errorf("rebindSafe(%q) = %v, want %v", host, got, want)
		}
	}
	for host, want := range map[string]bool{
		"localhost:3696": true, "127.0.0.1:3696": true, "[::1]:3696": true, "app.localhost": true,
		"rebind.attacker.example": false, "192.168.1.20": false,
	} {
		r := httptest.NewRequest("POST", "/api/auth/dev-login", nil)
		r.Host = host
		if got := localHost(r); got != want {
			t.Errorf("localHost(%q) = %v, want %v", host, got, want)
		}
	}
}
