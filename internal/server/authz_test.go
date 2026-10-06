package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/correlate"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/onboarding"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

// fullEnv mounts every optional subsystem, so every route in the table is
// on the router. No integration is registered: nothing reaches the network.
func fullEnv(t *testing.T) (*env, *store.Store) {
	t.Helper()
	var st *store.Store
	e := newEnvWith(t, func(d *Deps) {
		st = d.Store
		v, err := vault.New(d.Store, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		reg := integration.NewRegistry()
		tp := topics.New(graph.New(), time.Now())
		m, err := runtime.New(runtime.Options{Store: d.Store, Vault: v, Registry: reg, Topics: tp, Log: d.Log})
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
		sec, err := security.New(t.Context(), d.Store, d.Log)
		if err != nil {
			t.Fatal(err)
		}
		wall := time.Now()
		drv, err := demo.New(topics.New(graph.New(), wall), demo.Options{Seed: 1, At: demo.DefaultAt, Clock: func() time.Time { return wall }})
		if err != nil {
			t.Fatal(err)
		}
		d.Vault, d.Integrations, d.Onboarding, d.Security, d.Demo = v, m, ob, sec, drv
		d.Correlator = correlate.New(correlate.Options{Topics: tp, Store: d.Store, Log: d.Log})
		eng, err := action.New(t.Context(), action.Options{
			Store: d.Store, Audit: audit.New(d.Store), Registry: action.NewRegistry(), Graph: tp.Graph,
			Policy:   authz.NewPolicy(d.Store, d.Log).ForEngine(),
			Sessions: action.Sessions{StepUpUntil: func(id auth.Identity) time.Time { return id.Session.StepUpUntil }},
			Log:      d.Log,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(eng.Close)
		d.Actions = eng
		d.Audit = audit.New(d.Store)
	})
	return e, st
}

// raw sends body as is (JSON content type when non-nil) and returns the
// status, the error code if any, and the body.
func (e *env) raw(t *testing.T, c *http.Client, method, path string, body []byte, hdr map[string]string) (int, string, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var env struct {
		Error struct{ Code, Message string }
	}
	_ = json.Unmarshal(b, &env)
	return resp.StatusCode, env.Error.Code, b
}

// call sends v as JSON.
func (e *env) call(t *testing.T, c *http.Client, method, path string, v any) (int, string, []byte) {
	t.Helper()
	var b []byte
	if v != nil {
		b, _ = json.Marshal(v)
	}
	return e.raw(t, c, method, path, b, nil)
}

const testPassword = "correct horse battery"

// addUser has the owner create an account with role and returns its ID and
// a signed-in client for it.
func addUser(t *testing.T, e *env, owner *http.Client, name, role string) (string, *http.Client) {
	t.Helper()
	status, _, b := e.call(t, owner, "POST", "/api/users", map[string]string{"username": name, "password": testPassword, "role": role})
	if status != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, status, b)
	}
	var u auth.UserInfo
	if err := json.Unmarshal(b, &u); err != nil || u.Role != role || u.Username != name {
		t.Fatalf("created %s", b)
	}
	return u.ID, login(t, e, name)
}

func login(t *testing.T, e *env, name string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if status, _, b := e.call(t, c, "POST", "/api/auth/login", map[string]string{"username": name, "password": testPassword}); status != http.StatusOK {
		t.Fatalf("login %s: %d %s", name, status, b)
	}
	return c
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// TestRouteMatrix calls every route in internal/authz's table through the
// real router, anonymously and as each role, and checks Guard's answer
// against the row. (internal/authz's TestEveryRouteHasARow checks that every
// registered route has a row; Guard refuses one without.)
//
// Unsafe requests carry a malformed JSON body: a route that's allowed
// answers 400 (or 404 for the made-up IDs) before doing anything, so the
// matrix changes nothing and reaches nothing.
func TestRouteMatrix(t *testing.T) {
	e, _ := fullEnv(t)
	clients := map[string]*http.Client{auth.RoleOwner: signIn(t, e)}
	for _, role := range []string{auth.RoleAdmin, auth.RoleOperator, auth.RoleViewer, auth.RoleHousehold} {
		_, clients[role] = addUser(t, e, clients[auth.RoleOwner], "a-"+role, role)
	}
	anon := &http.Client{}

	checked := 0
	for _, row := range authz.Routes {
		method, path, ok := strings.Cut(row.Pattern, " ")
		if !ok || strings.HasSuffix(path, "/") {
			continue // the catch-alls ("/", "/api/") are public; the web tests cover them
		}
		path = pathParam.ReplaceAllString(path, "zz-matrix")
		var body []byte
		if method != "GET" {
			body = []byte("{")
		}
		hit := func(c *http.Client) (int, string, string) {
			status, code, b := e.raw(t, c, method, path, body, nil)
			if code == "no_authz_rule" {
				t.Errorf("%s: Guard found no row: %s", row.Pattern, b)
			}
			return status, code, string(b)
		}

		// Is it on the router? The API's fallback answers what isn't. Public
		// routes are probed anonymously (one of them is sign-out).
		prober := clients[auth.RoleOwner]
		if row.Access == authz.Public {
			prober = anon
		}
		status, _, b := hit(prober)
		if status == http.StatusNotFound && strings.Contains(b, "no such endpoint") || status == http.StatusMethodNotAllowed {
			if !row.Optional {
				t.Errorf("%s isn't on the router (%d)", row.Pattern, status)
			}
			continue
		}
		checked++
		if row.Access == authz.Public {
			if status == http.StatusUnauthorized {
				t.Errorf("%s is public but asked for a session", row.Pattern)
			}
			continue
		}
		if status, code, _ := hit(anon); status != http.StatusUnauthorized || code != "unauthenticated" {
			t.Errorf("%s without a session: %d %s", row.Pattern, status, code)
		}
		for role, c := range clients {
			status, code, b := hit(c)
			denied := status == http.StatusForbidden && code == "forbidden"
			switch {
			case status == http.StatusUnauthorized:
				t.Errorf("%s as %s: 401 %s", row.Pattern, role, b)
			case row.Access.Allows(role) && denied:
				t.Errorf("%s as %s: refused, but the table allows it", row.Pattern, role)
			case !row.Access.Allows(role) && !denied:
				t.Errorf("%s as %s: %d %s, but the table refuses it", row.Pattern, role, status, b)
			}
		}
	}
	if checked < 50 {
		t.Fatalf("checked only %d routes", checked)
	}
}

func usersList(t *testing.T, e *env, c *http.Client) map[string]auth.UserInfo {
	t.Helper()
	status, _, b := e.call(t, c, "GET", "/api/users", nil)
	var list []auth.UserInfo
	if status != http.StatusOK || json.Unmarshal(b, &list) != nil {
		t.Fatalf("GET /api/users: %d %s", status, b)
	}
	out := map[string]auth.UserInfo{}
	owners := 0
	for i, u := range list {
		out[u.Username] = u
		if u.Role == auth.RoleOwner {
			owners++
			if i != 0 {
				t.Errorf("the owner is listed at %d", i)
			}
		}
	}
	if owners != 1 {
		t.Fatalf("%d owners: %s", owners, b)
	}
	return out
}

func stepUp(t *testing.T, e *env, c *http.Client) {
	t.Helper()
	if status, _, b := e.call(t, c, "POST", "/api/auth/step-up", map[string]string{"password": testPassword}); status != http.StatusOK {
		t.Fatalf("step-up: %d %s", status, b)
	}
}

func TestUsersAndOwnership(t *testing.T) {
	e, st := fullEnv(t)
	owner := signIn(t, e)
	adaID, ada := addUser(t, e, owner, "ada", auth.RoleAdmin)
	vicID, vic := addUser(t, e, owner, "vic", auth.RoleViewer)
	halID, hal := addUser(t, e, owner, "hal", auth.RoleHousehold)

	// Creating: never a second owner, never a taken name, never a made-up role.
	for _, c := range []struct {
		body   map[string]string
		status int
	}{
		{map[string]string{"username": "eve", "password": testPassword, "role": "owner"}, http.StatusUnprocessableEntity},
		{map[string]string{"username": "eve", "password": testPassword, "role": "root"}, http.StatusUnprocessableEntity},
		{map[string]string{"username": "eve", "password": "short", "role": "viewer"}, http.StatusUnprocessableEntity},
		{map[string]string{"username": "ADA", "password": testPassword, "role": "viewer"}, http.StatusConflict},
	} {
		if status, _, b := e.call(t, owner, "POST", "/api/users", c.body); status != c.status {
			t.Errorf("create %v: %d %s", c.body, status, b)
		}
	}

	users := usersList(t, e, owner)
	if len(users) != 4 || users["owner"].Role != auth.RoleOwner || users["vic"].HasPasskey || users["vic"].Disabled || users["vic"].LastSeenAt == nil {
		t.Fatalf("users: %+v", users)
	}
	ownerID := users["owner"].ID

	// The owner can't be demoted or disabled, by anyone, except by transfer.
	for _, body := range []map[string]any{{"role": "admin"}, {"disabled": true}, {"role": "owner"}} {
		status, code, b := e.call(t, owner, "PUT", "/api/users/"+ownerID, body)
		if status != http.StatusConflict && status != http.StatusUnprocessableEntity {
			t.Errorf("owner PUT self %v: %d %s %s", body, status, code, b)
		}
		if status, code, _ := e.call(t, ada, "PUT", "/api/users/"+ownerID, body); status != http.StatusForbidden || code != "forbidden" {
			t.Errorf("admin PUT owner %v: %d %s", body, status, code)
		}
	}
	// Nobody becomes the owner through PUT either.
	if status, _, _ := e.call(t, owner, "PUT", "/api/users/"+vicID, map[string]string{"role": "owner"}); status != http.StatusUnprocessableEntity {
		t.Errorf("PUT role owner: %d", status)
	}
	if status, _, _ := e.call(t, owner, "PUT", "/api/users/u_nobody", map[string]string{"role": "viewer"}); status != http.StatusNotFound {
		t.Errorf("PUT unknown user: %d", status)
	}

	// A role change takes effect on the user's next request, same session.
	if status, code, _ := e.raw(t, vic, "PUT", "/api/boards/b_x/layout", []byte("{"), nil); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("viewer saving a layout: %d %s", status, code)
	}
	if status, _, b := e.call(t, ada, "PUT", "/api/users/"+vicID, map[string]string{"role": "admin"}); status != http.StatusOK {
		t.Fatalf("admin promoting a viewer: %d %s", status, b)
	}
	if status, code, _ := e.raw(t, vic, "PUT", "/api/boards/b_x/layout", []byte("{"), nil); status == http.StatusForbidden || status == http.StatusUnauthorized {
		t.Fatalf("promoted, saving a layout: %d %s", status, code)
	}

	// Disabling ends the user's sessions at once and stops new ones.
	if status, _, _ := e.call(t, hal, "GET", "/api/boards", nil); status != http.StatusOK {
		t.Fatalf("household reading boards: %d", status)
	}
	if status, _, b := e.call(t, ada, "PUT", "/api/users/"+halID, map[string]bool{"disabled": true}); status != http.StatusOK || !strings.Contains(string(b), `"disabled":true`) {
		t.Fatalf("disable: %d %s", status, b)
	}
	if status, _, _ := e.call(t, hal, "GET", "/api/boards", nil); status != http.StatusUnauthorized {
		t.Fatalf("a disabled user's session still works: %d", status)
	}
	var n int
	if err := st.Reader().QueryRow(`SELECT count(*) FROM sessions WHERE user_id = ?`, halID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a disabled user keeps %d sessions (%v)", n, err)
	}
	if status, _, _ := e.call(t, &http.Client{}, "POST", "/api/auth/login", map[string]string{"username": "hal", "password": testPassword}); status != http.StatusUnauthorized {
		t.Fatalf("a disabled user signed in: %d", status)
	}
	if status, _, _ := e.call(t, owner, "PUT", "/api/users/"+halID, map[string]bool{"disabled": false}); status != http.StatusOK {
		t.Fatalf("enable: %d", status)
	}
	login(t, e, "hal")

	// Handing over ownership is T3: it needs a step-up, and an expired one won't do.
	if status, code, _ := e.call(t, owner, "POST", "/api/users/"+adaID+"/transfer-ownership", nil); status != http.StatusForbidden || code != "step_up_required" {
		t.Fatalf("transfer without a step-up: %d %s", status, code)
	}
	if _, err := st.Writer().Exec(`UPDATE sessions SET step_up_until = ? WHERE user_id = ?`, time.Now().Add(-time.Second).UnixMilli(), ownerID); err != nil {
		t.Fatal(err)
	}
	if status, code, _ := e.call(t, owner, "POST", "/api/users/"+adaID+"/transfer-ownership", nil); status != http.StatusForbidden || code != "step_up_required" {
		t.Fatalf("transfer with an expired step-up: %d %s", status, code)
	}
	// Only the owner may hand it over, step-up or not.
	stepUp(t, e, ada)
	if status, code, _ := e.call(t, ada, "POST", "/api/users/"+adaID+"/transfer-ownership", nil); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("an admin taking ownership: %d %s", status, code)
	}
	stepUp(t, e, owner)
	if status, _, _ := e.call(t, owner, "PUT", "/api/users/"+halID, map[string]bool{"disabled": true}); status != http.StatusOK {
		t.Fatal("disable")
	}
	if status, _, b := e.call(t, owner, "POST", "/api/users/"+halID+"/transfer-ownership", nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("transfer to a disabled user: %d %s", status, b)
	}
	if status, _, b := e.call(t, owner, "POST", "/api/users/"+adaID+"/transfer-ownership", nil); status != http.StatusOK || !strings.Contains(string(b), `"role":"owner"`) {
		t.Fatalf("transfer: %d %s", status, b)
	}
	users = usersList(t, e, ada)
	if users["ada"].Role != auth.RoleOwner || users["owner"].Role != auth.RoleAdmin {
		t.Fatalf("after the transfer: %+v", users)
	}
	// The old owner is an admin from their next request on.
	if status, code, _ := e.call(t, owner, "PUT", "/api/settings/security", map[string]string{"posture": "strict"}); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("the old owner changing security: %d %s", status, code)
	}
	if status, _, _ := e.call(t, ada, "PUT", "/api/settings/security", map[string]string{"posture": "strict"}); status != http.StatusOK {
		t.Fatalf("the new owner changing security: %d", status)
	}
	// And the new owner is protected like the old one was.
	if status, code, _ := e.call(t, owner, "PUT", "/api/users/"+adaID, map[string]bool{"disabled": true}); status != http.StatusForbidden || code != "forbidden" {
		t.Fatalf("the old owner disabling the new one: %d %s", status, code)
	}
}

