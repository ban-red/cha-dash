package labkit

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// State is the sanitizer's memory between runs: the salt for re-hashing IDs
// and the real → fake maps. It holds real values, so it lives outside the repo
// (chmod 600) and is never committed.
type State struct {
	Salt    string            `json:"salt"`
	Domains map[string]string `json:"domains"` // real registrable domain → fake (lab.example, family.example, …)
	Words   map[string]string `json:"words"`   // real literal (case-insensitive) → neutral replacement
	LANs    []string          `json:"lans"`    // real /24s that become 10.0.11.0/24
	Others  []string          `json:"others"`  // other private /24s → 10.0.12+k.0/24
	Public  []string          `json:"public"`  // public addresses → 203.0.113.k
	MACs    []string          `json:"macs"`
	Keep    []string          `json:"keep"` // registrable domains confirmed public (kept as is)
}

// DefaultKeep lists registrable domains of public projects, registries and
// vendors. URLs to them stay as they are; any other domain found in a URL or
// a Host() rule is treated as personal and rewritten.
var DefaultKeep = []string{
	"github.com", "githubusercontent.com", "github.io", "docker.com", "docker.io", "ghcr.io", "lscr.io", "linuxserver.io",
	"gitlab.com", "quay.io", "hotio.dev", "imagegenius.io", "mealie.io", "immich.app", "plex.tv", "unraid.net",
	"jsdelivr.net", "nvidia.com", "binhex.net", "discord.gg", "discord.com", "redis.io", "haproxy.org", "python.org",
	"pypa.io", "nodejs.org", "npmjs.org", "debian.org", "ubuntu.com", "alpinelinux.org", "archlinux.org", "openai.com",
	"microsoft.com", "google.com", "googleapis.com", "cloudflare.com", "letsencrypt.org", "kasmweb.com",
	"nginx.com", "nginx.org", "paperless-ngx.com", "lavaforge.org",
}

// LoadState reads path, or starts a fresh state with a random salt.
func LoadState(path string) (*State, error) {
	st, err := loadState(path)
	if err != nil {
		return nil, err
	}
	for _, d := range DefaultKeep {
		if !slices.Contains(st.Keep, d) {
			st.Keep = append(st.Keep, d)
		}
	}
	return st, nil
}

func loadState(path string) (*State, error) {
	st := &State{Domains: map[string]string{}, Words: map[string]string{}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, st); err != nil {
			return nil, fmt.Errorf("state: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if st.Salt == "" {
		var r [32]byte
		rand.Read(r[:])
		st.Salt = hex.EncodeToString(r[:])
	}
	if st.Domains == nil {
		st.Domains = map[string]string{}
	}
	if st.Words == nil {
		st.Words = map[string]string{}
	}
	return st, nil
}

// Save writes the state with mode 600.
func (st *State) Save(path string) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// Scrubber rewrites strings and JSON trees so they carry no real domain,
// address, MAC, ID, email or secret. Learn first (pass 1), then Scrub (pass 2).
type Scrubber struct {
	st      *State
	salt    []byte
	ids     map[string]bool   // real 64-hex ids seen in pass 1
	short12 map[string]string // 12-char prefix → id

	// Real collects every real value replaced or learned, with its kind,
	// for the leak list.
	Real map[string]string

	domainRes []domainRe
	wordRes   []wordRe
	short     *regexp.Regexp
}

type domainRe struct {
	re   *regexp.Regexp
	dash *regexp.Regexp // the domain with dots as dashes (router names)
	esc  *regexp.Regexp // the domain with escaped dots (HostRegexp patterns)
	fake string
}

type wordRe struct {
	re *regexp.Regexp
	to string
}

// NewScrubber builds a scrubber over st.
func NewScrubber(st *State) *Scrubber {
	salt, _ := hex.DecodeString(st.Salt)
	s := &Scrubber{st: st, salt: salt, ids: map[string]bool{}, Real: map[string]string{}}
	s.compile()
	return s
}

var (
	hex64Re = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	hex12Re = regexp.MustCompile(`\b[0-9a-f]{12}\b`)
	ipv4Re  = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6Re  = regexp.MustCompile(`(?i)[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}(?:%[a-z0-9]+)?`)
	macRe   = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?::[0-9a-f]{2}){5}\b`)
	// An email not preceded by ":" or "/" (that would be URL userinfo).
	emailRe    = regexp.MustCompile(`(?i)(^|[^:/a-z0-9._%+-])([a-z0-9._%+-]+@(?:[a-z0-9-]+\.)+[a-z]{2,24})\b`)
	userinfoRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s@:"'\x60]+:[^/\s@"'\x60]*@`)
	urlHostRe  = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://(?:[^/\s@"'\x60]*@)?(\[[0-9a-f:.]+\]|[^/\s:?#"'\x60,;)\]]+)`)
	hostRuleRe = regexp.MustCompile("(?i)Host(?:SNI|Regexp|SNIRegexp)?\\(((?:\\s*(?:`[^`]*`|\"[^\"]*\")\\s*,?)+)\\)")
	quotedRe   = regexp.MustCompile("[`\"']([^`\"']+)[`\"']")
	fqdnRe     = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}\b`)
	hexish     = regexp.MustCompile(`^[0-9a-f-]+$`)
	// Secret-looking query parameters (?apikey=…, &token=…) in any URL.
	secretQueryRe = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|apikey|x-api-key|token|access_token|auth|key|pass|password|secret|sig)=)[^&\s"'\x60]+`)
	// Secret-looking command-line flags: --api-key=X, --token X, -password=X.
	secretFlagRe = regexp.MustCompile(`(?i)(--?[a-z0-9_.-]*(?:pass(?:word|wd)?|token|secret|api[_-]?key|apikey|credential)[a-z0-9_.-]*(?:=|\s+))([^\s"'\x60]+)`)
	secretKey    = regexp.MustCompile(`(?i)(passw(or)?d|passwd|_pass$|^pass$|secret|token|api[-_]?key|apikey|(^|[-_.])key$|^users$|credential|private[-_]?key|^auth(orization)?$|bearer|cookie|jwt|signature|claim)`)
)

