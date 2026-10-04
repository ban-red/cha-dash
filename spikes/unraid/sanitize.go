package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"dash.cha.sh/spikes/traefik/labkit"
)

// sanitize turns the raw captures (outside the repo) into committed fixtures, with the same
// labkit scrubber and address/domain conventions as the Traefik and Docker fixtures:
//
//   - LAN addresses → 10.0.11.x (last octet kept), Docker bridges → 10.(100+n).x.y, MACs →
//     02:42:0a:00:…, personal domains → *.lab.example / *.family.example, emails → redacted;
//   - every 64-hex value (the server id prefix on every PrefixedID, container, network and
//     endpoint ids, compose hashes) re-hashed with a private salt;
//   - disk serials (from the array's disk ids and the disks read, which keys disks by serial),
//     machine id, system UUID, VM UUIDs and the API key's id → format-keeping fakes; personal-looking share names → neutral names (from a
//     private words file); comments → REDACTED; secret-looking labels → fakes;
//   - container ids and domain choices aligned with testdata/integrations/docker, so the two
//     fixtures join by container id as well as by name.
//
// With overlayDir (a later run with a different key), the key-dependent reads come from that
// run and everything else from rawDir: see fromOverlay.
//
// It writes the list of every real value it saw (plus every env value, the API key included)
// to leakPath, outside the repo, for the leak check. The list is merged with the one already
// there, so values from earlier runs (an earlier API key, say) stay on it.
func sanitize(rawDir, overlayDir, statePath, wordsPath, dockerFixture, outDir, leakPath string, cfg instanceConfig, envPath string, w io.Writer) error {
	if outDir == "" || leakPath == "" || statePath == "" {
		return errors.New("sanitize needs -fixtures, -leak and -state")
	}
	raw, err := loadRaw(rawDir)
	if err != nil {
		return err
	}
	if overlayDir != "" {
		over, err := loadRawFiles(overlayDir)
		if err != nil {
			return err
		}
		for name, doc := range over {
			switch {
			case name == "summary":
				raw["summaryOverlay"] = doc
			case fromOverlay(name):
				raw[name] = doc
			}
		}
	}
	st, err := labkit.LoadState(statePath)
	if err != nil {
		return err
	}
	s := labkit.NewScrubber(st)
	if u, err := url.Parse(cfg.URL); err == nil {
		s.AddLAN(u.Hostname())
	}
	if wordsPath != "" {
		if err := loadWords(s, wordsPath); err != nil {
			return err
		}
	}

	containers := dig(raw["Docker"], "data", "docker", "containers").([]any)

	// Align with the Docker fixture: its domain choices, the personal image handle it
	// replaced, and (after scrubbing) its container ids.
	fixtureIDs, err := alignWithDockerFixture(s, containers, dockerFixture)
	if err != nil {
		return err
	}

	// Identifiers that labkit's patterns don't recognize.
	kinds := map[string]string{} // real value → leak-list kind, applied at the end
	addFake := func(real, kind string) {
		if len(real) < 4 {
			return
		}
		if _, seen := kinds[real]; !seen {
			s.AddWord(real, s.FakeSecret(real))
		}
		kinds[real] = kind
	}
	arr, _ := dig(raw["Array"], "data", "array").(map[string]any)
	for _, d := range arrayDisks(arr) {
		local := localID(str(d["id"]))
		if local == "" {
			continue
		}
		kinds[local] = "disk-id"
		if serial := diskSerial(local); serial != "" {
			addFake(serial, "disk-serial")
		}
	}
	// The disks read keys every disk by its bare serial (id "<server id>:<serial>"). They're the
	// array's serials, so the fakes above already cover them; a disk outside the array (an
	// unassigned device) would bring new ones.
	for _, d := range asList(dig(raw["Disks"], "data", "disks")) {
		addFake(str(dig(d, "serialNum")), "disk-serial")
		addFake(localID(str(dig(d, "id"))), "disk-serial")
	}
	info, _ := dig(raw["Info"], "data", "info").(map[string]any)
	addFake(str(dig(info, "machineId")), "machine-id")
	addFake(str(dig(info, "system", "uuid")), "system-uuid")
	for _, v := range asList(dig(raw["Vms"], "data", "vms", "domains")) {
		addFake(localID(str(dig(v, "id"))), "vm-uuid")
	}
	addFake(localID(str(dig(raw["Me"], "data", "me", "id"))), "api-key-id")

	// Pass 1: learn domains, addresses, MACs, emails and ids from everything we keep.
	for name, doc := range raw {
		if name == "Docker" {
			learnContainers(s, containers)
			continue
		}
		s.LearnTree(doc)
	}
	s.Finish()

	// Pass 2: write the fixtures.
	dir := filepath.Join(outDir, "jupiter")
	if err := os.MkdirAll(filepath.Join(dir, "scope"), 0o755); err != nil {
		return err
	}
	out := map[string]any{} // relative path → sanitized document

	for name, file := range map[string]string{
		"Info": "info.json", "Network": "network.json", "Metrics": "metrics.json", "ParityHistory": "parity-history.json",
		"DockerNetworks": "docker-networks.json", "Vms": "vms.json", "Notifications": "notifications.json", "Me": "me.json",
		"Disks": "disks.json", "RootQueryFields": "schema/root-query.json", "RootMutationFields": "schema/root-mutation.json",
		"RootSubscriptionFields": "schema/root-subscription.json", "IntrospectionQuery": "schema/introspection-refused.json",
	} {
		if doc, ok := raw[name]; ok {
			out[file] = s.Tree(doc)
		}
	}
	if doc, ok := raw["Array"]; ok {
		out["array.json"] = redactComments(s.Tree(doc))
	}
	if doc, ok := raw["Shares"]; ok {
		out["shares.json"] = redactComments(s.Tree(doc))
	}
	if doc, ok := raw["Docker"]; ok {
		d := s.Tree(doc).(map[string]any)
		list := make([]any, 0, len(containers))
		for _, c := range containers {
			list = append(list, scrubContainer(s, c.(map[string]any)))
		}
		d["data"].(map[string]any)["docker"].(map[string]any)["containers"] = list
		out["docker.json"] = d
	}
	// Out-of-scope reads: keep the refusals (errors only). With the first key, two of them
	// succeeded because it was broader than requested; data from any such success (log file
	// paths, the server id) is dropped, and the capture file records the status.
	for _, q := range outOfScope {
		doc, ok := raw[q.Name].(map[string]any)
		if !ok {
			continue
		}
		kept := map[string]any{"errors": doc["errors"]}
		if d, ok := doc["data"]; ok && hasNonNull(d) {
			kept["data"] = "omitted: the read succeeded (the key was broader than requested); see capture.json"
		} else {
			kept["data"] = d
		}
		out["scope/"+strings.TrimPrefix(q.Name, "OutOfScope")+".json"] = s.Tree(kept)
	}
	if doc, ok := raw["subscriptions"]; ok {
		out["subscriptions.json"] = s.Tree(trimEvents(doc, 6))
	}
	if doc, ok := raw["summary"]; ok {
		out["capture.json"] = s.TreePlain(doc)
	}
	if doc, ok := raw["summaryOverlay"]; ok {
		out["capture-consent-key.json"] = s.TreePlain(doc)
	}
	// Spin state around the disks reads: emhttp's view (names, isSpinning, temp; no ids).
	spin := map[string]any{}
	for key, pair := range map[string][2]string{
		"timed_out_read": {"SpinBeforeDisksTimedOut", "SpinAfterDisksTimedOut"},
		"rerun":          {"SpinBeforeDisks", "SpinAfterDisks"},
	} {
		before, okB := raw[pair[0]]
		after, okA := raw[pair[1]]
		if okB && okA {
			spin[key] = map[string]any{"before": before, "after": after}
		}
	}
	if len(spin) > 0 {
		out["disks-spin.json"] = s.Tree(spin)
	}

	// Container ids: swap our fakes for the Docker fixture's, by name. Full ids first, then
	// the 12-character short forms (replacing a prefix first would leave hybrids).
	var swaps [][2]string
	var short [][2]string
	for _, c := range out["docker.json"].(map[string]any)["data"].(map[string]any)["docker"].(map[string]any)["containers"].([]any) {
		m := c.(map[string]any)
		name := strings.TrimPrefix(str(asList(m["names"])[0]), "/")
		if fid, ok := fixtureIDs[name]; ok {
			mine := localID(str(m["id"]))
			swaps = append(swaps, [2]string{mine, fid})
			short = append(short, [2]string{mine[:12], fid[:12]})
		}
	}
	aligned := len(swaps)
	swaps = append(swaps, short...)

	files := make([]string, 0, len(out))
	for f := range out {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := json.MarshalIndent(out[f], "", "  ")
		if err != nil {
			return err
		}
		text := string(b)
		for _, sw := range swaps {
			text = strings.ReplaceAll(text, sw[0], sw[1])
		}
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(text+"\n"), 0o644); err != nil {
			return err
		}
	}

	if err := st.Save(statePath); err != nil {
		return err
	}
	env, err := labkit.LoadEnv(envPath)
	if err != nil {
		return err
	}
	real := s.RealValues()
	for v, k := range kinds {
		real[v] = k
	}
	if err := labkit.WriteLeakList(leakPath, real, env); err != nil {
		return err
	}
	// Label the key and the disk serials explicitly, so the leak test can insist they're listed.
	list, err := labkit.ReadLeakList(leakPath)
	if err != nil {
		return err
	}
	kept := list[:0]
	for _, v := range list {
		if v.V == cfg.APIKey {
			v.Kind = "api-key"
		} else if k, ok := kinds[v.V]; ok {
			v.Kind = k
		}
		if genericConstant(v) {
			continue // identifies nobody, and other fixtures legitimately contain it
		}
		kept = append(kept, v)
	}
	list = kept
	b, _ := json.Marshal(list)
	if err := os.WriteFile(leakPath, b, 0o600); err != nil {
		return err
	}
	count := map[string]int{}
	for _, v := range list {
		count[v.Kind]++
	}
	fmt.Fprintf(w, "wrote %d fixture files; leak list: %d real values %v\n", len(files), len(list), count)
	fmt.Fprintf(w, "container ids aligned with the Docker fixture: %d of %d\n", aligned, len(containers))
	return nil
}

