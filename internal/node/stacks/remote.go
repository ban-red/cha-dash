package stacks

// Git remotes a stack follows (ADR-0021): stack.remote.check lists a
// remote's refs without fetching, and stack.fetch does a shallow,
// single-branch fetch into a cache and reads one file at the tip. The
// remote is an input, never history to merge: nothing here touches the
// stacks root. The hub sends the credential with each call; it lives only
// in memory, is never logged, and is scrubbed from every error.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/crypto/ssh"

	"dash.cha.sh/internal/stackproto"
)

// Limits on a remote call, and on the caches.
const (
	DefaultFetchTimeout = 60 * time.Second
	DefaultMaxFile      = 1 << 20
	// DefaultMaxFetch is how many bytes one fetch may write to a cache.
	// A remote that sends more is cut off, and its cache thrown away.
	DefaultMaxFetch = 64 << 20
	// cacheMaxIdle is how long a cache no fetch used is kept.
	cacheMaxIdle = 14 * 24 * time.Hour
	// pruneEvery is how often fetches look for idle caches.
	pruneEvery = time.Hour
)

// gitClient is the network side of go-git: listing a remote's refs and
// fetching into a repository. Tests replace it to stall or fail on cue.
type gitClient interface {
	List(ctx context.Context, url string, auth transport.AuthMethod) ([]*plumbing.Reference, error)
	Fetch(ctx context.Context, r *git.Repository, o *git.FetchOptions) error
}

type goGit struct{}

func (goGit) List(ctx context.Context, u string, auth transport.AuthMethod) ([]*plumbing.Reference, error) {
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{u}})
	return rem.ListContext(ctx, &git.ListOptions{Auth: auth})
}

func (goGit) Fetch(ctx context.Context, r *git.Repository, o *git.FetchOptions) error {
	return r.FetchContext(ctx, o)
}

// remotes is the module's state for git remotes: settings and one lock
// per cache in use.
type remotes struct {
	cacheDir  string
	allow     []string
	timeout   time.Duration
	maxFile   int64
	maxFetch  int64
	allowFile bool
	client    gitClient
	log       *slog.Logger
	now       func() time.Time

	mu        sync.Mutex
	caches    map[string]*cacheLock // only while a fetch or the pruner holds one
	lastPrune time.Time
}

// cacheLock is one cache's lock, counted so the map keeps only the caches
// in use.
type cacheLock struct {
	mu   sync.Mutex
	refs int
}

func newRemotes(o Options, log *slog.Logger) *remotes {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	g := &remotes{cacheDir: o.CacheDir, allow: o.Remotes, timeout: o.FetchTimeout, maxFile: o.MaxFile, maxFetch: o.MaxFetch,
		allowFile: o.AllowFileRemotes, client: goGit{}, log: log, now: o.Now, caches: map[string]*cacheLock{}}
	if g.timeout <= 0 {
		g.timeout = DefaultFetchTimeout
	}
	if g.maxFile <= 0 {
		g.maxFile = DefaultMaxFile
	}
	if g.maxFetch <= 0 {
		g.maxFetch = DefaultMaxFetch
	}
	if g.now == nil {
		g.now = time.Now
	}
	// On start: caches no fetch used for cacheMaxIdle go.
	g.prune()
	return g
}

// ── the verbs ────────────────────────────────────────────────────────────

func (m *Module) remoteCheck(ctx context.Context, req stackproto.RemoteCheckRequest, _ emitter) (stackproto.RemoteCheckResponse, error) {
	g := m.git
	u, err := g.target(req.URL, req.Branch)
	if err != nil {
		return stackproto.RemoteCheckResponse{}, err
	}
	var presented ssh.PublicKey
	auth, err := authFor(u, req.Auth, "", &presented)
	if err != nil {
		return stackproto.RemoteCheckResponse{}, err
	}
	ctx, cancel := context.WithTimeout(g.redirects(ctx), g.timeout)
	defer cancel()
	refs, lerr := g.client.List(ctx, u.raw, auth)
	if lerr != nil {
		return stackproto.RemoteCheckResponse{}, g.remoteError(ctx, u, req.Auth, lerr)
	}
	want := plumbing.NewBranchReferenceName(req.Branch)
	for _, r := range refs {
		if r.Name() == want && r.Type() == plumbing.HashReference {
			out := stackproto.RemoteCheckResponse{SHA: r.Hash().String()}
			if presented != nil {
				out.HostKey = authorizedKey(presented)
			}
			return out, nil
		}
	}
	return stackproto.RemoteCheckResponse{}, stackproto.Errorf(stackproto.CodeNotFound, "The remote has no branch %s.", req.Branch)
}

