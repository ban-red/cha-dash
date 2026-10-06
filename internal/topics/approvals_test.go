package topics

import (
	"encoding/json"
	"testing"
)

// Plans waiting for a person reach approvers only: viewers and operators see
// the rest of the Inbox in full, the household never sees them.
func TestApprovalsOnlyForApprovers(t *testing.T) {
	doc := map[string]any{Incidents: map[string]json.RawMessage{
		"plex-down":    json.RawMessage(`{"id":"plex-down","class":"problem","severity":"down","status":"open","target":"app:plex","blast_radius":[]}`),
		"approval.a_1": json.RawMessage(`{"id":"approval.a_1","class":"approval","severity":"attention","status":"open","target":"app:plex","blast_radius":[]}`),
	}}
	count := func(v InboxView) int {
		cp := map[string]any{Incidents: map[string]json.RawMessage{}}
		for k, raw := range doc[Incidents].(map[string]json.RawMessage) {
			cp[Incidents].(map[string]json.RawMessage)[k] = raw
		}
		return len(ProjectInbox(cp, v)[Incidents].(map[string]json.RawMessage))
	}
	if n := count(InboxView{Detail: true, Approvals: true}); n != 2 {
		t.Errorf("approver sees %d", n)
	}
	if n := count(InboxView{Detail: true}); n != 1 {
		t.Errorf("viewer sees %d", n)
	}
	if n := count(InboxView{Relevant: func(string) bool { return true }, Approvals: true}); n != 1 {
		t.Errorf("household sees %d", n)
	}
}
