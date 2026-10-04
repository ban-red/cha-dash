package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"dash.cha.sh/spikes/traefik/labkit"
)

// Label keys whose values come from the image (OCI metadata and friends).
// URLs in them are public and are not learned as personal domains.
var imageMetaKey = regexp.MustCompile(`^(org\.opencontainers\.|org\.label-schema\.|com\.nvidia\.|tachidesk\.|net\.m1k1o\.|maintainer$|authors$|description$|Description$|documentation$|homepage$|vendor$|url$|download_url$|DISCORD_URL$|build_version$|build-date$|build_date$|name$|CW-base-version$|net\.unraid\.docker\.icon$)`)

type rawEndpoint struct {
	name       string
	meta       map[string]any
	version    any
	info       any
	containers []any
	inspect    []any
	pastEv     []any
	liveEv     []any
	probe      any
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
		if u, err := url.Parse(env["DOCKER_JUPITER_URL"]); err == nil {
			s.AddLAN(u.Hostname())
		}
	}

	dirs, err := os.ReadDir(rawDir)
	if err != nil {
		return err
	}
	var eps []*rawEndpoint
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		ep, err := loadEndpoint(filepath.Join(rawDir, d.Name()))
		if err != nil {
			return fmt.Errorf("%s: %w", d.Name(), err)
		}
		ep.name = d.Name()
		eps = append(eps, ep)
	}

	// Pass 1: learn ids, domains, addresses; remember env secrets for the leak list.
	for _, ep := range eps {
		for _, doc := range append(append([]any{ep.version, ep.info, ep.probe}, ep.containers...), ep.inspect...) {
			learnDoc(s, doc)
		}
		for _, ev := range append(ep.pastEv, ep.liveEv...) {
			learnDoc(s, ev)
		}
	}
	s.Finish()

	// Pass 2: write fixtures.
	for _, ep := range eps {
		dir := filepath.Join(outDir, ep.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if ep.meta != nil {
			ep.meta["eventStats"] = eventStats(ep.pastEv, ep.liveEv)
		}
		meta := s.TreePlain(ep.meta)
		if err := writeFixture(filepath.Join(dir, "capture.json"), meta); err != nil {
			return err
		}
		if ep.version == nil {
			continue // unreachable endpoint: only capture.json
		}
		if err := os.MkdirAll(filepath.Join(dir, "inspect"), 0o755); err != nil {
			return err
		}
		writeFixture(filepath.Join(dir, "version.json"), s.Tree(ep.version))
		if ep.info != nil {
			writeFixture(filepath.Join(dir, "info.json"), s.Tree(ep.info))
		}
		var list []any
		for _, c := range ep.containers {
			list = append(list, scrubContainer(s, c))
		}
		writeFixture(filepath.Join(dir, "containers.json"), list)
		names := map[string]bool{}
		for _, c := range ep.inspect {
			sc := scrubContainer(s, c).(map[string]any)
			name := strings.TrimPrefix(fmt.Sprint(sc["Name"]), "/")
			if name == "" || names[name] {
				name = fmt.Sprint(sc["Id"])[:12]
			}
			names[name] = true
			writeFixture(filepath.Join(dir, "inspect", safeFile(name)+".json"), sc)
		}
		// The engine's buffer is ~250 healthcheck exec events; keep the newest
		// 40 (capture.json has the full count).
		past := ep.pastEv
		if len(past) > 40 {
			past = past[len(past)-40:]
		}
		writeFixture(filepath.Join(dir, "events-past.json"), scrubEvents(s, past))
		writeFixture(filepath.Join(dir, "events-live.json"), scrubEvents(s, ep.liveEv))
		writeFixture(filepath.Join(dir, "probe.json"), s.TreePlain(ep.probe))
	}
	if err := st.Save(statePath); err != nil {
		return err
	}
	if leakPath != "" {
		if err := labkit.WriteLeakList(leakPath, s.RealValues(), env); err != nil {
			return err
		}
	}
	logf("sanitized %d endpoints; %d personal domains mapped, %d real values on the leak list", len(eps), len(st.Domains), len(s.RealValues()))
	logf("hygiene: %d secret-looking env values are a short lowercase word (default-style passwords)", weakSecrets)
	return nil
}

func loadEndpoint(dir string) (*rawEndpoint, error) {
	ep := &rawEndpoint{}
	read := func(name string, dst any) error {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return labkit.DecodeJSON(b, dst)
	}
	if err := read("meta.json", &ep.meta); err != nil {
		return nil, err
	}
	for name, dst := range map[string]any{"version.json": &ep.version, "info.json": &ep.info, "containers.json": &ep.containers,
		"events-past.json": &ep.pastEv, "events-live.json": &ep.liveEv, "probe.json": &ep.probe} {
		if err := read(name, dst); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "inspect", "*.json"))
	sort.Strings(files)
	for _, f := range files {
		var v any
		if err := read(filepath.Join("inspect", filepath.Base(f)), &v); err != nil {
			return nil, err
		}
		ep.inspect = append(ep.inspect, v)
	}
	return ep, nil
}

// learnDoc runs the scrubber's pass 1 over a document, but treats env values
// and image-metadata labels specially.
func learnDoc(s *labkit.Scrubber, doc any) {
	var walk func(key string, v any)
	walk = func(key string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				if key == "Labels" || key == "Attributes" {
					s.LearnString(k)
					if str, ok := x.(string); ok && imageMetaKey.MatchString(k) {
						learnPublic(s, str)
						continue
					}
				}
				walk(k, x)
			}
		case []any:
			for _, x := range t {
				if key == "Env" {
					if str, ok := x.(string); ok {
						learnEnv(s, str)
						continue
					}
				}
				walk(key, x)
			}
		case string:
			s.LearnString(t)
		}
	}
	walk("", doc)
}

