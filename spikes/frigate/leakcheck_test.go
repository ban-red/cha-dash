package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The leak check scans everything this spike would commit for every real
// value seen during capture. The needle list lives outside the repo:
//
//	LEAKCHECK_NEEDLES=$SCRATCH/real.needles LEAKCHECK_ENV=../../../.env.local go test -run Leak -v
//
// Hits are reported by file, line, class and needle index, never by value.

const canary = "chadash-leak-canary-7f3a91"

type needle struct {
	class string
	value string
	re    *regexp.Regexp
	wordy bool // a word-like value: JSON keys and fake domains don't count
	key   *regexp.Regexp
}

// genericWords are needle values too generic to scan for: they are also
// protocol or product names, or config vocabulary (an MQTT user literally
// named "mqtt", an interface labelled after the VPN product whose device,
// tailscale0, is kept verbatim anyway).
var genericWords = map[string]bool{"mqtt": true, "admin": true, "root": true, "user": true, "frigate": true, "viewer": true, "tailscale": true, "wireguard": true}

func makeNeedle(class, v string) needle {
	n := needle{class: class, value: v}
	q := regexp.QuoteMeta(v)
	switch {
	case class == "ip4":
		n.re = regexp.MustCompile(`(^|[^0-9.])` + q + `($|[^0-9])`)
	case class == "mac":
		hex := strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(v))
		parts := make([]string, 0, 6)
		for i := 0; i < 12; i += 2 {
			parts = append(parts, hex[i:i+2])
		}
		n.re = regexp.MustCompile(`(?i)` + strings.Join(parts, `[:\-]?`))
	case class == "label" || class == "user":
		n.re, n.wordy = regexp.MustCompile(`(?i)\b`+q+`\b`), true
	case class == "camera" || class == "zone" || class == "stream" || class == "mqtt-user":
		// "_" separates here: a real "gate" must hit "gate_sub" too.
		n.re, n.wordy = regexp.MustCompile(`(?i)(^|[^a-z0-9])`+q+`($|[^a-z0-9])`), true
	case strings.HasPrefix(class, "env") && len(v) < 8 && regexp.MustCompile(`^\w+$`).MatchString(v):
		// A short word-like value (a username, say) is only a leak as a
		// whole word, not inside "reduced" or "recorded".
		n.re, n.wordy = regexp.MustCompile(`(?i)\b`+q+`\b`), true
	default:
		n.re = regexp.MustCompile(`(?i)` + q)
	}
	if n.wordy {
		n.key = regexp.MustCompile(`(?i)"` + q + `"\s*:`) // the same word as a JSON key is schema, not data
	}
	return n
}

func loadNeedles(t *testing.T) []needle {
	path := os.Getenv("LEAKCHECK_NEEDLES")
	if path == "" {
		t.Skip("LEAKCHECK_NEEDLES not set; the real-value list lives outside the repo")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []needle
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		class, v, ok := strings.Cut(sc.Text(), "\t")
		if !ok || len(v) < 3 || genericWords[strings.ToLower(v)] {
			continue
		}
		out = append(out, makeNeedle(class, v))
	}
	if p := os.Getenv("LEAKCHECK_ENV"); p != "" {
		env, err := loadEnv(p)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range env {
			if len(v) >= 3 {
				out = append(out, makeNeedle("env:"+k, v))
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("needle list is empty")
	}
	return out
}

type hit struct {
	line  int
	class string
	idx   int
}

// fakeDomain matches the sanitized DNS names the fixtures are required to use
// (*.lab.example). A real interface label that happens to be the same common
// word must not trip on them.
var fakeDomain = regexp.MustCompile(`(?i)[a-z0-9.-]*\.[l]ab\.example\b`) // [l] keeps this line itself from matching

func scan(b []byte, needles []needle) []hit {
	var hits []hit
	for ln, line := range bytes.Split(b, []byte("\n")) {
		stripped := fakeDomain.ReplaceAll(line, []byte("<fake-domain>"))
		for i, n := range needles {
			l := line
			if n.wordy {
				l = n.key.ReplaceAll(stripped, []byte("<key>:"))
			}
			if n.re.Match(l) {
				hits = append(hits, hit{ln + 1, n.class, i})
			}
		}
	}
	return hits
}

func TestLeakCheck(t *testing.T) {
	needles := loadNeedles(t)

	// Sanity: the scanner must find a known test string, and a real value
	// planted in memory, or a clean result means nothing.
	withCanary := append([]needle{makeNeedle("canary", canary)}, needles...)
	if h := scan([]byte(`{"note": "`+canary+`"}`), withCanary); len(h) == 0 || h[0].class != "canary" {
		t.Fatal("sanity check failed: canary not detected")
	}
	planted := 0
	for _, n := range needles {
		if len(scan([]byte(`{"x": "`+n.value+`"}`), []needle{n})) == 1 {
			planted++
		}
	}
	if planted != len(needles) {
		t.Fatalf("sanity check failed: only %d of %d planted needles detected", planted, len(needles))
	}
	t.Logf("sanity ok: canary and all %d planted real values detected", len(needles))

	roots := []string{".", "../../testdata/integrations/frigate"}
	if extra := os.Getenv("LEAKCHECK_EXTRA_ROOTS"); extra != "" {
		roots = append(roots, strings.Split(extra, ":")...)
	}
	files := 0
	var leaks []string
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			t.Logf("skipping missing root %s", filepath.Base(root))
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "raw" || d.Name() == "bin") {
				return filepath.SkipDir
			}
			if d.IsDir() || strings.Contains(d.Name(), ".raw.") || strings.HasSuffix(d.Name(), ".pin") || strings.HasSuffix(d.Name(), ".needles") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files++
			for _, h := range scan(b, needles) {
				leaks = append(leaks, fmt.Sprintf("%s:%d needle #%d (%s)", p, h.line, h.idx, h.class))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files == 0 {
		t.Fatal("scanned no files")
	}
	for _, l := range leaks {
		t.Error("LEAK " + l)
	}
	t.Logf("scanned %d files against %d needles: %d hits", files, len(needles), len(leaks))
}
