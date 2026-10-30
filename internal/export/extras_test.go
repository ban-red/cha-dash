package export

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const extrasFile = `apiVersion: chadash/v1alpha1
kind: Home
security:
  posture: standard
  step_up: passkey_only
correlation:
  - {from: 'keyed:i_sonarr/app', rel: same_as, to: 'keyed:i_radarr/app', verdict: confirm, rule: name}
  - {from: 'docker:i_d/container/plex', rel: runs_on, to: 'proxmox:i_p/lxc/101', verdict: confirm}
`

func TestExportCarriesSecurityAndDecisions(t *testing.T) {
	h := newHub(t)
	seed(t, h)
	h.exec(t, `INSERT INTO settings (key, value, updated_at) VALUES ('security.posture', '"strict"', 1)`)
	out, err := h.svc.Export(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out.Data)
	for _, want := range []string{
		"security:\n  posture: strict\n  step_up: passkey_or_password\n",
		// Stored the wrong way round, written in stored order: the smaller ID first.
		"correlation:\n  - {from: 'keyed:i_radarr/app', rel: same_as, to: 'keyed:i_sonarr/app', verdict: reject, rule: FAKE-correlation-rule}\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("export lacks %q:\n%s", want, text)
		}
	}
	d := parse(t, out.Data)
	if d.Security == nil || *d.Security != (Security{Posture: "strict", StepUp: "passkey_or_password"}) || len(d.Correlation) != 1 {
		t.Fatalf("parsed security %+v, correlation %+v", d.Security, d.Correlation)
	}
}

