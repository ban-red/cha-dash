package authz

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// publicDemoRoutes are the only unsafe routes the public demo serves: a
// visitor signing in and out, and the action engine, which there runs only
// the Demo Home's actions against the simulator. A row added here can change
// something on a public host, so update the list only after review.
var publicDemoRoutes = []string{
	"POST /api/auth/logout",
	"POST /api/auth/demo",
	"POST /api/actions/plan",
	"POST /api/plans/{id}/approve",
	"POST /api/tasks/{id}/cancel",
}

func TestPublicDemoRoutes(t *testing.T) {
	var marked []string
	for _, r := range Routes {
		if r.PublicDemo {
			marked = append(marked, r.Pattern)
		}
	}
	if !slices.Equal(marked, publicDemoRoutes) {
		t.Fatalf("PublicDemo rows = %q, reviewed list = %q: a PublicDemo row lets a visitor change something on a public host, so update the list only after review", marked, publicDemoRoutes)
	}
}

// The matrix: every row of the table, with its own method or each unsafe
// one, refused or not by the public demo.
func TestDemoRefusesAcrossTheRouteTable(t *testing.T) {
	unsafe := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	for _, r := range Routes {
		var methods []string
		if m, _, ok := strings.Cut(r.Pattern, " "); ok {
			methods = []string{m}
		} else {
			methods = append([]string{http.MethodGet}, unsafe...)
		}
		for _, m := range methods {
			got := DemoRefuses(r, m)
			safe := m == http.MethodGet || m == http.MethodHead
			var want bool
			switch {
			case r.Access == NodeKey || r.Access == Signature:
				want = true // no nodes or forges in the demo
			case r.Access == Admin || r.Access == Owner:
				want = true // settings, users, integrations, imports, specs, stacks, notifications, nodes, export, audit
			case safe:
				want = false // reads below admin stay open
			default:
				want = !slices.Contains(publicDemoRoutes, r.Pattern)
			}
			if got != want {
				t.Errorf("%s %s (%s): DemoRefuses = %v, want %v", m, r.Pattern, r.Access, got, want)
			}
		}
	}
}

// What the public demo must never let a visitor change, by name: a real
// integration, a spec, a node, a token, a user, a setting, a stack or a
// notification channel. Each is a row of the table, refused.
func TestPublicDemoRefusesWhatReachesOutside(t *testing.T) {
	for _, p := range []string{
		"POST /api/integrations/instances",
		"PUT /api/integrations/instances/{id}",
		"DELETE /api/integrations/instances/{id}",
		"POST /api/integrations/test",
		"POST /api/integrations/instances/{id}/refresh",
		"POST /api/integrations/authorize/{integration}",
		"POST /api/import/homepage",
		"POST /api/import/homarr",
		"POST /api/import/yaml",
		"POST /api/import/discovery",
		"POST /api/import/chadash",
		"POST /api/onboarding/discover",
		"POST /api/discover",
		"POST /api/specs/installed",
		"PUT /api/specs/installed/{id}/actions",
		"POST /api/nodes/tokens",
		"POST /api/nodes/{id}/approve",
		"POST /api/nodes/pairing/regenerate",
		"GET /node",
		"POST /api/tokens",
		"POST /api/users",
		"PUT /api/users/{id}",
		"PUT /api/settings/security",
		"PUT /api/settings/sign-in",
		"PUT /api/settings/agents",
		"PUT /api/settings/history",
		"PUT /api/stacks/{node}/{stack}",
		"PUT /api/stacks/{node}/{stack}/sync",
		"POST /api/stacks/hooks",
		"POST /api/hooks/{id}",
		"POST /api/notify/channels",
		"PUT /api/notify/channels/{id}",
		"POST /api/notify/channels/{id}/test",
		"PUT /api/boards/{id}/layout",
		"POST /api/updates/{resource}/hold",
		"POST /api/setup",
		"POST /api/auth/login",
		"POST /api/auth/passkeys/register/begin",
		"POST /api/auth/step-up",
		"POST /api/notify/push/subscriptions",
		"GET /api/export",
		"GET /api/audit",
	} {
		r, ok := Lookup(p)
		if !ok {
			t.Errorf("%s isn't in the table", p)
			continue
		}
		method, _, _ := strings.Cut(p, " ")
		if !DemoRefuses(r, method) {
			t.Errorf("the public demo serves %s", p)
		}
	}
}
