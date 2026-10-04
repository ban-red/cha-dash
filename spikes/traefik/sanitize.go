package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dash.cha.sh/spikes/traefik/labkit"
)

var rawFiles = []string{
	"version.json", "overview.json", "entrypoints.json",
	"http-routers.json", "http-services.json", "http-middlewares.json",
	"tcp-routers.json", "tcp-services.json", "tcp-middlewares.json",
	"udp-routers.json", "udp-services.json",
}

func sanitize(rawDir, statePath, outDir, leakPath, envPath string) error {
	if outDir == "" {
		return errors.New("--out is required")
	}
	st, err := labkit.LoadState(statePath)
	if err != nil {
		return err
	}
	s := labkit.NewScrubber(st)
	var env labkit.Env
	if envPath != "" {
		if env, err = labkit.LoadEnv(envPath); err != nil {
			return err
		}
		seedLAN(s, env)
	}

	docs := map[string]any{}
	for _, f := range rawFiles {
		b, err := os.ReadFile(filepath.Join(rawDir, f))
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		var v any
		if err := labkit.DecodeJSON(b, &v); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		docs[f] = v
	}

	// Personal domains in order of use, so the busiest becomes lab.example and
	// the next family.example (the same split as the Homepage fixture).
	count := map[string]int{}
	first := map[string]string{}
	for _, f := range []string{"http-routers.json", "tcp-routers.json"} {
		list, _ := docs[f].([]any)
		for _, r := range list {
			rule, _ := r.(map[string]any)["rule"].(string)
			for _, h := range labkit.RuleHosts(rule) {
				if strings.ContainsAny(h, "^$()|\\") {
					continue // HostRegexp patterns are learned below
				}
				reg := labkit.Registrable(h)
				count[reg]++
				if _, ok := first[reg]; !ok {
					first[reg] = h
				}
			}
		}
	}
	regs := make([]string, 0, len(count))
	for r := range count {
		regs = append(regs, r)
	}
	sort.Slice(regs, func(i, j int) bool {
		if count[regs[i]] != count[regs[j]] {
			return count[regs[i]] > count[regs[j]]
		}
		return regs[i] < regs[j]
	})
	for _, r := range regs {
		s.AddDomain(first[r])
	}
	if env != nil {
		seedTraefikHost(s, env)
	}
	for _, f := range rawFiles {
		if d, ok := docs[f]; ok {
			s.LearnTree(d)
		}
	}
	s.Finish()

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for _, f := range rawFiles {
		d, ok := docs[f]
		if !ok {
			continue
		}
		if err := writeFixture(filepath.Join(outDir, f), s.Tree(d)); err != nil {
			return err
		}
	}
	// The capture metadata has no identifying values; keep it as capture.json.
	if b, err := os.ReadFile(filepath.Join(rawDir, "meta.json")); err == nil {
		var v any
		if labkit.DecodeJSON(b, &v) == nil {
			if err := writeFixture(filepath.Join(outDir, "capture.json"), s.TreePlain(v)); err != nil {
				return err
			}
		}
	}
	if err := st.Save(statePath); err != nil {
		return err
	}
	if leakPath != "" {
		if err := labkit.WriteLeakList(leakPath, s.RealValues(), env); err != nil {
			return err
		}
	}
	logf("sanitized %d files into the fixture directory; %d personal domains mapped, %d real values on the leak list",
		len(docs), len(st.Domains), len(s.RealValues()))
	return nil
}

// seedLAN marks the Unraid host's /24 as the home LAN (→ 10.0.11.x, so
// jupiter stays 10.0.11.120 as in the Homepage fixture).
func seedLAN(s *labkit.Scrubber, env labkit.Env) {
	if u, err := url.Parse(env["DOCKER_JUPITER_URL"]); err == nil {
		h := u.Hostname()
		if strings.Count(h, ".") == 3 && strings.Trim(h, "0123456789.") == "" {
			s.AddLAN(h)
		}
	}
}

// seedTraefikHost marks the dashboard's own domain as personal.
func seedTraefikHost(s *labkit.Scrubber, env labkit.Env) {
	if u, err := url.Parse(env["TRAEFIK_URL"]); err == nil && strings.Contains(u.Hostname(), ".") {
		s.AddDomain(u.Hostname())
	}
}

func writeFixture(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
