package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Fixture layout (per instance, under -out):
//
//	<instance>/_index.json   every request in capture order: sanitized path, file, status,
//	                         reason, latency, decoded and wire size, what was trimmed or dropped
//	<instance>/_tls.json     the TLS facts without fingerprints or dates
//	<instance>/<api path>.json  the response body exactly as PVE shaped it ({"data": …}), sanitized
//
// A replay server maps "GET /api2/json" + index.path to index.file and index.status.

// IndexEntry is one recorded request in _index.json.
type IndexEntry struct {
	Path      string  `json:"path"`
	File      string  `json:"file,omitempty"`
	Status    int     `json:"status"`
	Reason    string  `json:"reason,omitempty"`
	MS        float64 `json:"ms"`
	Bytes     int     `json:"bytes"`
	WireBytes int     `json:"wire_bytes"`
	Encoding  string  `json:"encoding,omitempty"`
	Error     string  `json:"error,omitempty"`
	Trimmed   string  `json:"trimmed,omitempty"`
}

// Index is _index.json.
type Index struct {
	Instance   string         `json:"instance"`
	CapturedAt string         `json:"captured_at"`
	WallMS     float64        `json:"wall_ms"`
	Dropped    map[string]int `json:"dropped_keys"` // key → how many values were removed
	Requests   []IndexEntry   `json:"requests"`
}

// TLSReport is _tls.json: what the TLS UX needs to know, nothing identifying.
type TLSReport struct {
	FirstMode        string `json:"first_mode"`
	Mode             string `json:"mode"`
	Handshakes       int    `json:"handshakes"`
	TLSVersion       string `json:"tls_version"`
	Cipher           string `json:"cipher"`
	ChainLen         int    `json:"chain_len"`
	LeafSelfSigned   bool   `json:"leaf_self_signed"`
	IssuerIsPVECA    bool   `json:"issuer_is_pve_ca"`
	ChainHasCA       bool   `json:"chain_has_ca"`
	SANMatchesHost   bool   `json:"san_matches_host"`
	LeafKey          string `json:"leaf_key"`
	LeafValidDays    int    `json:"leaf_valid_days"`
	LeafDaysLeft     int    `json:"leaf_days_left"`
	ServedCertFile   string `json:"served_cert_file"`              // which certificates/info entry the server presented
	CustomProxyCert  bool   `json:"custom_pveproxy_cert"`          // pveproxy-ssl.pem exists
	NodesFPMatches   *bool  `json:"nodes_ssl_fingerprint_matches"` // /nodes ssl_fingerprint == TLS leaf
	JoinFPMatches    *bool  `json:"join_pve_fp_matches,omitempty"` // /cluster/config/join pve_fp == TLS leaf (clusters)
	RootCAFromAPI    bool   `json:"root_ca_readable_via_api"`      // certificates/info returns pve-root-ca.pem
	RootCAVerifies   *bool  `json:"leaf_verifies_against_root_ca"` // served cert chains to that CA with the URL host
	RootCAValidYears int    `json:"root_ca_valid_years,omitempty"`
}

func sanitizeAll(ins []Instance, rawDir, outDir string) error {
	m, err := buildMapper(ins, rawDir)
	if err != nil {
		return err
	}
	for _, in := range ins {
		if err := sanitizeInstance(m, in, rawDir, outDir); err != nil {
			return fmt.Errorf("%s: %w", in.Name, err)
		}
	}
	printMapSummary(m, os.Stderr)
	return nil
}

