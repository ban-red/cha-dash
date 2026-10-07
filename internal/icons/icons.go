// Package icons is the hub's icon cache. Tiles can't load an image from
// another origin under the hub's CSP (default-src 'self'), so they showed
// monograms. The hub fetches each icon itself, checks it, keeps it in
// SQLite (the icons table, migration 00013) and serves it same-origin:
//
//	GET /api/icons        {"by_icon": {<tile icon URL>: path}, "by_origin": {<origin>: path},   (signed in)
//	                      "by_instance": {<instance id>: path}}; also asks for a refresh
//	GET /api/icons/{id}   the image, with a long private cache, an ETag and a sandbox CSP        (signed in)
//
// A path is "/api/icons/<id>?v=<etag>", so a refreshed icon gets a new URL.
//
// What gets fetched is decided here, from what the owner configured, never
// from a request: a tile's icon when it's an http(s) URL; otherwise the
// favicon of the tile's link's origin; and the favicon of each integration
// instance's url. Homepage-style icon names ("sonarr.png", "mdi-…") name a
// third-party icon set, so they're not fetched; their tile falls back to its
// link's favicon, then to the monogram.
//
// The fetcher's rules (SECURITY.md has them for users):
//   - http and https only, no credentials in the URL, no proxy;
//   - every connection's address is checked when it's dialled (so a DNS
//     answer can't swap one in later): loopback, link-local (the cloud
//     metadata address among them), unspecified, multicast and the known
//     metadata addresses are refused; private LAN addresses are allowed,
//     because that's where a homelab's services live;
//   - at most one redirect, to the same host;
//   - 5 s to connect, 10 s in all;
//   - a 200 with at most 256 KiB, whose bytes are a PNG, an ICO, a WebP or
//     an SVG (the declared type must agree, or be generic); an SVG with a
//     script, an event handler, foreignObject, a DOCTYPE or entity, or an
//     external reference is refused rather than cleaned;
//   - every icon is served with its checked type, nosniff and
//     "Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox",
//     and the web only ever shows it in an <img>, where scripts never run.
//
// Icons are fetched when first seen, re-fetched weekly, retried after a
// failure every six hours, and forgotten 30 days after nothing refers to
// them.
package icons

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/store"
)

// Limits and schedule.
const (
	MaxBytes      = 256 << 10
	dialTimeout   = 5 * time.Second
	fetchTimeout  = 10 * time.Second
	refreshAfter  = 7 * 24 * time.Hour
	retryAfter    = 6 * time.Hour
	forgetAfter   = 30 * 24 * time.Hour
	syncEvery     = time.Hour
	firstSync     = 30 * time.Second
	perPass       = 32 // fetches at most per pass; the rest wait for the next
	parallelFetch = 4
)

// The icon types the hub serves.
const (
	TypePNG  = "image/png"
	TypeSVG  = "image/svg+xml"
	TypeICO  = "image/x-icon"
	TypeWebP = "image/webp"
)

// faviconPaths are tried in order for an origin.
var faviconPaths = []string{"/favicon.ico", "/favicon.svg", "/apple-touch-icon.png", "/favicon.png"}

// Service fetches, stores and serves icons.
type Service struct {
	st    *store.Store
	log   *slog.Logger
	now   func() time.Time
	fetch bool

	// allowed decides whether an address may be dialled (tests widen it to
	// loopback for httptest).
	allowed func(netip.Addr) bool
	client  *http.Client

	kick chan struct{}
	mu   sync.Mutex // one pass at a time
}

// Options configure a Service.
type Options struct {
	Store *store.Store
	Log   *slog.Logger
	Now   func() time.Time
	// Fetch turns the fetcher on. Off (demo mode), the routes serve what's
	// stored and nothing reaches the network.
	Fetch bool
}

// New returns a Service. Nothing is fetched until Run or Sync.
func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Service{st: o.Store, log: o.Log, now: o.Now, fetch: o.Fetch, allowed: Allowed, kick: make(chan struct{}, 1)}
	s.client = s.newClient()
	return s
}

