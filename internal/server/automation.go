package server

import (
	"time"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/msg"
)

// AutomationFindingID is the Inbox item test automation keeps open.
const AutomationFindingID = "hub.test-automation"

// AutomationFinding opens a security finding on the hub for as long as
// test automation is on (CHADASH_TEST_AUTOMATION; internal/auth/automation.go).
// It's at attention whatever the posture: a key on disk signs in as the owner
// without a passkey, so nobody should miss it. The inbox lives in memory, so
// a start without the flag has no finding to resolve.
func AutomationFinding(inbox *livedoc.Doc, now time.Time) {
	if inbox == nil {
		return
	}
	now = now.UTC()
	item := integration.Incident{
		ID: AutomationFindingID, Scenario: "test-automation", Class: integration.ClassProblem,
		Category: integration.CategorySecurity, Severity: integration.SeverityAttention, Status: integration.StatusOpen,
		Title:        msg.New("hub.automation_title").Text(),
		Summary:      msg.New("hub.automation_summary").Text(),
		Target:       integration.HubResource,
		ResolvesWhen: msg.New("hub.automation_resolves").Text(),
		Phase:        "open",
		OpenedAt:     now, UpdatedAt: now,
		Causes: []integration.Cause{}, BlastRadius: []integration.Impact{},
		Suggested: integration.Action{ID: "link.open", Label: msg.New("hub.automation_action").Text(), Tier: "T0", Target: integration.HubResource},
	}
	_, _ = inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put("incidents", item.ID, item)
		return nil
	})
}