// RuleHosts returns the arguments of every Host/HostSNI/HostRegexp matcher in
// a Traefik rule (regexp patterns are returned as written).
func RuleHosts(rule string) []string {
	var out []string
	for _, m := range hostRuleRe.FindAllStringSubmatch(rule, -1) {
		for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
			out = append(out, q[1])
		}
	}
	return out
}

// SecretLookingKey reports whether an env/label/config key looks like it holds
// a secret (*_KEY, *_TOKEN, *PASSWORD*, *SECRET*, …).
func SecretLookingKey(k string) bool { return secretKey.MatchString(k) }

// publicSuffix2 lists two-label public suffixes (incl. dynamic-DNS and
// tailnet zones, so the user's own label counts as their domain).
var publicSuffix2 = map[string]bool{
	"co.uk": true, "org.uk": true, "me.uk": true, "com.au": true, "net.au": true, "co.nz": true, "com.br": true,
	"co.jp": true, "co.za": true, "com.mx": true, "co.in": true,
	"duckdns.org": true, "ddns.net": true, "dynu.net": true, "no-ip.org": true, "no-ip.com": true, "hopto.org": true,
	"zapto.org": true, "synology.me": true, "myqnapcloud.com": true, "ts.net": true, "freeddns.org": true,
	"dyndns.org": true, "mooo.com": true, "afraid.org": true, "nip.io": true, "sslip.io": true,
}

// Registrable returns the registrable domain (eTLD+1) of host, lowercased.
func Registrable(host string) string {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return h
	}
	n := 2
	if len(labels) >= 3 && publicSuffix2[strings.Join(labels[len(labels)-2:], ".")] {
		n = 3
	}
	if len(labels) < n {
		return h
	}
	return strings.Join(labels[len(labels)-n:], ".")
}

// Safe registrable domains: never rewritten.
var safeDomains = map[string]bool{
	"example": true, "lab.example": true, "family.example": true, "example.com": true, "example.org": true,
	"localhost": true, "local": true, "lan": true, "internal": true, "home.arpa": true,
}

func isSafeDomain(reg string) bool {
	return safeDomains[reg] || strings.HasSuffix(reg, ".example") || strings.HasSuffix(reg, ".localhost") ||
		regexp.MustCompile(`^(host|home)\d+\.(local|lan|internal|home\.arpa)$`).MatchString(reg)
}

