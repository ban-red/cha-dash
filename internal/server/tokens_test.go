package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/auth"
)

// testActions are four actions on one made-up target, "thing:1", one per
// tier from T0 to T3, that do nothing.
func testActions() []action.Def {
	resolve := func(_ context.Context, target string) (action.Target, error) {
		if target != "thing:1" {
			return action.Target{}, action.ErrNotFound
		}
		return action.Target{ID: target, Name: "thing", State: "ok"}, nil
	}
	nothing := func(context.Context, action.Request, *action.TaskRun) error { return nil }
	var defs []action.Def
	for i, v := range []string{"look", "poke", "restart", "remove"} {
		defs = append(defs, action.Def{ID: "test.thing." + v, Title: strings.ToUpper(v[:1]) + v[1:], Tier: action.Tier(i), Resolve: resolve, Execute: nothing})
	}
	return defs
}

// bearerTransport adds an API token to every request.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// withToken is a client that sends token, and the cookies in jar if any.
func withToken(jar http.CookieJar, token string) *http.Client {
	return &http.Client{Jar: jar, Transport: bearerTransport{token}}
}

// mintToken has c create a token and returns its secret.
func mintToken(t *testing.T, e *env, c *http.Client, name string, scopes ...string) string {
	t.Helper()
	status, _, b := e.call(t, c, "POST", "/api/tokens", map[string]any{"name": name, "scopes": scopes})
	var out struct {
		Token  auth.APIToken `json:"token"`
		Secret string        `json:"secret"`
	}
	if status != http.StatusCreated || json.Unmarshal(b, &out) != nil || !strings.HasPrefix(out.Secret, "cdt_") {
		t.Fatalf("mint %s %v: %d %s", name, scopes, status, b)
	}
	return out.Secret
}

