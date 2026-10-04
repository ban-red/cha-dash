package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var envKey = regexp.MustCompile(`^[A-Z0-9_]+=`)

// loadEnv reads KEY=value lines from a dotenv file. Values stay in memory and
// are never printed; callers only ever log key names.
func loadEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
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
		out[k] = v
	}
	return out, sc.Err()
}

func need(env map[string]string, keys ...string) error {
	for _, k := range keys {
		if env[k] == "" {
			return fmt.Errorf("env: %s is missing or empty", k)
		}
	}
	return nil
}
