package security

import (
	"errors"
	"path/filepath"
	"testing"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/store"
)

func TestSeverity(t *testing.T) {
	const sec, none = integration.CategorySecurity, ""
	for _, c := range []struct {
		p                  Posture
		category, declared string
		want               string
	}{
		{Relaxed, sec, "attention", "info"},
		{Relaxed, sec, "info", "info"},
		{Relaxed, sec, "down", "info"},
		{Standard, sec, "attention", "attention"},
		{Standard, sec, "info", "info"},
		{Strict, sec, "info", "attention"},
		{Strict, sec, "attention", "attention"},
		// Anything else keeps its severity, under every posture.
		{Relaxed, none, "attention", "attention"},
		{Strict, none, "info", "info"},
		{Relaxed, none, "down", "down"},
		// An unknown posture changes nothing.
		{"", sec, "attention", "attention"},
	} {
		if got := c.p.Severity(c.category, c.declared); got != c.want {
			t.Errorf("%q.Severity(%q, %q) = %q, want %q", c.p, c.category, c.declared, got, c.want)
		}
	}
}

func TestLevels(t *testing.T) {
	if len(Levels) != 3 || Levels[0].ID != Relaxed || !Levels[0].Default || Levels[1].ID != Standard || Levels[2].ID != Strict {
		t.Fatalf("levels = %+v", Levels)
	}
	for _, l := range Levels {
		if l.Label == "" || l.Description == "" || l.Default != (l.ID == Default) {
			t.Errorf("level %+v", l)
		}
		if p, ok := Parse(string(l.ID)); !ok || p != l.ID {
			t.Errorf("Parse(%q)", l.ID)
		}
	}
	for _, s := range []string{"", "Relaxed", "paranoid"} {
		if _, ok := Parse(s); ok {
			t.Errorf("Parse(%q) accepted", s)
		}
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestService(t *testing.T) {
	st := openStore(t)
	s, err := New(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Posture() != Relaxed {
		t.Fatalf("default = %q", s.Posture())
	}
	var seen []Posture
	s.OnChange(func(p Posture) { seen = append(seen, p) })

	if err := s.Set(t.Context(), "paranoid"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown level: %v", err)
	}
	if err := s.Set(t.Context(), Strict); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(t.Context(), Strict); err != nil { // unchanged: nobody is told
		t.Fatal(err)
	}
	if err := s.Set(t.Context(), Standard); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != Strict || seen[1] != Standard || s.Posture() != Standard {
		t.Fatalf("seen %v, posture %q", seen, s.Posture())
	}

	var raw string
	if err := st.Reader().QueryRowContext(t.Context(), `SELECT value FROM settings WHERE key = ?`, SettingsKey).Scan(&raw); err != nil || raw != `"standard"` {
		t.Fatalf("row = %q %v", raw, err)
	}
	again, err := New(t.Context(), st, nil)
	if err != nil || again.Posture() != Standard {
		t.Fatalf("reloaded: %v", err)
	}

	// An unreadable row falls back to the default rather than failing boot.
	for _, bad := range []string{`"paranoid"`, `{"posture":"strict"}`, `nope`} {
		if _, err := st.Writer().ExecContext(t.Context(), `UPDATE settings SET value = ? WHERE key = ?`, bad, SettingsKey); err != nil {
			t.Fatal(err)
		}
		s, err := New(t.Context(), st, nil)
		if err != nil || s.Posture() != Default {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

// TestReload: settings written behind the service's back (a Home file
// import) take effect, and a changed posture reaches the subscribers.
func TestReload(t *testing.T) {
	st := openStore(t)
	s, err := New(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []Posture
	s.OnChange(func(p Posture) { got = append(got, p) })
	if _, err := st.Writer().Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('security.posture', '"strict"', 1), ('security.step_up', '"passkey_only"', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s.Posture() != Strict || s.StepUp() != auth.StepUpPasskeyOnly || len(got) != 1 || got[0] != Strict {
		t.Fatalf("after reload: %q %q, subscribers saw %v", s.Posture(), s.StepUp(), got)
	}
	// An unknown value keeps what's in force.
	if _, err := st.Writer().Exec(`UPDATE settings SET value = '"wild"'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(t.Context()); err != nil || s.Posture() != Strict || s.StepUp() != auth.StepUpPasskeyOnly || len(got) != 1 {
		t.Fatalf("unknown values: %v %q %q", err, s.Posture(), s.StepUp())
	}
}

func TestViewNotes(t *testing.T) {
	s, err := New(t.Context(), openStore(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := s.view(); v.Notes != nil {
		t.Fatalf("notes without a source = %v", v.Notes)
	}
	s.ProxyNotes = func() []httpx.ProxyNote { return []httpx.ProxyNote{{Code: httpx.NoteUntrustedProxy, Message: "m"}} }
	if v := s.view(); len(v.Notes) != 1 || v.Notes[0].Code != httpx.NoteUntrustedProxy {
		t.Fatalf("notes = %v", v.Notes)
	}
}

// The sign-in settings survive a restart and a Reload, an invalid row reads
// as the defaults (OIDC and forward-auth off), and unknown fields are refused.
func TestSignInSettings(t *testing.T) {
	st := openStore(t)
	s, err := New(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if set := s.SignIn(); set.OIDC.Enabled || set.ForwardAuth.UserHeader != auth.DefaultUserHeader {
		t.Fatalf("defaults = %+v", set)
	}
	want := auth.DefaultSignIn()
	want.ForwardAuth.Enabled = true
	want.Mappings = []auth.GroupMapping{{Group: "lab-ops", Role: auth.RoleOperator}}
	if err := s.SetSignIn(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	again, err := New(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if set := again.SignIn(); !set.ForwardAuth.Enabled || len(set.Mappings) != 1 {
		t.Fatalf("after a restart = %+v", set)
	}
	if _, err := st.Writer().Exec(`UPDATE settings SET value = '{"mappings":[{"group":"x","role":"owner"}]}' WHERE key = ?`, SignInKey); err != nil {
		t.Fatal(err)
	}
	if err := again.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if set := again.SignIn(); set.ForwardAuth.Enabled || len(set.Mappings) != 0 {
		t.Fatalf("an invalid row = %+v", set)
	}
	if _, err := ParseSignIn([]byte(`{"client_secret":"FAKE"}`)); err == nil {
		t.Fatal("a client secret was accepted in the settings")
	}
	if s.hasClientSecret(t.Context()) {
		t.Fatal("no vault, but a secret")
	}
}
