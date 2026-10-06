package notify

import (
	"encoding/json"
	"time"

	"dash.cha.sh/internal/topics"
)

// What notifies: symptoms, not causes (02 · Experience → Voice and copy;
// 04 · Roadmap → symptom-first defaults). The detector reads the inbox
// topic as each viewer would and turns incident transitions into
// candidates. It never notifies on:
//
//   - hints: an open info problem nothing is working on, standing advice
//     (web/src/model/inbox.ts isHint). The security posture already decides
//     what reaches attention, so under Relaxed every security finding is a
//     hint and never notifies;
//   - anything at info, settling ones included, or the correlator's
//     suggestions;
//   - an attention problem that hasn't stayed open past the grace period
//     (default 5 minutes), or that something is already fixing, so flaps
//     don't page. Dropping back to info restarts the grace period.
//
// A down problem notifies at once (also when an attention one escalates);
// a repair (an integration to reconnect) once per opening; a stuck stack
// (internal/stacks' "stack-stuck") under its own rule; a plan an API token
// left waiting for a person (class "approval") once, to the approvers. A resolution is a
// candidate whenever something that was open resolves; the service only
// sends it when the opening was notified and the rule wants resolutions.

// incident is the part of an inbox incident the detector reads (the
// simulator's shape, sim/README.md).
type incident struct {
	ID       string `json:"id"`
	Scenario string `json:"scenario"`
	Class    string `json:"class"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	Phase    string `json:"phase"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Target   string `json:"target"`
	OpenedAt string `json:"opened_at"`

	raw json.RawMessage
}

// fixingPhases are the phases in which something is already working on an
// incident (web/src/model/inbox.ts FIXING_PHASES).
var fixingPhases = map[string]bool{
	"retrying": true, "pruning": true, "collecting": true, "restarting": true, "recovering": true, "recovered": true,
	"applying": true, "updating": true, "reconnecting": true, "fixing": true, "in_progress": true,
}

// Transitions.
const (
	trDown      = "down"
	trAttention = "attention"
	trRepair    = "repair"
	trStuck     = "stuck"
	trResolved  = "resolved"
	trAction    = "action"
	trApproval  = "approval"
)

// stuckScenario is internal/stacks' stuck-stack incident.
const stuckScenario = "stack-stuck"

// candidate is a transition the service may notify.
type candidate struct {
	rule       string
	transition string
	inc        incident
}

// tracked is what the detector remembers about one incident.
type tracked struct {
	open      bool
	attnSince time.Time       // when it was last seen reach attention; zero below it
	done      map[string]bool // transitions already proposed since it opened
}

// detector turns successive inbox snapshots into candidates. It isn't
// safe for concurrent use; the service's loop owns it.
type detector struct {
	seen map[string]*tracked
}

func newDetector() *detector { return &detector{seen: map[string]*tracked{}} }

// observe compares a snapshot with what it saw before. grace is the
// attention rule's grace period.
func (d *detector) observe(now time.Time, incs map[string]incident, grace time.Duration) []candidate {
	var out []candidate
	for id := range d.seen {
		if _, ok := incs[id]; !ok {
			delete(d.seen, id) // gone (pruned, or its instance deleted): nothing to say
		}
	}
	for _, id := range sortedIDs(incs) {
		in := incs[id]
		if in.Class == "suggestion" {
			continue
		}
		tr := d.seen[id]
		if tr == nil {
			tr = &tracked{done: map[string]bool{}}
			d.seen[id] = tr
		}
		propose := func(rule, transition string) {
			if !tr.done[transition] {
				tr.done[transition] = true
				out = append(out, candidate{rule: rule, transition: transition, inc: in})
			}
		}
		switch in.Status {
		case "open":
			tr.open = true
			fixing := fixingPhases[in.Phase]
			switch {
			case in.Class == "repair":
				if !fixing {
					propose(RuleRepair, trRepair)
				}
			case in.Class == topics.ApprovalClass:
				// A token's plan waiting for a person. Only approvers' views
				// include it, so Web Push reaches only them.
				propose(RuleApproval, trApproval)
			case in.Class != "problem":
			case in.Scenario == stuckScenario:
				propose(RuleStuck, trStuck)
			case in.Severity == "down":
				propose(RuleDown, trDown)
			case in.Severity == "attention":
				if tr.attnSince.IsZero() {
					tr.attnSince = now
				}
				if !tr.done[trDown] && !fixing && now.Sub(tr.attnSince) >= grace {
					propose(RuleAttention, trAttention)
				}
			default:
				// info: a hint, or settling. Never a notification, and the
				// attention grace starts again if it comes back.
				tr.attnSince = time.Time{}
			}
			if in.Severity != "attention" {
				tr.attnSince = time.Time{}
			}
		case "resolved":
			if tr.open {
				out = append(out, candidate{transition: trResolved, inc: in})
			}
			tr.open, tr.attnSince, tr.done = false, time.Time{}, map[string]bool{}
		}
	}
	return out
}

// pendingGrace reports whether an attention problem is waiting out its
// grace period, so the loop knows to look again even when the inbox is
// quiet.
func (d *detector) pendingGrace() bool {
	for _, tr := range d.seen {
		if tr.open && !tr.attnSince.IsZero() && !tr.done[trAttention] && !tr.done[trDown] {
			return true
		}
	}
	return false
}

// parseIncidents reads the inbox collection from a livedoc snapshot.
func parseIncidents(coll any) map[string]incident {
	out := map[string]incident{}
	m, ok := coll.(map[string]json.RawMessage)
	if !ok {
		return out
	}
	for id, raw := range m {
		var in incident
		if json.Unmarshal(raw, &in) != nil {
			continue
		}
		if in.ID == "" {
			in.ID = id
		}
		in.raw = raw
		out[id] = in
	}
	return out
}