func TestStepUpPassword(t *testing.T) {
	e, st := fullEnv(t)
	owner := signIn(t, e)
	session := func() (until any, methods []any) {
		t.Helper()
		_, _, b := e.call(t, owner, "GET", "/api/auth/session", nil)
		var s map[string]any
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		if _, ok := s["step_up_until"]; !ok {
			t.Fatalf("session without step_up_until: %s", b)
		}
		m, ok := s["step_up_methods"].([]any)
		if !ok {
			t.Fatalf("session without step_up_methods: %s", b)
		}
		return s["step_up_until"], m
	}
	if until, methods := session(); until != nil || len(methods) != 1 || methods[0] != "password" {
		t.Fatalf("before: %v %v", until, methods)
	}
	if status, _, b := e.call(t, owner, "POST", "/api/auth/step-up/begin", nil); status != http.StatusOK || !strings.Contains(string(b), `"method":"password"`) {
		t.Fatalf("begin: %d %s", status, b)
	}
	if status, code, _ := e.call(t, owner, "POST", "/api/auth/step-up", map[string]string{"password": "not the password"}); status != http.StatusForbidden || code != "step_up_failed" {
		t.Fatalf("wrong password: %d %s", status, code)
	}
	if until, _ := session(); until != nil {
		t.Fatal("a failed step-up granted one")
	}
	// Bracket the call: under load, hashing the password alone can take
	// longer than any fixed tolerance.
	before := time.Now()
	status, _, b := e.call(t, owner, "POST", "/api/auth/step-up", map[string]string{"password": testPassword})
	after := time.Now()
	var got struct{ Until time.Time }
	if status != http.StatusOK || json.Unmarshal(b, &got) != nil {
		t.Fatalf("step-up: %d %s", status, b)
	}
	if got.Until.Before(before.Add(auth.StepUpTTL).Truncate(time.Millisecond)) || got.Until.After(after.Add(auth.StepUpTTL)) {
		t.Fatalf("grant ends %v, want five minutes from the call (%v to %v)", got.Until, before, after)
	}
	if until, _ := session(); until == nil {
		t.Fatal("the session doesn't show the grant")
	}

	// Passkey only: changing the rule needs the step-up we have.
	if status, _, b := e.call(t, owner, "PUT", "/api/settings/security", map[string]string{"step_up": "passkey_only"}); status != http.StatusOK || !strings.Contains(string(b), `"step_up":"passkey_only"`) {
		t.Fatalf("passkey_only: %d %s", status, b)
	}
	if _, methods := session(); len(methods) != 0 {
		t.Fatalf("methods under passkey_only without a passkey: %v", methods)
	}
	if status, code, _ := e.call(t, owner, "POST", "/api/auth/step-up/begin", nil); status != http.StatusForbidden || code != "passkey_required" {
		t.Fatalf("begin under passkey_only: %d %s", status, code)
	}
	if status, code, _ := e.call(t, owner, "POST", "/api/auth/step-up", map[string]string{"password": testPassword}); status != http.StatusForbidden || code != "passkey_required" {
		t.Fatalf("password under passkey_only: %d %s", status, code)
	}

	// Once the grant lapses, the rule can't be changed back without a new one;
	// the posture still can.
	if _, err := st.Writer().Exec(`UPDATE sessions SET step_up_until = NULL`); err != nil {
		t.Fatal(err)
	}
	if status, code, _ := e.call(t, owner, "PUT", "/api/settings/security", map[string]string{"step_up": "passkey_or_password"}); status != http.StatusForbidden || code != "step_up_required" {
		t.Fatalf("changing step_up without a step-up: %d %s", status, code)
	}
	if status, code, _ := e.call(t, owner, "PUT", "/api/settings/security", map[string]string{"posture": "standard", "step_up": "passkey_or_password"}); status != http.StatusForbidden || code != "step_up_required" {
		t.Fatalf("both without a step-up: %d %s", status, code)
	}
	status, _, b = e.call(t, owner, "PUT", "/api/settings/security", map[string]string{"posture": "strict", "step_up": "passkey_only"})
	if status != http.StatusOK || !strings.Contains(string(b), `"posture":"strict"`) || !strings.Contains(string(b), `"step_up":"passkey_only"`) {
		t.Fatalf("posture with an unchanged step_up: %d %s", status, b)
	}
	if status, _, _ := e.call(t, owner, "PUT", "/api/settings/security", map[string]string{"step_up": "sometimes"}); status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown rule: %d", status)
	}
	if status, _, _ := e.call(t, owner, "PUT", "/api/settings/security", map[string]string{}); status != http.StatusUnprocessableEntity {
		t.Fatalf("empty PUT: %d", status)
	}
}

