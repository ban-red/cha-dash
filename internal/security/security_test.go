package security

import (
	"errors"
	"path/filepath"
	"testing"

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
