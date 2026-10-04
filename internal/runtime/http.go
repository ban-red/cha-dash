package runtime

import (
	"errors"
	"net/http"
	"runtime"
	"time"

	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/integration"
)

// Register mounts the integrations API, each route wrapped by wrap (the
// hub's owner-session check). Bodies are JSON; errors use the hub's
// envelope, and a 422 adds "fields": [{"path", "message"}].
//
//	GET    /api/integrations/catalog                    [{id, name, icon, version, tier, capabilities, config_schema}]
//	GET    /api/integrations/instances                  [instance]
//	POST   /api/integrations/instances                  {integration, name, config, secrets, enabled?} → 201 {id}
//	GET    /api/integrations/instances/{id}             instance
//	PUT    /api/integrations/instances/{id}             {name?, config?, secrets?, enabled?} → 200 instance
//	DELETE /api/integrations/instances/{id}             204
//	POST   /api/integrations/test                       {integration, config, secrets} or {instance_id, config?, secrets?} → test result
//	POST   /api/integrations/instances/{id}/test        test result
//	GET    /api/integrations/instances/{id}/diagnostics diagnostics, secrets redacted
//
// An instance is {id, integration, name, enabled, status, version, last_ok,
// last_error, warnings, config, secrets, created_at, updated_at}; secrets
// only ever appear as {"<field>": {"set": bool}}.
func (m *Manager) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	h := func(f http.HandlerFunc) http.Handler { return wrap(f) }
	mux.Handle("GET /api/integrations/catalog", h(m.handleCatalog))
	mux.Handle("GET /api/integrations/instances", h(m.handleList))
	mux.Handle("POST /api/integrations/instances", h(m.handleCreate))
	mux.Handle("GET /api/integrations/instances/{id}", h(m.handleGet))
	mux.Handle("PUT /api/integrations/instances/{id}", h(m.handleUpdate))
	mux.Handle("DELETE /api/integrations/instances/{id}", h(m.handleDelete))
	mux.Handle("POST /api/integrations/test", h(m.handleTest))
	mux.Handle("POST /api/integrations/instances/{id}/test", h(m.handleTestSaved))
	mux.Handle("GET /api/integrations/instances/{id}/diagnostics", h(m.handleDiagnostics))
}

// invalidBody is a 422 with the fields that failed.
type invalidBody struct {
	httpx.ErrorBody
	Fields []integration.FieldError `json:"fields"`
}

func (m *Manager) writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, invalidBody{
			ErrorBody: httpx.ErrorBody{Error: httpx.ErrorDetail{Code: "invalid", Message: ve.Error()}},
			Fields:    ve.Fields,
		})
	case errors.Is(err, ErrUnknownIntegration):
		httpx.Error(w, http.StatusUnprocessableEntity, "unknown_integration", "this hub doesn't have that integration")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not_found", "no such instance")
	case errors.Is(err, ErrNameTaken):
		httpx.Error(w, http.StatusConflict, "conflict", "another instance of this integration already has that name")
	default:
		m.log.Error("integrations API failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func (m *Manager) handleCatalog(w http.ResponseWriter, r *http.Request) {
	out := m.Catalog()
	for i := range out {
		if out[i].Capabilities == nil {
			out[i].Capabilities = []integration.Capability{}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Manager) handleList(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, m.List())
}

func (m *Manager) handleGet(w http.ResponseWriter, r *http.Request) {
	v, err := m.Get(r.PathValue("id"))
	if err != nil {
		m.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (m *Manager) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	id, err := m.Create(r.Context(), req)
	if err != nil {
		m.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (m *Manager) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req UpdateRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if err := m.Update(r.Context(), id, req); err != nil {
		m.writeError(w, err)
		return
	}
	v, err := m.Get(id)
	if err != nil {
		m.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (m *Manager) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := m.Delete(r.Context(), r.PathValue("id")); err != nil {
		m.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) handleTest(w http.ResponseWriter, r *http.Request) {
	var req TestRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := m.Test(r.Context(), req)
	if err != nil {
		m.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (m *Manager) handleTestSaved(w http.ResponseWriter, r *http.Request) {
	res, err := m.TestSaved(r.Context(), r.PathValue("id"))
	if err != nil {
		m.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (m *Manager) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	d, err := m.Diagnostics(r.PathValue("id"))
	if err != nil {
		m.writeError(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="chadash-diagnostics-`+d.Instance.ID+`.json"`)
	httpx.WriteJSON(w, http.StatusOK, d)
}

// Diagnostics is a snapshot of one instance for a bug report. It holds no
// secret values: secrets appear as {"set": bool}, and every message was
// scrubbed of secret values before it was recorded.
type Diagnostics struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Hub         hubInfo      `json:"hub"`
	Instance    InstanceView `json:"instance"`
	Manifest    manifestInfo `json:"manifest"`
	Schedule    scheduleInfo `json:"schedule"`
	TLS         tlsInfo      `json:"tls"`
	Facts       FactStats    `json:"facts"`
	Attempts    []Attempt    `json:"attempts"`
}

type hubInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Go        string `json:"go"`
	Sandboxed bool   `json:"sandboxed"` // demo mode: real instances write to a private graph
}

type manifestInfo struct {
	ID         string  `json:"id"`
	Version    string  `json:"version"`
	Tier       string  `json:"tier"`
	Engine     string  `json:"engine"`
	IntervalS  float64 `json:"interval_s"`
	IdleS      float64 `json:"idle_s"`
	TimeoutS   float64 `json:"timeout_s"`
	Background bool    `json:"background"`
}

type scheduleInfo struct {
	Watched             bool       `json:"watched"`
	IntervalS           float64    `json:"interval_s"`
	NextSync            *time.Time `json:"next_sync"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
}

type tlsInfo struct {
	Mode string `json:"mode"`
	Pin  string `json:"pin,omitempty"`
}

// Diagnostics returns the snapshot for one instance.
func (m *Manager) Diagnostics(id string) (Diagnostics, error) {
	mi := m.get(id)
	if mi == nil {
		return Diagnostics{}, ErrNotFound
	}
	r, _ := mi.snapshotRow()
	snap := mi.st.snapshot()
	d := Diagnostics{
		GeneratedAt: m.o.Now().UTC().Truncate(time.Second),
		Hub:         hubInfo{Version: buildinfo.Version, Commit: buildinfo.Commit, Go: runtime.Version(), Sandboxed: m.o.Sandboxed},
		Instance:    m.view(mi),
		Manifest: manifestInfo{
			ID: mi.man.ID, Version: mi.man.Version, Tier: string(mi.man.Tier), Engine: string(mi.man.Engine),
			IntervalS: mi.man.Poll.Interval.Seconds(), IdleS: mi.man.Poll.Idle.Seconds(),
			TimeoutS: mi.man.Poll.Timeout.Seconds(), Background: mi.man.Poll.Background,
		},
		Schedule: scheduleInfo{Watched: snap.Watched, IntervalS: snap.Interval.Seconds(), ConsecutiveFailures: snap.Failures},
		Facts:    mi.emit.Stats(),
		Attempts: snap.Attempts,
	}
	if !snap.NextSync.IsZero() && snap.Status != StatusDisabled {
		t := snap.NextSync.UTC().Truncate(time.Second)
		d.Schedule.NextSync = &t
	}
	if d.Attempts == nil {
		d.Attempts = []Attempt{}
	}
	if p, err := integration.ParseTLS(r.Config["tls"]); err == nil {
		d.TLS = tlsInfo{Mode: string(p.Mode), Pin: p.Pin}
	}
	return d, nil
}