var dockerGateway = regexp.MustCompile(`^172\.(1[6-9]|2[0-9]|3[01])\.[0-9]+\.1$`)

// genericConstant: values that are the same on every machine (the loopback interface's
// all-zero MAC, the broadcast MAC, Docker's default bridge gateways). They aren't leaks, and
// keeping them on the list makes cross-fixture scans report other fixtures' zeroed MACs and
// default gateways.
func genericConstant(v labkit.LeakValue) bool {
	switch v.Kind {
	case "mac":
		l := strings.ToLower(v.V)
		return l == "00:00:00:00:00:00" || l == "ff:ff:ff:ff:ff:ff"
	case "ipv4":
		return dockerGateway.MatchString(v.V)
	}
	return false
}

func loadRaw(dir string) (map[string]any, error) {
	out, err := loadRawFiles(dir)
	if err != nil {
		return nil, err
	}
	if out["Docker"] == nil || out["Array"] == nil {
		return nil, errors.New("raw directory lacks Docker or Array captures")
	}
	return out, nil
}

// loadRawFiles reads every <name>.raw.json in dir, keyed by name.
func loadRawFiles(dir string) (map[string]any, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.raw.json"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no raw captures in %s", filepath.Base(dir))
	}
	out := map[string]any{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var v any
		if err := labkit.DecodeJSON(b, &v); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		out[strings.TrimSuffix(filepath.Base(f), ".raw.json")] = v
	}
	return out, nil
}