func (m *Module) fetch(ctx context.Context, req stackproto.FetchRequest, _ emitter) (stackproto.FetchResponse, error) {
	g := m.git
	u, err := g.target(req.URL, req.Branch)
	if err != nil {
		return stackproto.FetchResponse{}, err
	}
	if err := validFilePath(req.Path); err != nil {
		return stackproto.FetchResponse{}, err
	}
	auth, err := authFor(u, req.Auth, req.HostKey, nil)
	if err != nil {
		return stackproto.FetchResponse{}, err
	}
	if g.cacheDir == "" {
		return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeInternal, "This node has no cache directory for git remotes.")
	}
	ctx, cancel := context.WithTimeout(g.redirects(ctx), g.timeout)
	defer cancel()
	defer g.pruneSometimes()

	key := cacheKey(u, req.Branch)
	unlock := g.lockCache(key)
	defer unlock()
	dir := filepath.Join(g.cacheDir, key)
	r, existed, err := g.openCache(dir)
	if err != nil {
		return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeInternal, "The node couldn't open its cache for this remote.")
	}
	ferr := g.fetchInto(ctx, r, u, req.Branch, auth)
	if ferr != nil && existed && ctx.Err() == nil && !classified(ferr) {
		// A cache that went bad (an interrupted fetch, a history rewritten
		// on the remote) is thrown away and fetched again once.
		g.log.Warn("stacks: starting a git cache again", "host", u.host)
		if err := os.RemoveAll(dir); err == nil {
			if r, _, err = g.openCache(dir); err == nil {
				ferr = g.fetchInto(ctx, r, u, req.Branch, auth)
			}
		}
	}
	if ferr != nil {
		// Whatever a failed or cut-off fetch left behind goes.
		if err := os.RemoveAll(dir); err != nil {
			g.log.Warn("stacks: remove a git cache after a failed fetch", "host", u.host, "err", err)
		}
		return stackproto.FetchResponse{}, g.remoteError(ctx, u, req.Auth, ferr)
	}
	// The directory's time says when a fetch last used it (prune).
	now := g.now()
	_ = os.Chtimes(dir, now, now)
	return g.readTip(r, req.Branch, req.Path)
}

// fetchInto fetches branch's tip, depth 1, into r.
func (g *remotes) fetchInto(ctx context.Context, r *git.Repository, u remoteURL, branch string, auth transport.AuthMethod) error {
	ref := plumbing.NewBranchReferenceName(branch)
	o := &git.FetchOptions{
		RemoteName: "origin", RemoteURL: u.raw, Auth: auth, Force: true, Tags: git.NoTags, Depth: 1,
		RefSpecs: []config.RefSpec{config.RefSpec("+" + ref.String() + ":" + ref.String())},
	}
	if u.scheme == "file" {
		// file:// is for tests, served by go-git's in-process server, which
		// can't answer a shallow fetch.
		o.Depth = 0
	}
	err := g.client.Fetch(ctx, r, o)
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return err
}

// readTip reads p at branch's tip in the cache.
func (g *remotes) readTip(r *git.Repository, branch, p string) (stackproto.FetchResponse, error) {
	ref, err := r.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeNotFound, "The remote has no branch %s.", branch)
	}
	c, err := r.CommitObject(ref.Hash())
	if err != nil {
		return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeInternal, "The fetched commit couldn't be read.")
	}
	tree, err := c.Tree()
	if err != nil {
		return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeInternal, "The fetched commit couldn't be read.")
	}
	short := c.Hash.String()[:7]
	f, err := tree.File(p)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) || errors.Is(err, object.ErrDirectoryNotFound) || errors.Is(err, plumbing.ErrObjectNotFound) {
			return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeNotFound, "There's no %s on %s at %s.", p, branch, short)
		}
		return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeInternal, "%s couldn't be read at %s.", p, short)
	}
	if f.Size > g.maxFile {
		return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeInvalid, "%s is %d KiB at %s; a synced file can be at most %d KiB.", p, (f.Size+1023)/1024, short, g.maxFile/1024)
	}
	rd, err := f.Reader()
	if err != nil {
		return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeInternal, "%s couldn't be read at %s.", p, short)
	}
	defer rd.Close()
	b, err := io.ReadAll(io.LimitReader(rd, g.maxFile+1))
	if err != nil || int64(len(b)) > g.maxFile {
		return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeInvalid, "%s couldn't be read within the %d KiB limit.", p, g.maxFile/1024)
	}
	subject, _, _ := strings.Cut(strings.TrimSpace(c.Message), "\n")
	return stackproto.FetchResponse{SHA: c.Hash.String(), Content: string(b), Subject: strings.TrimSpace(subject), Author: c.Author.Name}, nil
}

