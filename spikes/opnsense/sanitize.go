package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
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

// Interface descriptions other than the stock WAN/LAN are user labels.
var neutralIfNames = map[string]string{
	"opt1": "Net A", "opt2": "Net B", "opt3": "Net C", "opt4": "Net D", "opt5": "Net E", "opt6": "Tailnet",
}

// System accounts that may stay in process listings.
var systemUsers = map[string]bool{"root": true, "www": true, "unbound": true, "nobody": true, "_dhcp": true, "hostd": true, "_flowd": true, "kea": true, "dnsmasq": true, "ntpd": true, "_ntp": true, "daemon": true, "_pflogd": true}

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

func runSanitize(args []string) error {
	fs := flag.NewFlagSet("sanitize", flag.ExitOnError)
	raw := fs.String("raw", "", "raw capture directory")
	out := fs.String("out", "", "fixture directory (testdata/integrations/opnsense)")
	envPath := fs.String("env", "", "dotenv file; OPNSENSE_URL marks the LAN /24 (read in memory only)")
	netmap := fs.String("netmap", "", "git-ignored file outside the repo, shared with the Frigate spike")
	_ = fs.Parse(args)
	if *raw == "" || *out == "" {
		return fmt.Errorf("sanitize: --raw and --out are required")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	rp := func(name string) string { return filepath.Join(*raw, name) }
	m := newMapper()

	var corpus [][]byte
	files, _ := filepath.Glob(rp("*.raw.json"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		corpus = append(corpus, b)
	}
	lan := ""
	if *envPath != "" {
		env, err := loadEnv(*envPath)
		if err != nil {
			return err
		}
		lan = lanPrefixFromURL(env["OPNSENSE_URL"])
	}
	if err := m.planNets(corpus, lan, *netmap); err != nil {
		return err
	}

	// 1. Interface counters. Learn the label map from here.
	var ti map[string]any
	if err := readJSON(rp("api_diagnostics_traffic_interface.raw.json"), &ti); err != nil {
		return err
	}
	ifs := ti["interfaces"].(map[string]any)
	for key, v := range ifs {
		row := v.(map[string]any)
		name, _ := row["name"].(string)
		if repl, ok := neutralIfNames[key]; ok && name != "" {
			m.text[`"name":"`+name+`"`] = `"name":"` + repl + `"`
			m.text[`"name": "`+name+`"`] = `"name": "` + repl + `"`
			row["name"] = repl
		}
	}
	if err := writeJSON(filepath.Join(*out, "traffic_interface.json"), ti); err != nil {
		return err
	}

	// 2. SSE transcripts: keep the wire format, scrub labels in place.
	for _, s := range []struct {
		in, out string
		keep    int
	}{
		{"stream_api_diagnostics_traffic_stream_1.raw.json", "traffic_stream_1s.sse", 8},
		{"stream_api_diagnostics_traffic_stream_5.raw.json", "traffic_stream_5s.sse", 4},
	} {
		var rec struct {
			Result Result        `json:"result"`
			Events []streamEvent `json:"events"`
			Window string        `json:"window"`
		}
		if err := readJSON(rp(s.in), &rec); err != nil {
			return err
		}
		var sse strings.Builder
		fmt.Fprintf(&sse, ": recorded by spikes/opnsense (GET %s), %d of %d events, window %s; see README\n\n", rec.Result.Path, min(s.keep, len(rec.Events)), len(rec.Events), rec.Window)
		for i, e := range rec.Events {
			if i >= s.keep {
				break
			}
			fmt.Fprintf(&sse, "event: %s\ndata: %s\n\n", e.Event, m.Text(e.Data))
		}
		if err := os.WriteFile(filepath.Join(*out, s.out), []byte(sse.String()), 0o644); err != nil {
			return err
		}
		timing := map[string]any{
			"path": rec.Result.Path, "status": rec.Result.Status, "content_type": rec.Result.ContentType,
			"headers": rec.Result.Headers, "window": rec.Window, "total_bytes": rec.Result.Bytes,
		}
		var evs []map[string]any
		for _, e := range rec.Events {
			evs = append(evs, map[string]any{"at_ms": e.AtMillis, "event": e.Event, "bytes": e.Bytes})
		}
		timing["events"] = evs
		if err := writeJSON(filepath.Join(*out, strings.TrimSuffix(s.out, ".sse")+"_timing.json"), timing); err != nil {
			return err
		}
	}

	// 3. top(1) via System Activity: headers verbatim (numbers only), the
	//    process list cut to the 10 busiest rows, arguments dropped.
	var act map[string]any
	if err := readJSON(rp("api_diagnostics_activity_get_activity.raw.json"), &act); err != nil {
		return err
	}
	hdrs := act["headers"].([]any)
	for i, h := range hdrs {
		hdrs[i] = m.Text(h.(string))
	}
	details := act["details"].([]any)
	total := len(details)
	sort.SliceStable(details, func(i, j int) bool {
		return pct(details[i]) > pct(details[j])
	})
	keep := details[:min(10, len(details))]
	for _, d := range keep {
		row := d.(map[string]any)
		cmd, _ := row["COMMAND"].(string)
		row["COMMAND"] = programName(cmd)
		if u, _ := row["USERNAME"].(string); !systemUsers[u] {
			row["USERNAME"] = "user"
		}
	}
	act["details"] = keep
	act["_fixture_note"] = fmt.Sprintf("details cut from %d rows to the %d busiest; COMMAND reduced to the program name", total, len(keep))
	if err := writeJSON(filepath.Join(*out, "activity.json"), act); err != nil {
		return err
	}

	// 4. Top talkers on LAN: addresses mapped, reverse-DNS names replaced.
	var top map[string]any
	if err := readJSON(rp("api_diagnostics_traffic_top_lan.raw.json"), &top); err == nil {
		lan := top["lan"].(map[string]any)
		for _, r := range lan["records"].([]any) {
			row := r.(map[string]any)
			addr, _ := row["address"].(string)
			if rn, ok := row["rname"].(string); ok && rn != "" {
				row["rname"] = m.Host(rn, addr)
			}
			row["address"] = m.Text(addr)
			for _, d := range row["details"].([]any) {
				dr := d.(map[string]any)
				da, _ := dr["address"].(string)
				if rn, ok := dr["rname"].(string); ok && rn != "" {
					dr["rname"] = m.Host(rn, da)
				}
				dr["address"] = m.Text(da)
			}
		}
		if err := writeJSON(filepath.Join(*out, "traffic_top_lan.json"), top); err != nil {
			return err
		}
	}

	// 5. Menu tree: the caller's ACL fingerprint (stock menu entries only).
	if b, err := os.ReadFile(rp("api_core_menu_tree.raw.json")); err == nil {
		var v any
		_ = json.Unmarshal(b, &v)
		if err := writeJSON(filepath.Join(*out, "menu_tree.json"), v); err != nil {
			return err
		}
	}

	// 6. Endpoint map: status per path, with the privilege that grants it.
	var index map[string]Result
	if err := readJSON(rp("index.raw.json"), &index); err != nil {
		return err
	}
	priv := map[string]Endpoint{}
	for _, e := range catalogue {
		priv[e.Path] = e
	}
	priv["/api/diagnostics/traffic/top/lan"] = Endpoint{Priv: "Reporting: Traffic", Note: "iftop for 2 s on the interface, plus reverse DNS"}
	type row struct {
		Path        string            `json:"path"`
		Status      int               `json:"status"`
		Bytes       int               `json:"bytes"`
		Millis      int64             `json:"ms"`
		ContentType string            `json:"content_type"`
		Privilege   string            `json:"privilege,omitempty"`
		Note        string            `json:"note,omitempty"`
		Headers     map[string]string `json:"headers,omitempty"`
	}
	var rows []row
	for p, r := range index {
		if strings.Contains(p, "systemInformation") {
			continue // camelCase duplicate from the first contact
		}
		rows = append(rows, row{p, r.Status, r.Bytes, r.Millis, r.ContentType, priv[p].Priv, priv[p].Note, r.Headers})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	if err := writeJSON(filepath.Join(*out, "endpoints.json"), rows); err != nil {
		return err
	}
	// The 403 body, verbatim.
	if b, err := os.ReadFile(rp("api_core_firmware_status.raw.json")); err == nil {
		if err := os.WriteFile(filepath.Join(*out, "forbidden_403.json"), append(bytes.TrimSpace(b), '\n'), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("sanitized: %d private /24s, %d IPv4, %d IPv6, %d MACs, %d labels mapped\n", len(m.nets), len(m.ip4), len(m.ip6), len(m.mac), len(m.text))
	return nil
}

func pct(v any) float64 {
	s, _ := v.(map[string]any)["WCPU"].(string)
	f, _ := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	return f
}

// programName keeps only the executable's base name: process arguments can
// carry addresses, interface labels and paths.
func programName(cmd string) string {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return ""
	}
	p := f[0]
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	if i := strings.IndexAny(p, "{:"); i > 0 { // "idle{idle: cpu0}" → "idle"
		p = p[:i]
	}
	return strings.Trim(p, "[]")
}
