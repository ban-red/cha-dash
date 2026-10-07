package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/integrations/checks"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/notify"
	"dash.cha.sh/internal/onboarding"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/stackproto"
	"dash.cha.sh/internal/stacks"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

// oneNode is a stacks node caller with one node whose stacks don't exist
// yet: enough to save and discard drafts.
type oneNode struct{}

func (oneNode) Call(context.Context, string, string, any, func(json.RawMessage)) (json.RawMessage, error) {
	return nil, &stackproto.Error{Code: stackproto.CodeNotFound, Message: "no such stack"}
}

// auditEnv mounts the subsystems whose changes the audit routes test
// makes, with a real integration (checks) and one stacks node.
func auditEnv(t *testing.T) (*env, *store.Store) {
	t.Helper()
	var st *store.Store
	e := newEnvWith(t, func(d *Deps) {
		st = d.Store
		v, err := vault.New(d.Store, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		reg := integration.NewRegistry()
		if err := reg.Register(checks.Integration{}); err != nil {
			t.Fatal(err)
		}
		m, err := runtime.New(runtime.Options{Store: d.Store, Vault: v, Registry: reg, Topics: topics.New(graph.New(), time.Now()), Log: d.Log})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		ob, err := onboarding.New(onboarding.Options{Store: d.Store, Integrations: m, Registry: reg, Log: d.Log})
		if err != nil {
			t.Fatal(err)
		}
		stk, err := stacks.New(stacks.Options{Store: d.Store, Vault: v, Caller: oneNode{}, Log: d.Log,
			Nodes: func(context.Context) ([]stacks.Node, error) {
				return []stacks.Node{{ID: "n1", Name: "local", Online: true}}, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		nt, err := notify.New(t.Context(), notify.Options{Store: d.Store, Vault: v, Log: d.Log})
		if err != nil {
			t.Fatal(err)
		}
		d.Vault, d.Integrations, d.Onboarding, d.Stacks, d.Notify = v, m, ob, stk, nt
		d.Audit = audit.New(d.Store)
	})
	return e, st
}

// auditEvents returns every event, oldest first.
func auditEvents(t *testing.T, st *store.Store) []audit.Event {
	t.Helper()
	page, err := audit.New(st).Query(t.Context(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(page.Events)
	return page.Events
}

// lastAudit is the newest event recording action.
func lastAudit(t *testing.T, st *store.Store, action string) (audit.Event, bool) {
	t.Helper()
	evs := auditEvents(t, st)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Action == action {
			return evs[i], true
		}
	}
	return audit.Event{}, false
}

// provenElsewhere names, for audited routes this file doesn't call, the
// test that shows them recording their action.
var provenElsewhere = map[string]string{
	"POST /api/auth/passkeys/register/finish":      "auth.TestAuthEvents (the event) and TestAuthEventsAudited (its record)",
	"PATCH /api/auth/passkeys/{id}":                "auth.TestAuthEvents and TestAuthEventsAudited",
	"DELETE /api/auth/passkeys/{id}":               "auth.TestAuthEvents and TestAuthEventsAudited",
	"POST /api/actions/plan":                       "action.TestPlanRefusals, action.TestPlanApproveRun",
	"POST /api/plans/{id}/approve":                 "action.TestPlanApproveRun, action.TestApproveRefusals",
	"POST /api/tasks/{id}/cancel":                  "action.TestFailures (task.cancel, refusals included)",
	"POST /api/correlate/suggestions/{id}/confirm": "correlate.TestAPI",
	"POST /api/correlate/suggestions/{id}/reject":  "correlate.TestAPI (the confirm path; reject is the same handler with the other verdict)",
	"POST /api/correlate/decisions":                "correlate.TestAPI",
	"DELETE /api/correlate/decisions/{id}":         "correlate.TestAPI",
	"PUT /api/settings/security":                   "TestChangesAreAudited",
	"POST /api/nodes/{id}/revoke":                  "nodes.TestRevoke",
	"POST /api/approvals/{id}/approve":             "action.TestTokenT2WaitsForAPerson",
	"POST /api/approvals/{id}/deny":                "action.TestApprovalDenyExpireWithdraw",
}

// Every route authz.Audited lists records its action: here, by calling it
// through the real router, or in the test provenElsewhere names. A new
// audited route without either fails this test.
func TestAuditedRoutesRecord(t *testing.T) {
	e, st := auditEnv(t)
	owner := signIn(t, e)
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		status, _, b := e.call(t, owner, method, path, body)
		if status != want {
			t.Fatalf("%s %s: %d %s", method, path, status, b)
		}
		return b
	}
	covered := map[string]bool{}
	expect := func(pattern string) audit.Event {
		t.Helper()
		action := authz.Audited[pattern]
		ev, ok := lastAudit(t, st, action)
		if !ok || ev.Result != audit.OK {
			t.Fatalf("%s: no %s event (%+v)", pattern, action, ev)
		}
		covered[pattern] = true
		return ev
	}

	// Boards.
	var boards struct{ Boards []struct{ ID string } }
	json.Unmarshal(call("GET", "/api/boards", nil, 200), &boards)
	board := boards.Boards[0].ID
	call("PUT", "/api/boards/"+board+"/layout", map[string]any{"revision": 0, "format_version": 1, "layout": map[string]any{}}, 200)
	if ev := expect("PUT /api/boards/{id}/layout"); ev.Target != "board:"+board || !strings.Contains(string(ev.Params), `"revision":1`) {
		t.Fatalf("layout save: %+v", ev)
	}
	call("POST", "/api/boards/"+board+"/discovery/dismiss", map[string]any{"resources": []string{"docker:i_x/container/a"}}, 200)
	expect("POST /api/boards/{id}/discovery/dismiss")
	call("POST", "/api/boards/"+board+"/discovery/restore", map[string]any{"resources": []string{"docker:i_x/container/a"}}, 200)
	expect("POST /api/boards/{id}/discovery/restore")

	// Integrations: names, field names and the URL's host, never a value.
	const target = "http://lab-check.example:8080/health?key=FAKE-query-value"
	b := call("POST", "/api/integrations/instances", map[string]any{"integration": "checks", "name": "Lab",
		"config": map[string]any{"targets": []any{map[string]any{"name": "web", "kind": "http", "url": target}}}}, 201)
	var created struct{ ID string }
	json.Unmarshal(b, &created)
	ev := expect("POST /api/integrations/instances")
	if p := string(ev.Params); ev.Target != "integration:"+created.ID || !strings.Contains(p, `"config_fields":["targets"]`) || !strings.Contains(p, `"name":"Lab"`) || strings.Contains(p, "FAKE-query-value") {
		t.Fatalf("instance create: %+v %s", ev, ev.Params)
	}
	call("PUT", "/api/integrations/instances/"+created.ID, map[string]any{"name": "Lab 2", "enabled": false}, 200)
	if ev := expect("PUT /api/integrations/instances/{id}"); !strings.Contains(string(ev.Params), `"renamed":true`) || !strings.Contains(string(ev.Params), `"enabled":false`) {
		t.Fatalf("instance update: %s", ev.Params)
	}
	call("DELETE", "/api/integrations/instances/"+created.ID, nil, 204)
	if ev := expect("DELETE /api/integrations/instances/{id}"); !strings.Contains(string(ev.Params), `"name":"Lab 2"`) {
		t.Fatalf("instance delete: %s", ev.Params)
	}

	// Import and export: counts and IDs.
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	call("POST", "/api/import/homepage?dry_run=1", map[string]string{"services_yaml": "- Apps:\n  - Sonarr:\n      href: " + up.URL + "\n"}, 200)
	if _, ok := lastAudit(t, st, "import.homepage"); ok {
		t.Fatal("a dry run was audited")
	}
	call("POST", "/api/import/homepage", map[string]string{"services_yaml": "- Apps:\n  - Sonarr:\n      href: " + up.URL + "\n      siteMonitor: " + up.URL + "\n"}, 200)
	if ev := expect("POST /api/import/homepage"); !strings.Contains(string(ev.Params), `"instance_ids":["i_`) {
		t.Fatalf("homepage import: %s", ev.Params)
	}
	home := call("POST", "/api/export", map[string]string{"passphrase": "a long enough passphrase"}, 200)
	if ev := expect("POST /api/export"); strings.Contains(string(ev.Params), "passphrase") || !strings.Contains(string(ev.Params), `"bundled"`) {
		t.Fatalf("export: %s", ev.Params)
	}
	call("POST", "/api/import/chadash", map[string]string{"yaml": string(home), "passphrase": "a long enough passphrase"}, 200)
	if ev := expect("POST /api/import/chadash"); strings.Contains(string(ev.Params), "passphrase") || !strings.Contains(string(ev.Params), `"unchanged"`) {
		t.Fatalf("home import: %s", ev.Params)
	}

	// Onboarding.
	call("PUT", "/api/onboarding", map[string]string{"status": "skipped"}, 200)
	expect("PUT /api/onboarding")
	call("POST", "/api/onboarding/discover", map[string]any{}, 200)
	expect("POST /api/onboarding/discover")

	// Stacks: drafts (never their content) and secrets (never their value).
	call("PUT", "/api/stacks/n1/media", map[string]any{"content": "services:\n  web:\n    image: nginx\n    environment:\n      TOKEN: FAKE-compose-secret\n", "revision": 0}, 200)
	if ev := expect("PUT /api/stacks/{node}/{stack}"); ev.Target != "stack:n1/media" || strings.Contains(string(ev.Params), "FAKE-compose-secret") {
		t.Fatalf("draft save: %+v %s", ev, ev.Params)
	}
	call("DELETE", "/api/stacks/n1/media/draft", nil, 204)
	expect("DELETE /api/stacks/{node}/{stack}/draft")
	call("PUT", "/api/stacks/secrets/db_password", map[string]string{"value": "FAKE-stack-secret-value"}, 204)
	if ev := expect("PUT /api/stacks/secrets/{name}"); strings.Contains(string(ev.Params), "FAKE-stack-secret") {
		t.Fatalf("stack secret: %s", ev.Params)
	}
	call("DELETE", "/api/stacks/secrets/db_password", nil, 204)
	expect("DELETE /api/stacks/secrets/{name}")

	// Notifications: a webhook at a closed port, so its test fails at once.
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	b = call("POST", "/api/notify/channels", map[string]any{"kind": "webhook", "name": "Hook", "config": map[string]string{"url": closedURL + "/hook"},
		"secrets": map[string]string{"secret": "whsec_fakesigningsecret0123456789ABCDEF"}}, 201)
	var ch struct{ ID string }
	json.Unmarshal(b, &ch)
	expect("POST /api/notify/channels")
	call("PUT", "/api/notify/channels/"+ch.ID, map[string]any{"name": "Hook 2"}, 200)
	expect("PUT /api/notify/channels/{id}")
	call("POST", "/api/notify/channels/"+ch.ID+"/test", nil, 200)
	expect("POST /api/notify/channels/{id}/test")
	call("DELETE", "/api/notify/channels/"+ch.ID, nil, 204)
	expect("DELETE /api/notify/channels/{id}")
	call("PUT", "/api/notify/rules/incident.attention", map[string]int{"grace_minutes": 15}, 200)
	expect("PUT /api/notify/rules/{id}")

	// Users and tokens.
	b = call("POST", "/api/users", map[string]string{"username": "kim", "password": testPassword, "role": "viewer"}, 201)
	var kim auth.UserInfo
	json.Unmarshal(b, &kim)
	if ev := expect("POST /api/users"); strings.Contains(string(ev.Params), testPassword) {
		t.Fatalf("a password reached the audit log: %s", ev.Params)
	}
	call("PUT", "/api/users/"+kim.ID, map[string]string{"role": "operator"}, 200)
	expect("PUT /api/users/{id}")
	b = call("POST", "/api/tokens", map[string]any{"name": "script", "scopes": []string{"graph.read"}}, 201)
	var tok struct {
		Token  struct{ ID string }
		Secret string
	}
	json.Unmarshal(b, &tok)
	expect("POST /api/tokens")
	call("DELETE", "/api/tokens/"+tok.Token.ID, nil, 200)
	expect("DELETE /api/tokens/{id}")
	stepUp(t, e, owner)
	if ev := expect("POST /api/auth/step-up"); !strings.Contains(string(ev.Params), `"method":"password"`) {
		t.Fatalf("step-up: %s", ev.Params)
	}
	call("POST", "/api/users/"+kim.ID+"/transfer-ownership", nil, 200)
	expect("POST /api/users/{id}/transfer-ownership")

	// No secret anywhere in the chain, and it holds.
	for _, ev := range auditEvents(t, st) {
		raw, _ := json.Marshal(ev)
		for _, secret := range []string{tok.Secret, "FAKE-", "whsec_", testPassword, "a long enough passphrase"} {
			if secret != "" && strings.Contains(string(raw), secret) {
				t.Fatalf("%s carries a secret: %s", ev.Action, raw)
			}
		}
	}
	if rep, err := audit.New(st).Verify(t.Context()); err != nil || !rep.OK {
		t.Fatalf("verify: %+v %v", rep, err)
	}

	for pattern := range authz.Audited {
		if !covered[pattern] && provenElsewhere[pattern] == "" {
			t.Errorf("%s is audited (%s) but no test shows it: call it here or name the test in provenElsewhere", pattern, authz.Audited[pattern])
		}
		if covered[pattern] && provenElsewhere[pattern] != "" {
			t.Errorf("%s is called here and also listed in provenElsewhere", pattern)
		}
	}
	for pattern := range provenElsewhere {
		if _, ok := authz.Audited[pattern]; !ok {
			t.Errorf("provenElsewhere names %s, which isn't in authz.Audited", pattern)
		}
	}
}

// The passkey and step-up events become audit records: names and IDs,
// never key material.
func TestAuthEventsAudited(t *testing.T) {
	var st *store.Store
	e := newEnvWith(t, func(d *Deps) { st = d.Store; d.Audit = audit.New(d.Store) })
	_ = e
	rec := &authz.Recorder{Log: audit.New(st)}
	r := httptest.NewRequest("POST", "/api/auth/passkeys/register/finish", nil)
	for _, ev := range []auth.Event{
		{Kind: auth.EventPasskeyRegistered, Passkey: "pk_1", Name: "iCloud Keychain", Request: r},
		{Kind: auth.EventPasskeyRenamed, Passkey: "pk_1", Name: "Laptop", Request: r},
		{Kind: auth.EventPasskeyDeleted, Passkey: "pk_1", Request: r},
		{Kind: auth.EventStepUpFailed, SessionID: "s_1", Reason: "wrong password", Request: r},
		{Kind: auth.EventStepUp, SessionID: "s_1", Method: auth.MethodPasskey, Request: r},
		{Kind: auth.EventSessionEnded, SessionID: "s_1", Request: r}, // not audited
		{Kind: auth.EventPasskeyDeleted, Passkey: "pk_2"},            // no request: nothing to attribute it to
	} {
		auditAuthEvent(rec, ev)
	}
	var got []string
	for _, ev := range auditEvents(t, st) {
		got = append(got, ev.Action+" "+ev.Target+" "+ev.Result)
	}
	want := []string{"auth.passkey.register passkey:pk_1 ok", "auth.passkey.rename passkey:pk_1 ok", "auth.passkey.delete passkey:pk_1 ok",
		"auth.step-up session:s_1 refused", "auth.step-up session:s_1 ok"}
	if !slices.Equal(got, want) {
		t.Fatalf("audited %v, want %v", got, want)
	}
}

// Refusals outside the engine are audited too: the owner's protected
// account, validation, a scope a role can't hand out, someone else's
// token. An admin can't see or revoke the owner's tokens.
func TestRefusalsAreAudited(t *testing.T) {
	e, st := fullEnv(t)
	owner := signIn(t, e)
	_, ada := addUser(t, e, owner, "ada", auth.RoleAdmin)
	_, vic := addUser(t, e, owner, "vic", auth.RoleViewer)
	users := usersList(t, e, owner)
	ownerID := users["owner"].ID

	refused := func(action, code string) audit.Event {
		t.Helper()
		ev, ok := lastAudit(t, st, action)
		if !ok || ev.Result != audit.Refused || ev.Error == nil || ev.Error.Code != code {
			t.Fatalf("%s: last event %+v, want refused %s", action, ev, code)
		}
		return ev
	}
	// An admin changing the owner's account.
	if status, code, _ := e.call(t, ada, "PUT", "/api/users/"+ownerID, map[string]bool{"disabled": true}); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("admin disabling the owner: %d %s", status, code)
	}
	if ev := refused("users.update", "forbidden"); ev.Actor.Name != "ada" || ev.Target != "user:"+ownerID {
		t.Fatalf("refusal: %+v", ev)
	}
	// The owner demoting themselves, and an invalid role.
	if status, code, _ := e.call(t, owner, "PUT", "/api/users/"+ownerID, map[string]string{"role": "admin"}); status != http.StatusConflict || code != "owner_protected" {
		t.Fatalf("owner demoting themselves: %d %s", status, code)
	}
	refused("users.update", "owner_protected")
	if status, _, _ := e.call(t, owner, "POST", "/api/users", map[string]string{"username": "x", "password": testPassword, "role": "root"}); status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid role: %d", status)
	}
	if ev := refused("users.create", "invalid"); strings.Contains(string(ev.Params), testPassword) {
		t.Fatalf("a password reached the audit log: %s", ev.Params)
	}
	// A viewer asking for audit.read.
	if status, _, _ := e.call(t, vic, "POST", "/api/tokens", map[string]any{"name": "peek", "scopes": []string{"audit.read"}}); status != http.StatusUnprocessableEntity {
		t.Fatalf("viewer audit.read token: %d", status)
	}
	if ev := refused("tokens.create", "invalid"); !strings.Contains(string(ev.Params), "audit.read") || ev.Actor.Name != "vic" {
		t.Fatalf("token refusal: %+v %s", ev, ev.Params)
	}

	// The owner's tokens: hidden from admins, and revoking one is a 404,
	// audited.
	ownerTok := mintToken(t, e, owner, "owner's script", "graph.read", "test.thing.look")
	mintToken(t, e, ada, "ada's script", "graph.read")
	listIDs := func(c *http.Client) []string {
		t.Helper()
		_, _, b := e.call(t, c, "GET", "/api/tokens", nil)
		var out struct{ Tokens []auth.APIToken }
		json.Unmarshal(b, &out)
		var ids []string
		for _, tk := range out.Tokens {
			ids = append(ids, tk.Username+":"+tk.Name)
		}
		return ids
	}
	if ids := listIDs(ada); slices.Contains(ids, "owner:owner's script") || !slices.Contains(ids, "ada:ada's script") {
		t.Fatalf("an admin's token list = %v", ids)
	}
	if ids := listIDs(owner); !slices.Contains(ids, "owner:owner's script") || !slices.Contains(ids, "ada:ada's script") {
		t.Fatalf("the owner's token list = %v", ids)
	}
	var full struct{ Tokens []auth.APIToken }
	_, _, b := e.call(t, owner, "GET", "/api/tokens", nil)
	json.Unmarshal(b, &full)
	ownerTokID := ""
	for _, tk := range full.Tokens {
		if tk.Name == "owner's script" {
			ownerTokID = tk.ID
		}
	}
	if status, code, _ := e.call(t, ada, "DELETE", "/api/tokens/"+ownerTokID, nil); status != http.StatusNotFound || code != "not_found" {
		t.Fatalf("admin revoking the owner's token: %d %s", status, code)
	}
	if ev := refused("tokens.revoke", "not_found"); ev.Actor.Name != "ada" || ev.Target != "token:"+ownerTokID {
		t.Fatalf("revoke refusal: %+v", ev)
	}
	// The owner's token still works.
	if status, _, _ := e.raw(t, withToken(nil, ownerTok), "GET", "/api/graph", nil, nil); status != http.StatusOK {
		t.Fatalf("the owner's token after the refused revoke: %d", status)
	}

	// A token's actions name the user it acts for (on_behalf_of); a
	// session's records don't need to.
	if status, _, b := e.raw(t, withToken(nil, ownerTok), "POST", "/api/actions/plan", []byte(`{"action":"test.thing.look","target":"thing:1"}`), nil); status != http.StatusOK {
		t.Fatalf("token plan: %d %s", status, b)
	}
	// A refused plan is audited at once; plan one the token can't.
	if status, _, _ := e.raw(t, withToken(nil, ownerTok), "POST", "/api/actions/plan", []byte(`{"action":"test.thing.poke","target":"thing:1"}`), nil); status != http.StatusForbidden {
		t.Fatalf("token plan out of scope: %d", status)
	}
	if ev, ok := lastAudit(t, st, "test.thing.poke"); !ok || ev.Actor.Kind != "token" || ev.Actor.OnBehalfOf != ownerID {
		t.Fatalf("token action's actor = %+v", ev.Actor)
	}
	if ev, _ := lastAudit(t, st, "tokens.create"); ev.Actor.OnBehalfOf != "" {
		t.Fatalf("a session's record names on_behalf_of: %+v", ev.Actor)
	}
}
