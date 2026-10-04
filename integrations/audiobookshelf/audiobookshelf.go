// Package audiobookshelf reads Audiobookshelf's API: how many books and
// podcasts its libraries hold and how long they run, and how many listening
// sessions are open, with an API key (or a user's token). See README.md.
package audiobookshelf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "audiobookshelf"

// libraryEvery is how often the libraries' statistics are read again while
// watched; they change when something is added, and each library is a
// request.
const libraryEvery = 10 * time.Minute

// maxLibraries bounds the per-library requests of one sync.
const maxLibraries = 20

// libraryIDRE is what a library ID may look like before it goes into a path:
// a UUID, or the older "lib_…" form.
var libraryIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func init() { integration.Register(Integration{}) }

// Integration is the Audiobookshelf integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	key := integration.Secret("API key").Describe("Settings → API Keys (Audiobookshelf 2.26 and later), or a user's API token from Settings → Users.")
	return integration.Manifest{
		ID: ID, Name: "Audiobookshelf", Icon: "audiobookshelf", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"media.library", "media.sessions"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Audiobookshelf's address, with its path if it has one, e.g. https://audiobooks.home.arpa.")),
			integration.TLSProp(),
			integration.Prop("api_key", key),
		).Require("url", "api_key"),
		Poll: integration.Poll{Interval: time.Minute, Idle: 5 * time.Minute, Timeout: 30 * time.Second},
	}
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	return &instance{cfg: cfg, d: d, base: strings.TrimRight(cfg.String("url"), "/"), key: cfg.String("api_key"), rid: ID + ":" + key + "/app"}, nil
}

type instance struct {
	cfg  integration.Config
	d    integration.Deps
	base string
	key  string
	rid  string

	// From the last library read; only Sync touches them.
	lib    *totals
	readAt time.Time
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// statusError is an HTTP status a caller may tolerate (a 403 on the
// sessions, admin only).
type statusError struct {
	code int
	err  error
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

// get reads path into dst with the key as a bearer token.
func (in *instance) get(ctx context.Context, path string, auth bool, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.base+path, nil)
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	if auth {
		req.Header.Set("Authorization", "Bearer "+in.key)
	}
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &integration.AuthError{Field: "api_key", Message: "Audiobookshelf rejected the API key"}
	case http.StatusForbidden:
		return &statusError{code: resp.StatusCode, err: &integration.AuthError{Field: "api_key",
			Message: "Audiobookshelf refused " + path + " to this API key's user"}}
	case http.StatusNotFound:
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " answered 404 Not Found: check that the URL is Audiobookshelf's"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET " + path + " didn't answer with Audiobookshelf's JSON: check the URL"}
	}
	return nil
}

func forbidden(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.code == http.StatusForbidden
}

// status is GET /status, which needs no key.
type status struct {
	App           string `json:"app"`
	ServerVersion string `json:"serverVersion"`
	IsInit        bool   `json:"isInit"`
}

func (in *instance) readStatus(ctx context.Context) (status, error) {
	var s status
	if err := in.get(ctx, "/status", false, &s); err != nil {
		return s, err
	}
	if (s.App != "" && s.App != "audiobookshelf") || s.ServerVersion == "" {
		return s, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "/status doesn't say it's Audiobookshelf: check the URL"}
	}
	return s, nil
}

// totals are the libraries' statistics, by media type.
type totals struct {
	version         string
	libraries       int
	books, podcasts float64
	bookSec, podSec float64
}

// readLibraries reads /api/libraries and each library's /stats.
func (in *instance) readLibraries(ctx context.Context) (*totals, error) {
	var ls struct {
		Libraries []struct {
			ID        string `json:"id"`
			MediaType string `json:"mediaType"` // book, podcast
		} `json:"libraries"`
	}
	if err := in.get(ctx, "/api/libraries", true, &ls); err != nil {
		return nil, err
	}
	t := &totals{libraries: len(ls.Libraries)}
	for i, l := range ls.Libraries {
		if i == maxLibraries {
			break
		}
		if !libraryIDRE.MatchString(l.ID) {
			return nil, &integration.RepairError{Code: integration.CodeAPIChanged,
				Message: fmt.Sprintf("Audiobookshelf answered a library ID Cha Dash doesn't recognise (%q)", l.ID)}
		}
		var st struct {
			TotalItems    float64 `json:"totalItems"`
			TotalDuration float64 `json:"totalDuration"` // seconds
		}
		if err := in.get(ctx, "/api/libraries/"+url.PathEscape(l.ID)+"/stats", true, &st); err != nil {
			return nil, err
		}
		switch l.MediaType {
		case "podcast":
			t.podcasts += st.TotalItems
			t.podSec += st.TotalDuration
		default:
			t.books += st.TotalItems
			t.bookSec += st.TotalDuration
		}
	}
	return t, nil
}

