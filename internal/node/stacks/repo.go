package stacks

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"

	"dash.cha.sh/internal/stackproto"
)

// The stacks root is a git repository (go-git; no git binary). Every
// change Cha Dash makes to a stack is a commit, and the commit message
// carries trailers the module reads back:
//
//	Chadash-Kind: deploy | rollback | adopt | unadopt | accept
//	Chadash-Stack: media
//	Chadash-Actor: sam
//	Chadash-Previous-Image: web=nginx@sha256:…   (one per service, on deploys)
//
// Chadash-Previous-Image records the images that were running when a
// deploy started, which are the images the previous commit last ran: a
// rollback to that commit pins them, so a moved tag can't roll back to a
// different image.
//
// Files are staged by writing the index directly rather than with
// Worktree.Add, which walks the whole worktree: a stack's directory often
// holds its data too, and that can be millions of files.

const gitignore = `# Written by chadash-node. Env files can hold secrets, so they never enter
# history. Secrets belong in Cha Dash as ${secret:name} references anyway.
.env
*.env
`

// Trailer keys.
const (
	trailerKind     = "Chadash-Kind"
	trailerStack    = "Chadash-Stack"
	trailerActor    = "Chadash-Actor"
	trailerPrevious = "Chadash-Previous-Image"
)

var signature = object.Signature{Name: "chadash-node", Email: "node@chadash.invalid"}

// repo is the stacks root's repository. Its methods serialise on mu: go-git
// isn't safe for concurrent writers.
type repo struct {
	root string
	now  func() time.Time
	mu   sync.Mutex
	r    *git.Repository // nil until the first commit creates it
}

func openRepo(root string, now func() time.Time) (*repo, error) {
	rp := &repo{root: root, now: now}
	r, err := git.PlainOpen(root)
	switch {
	case err == nil:
		rp.r = r
	case errors.Is(err, git.ErrRepositoryNotExists):
	default:
		return nil, fmt.Errorf("open the stacks repo: %w", err)
	}
	return rp, nil
}

// ensure creates the repository on first use, with a .gitignore that keeps
// env files out. Callers hold mu.
func (rp *repo) ensure() error {
	if rp.r != nil {
		return nil
	}
	if err := os.MkdirAll(rp.root, 0o755); err != nil {
		return err
	}
	r, err := git.PlainInit(rp.root, false)
	if err != nil {
		return fmt.Errorf("create the stacks repo: %w", err)
	}
	rp.r = r
	ignore := filepath.Join(rp.root, ".gitignore")
	content, err := os.ReadFile(ignore)
	if errors.Is(err, os.ErrNotExist) {
		content = []byte(gitignore)
		if err := os.WriteFile(ignore, content, 0o644); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err = rp.commitLocked(change{writes: map[string][]byte{".gitignore": content}}, "Start the stacks repo", nil)
	return err
}

// change is what a commit stages: files to write (by path relative to the
// root) and paths to drop from the index. It never touches the disk; the
// caller writes the real files first.
type change struct {
	writes map[string][]byte
	drops  []string
}

// commit stages c and commits it with msg and trailers (key, value pairs,
// in order). It creates the repository on first use.
func (rp *repo) commit(c change, msg string, trailers [][2]string) (plumbing.Hash, error) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if err := rp.ensure(); err != nil {
		return plumbing.ZeroHash, err
	}
	return rp.commitLocked(c, msg, trailers)
}

func (rp *repo) commitLocked(c change, msg string, trailers [][2]string) (plumbing.Hash, error) {
	idx, err := rp.r.Storer.Index()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	now := rp.now()
	for _, path := range sortedKeys(c.writes) {
		content := c.writes[path]
		obj := rp.r.Storer.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		w, err := obj.Writer()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if _, err := w.Write(content); err != nil {
			return plumbing.ZeroHash, err
		}
		if err := w.Close(); err != nil {
			return plumbing.ZeroHash, err
		}
		h, err := rp.r.Storer.SetEncodedObject(obj)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		e, err := idx.Entry(path)
		if errors.Is(err, index.ErrEntryNotFound) {
			e = idx.Add(path)
		} else if err != nil {
			return plumbing.ZeroHash, err
		}
		e.Hash, e.Mode, e.Size, e.ModifiedAt, e.CreatedAt = h, filemode.Regular, uint32(len(content)), now, now
	}
	for _, path := range c.drops {
		if _, err := idx.Remove(path); err != nil && !errors.Is(err, index.ErrEntryNotFound) {
			return plumbing.ZeroHash, err
		}
	}
	slices.SortFunc(idx.Entries, func(a, b *index.Entry) int { return strings.Compare(a.Name, b.Name) })
	if err := rp.r.Storer.SetIndex(idx); err != nil {
		return plumbing.ZeroHash, err
	}
	wt, err := rp.r.Worktree()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	var b strings.Builder
	b.WriteString(oneLine(msg))
	if len(trailers) > 0 {
		b.WriteString("\n\n")
		for _, t := range trailers {
			fmt.Fprintf(&b, "%s: %s\n", t[0], oneLine(t[1]))
		}
	}
	sig := signature
	sig.When = now
	return wt.Commit(b.String(), &git.CommitOptions{Author: &sig, Committer: &sig})
}

func mapsKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// oneLine keeps a commit subject or trailer value on one line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (rp *repo) headLocked() (*object.Commit, error) {
	if rp.r == nil {
		return nil, nil
	}
	ref, err := rp.r.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rp.r.CommitObject(ref.Hash())
}

