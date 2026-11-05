package updates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"dash.cha.sh/internal/buildinfo"
)

// Release notes (docs/plan/14-v0.4.md, "Release notes", decision 3): for an
// image whose org.opencontainers.image.source label is a GitHub repository,
// the hub reads that repository's releases newer than the version the
// container says it runs. It's the hub calling GitHub, so it's a setting,
// off by default; the call is unauthenticated, with GitHub's rate limit
// (60 an hour per address), so answers are kept for 6 hours (30 minutes
// after a failure).
//
// What comes back is untrusted text: HTML is removed, and a link stays only
// when it points into the same repository (anything else becomes "link
// removed"). The web shows it as plain text and links only those.

// NotesKey is the settings row: {"enabled": bool}.
const NotesKey = "updates.release_notes"

// Release-notes limits.
const (
	notesTTL      = 6 * time.Hour
	notesFailTTL  = 30 * time.Minute
	notesTimeout  = 10 * time.Second
	notesMaxBytes = 2 << 20 // GitHub's answer for 30 releases
	// MaxReleases is how many releases a row's notes show, newest first.
	MaxReleases = 10
	// MaxNoteBytes is how much of one release's text is kept.
	MaxNoteBytes = 8 << 10
)

// Release is one release, as the notes route answers it.
type Release struct {
	Tag       string    `json:"tag"`
	Name      string    `json:"name,omitempty"`
	Published time.Time `json:"published_at,omitzero"`
	// URL is the release's page, only ever in the same repository.
	URL string `json:"url,omitempty"`
	// Notes is the release's text, untrusted: no HTML, links only into the
	// same repository. Show it as plain text.
	Notes     string `json:"notes"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Notes is GET /api/updates/{resource}/notes.
type Notes struct {
	Repo string `json:"repo"` // https://github.com/<owner>/<repo>
	// From is the version the container runs, when it says.
	From     string    `json:"from,omitempty"`
	Releases []Release `json:"releases"`
	// Newer: Releases are those newer than From. False when From isn't
	// known: they're the newest few.
	Newer     bool      `json:"newer"`
	FetchedAt time.Time `json:"fetched_at"`
}

// githubRE is a GitHub repository URL as the label gives it, or a tree or
// file in it (authelia's label is .../tree/<commit>): only the owner and
// the repository are kept.
var githubRE = regexp.MustCompile(`^(?i)(?:https?://|git@|git\+https://)?(?:www\.)?github\.com[/:]([A-Za-z0-9](?:[A-Za-z0-9-]{0,38}))/([A-Za-z0-9._-]{1,100}?)(?:\.git)?(?:/(?:tree|blob)/[A-Za-z0-9._/-]{1,200})?/?$`)

// githubRepo is the canonical https://github.com/<owner>/<repo> of a
// source label, or "" when it isn't one.
func githubRepo(label string) string {
	m := githubRE.FindStringSubmatch(strings.TrimSpace(label))
	if m == nil || m[2] == "." || m[2] == ".." {
		return ""
	}
	return "https://github.com/" + m[1] + "/" + m[2]
}

type notesEntry struct {
	at       time.Time
	ok       bool
	releases []Release // newest first, as GitHub lists them
	err      error
}

// notesCache fetches and keeps each repository's releases.
type notesCache struct {
	base   string
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*notesEntry
	flight  map[string]chan struct{}
}

func newNotesCache(base string, client *http.Client) *notesCache {
	if base == "" {
		base = "https://api.github.com"
	}
	if client == nil {
		client = notesClient()
	}
	return &notesCache{base: strings.TrimSuffix(base, "/"), client: client, entries: map[string]*notesEntry{}, flight: map[string]chan struct{}{}}
}

// notesClient talks to GitHub only: no proxy from the environment, no
// redirects, bounded in time and size.
func notesClient() *http.Client {
	return &http.Client{
		Timeout: notesTimeout,
		Transport: &http.Transport{
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  notesTimeout,
			MaxResponseHeaderBytes: 32 << 10,
			ForceAttemptHTTP2:      true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("no redirects") },
	}
}

// errNoNotes: GitHub has no releases for the repository (or no such
// repository).
var errNoNotes = errors.New("updates: no releases")

// releases returns repo's releases, from the cache or GitHub.
func (c *notesCache) releases(ctx context.Context, repo string) ([]Release, time.Time, error) {
	for {
		c.mu.Lock()
		if e := c.entries[repo]; e != nil {
			ttl := notesTTL
			if !e.ok {
				ttl = notesFailTTL
			}
			if c.now().Sub(e.at) < ttl {
				c.mu.Unlock()
				return e.releases, e.at, e.err
			}
		}
		if wait, busy := c.flight[repo]; busy {
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, time.Time{}, ctx.Err()
			}
		}
		done := make(chan struct{})
		c.flight[repo] = done
		c.mu.Unlock()

		rels, err := c.fetch(ctx, repo)
		e := &notesEntry{at: c.now(), ok: err == nil || errors.Is(err, errNoNotes), releases: rels, err: err}
		c.mu.Lock()
		if ctx.Err() == nil {
			c.entries[repo] = e
		}
		delete(c.flight, repo)
		close(done)
		c.mu.Unlock()
		return rels, e.at, err
	}
}

// cached is what the cache holds for repo, without asking GitHub.
func (c *notesCache) cached(repo string) ([]Release, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[repo]
	if e == nil || !e.ok {
		return nil, false
	}
	return e.releases, true
}

func (c *notesCache) fetch(ctx context.Context, repo string) ([]Release, error) {
	path := strings.TrimPrefix(repo, "https://github.com/")
	owner, name, _ := strings.Cut(path, "/")
	u := c.base + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/releases?per_page=30"
	ctx, cancel := context.WithTimeout(ctx, notesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "chadash/"+buildinfo.Version+" (release notes; +https://dash.cha.sh)")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("updates: GitHub: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errNoNotes
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, errRateLimited
	default:
		return nil, fmt.Errorf("updates: GitHub answered %d", resp.StatusCode)
	}
	var in []struct {
		Tag        string    `json:"tag_name"`
		Name       string    `json:"name"`
		Body       string    `json:"body"`
		HTMLURL    string    `json:"html_url"`
		Draft      bool      `json:"draft"`
		Prerelease bool      `json:"prerelease"`
		Published  time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, notesMaxBytes)).Decode(&in); err != nil {
		return nil, fmt.Errorf("updates: GitHub's answer: %w", err)
	}
	out := make([]Release, 0, len(in))
	for _, r := range in {
		if r.Draft || r.Prerelease || r.Tag == "" {
			continue
		}
		rel := Release{Tag: plainLine(r.Tag, 100), Name: plainLine(r.Name, 200), Published: r.Published}
		if inRepo(r.HTMLURL, repo) {
			rel.URL = r.HTMLURL
		}
		rel.Notes, rel.Truncated = SanitizeNotes(r.Body, repo)
		out = append(out, rel)
	}
	if len(out) == 0 {
		return nil, errNoNotes
	}
	return out, nil
}