// lanSuffix returns the LAN-only suffix of a registrable domain (.local, .lan,
// .internal, .home.arpa), or "".
func lanSuffix(reg string) string {
	for _, sfx := range []string{"home.arpa", "local", "lan", "internal"} {
		if strings.HasSuffix(reg, "."+sfx) {
			return sfx
		}
	}
	return ""
}

// AddDomain marks a host's registrable domain as personal (to be rewritten).
// Fakes are handed out in order: lab.example, family.example, lab3.example, …
func (s *Scrubber) AddDomain(host string) {
	host = strings.Trim(strings.ToLower(host), ".*` \"'")
	if host == "" || !strings.Contains(host, ".") {
		return
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return
	}
	if strings.ContainsAny(host, "{}()^$|\\+?[]") { // a HostRegexp pattern
		for _, m := range fqdnRe.FindAllString(strings.ReplaceAll(host, `\.`, "."), -1) {
			s.AddDomain(m)
		}
		return
	}
	reg := Registrable(host)
	if isSafeDomain(reg) || slices.Contains(s.st.Keep, reg) {
		return
	}
	s.Real[host] = "fqdn"
	if _, ok := s.st.Domains[reg]; !ok {
		var fake string
		if sfx := lanSuffix(reg); sfx != "" {
			// LAN-only names keep their suffix (mDNS .local, .lan) so the
			// fixture still shows they can't have a public certificate.
			n := 1
			for _, f := range s.st.Domains {
				if strings.HasSuffix(f, "."+sfx) {
					n++
				}
			}
			prefix := "home"
			if sfx == "local" {
				prefix = "host"
			}
			fake = fmt.Sprintf("%s%d.%s", prefix, n, sfx)
		} else {
			fakes := []string{"lab.example", "family.example"}
			n := 0
			for _, f := range s.st.Domains {
				if strings.HasSuffix(f, ".example") {
					n++
				}
			}
			fake = fmt.Sprintf("lab%d.example", n+1)
			if n < len(fakes) {
				fake = fakes[n]
			}
		}
		s.st.Domains[reg] = fake
		s.compile()
	}
	s.Real[reg] = "domain"
}

// AddWord adds a literal (e.g. a personal folder name) that is replaced
// case-insensitively everywhere.
func (s *Scrubber) AddWord(real, to string) {
	s.st.Words[real] = to
	s.Real[real] = "word"
	s.compile()
}

func (s *Scrubber) compile() {
	s.domainRes = s.domainRes[:0]
	regs := make([]string, 0, len(s.st.Domains))
	for r := range s.st.Domains {
		regs = append(regs, r)
	}
	sort.Slice(regs, func(i, j int) bool { return len(regs[i]) > len(regs[j]) })
	for _, r := range regs {
		dash := strings.ReplaceAll(r, ".", "-")
		s.domainRes = append(s.domainRes, domainRe{
			re:   regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(r) + `\b`),
			dash: regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(dash) + `\b`),
			esc:  regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(strings.ReplaceAll(r, ".", `\.`)) + `\b`),
			fake: s.st.Domains[r],
		})
		s.Real[r] = "domain"
		if len(dash) >= 6 {
			s.Real[dash] = "domain"
		}
	}
	s.wordRes = s.wordRes[:0]
	words := make([]string, 0, len(s.st.Words))
	for w := range s.st.Words {
		words = append(words, w)
	}
	sort.Slice(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })
	for _, w := range words {
		s.wordRes = append(s.wordRes, wordRe{re: regexp.MustCompile(`(?i)` + regexp.QuoteMeta(w)), to: s.st.Words[w]})
		s.Real[w] = "word"
	}
}

// LearnString is pass 1 for one string: it collects 64-hex ids, URL hosts and
// Host() rule hosts, and remembers addresses, MACs and emails for the leak list.
func (s *Scrubber) LearnString(v string) {
	for _, m := range hex64Re.FindAllString(v, -1) {
		s.ids[m] = true
	}
	for _, m := range hostRuleRe.FindAllStringSubmatch(v, -1) {
		for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
			s.AddDomain(q[1])
		}
	}
	for _, m := range urlHostRe.FindAllStringSubmatch(v, -1) {
		s.AddDomain(m[1])
	}
	for _, sm := range emailRe.FindAllStringSubmatch(v, -1) {
		m := sm[2]
		s.Real[m] = "email"
		_, dom, _ := strings.Cut(m, "@")
		if !isPublicMailDomain(dom) {
			s.AddDomain(dom)
		}
	}
	for _, m := range ipv4Re.FindAllString(v, -1) {
		if a, err := netip.ParseAddr(m); err == nil && !keepAddr(a) {
			s.Real[m] = "ipv4"
		}
	}
	for _, m := range macRe.FindAllString(v, -1) {
		s.Real[strings.ToLower(m)] = "mac"
	}
}

