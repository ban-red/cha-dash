package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"dash.cha.sh/internal/nodeproto"
)

// DefaultHealthFile is where the node reports its state for the
// healthcheck. The node dials out and listens on no port, so the check
// reads a file instead of asking an endpoint; /tmp is the image's tmpfs.
const DefaultHealthFile = "/tmp/chadash-node.health"

// HealthEvery is how often the health file is refreshed, and HealthStale
// how old it may be before the check fails.
const (
	HealthEvery = 15 * time.Second
	HealthStale = time.Minute
)

// HealthReport is the health file's content.
type HealthReport struct {
	State  string    `json:"state"`            // connecting, connected, refused
	Detail string    `json:"detail,omitempty"` // the node ID, or the refusal's code
	At     time.Time `json:"at"`
}

// Health writes the node's state to a file, now and every HealthEvery. A
// nil *Health does nothing.
type Health struct {
	path string
	now  func() time.Time

	mu     sync.Mutex
	report HealthReport
}

// NewHealth reports to path.
func NewHealth(path string) *Health {
	return &Health{path: path, now: time.Now, report: HealthReport{State: StateConnecting}}
}

// Set records a new state and writes it.
func (h *Health) Set(state, detail string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.report.State, h.report.Detail = state, detail
	h.mu.Unlock()
	_ = h.write()
}

func (h *Health) write() error {
	h.mu.Lock()
	r := h.report
	h.mu.Unlock()
	r.At = h.now().UTC()
	b, _ := json.Marshal(r)
	return nodeproto.WriteFileAtomic(h.path, b, 0o600)
}

// Run refreshes the file until ctx ends, then removes it.
func (h *Health) Run(ctx context.Context) {
	if h == nil {
		return
	}
	t := time.NewTicker(HealthEvery)
	defer t.Stop()
	_ = h.write()
	for {
		select {
		case <-ctx.Done():
			_ = os.Remove(h.path)
			return
		case <-t.C:
			_ = h.write()
		}
	}
}

// CheckHealth reads the health file: an error when it's missing or stale
// (the node is stuck or gone), else the report. A node that's connecting
// or refused is still healthy as a process; the report says which.
func CheckHealth(path string, now time.Time) (HealthReport, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return HealthReport{}, errors.New("the node isn't running (no health file)")
	}
	if err != nil {
		return HealthReport{}, err
	}
	var r HealthReport
	if err := json.Unmarshal(b, &r); err != nil {
		return HealthReport{}, fmt.Errorf("unreadable health file: %w", err)
	}
	if age := now.Sub(r.At); age > HealthStale {
		return r, fmt.Errorf("the node last reported %s ago", age.Round(time.Second))
	}
	return r, nil
}
