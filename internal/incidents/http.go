package incidents

import (
	"errors"
	"net/http"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// AckRequest is POST /api/inbox/{id}/acknowledge's body.
type AckRequest struct {
	// Note is optional, at most MaxNote characters: "on it, the UPS is
	// being replaced".
	Note string `json:"note,omitempty"`
}

// AckResponse answers both acknowledgement routes: the incident, and its
// acknowledgement (null once cleared).
type AckResponse struct {
	Incident     string `json:"incident"`
	Acknowledged *Ack   `json:"acknowledged"`
}

// Register mounts acknowledging. Who may call it is internal/authz's
// decision (operator and up, in a session: never a token or an agent);
// session only attaches the identity. {id} is the inbox item's ID,
// path-escaped.
//
//	POST   /api/inbox/{id}/acknowledge  {"note"?} → AckResponse
//	DELETE /api/inbox/{id}/acknowledge  → AckResponse, acknowledged null
//
// Both are audited (incident.acknowledge, incident.unacknowledge), with
// the incident's target as the record's target.
func (a *Acks) Register(mux *http.ServeMux, session func(http.Handler) http.Handler) {
	mux.Handle("POST /api/inbox/{id}/acknowledge", session(http.HandlerFunc(a.handleAck)))
	mux.Handle("DELETE /api/inbox/{id}/acknowledge", session(http.HandlerFunc(a.handleUnack)))
}

func (a *Acks) handleAck(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body AckRequest
	if !httpx.DecodeJSONLimit(w, r, &body, 4<<10) {
		return
	}
	if len([]rune(body.Note)) > MaxNote {
		httpx.Say(w, http.StatusUnprocessableEntity, "invalid", msg.New("incidents.note_long", "max", MaxNote))
		return
	}
	who, ok := a.who(r)
	if !ok {
		httpx.Say(w, http.StatusUnauthorized, "unauthenticated", msg.New("auth.signin_required"))
		return
	}
	ack, err := a.Acknowledge(r.Context(), id, who, body.Note)
	if a.refused(w, err) {
		return
	}
	it, _ := decodeItem(a.inbox.Item(inboxColl, id))
	a.Audit.Change(r, "incident.acknowledge", it.in.Target, map[string]any{"incident": id, "title": clip(it.in.Title), "note": ack.Note}, authz.T0, nil)
	httpx.WriteJSON(w, http.StatusOK, AckResponse{Incident: id, Acknowledged: &ack})
}

func (a *Acks) handleUnack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	who, ok := a.who(r)
	if !ok {
		httpx.Say(w, http.StatusUnauthorized, "unauthenticated", msg.New("auth.signin_required"))
		return
	}
	was, err := a.Unacknowledge(r.Context(), id, who)
	if a.refused(w, err) {
		return
	}
	it, _ := decodeItem(a.inbox.Item(inboxColl, id))
	a.Audit.Change(r, "incident.unacknowledge", it.in.Target, map[string]any{"incident": id, "title": clip(it.in.Title), "was_by": was.By}, authz.T0, nil)
	httpx.WriteJSON(w, http.StatusOK, AckResponse{Incident: id})
}

// who is the signed-in person's username.
func (a *Acks) who(r *http.Request) (string, bool) {
	if a.whoFor != nil {
		return a.whoFor(r)
	}
	actor, ok := authz.ActorFrom(r)
	return actor.Name, ok && actor.Name != ""
}

// refused writes err's answer and reports whether there was one.
func (a *Acks) refused(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNoIncident):
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("incidents.no_incident"))
	case errors.Is(err, ErrNotProblem):
		httpx.Say(w, http.StatusConflict, "not_acknowledgeable", msg.New("incidents.not_problem"))
	case errors.Is(err, ErrResolved):
		httpx.Say(w, http.StatusConflict, "resolved", msg.New("incidents.ack_resolved"))
	case errors.Is(err, ErrAcknowledged):
		httpx.Say(w, http.StatusConflict, "already_acknowledged", msg.New("incidents.already_acknowledged"))
	case errors.Is(err, ErrNotAcknowledged):
		httpx.Say(w, http.StatusConflict, "not_acknowledged", msg.New("incidents.not_acknowledged"))
	default:
		a.log.Error("incidents: acknowledging failed", "err", err)
		httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
	}
	return true
}