// ── the cache ────────────────────────────────────────────────────────────

// lockCache locks one cache. The map holds a lock only while someone holds
// or waits for it, so it never grows past the caches in use.
func (g *remotes) lockCache(key string) func() {
	g.mu.Lock()
	l := g.caches[key]
	if l == nil {
		l = &cacheLock{}
		g.caches[key] = l
	}
	l.refs++
	g.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		g.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(g.caches, key)
		}
		g.mu.Unlock()
	}
}

// cacheKeyRE is a cache directory's name (cacheKey).
var cacheKeyRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// prune removes the caches no fetch used for cacheMaxIdle, skipping any a
// fetch holds now.
func (g *remotes) prune() {
	if g.cacheDir == "" {
		return
	}
	g.mu.Lock()
	g.lastPrune = g.now()
	g.mu.Unlock()
	entries, err := os.ReadDir(g.cacheDir)
	if err != nil {
		return
	}
	cutoff := g.now().Add(-cacheMaxIdle)
	for _, e := range entries {
		if !e.IsDir() || !cacheKeyRE.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		g.mu.Lock()
		_, busy := g.caches[e.Name()]
		g.mu.Unlock()
		if busy {
			continue
		}
		unlock := g.lockCache(e.Name())
		if err := os.RemoveAll(filepath.Join(g.cacheDir, e.Name())); err != nil {
			g.log.Warn("stacks: prune a git cache", "err", err)
		}
		unlock()
	}
}

// pruneSometimes prunes after a fetch, at most once per pruneEvery.
func (g *remotes) pruneSometimes() {
	g.mu.Lock()
	due := g.now().Sub(g.lastPrune) >= pruneEvery
	g.mu.Unlock()
	if due {
		g.prune()
	}
}

// openCache opens the bare cache repository at dir, or makes it. Its
// storage counts what a fetch writes (limitedStorage).
func (g *remotes) openCache(dir string) (*git.Repository, bool, error) {
	r, err := git.PlainOpen(dir)
	existed := err == nil
	if !existed {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, false, err
		}
		if r, err = git.PlainInit(dir, true); err != nil {
			return nil, false, err
		}
		// The URL is passed on each fetch (FetchOptions.RemoteURL); the
		// remote only has to exist.
		if _, err := r.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{"file:///nowhere"}}); err != nil {
			return nil, false, err
		}
	}
	fs, ok := r.Storer.(*filesystem.Storage)
	if !ok {
		return nil, false, errors.New("stacks: a git cache isn't on disk")
	}
	r.Storer = &limitedStorage{Storage: fs, max: g.maxFetch}
	return r, existed, nil
}

// errFetchTooLarge cuts off a fetch that writes more than maxFetch.
var errFetchTooLarge = errors.New("stacks: the fetch passed its size limit")

// limitedStorage is a cache's storage with a cap on the pack a fetch
// writes: go-git writes a fetched pack through PackfileWriter, whatever the
// transport, so the cap holds for https, ssh and file alike.
type limitedStorage struct {
	*filesystem.Storage
	max int64
}

// PackfileWriter implements storer.PackfileWriter.
func (s *limitedStorage) PackfileWriter() (io.WriteCloser, error) {
	w, err := s.Storage.PackfileWriter()
	if err != nil {
		return nil, err
	}
	return &limitWriter{w: w, left: s.max}, nil
}

type limitWriter struct {
	w    io.WriteCloser
	left int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.left {
		return 0, errFetchTooLarge
	}
	l.left -= int64(len(p))
	return l.w.Write(p)
}

func (l *limitWriter) Close() error { return l.w.Close() }

// cacheKey names a remote and branch's cache: a hash of the normalized URL
// (lowercase scheme and host, no default port, no trailing / or .git) and
// the branch, so every stack following them shares one.
func cacheKey(u remoteURL, branch string) string {
	sum := sha256.Sum256([]byte(u.normalized() + "\x00" + branch))
	return hex.EncodeToString(sum[:16])
}

