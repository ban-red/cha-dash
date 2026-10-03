package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/utils/diff"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/sergi/go-diff/diffmatchpatch"
)

var (
	errBlocked    = errors.New("blocked")     // preflight failed; nothing was touched
	errRolledBack = errors.New("rolled back") // gate failed; the previous version is running again
	errStuck      = errors.New("stuck")       // the rollback failed too
)

// change is what deploying a proposed compose file would do to a stack.
type change struct {
	stack     stack
	current   string
	proposed  string
	actions   []string // from the config-hash plan, e.g. "recreate web"; used in commit messages
	dryRunErr error    // Compose's dry run refused the proposal (e.g. the image doesn't exist)
}

// diff reports a proposal three ways: the text, a static config-hash comparison, and a dry run
// through Compose's own reconciler. The dry run also resolves image references against the
// registry, so it doubles as the deploy preflight.
func (n *node) diff(ctx context.Context, name, proposedPath string) (*change, error) {
	s, err := n.findStack(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := s.adoptable(); err != nil {
		return nil, err
	}
	cur, err := os.ReadFile(s.file())
	if err != nil {
		return nil, err
	}
	next, err := os.ReadFile(proposedPath)
	if err != nil {
		return nil, err
	}
	c := &change{stack: s, current: string(cur), proposed: string(next)}

	fmt.Printf("── text · %s\n", s.rel)
	if c.current == c.proposed {
		fmt.Println("  identical")
	} else {
		fmt.Print(lineDiff(c.current, c.proposed))
	}

	p, err := n.loadProposal(ctx, s, c.proposed)
	if err != nil {
		return nil, fmt.Errorf("the proposal doesn't load: %w", err)
	}

	fmt.Println("── config hash · ours vs the label on each running container")
	if c.actions, err = n.printHashPlan(ctx, s.name, p); err != nil {
		return nil, err
	}

	fmt.Println("── dry run · Compose's reconciler, nothing applied")
	dry, err := compose.NewComposeService(n.cli,
		compose.WithDryRun,
		compose.WithPrompt(compose.AlwaysOkPrompt()),
		compose.WithEventProcessor(newEventLog()),
	)
	if err != nil {
		return nil, err
	}
	if err := dry.Up(ctx, p, upOptions(p, 0)); err != nil {
		c.dryRunErr = err
		fmt.Printf("  ✗ would fail: %v\n", err)
	}
	return c, nil
}

// loadProposal loads the proposed file as if it were the stack's own: same directory and
// project name, so relative paths, env files and volumes resolve as they will after a deploy.
func (n *node) loadProposal(ctx context.Context, s stack, content string) (*types.Project, error) {
	dir := filepath.Dir(s.file())
	tmp, err := os.CreateTemp(dir, ".chadash-proposal-*.yaml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	return n.compose.LoadProject(ctx, api.ProjectLoadOptions{
		ProjectName: s.name, ConfigPaths: []string{tmp.Name()}, WorkingDir: dir,
	})
}

// printHashPlan predicts recreations without touching Docker: Compose stamps each container
// with a hash of its service config, so equal hashes mean "left alone".
func (n *node) printHashPlan(ctx context.Context, name string, p *types.Project) ([]string, error) {
	running, err := n.compose.Ps(ctx, name, api.PsOptions{All: true})
	if err != nil {
		return nil, err
	}
	var actions []string
	labels := map[string]string{}
	for _, c := range running {
		labels[c.Service] = c.Labels[api.ConfigHashLabel]
	}
	for _, svc := range slices.Sorted(maps.Keys(p.Services)) {
		h, err := compose.ServiceHash(p.Services[svc])
		if err != nil {
			return nil, err
		}
		old, ok := labels[svc]
		delete(labels, svc)
		switch {
		case !ok:
			fmt.Printf("  + %-12s create\n", svc)
			actions = append(actions, "create "+svc)
		case old == h:
			fmt.Printf("  = %-12s unchanged (%.12s)\n", svc, h)
		default:
			fmt.Printf("  ~ %-12s recreate  (%.12s → %.12s)\n", svc, old, h)
			actions = append(actions, "recreate "+svc)
		}
	}
	for _, svc := range slices.Sorted(maps.Keys(labels)) {
		fmt.Printf("  - %-12s remove (no longer in the file)\n", svc)
		actions = append(actions, "remove "+svc)
	}
	return actions, nil
}

