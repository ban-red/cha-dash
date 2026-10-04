package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// secrets collects credentials found in URLs while scrubbing, so the leak
// check can look for them. They are written only to a 0600 needle file
// outside the repo, never printed.
type secrets struct {
	mu sync.Mutex
	v  map[string]string // value → class
}

func (s *secrets) add(class, v string) {
	if len(v) < 3 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.v == nil {
		s.v = map[string]string{}
	}
	s.v[v] = class
}

func (s *secrets) save(path string) error {
	if path == "" || len(s.v) == 0 {
		return nil
	}
	old, _ := os.ReadFile(path)
	set := map[string]string{}
	for _, l := range strings.Split(string(old), "\n") {
		if c, v, ok := strings.Cut(l, "\t"); ok {
			set[v] = c
		}
	}
	for v, c := range s.v {
		set[v] = c
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\t%s\n", set[k], k)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

var (
	// scheme://user:pass@host… in any string (RTSP inputs, go2rtc, MQTT)
	reCredURL = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)([^/@\s:"']+):([^/@\s"']*)@`)
	// user=…&password=… query strings some cameras use
	reCredQuery = regexp.MustCompile(`(?i)\b(user(?:name)?|pass(?:word)?|pwd|token|key)=([^&\s"']+)`)
	// base64 image payloads: JPEG, PNG, WebP, GIF, or a data: URI
	reB64Image = regexp.MustCompile(`^(data:image/|/9j/|iVBORw0KGgo|UklGR|R0lGOD)`)
)

// redactedSentinel is what Frigate 0.18 puts in place of saved credentials
// (frigate/const.py REDACTED_CREDENTIAL_SENTINEL). It is not a secret, and
// the integration should recognise it.
const redactedSentinel = "__FRIGATE_SAVED_CREDENTIAL__"

// secretKeys hold credentials in Frigate's config (MQTT, ONVIF, Frigate+,
// notifications, GenAI providers).
var secretKeys = map[string]bool{"password": true, "pass": true, "secret": true, "token": true, "api_key": true, "client_secret": true, "plus_api_key": true, "private_key": true, "vapid_private_key": true}

// imageKeys are fields that carry pictures in Frigate payloads.
var imageKeys = map[string]bool{"thumbnail": true, "thumb": true, "snapshot": true, "image": true, "jpg": true, "frame": true}

// scrubImages removes every picture and credential from a metadata payload
// before it is written anywhere, even to the private raw directory.
func scrubImages(b []byte, sec *secrets) []byte {
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	// json.Valid first: a version string such as "0.18.1-abc" starts with a
	// valid JSON number, and Decode alone would silently keep only "0.18".
	if !json.Valid(b) || d.Decode(&v) != nil {
		return []byte(scrubString(string(b), sec))
	}
	v = scrubValue(v, "", sec)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return out.Bytes()
}

func scrubValue(v any, key string, sec *secrets) any {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			t[k] = scrubValue(vv, k, sec)
		}
		return t
	case []any:
		for i, vv := range t {
			t[i] = scrubValue(vv, key, sec)
		}
		return t
	case string:
		if isImageString(key, t) {
			return fmt.Sprintf("<image removed: %d chars>", len(t))
		}
		if secretKeys[strings.ToLower(key)] && t != "" && t != redactedSentinel {
			sec.add("cred-"+strings.ToLower(key), t)
			return "<redacted>"
		}
		return scrubString(t, sec)
	}
	return v
}

func isImageString(key, s string) bool {
	if reB64Image.MatchString(s) {
		return true
	}
	return imageKeys[strings.ToLower(key)] && len(s) > 256
}

func scrubString(s string, sec *secrets) string {
	s = reCredURL.ReplaceAllStringFunc(s, func(m string) string {
		p := reCredURL.FindStringSubmatch(m)
		if p[2] == "*" && p[3] == "*" {
			return m // Frigate's own mask (clean_camera_user_pass): keep the wire format
		}
		if u, err := url.PathUnescape(p[2]); err == nil {
			sec.add("cred-user", u)
		}
		sec.add("cred-user", p[2])
		if pw, err := url.PathUnescape(p[3]); err == nil {
			sec.add("cred-pass", pw)
		}
		sec.add("cred-pass", p[3])
		return p[1] + "<user>:<password>@"
	})
	s = reCredQuery.ReplaceAllStringFunc(s, func(m string) string {
		p := reCredQuery.FindStringSubmatch(m)
		if p[2] == "*" {
			return m // already masked by Frigate (user=*&password=*)
		}
		if !strings.HasPrefix(p[2], "{") && !strings.HasPrefix(p[2], "<") {
			sec.add("cred-"+strings.ToLower(p[1]), p[2])
		}
		return p[1] + "=<redacted>"
	})
	return s
}