func sanitizeInstance(m *Mapper, in Instance, rawDir, outDir string) error {
	m.cur = in
	src := filepath.Join(rawDir, in.Name)
	dst := filepath.Join(outDir, in.Name)
	var meta RawMeta
	if err := readJSON(filepath.Join(src, "_meta.json"), &meta); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	idx := Index{Instance: in.Name, CapturedAt: meta.CapturedAt.Format("2006-01-02T15:04Z"), WallMS: round1(meta.WallMS), Dropped: map[string]int{}}
	for _, r := range meta.Requests {
		e := IndexEntry{Status: r.Status, Reason: m.str(r.Reason), MS: round1(r.LatencyMS), Bytes: r.Bytes, WireBytes: r.WireBytes,
			Encoding: r.Encoding, Error: m.str(r.Err)}
		p, err := url.PathUnescape(r.Path)
		if err != nil {
			p = r.Path
		}
		if q, err := url.QueryUnescape(p); err == nil {
			p = q
		}
		e.Path = m.str(p)
		if r.File != "" && fileExists(filepath.Join(src, r.File)) {
			var segs []string
			for _, s := range strings.Split(r.File, "/") {
				segs = append(segs, m.str(s))
			}
			e.File = strings.Join(segs, "/")
			body, trimmed, err := m.sanitizeBody(r.File, filepath.Join(src, r.File), idx.Dropped)
			if err != nil {
				return fmt.Errorf("%s: %w", e.File, err)
			}
			e.Trimmed = trimmed
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dst, e.File)), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dst, e.File), body, 0o644); err != nil {
				return err
			}
		}
		idx.Requests = append(idx.Requests, e)
	}
	if err := writeJSON(filepath.Join(dst, "_index.json"), idx); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dst, "_tls.json"), tlsReport(m, in, src, meta.TLS))
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// sanitizeBody rewrites one raw response. It keeps PVE's {"data": …} shape.
func (m *Mapper) sanitizeBody(rel, path string, dropped map[string]int) ([]byte, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var v any
	dec := json.NewDecoder(bytesReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		// Not JSON: keep nothing but its length.
		return []byte(fmt.Sprintf("{\"_non_json_bytes\": %d}\n", len(b))), "non-JSON body dropped", nil
	}
	trimmed := ""
	if top, ok := v.(map[string]any); ok {
		switch {
		case strings.HasSuffix(rel, "rrddata.day.json"):
			if a, ok := top["data"].([]any); ok && len(a) > 72 {
				step := (len(a) + 71) / 72
				var keep []any
				for i := 0; i < len(a); i += step {
					keep = append(keep, a[i])
				}
				trimmed = fmt.Sprintf("downsampled %d → %d points (every %dth)", len(a), len(keep), step)
				top["data"] = keep
			}
		case strings.Contains(rel, "metrics/export.history"):
			if d, ok := top["data"].(map[string]any); ok {
				if a, ok := d["data"].([]any); ok {
					times := map[string]bool{}
					var ts []string
					for _, x := range a {
						t := str(x.(map[string]any)["timestamp"])
						if !times[t] {
							times[t] = true
							ts = append(ts, t)
						}
					}
					sort.Strings(ts)
					keepT := map[string]bool{}
					for _, t := range ts[:min(3, len(ts))] {
						keepT[t] = true
					}
					var keep []any
					for _, x := range a {
						if keepT[str(x.(map[string]any)["timestamp"])] {
							keep = append(keep, x)
						}
					}
					trimmed = fmt.Sprintf("kept the first 3 of %d timestamps: %d → %d points", len(ts), len(a), len(keep))
					d["data"] = keep
				}
			}
		}
	}
	out := m.clean(rel, "", v, dropped)
	ob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, "", err
	}
	return append(ob, '\n'), trimmed, nil
}

var (
	dropKeys = map[string]bool{
		"description": true, "comment": true, "args": true, "lxc": true, "pem": true, "keys": true,
		"email": true, "firstname": true, "lastname": true, "sshkeys": true, "cicustom": true,
		"searchdomain": true, "nameserver": true, "hookscript": true,
	}
	reTemplate = regexp.MustCompile(`\{\{[a-z]+\}\}`)
)

// clean applies the key rules, then the string pipeline, recursively.
func (m *Mapper) clean(rel, key string, v any, dropped map[string]int) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, y := range x {
			nv, keep := m.rule(rel, k, y)
			if !keep {
				dropped[k]++
				continue
			}
			if k == "tokens" && strings.HasSuffix(rel, "access/user.json") {
				out[k] = nv
				continue
			}
			out[m.str(k)] = m.clean(rel, k, nv, dropped)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, y := range x {
			out[i] = m.clean(rel, key, y, dropped)
		}
		return out
	case string:
		return m.str(x)
	}
	return v
}

// rule is the per-key policy. It returns the value to keep and whether to keep it.
func (m *Mapper) rule(rel, k string, v any) (any, bool) {
	if dropKeys[k] {
		return nil, false
	}
	s, isStr := v.(string)
	switch k {
	case "notes-template":
		if isStr && m.allowed(reTemplate.ReplaceAllString(s, "")) {
			return s, true
		}
		return "{{guestname}}", true
	case "notes":
		if isStr && m.allowed(s) {
			return s, true
		}
		return nil, false
	case "smbios1":
		if u := reUUID.FindString(s); u != "" {
			return "uuid=" + m.mapPat("uuid", u), true
		}
		return nil, false
	case "cipassword":
		return "**********", true
	case "fingerprint", "ssl_fingerprint", "pve_fp":
		if isStr && s != "" && !reFP.MatchString(s) {
			return m.mapPat("fp", fmt.Sprintf("%x", sha256.Sum256([]byte(s)))[:64]), true
		}
	case "encryption-key":
		if isStr && s != "" && !reFP.MatchString(s) {
			return "1", true
		}
	case "tokens":
		if strings.HasSuffix(rel, "access/user.json") {
			return m.cleanTokens(rel, v), true
		}
	}
	return v, true
}