func TestStepUpRateLimited(t *testing.T) {
	e, _ := fullEnv(t)
	owner := signIn(t, e)
	// The budget is 10 then one per 6 s. Allow for a slow machine refilling
	// a few tokens while argon2 runs: what matters is that it does limit.
	limited := false
	for i := 0; i < 30 && !limited; i++ {
		status, _, _ := e.call(t, owner, "POST", "/api/auth/step-up", map[string]string{"password": "not the password"})
		limited = status == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("thirty wrong step-ups in a row were never rate-limited")
	}
	// Signing in has its own budget, so a fumbled step-up locks nobody out.
	if status, _, _ := e.call(t, &http.Client{}, "POST", "/api/auth/login", map[string]string{"username": "owner", "password": testPassword}); status != http.StatusOK {
		t.Fatalf("login after a rate-limited step-up: %d", status)
	}
}

// TestNewUnsafeRoutesRefuseCrossOrigin: the users, step-up and security
// changes sit behind the same cross-origin protection as everything else.
func TestNewUnsafeRoutesRefuseCrossOrigin(t *testing.T) {
	e, _ := fullEnv(t)
	owner := signIn(t, e)
	stepUp(t, e, owner)
	evil := map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/api/users", `{"username":"eve","password":"correct horse battery","role":"admin"}`},
		{"PUT", "/api/users/u_x", `{"role":"admin"}`},
		{"POST", "/api/users/u_x/transfer-ownership", `{}`},
		{"POST", "/api/auth/step-up/begin", `{}`},
		{"POST", "/api/auth/step-up", `{"password":"correct horse battery"}`},
		{"PUT", "/api/settings/security", `{"step_up":"passkey_only"}`},
	} {
		if status, code, _ := e.raw(t, owner, r.method, r.path, []byte(r.body), evil); status != http.StatusForbidden || code != "cross_origin" {
			t.Errorf("cross-origin %s %s: %d %s", r.method, r.path, status, code)
		}
	}
	// The trusted extra origin still works.
	if status, _, b := e.raw(t, owner, "POST", "/api/auth/step-up/begin", nil, map[string]string{"Origin": "https://dash.example.com", "Sec-Fetch-Site": "cross-site"}); status != http.StatusOK {
		t.Errorf("allowed origin: %d %s", status, b)
	}
}

