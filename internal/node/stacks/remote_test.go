package stacks

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"

	"dash.cha.sh/internal/stackproto"
)

// file:// remotes are served in process by go-git's server, so the tests
// need no git binary. Installed once, before any test runs.
func TestMain(m *testing.M) {
	client.InstallProtocol("file", server.DefaultServer)
	os.Exit(m.Run())
}

// upstream is a git repository on disk the tests push commits to.
type upstream struct {
	t   *testing.T
	dir string
	r   *git.Repository
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return &upstream{t: t, dir: dir, r: r}
}

// url is the repository's file:// address (its .git directory).
func (u *upstream) url() string { return "file://" + filepath.Join(u.dir, ".git") }

// commit writes files and commits them on the current branch.
func (u *upstream) commit(subject string, files map[string]string) string {
	u.t.Helper()
	wt, err := u.r.Worktree()
	if err != nil {
		u.t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(u.dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			u.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			u.t.Fatal(err)
		}
		if _, err := wt.Add(name); err != nil {
			u.t.Fatal(err)
		}
	}
	h, err := wt.Commit(subject+"\n\nA body line.", &git.CommitOptions{Author: &object.Signature{Name: "Ada Example", Email: "ada@git.example", When: time.Now()}})
	if err != nil {
		u.t.Fatal(err)
	}
	return h.String()
}

func (u *upstream) branch() string {
	u.t.Helper()
	h, err := u.r.Head()
	if err != nil {
		u.t.Fatal(err)
	}
	return h.Name().Short()
}

func gitNode(t *testing.T, opts ...func(*Options)) *testNode {
	t.Helper()
	cache := t.TempDir()
	return newTestNode(t, append([]func(*Options){func(o *Options) {
		o.CacheDir, o.AllowFileRemotes = cache, true
	}}, opts...)...)
}

// Check lists the tip without fetching; fetch reads the file at the tip
// into the cache, outside the stacks root, and sees a new commit.
func TestFetchFromALocalRemote(t *testing.T) {
	up := newUpstream(t)
	first := up.commit("Add media", map[string]string{"media/compose.yaml": v1, "README.md": "hi"})
	n := gitNode(t)
	branch := up.branch()

	var chk stackproto.RemoteCheckResponse
	n.must(stackproto.VerbRemoteCheck, stackproto.RemoteCheckRequest{URL: up.url(), Branch: branch}, &chk)
	if chk.SHA != first || chk.HostKey != "" {
		t.Fatalf("check: %+v, want %s", chk, first)
	}

	req := stackproto.FetchRequest{URL: up.url(), Branch: branch, Path: "media/compose.yaml"}
	var got stackproto.FetchResponse
	n.must(stackproto.VerbFetch, req, &got)
	if got.SHA != first || got.Content != v1 || got.Subject != "Add media" || got.Author != "Ada Example" {
		t.Fatalf("fetch: %+v", got)
	}
	// The cache is in CacheDir, and the stacks root is untouched.
	if entries, _ := os.ReadDir(n.m.git.cacheDir); len(entries) != 1 {
		t.Fatalf("cache dirs: %v", entries)
	}
	if entries, _ := os.ReadDir(n.root); len(entries) != 0 {
		t.Fatalf("the stacks root changed: %v", entries)
	}

	second := up.commit("Bump nginx", map[string]string{"media/compose.yaml": v2Good})
	n.must(stackproto.VerbFetch, req, &got)
	if got.SHA != second || got.Content != v2Good || got.Subject != "Bump nginx" {
		t.Fatalf("second fetch: %+v", got)
	}

	// Another stack on the same remote and branch shares the cache.
	other := req
	other.Path = "README.md"
	n.must(stackproto.VerbFetch, other, &got)
	if got.Content != "hi" {
		t.Fatalf("readme: %+v", got)
	}
	if entries, _ := os.ReadDir(n.m.git.cacheDir); len(entries) != 1 {
		t.Fatalf("a second cache for the same remote: %v", entries)
	}

	// A missing file, a missing branch and a bad path.
	missing := req
	missing.Path = "nope/compose.yaml"
	if err := n.call(stackproto.VerbFetch, missing, nil); codeOf(err) != stackproto.CodeNotFound {
		t.Errorf("missing file: %v", err)
	}
	if err := n.call(stackproto.VerbRemoteCheck, stackproto.RemoteCheckRequest{URL: up.url(), Branch: "nope"}, nil); codeOf(err) != stackproto.CodeNotFound {
		t.Errorf("missing branch: %v", err)
	}
	for _, p := range []string{"", "/etc/passwd", "../x", "media/../../x", "a//b", "."} {
		bad := req
		bad.Path = p
		if err := n.call(stackproto.VerbFetch, bad, nil); codeOf(err) != stackproto.CodeInvalid {
			t.Errorf("path %q: %v", p, err)
		}
	}
}

