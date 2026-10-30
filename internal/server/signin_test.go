package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// signInEnv is a hub with the security settings, a vault, the audit log,
// and 127.0.0.0/8 as a trusted proxy, so the test client can send
// forward-auth headers.
func signInEnv(t *testing.T) (*env, *store.Store, *vault.Vault) {
	t.Helper()
	var st *store.Store
	var v *vault.Vault
	e := newEnvWith(t, func(d *Deps) {
		st = d.Store
		var err error
		v, err = vault.New(d.Store, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		proxies := httpx.NewProxies([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
		a, err := auth.New(t.Context(), d.Store, d.Log, proxies)
		if err != nil {
			t.Fatal(err)
		}
		sec, err := security.New(t.Context(), d.Store, d.Log)
		if err != nil {
			t.Fatal(err)
		}
		d.Auth, d.Proxies, d.Vault, d.Security, d.Audit = a, proxies, v, sec, audit.New(d.Store)
	})
	return e, st, v
}

func jsonOf(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return m
}

// Settings → Sign-in is the owner's, every change needs a step-up, the
// client secret goes to the vault and never comes back out, and the
// changes are audited without it.
func TestSignInSettingsAPI(t *testing.T) {
	e, st, v := signInEnv(t)
	owner := signIn(t, e)
	_, ada := addUser(t, e, owner, "ada", auth.RoleAdmin)

	if status, _, _ := e.call(t, ada, "GET", "/api/settings/sign-in", nil); status != http.StatusForbidden {
		t.Fatalf("admin read: %d", status)
	}
	status, _, b := e.call(t, owner, "GET", "/api/settings/sign-in", nil)
	view := jsonOf(t, b)
	if status != 200 || view["client_secret_set"] != false || view["trusted_proxies"] != true ||
		!strings.HasSuffix(view["redirect_uri"].(string), "/api/auth/oidc/callback") || view["forward_auth"].(map[string]any)["user_header"] != "Remote-User" {
		t.Fatalf("GET: %d %s", status, b)
	}

	const secret = "FAKE-oidc-client-secret"
	body := map[string]any{
		"oidc":          map[string]any{"enabled": false, "name": "Pocket ID", "issuer": "https://id.lab.example", "client_id": "dash", "scopes": []string{"profile", "groups"}},
		"forward_auth":  map[string]any{"enabled": true},
		"mappings":      []map[string]string{{"group": "lab-ops", "role": "operator"}},
		"client_secret": secret,
	}
	if _, code, _ := e.call(t, owner, "PUT", "/api/settings/sign-in", body); code != "step_up_required" {
		t.Fatalf("PUT without a step-up: %s", code)
	}
	if ev, ok := lastAudit(t, st, "settings.sign_in.update"); !ok || ev.Result != audit.Refused {
		t.Fatalf("the refusal wasn't audited: %+v", ev)
	}
	stepUp(t, e, owner)
	if status, code, _ := e.call(t, owner, "PUT", "/api/settings/sign-in", map[string]any{"mappings": []map[string]string{{"group": "x", "role": "owner"}}}); status != 422 {
		t.Fatalf("owner mapping: %d %s", status, code)
	}
	status, _, b = e.call(t, owner, "PUT", "/api/settings/sign-in", body)
	if status != 200 || strings.Contains(string(b), secret) {
		t.Fatalf("PUT: %d %s", status, b)
	}
	view = jsonOf(t, b)
	if view["client_secret_set"] != true || view["oidc"].(map[string]any)["scopes"].([]any)[0] != "openid" {
		t.Fatalf("after PUT: %s", b)
	}
	if got, err := v.Get(t.Context(), auth.OIDCClientSecretName); err != nil || string(got) != secret {
		t.Fatalf("vault: %q %v", got, err)
	}
	ev, ok := lastAudit(t, st, "settings.sign_in.update")
	if !ok || ev.Result != audit.OK || strings.Contains(string(ev.Params), secret) || !strings.Contains(string(ev.Params), `"client_secret":"set"`) {
		t.Fatalf("audit: %+v %s", ev, ev.Params)
	}

	// Test: discovery against a provider, and whether it lists auth_time.
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token",
			"jwks_uri": base + "/jwks", "claims_supported": []string{"sub", "auth_time"}})
	}))
	defer idp.Close()
	body["oidc"].(map[string]any)["issuer"] = idp.URL
	body["oidc"].(map[string]any)["enabled"] = true
	delete(body, "client_secret")
	if status, _, b := e.call(t, owner, "PUT", "/api/settings/sign-in", body); status != 200 || jsonOf(t, b)["client_secret_set"] != true {
		t.Fatalf("PUT keeping the secret: %d %s", status, b)
	}
	status, _, b = e.call(t, owner, "POST", "/api/settings/sign-in/test", map[string]any{})
	test := jsonOf(t, b)
	if status != 200 || test["ok"] != true || test["auth_time"] != "listed" || !strings.HasPrefix(test["login_url"].(string), idp.URL+"/authorize?") {
		t.Fatalf("test: %d %s", status, b)
	}
	if status, _, _ := e.call(t, ada, "POST", "/api/settings/sign-in/test", map[string]any{}); status != http.StatusForbidden {
		t.Fatalf("admin test: %d", status)
	}

	// The sign-in page learns the provider's name.
	_, _, b = e.call(t, &http.Client{}, "GET", "/api/setup", nil)
	if si := jsonOf(t, b)["sign_in"].(map[string]any); si["oidc"].(map[string]any)["name"] != "Pocket ID" || si["password_form"] != true {
		t.Fatalf("setup: %s", b)
	}
}

