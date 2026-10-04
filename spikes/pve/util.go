package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// replaceAllFold replaces every case-insensitive occurrence of old in s.
func replaceAllFold(s, old, new string) string {
	if old == "" {
		return s
	}
	lo, lold := strings.ToLower(s), strings.ToLower(old)
	if !strings.Contains(lo, lold) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(lo, lold)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(new)
		s, lo = s[i+len(old):], lo[i+len(old):]
	}
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// envelope is PVE's response shape: {"data": …}. Errors may add "errors".
type envelope struct {
	Data json.RawMessage `json:"data"`
}

func dataOf(body []byte, v any) error {
	var e envelope
	if err := json.Unmarshal(body, &e); err != nil {
		return err
	}
	return json.Unmarshal(e.Data, v)
}

// num reads a JSON number or numeric string (PVE mixes both).
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		var f float64
		_ = json.Unmarshal([]byte(x), &f)
		return f
	}
	return 0
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		b, _ := json.Marshal(x)
		return string(b)
	case json.Number:
		return x.String()
	}
	return ""
}