// learnPublic: image metadata (OCI labels, maintainer) is public. Its URLs
// and email domains are not learned as personal; emails are still rewritten
// in the output and join the leak list.
func learnPublic(s *labkit.Scrubber, v string) {
	s.LearnLeakOnly(urlRe.ReplaceAllString(v, " "))
}

var urlRe = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+`)

// learnEnv: env values are all redacted in the fixtures, but secret ones join
// the leak list (they must not show up anywhere else, e.g. in a command line),
// and their URL hosts, addresses and emails are learned.
func learnEnv(s *labkit.Scrubber, kv string) {
	k, v, _ := strings.Cut(kv, "=")
	if v == "" {
		return
	}
	if labkit.SecretLookingKey(k) && len(v) >= 4 {
		if weakWord.MatchString(v) {
			// A short lowercase word (often the project name, a compose
			// default). It also names containers and paths, so it can't be a
			// leak-list entry; counted as a hygiene finding instead.
			weakSecrets++
		} else {
			s.RealValues()[v] = "env-secret"
		}
	}
	s.LearnLeakOnly(v)
}

var (
	weakWord    = regexp.MustCompile(`^[a-z]{1,8}$`)
	weakSecrets int
)

func scrubContainer(s *labkit.Scrubber, c any) any {
	m, ok := c.(map[string]any)
	if !ok {
		return s.Tree(c)
	}
	out := s.Tree(m).(map[string]any)
	// Labels: list shape has "Labels" at the top, inspect under "Config".
	if l, ok := m["Labels"].(map[string]any); ok {
		out["Labels"] = scrubLabels(s, l)
	}
	if cmd, ok := m["Command"].(string); ok {
		out["Command"] = s.Command(cmd)
	}
	if args, ok := m["Args"]; ok {
		out["Args"] = s.Argv(args)
	}
	if cfg, ok := m["Config"].(map[string]any); ok {
		oc := out["Config"].(map[string]any)
		if l, ok := cfg["Labels"].(map[string]any); ok {
			oc["Labels"] = scrubLabels(s, l)
		}
		if env, ok := cfg["Env"].([]any); ok {
			oc["Env"] = redactEnv(env)
		}
		for _, k := range []string{"Cmd", "Entrypoint"} {
			if v, ok := cfg[k]; ok && v != nil {
				oc[k] = s.Argv(v)
			}
		}
		if hc, ok := cfg["Healthcheck"].(map[string]any); ok {
			ohc := oc["Healthcheck"].(map[string]any)
			if t, ok := hc["Test"]; ok {
				ohc["Test"] = s.Argv(t)
			}
		}
	}
	if stt, ok := out["State"].(map[string]any); ok {
		if h, ok := stt["Health"].(map[string]any); ok {
			if logs, ok := h["Log"].([]any); ok {
				for _, e := range logs {
					if em, ok := e.(map[string]any); ok {
						if o, _ := em["Output"].(string); o != "" {
							em["Output"] = "REDACTED"
						}
					}
				}
			}
		}
	}
	return out
}

// redactEnv keeps every key and drops every value.
func redactEnv(env []any) []any {
	out := make([]any, 0, len(env))
	for _, e := range env {
		str, _ := e.(string)
		k, _, _ := strings.Cut(str, "=")
		out = append(out, k+"=REDACTED")
	}
	return out
}

func scrubLabels(s *labkit.Scrubber, l map[string]any) map[string]any {
	out := make(map[string]any, len(l))
	for k, v := range l {
		str, _ := v.(string)
		nk := s.String(k)
		switch {
		case labkit.SecretLookingKey(k):
			out[nk] = s.FakeSecret(str)
		case strings.HasSuffix(k, ".username") || strings.HasSuffix(k, ".user"):
			out[nk] = "admin"
		default:
			out[nk] = s.Command(str)
		}
	}
	return out
}

func scrubEvents(s *labkit.Scrubber, evs []any) []any {
	out := make([]any, 0, len(evs))
	for _, e := range evs {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		om := s.Tree(m).(map[string]any)
		for _, k := range []string{"Action", "status"} {
			if a, ok := m[k].(string); ok {
				if verb, _, found := strings.Cut(a, ": "); found {
					om[k] = verb + ": REDACTED" // exec_create: <command line>
				}
			}
		}
		if actor, ok := m["Actor"].(map[string]any); ok {
			if attrs, ok := actor["Attributes"].(map[string]any); ok {
				om["Actor"].(map[string]any)["Attributes"] = scrubLabels(s, attrs)
			}
		}
		out = append(out, om)
	}
	return out
}

// eventStats summarizes both event windows (types, action verbs, time span)
// before the past window is trimmed for the fixture.
func eventStats(past, live []any) map[string]any {
	sum := func(evs []any) map[string]any {
		verbs := map[string]int{}
		var first, last float64
		for _, e := range evs {
			m, _ := e.(map[string]any)
			a, _ := m["Action"].(string)
			verb, _, _ := strings.Cut(a, ":")
			verbs[fmt.Sprint(m["Type"])+"/"+verb]++
			if n, ok := m["timeNano"].(json.Number); ok {
				t, _ := n.Float64()
				if first == 0 || t < first {
					first = t
				}
				if t > last {
					last = t
				}
			}
		}
		out := map[string]any{"count": len(evs), "byTypeAction": verbs}
		if first > 0 {
			out["spanSeconds"] = int((last - first) / 1e9)
		}
		return out
	}
	return map[string]any{"past": sum(past), "live": sum(live)}
}

var unsafeFile = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeFile(name string) string { return unsafeFile.ReplaceAllString(name, "_") }

func writeFixture(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