// Forward-auth through the hub's whole chain: a user links the name their
// proxy sends (with a step-up), then that proxy signs them in; their role
// then comes from groups, and an admin's change holds until the proxy's
// next say. Unlinking an OIDC identity is audited, and an admin can read
// anyone's identities.
func TestLinkedIdentitiesAPI(t *testing.T) {
	e, st, _ := signInEnv(t)
	owner := signIn(t, e)
	stepUp(t, e, owner)
	if status, _, b := e.call(t, owner, "PUT", "/api/settings/sign-in", map[string]any{
		"forward_auth": map[string]any{"enabled": true},
		"mappings":     []map[string]string{{"group": "lab-ops", "role": "operator"}},
	}); status != 200 {
		t.Fatalf("settings: %d %s", status, b)
	}
	franID, fran := addUser(t, e, owner, "fran", auth.RoleViewer)
	_, ada := addUser(t, e, owner, "ada", auth.RoleAdmin)
	proxied := map[string]string{"Remote-User": "fran@proxy", "Remote-Groups": "lab-ops"}

	// Fran, signed in with a password, comes through the proxy: the name is
	// offered to link, and her session stays.
	_, _, b := e.raw(t, fran, "GET", "/api/auth/identities", nil, proxied)
	if got := jsonOf(t, b); got["forward_auth_username"] != "fran@proxy" || len(got["identities"].([]any)) != 0 {
		t.Fatalf("identities: %s", b)
	}
	if status, code, _ := e.raw(t, fran, "POST", "/api/auth/identities/forward-auth", nil, proxied); code != "step_up_required" {
		t.Fatalf("link without a step-up: %d %s", status, code)
	}
	stepUp(t, e, fran)
	if status, _, b := e.raw(t, fran, "POST", "/api/auth/identities/forward-auth", nil, proxied); status != http.StatusCreated {
		t.Fatalf("link: %d %s", status, b)
	}
	if ev, ok := lastAudit(t, st, "auth.forward.link"); !ok || ev.Result != audit.OK || !strings.Contains(string(ev.Params), "fran@proxy") {
		t.Fatalf("link audit: %+v", ev)
	}

	// A fresh browser through the proxy is signed in as fran, as an operator.
	jar, _ := cookiejar.New(nil)
	fresh := &http.Client{Jar: jar}
	page := map[string]string{"Sec-Fetch-Mode": "navigate"}
	maps.Copy(page, proxied)
	_, _, b = e.raw(t, fresh, "GET", "/api/auth/session", nil, page)
	sess := jsonOf(t, b)
	if sess["session"].(map[string]any)["method"] != "forward_auth" || sess["user"].(map[string]any)["role"] != "operator" {
		t.Fatalf("forward-auth session: %s", b)
	}
	if ev, ok := lastAudit(t, st, "auth.forward.signin"); !ok || ev.Actor.Name != "fran" || string(ev.Params) != `{"provisioned":false,"role":"operator"}` {
		t.Fatalf("sign-in audit: %+v", ev)
	}
	// The groups changed her role, and the log says so.
	if ev, ok := lastAudit(t, st, "users.role.from_groups"); !ok || ev.Target != "user:"+franID || string(ev.Params) != `{"from":"viewer","method":"forward_auth","to":"operator"}` {
		t.Fatalf("role-change audit: %+v", ev)
	}
	// Her role now comes from groups, but an admin can still change it;
	// it then stops counting as from groups...
	if status, _, b := e.call(t, owner, "PUT", "/api/users/"+franID, map[string]string{"role": "viewer"}); status != 200 || !strings.Contains(string(b), `"role_from_groups":false`) {
		t.Fatalf("role change: %d %s", status, b)
	}
	// ...until the proxy next has its say.
	if _, _, b := e.raw(t, fresh, "GET", "/api/auth/session", nil, proxied); jsonOf(t, b)["user"].(map[string]any)["role"] != "operator" {
		t.Fatalf("after the proxy's next request: %s", b)
	}
	if ev, ok := lastAudit(t, st, "users.role.from_groups"); !ok || string(ev.Params) != `{"from":"viewer","method":"forward_auth","to":"operator"}` || countAudit(t, st, "users.role.from_groups") != 2 {
		t.Fatalf("second role-change audit: %+v", ev)
	}
	_, _, b = e.call(t, owner, "GET", "/api/users", nil)
	if !strings.Contains(string(b), `"username":"fran","role":"operator","disabled":false,"has_passkey":false,"last_seen_at"`) || !strings.Contains(string(b), `"role_from_groups":true`) {
		t.Fatalf("users: %s", b)
	}

	// Unlinking an OIDC identity: a step-up, then audited.
	now := time.Now().UnixMilli()
	if _, err := st.Writer().Exec(`INSERT INTO oidc_identities (issuer, subject, user_id, email, created_at, last_used_at) VALUES ('https://id.lab.example', 'sub-fran', ?, 'fran@lab.example', ?, ?)`, franID, now, now); err != nil {
		t.Fatal(err)
	}
	if status, _, b := e.call(t, ada, "GET", "/api/users/"+franID+"/identities", nil); status != 200 || strings.Count(string(b), `"kind"`) != 2 {
		t.Fatalf("admin read: %d %s", status, b)
	}
	if status, _, _ := e.call(t, fran, "GET", "/api/users/"+franID+"/identities", nil); status != http.StatusForbidden {
		t.Fatalf("viewer read of the support route: %d", status)
	}
	path := "/api/auth/identities/" + url.PathEscape("https://id.lab.example") + "/sub-fran"
	if status, _, b := e.call(t, fran, "DELETE", path, nil); status != http.StatusNoContent {
		t.Fatalf("unlink: %d %s", status, b)
	}
	if ev, ok := lastAudit(t, st, "auth.oidc.unlink"); !ok || ev.Result != audit.OK {
		t.Fatalf("unlink audit: %+v", ev)
	}
	if status, _, _ := e.call(t, fran, "DELETE", path, nil); status != http.StatusNotFound {
		t.Fatalf("second unlink: %d", status)
	}

	// The proxy names someone else: fran's session ends.
	if status, _, _ := e.raw(t, fresh, "GET", "/api/auth/session", nil, map[string]string{"Remote-User": "nobody@proxy"}); status != http.StatusUnauthorized {
		t.Fatalf("changed header: %d", status)
	}
}

// countAudit is how many records of action the log holds.
func countAudit(t *testing.T, st *store.Store, action string) int {
	t.Helper()
	n := 0
	for _, ev := range auditEvents(t, st) {
		if ev.Action == action {
			n++
		}
	}
	return n
}