// LearnLeakOnly is pass 1 for values that are redacted wholesale (env): URL
// hosts, emails, addresses and MACs only join the leak list. They don't enter
// the rewrite map, so an image's baked-in project URL doesn't turn a public
// domain into a "personal" one. If such a value shows up anywhere else, the
// leak check flags it.
func (s *Scrubber) LearnLeakOnly(v string) {
	for _, m := range hex64Re.FindAllString(v, -1) {
		s.ids[m] = true
	}
	for _, m := range urlHostRe.FindAllStringSubmatch(v, -1) {
		h := strings.ToLower(m[1])
		reg := Registrable(h)
		if _, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil || !strings.Contains(h, ".") ||
			isSafeDomain(reg) || slices.Contains(s.st.Keep, reg) {
			continue
		}
		s.Real[h] = "env-url-host"
	}
	for _, sm := range emailRe.FindAllStringSubmatch(v, -1) {
		s.Real[sm[2]] = "email"
	}
	for _, m := range ipv4Re.FindAllString(v, -1) {
		if a, err := netip.ParseAddr(m); err == nil && !keepAddr(a) {
			s.Real[m] = "ipv4"
		}
	}
	for _, m := range macRe.FindAllString(v, -1) {
		s.Real[strings.ToLower(m)] = "mac"
	}
}

// PinDomain sets the fake for a real domain explicitly (e.g. to line up with
// another fixture's choice of lab.example / family.example).
func (s *Scrubber) PinDomain(real, fake string) {
	s.st.Domains[strings.ToLower(real)] = fake
	s.compile()
}

// LearnTree runs LearnString over every key and string value.
func (s *Scrubber) LearnTree(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			s.LearnString(k)
			s.LearnTree(x)
		}
	case []any:
		for _, x := range t {
			s.LearnTree(x)
		}
	case string:
		s.LearnString(t)
	}
}

// Finish ends pass 1: known ids become rewritable, also as 12-char prefixes.
func (s *Scrubber) Finish() {
	s.short12 = map[string]string{}
	for id := range s.ids {
		s.Real[id] = "id"
		s.Real[id[:12]] = "id12"
		s.short12[id[:12]] = id
	}
}

func isPublicMailDomain(d string) bool {
	switch strings.ToLower(d) {
	case "gmail.com", "googlemail.com", "outlook.com", "hotmail.com", "icloud.com", "yahoo.com", "proton.me", "protonmail.com":
		return true
	}
	return false
}

func keepAddr(a netip.Addr) bool {
	return a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() || a.IsInterfaceLocalMulticast() ||
		(a.Is4() && a.As4()[0] == 255) || (a.Is4() && a.As4()[0] == 0)
}

// ID returns the stable fake for a real 64-hex id.
func (s *Scrubber) ID(real string) string {
	m := hmac.New(sha256.New, s.salt)
	m.Write([]byte(strings.ToLower(real)))
	return hex.EncodeToString(m.Sum(nil))
}

// String scrubs one string.
func (s *Scrubber) String(in string) string {
	out := in
	for _, w := range s.wordRes {
		out = w.re.ReplaceAllString(out, w.to)
	}
	out = userinfoRe.ReplaceAllString(out, "${1}redacted:redacted@")
	out = secretQueryRe.ReplaceAllString(out, "${1}REDACTED")
	out = emailRe.ReplaceAllStringFunc(out, func(full string) string {
		sm := emailRe.FindStringSubmatch(full)
		pre, m := sm[1], sm[2]
		local, _, _ := strings.Cut(m, "@")
		if strings.EqualFold(local, "git") {
			return full
		}
		s.Real[m] = "email"
		return pre + "redacted@example.com"
	})
	for _, d := range s.domainRes {
		out = d.esc.ReplaceAllLiteralString(out, strings.ReplaceAll(d.fake, ".", `\.`))
		out = d.re.ReplaceAllString(out, d.fake)
		out = d.dash.ReplaceAllString(out, strings.ReplaceAll(d.fake, ".", "-"))
	}
	out = macRe.ReplaceAllStringFunc(out, func(m string) string { return s.mac(strings.ToLower(m)) })
	out = ipv4Re.ReplaceAllStringFunc(out, s.ipv4)
	out = ipv6Re.ReplaceAllStringFunc(out, s.ipv6)
	// Every 64-hex string is an id, a digest or a key: all are re-hashed.
	out = hex64Re.ReplaceAllStringFunc(out, s.ID)
	out = hex12Re.ReplaceAllStringFunc(out, func(m string) string {
		if id, ok := s.short12[m]; ok {
			return s.ID(id)[:12]
		}
		return m
	})
	return out
}