// fromOverlay: the reads whose result depends on the key. A later run with another key
// replaces the base run's captures of exactly these. Everything else stays from the base run,
// whose containers were captured alongside the Docker fixture, so the two keep joining.
func fromOverlay(name string) bool {
	return name == "Disks" || name == "Me" || strings.HasPrefix(name, "OutOfScope") || strings.HasPrefix(name, "Spin")
}

// loadWords reads "real<TAB>replacement" lines (private, outside the repo).
func loadWords(s *labkit.Scrubber, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		real, to, ok := strings.Cut(sc.Text(), "\t")
		if ok && len(real) >= 3 {
			s.AddWord(real, to)
		}
	}
	return sc.Err()
}

// alignWithDockerFixture pins the personal domains to the fakes the Docker fixture used,
// adds the personal image handle it replaced, and returns its container ids by name.
func alignWithDockerFixture(s *labkit.Scrubber, containers []any, fixtureDir string) (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(fixtureDir, "containers.json"))
	if err != nil {
		return nil, fmt.Errorf("docker fixture: %w", err)
	}
	var fx []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		return nil, err
	}
	byName := map[string]int{}
	ids := map[string]string{}
	for i, c := range fx {
		n := strings.TrimPrefix(c.Names[0], "/")
		byName[n] = i
		ids[n] = c.ID
	}
	pinned := map[string]string{}
	for _, c := range containers {
		m := c.(map[string]any)
		name := strings.TrimPrefix(str(asList(m["names"])[0]), "/")
		i, ok := byName[name]
		if !ok {
			continue
		}
		f := fx[i]
		labels, _ := m["labels"].(map[string]any)
		for k, v := range labels {
			realHost, fakeHost := urlHost(str(v)), urlHost(f.Labels[k])
			if realHost == "" || fakeHost == "" || realHost == fakeHost {
				continue
			}
			rr, fr := labkit.Registrable(realHost), labkit.Registrable(fakeHost)
			if !strings.HasSuffix(fr, ".example") || rr == fr {
				continue
			}
			if prev, ok := pinned[rr]; ok && prev != fr {
				return nil, fmt.Errorf("docker fixture maps one real domain to two fakes")
			}
			pinned[rr] = fr
		}
		// A personal handle in an image path ("ghcr.io/<handle>/app") became homelab-user.
		rs, fs := strings.Split(str(m["image"]), "/"), strings.Split(f.Image, "/")
		if len(rs) == len(fs) {
			for j := range rs {
				if fs[j] == "homelab-user" && rs[j] != fs[j] {
					s.AddWord(rs[j], "homelab-user")
				}
			}
		}
	}
	for real, fake := range pinned {
		s.PinDomain(real, fake)
		s.RealValues()[real] = "domain"
	}
	return ids, nil
}

