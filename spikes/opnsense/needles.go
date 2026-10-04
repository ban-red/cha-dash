package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// runNeedles builds the leak-check list: every real value seen in the raw
// captures (addresses, MACs, DNS names, labels, usernames) plus every value
// in the dotenv file. The list is written to a git-ignored file outside the
// repo and is never printed; only counts are.
func runNeedles(args []string) error {
	fs := flag.NewFlagSet("needles", flag.ExitOnError)
	raw := fs.String("raw", "", "raw capture directory")
	envPath := fs.String("env", "", "dotenv file")
	out := fs.String("out", "", "needle file (outside the repo)")
	_ = fs.Parse(args)
	if *raw == "" || *out == "" {
		return fmt.Errorf("needles: --raw and --out are required")
	}
	set := map[string]string{} // value → class
	add := func(class, v string) {
		v = strings.TrimSpace(v)
		if len(v) < 3 {
			return
		}
		if _, ok := set[v]; !ok {
			set[v] = class
		}
	}
	files, _ := filepath.Glob(filepath.Join(*raw, "*.raw.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		collectTokens(string(b), add)
		var v any
		if json.Unmarshal(b, &v) == nil {
			walkStrings(v, "", func(key, s string) {
				collectTokens(s, add) // SSE data is JSON inside a string
				switch key {
				case "rname", "hostname", "host", "domain", "fqdn", "client-hostname":
					add("host", s)
				case "USERNAME":
					if !systemUsers[s] {
						add("user", s)
					}
				case "name":
					// interface descriptions (labels) other than stock ones
					if s != "WAN" && s != "LAN" && len(s) >= 3 {
						add("label", s)
					}
				}
			})
		}
	}
	if *envPath != "" {
		env, err := loadEnv(*envPath)
		if err != nil {
			return err
		}
		for k, v := range env {
			add("env:"+k, v)
			if u, err := url.Parse(v); err == nil && u.Host != "" {
				add("env-host", u.Host)
				add("env-host", u.Hostname())
			}
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	counts := map[string]int{}
	for _, k := range keys {
		fmt.Fprintf(w, "%s\t%s\n", set[k], k)
		c := set[k]
		if strings.HasPrefix(c, "env:") {
			c = "env"
		}
		counts[c]++
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Printf("needles written: %d (%v)\n", len(keys), counts)
	return nil
}

func collectTokens(s string, add func(class, v string)) {
	for _, t := range reIP4tok.FindAllString(s, -1) {
		if a, err := netip.ParseAddr(t); err == nil && !isSpecial4(a) {
			add("ip4", t)
		}
	}
	for _, t := range reMACtok.FindAllString(s, -1) {
		add("mac", t)
	}
	for _, t := range reIP6tok.FindAllString(s, -1) {
		base, _, _ := strings.Cut(t, "%")
		if a, err := netip.ParseAddr(base); err == nil && a.Is6() && !a.IsLoopback() && !a.IsUnspecified() {
			add("ip6", base)
		}
	}
}

func walkStrings(v any, key string, fn func(key, s string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			fn("$key", k)
			walkStrings(vv, k, fn)
		}
	case []any:
		for _, vv := range t {
			walkStrings(vv, key, fn)
		}
	case string:
		fn(key, t)
	}
}
