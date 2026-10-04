package labkit

import (
	"bufio"
	"encoding/json"
	"errors"
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

// LeakValue is one real value the fixtures must never contain. The list is
// written outside the repo and read by the leak-check tests.
type LeakValue struct {
	V    string `json:"v"`
	Kind string `json:"kind"`
}

// Allowed host nicknames: these may appear in fixtures (see the dogfood README).
var Nicknames = map[string]bool{
	"jupiter": true, "pvedocker": true, "pluto": true, "minig5": true, "mars": true, "mars-1": true, "mars-2": true,
	"io": true, "jupiter-nas": true, "jupiternas": true, "mini-titan": true, "dockerproxy": true, "unraid": true,
}

var generic = map[string]bool{"admin": true, "root": true, "user": true, "true": true, "false": true, "http": true, "https": true, "tcp": true}

// WriteLeakList merges vals (value → kind) into the JSON list at path (mode 600).
// Env values are split into their URL parts too, so a host or password inside
// a URL is caught on its own.
func WriteLeakList(path string, vals map[string]string, env Env) error {
	all := map[string]string{}
	if old, err := ReadLeakList(path); err == nil {
		for _, v := range old {
			all[v.V] = v.Kind
		}
	}
	for v, k := range vals {
		all[v] = k
	}
	for _, v := range env.Values() {
		all[v] = "env"
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if u, err := url.Parse(part); err == nil && u.Host != "" {
				all[u.Host] = "env-host"
				if h := u.Hostname(); h != "" {
					all[h] = "env-host"
				}
				if p, ok := u.User.Password(); ok {
					all[p] = "env-secret"
				}
			}
		}
	}
	var list []LeakValue
	for v, k := range all {
		lv := strings.ToLower(v)
		if len(v) < 4 || generic[lv] || Nicknames[lv] {
			continue
		}
		if a, err := netip.ParseAddr(v); err == nil && keepAddr(a) {
			continue
		}
		list = append(list, LeakValue{V: v, Kind: k})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Kind+list[i].V < list[j].Kind+list[j].V })
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// ReadLeakList reads a list written by WriteLeakList.
func ReadLeakList(path string) ([]LeakValue, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []LeakValue
	return list, json.Unmarshal(b, &list)
}

// Finding locates a leaked value without repeating it.
type Finding struct {
	File  string
	Line  int
	Index int // index into the leak list
	Kind  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: real value #%d (%s) found", f.File, f.Line, f.Index, f.Kind)
}

// bounded kinds must not be part of a longer token (10.0.0.1 vs 10.0.0.12).
var bounded = map[string]bool{"ipv4": true, "ipv6": true, "id12": true, "mac": true, "env-host": true}

func isTokenChar(b byte) bool {
	return b == '.' || b == '-' || b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// ScanDir reports every occurrence of a leak-list value in the files under
// dir. Matching is case-insensitive.
func ScanDir(dir string, vals []LeakValue) ([]Finding, error) {
	low := make([]string, len(vals))
	for i, v := range vals {
		low[i] = strings.ToLower(v.V)
	}
	var out []Finding
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		line := 0
		for sc.Scan() {
			line++
			text := strings.ToLower(sc.Text())
			for i, v := range low {
				if v == "" {
					continue
				}
				for off := 0; ; {
					j := strings.Index(text[off:], v)
					if j < 0 {
						break
					}
					j += off
					end := j + len(v)
					ok := true
					if bounded[vals[i].Kind] {
						if j > 0 && isTokenChar(text[j-1]) && !(text[j-1] == '.' && vals[i].Kind == "env-host") {
							ok = false
						}
						if end < len(text) && isTokenChar(text[end]) && text[end] != '.' && text[end] != '-' {
							ok = false
						}
						if vals[i].Kind == "ipv4" && end < len(text) && (text[end] == '.' && end+1 < len(text) && text[end+1] >= '0' && text[end+1] <= '9') {
							ok = false
						}
					}
					if ok {
						out = append(out, Finding{File: path, Line: line, Index: i, Kind: vals[i].Kind})
						break
					}
					off = j + 1
				}
			}
		}
		return sc.Err()
	})
	return out, err
}

