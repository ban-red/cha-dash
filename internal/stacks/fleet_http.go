package stacks

import (
	"errors"
	"net/http"
	"strings"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/stackproto"
)

// registerFleet mounts the fleet routes (v0.3, wave 14):
//
//	POST /api/stacks/{node}/{stack}/move/preview  {from_path?, content?} → rewrites, plan, refusal; saves nothing (admin)
//	GET  /api/nodes/{id}/self-update              the last self-update and whether this node may run one (admin)
//
// Self-update lives under /api/nodes, not /api/stacks/{node}/…, where it
// would shadow a stack called self-update.
func (s *Service) registerFleet(mux *http.ServeMux, session func(http.Handler) http.Handler) {
	mux.Handle("POST /api/stacks/{node}/{stack}/move/preview", session(http.HandlerFunc(s.handleMovePreview)))
	mux.Handle("GET /api/nodes/{id}/self-update", session(http.HandlerFunc(s.handleSelfUpdateStatus)))
}

// MovePreview is POST …/move/preview: what moving the stack into the root
// would do, or why it can't.
type MovePreview struct {
	OK       bool                 `json:"ok"`
	Rewrites []stackproto.Rewrite `json:"rewrites"`
	Plan     *stackproto.Plan     `json:"plan"`
	Path     string               `json:"path"` // where it would live
	Refusal  *httpError           `json:"refusal"`
}

func (s *Service) handleMovePreview(w http.ResponseWriter, r *http.Request) {
	node, name, ok := pathStack(w, r)
	if !ok {
		return
	}
	var req struct {
		FromPath string `json:"from_path"`
		Content  string `json:"content"`
	}
	if !httpx.DecodeJSONLimit(w, r, &req, maxBody) {
		return
	}
	if len(req.Content) > 256<<10 {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "too_large", "A compose file can be at most 256 KiB.")
		return
	}
	if (req.FromPath == "") == (strings.TrimSpace(req.Content) == "") {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "Send exactly one of from_path and content.")
		return
	}
	var res stackproto.MoveResult
	err := s.call(r.Context(), node, stackproto.VerbMove, stackproto.MoveRequest{Stack: name, FromPath: req.FromPath, Content: req.Content, DryRun: true}, &res, s.timeout, nil)
	out := MovePreview{Rewrites: []stackproto.Rewrite{}}
	if e := stackproto.ErrorFrom(err); err != nil {
		var off errNodeOffline
		if e == nil || e.Code == stackproto.CodeInternal || errors.As(err, &off) || errors.As(err, new(errNodeUnknown)) {
			s.fail(w, err)
			return
		}
		// The node refused the move: say why, as a review would.
		out.Refusal = &httpError{Code: e.Code, Message: e.Message}
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	out.OK, out.Plan, out.Path = true, &res.Plan, res.Path
	if res.Rewrites != nil {
		out.Rewrites = res.Rewrites
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// SelfUpdateStatus is GET /api/nodes/{id}/self-update.
type SelfUpdateStatus struct {
	Node string `json:"node"`
	// Allowed: the node's policy grants stacks.selfupdate. Available: it
	// also runs beside the hub, so it can update it; Reason says why not.
	Allowed   bool   `json:"allowed"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Running is the image the hub runs now, as the node sees it.
	Running string `json:"running,omitempty"`
	// Last is the last self-update, if there was one.
	Last *stackproto.SelfUpdateResult `json:"last"`
}

func (s *Service) handleSelfUpdateStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !nodeIDRE.MatchString(id) {
		httpx.Error(w, http.StatusNotFound, "not_found", "There's no such node.")
		return
	}
	res, allowed, err := s.selfUpdateStatus(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := SelfUpdateStatus{Node: id, Allowed: allowed}
	if !allowed {
		n, _ := s.node(r.Context(), id)
		name := n.Name
		if name == "" {
			name = id
		}
		out.Reason = selfUpdateOff(name)
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	out.Available, out.Reason, out.Running = res.Available, res.Unavailable, res.Running
	if res.Outcome != "" {
		last := res
		last.Available, last.Unavailable, last.Running = false, "", ""
		out.Last = &last
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