func TestImportSecurityAndDecisions(t *testing.T) {
	ctx := t.Context()
	h := newHub(t)
	seed(t, h) // has keyed:i_sonarr/app same_as keyed:i_radarr/app, rejected
	h.exec(t, `INSERT INTO correlation_decisions (id, from_id, rel, to_id, verdict, rule, created_at) VALUES ('cd_2', 'a:x/1', 'monitors', 'b:y/2', 'confirm', '', 1)`)
	d := parse(t, []byte(extrasFile))

	plan, err := h.svc.Plan(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	if c := change(t, plan, "security", "security"); c.Action != actionUpdate || jsonOf(t, c.Fields) != `[`+
		`{"path":"/posture","op":"replace","from":"relaxed","to":"standard"},`+
		`{"path":"/step_up","op":"replace","from":"passkey_or_password","to":"passkey_only"}]` {
		t.Fatalf("security = %+v", c)
	}
	// The file's pair is written the other way round: it's still the same pair.
	pair := "keyed:i_radarr/app same_as keyed:i_sonarr/app"
	if c := change(t, plan, "correlation", pair); c.Action != actionUpdate || jsonOf(t, c.Fields) != `[`+
		`{"path":"/rule","op":"replace","from":"FAKE-correlation-rule","to":"name"},`+
		`{"path":"/verdict","op":"replace","from":"reject","to":"confirm"}]` {
		t.Fatalf("decision = %+v / %s", c, jsonOf(t, c.Fields))
	}
	if c := change(t, plan, "correlation", "docker:i_d/container/plex runs_on proxmox:i_p/lxc/101"); c.Action != actionCreate || c.Detail != "confirm" ||
		c.Name != "docker:i_d/container/plex runs on proxmox:i_p/lxc/101" {
		t.Fatalf("new decision = %+v", c)
	}
	if c := change(t, plan, "correlation", "a:x/1 monitors b:y/2"); c.Action != actionStale {
		t.Fatalf("hub-only decision = %+v", c)
	}
	// No boards, instances or settings in the file: nothing said about them.
	for _, c := range plan.Changes {
		if c.Kind != "security" && c.Kind != "correlation" && c.Action != actionStale {
			t.Errorf("unexpected change %s %s %s", c.Kind, c.ID, c.Action)
		}
	}

	if _, err := h.svc.Apply(ctx, d, "", "u_owner"); err != nil {
		t.Fatal(err)
	}
	var posture, stepUp string
	_ = h.st.Reader().QueryRow(`SELECT value FROM settings WHERE key = 'security.posture'`).Scan(&posture)
	_ = h.st.Reader().QueryRow(`SELECT value FROM settings WHERE key = 'security.step_up'`).Scan(&stepUp)
	if posture != `"standard"` || stepUp != `"passkey_only"` {
		t.Fatalf("settings = %s, %s", posture, stepUp)
	}
	var verdict, rule string
	var fromInst, toInst *string
	if err := h.st.Reader().QueryRow(`SELECT verdict, rule, from_instance, to_instance FROM correlation_decisions WHERE id = 'cd_1'`).Scan(&verdict, &rule, &fromInst, &toInst); err != nil {
		t.Fatal(err)
	}
	if verdict != "confirm" || rule != "name" || fromInst == nil || *fromInst != "i_sonarr" {
		t.Fatalf("updated in place: %s %s %v", verdict, rule, fromInst)
	}
	if n := rowCount(t, h.st, "correlation_decisions"); n != 3 {
		t.Fatalf("%d decisions, want 3 (one updated, one created, the stale one kept)", n)
	}
	if err := h.st.Reader().QueryRow(`SELECT from_instance FROM correlation_decisions WHERE from_id = 'docker:i_d/container/plex'`).Scan(&fromInst); err != nil || fromInst != nil {
		t.Fatalf("a decision about an instance this hub doesn't have: %v, %v", fromInst, err)
	}

	// Again: nothing to do.
	again, err := h.svc.Apply(ctx, parse(t, []byte(extrasFile)), "", "u_owner")
	if err != nil || again.Changed {
		t.Fatalf("again = %+v, %v", again.Plan.Summary, err)
	}
}

func TestSecurityGate(t *testing.T) {
	ctx := t.Context()
	h := newHub(t)
	refuse := errors.New("Only the owner can change the security settings, so this file's security settings weren't imported.")
	var asked []bool
	gated := WithSecurityGate(ctx, func(stepUp bool) error { asked = append(asked, stepUp); return refuse })
	applied, err := h.svc.Apply(gated, parse(t, []byte(extrasFile)), "", "")
	if err != nil {
		t.Fatal(err)
	}
	c := change(t, applied.Plan, "security", "security")
	if c.Action != actionUnchanged || c.Skipped != refuse.Error() || len(c.Fields) != 2 || len(asked) != 1 || !asked[0] {
		t.Fatalf("security = %+v, asked %v", c, asked)
	}
	if !strings.Contains(jsonOf(t, applied.Plan.Warnings), `"code":"security_skipped"`) {
		t.Fatalf("warnings = %+v", applied.Plan.Warnings)
	}
	if n := rowCount(t, h.st, "settings"); n != 0 {
		t.Fatalf("%d settings written", n)
	}
	// The rest of the file still imported.
	if n := rowCount(t, h.st, "correlation_decisions"); n != 2 {
		t.Fatalf("%d decisions", n)
	}
	// Only the posture: the gate is asked without the step-up.
	asked = nil
	only := strings.Replace(extrasFile, "passkey_only", "passkey_or_password", 1)
	if _, err := h.svc.Plan(gated, parse(t, []byte(only)), ""); err != nil || len(asked) != 1 || asked[0] {
		t.Fatalf("asked %v, %v", asked, err)
	}
}

// TestHTTPImportSecurityNeedsTheOwner: through the API, an importer that
// isn't the owner (here: no session at all) gets the rest of the file.
func TestHTTPImportSecurityNeedsTheOwner(t *testing.T) {
	h := newHub(t)
	var after int
	h.svc.AfterApply = func(context.Context) { after++ }
	srv := h.serve(t)
	status, _, body := call(t, srv, http.MethodPost, "/api/import/chadash", "application/json", map[string]string{"yaml": extrasFile})
	if status != 200 || !strings.Contains(string(body), `"skipped":"Only the owner can change the security settings`) {
		t.Fatalf("import = %d %s", status, body)
	}
	if n := rowCount(t, h.st, "settings"); n != 0 || after != 1 {
		t.Fatalf("%d settings, after ran %d times", n, after)
	}
}

// The sign-in settings travel with the security settings, never the client
// secret; importing them asks the gate for a step-up; a file without them
// leaves the hub's alone; a bad one fails to parse.
func TestSignInSettingsRoundTrip(t *testing.T) {
	ctx := t.Context()
	h := newHub(t)
	h.exec(t, `INSERT INTO settings (key, value, updated_at) VALUES ('security.sign_in', '{"oidc":{"enabled":true,"name":"Pocket ID","issuer":"https://id.lab.example","client_id":"dash"},"mappings":[{"group":"lab-ops","role":"operator"}]}', 1)`)
	out, err := h.svc.Export(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out.Data)
	if !strings.Contains(text, "sign_in:\n") || !strings.Contains(text, "https://id.lab.example") || strings.Contains(text, "client_secret") {
		t.Fatalf("export:\n%s", text)
	}

	other := newHub(t)
	var asked []bool
	gated := WithSecurityGate(ctx, func(stepUp bool) error { asked = append(asked, stepUp); return nil })
	if _, err := other.svc.Apply(gated, parse(t, out.Data), "", "u_owner"); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || !asked[0] {
		t.Fatalf("gate asked %v; changing sign-in needs a step-up", asked)
	}
	var raw string
	if err := other.st.Reader().QueryRow(`SELECT value FROM settings WHERE key = 'security.sign_in'`).Scan(&raw); err != nil || !strings.Contains(raw, `"issuer":"https://id.lab.example"`) {
		t.Fatalf("imported = %s, %v", raw, err)
	}

	// A file without sign_in: no change to them.
	plan, err := other.svc.Plan(ctx, parse(t, []byte(extrasFile)), "")
	if err != nil {
		t.Fatal(err)
	}
	if c := change(t, plan, "security", "security"); strings.Contains(jsonOf(t, c.Fields), "sign_in") {
		t.Fatalf("security = %s", jsonOf(t, c.Fields))
	}
	bad := strings.Replace(extrasFile, "  step_up: passkey_only\n", "  step_up: passkey_only\n  sign_in:\n    mappings: [{group: x, role: owner}]\n", 1)
	if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "owner role never comes from a group") {
		t.Fatalf("parse = %v", err)
	}
}

