package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type node struct {
	root    string
	cli     command.Cli
	compose api.Compose
	wait    time.Duration
	soak    time.Duration
}

func newNode(root string, wait, soak time.Duration) (*node, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	cli, err := command.NewDockerCli()
	if err != nil {
		return nil, err
	}
	if err := cli.Initialize(&flags.ClientOptions{}); err != nil {
		return nil, err
	}
	svc, err := compose.NewComposeService(cli,
		compose.WithPrompt(compose.AlwaysOkPrompt()),
		compose.WithEventProcessor(newEventLog()),
	)
	if err != nil {
		return nil, err
	}
	return &node{root: root, cli: cli, compose: svc, wait: wait, soak: soak}, nil
}

// stack is a compose project as the node sees it.
type stack struct {
	name   string
	status string
	files  []string // absolute compose file paths, from the project's labels
	rel    string   // the single compose file relative to the stacks root; "" if outside it
}

func (s stack) file() string { return s.files[0] }

// adoptable says why the node can't own this stack in place, or nil if it can.
func (s stack) adoptable() error {
	switch {
	case len(s.files) != 1:
		return fmt.Errorf("%s uses %d compose files; the spike handles exactly one", s.name, len(s.files))
	case s.rel == "":
		return fmt.Errorf("%s lives outside the stacks root (%s); importing would copy it in", s.name, s.files[0])
	}
	return nil
}

func (n *node) stacks(ctx context.Context) ([]stack, error) {
	list, err := n.compose.List(ctx, api.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]stack, 0, len(list))
	for _, l := range list {
		s := stack{name: l.Name, status: l.Status}
		for f := range strings.SplitSeq(l.ConfigFiles, ",") {
			if f = strings.TrimSpace(f); f != "" {
				s.files = append(s.files, f)
			}
		}
		if len(s.files) == 1 {
			s.rel = n.relToRoot(s.files[0])
		}
		out = append(out, s)
	}
	return out, nil
}

// findStack looks the stack up among compose projects Docker knows about, falling back to
// <root>/<name>/compose.yaml so an owned stack that is fully down can still be deployed.
func (n *node) findStack(ctx context.Context, name string) (stack, error) {
	all, err := n.stacks(ctx)
	if err != nil {
		return stack{}, err
	}
	for _, s := range all {
		if s.name == name {
			return s, nil
		}
	}
	f := filepath.Join(n.root, name, "compose.yaml")
	if _, err := os.Stat(f); err == nil {
		return stack{name: name, status: "down", files: []string{f}, rel: n.relToRoot(f)}, nil
	}
	return stack{}, fmt.Errorf("no compose project named %q (and no %s)", name, f)
}

func (n *node) relToRoot(path string) string {
	rel, err := filepath.Rel(n.root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// ── stacks repo ──────────────────────────────────────────────────────────────

const gitignore = `# Written by chadash-node. Env files hold secrets, so they never enter history;
# the node records which env files a deploy used by content hash instead.
.env
*.env
`

// repo opens the stacks repo, creating it (with a .gitignore that keeps secrets out) if asked.
func (n *node) repo(create bool) (*git.Repository, error) {
	r, err := git.PlainOpen(n.root)
	if err == nil || !create || !errors.Is(err, git.ErrRepositoryNotExists) {
		return r, err
	}
	if r, err = git.PlainInit(n.root, false); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(n.root, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		return nil, err
	}
	if _, err := commit(r, "Start the stacks repo", ".gitignore"); err != nil {
		return nil, err
	}
	return r, nil
}

func commit(r *git.Repository, msg string, paths ...string) (plumbing.Hash, error) {
	wt, err := r.Worktree()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	for _, p := range paths {
		if _, err := wt.Add(p); err != nil {
			return plumbing.ZeroHash, err
		}
	}
	return wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{
		Name: "chadash-node (spike)", Email: "node@chadash.invalid", When: time.Now(),
	}})
}

