package backups

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
)

// Bounds of an objective someone sets: an hour to a year and a day.
const (
	MinObjective = time.Hour
	MaxObjective = 366 * 24 * time.Hour
)

// Errors of SetObjective and ClearObjective.
var (
	ErrNotProtectable = errors.New("backups: not on the coverage map")
	ErrOutOfRange     = errors.New("backups: objective out of range")
	ErrNotSet         = errors.New("backups: no objective set")
)

// Options configure a Service.
type Options struct {
	// Topics is the set whose graph it reads and whose backups topic it
	// writes (in demo mode the Demo Home's, like the cause engine).
	Topics *topics.Set
	// Store holds the objectives (backup_objectives). Nil keeps them in
	// memory only.
	Store *store.Store
	Log   *slog.Logger
	// Now is the clock (nil: time.Now); demo mode passes the simulator's.
	Now func() time.Time
	// Debounce is how long Run waits after a graph change (default 1 s);
	// Every how often it runs regardless, since freshness moves with the
	// clock (default 1 minute).
	Debounce, Every time.Duration
}

// Service keeps the backups topic current and holds the objectives. Safe
// for concurrent use.
type Service struct {
	o   Options
	log *slog.Logger
	// Audit records objective changes. Set by the server.
	Audit *authz.Recorder

	mu         sync.Mutex
	objectives map[string]Objective
	items      map[string]Item
}

// New loads the objectives and returns a service. Call Run to start it.
func New(ctx context.Context, o Options) (*Service, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Debounce <= 0 {
		o.Debounce = time.Second
	}
	if o.Every <= 0 {
		o.Every = time.Minute
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{o: o, log: log, objectives: map[string]Objective{}, items: map[string]Item{}}
	if o.Store != nil {
		list, err := readObjectives(ctx, o.Store.Reader())
		if err != nil {
			return nil, err
		}
		for _, ob := range list {
			s.objectives[ob.Resource] = ob
		}
	}
	return s, nil
}

// Run recomputes the map whenever the graph changes, and every Every,
// until ctx ends.
func (s *Service) Run(ctx context.Context) {
	gn := make(chan struct{}, 1)
	gw := s.o.Topics.Graph.Watch(nil, gn, 0)
	defer gw.Close()
	tick := time.NewTicker(s.o.Every)
	defer tick.Stop()
	for {
		if err := s.Sync(); err != nil && ctx.Err() == nil {
			s.log.Warn("backup coverage failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-gn:
		case <-tick.C:
		}
		t := time.NewTimer(s.o.Debounce)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if _, _, err := gw.Drain(); err != nil {
			gw.Snapshot()
		}
	}
}

// Sync recomputes the coverage map now and writes the topic.
func (s *Service) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, doc := s.o.Topics.Graph.Snapshot(nil)
	over := make(map[string]time.Duration, len(s.objectives))
	for id, ob := range s.objectives {
		over[id] = time.Duration(ob.MaxAgeMS) * time.Millisecond
	}
	items := Compute(doc, over, s.o.Now())
	_, err := s.o.Topics.Backups.Update(func(tx *livedoc.Tx) error {
		for id := range s.items {
			if _, ok := items[id]; !ok {
				tx.Delete(topics.BackupItems, id)
			}
		}
		for _, id := range slices.Sorted(maps.Keys(items)) {
			tx.Put(topics.BackupItems, id, items[id])
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.items = items
	return nil
}

// Item returns one resource's place on the map.
func (s *Service) Item(id string) (Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	return it, ok
}

// Objectives returns the objectives set, by resource.
func (s *Service) Objectives() []Objective {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Collect(maps.Values(s.objectives))
	slices.SortFunc(out, func(a, b Objective) int { return cmp.Compare(a.Resource, b.Resource) })
	return out
}

// SetObjective sets a resource's objective, replacing any it had, and
// recomputes the map. by is the user who set it ("" for none).
func (s *Service) SetObjective(ctx context.Context, resource string, maxAge time.Duration, by string) (Objective, error) {
	if maxAge < MinObjective || maxAge > MaxObjective {
		return Objective{}, ErrOutOfRange
	}
	if _, ok := s.Item(resource); !ok {
		return Objective{}, ErrNotProtectable
	}
	ob := Objective{Resource: resource, MaxAgeMS: maxAge.Milliseconds(), SetBy: by, SetAt: s.o.Now().UTC().Truncate(time.Millisecond)}
	if s.o.Store != nil {
		err := s.o.Store.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO backup_objectives (resource, max_age_ms, set_by, set_at) VALUES (?, ?, NULLIF(?, ''), ?)
				ON CONFLICT (resource) DO UPDATE SET max_age_ms = excluded.max_age_ms, set_by = excluded.set_by, set_at = excluded.set_at`,
				ob.Resource, ob.MaxAgeMS, by, ob.SetAt.UnixMilli())
			return err
		})
		if err != nil {
			return Objective{}, err
		}
	}
	s.mu.Lock()
	s.objectives[resource] = ob
	s.mu.Unlock()
	return ob, s.Sync()
}

// ClearObjective takes a resource's objective away, so its schedule's
// default applies again. ErrNotSet when it had none.
func (s *Service) ClearObjective(ctx context.Context, resource string) (Objective, error) {
	s.mu.Lock()
	ob, ok := s.objectives[resource]
	s.mu.Unlock()
	if !ok {
		return Objective{}, ErrNotSet
	}
	if s.o.Store != nil {
		err := s.o.Store.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM backup_objectives WHERE resource = ?`, resource)
			return err
		})
		if err != nil {
			return Objective{}, err
		}
	}
	s.mu.Lock()
	delete(s.objectives, resource)
	s.mu.Unlock()
	return ob, s.Sync()
}

func readObjectives(ctx context.Context, db *sql.DB) ([]Objective, error) {
	rows, err := db.QueryContext(ctx, `SELECT resource, max_age_ms, COALESCE(set_by, ''), set_at FROM backup_objectives ORDER BY resource`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Objective
	for rows.Next() {
		var ob Objective
		var at int64
		if err := rows.Scan(&ob.Resource, &ob.MaxAgeMS, &ob.SetBy, &at); err != nil {
			return nil, err
		}
		ob.SetAt = time.UnixMilli(at).UTC()
		out = append(out, ob)
	}
	return out, rows.Err()
}