// ── URLs and the allowlist ───────────────────────────────────────────────

// remoteURL is a checked remote address.
type remoteURL struct {
	raw    string
	scheme string
	host   string // lowercase, no brackets, no trailing dot
	port   string // the effective port ("" for file)
	path   string
}

var defaultPorts = map[string]string{"https": "443", "http": "80", "ssh": "22"}

func (u remoteURL) normalized() string {
	p := strings.TrimSuffix(strings.TrimRight(u.path, "/"), ".git")
	if u.scheme == "file" {
		return "file://" + p
	}
	host := u.host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if u.port != defaultPorts[u.scheme] {
		host += ":" + u.port
	}
	return u.scheme + "://" + host + p
}

var scpLikeRE = regexp.MustCompile(`^([^/@:]+@)?[A-Za-z0-9.\-\[\]]+:[^/]`)

// parseRemote checks a remote URL before any I/O: a scheme it may use, a
// host, and no user or password.
func parseRemote(raw string, allowFile bool) (remoteURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return remoteURL{}, stackproto.Errorf(stackproto.CodeInvalid, "Give the remote's URL.")
	}
	if !strings.Contains(raw, "://") {
		if scpLikeRE.MatchString(raw) {
			return remoteURL{}, stackproto.Errorf(stackproto.CodeInvalid, "Write the remote as ssh://host/path, and give the user with the sync's credential, not in the URL.")
		}
		return remoteURL{}, stackproto.Errorf(stackproto.CodeInvalid, "The remote's URL needs a scheme: https://, http:// or ssh://.")
	}
	pu, err := url.Parse(raw)
	if err != nil {
		return remoteURL{}, stackproto.Errorf(stackproto.CodeInvalid, "The remote's URL doesn't parse.")
	}
	if pu.User != nil {
		return remoteURL{}, stackproto.Errorf(stackproto.CodeInvalid, "The remote's URL has a user or password in it. Leave them out and give them as the sync's credential.")
	}
	if pu.RawQuery != "" || pu.Fragment != "" {
		return remoteURL{}, stackproto.Errorf(stackproto.CodeInvalid, "The remote's URL can't have a query or a fragment.")
	}
	u := remoteURL{raw: raw, scheme: strings.ToLower(pu.Scheme), path: pu.Path}
	switch u.scheme {
	case "https", "http", "ssh":
	case "file":
		if !allowFile {
			return remoteURL{}, stackproto.Errorf(stackproto.CodeInvalid, "file:// remotes aren't allowed; use https:// or ssh://.")
		}
		return u, nil
	default:
		return remoteURL{}, stackproto.Errorf(stackproto.CodeInvalid, "A remote is https://, http:// or ssh://, not %s://.", u.scheme)
	}
	u.host = strings.TrimSuffix(strings.ToLower(pu.Hostname()), ".")
	if u.host == "" {
		return remoteURL{}, stackproto.Errorf(stackproto.CodeInvalid, "The remote's URL has no host.")
	}
	u.port = pu.Port()
	if u.port == "" {
		u.port = defaultPorts[u.scheme]
	}
	return u, nil
}

// allowed reports whether the policy's stacks.remotes admits u. An entry
// without a port matches any port; empty admits every host.
func (g *remotes) allowed(u remoteURL) bool {
	if len(g.allow) == 0 {
		return true
	}
	if u.scheme == "file" {
		return false
	}
	for _, e := range g.allow {
		host, port := splitRemoteEntry(e)
		if strings.EqualFold(strings.TrimSuffix(host, "."), u.host) && (port == "" || port == u.port) {
			return true
		}
	}
	return false
}

// splitRemoteEntry splits a stacks.remotes entry: host, host:port, [v6],
// [v6]:port or a bare IPv6 address.
func splitRemoteEntry(e string) (host, port string) {
	switch {
	case strings.HasPrefix(e, "[") && strings.HasSuffix(e, "]"):
		return e[1 : len(e)-1], ""
	case strings.HasPrefix(e, "[") || strings.Count(e, ":") == 1:
		if h, p, err := net.SplitHostPort(e); err == nil {
			return h, p
		}
	}
	return e, ""
}

