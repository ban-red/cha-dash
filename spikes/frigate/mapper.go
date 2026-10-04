package main

// Copied from spikes/opnsense/sanitize.go (each spike is its own module);
// keep the two in step.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// mapper turns real identifiers into stable fake ones, following the
// conventions of testdata/importers/homepage/dogfood:
//   - private IPv4: the busiest /24 becomes 10.0.11.x (last octet kept);
//     other private /24s become 10.0.21.x, 10.0.22.x, … in sorted order;
//   - CGNAT/tailnet 100.64/10 → 100.64.0.n; public IPv4 → 203.0.113.n;
//   - IPv6: global → 2001:db8::n, ULA → fd00::n, link-local → fe80::n;
//   - MACs → 02:00:00:00:00:nn; DNS names → client-<octet>.lab.example.
type mapper struct {
	nets  map[string]string // real /24 prefix "a.b.c" → fake "10.0.x"
	ip4   map[string]string
	ip6   map[string]string
	mac   map[string]string
	text  map[string]string // exact strings (labels, names) → replacement
	pub   int
	cgnat int
	v6    int
	macN  int
	hostN int
}

var (
	reIP4tok = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reMACtok = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?:[:-][0-9a-f]{2}){5}\b`)
	reIP6tok = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){2,7}[0-9a-f]{0,4}(?:%[a-z0-9_.]+)?`)
)

func newMapper() *mapper {
	return &mapper{nets: map[string]string{}, ip4: map[string]string{}, ip6: map[string]string{}, mac: map[string]string{}, text: map[string]string{}}
}

func isSpecial4(a netip.Addr) bool {
	return a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() || strings.HasPrefix(a.String(), "255.")
}

// planNets assigns fake /24s. The LAN (the /24 holding the router's own
// address, from OPNSENSE_URL) becomes 10.0.11; other private /24s get
// 10.0.21, 10.0.22, … The assignment is shared with the Frigate spike through
// a git-ignored netmap file outside the repo, so both fixture sets agree.
func (m *mapper) planNets(corpus [][]byte, lanPrefix, netmap string) error {
	if netmap != "" {
		if b, err := os.ReadFile(netmap); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if real, fake, ok := strings.Cut(line, "\t"); ok {
					m.nets[real] = fake
				}
			}
		}
	}
	if lanPrefix != "" {
		if _, ok := m.nets[lanPrefix]; !ok {
			m.nets[lanPrefix] = "10.0.11"
		}
	}
	var fresh []string
	for _, b := range corpus {
		for _, s := range reIP4tok.FindAllString(string(b), -1) {
			a, err := netip.ParseAddr(s)
			if err != nil || !a.IsPrivate() || isSpecial4(a) {
				continue
			}
			p := s[:strings.LastIndex(s, ".")]
			if _, ok := m.nets[p]; !ok && !slices.Contains(fresh, p) {
				fresh = append(fresh, p)
			}
		}
	}
	sort.Strings(fresh)
	for _, p := range fresh {
		m.nets[p] = m.nextNet()
	}
	if netmap == "" {
		return nil
	}
	var sb strings.Builder
	for real, fake := range m.nets {
		fmt.Fprintf(&sb, "%s\t%s\n", real, fake)
	}
	return os.WriteFile(netmap, []byte(sb.String()), 0o600)
}

func (m *mapper) nextNet() string {
	used := map[string]bool{}
	for _, f := range m.nets {
		used[f] = true
	}
	for i := 21; ; i++ {
		if f := fmt.Sprintf("10.0.%d", i); !used[f] {
			return f
		}
	}
}

// lanPrefixFromURL returns "a.b.c" when the URL's host is a private IPv4.
func lanPrefixFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	a, err := netip.ParseAddr(u.Hostname())
	if err != nil || !a.Is4() || !a.IsPrivate() {
		return ""
	}
	s := a.String()
	return s[:strings.LastIndex(s, ".")]
}

func (m *mapper) IP4(s string) string {
	if v, ok := m.ip4[s]; ok {
		return v
	}
	for _, fake := range m.ip4 {
		if fake == s {
			return s // already sanitized: a second pass must not remap it
		}
	}
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return s
	}
	if isSpecial4(a) {
		return s
	}
	var out string
	switch {
	case a.IsPrivate():
		prefix := s[:strings.LastIndex(s, ".")]
		fake, ok := m.nets[prefix]
		if !ok {
			fake = m.nextNet()
			m.nets[prefix] = fake
		}
		out = fake + s[strings.LastIndex(s, "."):]
	case netip.MustParsePrefix("100.64.0.0/10").Contains(a):
		m.cgnat++
		out = fmt.Sprintf("100.64.0.%d", m.cgnat)
	case a.IsLinkLocalUnicast():
		out = s
	default:
		m.pub++
		out = fmt.Sprintf("203.0.113.%d", 9+m.pub)
	}
	m.ip4[s] = out
	return out
}

func (m *mapper) IP6(s string) string {
	if v, ok := m.ip6[s]; ok {
		return v
	}
	base, zone, _ := strings.Cut(s, "%")
	a, err := netip.ParseAddr(base)
	if err != nil || !a.Is6() {
		return s
	}
	if a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() {
		return s
	}
	m.v6++
	var out string
	switch {
	case a.IsLinkLocalUnicast():
		out = fmt.Sprintf("fe80::%x", m.v6)
	case a.IsPrivate():
		out = fmt.Sprintf("fd00::%x", m.v6)
	default:
		out = fmt.Sprintf("2001:db8::%x", m.v6)
	}
	if zone != "" {
		out += "%" + zone
	}
	m.ip6[s] = out
	return out
}

func (m *mapper) MAC(s string) string {
	k := strings.ToLower(strings.ReplaceAll(s, "-", ":"))
	if v, ok := m.mac[k]; ok {
		return v
	}
	m.macN++
	out := fmt.Sprintf("02:00:00:00:%02x:%02x", m.macN/256, m.macN%256)
	m.mac[k] = out
	return out
}

// Host returns a neutral DNS name, keyed on the last octet when known.
func (m *mapper) Host(real, addr string) string {
	if v, ok := m.text[real]; ok {
		return v
	}
	var out string
	if a, err := netip.ParseAddr(addr); err == nil && a.Is4() {
		fake := m.IP4(addr)
		out = "client-" + fake[strings.LastIndex(fake, ".")+1:] + ".lab.example"
	} else {
		m.hostN++
		out = fmt.Sprintf("host-%d.lab.example", m.hostN)
	}
	m.text[real] = out
	return out
}

// Text scrubs free text: exact labels first (longest first), then any
// embedded address token.
func (m *mapper) Text(s string) string {
	keys := make([]string, 0, len(m.text))
	for k := range m.text {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		s = strings.ReplaceAll(s, k, m.text[k])
	}
	s = reMACtok.ReplaceAllStringFunc(s, m.MAC)
	s = reIP4tok.ReplaceAllStringFunc(s, m.IP4)
	s = reIP6tok.ReplaceAllStringFunc(s, func(t string) string {
		if strings.Count(t, ":") < 2 || regexp.MustCompile(`^\d+:\d+:\d+$`).MatchString(t) {
			return t // a clock time such as 13+08:52:19
		}
		return m.IP6(t)
	})
	return s
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}

func writeJSON(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