func (s *Scrubber) mac(m string) string {
	i := slices.Index(s.st.MACs, m)
	if i < 0 {
		s.st.MACs = append(s.st.MACs, m)
		i = len(s.st.MACs) - 1
	}
	s.Real[m] = "mac"
	return fmt.Sprintf("02:42:0a:00:%02x:%02x", (i+1)>>8, (i+1)&0xff)
}

func (s *Scrubber) ipv4(m string) string {
	a, err := netip.ParseAddr(m)
	if err != nil || keepAddr(a) {
		return m
	}
	s.Real[m] = "ipv4"
	b := a.As4()
	net24 := fmt.Sprintf("%d.%d.%d.0/24", b[0], b[1], b[2])
	switch {
	case slices.Contains(s.st.LANs, net24):
		return fmt.Sprintf("10.0.11.%d", b[3])
	case b[0] == 172 && b[1] >= 16 && b[1] <= 31:
		// Docker bridge space: 172.(16+n).c.d → 10.(100+n).c.d keeps the
		// network structure (gateways .1, /16 subnets) without real values.
		return fmt.Sprintf("10.%d.%d.%d", 100+int(b[1])-16, b[2], b[3])
	case a.IsPrivate() || a.IsLinkLocalUnicast():
		i := slices.Index(s.st.Others, net24)
		if i < 0 {
			s.st.Others = append(s.st.Others, net24)
			i = len(s.st.Others) - 1
		}
		return fmt.Sprintf("10.0.%d.%d", 12+i, b[3])
	default:
		i := slices.Index(s.st.Public, m)
		if i < 0 {
			s.st.Public = append(s.st.Public, m)
			i = len(s.st.Public) - 1
		}
		if i < 254 {
			return fmt.Sprintf("203.0.113.%d", i+1)
		}
		return fmt.Sprintf("198.51.100.%d", (i-254)%254+1)
	}
}

func (s *Scrubber) ipv6(m string) string {
	if strings.Count(m, ":") < 2 {
		return m
	}
	a, err := netip.ParseAddr(m)
	if err != nil || a.Is4() || keepAddr(a) {
		return m
	}
	s.Real[m] = "ipv6"
	sum := sha256.Sum256(append(s.salt, m...))
	tail := fmt.Sprintf("%x:%x", uint16(sum[0])<<8|uint16(sum[1]), uint16(sum[2])<<8|uint16(sum[3]))
	switch {
	case a.IsLinkLocalUnicast():
		return "fe80::" + tail
	case a.IsPrivate():
		return "fd00:db8::" + tail
	default:
		return "2001:db8::" + tail
	}
}

// AddLAN marks the /24 around addr as the home LAN (→ 10.0.11.0/24).
func (s *Scrubber) AddLAN(addr string) {
	a, err := netip.ParseAddr(addr)
	if err != nil || !a.Is4() {
		return
	}
	b := a.As4()
	n := fmt.Sprintf("%d.%d.%d.0/24", b[0], b[1], b[2])
	if !slices.Contains(s.st.LANs, n) {
		s.st.LANs = append(s.st.LANs, n)
	}
	s.Real[addr] = "ipv4"
}

