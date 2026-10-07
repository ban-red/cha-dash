package stacks

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/stackproto"
)

const otherValue = "another-stacks-SECRET-5678"

// addForeignSecret makes a node's replies to verb name a secret that isn't
// its stack's (review finding 4).
func (h *hubEnv) addForeignSecret(verb string) {
	h.caller.mu.Lock()
	defer h.caller.mu.Unlock()
	h.caller.tamper = func(v string, _, reply json.RawMessage) json.RawMessage {
		if v != verb {
			return reply
		}
		var m map[string]any
		if json.Unmarshal(reply, &m) != nil {
			return reply
		}
		secrets, _ := m["secrets"].([]any)
		m["secrets"] = append(secrets, "vpn_password")
		if c, ok := m["content"].(string); ok {
			m["content"] = c + "# ${secret:vpn_password}\n"
		}
		b, _ := json.Marshal(m)
		return b
	}
}

func (h *hubEnv) sentWith(value string) []string {
	h.caller.mu.Lock()
	defer h.caller.mu.Unlock()
	var out []string
	for _, s := range h.caller.sent {
		if strings.Contains(s, value) {
			out = append(out, strings.SplitN(s, " ", 2)[0])
		}
	}
	return out
}

// TestRollbackSendsOnlyGrantedSecrets: a rollback sends only secrets an
// earlier deploy sent to that stack; a node that names another stack's is
// refused, and the value never leaves the vault (review finding 4).
func TestRollbackSendsOnlyGrantedSecrets(t *testing.T) {
	h := newHub(t)
	target := Target("local", "media")
	for name, value := range map[string]string{"db_password": secretValue, "vpn_password": otherValue} {
		if w := h.do("PUT", "/api/stacks/secrets/"+name, map[string]string{"value": value}); w.Code != 204 {
			t.Fatalf("set %s: %d", name, w.Code)
		}
	}
	h.saveDraft("media", good)
	if _, task := h.run(ActionDeploy, target, ""); task.State != action.StateSucceeded {
		t.Fatalf("first deploy: %+v", task.Error)
	}
	h.saveDraft("media", strings.Replace(good, "nginx:1.27", "nginx:1.28", 1))
	if _, task := h.run(ActionDeploy, target, ""); task.State != action.StateSucceeded {
		t.Fatalf("second deploy: %+v", task.Error)
	}
	d := decode[StackDetail](t, h.do("GET", "/api/stacks/local/media", nil))
	first := d.History[len(d.History)-1].Commit

	// The node claims the old version also needs vpn_password.
	h.addForeignSecret(stackproto.VerbGet)
	_, task := h.run(ActionRollback, target, `{"commit":"`+first+`"}`)
	if task.State != action.StateFailed || task.Error == nil || task.Error.Code != "secret_not_granted" || !strings.Contains(task.Error.Message, "vpn_password") {
		t.Fatalf("rollback with a foreign secret: %v %+v", task.State, task.Error)
	}
	if got := h.sentWith(otherValue); len(got) != 0 {
		t.Fatalf("another stack's secret went to the node with %v", got)
	}

	// An honest node rolls back, with the stack's own secret.
	h.caller.mu.Lock()
	h.caller.tamper = nil
	h.caller.mu.Unlock()
	if _, task := h.run(ActionRollback, target, `{"commit":"`+first+`"}`); task.State != action.StateSucceeded {
		t.Fatalf("honest rollback: %+v", task.Error)
	}
	if got := h.docker.LastEnv()["web"]["DB_PASSWORD"]; got != secretValue {
		t.Fatalf("the rollback ran with %q", got)
	}
	// Grants are per stack and node.
	if err := h.svc.grantedOnly(t.Context(), "local", "media", []string{"db_password"}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.grantedOnly(t.Context(), "local", "other", []string{"db_password"}); err == nil {
		t.Fatal("a grant leaked to another stack")
	}
	if err := h.svc.grantedOnly(t.Context(), "elsewhere", "media", []string{"db_password"}); err == nil {
		t.Fatal("a grant leaked to another node")
	}
}

// TestPlanSendsSecretsFromTheHubsReading: validating a draft sends the
// node only the secrets the hub finds in the draft itself, whatever the
// node's validate reply lists (review finding 4).
func TestPlanSendsSecretsFromTheHubsReading(t *testing.T) {
	h := newHub(t)
	for name, value := range map[string]string{"db_password": secretValue, "vpn_password": otherValue} {
		if w := h.do("PUT", "/api/stacks/secrets/"+name, map[string]string{"value": value}); w.Code != 204 {
			t.Fatalf("set %s: %d", name, w.Code)
		}
	}
	h.addForeignSecret(stackproto.VerbValidate)
	v := decode[Validation](t, h.do("POST", "/api/stacks/local/media/validate", map[string]string{"content": good}))
	if !v.OK || v.Plan == nil {
		t.Fatalf("validate: %+v", v)
	}
	if slices.ContainsFunc(v.Secrets, func(s SecretState) bool { return s.Name == "vpn_password" }) {
		t.Fatalf("the node's list leaked into the secrets shown: %+v", v.Secrets)
	}
	if got := h.sentWith(otherValue); len(got) != 0 {
		t.Fatalf("another stack's secret went to the node with %v", got)
	}
	if got := h.sentWith(secretValue); !slices.Equal(got, []string{"stack.plan"}) {
		t.Fatalf("the draft's own secret went with %v", got)
	}
}

// TestRootEquivalentInTheConfirmation: what the node flags as root on its
// host leads the deploy's steps (review finding 2).
func TestRootEquivalentInTheConfirmation(t *testing.T) {
	h := newHub(t)
	risky := "services:\n  vpn:\n    image: wireguard:1.0\n    cap_add: [NET_ADMIN]\n    healthcheck:\n      test: [\"CMD\", \"true\"]\n"
	h.saveDraft("vpn", risky)
	p, err := h.eng.Plan(t.Context(), sam, action.PlanRequest{Action: ActionDeploy, Target: Target("local", "vpn")})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) == 0 || !strings.HasPrefix(p.Steps[0].Label, "Root-equivalent on the node's host") || !strings.Contains(p.Steps[0].Label, "vpn adds capabilities (NET_ADMIN)") {
		t.Fatalf("steps %+v", p.Steps)
	}
	v := decode[Validation](t, h.do("POST", "/api/stacks/local/vpn/validate", map[string]string{"content": risky}))
	if !v.OK || !slices.ContainsFunc(v.Messages, func(m stackproto.Message) bool {
		return m.Severity == stackproto.SeverityWarning && strings.Contains(m.Message, "root-equivalent")
	}) {
		t.Fatalf("the editor doesn't warn: %+v", v.Messages)
	}
}
