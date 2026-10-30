package nodes

import (
	"os"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestComposeNodeMatchesCompose: deploy/compose.node.yaml runs chadash-node
// as deploy/compose.yaml does. They drifted once: without compose.yaml's
// group_add the node couldn't read its own /etc/chadash-node and
// crash-looped on another host.
func TestComposeNodeMatchesCompose(t *testing.T) {
	node := func(path string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Services map[string]map[string]any `yaml:"services"`
		}
		if err := yaml.Unmarshal(b, &f); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		s, ok := f.Services["chadash-node"]
		if !ok {
			t.Fatalf("%s has no chadash-node service", path)
		}
		return s
	}
	hub, other := node("../../deploy/compose.yaml"), node("../../deploy/compose.node.yaml")
	for _, k := range []string{"user", "group_add", "read_only", "cap_drop", "security_opt"} {
		if hub[k] == nil {
			t.Errorf("compose.yaml's node has no %s", k)
		}
		if !reflect.DeepEqual(hub[k], other[k]) {
			t.Errorf("%s: compose.yaml has %v, compose.node.yaml %v", k, hub[k], other[k])
		}
	}
}
