package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The leak check scans files for every real value the Mapper collected. The
// values live only in memory; findings print the file, line, class and needle
// number, never the value.

// leakCanary is a known test string: the check must find it in a scratch file
// before its "no leaks" result means anything.
var leakCanary = "chadash-leak-" + "canary-41-do-not-commit" // split so this source file never contains it

type Finding struct {
	File  string
	Line  int
	Class string
	Idx   int
}

type Scanner struct {
	needles []Needle
	lower   []string
}

func newScanner(ns []Needle) *Scanner {
	s := &Scanner{needles: ns}
	for _, n := range ns {
		s.lower = append(s.lower, strings.ToLower(n.Value))
	}
	return s
}

// scanText returns (needle index, line) for every hit.
func (s *Scanner) scanText(text string) [][2]int {
	lo := strings.ToLower(text)
	var hits [][2]int
	for i, ln := range s.lower {
		if ln == "" {
			continue
		}
		for pos := 0; ; {
			j := findFold(lo, ln, pos, s.needles[i].Bounded)
			if j < 0 {
				break
			}
			hits = append(hits, [2]int{i, 1 + strings.Count(lo[:j], "\n")})
			pos = j + 1
		}
	}
	return hits
}

// scanPaths walks roots and scans every text file, skipping the given dirs.
func (s *Scanner) scanPaths(roots []string, skip []string) ([]Finding, int, error) {
	var out []Finding
	files := 0
	skipAbs := map[string]bool{}
	for _, p := range skip {
		if a, err := filepath.Abs(p); err == nil {
			skipAbs[a] = true
		}
	}
	seen := map[string]bool{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			a, _ := filepath.Abs(p)
			if d.IsDir() {
				if skipAbs[a] || d.Name() == ".git" || d.Name() == "bin" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if seen[a] || d.Name() == "tofu.json" {
				return nil
			}
			seen[a] = true
			b, err := os.ReadFile(p)
			if err != nil || len(b) > 16<<20 || bytes.IndexByte(b, 0) >= 0 {
				return nil
			}
			files++
			for _, h := range s.scanText(string(b)) {
				out = append(out, Finding{File: p, Line: h[1], Class: s.needles[h[0]].Class, Idx: h[0]})
			}
			return nil
		})
		if err != nil {
			return nil, files, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, files, nil
}

// selfTest proves the scanner works before trusting a clean result: the
// canary must be found in a scratch file, and every needle must be found when
// embedded in text (in memory only).
func selfTest(s *Scanner) error {
	dir, err := os.MkdirTemp("", "pve-leak-selftest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "planted.json"), []byte("{\n  \"note\": \""+leakCanary+"\"\n}\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "clean.json"), []byte("{\"node\": \"pluto\", \"ip\": \"10.0.11.10\"}\n"), 0o600); err != nil {
		return err
	}
	found, _, err := s.scanPaths([]string{dir}, nil)
	if err != nil {
		return err
	}
	if len(found) != 1 || found[0].Class != "canary" || filepath.Base(found[0].File) != "planted.json" || found[0].Line != 2 {
		return fmt.Errorf("self-test: planted canary not found exactly once (got %d findings)", len(found))
	}
	for i, n := range s.needles {
		hits := s.scanText("x " + n.Value + " x")
		ok := false
		for _, h := range hits {
			if h[0] == i {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("self-test: needle #%d (%s) not detectable", i, n.Class)
		}
	}
	return nil
}

func leakcheckCmd(ins []Instance, rawDir string, roots []string) (int, error) {
	m, err := buildMapper(ins, rawDir)
	if err != nil {
		return 0, err
	}
	// Sanitizing assigns fakes to anything collect() didn't see; do it in memory
	// so the needle list is complete.
	for _, in := range ins {
		files, _ := rawFiles(rawDir, in.Name)
		m.cur = in
		for _, f := range files {
			rel, _ := filepath.Rel(filepath.Join(rawDir, in.Name), f)
			if _, _, err := m.sanitizeBody(filepath.ToSlash(rel), f, map[string]int{}); err != nil {
				return 0, err
			}
		}
	}
	needles := append(m.Needles(), Needle{Value: leakCanary, Class: "canary"})
	s := newScanner(needles)
	if err := selfTest(s); err != nil {
		return 0, err
	}
	byClass := map[string]int{}
	for _, n := range needles {
		byClass[n.Class]++
	}
	fmt.Printf("leakcheck: %d real values in memory by class: ", len(needles)-1)
	for _, c := range sortedKeys(byClass) {
		if c != "canary" {
			fmt.Printf("%s=%d ", c, byClass[c])
		}
	}
	fmt.Println()
	fmt.Println("leakcheck: self-test passed (planted canary found in a scratch file; every value detectable in memory)")
	found, files, err := s.scanPaths(roots, []string{rawDir})
	if err != nil {
		return 0, err
	}
	fmt.Printf("leakcheck: scanned %d files under %s\n", files, strings.Join(roots, ", "))
	for _, f := range found {
		fmt.Printf("  LEAK %s:%d  class=%s needle=#%d\n", f.File, f.Line, f.Class, f.Idx)
	}
	if len(found) == 0 {
		fmt.Println("leakcheck: no real values found")
	}
	return len(found), nil
}
