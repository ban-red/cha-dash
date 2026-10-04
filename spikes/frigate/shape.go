package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// runShape prints the structure of raw captures without their string values,
// so the sanitizer can be written without anyone reading real hostnames,
// addresses or names. Numbers and booleans are shown; strings are shown only
// when they are clearly not identifying (numeric, a version, a known word).
// userNames holds camera, zone and go2rtc stream names read from the raw
// config next to the file being shaped; they are masked wherever they occur.
var userNames = map[string]bool{}

func loadUserNames(dir string) {
	b, err := os.ReadFile(filepath.Join(dir, "api_config.raw.json"))
	if err != nil {
		return
	}
	var cfg struct {
		Cameras map[string]struct {
			Zones map[string]json.RawMessage `json:"zones"`
		} `json:"cameras"`
		Go2rtc struct {
			Streams map[string]json.RawMessage `json:"streams"`
		} `json:"go2rtc"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return
	}
	for c, cc := range cfg.Cameras {
		userNames[c] = true
		for z := range cc.Zones {
			userNames[z] = true
		}
	}
	for s := range cfg.Go2rtc.Streams {
		userNames[s] = true
	}
}

func runShape(args []string) error {
	for _, p := range args {
		loadUserNames(filepath.Dir(p))
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Printf("== %s (%d bytes)\n", p[strings.LastIndex(p, "/")+1:], len(b))
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			fmt.Printf("  not JSON: %s\n", classify(string(b[:min(len(b), 4096)])))
			continue
		}
		printShape(v, "  ", 0, "")
	}
	return nil
}

// maskChildren lists parents whose keys are user-chosen names (cameras,
// zones, go2rtc streams, camera groups, faces), never printed.
var maskChildren = map[string]bool{"cameras": true, "zones": true, "streams": true, "camera_groups": true, "faces": true, "allowed_cameras": true, "sub_labels": true, "required_zones": true}

func printShape(v any, ind string, depth int, parent string) {
	if depth > 8 {
		fmt.Printf("%s…\n", ind)
		return
	}
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		maskKeys := looksLikeDataKeys(keys)
		for i, k := range keys {
			name := k
			if maskChildren[parent] || userNames[k] {
				name = fmt.Sprintf("<name#%d len=%d>", i, len(k))
			} else if maskKeys {
				name = fmt.Sprintf("<key %s>", classify(k))
				if i >= 3 {
					fmt.Printf("%s… %d more keys\n", ind, len(keys)-3)
					return
				}
			}
			switch c := t[k].(type) {
			case map[string]any, []any:
				fmt.Printf("%s%s:\n", ind, name)
				printShape(c, ind+"  ", depth+1, k)
			default:
				fmt.Printf("%s%s: %s\n", ind, name, scalar(c))
			}
		}
	case []any:
		fmt.Printf("%s[%d items]\n", ind, len(t))
		if len(t) == 0 {
			return
		}
		// Union of keys across object items, else the first item.
		if _, ok := t[0].(map[string]any); ok {
			merged := map[string]any{}
			for _, it := range t {
				if m, ok := it.(map[string]any); ok {
					for k, vv := range m {
						if _, seen := merged[k]; !seen {
							merged[k] = vv
						}
					}
				}
			}
			printShape(merged, ind+"  ", depth+1, parent)
			return
		}
		switch t[0].(type) {
		case []any:
			printShape(t[0], ind+"  ", depth+1, parent)
		default:
			fmt.Printf("%s  e.g. %s\n", ind, scalar(t[0]))
		}
	default:
		fmt.Printf("%s%s\n", ind, scalar(t))
	}
}

// looksLikeDataKeys reports whether object keys are data (IPs, names, MACs)
// rather than field names.
func looksLikeDataKeys(keys []string) bool {
	for _, k := range keys {
		c := classify(k)
		if strings.HasPrefix(c, "ip") || strings.HasPrefix(c, "mac") || strings.HasPrefix(c, "host") {
			return true
		}
	}
	return false
}

var (
	reIPv4    = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)
	reIPv6    = regexp.MustCompile(`(?i)\b[0-9a-f]{1,4}(:[0-9a-f]{0,4}){2,7}\b`)
	reMAC     = regexp.MustCompile(`(?i)\b[0-9a-f]{2}([:-][0-9a-f]{2}){5}\b`)
	reNumeric = regexp.MustCompile(`^[-+]?[0-9][0-9.,:_ ]*\s*(%|[KMGT]i?B|[KMGT]?bps|ms|s|C|°C|K|M|G|B|bytes|kbps|Mbps)?$`)
	reVersion = regexp.MustCompile(`^(OPNsense|FreeBSD|OpenSSL|LibreSSL|Python|PHP)?\s*[0-9]+(\.[0-9]+)+[0-9A-Za-z._\-]*(-amd64|-RELEASE[-p0-9]*)?$`)
	reURL     = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)
	reHost    = regexp.MustCompile(`(?i)^[a-z0-9-]+(\.[a-z0-9-]+)+\.?$`)
	reDate    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}([ T]\d{2}:\d{2}(:\d{2})?)?`)
	reB64     = regexp.MustCompile(`^[A-Za-z0-9+/=]{200,}$`)
)