// FakeSecret returns a deterministic fake that keeps v's format: digits stay
// digits, hex stays hex, case is kept, separators and short prefixes (uk2_,
// sk-) stay. A user:hash pair keeps the shape with user "admin".
func (s *Scrubber) FakeSecret(v string) string {
	if v == "" {
		return v
	}
	if strings.EqualFold(v, "redacted") || strings.HasPrefix(v, "FAKE") {
		return v
	}
	if len(v) >= 4 {
		s.Real[v] = "secret"
	}
	if u, h, ok := strings.Cut(v, ":"); ok && !strings.Contains(v, "://") && len(u) < 40 && strings.HasPrefix(h, "$") {
		// htpasswd: keep the scheme ($apr1$, $2y$) and a bcrypt cost; fake the rest.
		parts := strings.Split(h, "$")
		keep := 2
		if len(parts) > 2 && strings.HasPrefix(parts[1], "2") {
			keep = 3
		}
		for i := keep; i < len(parts); i++ {
			parts[i] = s.fakeChars(parts[i])
		}
		return "admin:" + strings.Join(parts, "$")
	}
	return s.fakeChars(v)
}

func (s *Scrubber) fakeChars(v string) string {
	m := hmac.New(sha256.New, s.salt)
	m.Write([]byte(v))
	seed := m.Sum(nil)
	isHex := hexish.MatchString(v)
	prefix := ""
	if i := strings.IndexAny(v, "_-"); i > 0 && i <= 4 && !isHex {
		prefix, v = v[:i+1], v[i+1:]
	}
	var b strings.Builder
	b.WriteString(prefix)
	for i, c := range v {
		r := seed[i%len(seed)] ^ byte(i*31)
		switch {
		case c >= '0' && c <= '9':
			b.WriteByte('0' + r%10)
		case isHex && c >= 'a' && c <= 'f':
			b.WriteByte("0123456789abcdef"[r%16])
		case c >= 'a' && c <= 'z':
			b.WriteByte('a' + r%26)
		case c >= 'A' && c <= 'Z':
			b.WriteByte('A' + r%26)
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// Tree scrubs every key and string value of a decoded JSON tree. Values under
// secret-looking keys get a format-preserving fake instead.
func (s *Scrubber) Tree(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			nk := s.String(k)
			if SecretLookingKey(k) {
				out[nk] = s.secretTree(x)
				continue
			}
			out[nk] = s.Tree(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = s.Tree(x)
		}
		return out
	case string:
		return s.String(t)
	}
	return v
}

// TreePlain scrubs keys and strings like Tree, without the secret-key rule
// (for capture metadata whose keys are endpoint paths such as /secrets).
func (s *Scrubber) TreePlain(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[s.String(k)] = s.TreePlain(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = s.TreePlain(x)
		}
		return out
	case string:
		return s.String(t)
	}
	return v
}

func (s *Scrubber) secretTree(v any) any {
	switch t := v.(type) {
	case string:
		return s.FakeSecret(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = s.secretTree(x)
		}
		return out
	case map[string]any:
		return s.Tree(t)
	}
	return v
}

// Command scrubs a command line or one argv element: String plus the values
// of secret-looking flags.
func (s *Scrubber) Command(v string) string {
	v = secretFlagRe.ReplaceAllStringFunc(v, func(m string) string {
		sm := secretFlagRe.FindStringSubmatch(m)
		if len(sm[2]) >= 3 {
			s.Real[sm[2]] = "secret"
		}
		return sm[1] + "REDACTED"
	})
	return s.String(v)
}

// Argv scrubs an argv list; a secret-looking flag without "=" redacts the
// next element.
func (s *Scrubber) Argv(v any) any {
	list, ok := v.([]any)
	if !ok {
		return s.Tree(v)
	}
	out := make([]any, len(list))
	redactNext := false
	for i, x := range list {
		str, ok := x.(string)
		if !ok {
			out[i] = x
			continue
		}
		if redactNext {
			if len(str) >= 3 {
				s.Real[str] = "secret"
			}
			out[i], redactNext = "REDACTED", false
			continue
		}
		if strings.HasPrefix(str, "-") && !strings.Contains(str, "=") && secretFlagRe.MatchString(str+" x") {
			redactNext = true
		}
		out[i] = s.Command(str)
	}
	return out
}

// DecodeJSON decodes b keeping numbers exact (json.Number), so re-encoded
// fixtures carry the same integers as the capture (timeNano, sizes, …).
func DecodeJSON(b []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	return dec.Decode(dst)
}

// RealValues returns the collected real values sorted, for the leak list.
func (s *Scrubber) RealValues() map[string]string { return s.Real }
