package runtime

import (
	"strings"
	"testing"

	"dash.cha.sh/internal/integration"
)

func TestInstanceState(t *testing.T) {
	e := newEnv(t)
	up := newUpstream(t, secret, false)
	off := false
	id, err := e.m.Create(t.Context(), CreateRequest{Integration: "echo", Name: "Echo", Enabled: &off,
		Config: map[string]any{"url": up.URL}, Secrets: map[string]any{"api_key": secret}})
	if err != nil {
		t.Fatal(err)
	}
	mi := e.m.get(id)
	cfg := integration.Config{InstanceID: id, Name: "Echo"}
	deps := func(mi *managed) integration.Deps {
		d, _, release := e.m.buildDeps(mi, 0, cfg, nil, discard{self: SelfID(id)})
		t.Cleanup(release)
		return d
	}

	// A saved instance's state is in the database: a restart sees it.
	ctx := t.Context()
	if v, err := deps(mi).State.Load(ctx, "totals"); v != nil || err != nil {
		t.Fatalf("empty state = %q, %v", v, err)
	}
	if err := deps(mi).State.Save(ctx, "totals", []byte(`{"rx":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := deps(mi).State.Save(ctx, "totals", []byte(`{"rx":2}`)); err != nil {
		t.Fatal(err)
	}
	if v, err := deps(mi).State.Load(ctx, "totals"); string(v) != `{"rx":2}` || err != nil {
		t.Fatalf("after a restart = %q, %v", v, err)
	}
	for name, v := range map[string][]byte{"not JSON": []byte("rx=1"), "too big": []byte(`"` + strings.Repeat("x", integration.MaxStateBytes) + `"`)} {
		if err := deps(mi).State.Save(ctx, "totals", v); err == nil {
			t.Errorf("%s: saved", name)
		}
	}

	// An unsaved config under test gets state that's gone with its deps.
	if err := deps(nil).State.Save(ctx, "totals", []byte(`{"rx":3}`)); err != nil {
		t.Fatal(err)
	}
	if v, _ := deps(nil).State.Load(ctx, "totals"); v != nil {
		t.Fatalf("an unsaved config's state persisted: %q", v)
	}
	if v, _ := deps(mi).State.Load(ctx, "totals"); string(v) != `{"rx":2}` {
		t.Fatalf("an unsaved config's state reached the instance's: %q", v)
	}

	// The state goes with the instance.
	if err := e.m.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM integration_state WHERE instance_id = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("state rows after delete = %d, %v", n, err)
	}
}
