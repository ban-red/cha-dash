package spec

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"dash.cha.sh/internal/integration/integrationtest"

	"github.com/expr-lang/expr"
)

// int() is exact past 32 bits on every build: byte counts and Unix milliseconds
// overflow a 32-bit int, which is Go's int on the armv7 image (CI's
// "Go 32-bit" job runs this with GOARCH=386).
func TestIntIsExactPast32Bits(t *testing.T) {
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

// The arithmetic after int() isn't in Go's int, which is what overflows on
// a 32-bit build: it's float64, exact to 2^53, so this shows on any build.
func TestIntArithmeticIsFloat(t *testing.T) {
	for src, want := range map[string]any{
		"int(2147483647) + 1":              float64(2147483648),
		"int(1794020400000) + int(500)":    float64(1794020400500),
		"string(int(48318382080.7))":       "48318382080",
		"string(1.5)":                      "1.5",
		"string(int(-3.9))":                "-3",
		"int(\"12\") * 2":                  float64(24),
		"string(2.0 * 4503599627370496.0)": "9.007199254740992e+15",
	} {
		p, err := expr.Compile(src, intBuiltin, stringBuiltin)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		got, err := expr.Run(p, nil)
		if err != nil || got != want {
			t.Errorf("%s = %#v (%v), want %#v", src, got, err, want)
		}
	}
}
