package action

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/store"
)

// Plan and step-up timing.
const (
	// PlanTTL is how long a plan can be approved.
	PlanTTL = 2 * time.Minute
	// StepUpTTL is how long a step-up grant lasts. T4 wants a grant made
	// after the plan, so the engine infers when a grant began from when it
	// ends.
	StepUpTTL = 5 * time.Minute
	// RecentTasks is how far back the tasks topic reaches, and MaxTasks
	// how many tasks it holds at most (running ones first).
	RecentTasks = 24 * time.Hour
	MaxTasks    = 50
)

// Retention of the engine's working tables. The audit log keeps the record.
const (
	planRetention        = 24 * time.Hour      // after an unapproved plan expires
	taskRetention        = 30 * 24 * time.Hour // tasks, and the plans they ran
	idempotencyRetention = 24 * time.Hour
)

// Options configure an Engine.
type Options struct {
	Store    *store.Store
	Audit    *audit.Log
	Registry *Registry
	Policy   Policy
	// Graph resolves targets and walks blast radii for Defs that don't do
	// it themselves.
	Graph *graph.Graph
	// Sessions turns the signed-in session into an Actor, for the HTTP
	// routes and the tasks topic.
	Sessions Sessions
	Log      *slog.Logger
	Now      func() time.Time // nil means time.Now
}

// Engine runs the lifecycle. It's safe for concurrent use.
type Engine struct {
	st       *store.Store
	audit    *audit.Log
	reg      *Registry
	policy   Policy
	graph    *graph.Graph
	sessions Sessions
	log      *slog.Logger
	now      func() time.Time

	// tasks is the tasks topic's document: {"tasks": {id: Task}}.
	tasks *livedoc.Doc

	ctx    context.Context // ends running tasks on Close
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	live   map[string]*liveTask // tasks in the topic, running or recent
	locks  map[string]string    // resource ID → the task holding it
	closed bool
}

// liveTask is a task the topic holds.
type liveTask struct {
	task    Task
	actorID string
	created time.Time
	cancel  context.CancelFunc // nil once finished
}

// tasksColl is the tasks topic's collection.
const tasksColl = "tasks"

// New starts an engine. Tasks the previous run left unfinished are marked
// failed (and audited) before it returns, and the last day's tasks fill
// the topic.
func New(ctx context.Context, o Options) (*Engine, error) {
	if o.Store == nil || o.Audit == nil || o.Registry == nil || o.Policy == nil {
		return nil, errors.New("action: Store, Audit, Registry and Policy are required")
	}
	e := &Engine{
		st: o.Store, audit: o.Audit, reg: o.Registry, policy: o.Policy, graph: o.Graph, sessions: o.Sessions, log: o.Log, now: o.Now,
		tasks: livedoc.New(tasksColl),
		live:  map[string]*liveTask{},
		locks: map[string]string{},
	}
	if e.log == nil {
		e.log = slog.New(slog.DiscardHandler)
	}
	if e.now == nil {
		e.now = time.Now
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	if err := e.recover(ctx); err != nil {
		return nil, err
	}
	if err := e.load(ctx); err != nil {
		return nil, err
	}
	return e, nil
}

// Registry is the engine's registry.
func (e *Engine) Registry() *Registry { return e.reg }

// Run prunes the topic and the working tables every hour until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		e.prune(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Close cancels running tasks and waits for them to record how they ended.
func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.cancel()
	e.wg.Wait()
}

// ── locks ────────────────────────────────────────────────────────────────

func (e *Engine) lock(resource, task string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, held := e.locks[resource]; held || e.closed {
		return false
	}
	e.locks[resource] = task
	return true
}

func (e *Engine) unlock(resource, task string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.locks[resource] == task {
		delete(e.locks, resource)
	}
}