// safeWords are values we are happy to see in the terminal.
var safeWords = map[string]bool{
	"": true, "up": true, "down": true, "none": true, "ok": true, "OK": true, "online": true, "offline": true,
	"true": true, "false": true, "yes": true, "no": true, "1": true, "0": true, "wan": true, "lan": true,
	"opt1": true, "opt2": true, "opt3": true, "opt4": true, "opt5": true, "opt6": true, "lo0": true,
	"running": true, "stopped": true, "enabled": true, "disabled": true, "active": true, "inactive": true,
	"static": true, "dhcp": true, "dhcp6": true, "inet": true, "inet6": true, "default": true, "ethernet": true,
	"Online": true, "Offline": true, "Pending": true, "Unknown": true, "pending": true, "unknown": true,
	"FreeBSD": true, "OPNsense": true, "amd64": true, "OpenSSL": true, "success": true, "error": true, "failed": true,
	"WAN": true, "LAN": true, "Loopback": true, "Up": true, "Down": true, "update": true, "upgrade": true,
}

func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return fmt.Sprint(t)
	case float64:
		return fmt.Sprint(t)
	case string:
		if userNames[t] {
			return fmt.Sprintf("<user-name len=%d>", len(t))
		}
		// Addresses first: an IPv4 address also looks "numeric".
		if reIPv4.MatchString(t) || reMAC.MatchString(t) || (reIPv6.MatchString(t) && strings.Count(t, ":") >= 2 && !reDate.MatchString(t)) {
			return "<" + classify(t) + ">"
		}
		if safeWords[t] || reNumeric.MatchString(t) || reVersion.MatchString(t) {
			return fmt.Sprintf("%q", t)
		}
		if reDate.MatchString(t) && len(t) <= 32 {
			return fmt.Sprintf("<date %d>", len(t))
		}
		return "<" + classify(t) + ">"
	}
	return fmt.Sprintf("<%T>", v)
}

func classify(s string) string {
	switch {
	case reMAC.MatchString(s):
		return fmt.Sprintf("mac len=%d", len(s))
	case reURL.MatchString(s):
		return fmt.Sprintf("url len=%d", len(s))
	case reIPv4.MatchString(s):
		return fmt.Sprintf("ip4 len=%d", len(s))
	case reIPv6.MatchString(s) && strings.Count(s, ":") >= 2:
		return fmt.Sprintf("ip6 len=%d", len(s))
	case reHost.MatchString(s) && !reNumeric.MatchString(s):
		return fmt.Sprintf("host? len=%d", len(s))
	case reB64.MatchString(s):
		return fmt.Sprintf("base64 len=%d", len(s))
	case strings.ContainsAny(s, "\n"):
		return fmt.Sprintf("text lines=%d len=%d", strings.Count(s, "\n")+1, len(s))
	default:
		return fmt.Sprintf("str len=%d", len(s))
	}
}
