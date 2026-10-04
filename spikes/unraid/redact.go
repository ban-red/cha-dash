package main

import (
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Redactor scrubs secrets from everything the spike prints: the API key, the server's URL,
// host name and IP, plus anything that merely looks like an address, MAC or key.
// Raw captures on disk are not passed through it; they live in a git-ignored directory
// and are sanitized separately.
type Redactor struct {
	mu      sync.RWMutex
	secrets []secret // longest first, so a URL is replaced before the host inside it
}

type secret struct {
	value string // matched case-insensitively
	label string
}

// minSecretLen keeps a stray one- or two-character value from redacting half the output.
const minSecretLen = 4

var (
	reAPIKeyParam = regexp.MustCompile(`(?i)(api_key=)[^&\s"'<>]+`)
	reKeyHeader   = regexp.MustCompile(`(?i)(x-api-key"?\s*[:=]\s*"?)[^\s",}]+`)
	reIPv4        = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	reMAC         = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}\b`)
	reHex64       = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`) // Unraid keys are 32 random bytes, hex
)

func NewRedactor() *Redactor { return &Redactor{} }

// Add registers a literal secret. Empty and very short values are ignored.
func (r *Redactor) Add(value, label string) {
	value = strings.TrimSpace(value)
	if len(value) < minSecretLen {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.secrets {
		if strings.EqualFold(s.value, value) {
			return
		}
	}
	r.secrets = append(r.secrets, secret{value: value, label: label})
	sort.SliceStable(r.secrets, func(i, j int) bool { return len(r.secrets[i].value) > len(r.secrets[j].value) })
}

// AddURL registers a base URL and every piece of it that identifies the server:
// the full URL, scheme://host[:port], host[:port], the bare host name or IP, and the
// matching ws:// or wss:// URL used for subscriptions.
func (r *Redactor) AddURL(raw string) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return
	}
	r.Add(raw, "<unraid-url>")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return
	}
	r.Add(u.Scheme+"://"+u.Host, "<unraid-url>")
	ws := "ws"
	if u.Scheme == "https" {
		ws = "wss"
	}
	r.Add(ws+"://"+u.Host, "<unraid-url>")
	r.Add(u.Host, "<unraid-host>")
	host := u.Hostname()
	r.Add(host, "<unraid-host>")
	if ip := net.ParseIP(host); ip == nil {
		// A name like tower.local: its first label is often the server's name on its own.
		if first, _, ok := strings.Cut(host, "."); ok {
			r.Add(first, "<unraid-host>")
		}
	}
}

// Redact returns s with every registered secret and every address-, MAC- or key-shaped
// token replaced.
func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	for _, sec := range r.secrets {
		s = replaceFold(s, sec.value, sec.label)
	}
	r.mu.RUnlock()
	s = reAPIKeyParam.ReplaceAllString(s, "${1}<redacted>")
	s = reKeyHeader.ReplaceAllString(s, "${1}<redacted>")
	s = reHex64.ReplaceAllString(s, "<hex64>")
	s = reMAC.ReplaceAllString(s, "<mac>")
	s = reIPv4.ReplaceAllString(s, "<ip>")
	return s
}

// Err renders an error with secrets removed. net/http errors embed the full request URL.
func (r *Redactor) Err(err error) string {
	if err == nil {
		return ""
	}
	return r.Redact(err.Error())
}

// replaceFold replaces every case-insensitive occurrence of old in s with repl.
func replaceFold(s, old, repl string) string {
	if old == "" {
		return s
	}
	lowS, lowOld := strings.ToLower(s), strings.ToLower(old)
	if len(lowS) != len(s) { // non-ASCII case folding changed lengths; fall back to exact match
		return strings.ReplaceAll(s, old, repl)
	}
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(lowS[i:], lowOld)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		b.WriteString(s[i : i+j])
		b.WriteString(repl)
		i += j + len(old)
	}
}
