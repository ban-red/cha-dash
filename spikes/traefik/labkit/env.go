// Package labkit is the shared plumbing for the read-only lab spikes (4.3
// Traefik, 4.4 Docker): env loading, a GET/HEAD-only HTTP client, TLS
// trust-on-first-use, a sanitizer for fixtures, and a leak check.
//
// Nothing in here prints a credential, URL, hostname or address.
package labkit

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

var envKey = regexp.MustCompile(`^[A-Z0-9_]+=`)

// Env holds KEY=value pairs read from an env file. Values are never logged.
type Env map[string]string

// LoadEnv reads KEY=value lines (keys matching ^[A-Z0-9_]+=). Comments, blank
// lines and anything else are skipped. Surrounding quotes are removed.
func LoadEnv(path string) (Env, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open env file: %w", err)
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Mode().Perm()&0o077 != 0 {
		fmt.Fprintln(os.Stderr, "warning: env file is readable by group or others (expected chmod 600)")
	}
	env := Env{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !envKey.MatchString(line) {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		env[k] = v
	}
	return env, sc.Err()
}

// Require returns an error naming (only) the keys that are missing or empty.
func (e Env) Require(keys ...string) error {
	var missing []string
	for _, k := range keys {
		if e[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("env file is missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// Values returns every non-empty value, sorted. They feed the leak check.
func (e Env) Values() []string {
	var out []string
	for _, v := range e {
		if v != "" {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}