// deploy commits the proposal, applies it, and holds it to the health gate. If the gate fails,
// the previous version is restored as a new commit, so failed attempts stay in the history.
func (n *node) deploy(ctx context.Context, name, proposedPath string) error {
	r, err := n.repo(false)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		return fmt.Errorf("%s isn't owned yet: run adopt first", name)
	} else if err != nil {
		return err
	}
	s, err := n.findStack(ctx, name)
	if err != nil {
		return err
	}
	if err := s.adoptable(); err != nil {
		return err
	}
	prev, tracked, from, err := headFile(r, s.rel)
	if err != nil {
		return err
	}
	if !tracked {
		return fmt.Errorf("%s isn't owned yet: run adopt first", name)
	}
	if disk, err := os.ReadFile(s.file()); err != nil {
		return err
	} else if string(disk) != prev {
		return fmt.Errorf("%s was edited outside the node since the last deploy; adopt again to accept the edits", s.rel)
	}

	c, err := n.diff(ctx, name, proposedPath)
	if err != nil {
		return err
	}
	if c.proposed == prev {
		fmt.Println("nothing to deploy")
		return nil
	}
	if c.dryRunErr != nil {
		return fmt.Errorf("%w: the dry run failed, so nothing was changed: %v", errBlocked, c.dryRunErr)
	}

	fmt.Println("── deploy")
	if err := os.WriteFile(s.file(), []byte(c.proposed), 0o644); err != nil {
		return err
	}
	to, err := commit(r, fmt.Sprintf("Deploy %s: %s", name, strings.Join(c.actions, ", ")), s.rel)
	if err != nil {
		return err
	}
	fmt.Printf("  committed %s\n", short(to))
	start := time.Now()
	gateErr := n.upAndGate(ctx, s)
	if gateErr == nil {
		fmt.Printf("✓ %s deployed and healthy in %s (including a %s soak)\n", name, since(start), n.soak)
		return nil
	}

	// Roll back even if the deploy was interrupted.
	ctx = context.WithoutCancel(ctx)
	fmt.Printf("✗ health gate failed after %s: %v\n── rollback to %s\n", since(start), gateErr, short(from))
	if err := os.WriteFile(s.file(), []byte(prev), 0o644); err != nil {
		return fmt.Errorf("%w: couldn't restore %s: %v", errStuck, s.rel, err)
	}
	back, err := commit(r, fmt.Sprintf("Roll back %s: %v", name, gateErr), s.rel)
	if err != nil {
		return fmt.Errorf("%w: couldn't commit the rollback: %v", errStuck, err)
	}
	if err := n.upAndGate(ctx, s); err != nil {
		return fmt.Errorf("%w: %s is back on %s but not healthy: %v", errStuck, name, short(back), err)
	}
	return fmt.Errorf("%w: %s is healthy on the previous version again (%s), %s after the deploy started",
		errRolledBack, name, short(back), since(start))
}

// upAndGate applies the stack's file and holds it to the gate: up --wait (running, and healthy
// where a healthcheck exists), then a soak that catches crash loops --wait can miss.
func (n *node) upAndGate(ctx context.Context, s stack) error {
	p, err := n.compose.LoadProject(ctx, api.ProjectLoadOptions{
		ProjectName: s.name, ConfigPaths: []string{s.file()}, WorkingDir: filepath.Dir(s.file()),
	})
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if err := n.compose.Up(ctx, p, upOptions(p, n.wait)); err != nil {
		return err
	}
	return n.soakCheck(ctx, s.name)
}

func upOptions(p *types.Project, wait time.Duration) api.UpOptions {
	return api.UpOptions{
		Create: api.CreateOptions{
			Recreate:             api.RecreateDiverged,
			RecreateDependencies: api.RecreateDiverged,
			RemoveOrphans:        true, // for an owned stack, the file is the whole truth
			QuietPull:            true,
		},
		Start: api.StartOptions{Project: p, Wait: wait > 0, WaitTimeout: wait},
	}
}

// soakCheck fails if any container stops running, turns unhealthy, or restarts. Polling the
// state alone misses crash loops that spend most of their time "running"; the restart count
// doesn't.
func (n *node) soakCheck(ctx context.Context, name string) error {
	if n.soak <= 0 {
		return nil
	}
	fmt.Printf("  soak: every container must stay up for %s\n", n.soak)
	end := time.Now().Add(n.soak)
	restarts := map[string]int{} // container ID → restart count when the soak first saw it
	for {
		cs, err := n.compose.Ps(ctx, name, api.PsOptions{All: true})
		if err != nil {
			return err
		}
		if len(cs) == 0 {
			return errors.New("no containers")
		}
		for _, c := range cs {
			switch {
			case c.State == container.StateExited:
				return fmt.Errorf("%s exited with code %d", c.Name, c.ExitCode)
			case c.State != container.StateRunning:
				return fmt.Errorf("%s is %s", c.Name, c.State)
			case c.Health == container.Unhealthy:
				return fmt.Errorf("%s is unhealthy", c.Name)
			}
			info, err := n.cli.Client().ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
			if err != nil {
				return err
			}
			rc := info.Container.RestartCount
			if first, seen := restarts[c.ID]; !seen {
				restarts[c.ID] = rc
			} else if rc > first {
				return fmt.Errorf("%s restarted %d× during the soak", c.Name, rc-first)
			}
		}
		if time.Now().After(end) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func since(t time.Time) time.Duration { return time.Since(t).Round(100 * time.Millisecond) }

// lineDiff renders a line diff, folding long unchanged runs.
func lineDiff(a, b string) string {
	var out strings.Builder
	for _, d := range diff.Do(a, b) {
		lines := strings.SplitAfter(d.Text, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		mark := "  "
		switch d.Type {
		case diffmatchpatch.DiffDelete:
			mark = "- "
		case diffmatchpatch.DiffInsert:
			mark = "+ "
		case diffmatchpatch.DiffEqual:
			if len(lines) > 6 {
				lines = slices.Concat(lines[:2], []string{"…"}, lines[len(lines)-2:])
			}
		}
		for _, l := range lines {
			out.WriteString("  " + mark + strings.TrimSuffix(l, "\n") + "\n")
		}
	}
	return out.String()
}

// eventLog prints Compose's progress events, minus the in-progress noise and repeats.
type eventLog struct {
	mu   sync.Mutex
	seen map[string]bool
}

func newEventLog() *eventLog { return &eventLog{seen: map[string]bool{}} }

// Start begins a new Compose operation; repeats are only folded within one operation.
func (e *eventLog) Start(context.Context, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = map[string]bool{}
}

func (e *eventLog) Done(string, bool) {}

func (e *eventLog) On(events ...api.Resource) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range events {
		if ev.Status == api.Working {
			continue
		}
		line := strings.TrimSpace(fmt.Sprintf("%s  %s %s", ev.ID, ev.Text, ev.Details))
		if e.seen[line] {
			continue
		}
		e.seen[line] = true
		mark := "·"
		switch ev.Status {
		case api.Warning:
			mark = "▲"
		case api.Error:
			mark = "✗"
		}
		fmt.Printf("  %s %s\n", mark, line)
	}
}
