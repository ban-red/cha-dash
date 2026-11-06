package spec

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"dash.cha.sh/internal/integration/integrationtest"
)

// int() is 64 bits on every build: byte counts and Unix milliseconds
// overflow a 32-bit int, which is Go's int on the armv7 image (CI's
// "Go 32-bit" job runs this with GOARCH=386).
func TestIntIs64Bits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"size": 48318382080.0, "at": "1794020400000", "frac": 2.9}`))
	}))
	t.Cleanup(srv.Close)
	for _, version := range []string{V1Alpha1, V1Beta1} {
		s := mustParse(t, `apiVersion: `+version+`
kind: Integration
meta: { id: widgets, name: Widgets, icon: widgets, version: 0.1.0, tier: bronze, capabilities: [primitives.stat] }
requests:
  info: { path: /api/info, extract: { size: size, at: at, frac: frac } }
resources:
  - id: app
    kind: app
    attrs:
      size: int(info.size)
      at: int(info.at) + 1000
      frac: int(info.frac)
      text: string(int(info.size))
`)
		h := integrationtest.New(t)
		if err := h.Connect(s, map[string]any{"url": srv.URL}, nil).Sync(t.Context(), integrationtest.Watched); err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		r, _ := h.Resource("widgets:i_test/app")
		if r.Attrs["size"] != 48318382080.0 || r.Attrs["at"] != 1794020401000.0 || r.Attrs["frac"] != 2.0 || r.Attrs["text"] != "48318382080" {
			t.Errorf("%s: %v", version, r.Attrs)
		}
	}
}
