package node

import (
	"slices"
	"strings"
	"testing"
)

// TestStacksDenyPolicy: stacks.deny and bind_roots parse, and nonsense in
// them stops the node (review finding 2).
func TestStacksDenyPolicy(t *testing.T) {
	p, err := ParsePolicy([]byte("stacks:\n  deny: [privileged, host_binds]\n  bind_roots: [/mnt/media]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Stacks.Denies("privileged") || p.Stacks.Denies("cap_add") || len(p.Stacks.BindRoots) != 1 {
		t.Fatalf("stacks %+v", p.Stacks)
	}
	if !slices.Equal(p.Stacks.Allow, DefaultPolicy().Stacks.Allow) {
		t.Fatalf("deny changed the allow list: %v", p.Stacks.Allow)
	}
	if d := DefaultPolicy(); len(d.Stacks.Deny) != 0 {
		t.Fatalf("the default denies %v", d.Stacks.Deny)
	}
	for doc, want := range map[string]string{
		"stacks: {deny: [everything]}":                      "unknown feature",
		"stacks: {deny: [host_binds], bind_roots: [media]}": "absolute path",
		"stacks: {deny: [host_binds], bind_roots: [/]}":     "absolute path",
		"stacks: {bind_roots: [/mnt/media]}":                "only means something",
		"stacks: {deny: [privileged], denny: [cap_add]}":    "denny",
	} {
		if _, err := ParsePolicy([]byte(doc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", doc, err, want)
		}
	}
}
