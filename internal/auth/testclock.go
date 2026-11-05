package auth

// The test clock (CHADASH_TEST_CLOCK, only with CHADASH_TEST_AUTOMATION;
// internal/clock): a test moves the hub's clock forward, so maintenance
// windows, notification grace, the digest and approval expiry happen in
// seconds. The automation key is the credential, as for signing in:
//
//	GET  /api/test/clock                                    → {now, skew_s}
//	POST /api/test/clock {"advance": "15m"} or {"set": "…"} → the same, after the move
//
// It only goes forward. Each move is logged.

import (
	"net/http"
	"time"

	"dash.cha.sh/internal/clock"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// EnableTestClock mounts /api/test/clock for c, a test clock. Call it after
// EnableAutomation and before Register.
func (s *Service) EnableTestClock(c *clock.Clock) {
	s.mu.Lock()
	s.testClock = c
	s.mu.Unlock()
	s.log.Warn("TEST CLOCK IS ON: the automation key can move the hub's clock forward; never on a hub that holds anything real")
}

func (s *Service) registerTestClock(mux *http.ServeMux) {
	s.mu.Lock()
	c := s.testClock
	s.mu.Unlock()
	if !c.Testing() {
		return
	}
	mux.HandleFunc("GET /api/test/clock", func(w http.ResponseWriter, r *http.Request) {
		if s.automationKeyOK(w, r) {
			writeClock(w, c)
		}
	})
	mux.HandleFunc("POST /api/test/clock", func(w http.ResponseWriter, r *http.Request) {
		if !s.automationKeyOK(w, r) {
			return
		}
		var req struct {
			Advance string    `json:"advance"`
			Set     time.Time `json:"set"`
		}
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}
		before := c.Now()
		var err error
		switch {
		case req.Advance != "" && req.Set.IsZero():
			var d time.Duration
			if d, err = time.ParseDuration(req.Advance); err == nil {
				err = c.Advance(d)
			}
		case req.Advance == "" && !req.Set.IsZero():
			err = c.Set(req.Set)
		default:
			err = clock.ErrBackwards
		}
		if err != nil {
			httpx.Say(w, http.StatusBadRequest, "invalid", msg.New("auth.test_clock_move"))
			return
		}
		s.log.Warn("test clock moved", "from", before.UTC().Format(time.RFC3339), "to", c.Now().UTC().Format(time.RFC3339))
		writeClock(w, c)
	})
}

func writeClock(w http.ResponseWriter, c *clock.Clock) {
	httpx.WriteJSON(w, http.StatusOK, struct {
		Now   time.Time `json:"now"`
		SkewS float64   `json:"skew_s"`
	}{c.Now().UTC(), c.Skew().Seconds()})
}
