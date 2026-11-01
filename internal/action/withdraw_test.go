package action

import (
	"encoding/json"
	"errors"
	"testing"

	"dash.cha.sh/internal/audit"
)

// TestAgentWithdrawsItsOwnProposal: an agent takes back one of its own
// waiting proposals. It ends as expired with the hub's sentence, the Inbox
// item resolves, the audit has approval.withdraw by the agent with its
// reason in the params, and its waiting slot frees up. Anyone else's, or
// one already decided, is refused.
func TestAgentWithdrawsItsOwnProposal(t *testing.T) {
	var ran []string
	limits := AgentLimits{Pending: 1, PerHour: 20}
	e, _ := newAgentEnv(t, &limits, restartDef(&ran))
	_, apv, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Health check failing for 12 min.")
	if err != nil || apv == nil {
		t.Fatalf("propose: %v %v", apv, err)
	}
	// Its one waiting slot is taken.
	if _, _, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Again."); refusal(t, err, "rate_limited") == nil {
		t.Fatal("a second proposal went past the limit")
	}

	// Another agent, a token and a person can't withdraw it; to them it doesn't exist.
	other := agent
	other.ID = "tok_other"
	for _, a := range []Actor{other, token, owner} {
		if _, err := e.e.WithdrawOwn(t.Context(), a, apv.ID, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s withdrew it: %v", a.Kind, err)
		}
	}
	if _, err := e.e.WithdrawOwn(t.Context(), agent, "apv_nope", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	got, err := e.e.WithdrawOwn(t.Context(), agent, apv.ID, "Fixed itself.\nNo longer needed.")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != ApprovalExpired || got.Reason != "The agent that proposed it withdrew it." || got.DecidedAt == nil {
		t.Fatalf("withdrawn %+v", got)
	}
	if it := e.item(apv.ID); it["status"] != "resolved" || it["resolved_by"] != ApprovalExpired {
		t.Fatalf("inbox item %v", it)
	}
	if len(ran) != 0 {
		t.Fatal("a withdrawn proposal ran")
	}
	// Once only: it's no longer waiting.
	if _, err := e.e.WithdrawOwn(t.Context(), agent, apv.ID, ""); refusal(t, err, "approval_expired") == nil {
		t.Fatal("withdrew twice")
	}
	// Its slot is free again.
	if _, apv2, err := e.propose(agent, "proxmox.guest.restart", "proxmox:i_x/qemu/201", "Failing again."); err != nil || apv2 == nil {
		t.Fatalf("after withdrawing: %v", err)
	}

	var ok, refused int
	for _, ev := range e.events() {
		if ev.Action != ActionApprovalWithdraw {
			continue
		}
		if ev.Actor.Kind != KindAgent || ev.Actor.ID != agent.ID {
			t.Fatalf("withdraw by %+v", ev.Actor)
		}
		switch ev.Result {
		case audit.OK:
			ok++
			var params map[string]string
			_ = json.Unmarshal(ev.Params, &params)
			if params["approval"] != apv.ID || params["agent_reason"] != "Fixed itself. No longer needed." || params["reason"] != got.Reason {
				t.Fatalf("params %v", params)
			}
		case audit.Refused:
			refused++
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("withdraw audited %d ok, %d refused", ok, refused)
	}
}
