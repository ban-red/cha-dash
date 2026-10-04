package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// envLine matches the only lines we read from .env.local: KEY=VALUE with an upper-case key.
var envLine = regexp.MustCompile(`^([A-Z0-9_]+)=(.*)$`)

// loadEnvFile reads KEY=VALUE pairs from path. Comments, blank lines and anything that
// doesn't start with an upper-case key are ignored. Surrounding single or double quotes
// are stripped. Values are never logged, so errors only ever name the line number.
func loadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := envLine.FindStringSubmatch(strings.TrimRight(sc.Text(), "\r"))
		if m == nil {
			continue
		}
		out[m[1]] = unquote(strings.TrimSpace(m[2]))
	}
	return out, sc.Err()
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// findEnvFile walks up from dir looking for .env.local. From a worktree under
// .claude/worktrees/<name>/spikes/unraid it reaches the main checkout's file.
func findEnvFile(dir string) (string, error) {
	for {
		p := filepath.Join(dir, ".env.local")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New(".env.local not found in this directory or any parent")
		}
		dir = parent
	}
}

// instanceConfig is one Unraid server's connection settings, read from the env file.
type instanceConfig struct {
	Name   string // upper-case instance name, e.g. JUPITER
	URL    string // base URL of the webGUI, e.g. http://<ip>
	APIKey string // empty until the consent flow has been completed
}

func loadInstance(envPath, name string) (instanceConfig, error) {
	vals, err := loadEnvFile(envPath)
	if err != nil {
		return instanceConfig{}, fmt.Errorf("read env file: %w", err)
	}
	name = strings.ToUpper(name)
	cfg := instanceConfig{
		Name:   name,
		URL:    strings.TrimRight(vals["UNRAID_"+name+"_URL"], "/"),
		APIKey: vals["UNRAID_"+name+"_API_KEY"],
	}
	if cfg.URL == "" {
		return cfg, fmt.Errorf("UNRAID_%s_URL is not set in the env file", name)
	}
	return cfg, nil
}
