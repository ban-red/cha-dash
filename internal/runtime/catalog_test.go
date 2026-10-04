package runtime

import (
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
)

func TestCatalogRecipe(t *testing.T) {
	e := newEnv(t)
	code, body := e.call(t, "GET", "/api/integrations/catalog", nil)
	if code != 200 || strings.Contains(body, `"recipe"`) {
		t.Fatalf("an integration without a recipe has no recipe field: %d %s", code, body)
	}
	rc, err := integration.ParseRecipe([]byte("---\ntitle: An API key\nsummary: Full access; there's no other kind.\naccess: full\n" +
		"params:\n  - {name: url, label: Address, from: config.url, default: 'https://echo.lab.example', pattern: '^https?://[a-z.]+$'}\n" +
		"discover:\n  - {scheme: http, port: 8080, path: /, match: Echo}\n" +
		"---\n## Copy the key\n\nFrom {{url}}/settings.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.reg.AddRecipe("echo", rc); err != nil {
		t.Fatal(err)
	}
	_, body = e.call(t, "GET", "/api/integrations/catalog", nil)
	cat := decodeAs[[]map[string]any](t, body)
	r, _ := cat[0]["recipe"].(map[string]any)
	if cat[0]["id"] != "echo" || cat[0]["config_schema"] == nil || r["access"] != "full" || r["title"] != "An API key" {
		t.Fatalf("catalog: %s", body)
	}
	for _, s := range []string{`"steps":[{"title":"Copy the key","blocks":[{"kind":"text","text":"From {{url}}/settings."}]}]`, `"from":"config.url"`, `"grants":[]`} {
		if !strings.Contains(body, s) {
			t.Errorf("catalog lacks %s: %s", s, body)
		}
	}
	if strings.Contains(body, "discover") || strings.Contains(body, `"engine"`) || strings.Contains(body, `"Poll"`) {
		t.Errorf("hub-only fields leaked: %s", body)
	}
}
