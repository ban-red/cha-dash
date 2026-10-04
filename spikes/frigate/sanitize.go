package main

import (
	"bufio"
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
	"strings"
)

// Neutral names. The owner allows driveway, side deck and backyard; a real
// camera that already has one of these names keeps it.
var (
	neutralCameras = []string{"driveway", "side_deck", "backyard", "garage", "carport", "shed"}
	neutralZones   = []string{"walkway", "lawn", "street", "steps"}
)

// names maps user-chosen identifiers (cameras, zones, go2rtc streams) to
// neutral ones.
type names struct {
	repl  map[string]string // real → fake, all kinds
	cams  map[string]string
	order []string // real names, longest first
	res   map[string]*regexp.Regexp
}

type frigateConfig struct {
	Cameras map[string]struct {
		Zones map[string]json.RawMessage `json:"zones"`
	} `json:"cameras"`
	Go2rtc struct {
		Streams map[string]json.RawMessage `json:"streams"`
	} `json:"go2rtc"`
	MQTT struct {
		Host string `json:"host"`
		User string `json:"user"`
	} `json:"mqtt"`
}

func loadConfig(path string) (frigateConfig, error) {
	var cfg frigateConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(b, &cfg)
}

func buildNames(cfg frigateConfig) *names {
	n := &names{repl: map[string]string{}, cams: map[string]string{}, res: map[string]*regexp.Regexp{}}
	cams := make([]string, 0, len(cfg.Cameras))
	for c := range cfg.Cameras {
		cams = append(cams, c)
	}
	sort.Strings(cams)
	used := map[string]bool{}
	for _, c := range cams {
		if slices.Contains(neutralCameras, c) {
			n.cams[c], used[c] = c, true
		}
	}
	for _, c := range cams {
		if _, ok := n.cams[c]; ok {
			continue
		}
		for _, f := range neutralCameras {
			if _, real := cfg.Cameras[f]; !used[f] && !real {
				n.cams[c], used[f] = f, true
				break
			}
		}
	}
	zi := 0
	for _, c := range cams {
		zs := make([]string, 0)
		for z := range cfg.Cameras[c].Zones {
			zs = append(zs, z)
		}
		sort.Strings(zs)
		for _, z := range zs {
			if _, ok := n.repl[z]; !ok {
				n.repl[z] = neutralZones[zi%len(neutralZones)]
				zi++
			}
		}
	}
	streams := make([]string, 0, len(cfg.Go2rtc.Streams))
	for s := range cfg.Go2rtc.Streams {
		streams = append(streams, s)
	}
	sort.Strings(streams)
	si := 0
	for _, s := range streams {
		if f, ok := n.cams[s]; ok {
			n.repl[s] = f
			continue
		}
		matched := false
		for _, c := range cams {
			if strings.HasPrefix(s, c) && len(s) > len(c) {
				n.repl[s] = n.cams[c] + s[len(c):]
				matched = true
				break
			}
		}
		if !matched {
			si++
			n.repl[s] = fmt.Sprintf("stream_%02d", si)
		}
	}
	for c, f := range n.cams {
		n.repl[c] = f
	}
	for r, f := range n.repl {
		if r == f {
			continue
		}
		n.order = append(n.order, r)
		n.res[r] = regexp.MustCompile(`(^|[^A-Za-z0-9])` + regexp.QuoteMeta(r) + `($|[^A-Za-z0-9])`)
	}
	sort.Slice(n.order, func(i, j int) bool {
		if len(n.order[i]) != len(n.order[j]) {
			return len(n.order[i]) > len(n.order[j])
		}
		return n.order[i] < n.order[j]
	})
	return n
}

// Replace swaps every real name token for its neutral one. Run twice so
// tokens sharing a boundary character ("a/a") both change.
func (n *names) Replace(s string) string {
	for range 2 {
		for _, r := range n.order {
			s = n.res[r].ReplaceAllString(s, "${1}"+n.repl[r]+"${2}")
		}
	}
	return s
}

// scrubCmdline keeps the program (and Frigate's own process title), never the
// arguments: ffmpeg command lines carry stream URLs and addresses.
func scrubCmdline(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	p := f[0]
	if i := strings.LastIndex(p, "/"); i >= 0 && !strings.HasPrefix(p, "frigate.") {
		p = p[i+1:]
	}
	if p == "python3" || p == "python" {
		for _, a := range f[1:] {
			if strings.HasPrefix(a, "frigate") || strings.HasSuffix(a, ".py") {
				return p + " " + a
			}
		}
	}
	return p + map[bool]string{true: " …", false: ""}[len(f) > 1]
}

