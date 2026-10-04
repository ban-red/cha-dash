// Package plex reads a Plex Media Server: what's playing, and how big the
// movie and show libraries are. Its token can come from plex.tv's PIN
// sign-in (see auth.go), so nobody has to dig an X-Plex-Token out of the
// web app. See README.md.
package plex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "plex"

// librariesEvery is how often the library counts are read again while
// someone watches.
const librariesEvery = 30 * time.Minute

func init() { integration.Register(Integration{}) }

// Integration is the Plex integration. It implements integration.Authorizer.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	token := integration.Secret("Token").Describe("Sign in with Plex to fill this in, or paste an X-Plex-Token. Use the server owner's account: only it can see what's playing.")
	token.Authorize = true
	return integration.Manifest{
		ID: ID, Name: "Plex", Icon: "plex", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"media.sessions", "media.library"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Your Plex Media Server: https://plex.lab.example, or https://<address>:32400.")),
			integration.TLSProp(),
			integration.Prop("token", token),
		).Require("url", "token"),
		Poll: integration.Poll{Interval: 30 * time.Second, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	return &instance{cfg: cfg, d: d, base: strings.TrimRight(cfg.String("url"), "/"), token: cfg.String("token")}, nil
}

type instance struct {
	cfg   integration.Config
	d     integration.Deps
	base  string
	token string

	// From the last slow read.
	identity  identity
	libraries *libraries // nil until read
	readAt    time.Time
}

// container is Plex's JSON envelope.
type container[T any] struct {
	MediaContainer T `json:"MediaContainer"`
}

type identity struct {
	MachineIdentifier string `json:"machineIdentifier"`
	Version           string `json:"version"`
	Claimed           bool   `json:"claimed"`
}

type sessions struct {
	Size     *int `json:"size"`
	Metadata []struct {
		Type             string    `json:"type"`
		TranscodeSession *struct{} `json:"TranscodeSession"`
	} `json:"Metadata"`
}

type sections struct {
	Directory []struct {
		Key   string `json:"key"`
		Type  string `json:"type"` // movie, show, artist, photo
		Title string `json:"title"`
	} `json:"Directory"`
}

type sectionAll struct {
	TotalSize *int `json:"totalSize"`
	Size      int  `json:"size"`
}

// libraries are the counts the Demo Home keeps in attrs.
type libraries struct {
	movies, shows, sections int
}

// get reads path into dst with the token in X-Plex-Token. A 401 is an auth
// Repair on token.
func (in *instance) get(ctx context.Context, path string, q url.Values, dst any) error {
	u := in.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", in.token)
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &integration.AuthError{Field: "token",
			Message: "Plex rejected the token: it was signed out, or the account isn't the server's owner. Sign in with Plex again"}
	case http.StatusNotFound:
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " answered 404 Not Found: check that the URL is a Plex Media Server"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " didn't answer with Plex's JSON: check that the URL is a Plex Media Server"}
	}
	return nil
}

func (in *instance) readIdentity(ctx context.Context) error {
	var c container[identity]
	if err := in.get(ctx, "/identity", nil, &c); err != nil {
		return err
	}
	if c.MediaContainer.MachineIdentifier == "" {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url", Message: "/identity has no machine identifier: check that the URL is a Plex Media Server"}
	}
	in.identity = c.MediaContainer
	return nil
}

func (in *instance) streams(ctx context.Context) (int, error) {
	var c container[sessions]
	if err := in.get(ctx, "/status/sessions", nil, &c); err != nil {
		return 0, err
	}
	if c.MediaContainer.Size != nil {
		return *c.MediaContainer.Size, nil
	}
	return len(c.MediaContainer.Metadata), nil
}

// readLibraries counts the movies and shows across the movie and show
// sections, one zero-size page each.
func (in *instance) readLibraries(ctx context.Context) (*libraries, error) {
	var c container[sections]
	if err := in.get(ctx, "/library/sections", nil, &c); err != nil {
		return nil, err
	}
	lib := &libraries{sections: len(c.MediaContainer.Directory)}
	page := url.Values{"X-Plex-Container-Start": {"0"}, "X-Plex-Container-Size": {"0"}}
	for _, s := range c.MediaContainer.Directory {
		if s.Type != "movie" && s.Type != "show" {
			continue
		}
		var all container[sectionAll]
		if err := in.get(ctx, "/library/sections/"+url.PathEscape(s.Key)+"/all", page, &all); err != nil {
			return nil, err
		}
		n := all.MediaContainer.Size
		if all.MediaContainer.TotalSize != nil {
			n = *all.MediaContainer.TotalSize
		}
		if s.Type == "movie" {
			lib.movies += n
		} else {
			lib.shows += n
		}
	}
	return lib, nil
}

// grant is what a Plex token can do: it's the account's, so everything the
// account can, the server's settings included. There are no read-only
// tokens.
func grant() integration.Grant {
	return integration.Grant{Name: "Plex account token", Access: integration.AccessAdmin,
		Detail: "a Plex token acts as the account: it can manage the server and the account; Plex has no read-only tokens"}
}

// Test implements integration.Instance: the server's identity, and the
// sessions (which need the owner's token).
func (in *instance) Test(ctx context.Context) integration.TestResult {
	if err := in.readIdentity(ctx); err != nil {
		return integration.Failed(err)
	}
	if _, err := in.streams(ctx); err != nil {
		return integration.Failed(err)
	}
	return integration.TestResult{OK: true, Version: in.identity.Version, Grants: []integration.Grant{grant()}}
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	if in.libraries == nil || (want.Watched && now.Sub(in.readAt) >= librariesEvery) {
		if err := in.readIdentity(ctx); err != nil {
			return err
		}
		in.d.Emit.SetVersion(in.identity.Version)
		lib, err := in.readLibraries(ctx)
		if err != nil {
			return err
		}
		in.libraries, in.readAt = lib, now
	}
	n, err := in.streams(ctx)
	if err != nil {
		return err
	}
	key := in.cfg.InstanceID
	if key == "" {
		key = "test"
	}
	rid := ID + ":" + key + "/app"
	attrs := map[string]any{
		"type": ID, "url": in.base, "version": in.identity.Version, "machine_identifier": in.identity.MachineIdentifier,
		"movies": in.libraries.movies, "shows": in.libraries.shows, "libraries": in.libraries.sections,
	}
	res := integration.Resource{ID: rid, Kind: "app", Name: in.cfg.Name, State: "running",
		Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs,
		Caps: []string{"media.sessions", "media.library"}, Links: []integration.Link{{Title: "Plex", URL: in.base}}}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: rid}},
		Metrics:   []integration.Metric{{Resource: rid, Key: "streams", Unit: "count", Max: 10, Band: [2]float64{0, 3}, Value: float64(n)}},
	})
}