// target checks a request's URL and branch, and the allowlist.
func (g *remotes) target(raw, branch string) (remoteURL, error) {
	u, err := parseRemote(raw, g.allowFile)
	if err != nil {
		return u, err
	}
	if err := validBranch(branch); err != nil {
		return u, err
	}
	if !g.allowed(u) {
		where := u.host
		if where == "" {
			where = "a file:// remote"
		}
		return u, stackproto.Errorf(stackproto.CodeForbidden, "This node's policy doesn't list %s in stacks.remotes, so it won't reach it.", where)
	}
	return u, nil
}

var branchRE = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)

func validBranch(b string) error {
	if b == "" || len(b) > 200 || !branchRE.MatchString(b) || strings.Contains(b, "..") || strings.Contains(b, "//") ||
		strings.HasPrefix(b, "/") || strings.HasPrefix(b, "-") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") || strings.HasSuffix(b, ".") {
		return stackproto.Errorf(stackproto.CodeInvalid, "%q isn't a branch name.", b)
	}
	return nil
}

func validFilePath(p string) error {
	if p == "" || len(p) > 512 || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return stackproto.Errorf(stackproto.CodeInvalid, "The file's path must be relative to the repository's top, like media/compose.yaml.")
	}
	return nil
}

// ── redirects ────────────────────────────────────────────────────────────

// go-git's HTTP transport is process-wide (client.Protocols), so the
// node installs one whose redirects it checks: a redirect must stay within
// stacks.remotes when the policy lists hosts, may not leave https, and
// never carries the credential to another host or port. The module's
// policy travels in the request's context (redirects).
func init() {
	c := githttp.NewClientWithOptions(&http.Client{Transport: http.DefaultTransport, CheckRedirect: checkRedirect}, nil)
	client.InstallProtocol("http", c)
	client.InstallProtocol("https", c)
}

type redirectPolicyKey struct{}

// errRedirect is a redirect the policy refuses.
var errRedirect = errors.New("stacks: redirect refused")

// redirects puts the module's allowlist in ctx for checkRedirect.
func (g *remotes) redirects(ctx context.Context) context.Context {
	return context.WithValue(ctx, redirectPolicyKey{}, g)
}

// checkRedirect is the HTTP client's CheckRedirect. go-git's own policy
// (only the first request may redirect, at most 10) runs before it.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	from := via[0].URL
	if strings.EqualFold(from.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
		return fmt.Errorf("%w: it leaves https", errRedirect)
	}
	if g, _ := req.Context().Value(redirectPolicyKey{}).(*remotes); g != nil && len(g.allow) > 0 {
		u, err := parseRemote(req.URL.Scheme+"://"+req.URL.Host+"/", false)
		if err != nil || !g.allowed(u) {
			return fmt.Errorf("%w: %s isn't in stacks.remotes", errRedirect, req.URL.Hostname())
		}
	}
	if hostPort(from) != hostPort(req.URL) {
		// net/http keeps Authorization for the same host on another port,
		// and for a subdomain; the credential is for one host and port.
		req.Header.Del("Authorization")
	}
	return nil
}

// hostPort is a URL's lowercase host and effective port.
func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = defaultPorts[strings.ToLower(u.Scheme)]
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// ── credentials ──────────────────────────────────────────────────────────

// authFor builds the transport's auth from the hub's credential. Over SSH,
// a check passes capture and accepts the key the server presents, keeping
// it to pin; a fetch passes the pinned hostKey and accepts only that.
func authFor(u remoteURL, a stackproto.RemoteAuth, hostKey string, capture *ssh.PublicKey) (transport.AuthMethod, error) {
	switch a.Kind {
	case "", stackproto.AuthNone:
		if u.scheme == "ssh" {
			return nil, stackproto.Errorf(stackproto.CodeInvalid, "An ssh:// remote needs an SSH key as the sync's credential.")
		}
		return nil, nil
	case stackproto.AuthHTTPS:
		if u.scheme != "https" && u.scheme != "http" {
			return nil, stackproto.Errorf(stackproto.CodeInvalid, "A user and token work only with an https:// remote.")
		}
		if a.Secret == "" {
			return nil, stackproto.Errorf(stackproto.CodeInvalid, "Give the token or password for the remote.")
		}
		return &githttp.BasicAuth{Username: or(a.Username, "git"), Password: a.Secret}, nil
	case stackproto.AuthSSH:
		if u.scheme != "ssh" {
			return nil, stackproto.Errorf(stackproto.CodeInvalid, "An SSH key works only with an ssh:// remote.")
		}
		keys, err := gitssh.NewPublicKeys(or(a.Username, "git"), []byte(a.Secret), "")
		if err != nil {
			return nil, stackproto.Errorf(stackproto.CodeInvalid, "The SSH key couldn't be read. Give an unencrypted private key, in OpenSSH or PEM form.")
		}
		if capture != nil {
			keys.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
				*capture = key
				return nil
			}
			return keys, nil
		}
		if strings.TrimSpace(hostKey) == "" {
			return nil, stackproto.Errorf(stackproto.CodeInvalid, "An ssh:// remote needs its host key pinned. Test the sync first and confirm the key.")
		}
		pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
		if err != nil {
			return nil, stackproto.Errorf(stackproto.CodeInvalid, "The pinned SSH host key doesn't parse.")
		}
		keys.HostKeyCallback = ssh.FixedHostKey(pinned)
		return keys, nil
	}
	return nil, stackproto.Errorf(stackproto.CodeInvalid, "%q isn't a credential kind (none, https or ssh).", a.Kind)
}