func TestParseSecurityAndDecisions(t *testing.T) {
	head := "apiVersion: chadash/v1alpha1\nkind: Home\n"
	for _, c := range []struct{ yaml, want string }{
		{"security: {posture: lax, step_up: passkey_only}", "security.posture: must be relaxed, standard or strict"},
		{"security: {posture: strict, step_up: totp}", "security.step_up: must be passkey_or_password or passkey_only"},
		{"security: {posture: strict}", "security: needs posture and step_up"},
		{"security: {posture: strict, step_up: passkey_only, extra: 1}", "security.extra: isn't a field"},
		{"correlation: {from: a}", "correlation: must be a list"},
		{"correlation: [{from: 'a:b/c', rel: owns, to: 'd:e/f', verdict: confirm}]", "correlation[0].rel: must be same_as"},
		{"correlation: [{from: 'a:b/c', rel: same_as, to: 'd:e/f', verdict: maybe}]", "correlation[0].verdict: must be confirm or reject"},
		{"correlation: [{from: 'a:b/c', rel: same_as, verdict: confirm}]", "correlation[0]: needs from, rel, to and verdict"},
		{"correlation: [{from: 'a:b/c', rel: same_as, to: 'a:b/c', verdict: confirm}]", "relates a resource to itself"},
		{"correlation: [{from: 'a:b/c', rel: same_as, to: 'd:e/f', verdict: confirm}, {from: 'd:e/f', rel: same_as, to: 'a:b/c', verdict: reject}]", "correlation[1]: decides a pair another entry"},
	} {
		_, err := Parse([]byte(head + c.yaml + "\n"))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.yaml, err, c.want)
		}
	}
	// Two runs_on decisions in opposite directions are different pairs.
	if _, err := Parse([]byte(head + "correlation: [{from: 'a:b/c', rel: runs_on, to: 'd:e/f', verdict: confirm}, {from: 'd:e/f', rel: runs_on, to: 'a:b/c', verdict: reject}]\n")); err != nil {
		t.Fatalf("opposite runs_on: %v", err)
	}
}