func scrubCPUUsages(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if cu, ok := m["cpu_usages"].(map[string]any); ok {
		for _, row := range cu {
			if r, ok := row.(map[string]any); ok {
				if s, ok := r["cmdline"].(string); ok {
					r["cmdline"] = scrubCmdline(s)
				}
			}
		}
	}
}

type sanitizer struct {
	raw, out string
	m        *mapper
	n        *names
}

// finish runs the text-level passes (names, addresses) on a JSON value and
// writes it.
func (s *sanitizer) write(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	txt := s.m.Text(s.n.Replace(string(b)))
	return os.WriteFile(filepath.Join(s.out, name), []byte(txt+"\n"), 0o644)
}

func (s *sanitizer) load(name string) (any, error) {
	var v any
	err := readJSON(filepath.Join(s.raw, name), &v)
	return v, err
}

func runSanitize(args []string) error {
	fs := flag.NewFlagSet("sanitize", flag.ExitOnError)
	raw := fs.String("raw", "", "raw capture directory")
	out := fs.String("out", "", "fixture directory (testdata/integrations/frigate)")
	envPath := fs.String("env", "", "dotenv file; OPNSENSE_URL marks the LAN /24 (read in memory only)")
	netmap := fs.String("netmap", "", "git-ignored file outside the repo, shared with the OPNsense spike")
	_ = fs.Parse(args)
	if *raw == "" || *out == "" {
		return fmt.Errorf("sanitize: --raw and --out are required")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	cfg, err := loadConfig(filepath.Join(*raw, "api_config.raw.json"))
	if err != nil {
		return err
	}
	s := &sanitizer{raw: *raw, out: *out, m: newMapper(), n: buildNames(cfg)}

	var corpus [][]byte
	files, _ := filepath.Glob(filepath.Join(*raw, "*.raw.json"))
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
	if err := s.m.planNets(corpus, lan, *netmap); err != nil {
		return err
	}
	if cfg.MQTT.User != "" {
		s.m.text[`"user": "`+cfg.MQTT.User+`"`] = `"user": "mqtt-user"`
	}
	if h := cfg.MQTT.Host; h != "" {
		if _, err := netip.ParseAddr(h); err != nil {
			s.m.text[h] = "mqtt.lab.example"
		}
	}

	// version (plain text)
	if b, err := os.ReadFile(filepath.Join(*raw, "api_version.raw.json")); err == nil {
		if err := os.WriteFile(filepath.Join(*out, "version.txt"), []byte(strings.TrimSpace(string(b))+"\n"), 0o644); err != nil {
			return err
		}
	}

	// config: go2rtc sources get neutral hosts; everything else by text pass.
	c, err := s.load("api_config.raw.json")
	if err != nil {
		return err
	}
	if g, ok := c.(map[string]any)["go2rtc"].(map[string]any); ok {
		if st, ok := g["streams"].(map[string]any); ok {
			for k, v := range st {
				if list, ok := v.([]any); ok {
					for i, src := range list {
						if str, ok := src.(string); ok {
							list[i] = s.neutralSource(str)
						}
					}
				}
				st[k] = v
			}
		}
	}
	if err := s.write("config.json", c); err != nil {
		return err
	}

	for _, f := range []struct {
		in, out string
		keep    int
		cpu     bool
	}{
		{"api_profile.raw.json", "profile.json", 0, false},
		{"api_stats.raw.json", "stats.json", 0, true},
		{"api_stats_history.raw.json", "stats_history.json", 3, true},
		{"api_events_limit_25_include_thumbnails_0.raw.json", "events.json", 0, false},
		{"api_events_summary.raw.json", "events_summary.json", 30, false},
		{"api_review_limit_25_after_1790474364.raw.json", "review.json", 0, false},
		{"api_review_limit_25.raw.json", "review_default_24h.json", 0, false},
		{"api_review_summary.raw.json", "review_summary.json", 0, false},
		{"api_recordings_storage.raw.json", "recordings_storage.json", 0, false},
		{"api_labels.raw.json", "labels.json", 0, false},
		{"api_sub_labels.raw.json", "sub_labels.json", 0, false},
		{"api_timeline_limit_25.raw.json", "timeline.json", 10, false},
	} {
		v, err := s.load(f.in)
		if err != nil {
			fmt.Printf("skip %s: %v\n", f.out, err)
			continue
		}
		if list, ok := v.([]any); ok && f.keep > 0 && len(list) > f.keep {
			v = list[:f.keep]
		}
		if f.cpu {
			if list, ok := v.([]any); ok {
				for _, it := range list {
					scrubCPUUsages(it)
				}
			} else {
				scrubCPUUsages(v)
			}
		}
		if err := s.write(f.out, v); err != nil {
			return err
		}
	}

	// websocket transcripts
	for _, f := range []struct{ in, out string }{
		{"ws_ws_onconnect.raw.json", "ws_onconnect.json"},
		{"ws_ws.raw.json", "ws_passive.json"},
	} {
		v, err := s.load(f.in)
		if err != nil {
			continue
		}
		if msgs, ok := v.(map[string]any)["messages"].([]any); ok {
			for _, mm := range msgs {
				if m, ok := mm.(map[string]any); ok && m["topic"] == "stats" {
					scrubCPUUsages(m["payload"])
				}
			}
		}
		if err := s.write(f.out, v); err != nil {
			return err
		}
	}

	// endpoint and media index
	var index map[string]Result
	if err := readJSON(filepath.Join(*raw, "index.raw.json"), &index); err != nil {
		return err
	}
	type row struct {
		Request     string            `json:"request"`
		Status      int               `json:"status"`
		Bytes       int64             `json:"bytes"`
		Declared    int64             `json:"content_length,omitempty"`
		Millis      int64             `json:"ms"`
		ContentType string            `json:"content_type,omitempty"`
		Headers     map[string]string `json:"headers,omitempty"`
	}
	var api, media []row
	for k, r := range index {
		req := k
		if !strings.Contains(k, " ") {
			req = "GET " + k
		}
		rw := row{req, r.Status, r.Bytes, r.Declared, r.Millis, r.ContentType, r.Headers}
		if isMedia(r.ContentType) || strings.HasPrefix(k, "HEAD ") || strings.Contains(k, ".jpg") || strings.Contains(k, ".mp4") || strings.Contains(k, ".webp") {
			media = append(media, rw)
		} else {
			api = append(api, rw)
		}
	}
	sort.Slice(api, func(i, j int) bool { return api[i].Request < api[j].Request })
	sort.Slice(media, func(i, j int) bool { return media[i].Request < media[j].Request })
	if err := s.write("endpoints.json", api); err != nil {
		return err
	}
	if err := s.write("media.json", map[string]any{
		"_note":    "Status, type and size only. No image, snapshot, thumbnail or clip bytes were stored; the spike counted them into io.Discard.",
		"requests": media,
	}); err != nil {
		return err
	}
	fmt.Printf("sanitized: %d cameras, %d names mapped, %d IPv4\n", len(s.n.cams), len(s.n.order), len(s.m.ip4))
	return nil
}

// neutralSource rewrites a go2rtc source: credentials stay as Frigate masked
// them (*:*), the host becomes a sanitized address or name, stream names in
// ffmpeg: references are mapped by the text pass.
func (s *sanitizer) neutralSource(src string) string {
	prefix := ""
	rest := src
	if strings.HasPrefix(rest, "ffmpeg:") && strings.Contains(rest, "://") {
		prefix, rest = "ffmpeg:", strings.TrimPrefix(rest, "ffmpeg:")
	}
	u, err := url.Parse(rest)
	if err != nil || u.Host == "" {
		return src
	}
	host := u.Hostname()
	if _, err := netip.ParseAddr(host); err != nil {
		fake, ok := s.m.text[host]
		if !ok {
			s.m.hostN++
			fake = fmt.Sprintf("camera-%d.lab.example", s.m.hostN)
			s.m.text[host] = fake
		}
		host = fake
	} else {
		host = s.m.IP4(host)
	}
	if p := u.Port(); p != "" {
		u.Host = host + ":" + p
	} else {
		u.Host = host
	}
	out := u.String()
	out = strings.Replace(out, "%2A:%2A@", "*:*@", 1) // keep Frigate's mask readable
	return prefix + out
}

// runNeedles writes the leak-check list for Frigate: addresses, user-chosen
// names (except the allowed neutral camera names), MQTT identity, sub-labels,
// credentials found while scrubbing, and every dotenv value.
func runNeedles(args []string) error {
	fs := flag.NewFlagSet("needles", flag.ExitOnError)
	raw := fs.String("raw", "", "raw capture directory")
	envPath := fs.String("env", "", "dotenv file")
	secPath := fs.String("secrets", "", "credentials collected while scrubbing (merged in)")
	out := fs.String("out", "", "needle file (outside the repo)")
	_ = fs.Parse(args)
	if *raw == "" || *out == "" {
		return fmt.Errorf("needles: --raw and --out are required")
	}
	set := map[string]string{}
	add := func(class, v string) {
		v = strings.TrimSpace(v)
		if len(v) < 3 || v == redactedSentinel {
			return
		}
		if _, ok := set[v]; !ok {
			set[v] = class
		}
	}
	cfg, err := loadConfig(filepath.Join(*raw, "api_config.raw.json"))
	if err != nil {
		return err
	}
	for c, cc := range cfg.Cameras {
		if !slices.Contains(neutralCameras[:3], c) {
			add("camera", c)
		}
		for z := range cc.Zones {
			add("zone", z)
		}
	}
	for st, v := range cfg.Go2rtc.Streams {
		if !slices.Contains(neutralCameras[:3], st) && !strings.HasPrefix(st, "driveway") && !strings.HasPrefix(st, "side_deck") {
			add("stream", st)
		}
		var list []string
		_ = json.Unmarshal(v, &list)
		for _, src := range list {
			rest := strings.TrimPrefix(src, "ffmpeg:")
			if u, err := url.Parse(rest); err == nil && u.Hostname() != "" {
				if _, err := netip.ParseAddr(u.Hostname()); err != nil {
					add("host", u.Hostname())
				}
			}
		}
	}
	add("mqtt-user", cfg.MQTT.User)
	if _, err := netip.ParseAddr(cfg.MQTT.Host); err != nil {
		add("host", cfg.MQTT.Host)
	}
	files, _ := filepath.Glob(filepath.Join(*raw, "*.raw.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		collectTokens(string(b), add)
		var v any
		if json.Unmarshal(b, &v) == nil {
			walkStrings(v, "", func(key, s string) {
				switch key {
				case "sub_label", "sub_labels", "recognized_license_plate", "face", "faces":
					add("label", s)
				}
			})
		}
	}
	if *secPath != "" {
		if b, err := os.ReadFile(*secPath); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if c, v, ok := strings.Cut(l, "\t"); ok {
					add(c, v)
				}
			}
		}
	}
	if *envPath != "" {
		env, err := loadEnv(*envPath)
		if err != nil {
			return err
		}
		for k, v := range env {
			add("env:"+k, v)
			if u, err := url.Parse(v); err == nil && u.Host != "" {
				add("env-host", u.Host)
				add("env-host", u.Hostname())
			}
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	counts := map[string]int{}
	for _, k := range keys {
		fmt.Fprintf(w, "%s\t%s\n", set[k], k)
		c := set[k]
		if strings.HasPrefix(c, "env:") {
			c = "env"
		}
		counts[c]++
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Printf("needles written: %d (%v)\n", len(keys), counts)
	return nil
}

func collectTokens(s string, add func(class, v string)) {
	for _, t := range reIP4tok.FindAllString(s, -1) {
		if a, err := netip.ParseAddr(t); err == nil && !isSpecial4(a) {
			add("ip4", t)
		}
	}
	for _, t := range reMACtok.FindAllString(s, -1) {
		add("mac", t)
	}
	for _, t := range reIP6tok.FindAllString(s, -1) {
		base, _, _ := strings.Cut(t, "%")
		if a, err := netip.ParseAddr(base); err == nil && a.Is6() && !a.IsLoopback() && !a.IsUnspecified() {
			add("ip6", base)
		}
	}
}

func walkStrings(v any, key string, fn func(key, s string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			walkStrings(vv, k, fn)
		}
	case []any:
		for _, vv := range t {
			walkStrings(vv, key, fn)
		}
	case string:
		fn(key, t)
	}
}