func TestTokensAPI(t *testing.T) {
	e, _ := fullEnv(t)
	owner := signIn(t, e)
	_, vic := addUser(t, e, owner, "vic", auth.RoleViewer)

	status, _, b := e.raw(t, owner, "POST", "/api/tokens", []byte(`{"name":"backup script","scopes":["graph.read","test.thing.*"],"expires_in_days":30}`),
		map[string]string{"Accept-Encoding": "gzip"})
	var created struct {
		Token  auth.APIToken `json:"token"`
		Secret string        `json:"secret"`
	}
	if status != http.StatusCreated || json.Unmarshal(b, &created) != nil {
		t.Fatalf("create: %d %s", status, b)
	}
	if exp := created.Token.ExpiresAt; exp == nil || time.Until(*exp) < 29*24*time.Hour || time.Until(*exp) > 30*24*time.Hour {
		t.Fatalf("expires %v", exp)
	}
	secret := created.Secret

	for name, body := range map[string]map[string]any{
		"no scopes":      {"name": "x", "scopes": []string{}},
		"a bad scope":    {"name": "x", "scopes": []string{"Not A Scope"}},
		"approvals":      {"name": "x", "scopes": []string{"approvals.decide"}},
		"no name":        {"name": " ", "scopes": []string{"graph.read"}},
		"never expiring": {"name": "x", "scopes": []string{"graph.read"}, "expires_in_days": 0},
		"two years":      {"name": "x", "scopes": []string{"graph.read"}, "expires_in_days": 730},
	} {
		if status, code, b := e.call(t, owner, "POST", "/api/tokens", body); status != http.StatusUnprocessableEntity || code != "invalid" {
			t.Errorf("%s: %d %s", name, status, b)
		}
	}
	// A viewer can't hand a token what the role can't read.
	if status, _, b := e.call(t, vic, "POST", "/api/tokens", map[string]any{"name": "x", "scopes": []string{"audit.read"}}); status != http.StatusUnprocessableEntity {
		t.Fatalf("viewer with audit.read: %d %s", status, b)
	}
	vicToken := mintToken(t, e, vic, "vic's", "graph.read")
	_ = vicToken

	list := func(c *http.Client) []auth.APIToken {
		t.Helper()
		status, _, b := e.call(t, c, "GET", "/api/tokens", nil)
		var out struct{ Tokens []auth.APIToken }
		if status != http.StatusOK || json.Unmarshal(b, &out) != nil {
			t.Fatalf("list: %d %s", status, b)
		}
		if strings.Contains(string(b), secret) || strings.Contains(string(b), "hash") {
			t.Fatalf("the list shows a secret: %s", b)
		}
		return out.Tokens
	}
	if got := list(owner); len(got) != 2 {
		t.Fatalf("the owner sees %d tokens", len(got))
	}
	mine := list(vic)
	if len(mine) != 1 || mine[0].Username != "vic" {
		t.Fatalf("vic sees %+v", mine)
	}
	// Revoking: your own, or any as an admin; once.
	if status, _, _ := e.call(t, vic, "DELETE", "/api/tokens/"+created.Token.ID, nil); status != http.StatusNotFound {
		t.Fatalf("vic revoking the owner's token: %d", status)
	}
	if status, _, b := e.call(t, owner, "DELETE", "/api/tokens/"+mine[0].ID, nil); status != http.StatusOK || !strings.Contains(string(b), `"revoked_at":"`) {
		t.Fatalf("owner revoking vic's: %d %s", status, b)
	}
	if status, code, _ := e.call(t, owner, "DELETE", "/api/tokens/"+mine[0].ID, nil); status != http.StatusConflict || code != "already_revoked" {
		t.Fatalf("revoking twice: %d %s", status, code)
	}

	// The scope picker: reads per role, actions with T3 out of reach.
	_, _, b = e.call(t, vic, "GET", "/api/tokens/scopes", nil)
	var sc struct {
		Reads []struct {
			Scope     string
			Available bool
		}
		Actions []struct {
			Scope     string
			Tier      string
			Available bool
		}
		Families []struct{ Scope string }
	}
	if err := json.Unmarshal(b, &sc); err != nil || len(sc.Reads) != 4 || len(sc.Actions) != 4 || len(sc.Families) != 1 || sc.Families[0].Scope != "test.*" {
		t.Fatalf("scopes: %v %s", err, b)
	}
	for _, a := range sc.Actions {
		if want := a.Tier == "T0"; a.Available != want { // a viewer runs T0 only
			t.Errorf("viewer offered %s (%s): %v", a.Scope, a.Tier, a.Available)
		}
	}
	for _, r := range sc.Reads {
		if want := r.Scope == "graph.read" || r.Scope == "inbox.read"; r.Available != want {
			t.Errorf("viewer offered %s: %v", r.Scope, r.Available)
		}
	}

	// Creating and revoking are audited, and no secret reaches the log.
	ada := addAdmin(t, e, owner)
	_, _, b = e.call(t, ada, "GET", "/api/audit/export", nil)
	if strings.Contains(string(b), secret) || !strings.Contains(string(b), `"action":"tokens.create"`) || !strings.Contains(string(b), `"action":"tokens.revoke"`) {
		t.Fatalf("audit export: %s", b)
	}
}

func addAdmin(t *testing.T, e *env, owner *http.Client) *http.Client {
	t.Helper()
	_, c := addUser(t, e, owner, "ada", auth.RoleAdmin)
	return c
}

type taskBody struct {
	Task *struct {
		ID         string
		State      string
		Actor      struct{ Kind, Name string }
		ApprovedBy *struct{ Kind, Name string } `json:"approved_by"`
	}
	Approval *struct {
		ID    string
		State string
		Plan  struct{ Hash string }
	}
}

