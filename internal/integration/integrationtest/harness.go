// Package integrationtest runs one integration the way the hub does, for
// contract tests against recorded fixtures: the config is validated against
// the manifest, the instance gets the runtime's real HTTP clients (TLS
// policy included) and emitter, and what it publishes lands in a private
// graph, metrics and inbox the test can read back.
//
//	h := integrationtest.New(t)
//	inst := h.Connect(sonarr.Integration{}, map[string]any{"url": srv.URL}, map[string]string{"api_key": "fake"})
//	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil { … }
//	r := h.Resource("sonarr:" + h.ID + "/app")
package integrationtest

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"maps"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/topics"
)

// Watched is the Interest of a board that's open on every kind.
var Watched = integration.Interest{Watched: true, Metrics: true}

// Harness is one instance under test.
type Harness struct {
	t   *testing.T
	ID  string // the instance ID; resource IDs usually include it
	Set *topics.Set
	// Emitter is the instance's emitter. Its own resource is published, so
	// relations to Self have an endpoint.
	Emitter *runtime.Emitter
	// RootCAs are trusted in tls mode verify (e.g. an httptest TLS server's
	// certificate). Set before Connect.
	RootCAs *x509.CertPool
	// State is the instance's key-value state, in memory. Every Connect of
	// the harness shares it, so connecting again is a restart.
	State integration.State

	mu     sync.Mutex
	now    time.Time
	config map[string]any // what UpdateConfig persisted
	logs   bytes.Buffer
}

// New returns a harness for instance "i_test" with its clock at
// 2026-10-03 14:05 UTC, the Demo Home's moment.
func New(t *testing.T) *Harness {
	h := &Harness{t: t, ID: "i_test", now: time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC), State: runtime.NewMemoryState()}
	h.Set = topics.New(graph.New(), h.now)
	h.Emitter = runtime.NewEmitter(h.Set, h.ID, h.Now)
	if err := h.Emitter.PublishSelf(integration.Resource{Kind: "integration", Name: "Test", State: "connected",
		Health: integration.Health{Status: integration.HealthOK}}); err != nil {
		t.Fatal(err)
	}
	return h
}

// Now is the harness clock.
func (h *Harness) Now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

// Advance moves the clock.
func (h *Harness) Advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

// Validate checks config and secrets the way the instances API does and
// returns the problems.
func Validate(integ integration.Integration, config map[string]any, secrets map[string]string) []integration.FieldError {
	m := integ.Manifest()
	if err := integration.ValidateManifest(m); err != nil {
		return []integration.FieldError{{Message: err.Error()}}
	}
	full := merged(config, secrets)
	errs := m.ConfigSchema.Validate(full)
	if len(errs) == 0 {
		if _, err := integration.ParseTLS(full["tls"]); err != nil {
			errs = append(errs, integration.FieldError{Path: "/tls", Message: err.Error()})
		}
	}
	if len(errs) == 0 {
		errs = m.CheckControl(full)
	}
	if v, ok := integ.(integration.ConfigValidator); ok && len(errs) == 0 {
		errs = v.ValidateConfig(integration.Config{InstanceID: "i_test", Values: full})
	}
	return errs
}

func merged(config map[string]any, secrets map[string]string) map[string]any {
	full := map[string]any{}
	if config != nil {
		b, _ := json.Marshal(config) // normalise to decoded JSON, as the API sees it
		_ = json.Unmarshal(b, &full)
	}
	for k, v := range secrets {
		full[k] = v
	}
	return full
}

// Connect validates the config (failing the test on any problem) and
// connects. The instance is closed when the test ends.
func (h *Harness) Connect(integ integration.Integration, config map[string]any, secrets map[string]string) integration.Instance {
	h.t.Helper()
	if errs := Validate(integ, config, secrets); len(errs) > 0 {
		h.t.Fatalf("invalid config: %v", errs)
	}
	full := merged(config, secrets)
	h.mu.Lock()
	h.config = maps.Clone(full)
	for k := range secrets {
		delete(h.config, k)
	}
	h.mu.Unlock()
	cfg := integration.Config{InstanceID: h.ID, Name: "Test", Values: full}
	deps, _, release := runtime.NewDeps(runtime.DepsOptions{
		Config: cfg, Emit: h.Emitter, Now: h.Now, RootCAs: h.RootCAs, State: h.State,
		Log: slog.New(slog.NewTextHandler(&lockedWriter{h: h}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		UpdateConfig: func(_ context.Context, mutate func(map[string]any)) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			mutate(h.config)
			return nil
		},
	})
	h.t.Cleanup(release)
	inst, err := integ.Connect(h.t.Context(), cfg, deps)
	if err != nil {
		h.t.Fatalf("connect: %v", err)
	}
	h.t.Cleanup(func() { inst.Close() })
	return inst
}

type lockedWriter struct{ h *Harness }

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	return w.h.logs.Write(p)
}

// Logs returns what the instance logged.
func (h *Harness) Logs() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logs.String()
}

// StoredConfig is the non-secret config with whatever the instance
// persisted through UpdateConfig (TOFU pins, say).
func (h *Harness) StoredConfig() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, _ := json.Marshal(h.config)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// Resource returns a published resource.
func (h *Harness) Resource(id string) (graph.Resource, bool) { return h.Set.Graph.Get(graph.RID(id)) }

// Graph returns every published resource and relation.
func (h *Harness) Graph() graph.Doc {
	_, doc := h.Set.Graph.Snapshot(nil)
	return doc
}

// Series returns a metrics series as the wire carries it ({unit, value,
// history, stale, …}), or nil.
func (h *Harness) Series(resource, metric string) map[string]any {
	b := h.Set.Metrics.Item(topics.Series, topics.SeriesKey(resource, metric))
	if b == nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

// Incident returns an inbox incident by the ID the integration raised it
// under (without the instance prefix).
func (h *Harness) Incident(local string) (integration.Incident, bool) {
	b := h.Set.Inbox.Item(topics.Incidents, h.ID+"."+local)
	if b == nil {
		return integration.Incident{}, false
	}
	var in integration.Incident
	if err := json.Unmarshal(b, &in); err != nil {
		h.t.Fatal(err)
	}
	return in, true
}