// file returns path's content in commit c (HEAD when c is nil), and whether
// it's there.
func (rp *repo) file(c *object.Commit, path string) (string, bool, error) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if c == nil {
		var err error
		if c, err = rp.headLocked(); err != nil || c == nil {
			return "", false, err
		}
	}
	f, err := c.File(path)
	if errors.Is(err, object.ErrFileNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	r, err := f.Reader()
	if err != nil {
		return "", false, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, maxFile+1))
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// commitByHash finds a commit by its full or abbreviated (≥ 7) hash.
func (rp *repo) commitByHash(h string) (*object.Commit, error) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if rp.r == nil || len(h) < 7 || len(h) > 40 || strings.Trim(strings.ToLower(h), "0123456789abcdef") != "" {
		return nil, stackproto.Errorf(stackproto.CodeNotFound, "There's no commit %q in the stacks repo.", h)
	}
	if len(h) == 40 {
		c, err := rp.r.CommitObject(plumbing.NewHash(h))
		if err != nil {
			return nil, stackproto.Errorf(stackproto.CodeNotFound, "There's no commit %s in the stacks repo.", h[:7])
		}
		return c, nil
	}
	hash, err := rp.r.ResolveRevision(plumbing.Revision(h))
	if err != nil {
		return nil, stackproto.Errorf(stackproto.CodeNotFound, "There's no commit %q in the stacks repo.", h)
	}
	return rp.r.CommitObject(*hash)
}

// history lists the commits that touched path (or name a stack in their
// trailer), newest first, following first parents from HEAD.
func (rp *repo) history(stack, path string, limit int) ([]*object.Commit, error) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	c, err := rp.headLocked()
	if err != nil || c == nil {
		return nil, err
	}
	var out []*object.Commit
	for c != nil && len(out) < limit {
		touched := trailer(c.Message, trailerStack) == stack
		if !touched {
			if touched, err = changes(c, path); err != nil {
				return nil, err
			}
		}
		if touched {
			out = append(out, c)
		}
		if len(c.ParentHashes) == 0 {
			break
		}
		if c, err = rp.r.CommitObject(c.ParentHashes[0]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// changes reports whether c changed path relative to its first parent.
func changes(c *object.Commit, path string) (bool, error) {
	cur, err := blobHash(c, path)
	if err != nil {
		return false, err
	}
	if len(c.ParentHashes) == 0 {
		return cur != plumbing.ZeroHash, nil
	}
	p, err := c.Parent(0)
	if err != nil {
		return false, err
	}
	prev, err := blobHash(p, path)
	if err != nil {
		return false, err
	}
	return cur != prev, nil
}

func blobHash(c *object.Commit, path string) (plumbing.Hash, error) {
	t, err := c.Tree()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	e, err := t.FindEntry(path)
	if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
		return plumbing.ZeroHash, nil
	}
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return e.Hash, nil
}

// pinsAfter finds the images commit target last ran: the
// Chadash-Previous-Image trailers of the first deploy after it on HEAD's
// first-parent chain. Nil when target is HEAD or nothing recorded them.
func (rp *repo) pinsAfter(stack string, target plumbing.Hash) (map[string]string, error) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	c, err := rp.headLocked()
	if err != nil || c == nil {
		return nil, err
	}
	var pins map[string]string
	for c != nil && c.Hash != target {
		if trailer(c.Message, trailerStack) == stack {
			if p := previousImages(c.Message); len(p) > 0 {
				pins = p // keep walking: the earliest one after target wins
			}
		}
		if len(c.ParentHashes) == 0 {
			return nil, nil // target isn't on the chain
		}
		if c, err = rp.r.CommitObject(c.ParentHashes[0]); err != nil {
			return nil, err
		}
	}
	return pins, nil
}

// trailer returns the last value of key in a commit message.
func trailer(msg, key string) string {
	v := ""
	for line := range strings.SplitSeq(msg, "\n") {
		if k, val, ok := strings.Cut(line, ": "); ok && k == key {
			v = strings.TrimSpace(val)
		}
	}
	return v
}

// previousImages reads the Chadash-Previous-Image trailers: service → ref.
func previousImages(msg string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(msg, "\n") {
		if k, val, ok := strings.Cut(line, ": "); ok && k == trailerPrevious {
			if svc, ref, ok := strings.Cut(strings.TrimSpace(val), "="); ok && svc != "" && ref != "" {
				out[svc] = ref
			}
		}
	}
	return out
}

// toCommit is a commit as the wire shows it.
func toCommit(c *object.Commit) stackproto.Commit {
	subject, _, _ := strings.Cut(c.Message, "\n")
	kind := trailer(c.Message, trailerKind)
	switch kind {
	case stackproto.KindDeploy, stackproto.KindRollback, stackproto.KindAdopt, stackproto.KindUnadopt, stackproto.KindAccept:
	default:
		kind = stackproto.KindOther
	}
	return stackproto.Commit{
		Commit: c.Hash.String(), Short: c.Hash.String()[:7], Subject: subject,
		Author: trailer(c.Message, trailerActor), At: c.Author.When.UTC(), Kind: kind,
	}
}

// tracked lists every path in HEAD's tree, for telling adopted stacks from
// directories nobody adopted.
func (rp *repo) tracked() (map[string]bool, error) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	out := map[string]bool{}
	c, err := rp.headLocked()
	if err != nil || c == nil {
		return out, err
	}
	t, err := c.Tree()
	if err != nil {
		return nil, err
	}
	err = t.Files().ForEach(func(f *object.File) error {
		out[f.Name] = true
		return nil
	})
	return out, err
}

func sortedKeys[V any](m map[string]V) []string {
	k := mapsKeys(m)
	slices.Sort(k)
	return k
}