// errRateLimited: GitHub refused for its rate limit.
var errRateLimited = errors.New("updates: GitHub's rate limit")

// since picks the releases newer than from, newest first, at most
// MaxReleases; when from isn't a version, the newest few.
func since(rels []Release, from string) ([]Release, bool) {
	v, ok := parseVersion(from)
	if !ok {
		return rels[:min(len(rels), 3)], false
	}
	var out []Release
	for _, r := range rels {
		if rv, ok := parseVersion(r.Tag); ok && compareVersion(rv, v) > 0 {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b Release) int {
		av, _ := parseVersion(a.Tag)
		bv, _ := parseVersion(b.Tag)
		return compareVersion(bv, av)
	})
	return out[:min(len(out), MaxReleases)], true
}

// risk is an image's risk from its running version to the newest release
// the cache holds, or unknown.
func (c *notesCache) risk(repo, from string) string {
	rels, ok := c.cached(repo)
	if !ok || from == "" {
		return RiskUnknown
	}
	newer, ok := since(rels, from)
	if !ok || len(newer) == 0 {
		return RiskUnknown
	}
	return Risk(from, newer[0].Tag)
}

// inRepo reports whether u is an https link into repo
// (https://github.com/<owner>/<repo>, or below it).
func inRepo(u, repo string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.User != nil || !strings.EqualFold(p.Host, "github.com") {
		return false
	}
	want := strings.TrimPrefix(repo, "https://github.com")
	path := p.EscapedPath()
	if strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") {
		return false
	}
	return strings.EqualFold(path, want) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(want)+"/")
}