// authorizedKey is key as an authorized_keys line: "ssh-ed25519 AAAA…".
func authorizedKey(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

var userinfoRE = regexp.MustCompile(`://[^/@\s]*@`)

// scrubCredentials takes a's secret, and any user:password@ in a URL, out
// of s.
func scrubCredentials(s string, a stackproto.RemoteAuth) string {
	s = scrub(s, map[string]string{"secret": a.Secret})
	return userinfoRE.ReplaceAllString(s, "://")
}

// classified reports whether err is a remote's answer rather than a cache
// problem: credentials, a missing repository, a host key. Those aren't
// retried with a fresh cache.
func classified(err error) bool {
	return errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed) ||
		errors.Is(err, transport.ErrRepositoryNotFound) || errors.Is(err, transport.ErrEmptyRemoteRepository) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, errFetchTooLarge) || errors.Is(err, errRedirect) ||
		strings.Contains(err.Error(), "host key mismatch") || strings.Contains(err.Error(), "unable to authenticate")
}

// remoteError turns a go-git failure into a sentence, without the
// credential. Problems the admin must fix are invalid; the rest are
// internal (the hub answers 502).
func (g *remotes) remoteError(ctx context.Context, u remoteURL, a stackproto.RemoteAuth, err error) error {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return stackproto.Errorf(stackproto.CodeInternal, "The remote didn't answer within %s.", g.timeout)
	case errors.Is(err, transport.ErrAuthenticationRequired):
		return stackproto.Errorf(stackproto.CodeInvalid, "The remote asks for credentials. Add them to the sync.")
	case errors.Is(err, transport.ErrAuthorizationFailed) || strings.Contains(msg, "unable to authenticate"):
		return stackproto.Errorf(stackproto.CodeInvalid, "The remote refused the sync's credentials.")
	case strings.Contains(msg, "host key mismatch"):
		return stackproto.Errorf(stackproto.CodeInvalid, "The remote's SSH host key changed since it was pinned. Check why, then test the sync again to pin the new key.")
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return stackproto.Errorf(stackproto.CodeNotFound, "There's no repository at that address, or the credentials can't see it.")
	case errors.Is(err, transport.ErrEmptyRemoteRepository):
		return stackproto.Errorf(stackproto.CodeNotFound, "The remote repository is empty.")
	case errors.Is(err, errFetchTooLarge) || strings.Contains(msg, errFetchTooLarge.Error()):
		limit := fmt.Sprintf("%d MiB", g.maxFetch>>20)
		if g.maxFetch < 1<<20 {
			limit = fmt.Sprintf("%d KiB", g.maxFetch>>10)
		}
		return stackproto.Errorf(stackproto.CodeInvalid, "The fetch passed the %s limit, so the node stopped it. Follow a smaller repository, or a branch with less in it.", limit)
	case errors.Is(err, errRedirect) || strings.Contains(msg, errRedirect.Error()):
		return stackproto.Errorf(stackproto.CodeForbidden, "The remote redirected somewhere this node won't follow: a host outside stacks.remotes, or away from https.")
	}
	where := u.host
	if where == "" {
		where = u.scheme
	}
	g.log.Warn("stacks: a git remote failed", "host", where)
	return stackproto.Errorf(stackproto.CodeInternal, "Couldn't read the remote: %s", scrubCredentials(msg, a))
}