// cleanTokens renames the token map in /access/users/{user}: keys are token
// names. Only privsep and expire survive.
func (m *Mapper) cleanTokens(rel string, v any) any {
	toks, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for name, t := range toks {
		fake := m.user(m.cur.User() + "!" + name)
		_, ft, _ := strings.Cut(fake, "!")
		keep := map[string]any{}
		if tm, ok := t.(map[string]any); ok {
			for _, f := range []string{"privsep", "expire"} {
				if x, ok := tm[f]; ok {
					keep[f] = x
				}
			}
		}
		out[ft] = keep
	}
	return out
}

func tlsReport(m *Mapper, in Instance, src string, f TLSFacts) TLSReport {
	r := TLSReport{FirstMode: f.FirstMode, Mode: f.Mode, Handshakes: f.Handshakes, TLSVersion: f.TLSVersion, Cipher: f.Cipher,
		ChainLen: f.ChainLen, LeafSelfSigned: f.LeafSelfSigned, IssuerIsPVECA: f.IssuerIsPVECA, ChainHasCA: f.ChainHasCA,
		SANMatchesHost: f.SANMatchesHost, LeafKey: f.LeafKey, LeafValidDays: f.LeafValidDays, LeafDaysLeft: f.LeafDaysLeft}
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, ":", "")) }
	leaf := norm(f.LeafSHA256)

	// /nodes: ssl_fingerprint of the node we talk to (the "local" one in /cluster/status).
	local := ""
	for _, e := range list(loadData(filepath.Join(src, "cluster/status.json"))) {
		if e["type"] == "node" && num(e["local"]) == 1 {
			local = str(e["name"])
		}
	}
	for _, e := range list(loadData(filepath.Join(src, "nodes.json"))) {
		if str(e["node"]) == local {
			b := norm(str(e["ssl_fingerprint"])) == leaf
			r.NodesFPMatches = &b
		}
	}
	if d, ok := loadData(filepath.Join(src, "cluster/config/join.json")).(map[string]any); ok {
		for _, n := range list(d["nodelist"]) {
			if str(n["name"]) == local {
				b := norm(str(n["pve_fp"])) == leaf
				r.JoinFPMatches = &b
			}
		}
	}
	// certificates/info: which file is served, and does it chain to the root CA?
	var ca, served *x509.Certificate
	for _, e := range list(loadData(filepath.Join(src, "nodes", local, "certificates/info.json"))) {
		name := str(e["filename"])
		if name == "pveproxy-ssl.pem" {
			r.CustomProxyCert = true
		}
		if norm(str(e["fingerprint"])) == leaf {
			r.ServedCertFile = name
		}
		blk, _ := pem.Decode([]byte(str(e["pem"])))
		if blk == nil {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		if name == "pve-root-ca.pem" {
			r.RootCAFromAPI = true
			ca = c
			r.RootCAValidYears = int(c.NotAfter.Sub(c.NotBefore).Hours() / 24 / 365)
		}
		if hex.EncodeToString(sha256Sum(c.Raw)) == leaf {
			served = c
		}
	}
	if ca != nil && served != nil {
		roots := x509.NewCertPool()
		roots.AddCert(ca)
		_, err := served.Verify(x509.VerifyOptions{DNSName: in.URL.Hostname(), Roots: roots})
		b := err == nil
		r.RootCAVerifies = &b
	}
	return r
}

func sha256Sum(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

// printMapSummary prints what was mapped, by class, without real values.
func printMapSummary(m *Mapper, w *os.File) {
	byClass := map[string]int{}
	for _, r := range m.reps {
		byClass[r.class]++
	}
	fmt.Fprintf(w, "sanitized: replacements by class: ")
	for _, c := range sortedKeys(byClass) {
		fmt.Fprintf(w, "%s=%d ", c, byClass[c])
	}
	fmt.Fprintf(w, "· subnets=%d · needles=%d\n", len(m.subnet), len(m.Needles()))
}