var (
	htmlTagRE  = regexp.MustCompile(`(?s)<!--.*?-->|</?[A-Za-z][^<>]*>`)
	mdImageRE  = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLinkRE   = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]*)[^)]*\)`)
	bareLinkRE = regexp.MustCompile(`(?i)\b(?:https?|ftp|javascript|data|file|mailto):[^\s<>()\[\]]*|\bwww\.[^\s<>()\[\]]+`)
)

// SanitizeNotes turns a release's Markdown into the text the web shows:
// HTML and images removed, every link outside repo replaced by "link
// removed", a Markdown link into repo written as "text (url)", control
// characters dropped, at most MaxNoteBytes.
func SanitizeNotes(body, repo string) (string, bool) {
	s := strings.ReplaceAll(body, "\r\n", "\n")
	s = htmlTagRE.ReplaceAllString(s, "")
	s = mdImageRE.ReplaceAllString(s, "$1")
	s = mdLinkRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdLinkRE.FindStringSubmatch(m)
		text, u := strings.TrimSpace(sub[1]), sub[2]
		if inRepo(u, repo) {
			if text == "" || text == u {
				return u
			}
			return text + " (" + u + ")"
		}
		if text == "" {
			return "(link removed)"
		}
		return text + " (link removed)"
	})
	s = bareLinkRE.ReplaceAllStringFunc(s, func(u string) string {
		if inRepo(strings.TrimRight(u, ".,;:!?'\""), repo) {
			return u
		}
		return "(link removed)"
	})
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || r == '\u202e' || r == '\u202d' || r == '\u2066' || r == '\u2067' || r == '\u2068' || r == '\u2069' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) <= MaxNoteBytes {
		return s, false
	}
	n := MaxNoteBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n]), true
}

// plainLine is one line of untrusted text: no HTML, no control
// characters, at most n bytes.
func plainLine(s string, n int) string {
	s, _ = SanitizeNotes(s, "")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// NotesEnabled reads the setting.
func (s *Service) NotesEnabled(ctx context.Context) (bool, error) {
	var raw string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, NotesKey).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var v struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return false, err
	}
	return v.Enabled, nil
}

// SetNotesEnabled writes the setting.
func (s *Service) SetNotesEnabled(ctx context.Context, on bool) error {
	b, _ := json.Marshal(map[string]bool{"enabled": on})
	_, err := s.st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, NotesKey, string(b), s.now().UnixMilli())
	return err
}

// Errors from ReleaseNotes.
var (
	ErrNotesOff = errors.New("updates: release notes are off")
	ErrNoSource = errors.New("updates: no GitHub source")
)

// ReleaseNotes reads a container's release notes: the releases of its
// image's GitHub repository newer than the version it runs.
func (s *Service) ReleaseNotes(ctx context.Context, resource string) (Notes, error) {
	on, err := s.NotesEnabled(ctx)
	if err != nil {
		return Notes{}, err
	}
	if !on {
		return Notes{}, ErrNotesOff
	}
	r, err := s.row(ctx, resource)
	if err != nil {
		return Notes{}, err
	}
	if r.Source == "" {
		return Notes{}, ErrNoSource
	}
	rels, at, err := s.notes.releases(ctx, r.Source)
	if errors.Is(err, errNoNotes) {
		return Notes{Repo: r.Source, From: r.Version, Releases: []Release{}, FetchedAt: at}, nil
	}
	if err != nil {
		return Notes{}, err
	}
	picked, newer := since(rels, r.Version)
	if picked == nil {
		picked = []Release{}
	}
	return Notes{Repo: r.Source, From: r.Version, Releases: picked, Newer: newer, FetchedAt: at}, nil
}
