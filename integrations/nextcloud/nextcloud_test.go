package nextcloud

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	token = "fake-serverinfo-token-0123456789" // synthetic
	admin = "admin"
	appPW = "fake-app-password-abcde" // synthetic
)

func upstream(t *testing.T) *fixture.Server {
	auth := func(r *http.Request) bool {
		if r.Header.Get("OCS-APIRequest") != "true" {
			return false
		}
		if r.Header.Get("NC-Token") == token {
			return true
		}
		u, p, ok := r.BasicAuth()
		return ok && u == admin && p == appPW
	}
	return fixture.New(t, fixture.Dir(t, ID), auth, map[string]fixture.Route{
		"GET /cloud" + infoPath: {File: "serverinfo.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url string, cfg map[string]any, secrets map[string]string) integration.Instance {
	cfg["url"] = url
	cfg["tls"] = map[string]any{"mode": "verify"}
	return h.Connect(Integration{}, cfg, secrets)
}

func TestValidate(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"url": "https://nextcloud.home.arpa", "tls": map[string]any{"mode": "verify"}}
	}
	// The Homepage importer's two shapes: key → token, or username and password.
	if errs := integrationtest.Validate(Integration{}, base(), map[string]string{"token": token}); len(errs) != 0 {
		t.Fatalf("token: %v", errs)
	}
	withUser := base()
	withUser["username"] = admin
	if errs := integrationtest.Validate(Integration{}, withUser, map[string]string{"password": appPW}); len(errs) != 0 {
		t.Fatalf("app password: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, base(), nil); len(errs) != 1 || errs[0].Path != "/token" {
		t.Fatalf("nothing: %v", errs)
	}
	if errs := integrationtest.Validate(Integration{}, withUser, nil); len(errs) != 1 || errs[0].Path != "/password" {
		t.Fatalf("username alone: %v", errs)
	}
	if _, m, ok := integration.Default.Get(ID); !ok || !m.IsSecret("token") || !m.IsSecret("password") || m.IsSecret("username") {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	up := upstream(t)
	h := integrationtest.New(t)
	inst := connect(t, h, up.URL+"/cloud/", map[string]any{}, map[string]string{"token": token})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	rid := "nextcloud:i_test/app"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "app" || r.Name != "Test" || r.State != "running" || r.Health != integration.HealthOK {
		t.Fatalf("app = %+v", r)
	}
	a := r.Attrs
	if a["type"] != "nextcloud" || a["version"] != "30.0.6.2" || a["php_version"] != "8.3.17" || a["database"] != "pgsql" ||
		a["users"] != 6.0 || a["url"] != up.URL+"/cloud" {
		t.Fatalf("attrs = %v", a)
	}
	if caps, _ := a["caps"].([]any); len(caps) != 1 || caps[0] != "files.storage" {
		t.Fatalf("caps = %v", a["caps"])
	}
	want := map[string]float64{"users": 6, "users.active": 1, "users.24h": 4, "files": 248113, "shares": 41, "free.gib": 912}
	for metric, v := range want {
		se := h.Series(rid, metric)
		unit := "count"
		if metric == "free.gib" {
			unit = "GiB"
		}
		if se == nil || se["value"] != v || se["unit"] != unit {
			t.Errorf("%s = %v, want %v", metric, se, v)
		}
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its app")
	}
	// The slow app-store and update checks are skipped; the token rides in
	// its header, never in the URL.
	for _, req := range up.Requests(infoPath) {
		q := req.URL.Query()
		if q.Get("format") != "json" || q.Get("skipApps") != "true" || q.Get("skipUpdate") != "true" || strings.Contains(req.URL.String(), token) {
			t.Fatalf("query = %s", req.URL.RawQuery)
		}
		if _, _, basic := req.BasicAuth(); basic {
			t.Fatal("a token instance sent basic auth")
		}
	}

	// Low on space: attention. False (disk_free_space disabled): unknown.
	up.Set("GET /cloud"+infoPath, fixture.Route{Body: `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":{"nextcloud":{"system":{"version":"30.0.6.2","freespace":3221225472},"storage":{"num_users":"6","num_files":10},"shares":{"num_shares":0}},"activeUsers":{"last5minutes":0,"last24hours":0}}}}`})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Resource(rid)
	if r.Health != integration.HealthAttention || r.Attrs["health.reason"] != "Nextcloud has 3.0 GiB free" {
		t.Fatalf("low space = %s %v", r.Health, r.Attrs["health.reason"])
	}
	if se := h.Series(rid, "users"); se["value"] != 6.0 {
		t.Fatalf("users as a string = %v", se)
	}
	up.Set("GET /cloud"+infoPath, fixture.Route{Body: `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":{"nextcloud":{"system":{"version":"30.0.6.2","freespace":false}}}}}`})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Resource(rid)
	if se := h.Series(rid, "free.gib"); r.Health != integration.HealthOK || se["value"] != nil {
		t.Fatalf("no freespace = %s %v", r.Health, se)
	}
}

func TestTest(t *testing.T) {
	up := upstream(t)
	res := connect(t, integrationtest.New(t), up.URL+"/cloud", map[string]any{}, map[string]string{"token": token}).Test(t.Context())
	if !res.OK || res.Version != "30.0.6.2" || len(res.Grants) != 1 || res.Grants[0].Name != "Nextcloud serverinfo token" || res.Grants[0].Access != integration.AccessRead {
		t.Fatalf("token: %+v", res)
	}
	res = connect(t, integrationtest.New(t), up.URL+"/cloud", map[string]any{"username": admin}, map[string]string{"password": appPW}).Test(t.Context())
	if !res.OK || len(res.Grants) != 1 || res.Grants[0].Name != "Nextcloud admin account" || res.Grants[0].Access != integration.AccessAdmin {
		t.Fatalf("app password: %+v", res)
	}
}

func TestFailures(t *testing.T) {
	up := upstream(t)
	test := func(url string, cfg map[string]any, secrets map[string]string) integration.TestResult {
		return connect(t, integrationtest.New(t), url, cfg, secrets).Test(t.Context())
	}
	res := test(up.URL+"/cloud", map[string]any{}, map[string]string{"token": "wrong-token"})
	if res.OK || res.ErrorCode != integration.CodeAuth || res.ErrorMessage != "Nextcloud rejected the serverinfo token" {
		t.Fatalf("wrong token: %+v", res)
	}
	res = test(up.URL+"/cloud", map[string]any{"username": admin}, map[string]string{"password": "wrong-password"})
	if res.OK || res.ErrorCode != integration.CodeAuth || strings.Contains(res.ErrorMessage, "wrong-password") {
		t.Fatalf("wrong app password: %+v", res)
	}
	// A user who isn't an admin.
	up.Set("GET /cloud"+infoPath, fixture.Route{Status: http.StatusForbidden, Body: `{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"Logged in user must be an admin"},"data":[]}}`})
	res = test(up.URL+"/cloud", map[string]any{"username": admin}, map[string]string{"password": appPW})
	if res.OK || res.ErrorCode != integration.CodeAuth || !strings.Contains(res.ErrorMessage, "needs an administrator") {
		t.Fatalf("not an admin: %+v", res)
	}
	// An old-style 200 with the failure inside.
	up.Set("GET /cloud"+infoPath, fixture.Route{Body: `{"ocs":{"meta":{"status":"failure","statuscode":997,"message":"Current user is not logged in"},"data":[]}}`})
	res = test(up.URL+"/cloud", map[string]any{}, map[string]string{"token": token})
	if res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("997: %+v", res)
	}
	// The Monitoring app disabled, or the wrong URL.
	res = test(up.URL, map[string]any{}, map[string]string{"token": token})
	if res.OK || res.ErrorCode != integration.CodeAPIChanged || !strings.Contains(res.ErrorMessage, "Monitoring app") {
		t.Fatalf("404: %+v", res)
	}
	// A login page.
	up.Set("GET /cloud"+infoPath, fixture.Route{Body: "<html>login</html>", ContentType: "text/html"})
	res = test(up.URL+"/cloud", map[string]any{}, map[string]string{"token": token})
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("html: %+v", res)
	}
}