// Kick asks for a pass soon (Run picks it up); it never blocks.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run syncs half a minute after start (or at the first Kick), then every
// hour and after each Kick, until ctx ends. The delay keeps a starting hub
// (and short-lived test hubs) off the network until someone looks.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(syncEvery)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return
	case <-time.After(firstSync):
	case <-s.kick:
	}
	for {
		if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("icons: sync failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
			// A burst of page loads asks once.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Sources

// Sources are what the owner configured, mapped to the icon source each
// needs: tiles' icon URLs (kind url), and the origins of tiles' links and
// instances' URLs (kind origin).
type Sources struct {
	ByIcon     map[string]string // tile icon → source
	ByOrigin   map[string]string // origin → source (the origin itself)
	ByInstance map[string]string // instance id → source (its origin)
}

func (s Sources) all() map[string]string {
	out := map[string]string{}
	for _, src := range s.ByIcon {
		out[src] = kindURL
	}
	for _, src := range s.ByOrigin {
		out[src] = kindOrigin
	}
	for _, src := range s.ByInstance {
		out[src] = kindOrigin
	}
	return out
}

const (
	kindURL    = "url"
	kindOrigin = "origin"
)

// IconURL reports whether a tile's icon is an http(s) URL the hub would
// fetch (and returns it cleaned), rather than an icon name.
func IconURL(icon string) (string, bool) {
	u, ok := parseHTTP(icon)
	if !ok {
		return "", false
	}
	return u.String(), true
}

// Origin is the scheme://host[:port] of an http(s) URL, as a browser
// writes it (the web looks icons up by a link's origin): host lower-cased,
// the scheme's default port left out.
func Origin(raw string) (string, bool) {
	u, ok := parseHTTP(raw)
	if !ok {
		return "", false
	}
	host := u.Host
	if p := u.Port(); u.Scheme == "http" && p == "80" || u.Scheme == "https" && p == "443" {
		host = strings.TrimSuffix(host, ":"+p)
	}
	return u.Scheme + "://" + host, true
}

func parseHTTP(raw string) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return nil, false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u, true
}

// tileRefs is what a tile says about its icon.
type tileRefs struct {
	Icon string `json:"icon"`
	Href string `json:"href"`
}

// ReadSources reads every board's latest tiles and every instance's url.
func (s *Service) ReadSources(ctx context.Context) (Sources, error) {
	q := s.st.Reader()
	src := Sources{ByIcon: map[string]string{}, ByOrigin: map[string]string{}, ByInstance: map[string]string{}}
	bs, err := boards.ReadAll(ctx, q)
	if err != nil {
		return src, fmt.Errorf("icons: boards: %w", err)
	}
	for _, b := range bs {
		if len(b.Layout.Tiles) == 0 {
			continue
		}
		var tiles map[string]tileRefs
		if json.Unmarshal(b.Layout.Tiles, &tiles) != nil {
			continue
		}
		for _, t := range tiles {
			if u, ok := IconURL(t.Icon); ok {
				src.ByIcon[t.Icon] = u
			}
			if o, ok := Origin(t.Href); ok {
				src.ByOrigin[o] = o
			}
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT id, config FROM integration_instances`)
	if err != nil {
		return src, fmt.Errorf("icons: instances: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, cfg string
		if err := rows.Scan(&id, &cfg); err != nil {
			return src, err
		}
		var c struct {
			URL string `json:"url"`
		}
		if json.Unmarshal([]byte(cfg), &c) != nil {
			continue
		}
		if o, ok := Origin(c.URL); ok {
			src.ByInstance[id] = o
		}
	}
	return src, rows.Err()
}

// ---------------------------------------------------------------------------
// The pass

type row struct {
	id, source, kind string
	etag             string
	hasBody          bool
	fetchedAt        sql.NullInt64
	checkedAt        int64
}

// Sync records the sources in use, fetches what's due (when fetching is on)
// and forgets icons nothing has referred to for 30 days.
func (s *Service) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := s.ReadSources(ctx)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	want := src.all()
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, source := range sortedKeys(want) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO icons (id, source, kind, seen_at, created_at) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (source) DO UPDATE SET seen_at = excluded.seen_at`,
				newID(), source, want[source], now, now); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM icons WHERE seen_at < ?`, now-forgetAfter.Milliseconds())
		return err
	})
	if err != nil {
		return fmt.Errorf("icons: recording sources: %w", err)
	}
	if !s.fetch {
		return nil
	}
	due, err := s.due(ctx, now)
	if err != nil {
		return err
	}
	work := make(chan row)
	var wg sync.WaitGroup
	for range parallelFetch {
		wg.Go(func() {
			for r := range work {
				s.refresh(ctx, r)
			}
		})
	}
	for _, r := range due {
		select {
		case work <- r:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
	return ctx.Err()
}

// due lists rows to fetch: never fetched or checked, failed more than six
// hours ago, or fetched more than a week ago. Oldest attempt first.
func (s *Service) due(ctx context.Context, now int64) ([]row, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, source, kind, etag, body IS NOT NULL, fetched_at, checked_at FROM icons
		WHERE checked_at = 0
		   OR (body IS NULL AND checked_at < ?)
		   OR (body IS NOT NULL AND COALESCE(fetched_at, 0) < ? AND checked_at < ?)
		ORDER BY checked_at, id LIMIT ?`,
		now-retryAfter.Milliseconds(), now-refreshAfter.Milliseconds(), now-retryAfter.Milliseconds(), perPass)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.source, &r.kind, &r.etag, &r.hasBody, &r.fetchedAt, &r.checkedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// refresh fetches one row's icon and stores the outcome. A failed refresh
// of an icon that has a body keeps the body: a service that's down for a
// day keeps its icon.
func (s *Service) refresh(ctx context.Context, r row) {
	ctype, body, err := s.fetchSource(ctx, r.kind, r.source)
	now := s.now().UnixMilli()
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.log.Debug("icons: fetch failed", "source", r.source, "err", err)
		_, werr := s.st.Writer().ExecContext(ctx, `UPDATE icons SET error = ?, checked_at = ? WHERE id = ?`, truncate(err.Error(), 300), now, r.id)
		if werr != nil {
			s.log.Warn("icons: recording a failed fetch", "err", werr)
		}
		return
	}
	sum := sha256.Sum256(body)
	etag := hex.EncodeToString(sum[:8])
	if _, err := s.st.Writer().ExecContext(ctx, `UPDATE icons SET content_type = ?, body = ?, etag = ?, error = '', fetched_at = ?, checked_at = ? WHERE id = ?`,
		ctype, body, etag, now, now, r.id); err != nil {
		s.log.Warn("icons: storing an icon", "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// fetchSource fetches a url source as it is, or an origin's favicon by
// trying the usual paths in turn.
func (s *Service) fetchSource(ctx context.Context, kind, source string) (string, []byte, error) {
	if kind == kindURL {
		return s.Fetch(ctx, source)
	}
	var last error
	for _, p := range faviconPaths {
		ctype, body, err := s.Fetch(ctx, source+p)
		if err == nil {
			return ctype, body, nil
		}
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		last = err
		var re *refusedError
		if errors.As(err, &re) && re.final {
			break // the address is refused: no other path will do better
		}
	}
	return "", nil, last
}

func newID() string { return "ic_" + strings.ToLower(rand.Text()) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ---------------------------------------------------------------------------
// Fetching

// refusedError is a fetch the rules refuse. final: no other path on the
// same origin can succeed (the address itself is refused).
type refusedError struct {
	msg   string
	final bool
}

func (e *refusedError) Error() string { return e.msg }

func refused(format string, args ...any) error {
	return &refusedError{msg: fmt.Sprintf(format, args...)}
}

// metadataAddrs are cloud metadata services outside the link-local range.
var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("fd00:ec2::254"),   // AWS over IPv6
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
	netip.MustParseAddr("192.0.0.192"),     // Oracle Cloud
}

var thisNetwork = netip.MustParsePrefix("0.0.0.0/8")

// Allowed is the fetcher's address rule: anything but loopback, link-local
// (169.254.169.254 among it), unspecified, "this network", multicast,
// broadcast and the known metadata addresses. Private LAN ranges are
// allowed: that's where a homelab's services are.
func Allowed(a netip.Addr) bool {
	a = a.Unmap()
	switch {
	case !a.IsValid(), a.IsLoopback(), a.IsUnspecified(), a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast(),
		a.IsInterfaceLocalMulticast(), a.IsMulticast(), a == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		return false
	case a.Is4() && thisNetwork.Contains(a):
		return false
	case slices.Contains(metadataAddrs, a):
		return false
	}
	// NAT64 and 6to4 carry an IPv4 address, which gets the same rule: a
	// translator would connect to it.
	for _, v4 := range httpx.EmbeddedIPv4(a) {
		if !Allowed(v4) {
			return false
		}
	}
	return true
}

func (s *Service) newClient() *http.Client {
	d := &net.Dialer{
		Timeout: dialTimeout,
		// Checked after the name resolved, on the address actually dialled,
		// so a rebinding DNS answer can't slip one past.
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return &refusedError{msg: "the address couldn't be checked", final: true}
			}
			if !s.allowed(ap.Addr()) {
				return &refusedError{msg: fmt.Sprintf("%s is an address icons are never fetched from (loopback, link-local or metadata)", ap.Addr()), final: true}
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            d.DialContext,
		TLSHandshakeTimeout:    dialTimeout,
		ResponseHeaderTimeout:  fetchTimeout,
		MaxResponseHeaderBytes: 32 << 10,
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      true,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 1 {
				return refused("more than one redirect")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return refused("a redirect to another scheme")
			}
			if req.URL.User != nil || !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
				return refused("a redirect to another host")
			}
			return nil
		},
	}
}

// acceptedTypes are the Content-Types an icon may be declared as: the four
// image types, and the generic ones servers use for .ico and .svg files.
var acceptedTypes = []string{
	TypePNG, TypeSVG, TypeICO, TypeWebP, "image/vnd.microsoft.icon", "image/ico", "image/icon", "image/x-ico",
	"application/octet-stream", "application/xml", "text/xml", "text/plain", "",
}

// Fetch fetches one URL under the fetcher's rules and returns the checked
// type and bytes.
func (s *Service) Fetch(ctx context.Context, raw string) (string, []byte, error) {
	u, ok := parseHTTP(raw)
	if !ok {
		return "", nil, &refusedError{msg: "only http(s) URLs without credentials are fetched", final: true}
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, &refusedError{msg: "the URL isn't valid", final: true}
	}
	req.Header.Set("Accept", "image/png,image/svg+xml,image/x-icon,image/webp;q=0.9,*/*;q=0.1")
	req.Header.Set("User-Agent", "chadash/"+buildinfo.Version+" (icon cache; +https://dash.cha.sh)")
	resp, err := s.client.Do(req)
	if err != nil {
		var re *refusedError
		if errors.As(err, &re) {
			return "", nil, re
		}
		return "", nil, fmt.Errorf("fetching the icon failed: %s", cleanErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxBytes {
		return "", nil, refused("larger than %d KiB", MaxBytes>>10)
	}
	declared := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if !slices.Contains(acceptedTypes, declared) {
		return "", nil, refused("declared as %s, not an icon type", truncate(declared, 60))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("reading the icon failed: %s", cleanErr(err))
	}
	if len(body) > MaxBytes {
		return "", nil, refused("larger than %d KiB", MaxBytes>>10)
	}
	ctype, err := Check(body)
	if err != nil {
		return "", nil, err
	}
	if declared != "" && strings.HasPrefix(declared, "image/") && !compatible(declared, ctype) {
		return "", nil, refused("declared as %s but it's %s", declared, ctype)
	}
	return ctype, body, nil
}

// compatible: a declared image type agrees with what the bytes are.
func compatible(declared, actual string) bool {
	switch actual {
	case TypeICO:
		return declared == TypeICO || declared == "image/vnd.microsoft.icon" || declared == "image/ico" || declared == "image/icon" || declared == "image/x-ico" || declared == TypePNG
	case TypePNG:
		// A favicon.ico that's really a PNG is common, and harmless.
		return declared == TypePNG || declared == TypeICO || declared == "image/vnd.microsoft.icon"
	}
	return declared == actual
}

// cleanErr drops the URL from a client error: the message is stored and
// shown, and a URL could carry anything.
func cleanErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timed out"
	}
	return truncate(err.Error(), 200)
}

// Check returns the icon type of b: PNG, ICO, WebP or a safe SVG. Anything
// else is refused.
func Check(b []byte) (string, error) {
	switch {
	case len(b) == 0:
		return "", refused("empty")
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return TypePNG, nil
	case len(b) >= 6 && b[0] == 0 && b[1] == 0 && (b[2] == 1 || b[2] == 2) && b[3] == 0 && (b[4] != 0 || b[5] != 0):
		return TypeICO, nil
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return TypeWebP, nil
	}
	return TypeSVG, checkSVG(b)
}

// SVG namespaces the checker knows. Elements must be SVG (or in no
// namespace, for an icon without xmlns); Inkscape's and Sodipodi's are
// inert editor data, and anything goes inside <metadata> but XHTML.
const (
	nsSVG      = "http://www.w3.org/2000/svg"
	nsXLink    = "http://www.w3.org/1999/xlink"
	nsXML      = "http://www.w3.org/XML/1998/namespace"
	nsXHTML    = "http://www.w3.org/1999/xhtml"
	nsInkscape = "http://www.inkscape.org/namespaces/inkscape"
	nsSodipodi = "http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd"
)

// svgElements are the elements an icon may use: shapes, text, paint
// servers, clipping and masking, reuse by fragment, and filters that read
// nothing from outside. No script, no foreignObject, no links, no
// animation (<animate>, <set> and friends can rewrite an href), no feImage.
var svgElements = setOf(
	"svg", "g", "defs", "symbol", "use", "switch", "title", "desc", "metadata", "style",
	"path", "circle", "ellipse", "line", "polyline", "polygon", "rect",
	"text", "tspan", "textPath",
	"linearGradient", "radialGradient", "stop", "pattern", "clipPath", "mask", "marker",
	"image", // only with an embedded raster (data:image/png and the like)
	"filter", "feBlend", "feColorMatrix", "feComponentTransfer", "feComposite", "feConvolveMatrix",
	"feDiffuseLighting", "feDisplacementMap", "feDistantLight", "feDropShadow", "feFlood",
	"feFuncA", "feFuncB", "feFuncG", "feFuncR", "feGaussianBlur", "feMerge", "feMergeNode",
	"feMorphology", "feOffset", "fePointLight", "feSpecularLighting", "feSpotLight", "feTile", "feTurbulence",
)

// svgAttrs are the attributes (without a namespace) an icon may use:
// geometry, presentation, text, paint-server and filter parameters. Plus
// aria-* and data-*. Every value is checked for url() and javascript:
// whatever its name.
var svgAttrs = setOf(
	"id", "class", "style", "lang", "version", "baseProfile", "viewBox", "preserveAspectRatio", "zoomAndPan",
	"width", "height", "x", "y", "x1", "y1", "x2", "y2", "cx", "cy", "r", "rx", "ry", "fx", "fy", "fr",
	"d", "points", "pathLength", "transform", "transform-origin", "transform-box", "href",
	"dx", "dy", "rotate", "textLength", "lengthAdjust", "startOffset", "method", "spacing", "side",
	"gradientUnits", "gradientTransform", "spreadMethod", "offset",
	"patternUnits", "patternContentUnits", "patternTransform", "clipPathUnits", "maskUnits", "maskContentUnits",
	"refX", "refY", "markerUnits", "markerWidth", "markerHeight", "orient",
	"requiredFeatures", "requiredExtensions", "systemLanguage", "role", "focusable", "tabindex", "type", "media",
	"fill", "fill-opacity", "fill-rule", "stroke", "stroke-width", "stroke-opacity", "stroke-linecap",
	"stroke-linejoin", "stroke-miterlimit", "stroke-dasharray", "stroke-dashoffset", "opacity",
	"color", "color-interpolation", "color-interpolation-filters", "color-rendering", "color-profile",
	"display", "visibility", "overflow", "clip", "clip-path", "clip-rule", "mask", "mask-type", "filter",
	"flood-color", "flood-opacity", "lighting-color", "stop-color", "stop-opacity",
	"marker", "marker-start", "marker-mid", "marker-end", "paint-order", "vector-effect",
	"shape-rendering", "image-rendering", "text-rendering", "isolation", "mix-blend-mode", "pointer-events",
	"enable-background", "font", "font-family", "font-size", "font-size-adjust", "font-stretch", "font-style",
	"font-variant", "font-weight", "letter-spacing", "word-spacing", "kerning", "text-anchor", "text-decoration",
	"dominant-baseline", "alignment-baseline", "baseline-shift", "writing-mode", "direction", "unicode-bidi",
	"glyph-orientation-horizontal", "glyph-orientation-vertical",
	"filterUnits", "primitiveUnits", "filterRes", "in", "in2", "result", "stdDeviation", "edgeMode", "mode",
	"operator", "k1", "k2", "k3", "k4", "values", "tableValues", "slope", "intercept", "amplitude", "exponent",
	"scale", "xChannelSelector", "yChannelSelector", "radius", "order", "kernelMatrix", "divisor", "bias",
	"targetX", "targetY", "preserveAlpha", "kernelUnitLength", "surfaceScale", "diffuseConstant",
	"specularConstant", "specularExponent", "limitingConeAngle", "azimuth", "elevation",
	"pointsAtX", "pointsAtY", "pointsAtZ", "z", "baseFrequency", "numOctaves", "seed", "stitchTiles",
)

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// embeddedRaster is the one non-fragment href allowed, on <image>: a
// raster image embedded in the file.
var embeddedRaster = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/=\s]*$`)

// checkSVG parses b as XML and refuses anything outside the allowlists:
// an element or attribute not on them, an event handler (on*), a reference
// that isn't to a fragment of the file itself (#id) in href or url(), a
// javascript: URL (entities decoded), CSS that can load something
// (@import, image-set, escapes), a DOCTYPE or entity, a processing
// instruction other than the XML declaration, or anything that isn't
// well-formed. Icons are also served with a sandboxing CSP (iconCSP).
func checkSVG(b []byte) error {
	t := bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	t = bytes.TrimSpace(t)
	lower := strings.ToLower(string(t))
	if !(strings.HasPrefix(lower, "<svg") || strings.HasPrefix(lower, "<?xml") || strings.HasPrefix(lower, "<!--")) || !strings.Contains(lower, "<svg") {
		return refused("not a PNG, ICO, WebP or SVG image")
	}
	dec := xml.NewDecoder(bytes.NewReader(t))
	dec.Strict = true // and no Entity map: only XML's five, so &nbsp; or a declared one fails
	var (
		stack    []string // open elements
		inMeta   int      // depth inside <metadata>, 0 outside
		inStyle  bool
		sawRoot  bool
		sawFirst bool // any token yet: the XML declaration must come first
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return refused("an SVG that isn't well-formed XML is refused")
		}
		first := !sawFirst
		sawFirst = true
		switch tok := tok.(type) {
		case xml.StartElement:
			name := tok.Name.Local
			if !sawRoot {
				if name != "svg" || (tok.Name.Space != "" && tok.Name.Space != nsSVG) {
					return refused("not a PNG, ICO, WebP or SVG image")
				}
				sawRoot = true
			} else if len(stack) == 0 {
				return refused("an SVG with more than one root element is refused")
			}
			if err := checkSVGElement(tok, inMeta > 0); err != nil {
				return err
			}
			stack = append(stack, name)
			if inMeta > 0 || (name == "metadata" && isSVGSpace(tok.Name.Space)) {
				inMeta++
			}
			inStyle = name == "style" && isSVGSpace(tok.Name.Space)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if inMeta > 0 {
				inMeta--
			}
			inStyle = false
		case xml.CharData:
			if inStyle {
				if err := checkCSS(string(tok)); err != nil {
					return err
				}
			} else if len(stack) == 0 && len(bytes.TrimSpace(tok)) > 0 {
				return refused("an SVG with text outside its root element is refused")
			}
		case xml.ProcInst:
			if tok.Target != "xml" || !first {
				return refused("an SVG with a processing instruction (<?%s) is refused", tok.Target)
			}
		case xml.Directive:
			return refused("an SVG with a DOCTYPE or entity declaration is refused")
		case xml.Comment:
		}
	}
	if !sawRoot {
		return refused("not a PNG, ICO, WebP or SVG image")
	}
	return nil
}

func isSVGSpace(ns string) bool { return ns == "" || ns == nsSVG }

// checkSVGElement checks one element and its attributes. Inside <metadata>,
// elements of other vocabularies (RDF, Dublin Core) are inert and allowed,
// but not XHTML's; so are Inkscape's and Sodipodi's anywhere. An inert
// element's attributes needn't be on the allowlist, but their values are
// checked all the same.
func checkSVGElement(el xml.StartElement, inMeta bool) error {
	name := el.Name.Local
	inert := true
	switch {
	case isSVGSpace(el.Name.Space):
		inert = false
		if !svgElements[name] {
			return refused("an SVG with <%s> is refused", clipName(name))
		}
	case el.Name.Space == nsInkscape || el.Name.Space == nsSodipodi || el.Name.Space == "inkscape" || el.Name.Space == "sodipodi":
	case inMeta && el.Name.Space != nsXHTML:
	default:
		return refused("an SVG with <%s> from another vocabulary is refused", clipName(name))
	}
	for _, a := range el.Attr {
		if err := checkSVGAttr(name, inert, a); err != nil {
			return err
		}
	}
	return nil
}

func checkSVGAttr(element string, inert bool, a xml.Attr) error {
	local := a.Name.Local
	if strings.HasPrefix(strings.ToLower(local), "on") {
		return refused("an SVG with an event handler is refused")
	}
	switch a.Name.Space {
	case "xmlns": // a namespace declaration
		return nil
	case "":
		switch {
		case inert:
		case local == "xmlns", svgAttrs[local], strings.HasPrefix(local, "aria-"), strings.HasPrefix(local, "data-"):
		default:
			return refused("an SVG with a %s attribute is refused", clipName(local))
		}
	case nsXLink, "xlink":
		if local != "href" && local != "title" && local != "type" && local != "role" && local != "arcrole" && local != "show" && local != "actuate" {
			return refused("an SVG with xlink:%s is refused", clipName(local))
		}
	case nsXML, "xml":
		if local != "space" && local != "lang" {
			return refused("an SVG with xml:%s is refused", clipName(local))
		}
	case nsInkscape, nsSodipodi, "inkscape", "sodipodi":
	default:
		if inert {
			break // RDF's about and resource on metadata, say
		}
		return refused("an SVG with an attribute from another vocabulary (%s) is refused", clipName(local))
	}
	v := a.Value // encoding/xml has decoded entities and character references
	if hasScriptURL(v) {
		return refused("an SVG with a javascript: URL is refused")
	}
	if local == "href" {
		ref := strings.Trim(v, " \t\n\r\f")
		switch {
		case strings.HasPrefix(ref, "#"):
		case element == "image" && embeddedRaster.MatchString(ref):
		default:
			return refused("an SVG with an external reference is refused")
		}
	}
	return checkCSS(v)
}

// hasScriptURL reports whether v holds a javascript: (or vbscript:) URL,
// ignoring the whitespace and control characters a URL parser drops.
func hasScriptURL(v string) bool {
	var b strings.Builder
	for _, r := range v {
		if r > ' ' && r != 0x7f {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	s := b.String()
	return strings.Contains(s, "javascript:") || strings.Contains(s, "vbscript:")
}

// checkCSS checks CSS (a style attribute, a <style> element, or a
// presentation attribute, which is parsed as CSS): every url() must name a
// fragment of the file, and nothing else may load: no @import, no
// image-set(), and no backslash escapes, which can spell either.
func checkCSS(css string) error {
	if hasScriptURL(css) {
		return refused("an SVG with a javascript: URL is refused")
	}
	lower := strings.ToLower(css)
	for _, bad := range []string{"@import", "image-set", "@font-face", "expression(", "-moz-binding", "behavior:"} {
		if strings.Contains(lower, bad) {
			return refused("an SVG with %s in its CSS is refused", bad)
		}
	}
	if strings.ContainsRune(css, '\\') {
		return refused("an SVG with a CSS escape is refused")
	}
	for rest := lower; ; {
		i := strings.Index(rest, "url(")
		if i < 0 {
			return nil
		}
		rest = rest[i+len("url("):]
		arg := strings.TrimLeft(rest, " \t\n\r\f\"'")
		if !strings.HasPrefix(arg, "#") {
			return refused("an SVG with an external reference is refused")
		}
	}
}

// clipName keeps a refusal's sentence short whatever a file names.
func clipName(s string) string { return truncate(s, 40) }