// Canary is a known test string the leak check must always find.
const Canary = "LEAKCHECK-CANARY-q7Zp2"

// SelfTest proves the scanner works: it plants Canary (and a bounded IPv4)
// in a temp dir and requires both to be found, and a near-miss not to be.
func SelfTest() error {
	dir, err := os.MkdirTemp("", "leakcheck")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	body := "{\n  \"a\": \"prefix-" + strings.ToLower(Canary) + "-suffix\",\n  \"b\": \"http://192.0.2.77:8080/\",\n  \"c\": \"192.0.2.7\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte(body), 0o600); err != nil {
		return err
	}
	vals := []LeakValue{{V: Canary, Kind: "canary"}, {V: "192.0.2.77", Kind: "ipv4"}, {V: "192.0.2.70", Kind: "ipv4"}}
	f, err := ScanDir(dir, vals)
	if err != nil {
		return err
	}
	found := map[int]bool{}
	for _, x := range f {
		found[x.Index] = true
	}
	if !found[0] || !found[1] || found[2] {
		return errors.New("leak check self-test failed: the scanner missed the canary or matched a near-miss")
	}
	return nil
}

// Sanitized-fixture invariants that need no private list (safe for CI).
var (
	ipv4Any = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	macAny  = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?::[0-9a-f]{2}){5}\b`)
)

// FakeAddr reports whether a is one of the sanitizer's fake or neutral ranges.
func FakeAddr(a netip.Addr) bool {
	if keepAddr(a) {
		return true
	}
	for _, p := range []string{"10.0.0.0/8", "203.0.113.0/24", "198.51.100.0/24", "192.0.2.0/24"} {
		if netip.MustParsePrefix(p).Contains(a) {
			return true
		}
	}
	return false
}

// CheckInvariants scans the JSON fixtures under dir for addresses outside the
// fake ranges, MACs other than the fake 02:42:0a:00:… ones, and Host() rules
// outside the fake or kept public domains. (READMEs describe the rules with
// example ranges, so only .json files are checked; ScanDir covers every file.)
func CheckInvariants(dir string) ([]string, error) {
	var problems []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for n, line := range strings.Split(string(b), "\n") {
			for _, m := range ipv4Any.FindAllStringIndex(line, -1) {
				s := line[m[0]:m[1]]
				// Skip dotted version strings like 1.2.3.4 in "v1.2.3.4" or "1.2.3.4-ls5".
				if m[0] > 0 && (line[m[0]-1] == 'v' || line[m[0]-1] == '-') || m[1] < len(line) && line[m[1]] == '-' {
					continue
				}
				if a, err := netip.ParseAddr(s); err == nil && !FakeAddr(a) {
					problems = append(problems, fmt.Sprintf("%s:%d: address outside the fake ranges", path, n+1))
				}
			}
			for _, m := range macAny.FindAllString(line, -1) {
				if !strings.HasPrefix(strings.ToLower(m), "02:42:0a:00:") {
					problems = append(problems, fmt.Sprintf("%s:%d: MAC address that is not a fake", path, n+1))
				}
			}
			for _, m := range hostRuleRe.FindAllStringSubmatch(line, -1) {
				for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
					h := strings.ToLower(strings.ReplaceAll(q[1], `\.`, "."))
					for _, f := range fqdnRe.FindAllString(h, -1) {
						if reg := Registrable(f); !isSafeDomain(reg) && !slices.Contains(DefaultKeep, reg) {
							problems = append(problems, fmt.Sprintf("%s:%d: Host() rule outside the fake domains", path, n+1))
						}
					}
				}
			}
		}
		return nil
	})
	return problems, err
}
