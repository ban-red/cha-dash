package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"dash.cha.sh/spikes/traefik/labkit"
)

// Meta is what capture learned about the API itself (no values that identify
// the lab: no URL, host, user or password).
type Meta struct {
	CapturedAt     time.Time         `json:"capturedAt"`
	Scheme         string            `json:"scheme"`         // http or https: plain http means basic auth in clear text
	TLSVerified    bool              `json:"tlsVerified"`    // publicly trusted chain
	TLSPinned      bool              `json:"tlsPinned"`      // self-signed, trusted on first use
	UnauthStatus   int               `json:"unauthStatus"`   // GET /api/version without credentials
	UnauthRawdata  int               `json:"unauthRawdata"`  // GET /api/rawdata without credentials
	AuthStatus     int               `json:"authStatus"`     // GET /api/version with credentials
	DashboardPath  int               `json:"dashboardPath"`  // GET /dashboard/ with credentials
	Endpoints      map[string]int    `json:"endpoints"`      // status per API path
	Pages          map[string]int    `json:"pages"`          // pages fetched per path
	ResponseHeader map[string]string `json:"responseHeader"` // a few non-identifying headers
}

var apiPaths = []string{
	"/api/version",
	"/api/overview",
	"/api/entrypoints",
	"/api/http/routers",
	"/api/http/services",
	"/api/http/middlewares",
	"/api/tcp/routers",
	"/api/tcp/services",
	"/api/tcp/middlewares",
	"/api/udp/routers",
	"/api/udp/services",
}

func capture(envPath, rawDir, pinFile string) error {
	env, err := labkit.LoadEnv(envPath)
	if err != nil {
		return err
	}
	if err := env.Require("TRAEFIK_URL", "TRAEFIK_USERNAME", "TRAEFIK_PASSWORD"); err != nil {
		return err
	}
	base, err := url.Parse(env["TRAEFIK_URL"])
	if err != nil || base.Host == "" {
		return errors.New("TRAEFIK_URL does not parse as a URL")
	}
	if err := os.MkdirAll(rawDir, 0o700); err != nil {
		return err
	}
	var tlsRes labkit.TLSResult
	tr := labkit.TOFUTransport(pinFile, &tlsRes)
	client := labkit.NewReadOnlyClient(tr, nil, 300*time.Millisecond, 15*time.Second)

	meta := Meta{CapturedAt: time.Now().UTC().Truncate(time.Second), Scheme: base.Scheme,
		Endpoints: map[string]int{}, Pages: map[string]int{}, ResponseHeader: map[string]string{}}
	user, pass := env["TRAEFIK_USERNAME"], env["TRAEFIK_PASSWORD"]

	get := func(path string, auth bool) (*http.Response, []byte, error) {
		u := *base
		u.Path, u.RawQuery, u.Fragment, u.User = "", "", "", nil
		ref, _ := url.Parse(path)
		target := u.ResolveReference(ref)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		req.Header.Set("Accept", "application/json")
		if auth {
			req.SetBasicAuth(user, pass)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, scrubErr(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		return resp, body, err
	}

	// 1. Is the API behind auth? Status codes only, bodies discarded.
	for _, p := range []struct {
		path string
		dst  *int
	}{{"/api/version", &meta.UnauthStatus}, {"/api/rawdata", &meta.UnauthRawdata}} {
		resp, _, err := get(p.path, false)
		if err != nil {
			return fmt.Errorf("unauthenticated probe %s: %w", p.path, err)
		}
		*p.dst = resp.StatusCode
		logf("GET %s (no auth) → %d", p.path, resp.StatusCode)
	}
	if resp, _, err := get("/dashboard/", true); err == nil {
		meta.DashboardPath = resp.StatusCode
		logf("GET /dashboard/ → %d", resp.StatusCode)
	}

	// 2. The API itself, with credentials. Traefik paginates with X-Next-Page.
	for _, path := range apiPaths {
		var all []json.RawMessage
		var single json.RawMessage
		page := 1
		for {
			q := path
			if path != "/api/version" && path != "/api/overview" {
				q += "?per_page=100&page=" + strconv.Itoa(page)
			}
			resp, body, err := get(q, true)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			meta.Endpoints[path] = resp.StatusCode
			if path == "/api/version" {
				meta.AuthStatus = resp.StatusCode
				for _, h := range []string{"Content-Type", "Server"} {
					if v := resp.Header.Get(h); v != "" {
						meta.ResponseHeader[h] = v
					}
				}
			}
			logf("GET %s → %d (%d bytes)", q, resp.StatusCode, len(body))
			if resp.StatusCode != http.StatusOK {
				break
			}
			meta.Pages[path] = page
			var items []json.RawMessage
			if err := json.Unmarshal(body, &items); err != nil {
				single = body // an object, not a list
				break
			}
			all = append(all, items...)
			next := resp.Header.Get("X-Next-Page")
			if n, err := strconv.Atoi(next); err != nil || n <= page || page >= 50 {
				break
			} else {
				page = n
			}
		}
		var out any = all
		if single != nil {
			out = single
		}
		if meta.Endpoints[path] == http.StatusOK {
			if err := writeJSON(filepath.Join(rawDir, fileFor(path)), out); err != nil {
				return err
			}
		}
	}
	meta.TLSVerified, meta.TLSPinned = tlsRes.Verified, tlsRes.Pinned
	if tlsRes.FirstUse {
		logf("TLS: self-signed certificate pinned on first use (fingerprint stored in the pin file)")
	}
	return writeJSON(filepath.Join(rawDir, "meta.json"), meta)
}

func fileFor(path string) string {
	switch path {
	case "/api/version":
		return "version.json"
	case "/api/overview":
		return "overview.json"
	case "/api/entrypoints":
		return "entrypoints.json"
	}
	// /api/http/routers → http-routers.json
	p := path[len("/api/"):]
	b := []byte(p)
	for i := range b {
		if b[i] == '/' {
			b[i] = '-'
		}
	}
	return string(b) + ".json"
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

// scrubErr keeps the error kind but drops the URL (which names the host).
func scrubErr(err error) error { return errors.New(labkit.SafeErr(err)) }
