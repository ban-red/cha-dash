package auth

import (
	"net"
	"net/http"
	"strings"
)

// localNameSuffixes are the names a public DNS server can't hand out, so a
// page on someone else's domain can't rebind them to the hub's address.
var localNameSuffixes = []string{".localhost", ".local", ".lan", ".home", ".home.arpa", ".internal", ".localdomain"}

// requestHost is r's Host header without its port, lower case.
func requestHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = strings.Trim(r.Host, "[]")
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// localHost reports whether r was addressed to this machine by name or to
// a loopback address. Dev login wants it: a web page whose name an attacker
// rebinds to 127.0.0.1 reaches the hub from loopback, but under its own name.
func localHost(r *http.Request) bool {
	host := requestHost(r)
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return isLocalhost(host)
}

// rebindSafe reports whether r's Host can't be a DNS name someone rebound to
// the hub: an IP address, a single-label or local-only name, the host of
// CHADASH_PUBLIC_URL, or any name a trusted proxy passed on (a proxy routes
// by name, so it only forwards the ones it serves). Creating the owner wants
// it, since until then anyone who reaches the hub may, and a rebound page in
// a browser on the LAN would otherwise count as reaching it.
func (s *Service) rebindSafe(r *http.Request) bool {
	host := requestHost(r)
	if host == "" {
		return false
	}
	if s.proxies.ViaTrusted(r) {
		return true
	}
	if net.ParseIP(host) != nil || host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	if s.publicURL != "" && host == s.publicRP.ID {
		return true
	}
	for _, sfx := range localNameSuffixes {
		if strings.HasSuffix(host, sfx) {
			return true
		}
	}
	return false
}
