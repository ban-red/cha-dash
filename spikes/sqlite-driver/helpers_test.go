package sqlitedriver

import (
	"bytes"
	"path/filepath"
	"testing"
)

// testKEK stands in for the hub's secret.key. A test value, not a real key.
var testKEK = bytes.Repeat([]byte{0x42}, 32)

// forEach runs f as a subtest for every linked variant.
func forEach(t *testing.T, f func(t *testing.T, v Variant)) {
	for _, v := range Variants() {
		t.Run(v.Name, func(t *testing.T) { f(t, v) })
	}
}

// openMigrated opens a fresh, migrated database for v.
func openMigrated(tb testing.TB, v Variant) (*Store, string) {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "chadash.db")
	st, err := Open(tb.Context(), v, path, testKEK)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(tb.Context()); err != nil {
		tb.Fatal(err)
	}
	return st, path
}

// seeded opens a migrated database with the standard fixture: 10 users,
// 1,000 sessions, one board.
func seeded(tb testing.TB, v Variant) (*Store, *Fixture, string) {
	tb.Helper()
	st, path := openMigrated(tb, v)
	fx, err := Seed(tb.Context(), st, 10, 1000)
	if err != nil {
		tb.Fatal(err)
	}
	return st, fx, path
}