func TestFetchSizeLimit(t *testing.T) {
	up := newUpstream(t)
	up.commit("Big", map[string]string{"big.yaml": strings.Repeat("x", 2048), "ok.yaml": "services: {}\n"})
	n := gitNode(t, func(o *Options) { o.MaxFile = 1024 })
	req := stackproto.FetchRequest{URL: up.url(), Branch: up.branch(), Path: "big.yaml"}
	err := n.call(stackproto.VerbFetch, req, nil)
	if codeOf(err) != stackproto.CodeInvalid || !strings.Contains(err.Error(), "at most 1 KiB") {
		t.Fatalf("2 KiB file: %v", err)
	}
	req.Path = "ok.yaml"
	n.must(stackproto.VerbFetch, req, nil)
	if newRemotes(Options{}, nil).maxFile != 1<<20 || newRemotes(Options{}, nil).timeout != 60*time.Second {
		t.Fatal("defaults aren't 1 MiB and 60 s")
	}
}

// URLs are checked before any I/O: no user or password, no other scheme,
// and only hosts in stacks.remotes.
func TestRemoteRefusals(t *testing.T) {
	n := newTestNode(t, func(o *Options) {
		o.CacheDir = t.TempDir()
		o.Remotes = []string{"git.lab.example", "Forge.Example:2222", "[fd00::5]:22", "10.20.0.5"}
	})
	n.m.git.client = failingGit{err: errors.New("no I/O expected")}
	cases := []struct {
		url, code string
	}{
		{"https://sam:FAKE-token-1234@git.lab.example/ops/stacks.git", stackproto.CodeInvalid},
		{"https://sam@git.lab.example/ops/stacks.git", stackproto.CodeInvalid},
		{"ssh://git@git.lab.example/ops/stacks.git", stackproto.CodeInvalid},
		{"git@git.lab.example:ops/stacks.git", stackproto.CodeInvalid},
		{"git.lab.example:ops/stacks.git", stackproto.CodeInvalid},
		{"ftp://git.lab.example/ops", stackproto.CodeInvalid},
		{"file:///srv/git/stacks.git", stackproto.CodeInvalid}, // not in tests' AllowFileRemotes here
		{"https://git.lab.example/x?token=1", stackproto.CodeInvalid},
		{"https://elsewhere.example/ops/stacks.git", stackproto.CodeForbidden},
		{"https://git.lab.example.evil.example/ops", stackproto.CodeForbidden},
		{"ssh://forge.example/ops/stacks.git", stackproto.CodeForbidden}, // port 22, entry says 2222
		{"ssh://forge.example:2222/ops/stacks.git", "allowed"},           // case-insensitive, port matches
		{"https://GIT.lab.example:8443/ops/stacks.git", "allowed"},       // no port in the entry: any port
		{"ssh://[fd00::5]/ops/stacks.git", "allowed"},
		{"https://10.20.0.5/ops/stacks.git", "allowed"},
	}
	for _, c := range cases {
		err := n.call(stackproto.VerbRemoteCheck, stackproto.RemoteCheckRequest{URL: c.url, Branch: "main"}, nil)
		got := codeOf(err)
		if c.code == "allowed" {
			// It got as far as I/O (or the credential check for ssh).
			if !strings.Contains(err.Error(), "no I/O expected") && !strings.Contains(err.Error(), "SSH key") {
				t.Errorf("%s: %v, want it allowed", c.url, err)
			}
			continue
		}
		if got != c.code {
			t.Errorf("%s: %v, want %s", c.url, err, c.code)
		}
		if strings.Contains(err.Error(), "FAKE-token") {
			t.Errorf("%s: the error repeats the password: %v", c.url, err)
		}
	}
	for _, b := range []string{"", "-x", "a..b", "a b", "main.lock", "/main", "main/"} {
		if err := n.call(stackproto.VerbRemoteCheck, stackproto.RemoteCheckRequest{URL: "https://git.lab.example/x", Branch: b}, nil); codeOf(err) != stackproto.CodeInvalid {
			t.Errorf("branch %q: %v", b, err)
		}
	}
	// Without a cache directory, fetch refuses.
	bare := newTestNode(t)
	if err := bare.call(stackproto.VerbFetch, stackproto.FetchRequest{URL: "https://git.lab.example/x", Branch: "main", Path: "a.yaml"}, nil); codeOf(err) != stackproto.CodeInternal {
		t.Errorf("no cache dir: %v", err)
	}
}

// failingGit fails every call with err, or waits for the context to end
// when block is set.
type failingGit struct {
	err   error
	block bool
}

