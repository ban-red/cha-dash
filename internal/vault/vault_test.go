package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/store"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func newStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(t.Context(), filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return st, dir
}

func newVault(t *testing.T) (*Vault, *store.Store, string) {
	t.Helper()
	st, dir := newStore(t)
	keyPath := filepath.Join(dir, "secret.key")
	v, err := Open(t.Context(), st, KeyConfig{Path: keyPath, Generate: true}, discard)
	if err != nil {
		t.Fatal(err)
	}
	return v, st, keyPath
}

func TestRoundTrip(t *testing.T) {
	v, st, _ := newVault(t)
	ctx := t.Context()

	if err := v.Put(ctx, "plex/token", []byte("fake-plex-token")); err != nil {
		t.Fatal(err)
	}
	if err := v.Put(ctx, "traefik.password", []byte("hunter2-not-real")); err != nil {
		t.Fatal(err)
	}
	got, err := v.Get(ctx, "plex/token")
	if err != nil || string(got) != "fake-plex-token" {
		t.Fatalf("Get = %q, %v", got, err)
	}

	// Overwrite keeps the name, replaces the value.
	if err := v.Put(ctx, "plex/token", []byte("rotated")); err != nil {
		t.Fatal(err)
	}
	if got, _ := v.Get(ctx, "plex/token"); string(got) != "rotated" {
		t.Fatalf("after overwrite Get = %q", got)
	}

	names, err := v.List(ctx)
	if err != nil || !slices.Equal(names, []string{"plex/token", "traefik.password"}) {
		t.Fatalf("List = %v, %v", names, err)
	}

	// The plaintext is nowhere in the database.
	var blob []byte
	if err := st.Reader().QueryRowContext(ctx, `SELECT data_key || ciphertext FROM vault_secrets WHERE name = 'traefik.password'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("hunter2")) {
		t.Fatal("plaintext found in stored row")
	}

	if err := v.Delete(ctx, "plex/token"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, "plex/token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
	if err := v.Delete(ctx, "plex/token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestEmptyValue(t *testing.T) {
	v, _, _ := newVault(t)
	if err := v.Put(t.Context(), "empty", nil); err != nil {
		t.Fatal(err)
	}
	got, err := v.Get(t.Context(), "empty")
	if err != nil || len(got) != 0 {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestValidation(t *testing.T) {
	v, _, _ := newVault(t)
	ctx := t.Context()
	for _, name := range []string{"", "-leading", "has space", "nul\x00byte", strings.Repeat("a", 129), "${secret:x}"} {
		if err := v.Put(ctx, name, []byte("x")); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Put(%q) = %v, want ErrInvalidName", name, err)
		}
	}
	if err := v.Put(ctx, "big", make([]byte, MaxValueSize+1)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized Put = %v", err)
	}
}

func TestPutTxFollowsTheTransaction(t *testing.T) {
	v, st, _ := newVault(t)
	ctx := t.Context()

	// Rolled back with the transaction: nothing stored.
	boom := errors.New("boom")
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := v.PutTx(ctx, tx, "rolled/back", []byte("fake")); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WriteTx = %v", err)
	}
	if _, err := v.Get(ctx, "rolled/back"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after rollback Get = %v, want ErrNotFound", err)
	}

	// Committed: readable, and a second PutTx replaces it.
	for _, val := range []string{"first", "second"} {
		if err := st.WriteTx(ctx, func(tx *sql.Tx) error { return v.PutTx(ctx, tx, "committed", []byte(val)) }); err != nil {
			t.Fatal(err)
		}
		if got, err := v.Get(ctx, "committed"); err != nil || string(got) != val {
			t.Fatalf("Get = %q, %v; want %q", got, err, val)
		}
	}

	err = st.WriteTx(ctx, func(tx *sql.Tx) error { return v.PutTx(ctx, tx, "bad name", []byte("x")) })
	if !errors.Is(err, ErrInvalidName) {
		t.Fatalf("PutTx with a bad name = %v", err)
	}
}

func TestTamperFails(t *testing.T) {
	for _, col := range []string{"data_key", "ciphertext"} {
		t.Run(col, func(t *testing.T) {
			v, st, _ := newVault(t)
			ctx := t.Context()
			if err := v.Put(ctx, "a", []byte("value-a")); err != nil {
				t.Fatal(err)
			}
			var b []byte
			if err := st.Reader().QueryRowContext(ctx, `SELECT `+col+` FROM vault_secrets WHERE name = 'a'`).Scan(&b); err != nil {
				t.Fatal(err)
			}
			b[len(b)-1] ^= 0x01
			if _, err := st.Writer().ExecContext(ctx, `UPDATE vault_secrets SET `+col+` = ? WHERE name = 'a'`, b); err != nil {
				t.Fatal(err)
			}
			if _, err := v.Get(ctx, "a"); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Get after tamper = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestSwapAndRenameFail(t *testing.T) {
	v, st, _ := newVault(t)
	ctx := t.Context()
	if err := v.Put(ctx, "a", []byte("value-a")); err != nil {
		t.Fatal(err)
	}
	if err := v.Put(ctx, "b", []byte("value-b")); err != nil {
		t.Fatal(err)
	}

	// Swap the sealed columns between two rows: each now carries a valid
	// envelope, but for the wrong ID and name.
	w := st.Writer()
	sealed := map[string][2][]byte{}
	for _, name := range []string{"a", "b"} {
		var dk, ct []byte
		if err := w.QueryRowContext(ctx, `SELECT data_key, ciphertext FROM vault_secrets WHERE name = ?`, name).Scan(&dk, &ct); err != nil {
			t.Fatal(err)
		}
		sealed[name] = [2][]byte{dk, ct}
	}
	for name, from := range map[string]string{"a": "b", "b": "a"} {
		if _, err := w.ExecContext(ctx, `UPDATE vault_secrets SET data_key = ?, ciphertext = ? WHERE name = ?`, sealed[from][0], sealed[from][1], name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a", "b"} {
		if _, err := v.Get(ctx, name); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("Get(%q) after swap = %v, want ErrCorrupt", name, err)
		}
	}

	// Renaming a row is caught too.
	if err := v.Put(ctx, "c", []byte("value-c")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ExecContext(ctx, `UPDATE vault_secrets SET name = 'renamed' WHERE name = 'c'`); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, "renamed"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Get after rename = %v, want ErrCorrupt", err)
	}
}

func TestGeneratedKeyFile(t *testing.T) {
	v, st, keyPath := newVault(t)
	ctx := t.Context()

	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file mode = %o, want 600", perm)
	}
	raw, _ := os.ReadFile(keyPath)
	if k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw))); err != nil || len(k) != keySize {
		t.Fatalf("key file is not base64 of %d bytes: %v", keySize, err)
	}

	if err := v.Put(ctx, "kept", []byte("across restarts")); err != nil {
		t.Fatal(err)
	}

	// A second boot loads the same key and reads existing secrets.
	v2, err := Open(ctx, st, KeyConfig{Path: keyPath, Generate: true}, discard)
	if err != nil {
		t.Fatal(err)
	}
	if v2.KeyID() != v.KeyID() {
		t.Fatal("reloaded key has a different ID")
	}
	if got, err := v2.Get(ctx, "kept"); err != nil || string(got) != "across restarts" {
		t.Fatalf("Get after reload = %q, %v", got, err)
	}
}

func TestMissingKeyWithSecretsRefusesToRegenerate(t *testing.T) {
	v, st, keyPath := newVault(t)
	ctx := t.Context()
	if err := v.Put(ctx, "x", []byte("y")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, st, KeyConfig{Path: keyPath, Generate: true}, discard); err == nil {
		t.Fatal("expected an error when the key is lost but secrets exist")
	}
	if ok, _ := exists(keyPath); ok {
		t.Fatal("a replacement key must not be written")
	}
}

func TestExplicitKeyFile(t *testing.T) {
	st, dir := newStore(t)
	ctx := t.Context()
	missing := filepath.Join(dir, "nope.key")
	if _, err := Open(ctx, st, KeyConfig{Path: missing}, discard); err == nil {
		t.Fatal("a missing explicit key file must be an error")
	}
	if ok, _ := exists(missing); ok {
		t.Fatal("an explicit key file must never be generated")
	}

	// A Docker secret is typically mode 0444 and hex or base64; it should load
	// with a warning.
	key := make([]byte, keySize)
	rand.Read(key)
	path := filepath.Join(dir, "docker-secret")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	v, err := Open(ctx, st, KeyConfig{Path: path}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "readable by group or others") {
		t.Fatalf("expected a permissions warning, got %q", logs.String())
	}
	if err := v.Put(ctx, "s", []byte("v")); err != nil {
		t.Fatal(err)
	}

	// A different key over the same secrets is refused at boot.
	other := filepath.Join(dir, "other.key")
	if _, err := generateKeyFile(other); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, st, KeyConfig{Path: other}, discard); err == nil {
		t.Fatal("opening with the wrong key should fail")
	}
	// And a vault built with the wrong key directly reports ErrWrongKey.
	wrong, _ := New(st, bytes.Repeat([]byte{7}, keySize))
	if _, err := wrong.Get(ctx, "s"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("Get with wrong key = %v, want ErrWrongKey", err)
	}
}

func TestParseKeyFormats(t *testing.T) {
	key := make([]byte, keySize)
	rand.Read(key)
	for name, raw := range map[string][]byte{
		"raw":       key,
		"hex":       []byte(hex.EncodeToString(key) + "\n"),
		"base64":    []byte(base64.StdEncoding.EncodeToString(key) + "\n"),
		"base64url": []byte(base64.RawURLEncoding.EncodeToString(key)),
	} {
		got, err := parseKey(raw)
		if err != nil || !bytes.Equal(got, key) {
			t.Errorf("%s: parseKey = %x, %v", name, got, err)
		}
	}
	for _, bad := range []string{"", "short", hex.EncodeToString(key[:16]), base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := parseKey([]byte(bad)); err == nil {
			t.Errorf("parseKey(%q) should fail", bad)
		}
	}
}

func TestContextCancelled(t *testing.T) {
	v, _, _ := newVault(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := v.Put(ctx, "x", []byte("y")); err == nil {
		t.Fatal("Put with a cancelled context should fail")
	}
}