func urlHost(v string) string {
	if !strings.Contains(v, "://") {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// imageMetaKey: labels baked into images. Their URLs are public and aren't learned as
// personal domains (emails in them are still rewritten). Same rule as spikes/docker-proxy.
var imageMetaKey = regexp.MustCompile(`^(org\.opencontainers\.|org\.label-schema\.|com\.nvidia\.|maintainer$|authors$|description$|documentation$|homepage$|vendor$|url$|build_version$|build-date$|build_date$|name$|net\.unraid\.docker\.icon$)`)

var anyURL = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S+`)

func learnContainers(s *labkit.Scrubber, containers []any) {
	for _, c := range containers {
		m := c.(map[string]any)
		for k, v := range m {
			switch k {
			case "labels":
				labels, _ := v.(map[string]any)
				for lk, lv := range labels {
					s.LearnString(lk)
					if imageMetaKey.MatchString(lk) {
						s.LearnLeakOnly(anyURL.ReplaceAllString(str(lv), " "))
						continue
					}
					s.LearnTree(lv)
				}
			case "iconUrl", "projectUrl", "registryUrl", "supportUrl":
				s.LearnLeakOnly(anyURL.ReplaceAllString(str(v), " "))
			default:
				s.LearnTree(v)
			}
		}
	}
}

func scrubContainer(s *labkit.Scrubber, m map[string]any) map[string]any {
	out := s.Tree(m).(map[string]any)
	if l, ok := m["labels"].(map[string]any); ok {
		ol := make(map[string]any, len(l))
		for k, v := range l {
			nk := s.String(k)
			switch {
			case labkit.SecretLookingKey(k):
				ol[nk] = s.FakeSecret(str(v))
			case strings.HasSuffix(k, ".username") || strings.HasSuffix(k, ".user"):
				ol[nk] = "admin"
			default:
				ol[nk] = s.Command(str(v))
			}
		}
		out["labels"] = ol
	}
	return out
}

// redactComments replaces every non-empty "comment" value in the tree.
func redactComments(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if k == "comment" {
				if sx, ok := x.(string); ok && sx != "" {
					t[k] = "REDACTED"
				}
				continue
			}
			redactComments(x)
		}
	case []any:
		for _, x := range t {
			redactComments(x)
		}
	}
	return v
}

// trimEvents keeps the first n events per subscription (CPU alone sends ~55 a minute).
func trimEvents(doc any, n int) any {
	m, ok := doc.(map[string]any)
	if !ok {
		return doc
	}
	for _, sub := range asList(m["subs"]) {
		sm, _ := sub.(map[string]any)
		if ev := asList(sm["events"]); len(ev) > n {
			sm["events_kept"] = n
			sm["events"] = ev[:n]
		}
	}
	return m
}

func arrayDisks(arr map[string]any) []map[string]any {
	var out []map[string]any
	for _, k := range []string{"boot", "parities", "disks", "caches", "bootDevices"} {
		switch v := arr[k].(type) {
		case map[string]any:
			out = append(out, v)
		case []any:
			for _, x := range v {
				if d, ok := x.(map[string]any); ok {
					out = append(out, d)
				}
			}
		}
	}
	return out
}

var serverPrefix = regexp.MustCompile(`^[0-9a-f]{64}:`)

// localID strips the "<server id>:" prefix of a PrefixedID. Not every id has one: the boot
// flash's id is "<vendor>_<model>_<serial>-0:0", whose colon belongs to the device.
func localID(id string) string {
	return serverPrefix.ReplaceAllString(id, "")
}

var serialRun = regexp.MustCompile(`[A-Za-z0-9]{6,}`)

// diskSerial returns the serial in an Unraid disk id ("<model with underscores>_<serial>",
// a USB flash adds "-0:0"): the longest alphanumeric run in the last "_" segment.
func diskSerial(local string) string {
	segs := strings.Split(local, "_")
	last := segs[len(segs)-1]
	best := ""
	for _, m := range serialRun.FindAllString(last, -1) {
		if len(m) > len(best) {
			best = m
		}
	}
	return best
}

func dig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func hasNonNull(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return v != nil
	}
	for _, x := range m {
		if x != nil {
			return true
		}
	}
	return false
}
