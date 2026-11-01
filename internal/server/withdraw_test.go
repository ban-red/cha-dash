package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestMCPWithdraw: an agent withdraws its own waiting proposal through the
// withdraw tool. It ends as expired, a person can no longer approve it,
// the audit has approval.withdraw by the agent, and another agent's token
// can't touch it.
func TestMCPWithdraw(t *testing.T) {
	e, st, tp := fullEnvTopics(t)
	labGraph(t, tp)
	owner := signIn(t, e)
	agentsOn(t, e, owner, true)
	_, secret := mintAgent(t, e, owner, "assistant", "graph.read", "propose:test.thing.poke")
	agent := withToken(nil, secret)
	_, otherSecret := mintAgent(t, e, owner, "other", "graph.read", "propose:test.thing.poke")
	other := withToken(nil, otherSecret)

	out, raw, isErr := toolCall(t, e, agent, "propose", map[string]any{"action": "test.thing.poke", "target": "thing:1", "rationale": "It looks stuck."})
	prop, _ := out["proposal"].(map[string]any)
	if isErr || prop["state"] != "awaiting_approval" {
		t.Fatalf("propose: %s", raw)
	}
	id, hash := prop["id"].(string), prop["plan_hash"].(string)

	// Not someone else's, and not with unknown arguments.
	if _, raw, isErr := toolCall(t, e, other, "withdraw", map[string]any{"proposal": id}); !isErr || !strings.Contains(raw, "not_found") {
		t.Fatalf("another agent withdrew it: %s", raw)
	}
	if status, r, raw := mcpCall(t, e, agent, "tools/call", map[string]any{"name": "withdraw", "arguments": map[string]any{"proposal": id, "force": true}}); status != http.StatusOK || r.Error == nil {
		t.Fatalf("unknown argument: %s", raw)
	}

	out, raw, isErr = toolCall(t, e, agent, "withdraw", map[string]any{"proposal": id, "reason": "It recovered on its own."})
	if isErr || out["state"] != "expired" || !strings.Contains(raw, "withdrew it") {
		t.Fatalf("withdraw: %s", raw)
	}
	if status, code, _ := e.call(t, owner, "POST", "/api/approvals/"+id+"/approve", map[string]string{"hash": hash}); status != http.StatusGone || code != "approval_expired" {
		t.Fatalf("approving a withdrawn proposal: %d %s", status, code)
	}
	ev, ok := lastAudit(t, st, "approval.withdraw")
	if !ok || ev.Actor.Kind != "agent" || ev.Result != "ok" || !strings.Contains(string(ev.Params), "It recovered on its own.") {
		t.Fatalf("approval.withdraw: %+v %s", ev, ev.Params)
	}
	if _, raw, isErr := toolCall(t, e, agent, "withdraw", map[string]any{"proposal": id}); !isErr || !strings.Contains(raw, "approval_expired") {
		t.Fatalf("withdrawing twice: %s", raw)
	}
}