func (f failingGit) List(ctx context.Context, _ string, _ transport.AuthMethod) ([]*plumbing.Reference, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, f.err
}

func (f failingGit) Fetch(ctx context.Context, _ *git.Repository, _ *git.FetchOptions) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

// A remote that never answers is given up on after FetchTimeout.
func TestRemoteTimeout(t *testing.T) {
	n := newTestNode(t, func(o *Options) { o.CacheDir, o.FetchTimeout = t.TempDir(), 50*time.Millisecond })
	n.m.git.client = failingGit{block: true}
	start := time.Now()
	err := n.call(stackproto.VerbFetch, stackproto.FetchRequest{URL: "https://git.lab.example/ops/stacks.git", Branch: "main", Path: "a.yaml"}, nil)
	if codeOf(err) != stackproto.CodeInternal || !strings.Contains(err.Error(), "didn't answer within 50ms") {
		t.Fatalf("timeout: %v", err)
	}
	if err := n.call(stackproto.VerbRemoteCheck, stackproto.RemoteCheckRequest{URL: "https://git.lab.example/ops/stacks.git", Branch: "main"}, nil); !strings.Contains(errText(err), "didn't answer") {
		t.Fatalf("check timeout: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the timeout didn't hold")
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The credential never comes back in an error, whatever the transport put
// in its message.
func TestRemoteErrorsAreScrubbed(t *testing.T) {
	const secret = "FAKE-gh-token-0123456789"
	n := newTestNode(t, func(o *Options) { o.CacheDir = t.TempDir() })
	n.m.git.client = failingGit{err: errors.New("dial https://sam:" + secret + "@git.lab.example/ops: connection refused (token " + secret + ")")}
	auth := stackproto.RemoteAuth{Kind: stackproto.AuthHTTPS, Username: "sam", Secret: secret}
	for _, call := range []func() error{
		func() error {
			return n.call(stackproto.VerbRemoteCheck, stackproto.RemoteCheckRequest{URL: "https://git.lab.example/ops", Branch: "main", Auth: auth}, nil)
		},
		func() error {
			return n.call(stackproto.VerbFetch, stackproto.FetchRequest{URL: "https://git.lab.example/ops", Branch: "main", Path: "a.yaml", Auth: auth}, nil)
		},
	} {
		err := call()
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sam:") || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("not scrubbed: %v", err)
		}
	}
	// Credentials the remote refuses read as a sentence.
	n.m.git.client = failingGit{err: transport.ErrAuthorizationFailed}
	if err := n.call(stackproto.VerbRemoteCheck, stackproto.RemoteCheckRequest{URL: "https://git.lab.example/ops", Branch: "main", Auth: auth}, nil); codeOf(err) != stackproto.CodeInvalid {
		t.Fatalf("refused credentials: %v", err)
	}
}

// Stubbed transports: the auth each credential kind builds, without a
// network.
func TestRemoteAuthMethods(t *testing.T) {
	https, _ := parseRemote("https://git.lab.example/ops/stacks.git", false)
	sshURL, _ := parseRemote("ssh://git.lab.example/ops/stacks.git", false)

	a, err := authFor(https, stackproto.RemoteAuth{Kind: stackproto.AuthHTTPS, Username: "sam", Secret: "FAKE-token"}, "", nil)
	if b, ok := a.(*githttp.BasicAuth); err != nil || !ok || b.Username != "sam" || b.Password != "FAKE-token" {
		t.Fatalf("https: %#v %v", a, err)
	}
	a, _ = authFor(https, stackproto.RemoteAuth{Kind: stackproto.AuthHTTPS, Secret: "FAKE-token"}, "", nil)
	if a.(*githttp.BasicAuth).Username != "git" {
		t.Fatal("https without a user")
	}
	if a, err := authFor(https, stackproto.RemoteAuth{Kind: stackproto.AuthNone}, "", nil); a != nil || err != nil {
		t.Fatalf("none: %v %v", a, err)
	}
	for name, c := range map[string]struct {
		u remoteURL
		a stackproto.RemoteAuth
	}{
		"https creds on ssh": {sshURL, stackproto.RemoteAuth{Kind: stackproto.AuthHTTPS, Secret: "x"}},
		"ssh key on https":   {https, stackproto.RemoteAuth{Kind: stackproto.AuthSSH, Secret: "x"}},
		"ssh without a key":  {sshURL, stackproto.RemoteAuth{Kind: stackproto.AuthNone}},
		"https no secret":    {https, stackproto.RemoteAuth{Kind: stackproto.AuthHTTPS}},
		"bad key":            {sshURL, stackproto.RemoteAuth{Kind: stackproto.AuthSSH, Secret: "not a key"}},
		"unknown kind":       {https, stackproto.RemoteAuth{Kind: "token"}},
	} {
		if _, err := authFor(c.u, c.a, "", nil); codeOf(err) != stackproto.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}

	// SSH: a fake client key, and two host keys.
	_, clientKey, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(clientKey, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(block))
	hostPub, _, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewPublicKey(hostPub)
	otherKey, _ := ssh.NewPublicKey(otherPub)
	addr := &net.TCPAddr{IP: net.ParseIP("10.20.0.5"), Port: 22}
	cred := stackproto.RemoteAuth{Kind: stackproto.AuthSSH, Secret: keyPEM}

	// Check: any host key is accepted and captured, to pin.
	var captured ssh.PublicKey
	a, err = authFor(sshURL, cred, "", &captured)
	keys, ok := a.(*gitssh.PublicKeys)
	if err != nil || !ok || keys.User != "git" {
		t.Fatalf("ssh: %#v %v", a, err)
	}
	if err := keys.HostKeyCallback("git.lab.example:22", addr, hostKey); err != nil || captured == nil {
		t.Fatalf("capture: %v", err)
	}
	pinned := authorizedKey(captured)
	if !strings.HasPrefix(pinned, "ssh-ed25519 AAAA") || strings.HasSuffix(pinned, "\n") {
		t.Fatalf("pinned %q", pinned)
	}

	// Fetch: the pinned key is required, and only it is accepted.
	if _, err := authFor(sshURL, cred, "", nil); codeOf(err) != stackproto.CodeInvalid {
		t.Fatalf("fetch without a pinned key: %v", err)
	}
	cred.Username = "deploy"
	a, err = authFor(sshURL, cred, pinned, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys = a.(*gitssh.PublicKeys)
	if keys.User != "deploy" {
		t.Fatalf("user %q", keys.User)
	}
	if err := keys.HostKeyCallback("git.lab.example:22", addr, hostKey); err != nil {
		t.Fatalf("pinned key refused: %v", err)
	}
	err = keys.HostKeyCallback("git.lab.example:22", addr, otherKey)
	if err == nil {
		t.Fatal("another host key was accepted")
	}
	g := newRemotes(Options{}, slog.New(slog.DiscardHandler))
	if e := g.remoteError(context.Background(), sshURL, cred, err); codeOf(e) != stackproto.CodeInvalid || !strings.Contains(e.Error(), "host key changed") {
		t.Fatalf("mismatch reads: %v", e)
	}
}

// Two URLs for one remote share a cache: with and without .git, a trailing
// slash, the default port, or upper case in the host.
func TestCacheKeyNormalizes(t *testing.T) {
	key := func(raw string) string {
		u, err := parseRemote(raw, false)
		if err != nil {
			t.Fatal(err)
		}
		return cacheKey(u, "main")
	}
	base := key("https://git.lab.example/ops/stacks")
	for _, same := range []string{"https://git.lab.example/ops/stacks.git", "https://git.lab.example/ops/stacks/", "HTTPS://Git.Lab.Example:443/ops/stacks.git"} {
		if key(same) != base {
			t.Errorf("%s gets its own cache", same)
		}
	}
	for _, other := range []string{"https://git.lab.example:8443/ops/stacks", "http://git.lab.example/ops/stacks", "https://git.lab.example/ops/other"} {
		if key(other) == base {
			t.Errorf("%s shares the cache", other)
		}
	}
	u, _ := parseRemote("https://git.lab.example/ops/stacks", false)
	if cacheKey(u, "main") == cacheKey(u, "dev") {
		t.Error("branches share a cache")
	}
}

// A deploy a sync started names its source in a trailer, and its subject
// says it was the sync, for its owner.
func TestDeployFromGitNamesItsSource(t *testing.T) {
	n := newTestNode(t)
	var res stackproto.DeployResult
	n.must("stack.deploy", map[string]any{"stack": "media", "content": v1, "by": "sam", "via": "sync", "ref": "t_sync",
		"source": "https://git.lab.example/ops/stacks.git@0123456789abcdef0123456789abcdef01234567"}, &res)
	if res.Outcome != stackproto.OutcomeDeployed {
		t.Fatalf("deploy: %+v", res)
	}
	c := headCommit(t, n.root, 0)
	subject, _, _ := strings.Cut(c.Message, "\n")
	if !strings.Contains(subject, "· by git sync for sam via sync") {
		t.Fatalf("subject %q", subject)
	}
	if trailer(c.Message, trailerSource) != "https://git.lab.example/ops/stacks.git@0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("message %q", c.Message)
	}
	// A deploy from the web has no source.
	n.deploy("media", v2Good, nil)
	if c := headCommit(t, n.root, 0); trailer(c.Message, trailerSource) != "" || strings.Contains(c.Message, "git sync") {
		t.Fatalf("web deploy %q", c.Message)
	}
}
