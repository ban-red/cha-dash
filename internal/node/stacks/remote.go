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
	"io"
	"log/slog"
	"net"
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
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/crypto/ssh"

	"dash.cha.sh/internal/stackproto"
)

// Limits on a remote call.
const (
	DefaultFetchTimeout = 60 * time.Second
	DefaultMaxFile      = 1 << 20
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
// per cache.
type remotes struct {
	cacheDir  string
	allow     []string
	timeout   time.Duration
	maxFile   int64
	allowFile bool
	client    gitClient
	log       *slog.Logger

	mu     sync.Mutex
	caches map[string]*sync.Mutex
}

func newRemotes(o Options, log *slog.Logger) *remotes {
	g := &remotes{cacheDir: o.CacheDir, allow: o.Remotes, timeout: o.FetchTimeout, maxFile: o.MaxFile,
		allowFile: o.AllowFileRemotes, client: goGit{}, log: log, caches: map[string]*sync.Mutex{}}
	if g.timeout <= 0 {
		g.timeout = DefaultFetchTimeout
	}
	if g.maxFile <= 0 {
		g.maxFile = DefaultMaxFile
	}
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
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
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
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	key := cacheKey(u, req.Branch)
	unlock := g.lockCache(key)
	defer unlock()
	dir := filepath.Join(g.cacheDir, key)
	r, existed, err := openCache(dir)
	if err != nil {
		return stackproto.FetchResponse{}, stackproto.Errorf(stackproto.CodeInternal, "The node couldn't open its cache for this remote.")
	}
	ferr := g.fetchInto(ctx, r, u, req.Branch, auth)
	if ferr != nil && existed && ctx.Err() == nil && !classified(ferr) {
		// A cache that went bad (an interrupted fetch, a history rewritten
		// on the remote) is thrown away and fetched again once.
		g.log.Warn("stacks: starting a git cache again", "host", u.host)
		if err := os.RemoveAll(dir); err == nil {
			if r, _, err = openCache(dir); err == nil {
				ferr = g.fetchInto(ctx, r, u, req.Branch, auth)
			}
		}
	}
	if ferr != nil {
		return stackproto.FetchResponse{}, g.remoteError(ctx, u, req.Auth, ferr)
	}
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

func (g *remotes) lockCache(key string) func() {
	g.mu.Lock()
	l := g.caches[key]
	if l == nil {
		l = &sync.Mutex{}
		g.caches[key] = l
	}
	g.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// openCache opens the bare cache repository at dir, or makes it.
func openCache(dir string) (*git.Repository, bool, error) {
	if r, err := git.PlainOpen(dir); err == nil {
		return r, true, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	r, err := git.PlainInit(dir, true)
	if err != nil {
		return nil, false, err
	}
	// The URL is passed on each fetch (FetchOptions.RemoteURL); the remote
	// only has to exist.
	if _, err := r.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{"file:///nowhere"}}); err != nil {
		return nil, false, err
	}
	return r, false, nil
}

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
	}
	where := u.host
	if where == "" {
		where = u.scheme
	}
	g.log.Warn("stacks: a git remote failed", "host", where)
	return stackproto.Errorf(stackproto.CodeInternal, "Couldn't read the remote: %s", scrubCredentials(msg, a))
}
