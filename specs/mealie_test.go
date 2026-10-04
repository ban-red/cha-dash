package specs

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const mealieToken = "fake-mealie-token"

func mealie(t *testing.T) *app {
	return &app{t: t, dir: "testdata/mealie",
		auth: func(r *http.Request) bool {
			return r.URL.Path == "/api/app/about" || r.Header.Get("Authorization") == "Bearer "+mealieToken
		},
		routes: map[string]string{
			"/api/app/about":             "app_about.json",
			"/api/households/statistics": "households_statistics.json",
		}}
}

func TestMealie(t *testing.T) {
	integ := bundled(t, "mealie", "Mealie", "token")
	importerShape(t, integ, "https://food.family.example", "token")
	up := mealie(t)
	srv := up.start()
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"token": mealieToken})

	if res := inst.Test(t.Context()); !res.OK || res.Version != "3.3.0" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	attrs := checkApp(t, h, "mealie", srv.URL)
	// The Demo Home's attrs.
	if attrs["version"] != "3.3.0" || attrs["recipes"] != 87.0 || attrs["categories"] != 8.0 || attrs["tags"] != 240.0 || attrs["tools"] != 12.0 {
		t.Fatalf("attrs = %v", attrs)
	}
	checkSeries(t, h, "mealie", map[string]any{"recipes": 87.0}, nil)
}

func TestMealieFailures(t *testing.T) {
	integ := bundled(t, "mealie", "Mealie", "token")
	up := mealie(t)
	srv := up.start()

	// The about page is public: the token is checked by the statistics.
	res := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"token": "wrong-token"}).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAuth || res.Version != "3.3.0" || strings.Contains(res.ErrorMessage, "wrong-token") {
		t.Fatalf("wrong token: %+v", res)
	}

	// Mealie 1.x has no household statistics: its 404 is an unsupported
	// version, not a changed API.
	up.set("/api/app/about", `{"production":true,"version":"v1.12.0","demoStatus":false}`)
	up.set("/api/households/statistics", "")
	up.set("/api/groups/statistics", `{"totalRecipes":87}`)
	inst := integrationtest.New(t).Connect(integ, map[string]any{"url": srv.URL}, map[string]string{"token": mealieToken})
	if code, msg := classify(t, inst); code != integration.CodeUnsupportedVersion ||
		msg != "Mealie v1.12.0 is older than this spec supports (2.0 or later, which counts recipes per household)." {
		t.Fatalf("1.x: %s %q", code, msg)
	}
	// A current version without it: the API changed.
	up.set("/api/app/about", "app_about.json")
	if code, msg := classify(t, inst); code != integration.CodeAPIChanged || !strings.Contains(msg, "(detected version 3.3.0)") {
		t.Fatalf("404: %s %q", code, msg)
	}
}