// Changes made outside the engine reach the audit log too: users, and the
// security settings, including the refused attempts.
func TestChangesAreAudited(t *testing.T) {
	e, _ := fullEnv(t)
	owner := signIn(t, e)
	_, ada := addUser(t, e, owner, "ada", auth.RoleAdmin)
	if status, _, b := e.call(t, owner, "PUT", "/api/settings/security", map[string]string{"posture": "standard"}); status != http.StatusOK {
		t.Fatalf("set posture: %d %s", status, b)
	}
	// Changing the step-up rule without a step-up is refused, and audited.
	if status, code, _ := e.call(t, owner, "PUT", "/api/settings/security", map[string]string{"step_up": "passkey_only"}); code != "step_up_required" {
		t.Fatalf("step_up without a grant: %d %s", status, code)
	}
	_, _, body := e.call(t, ada, "GET", "/api/audit?limit=10", nil)
	var got struct {
		Events []struct {
			Action string `json:"action"`
			Result string `json:"result"`
			Actor  struct {
				Name string `json:"name"`
			} `json:"actor"`
			Params map[string]any `json:"params"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("audit: %v %s", err, body)
	}
	var seen []string
	for _, ev := range got.Events {
		seen = append(seen, ev.Action+" "+ev.Result+" "+ev.Actor.Name)
		if _, ok := ev.Params["password"]; ok {
			t.Fatalf("a password reached the audit log: %+v", ev)
		}
	}
	want := []string{"settings.security.update refused owner", "settings.security.update ok owner", "users.create ok owner"}
	if !slices.Equal(seen, want) {
		t.Fatalf("audited %v, want %v", seen, want)
	}
	_, _, body = e.call(t, ada, "GET", "/api/audit/verify", nil)
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("verify: %s", body)
	}
}
