package stacks

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/stackproto"
)

// A fetch that writes more than MaxFetch is cut off, and its cache goes.
func TestFetchByteLimit(t *testing.T) {
	up := newUpstream(t)
	noise := make([]byte, 256<<10) // incompressible, so the pack is as big
	_, _ = rand.Read(noise)
	up.commit("Big", map[string]string{"compose.yaml": "services: {}\n", "blob.bin": hex.EncodeToString(noise)})
	n := gitNode(t, func(o *Options) { o.MaxFetch = 64 << 10 })
	req := stackproto.FetchRequest{URL: up.url(), Branch: up.branch(), Path: "compose.yaml"}
	err := n.call(stackproto.VerbFetch, req, nil)
	if codeOf(err) != stackproto.CodeInvalid || !strings.Contains(err.Error(), "64 KiB limit") {
		t.Fatalf("over the limit: %v", err)
	}
	if entries, _ := os.ReadDir(n.m.git.cacheDir); len(entries) != 0 {
		t.Fatalf("the cut-off fetch left a cache: %v", entries)
	}
	if len(n.m.git.caches) != 0 {
		t.Fatalf("locks kept: %d", len(n.m.git.caches))
	}
	// The same remote under the default limit fetches.
	n2 := gitNode(t)
	n2.must(stackproto.VerbFetch, req, nil)
	if newRemotes(Options{}, nil).maxFetch != 64<<20 {
		t.Fatal("the default isn't 64 MiB")
	}
}

// A failed fetch leaves no cache behind.
func TestFailedFetchRemovesItsCache(t *testing.T) {
	up := newUpstream(t)
	up.commit("One", map[string]string{"compose.yaml": v1})
	n := gitNode(t)
	err := n.call(stackproto.VerbFetch, stackproto.FetchRequest{URL: up.url(), Branch: "nope", Path: "compose.yaml"}, nil)
	if err == nil {
		t.Fatal("a missing branch fetched")
	}
	if entries, _ := os.ReadDir(n.m.git.cacheDir); len(entries) != 0 {
		t.Fatalf("a failed fetch left a cache: %v", entries)
	}
}

// Caches no fetch used for 14 days go, on start and after a fetch; the
// lock map keeps only the caches in use.
func TestCachesArePruned(t *testing.T) {
	up := newUpstream(t)
	up.commit("One", map[string]string{"compose.yaml": v1})
	n := gitNode(t)
	req := stackproto.FetchRequest{URL: up.url(), Branch: up.branch(), Path: "compose.yaml"}
	n.must(stackproto.VerbFetch, req, nil)
	g := n.m.git
	entries, _ := os.ReadDir(g.cacheDir)
	if len(entries) != 1 {
		t.Fatalf("caches: %v", entries)
	}
	if len(g.caches) != 0 {
		t.Fatalf("a lock outlived its fetch: %d", len(g.caches))
	}
	// Something that isn't a cache is left alone.
	other := filepath.Join(g.cacheDir, "notes")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	old := n.clock.Add(-15 * 24 * time.Hour)
	_ = os.Chtimes(other, old, old)

	// 13 days on, it stays.
	n.advance(13 * 24 * time.Hour)
	g.prune()
	if entries, _ := os.ReadDir(g.cacheDir); len(entries) != 2 {
		t.Fatalf("pruned too early: %v", entries)
	}
	// A node that starts 15 days on prunes it.
	n.advance(2 * 24 * time.Hour)
	newRemotes(Options{CacheDir: g.cacheDir, Now: g.now}, nil)
	entries, _ = os.ReadDir(g.cacheDir)
	if len(entries) != 1 || entries[0].Name() != "notes" {
		t.Fatalf("after 15 days: %v", entries)
	}

	// After a fetch, at most hourly: an idle cache of another branch goes.
	n.must(stackproto.VerbFetch, req, nil)
	stale := filepath.Join(g.cacheDir, strings.Repeat("a", 32))
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(stale, old, old)
	n.advance(2 * time.Hour)
	n.must(stackproto.VerbFetch, req, nil)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("an idle cache survived a fetch's prune: %v", err)
	}
	// A cache in use is skipped.
	busy := filepath.Join(g.cacheDir, strings.Repeat("b", 32))
	_ = os.Mkdir(busy, 0o700)
	_ = os.Chtimes(busy, old, old)
	unlock := g.lockCache(strings.Repeat("b", 32))
	g.prune()
	unlock()
	if _, err := os.Stat(busy); err != nil {
		t.Fatalf("a cache in use was pruned: %v", err)
	}
}

// Many fetches at once share a lock per cache and leave none behind.
func TestCacheLocksAreReleased(t *testing.T) {
	g := newRemotes(Options{}, nil)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			unlock := g.lockCache([]string{"a", "b", "c"}[i%3])
			unlock()
		})
	}
	wg.Wait()
	if len(g.caches) != 0 {
		t.Fatalf("locks kept: %d", len(g.caches))
	}
}

// An HTTP redirect stays within stacks.remotes, and never carries the
// credential to another host or port.
func TestRedirects(t *testing.T) {
	var mu sync.Mutex
	var targetHits int
	var targetAuth string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targetHits++
		targetAuth = r.Header.Get("Authorization")
		mu.Unlock()
		http.NotFound(w, r)
	}))
	defer target.Close()
	var firstAuth string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		firstAuth = r.Header.Get("Authorization")
		mu.Unlock()
		http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	defer origin.Close()
	_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))
	auth := stackproto.RemoteAuth{Kind: stackproto.AuthHTTPS, Username: "sam", Secret: "FAKE-token-redirect"}
	req := stackproto.RemoteCheckRequest{URL: origin.URL + "/ops/stacks.git", Branch: "main", Auth: auth}

	// With an allowlist, a redirect to a host:port outside it is refused
	// before it's followed.
	n := newTestNode(t, func(o *Options) { o.CacheDir, o.Remotes = t.TempDir(), []string{"127.0.0.1:" + originPort} })
	err := n.call(stackproto.VerbRemoteCheck, req, nil)
	if codeOf(err) != stackproto.CodeForbidden {
		t.Fatalf("redirect outside stacks.remotes: %v", err)
	}
	mu.Lock()
	if targetHits != 0 {
		t.Fatalf("the redirect was followed: %d", targetHits)
	}
	if firstAuth == "" {
		t.Fatal("the origin got no credential: the test proves nothing")
	}
	mu.Unlock()
	if err := n.call(stackproto.VerbFetch, stackproto.FetchRequest{URL: req.URL, Branch: "main", Path: "a.yaml", Auth: auth}, nil); codeOf(err) != stackproto.CodeForbidden {
		t.Fatalf("fetch through a redirect outside stacks.remotes: %v", err)
	}

	// Without one, it's followed, but the credential stays behind.
	open := newTestNode(t, func(o *Options) { o.CacheDir = t.TempDir() })
	_ = open.call(stackproto.VerbRemoteCheck, req, nil)
	mu.Lock()
	defer mu.Unlock()
	if targetHits == 0 {
		t.Fatal("the redirect wasn't followed")
	}
	if targetAuth != "" {
		t.Fatalf("the credential went to another port: %q", targetAuth)
	}
}

func (n *testNode) advance(d time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.clock = n.clock.Add(d)
}
