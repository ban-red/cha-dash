package demo

import (
	"errors"
	"net/http"
	"time"

	"dash.cha.sh/internal/httpx"
)

// Info is the body of GET /api/demo.
type Info struct {
	Enabled bool   `json:"enabled"`
	Now     string `json:"now"` // simulated time, RFC 3339
	Seed    int64  `json:"seed"`
}

// Register mounts the demo endpoints, each wrapped by wrap (the hub's
// owner-session check). Outside demo mode they aren't mounted, so they 404.
//
//	GET  /api/demo                          {"enabled": true, "now": RFC 3339, "seed": n}
//	POST /api/demo/incidents/{id}/fix       202 {"id"}; 404 unknown incident; 409 can't be fixed now (deprecated)
func (d *Driver) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/demo", wrap(http.HandlerFunc(d.handleInfo)))
	mux.Handle("POST /api/demo/incidents/{id}/fix", wrap(http.HandlerFunc(d.handleFix)))
}

func (d *Driver) handleInfo(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, Info{Enabled: true, Now: d.Now().UTC().Format(time.RFC3339), Seed: d.Seed()})
}

// handleFix presses an incident's suggested action directly.
//
// Deprecated: the Demo Home's fixes are actions on the engine now (see
// Driver.Actions), planned and approved through /api/actions/plan like any
// other. This endpoint stays until the web app moves over, then goes.
func (d *Driver) handleFix(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := d.Fix(id)
	switch {
	case errors.Is(err, ErrUnknownIncident):
		httpx.Error(w, http.StatusNotFound, "not_found", "no such incident")
	case err != nil:
		httpx.Error(w, http.StatusConflict, "conflict", err.Error())
	default:
		// Accepted: the fix has started. The incident's progress and
		// resolution arrive on the inbox topic.
		httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"id": id})
	}
}
