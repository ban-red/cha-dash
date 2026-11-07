package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
)

// FuzzSpec: an installed spec is anyone's YAML, and its templates are
// filled with the owner's secrets and a caller's parameters. Parse never
// panics. A spec that compiles never puts a secret in a request's path,
// whatever the secret holds. An action's path value is escaped as one
// segment: it adds no "/" and no ".." segment.
//
//	go test -run '^$' -fuzz '^FuzzSpec$' -fuzztime 60s ./internal/spec/
func FuzzSpec(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "specs", "*.yaml"))
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b, "c0ntainer-1")
		}
	}
	f.Add([]byte("apiVersion: chadash.dev/v1beta1\nkind: Integration\n"), "../..")
	f.Add([]byte("{{ secret.x }}"), "a/b")

	const marker = "SECRET-MARKER"
	f.Fuzz(func(t *testing.T, b []byte, value string) {
		const tpl = "/api/{{ target.id }}/start"
		if out, err := fillPath(tpl, map[string]string{"target.id": value}); err == nil {
			if strings.Count(out, "/") != 3 {
				t.Fatalf("value %q added a segment: %q", value, out)
			}
			for _, seg := range strings.Split(out, "/") {
				if seg == ".." || seg == "." {
					t.Fatalf("value %q made a %q segment: %q", value, seg, out)
				}
			}
		}

		// A spec compiles quickly, or a crafted one could hold the hub.
		watchdog := time.AfterFunc(2*time.Second, func() { panic(fmt.Sprintf("Parse took over 2s on %d bytes", len(b))) })
		s, err := Parse(b)
		watchdog.Stop()
		if err != nil {
			return
		}
		values := map[string]any{}
		for name := range s.secrets {
			values[name] = marker
		}
		in := s.newInstance(integration.Config{InstanceID: "i_fuzz", Name: "fuzz", Values: values})
		for _, r := range s.f.Requests {
			if strings.Contains(in.expand(r.Path), marker) {
				t.Fatalf("a secret reached %s's path %q", r.Name, r.Path)
			}
		}
	})
}