// headFile returns rel's content at HEAD, whether it's tracked, and the HEAD commit.
func headFile(r *git.Repository, rel string) (content string, tracked bool, head plumbing.Hash, err error) {
	ref, err := r.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", false, plumbing.ZeroHash, nil
	}
	if err != nil {
		return "", false, plumbing.ZeroHash, err
	}
	c, err := r.CommitObject(ref.Hash())
	if err != nil {
		return "", false, ref.Hash(), err
	}
	f, err := c.File(rel)
	if errors.Is(err, object.ErrFileNotFound) {
		return "", false, ref.Hash(), nil
	}
	if err != nil {
		return "", false, ref.Hash(), err
	}
	content, err = f.Contents()
	return content, err == nil, ref.Hash(), err
}

func short(h plumbing.Hash) string { return h.String()[:7] }

// ── commands ─────────────────────────────────────────────────────────────────

func (n *node) scan(ctx context.Context) error {
	all, err := n.stacks(ctx)
	if err != nil {
		return err
	}
	r, err := n.repo(false)
	repoState := "a git repo"
	if errors.Is(err, git.ErrRepositoryNotExists) {
		r, repoState = nil, "not a git repo yet; the first adopt creates it"
	} else if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STACK\tSTATUS\tCOMPOSE FILE\tNODE")
	for _, s := range all {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.name, s.status, strings.Join(s.files, ","), verdict(r, s))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("\nstacks root: %s (%s)\n", n.root, repoState)
	return nil
}

func verdict(r *git.Repository, s stack) string {
	if err := s.adoptable(); err != nil {
		if s.rel == "" && len(s.files) == 1 {
			return "outside the root: import would copy it in"
		}
		return "skip: several compose files"
	}
	if r == nil {
		return "adoptable in place"
	}
	head, tracked, _, err := headFile(r, s.rel)
	switch {
	case err != nil:
		return "error: " + err.Error()
	case !tracked:
		return "adoptable in place"
	}
	if disk, err := os.ReadFile(s.file()); err == nil && string(disk) != head {
		return "owned · drifted (edited outside the node)"
	}
	return "owned"
}

func (n *node) adopt(ctx context.Context, name string) error {
	s, err := n.findStack(ctx, name)
	if err != nil {
		return err
	}
	if err := s.adoptable(); err != nil {
		return err
	}
	r, err := n.repo(true)
	if err != nil {
		return err
	}
	disk, err := os.ReadFile(s.file())
	if err != nil {
		return err
	}
	head, tracked, _, err := headFile(r, s.rel)
	if err != nil {
		return err
	}
	if tracked && head == string(disk) {
		fmt.Printf("%s is already owned (%s)\n", name, s.rel)
		return nil
	}
	msg := "Adopt " + name + " in place"
	if tracked {
		msg = "Re-adopt " + name + ": accept edits made outside the node"
	}
	h, err := commit(r, msg, s.rel)
	if err != nil {
		return err
	}
	fmt.Printf("✓ adopted %s · %s @ %s\n", name, s.rel, short(h))
	fmt.Println("  Nothing was restarted; the project name and its volumes are untouched. Deploys now go through git.")
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.file()), ".env")); err == nil {
		fmt.Println("  .env stays out of history (secrets).")
	}
	return nil
}

func (n *node) history(ctx context.Context, name string) error {
	s, err := n.findStack(ctx, name)
	if err != nil {
		return err
	}
	r, err := n.repo(false)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		return fmt.Errorf("%s isn't owned yet: run adopt first", name)
	} else if err != nil {
		return err
	}
	iter, err := r.Log(&git.LogOptions{PathFilter: func(p string) bool { return p == s.rel }})
	if err != nil {
		return err
	}
	defer iter.Close()
	return iter.ForEach(func(c *object.Commit) error {
		subject, _, _ := strings.Cut(c.Message, "\n")
		fmt.Printf("%s  %s  %s\n", short(c.Hash), c.Author.When.Local().Format(time.DateTime), subject)
		return nil
	})
}