// online is GET /api/users/online (admin only).
type online struct {
	UsersOnline  []json.RawMessage `json:"usersOnline"`
	OpenSessions []json.RawMessage `json:"openSessions"`
}

// me is the part of GET /api/me the grant needs.
type me struct {
	Type        string `json:"type"` // root, admin, user, guest
	Permissions struct {
		Download bool `json:"download"`
		Update   bool `json:"update"`
		Delete   bool `json:"delete"`
		Upload   bool `json:"upload"`
	} `json:"permissions"`
}

// grant reads back what the key's user may do.
func grant(u me) integration.Grant {
	g := integration.Grant{Name: "Audiobookshelf user", Access: integration.AccessRead,
		Detail: "a " + u.Type + " who can't upload, change or delete items"}
	switch {
	case u.Type == "root" || u.Type == "admin":
		g.Access = integration.AccessAdmin
		g.Detail = "an " + u.Type + " can change libraries, users and settings"
		if u.Type == "root" {
			g.Detail = "the root user can change libraries, users and settings"
		}
	case u.Permissions.Update || u.Permissions.Delete || u.Permissions.Upload:
		g.Access = integration.AccessReadWrite
		var can []string
		for _, p := range []struct {
			on   bool
			verb string
		}{{u.Permissions.Upload, "upload"}, {u.Permissions.Update, "change"}, {u.Permissions.Delete, "delete"}} {
			if p.on {
				can = append(can, p.verb)
			}
		}
		g.Detail = "a " + u.Type + " who can " + strings.Join(can, ", ") + " items"
	}
	return g
}

// Test implements integration.Instance.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	s, err := in.readStatus(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	fail := func(err error) integration.TestResult {
		r := integration.Failed(err)
		r.Version = s.ServerVersion
		return r
	}
	var u me
	if err := in.get(ctx, "/api/me", true, &u); err != nil {
		return fail(err)
	}
	if _, err := in.readLibraries(ctx); err != nil {
		return fail(err)
	}
	res := integration.TestResult{OK: true, Version: s.ServerVersion, Grants: []integration.Grant{grant(u)}}
	var o online
	switch err := in.get(ctx, "/api/users/online", true, &o); {
	case forbidden(err):
		res.Warnings = append(res.Warnings, integration.Warning{Code: "sessions_not_admin", Severity: integration.WarnInfo,
			Message: "Audiobookshelf shows open listening sessions only to an admin, so they stay unknown with this key. Books, podcasts and their hours still read."})
	case err != nil:
		return fail(err)
	}
	return res
}

// Sync implements integration.Instance: the open sessions every sync, the
// libraries on the first and then every libraryEvery while watched.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	if in.lib == nil || (want.Watched && now.Sub(in.readAt) >= libraryEvery) {
		s, err := in.readStatus(ctx)
		if err != nil {
			return err
		}
		t, err := in.readLibraries(ctx)
		if err != nil {
			return err
		}
		t.version = s.ServerVersion
		in.lib, in.readAt = t, now
		in.d.Emit.SetVersion(s.ServerVersion)
	}
	streams := integration.Unknown()
	var o online
	switch err := in.get(ctx, "/api/users/online", true, &o); {
	case forbidden(err):
	case err != nil:
		return err
	default:
		streams = float64(len(o.OpenSessions))
	}
	t := in.lib
	attrs := map[string]any{"type": ID, "url": in.base, "version": t.version, "libraries": t.libraries}
	res := integration.Resource{ID: in.rid, Kind: "app", Name: in.cfg.Name, State: "running",
		Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs,
		Caps: []string{"media.library", "media.sessions"}, Links: []integration.Link{{Title: "Audiobookshelf", URL: in.base}}}
	hours := func(sec float64) float64 { return math.Round(sec/3600*10) / 10 }
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.rid}},
		Metrics: []integration.Metric{
			{Resource: in.rid, Key: "streams", Unit: "count", Max: 10, Band: [2]float64{0, 3}, Value: streams},
			{Resource: in.rid, Key: "books", Unit: "count", Max: 100000, Band: [2]float64{0, 100000}, Value: t.books},
			{Resource: in.rid, Key: "books.hours", Unit: "h", Max: 1e6, Band: [2]float64{0, 1e6}, Value: hours(t.bookSec)},
			{Resource: in.rid, Key: "podcasts", Unit: "count", Max: 10000, Band: [2]float64{0, 10000}, Value: t.podcasts},
			{Resource: in.rid, Key: "podcasts.hours", Unit: "h", Max: 1e6, Band: [2]float64{0, 1e6}, Value: hours(t.podSec)},
		},
	})
}
