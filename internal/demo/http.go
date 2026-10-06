package demo

import (
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

// Register mounts the demo endpoint, wrapped by wrap (the hub's session
// check). Outside demo mode it isn't mounted, so it 404s. Incident fixes
// aren't an endpoint of their own: they're engine actions (Driver.Actions),
// planned and approved through /api/actions/plan like any other.
//
//	GET  /api/demo                          {"enabled": true, "now": RFC 3339, "seed": n}
func (d *Driver) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/demo", wrap(http.HandlerFunc(d.handleInfo)))
}

func (d *Driver) handleInfo(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, Info{Enabled: true, Now: d.Now().UTC().Format(time.RFC3339), Seed: d.Seed()})
}