func TestTokenActionsAndApprovals(t *testing.T) {
	e, _ := fullEnv(t)
	owner := signIn(t, e)
	ada := addAdmin(t, e, owner)
	_, vic := addUser(t, e, owner, "vic", auth.RoleViewer)
	tok := withToken(nil, mintToken(t, e, owner, "deploy bot", "*"))

	plan := func(c *http.Client, act string) (int, string, map[string]any) {
		t.Helper()
		status, code, b := e.call(t, c, "POST", "/api/actions/plan", map[string]string{"action": act, "target": "thing:1"})
		var p map[string]any
		_ = json.Unmarshal(b, &p)
		return status, code, p
	}
	approve := func(c *http.Client, p map[string]any) (int, string, taskBody) {
		t.Helper()
		status, code, b := e.call(t, c, "POST", "/api/plans/"+p["id"].(string)+"/approve", map[string]any{"hash": p["hash"]})
		var out taskBody
		_ = json.Unmarshal(b, &out)
		return status, code, out
	}
	waitTask := func(c *http.Client, id string) taskBody {
		t.Helper()
		for range 500 {
			_, _, b := e.call(t, c, "GET", "/api/tasks/"+id, nil)
			var task struct {
				State string
			}
			_ = json.Unmarshal(b, &task)
			if task.State == action.StateSucceeded || task.State == action.StateFailed {
				var out taskBody
				_ = json.Unmarshal([]byte(`{"task":`+string(b)+`}`), &out)
				return out
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("task %s didn't finish", id)
		return taskBody{}
	}

	// T0 and T1: the token approves its own plan.
	for _, act := range []string{"test.thing.look", "test.thing.poke"} {
		status, _, p := plan(tok, act)
		if status != http.StatusOK {
			t.Fatalf("plan %s: %d %v", act, status, p)
		}
		status, _, out := approve(tok, p)
		if status != http.StatusAccepted || out.Task == nil {
			t.Fatalf("approve %s: %d %+v", act, status, out)
		}
		if done := waitTask(tok, out.Task.ID); done.Task.State != action.StateSucceeded || done.Task.Actor.Kind != "token" || done.Task.Actor.Name != "deploy bot (owner)" {
			t.Fatalf("task %+v", done.Task)
		}
	}

	// T3 and above: never a token's.
	if status, code, p := plan(tok, "test.thing.remove"); status != http.StatusForbidden || code != "forbidden" || !strings.Contains(p["error"].(map[string]any)["message"].(string), "API tokens can't run T3") {
		t.Fatalf("T3 by a token: %d %s %v", status, code, p)
	}

	// T2: the plan waits for a person.
	_, _, p := plan(tok, "test.thing.restart")
	status, _, out := approve(tok, p)
	if status != http.StatusAccepted || out.Task != nil || out.Approval == nil || out.Approval.State != action.ApprovalAwaiting || out.Approval.Plan.Hash != p["hash"] {
		t.Fatalf("T2 by a token: %d %+v", status, out)
	}
	apv := out.Approval.ID
	// It's in the Inbox for approvers only.
	inbox := func(c *http.Client) string {
		_, _, b := e.call(t, c, "GET", "/api/inbox", nil)
		return string(b)
	}
	if !strings.Contains(inbox(ada), `"approval.`+apv+`"`) || strings.Contains(inbox(vic), `"approval.`) {
		t.Fatalf("inbox: admin %s / viewer %s", inbox(ada), inbox(vic))
	}
	// The token polls it; other people can't read it.
	if status, _, b := e.call(t, tok, "GET", "/api/approvals/"+apv, nil); status != http.StatusOK || !strings.Contains(string(b), `"awaiting_approval"`) {
		t.Fatalf("token polling: %d %s", status, b)
	}
	if status, _, _ := e.call(t, vic, "GET", "/api/approvals/"+apv, nil); status != http.StatusNotFound {
		t.Fatalf("a viewer read it: %d", status)
	}
	if status, _, b := e.call(t, ada, "GET", "/api/approvals", nil); status != http.StatusOK || !strings.Contains(string(b), apv) {
		t.Fatalf("approvals list: %d %s", status, b)
	}
	// A person approves it, in a session, with a step-up made since.
	if status, code, _ := e.call(t, ada, "POST", "/api/approvals/"+apv+"/approve", map[string]any{"hash": p["hash"]}); status != http.StatusForbidden || code != "step_up_required" {
		t.Fatalf("approve without a step-up: %d %s", status, code)
	}
	stepUp(t, e, ada)
	status, _, b := e.call(t, ada, "POST", "/api/approvals/"+apv+"/approve", map[string]any{"hash": p["hash"]})
	if err := json.Unmarshal(b, &out); status != http.StatusAccepted || err != nil || out.Task == nil {
		t.Fatalf("approve: %d %s", status, b)
	}
	done := waitTask(tok, out.Task.ID)
	if done.Task.State != action.StateSucceeded || done.Task.ApprovedBy == nil || done.Task.ApprovedBy.Name != "ada" || done.Task.Actor.Kind != "token" {
		t.Fatalf("approved task %+v", done.Task)
	}
	if _, _, b := e.call(t, tok, "GET", "/api/approvals/"+apv, nil); !strings.Contains(string(b), `"approved"`) {
		t.Fatalf("after: %s", b)
	}

	// Deny.
	_, _, p = plan(tok, "test.thing.restart")
	_, _, out = approve(tok, p)
	if status, _, b := e.call(t, ada, "POST", "/api/approvals/"+out.Approval.ID+"/deny", map[string]string{"reason": "Not now."}); status != http.StatusOK || !strings.Contains(string(b), `"denied"`) {
		t.Fatalf("deny: %d %s", status, b)
	}
	if status, _, again := approve(tok, p); status != http.StatusAccepted || again.Approval == nil || again.Approval.State != action.ApprovalDenied {
		t.Fatalf("asking again after a deny: %d %+v", status, again)
	}

	// Revoking the token withdraws what it left waiting.
	_, _, p = plan(tok, "test.thing.restart")
	_, _, out = approve(tok, p)
	pending := out.Approval.ID
	var tokID string
	_, _, b = e.call(t, owner, "GET", "/api/tokens", nil)
	var tl struct{ Tokens []auth.APIToken }
	_ = json.Unmarshal(b, &tl)
	for _, x := range tl.Tokens {
		if x.Name == "deploy bot" {
			tokID = x.ID
		}
	}
	if status, _, _ := e.call(t, owner, "DELETE", "/api/tokens/"+tokID, nil); status != http.StatusOK {
		t.Fatal("revoke")
	}
	if _, _, b := e.call(t, ada, "GET", "/api/approvals/"+pending, nil); !strings.Contains(string(b), `"expired"`) {
		t.Fatalf("a revoked token's approval: %s", b)
	}
	if status, code, _ := plan(tok, "test.thing.look"); status != http.StatusUnauthorized || code != "token_revoked" {
		t.Fatalf("a revoked token: %d %s", status, code)
	}

	// Role ceiling and scopes.
	vt := withToken(nil, mintToken(t, e, vic, "vic's", "*"))
	if status, code, _ := plan(vt, "test.thing.poke"); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("a viewer's token at T1: %d %s", status, code)
	}
	if status, _, _ := plan(vt, "test.thing.look"); status != http.StatusOK {
		t.Fatalf("a viewer's token at T0: %d", status)
	}
	looker := withToken(nil, mintToken(t, e, owner, "looker", "test.thing.look", "graph.read"))
	if status, code, _ := plan(looker, "test.thing.poke"); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("out of scope: %d %s", status, code)
	}
	if status, _, _ := e.call(t, looker, "GET", "/api/graph", nil); status != http.StatusOK {
		t.Fatalf("graph.read: %d", status)
	}
	if status, code, _ := e.call(t, looker, "GET", "/api/inbox", nil); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("inbox without inbox.read: %d %s", status, code)
	}
	if status, code, _ := e.call(t, looker, "GET", "/api/audit", nil); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("audit without audit.read: %d %s", status, code)
	}

	// The audit log names the token and its user, and every step.
	_, _, b = e.call(t, ada, "GET", "/api/audit?limit=500", nil)
	var log struct {
		Events []struct {
			Action, Result string
			Actor          struct{ Kind, ID, Name, Session string }
		}
	}
	if err := json.Unmarshal(b, &log); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ev := range log.Events {
		seen[ev.Actor.Kind+" "+ev.Action+" "+ev.Result] = true
		if ev.Actor.Kind == "token" && (ev.Actor.Name != "deploy bot (owner)" && ev.Actor.Name != "vic's (vic)" && ev.Actor.Name != "looker (owner)" || ev.Actor.Session != "" || !strings.HasPrefix(ev.Actor.ID, "tok_")) {
			t.Errorf("token actor %+v", ev.Actor)
		}
	}
	for _, want := range []string{
		"token test.thing.look ok", "token test.thing.poke ok", "token test.thing.remove refused",
		"token approval.request ok", "user approval.approve refused", "user approval.approve ok", "token test.thing.restart ok",
		"user approval.deny ok", "system approval.expire ok", "user tokens.create ok", "user tokens.revoke ok",
	} {
		if !seen[want] {
			t.Errorf("not audited: %s (have %v)", want, seen)
		}
	}
}
