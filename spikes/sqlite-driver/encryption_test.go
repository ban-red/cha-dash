package sqlitedriver

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const marker = "chadash-spike-plaintext-marker"

func encryptedVariants() []Variant {
	var vs []Variant
	for _, v := range Variants() {
		if v.Encrypted {
			vs = append(vs, v)
		}
	}
	return vs
}

// Nothing readable reaches the disk: not the header, not row data, not the
// WAL. Plain variants are the control.
func TestEncryptedOnDisk(t *testing.T) {
	forEach(t, func(t *testing.T, v Variant) {
		st, fx, path := seeded(t, v)
		ctx := t.Context()
		if _, err := SaveLayout(ctx, st, fx.BoardID, 0, `{"note":"`+marker+`"}`, fx.OwnerID); err != nil {
			t.Fatal(err)
		}
		// Before closing, the new page lives in the WAL.
		for _, suffix := range []string{"-wal", ""} {
			b, err := os.ReadFile(path + suffix)
			if err != nil {
				t.Fatal(err)
			}
			header := bytes.HasPrefix(b, []byte("SQLite format 3\x00"))
			found := bytes.Contains(b, []byte(marker)) || bytes.Contains(b, []byte("CREATE TABLE"))
			t.Logf("chadash.db%s: %d bytes, SQLite header %v, plaintext found %v", suffix, len(b), header, found)
			if v.Encrypted && (header || found) {
				t.Errorf("chadash.db%s leaks plaintext", suffix)
			}
		}
		if v.Encrypted {
			return
		}
		st.Close()
		b, _ := os.ReadFile(path)
		if !bytes.Contains(b, []byte(marker)) {
			t.Error("control: plain database should contain the marker after checkpoint")
		}
	})
}

// A wrong key fails at Open, before anything is written, with SQLite's
// "not a database" error; the file is left untouched.
func TestWrongKey(t *testing.T) {
	for _, v := range encryptedVariants() {
		t.Run(v.Name, func(t *testing.T) {
			st, _, path := seeded(t, v)
			sum := func() [32]byte {
				b, _ := os.ReadFile(path)
				return sha256.Sum256(b)
			}
			// Checkpoint so the main file holds everything.
			if _, err := st.W.ExecContext(t.Context(), "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				t.Fatal(err)
			}
			before := sum()

			wrong := bytes.Repeat([]byte{0x17}, 32)
			_, err := Open(t.Context(), v, path, wrong)
			if err == nil {
				t.Fatal("opened with the wrong key")
			}
			t.Logf("wrong key: %v", err)
			vfsName := strings.TrimPrefix(v.Name, "ncruces+")
			for _, k := range [][]byte{wrong, testKEK} {
				dk, _ := DBKey(k, vfsName, 32)
				if strings.Contains(err.Error(), hex.EncodeToString(dk)[:16]) {
					t.Error("error message contains the key")
				}
			}
			if sum() != before {
				t.Error("database file changed after a failed open")
			}
		})
	}
}

// Opening an encrypted database without the encrypting VFS, or with the VFS
// but no key, fails the same way.
func TestNoKey(t *testing.T) {
	enc, _ := Lookup("ncruces+adiantum")
	_, _, path := seeded(t, enc)
	for _, name := range []string{"modernc", "ncruces"} {
		v, err := Lookup(name)
		if err != nil {
			t.Skip(err)
		}
		_, err = Open(t.Context(), v, path, nil)
		t.Logf("%s, plain: %v", name, err)
		if err == nil {
			t.Errorf("%s opened an encrypted database without a key", name)
		}
	}
	db, err := sql.Open("sqlite3", fileURI(path)+"?vfs=adiantum")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	t.Logf("adiantum VFS, no key: n=%d err=%v", n, err)
	if err == nil {
		t.Error("read users without a key")
	}
}

// An existing plaintext database (today's installs, written by modernc) can
// be converted with VACUUM INTO, and converted back for support. Key
// rotation is the same operation with a new key: there is no in-place rekey.
func TestConvert(t *testing.T) {
	modernc, err1 := Lookup("modernc")
	plain, err2 := Lookup("ncruces")
	enc, err3 := Lookup("ncruces+adiantum")
	if err1 != nil || err2 != nil || err3 != nil {
		t.Skip("needs modernc, ncruces and ncruces+adiantum")
	}
	ctx := t.Context()
	src, fx, srcPath := seeded(t, modernc)
	if _, err := SaveLayout(ctx, src, fx.BoardID, 0, LayoutDoc(1<<20, 1), fx.OwnerID); err != nil {
		t.Fatal(err)
	}
	src.Close()

	dir := t.TempDir()
	encPath := filepath.Join(dir, "encrypted.db")
	key, _ := DBKey(testKEK, "adiantum", 32)

	// 1. modernc's file opens under ncruces as is: same file format, and goose
	//    sees schema version 1, so the swap needs no migration.
	p, err := Open(ctx, plain, srcPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := p.Migrate(ctx); err != nil || n != 0 {
		t.Fatalf("migrate after swap: applied %d, %v", n, err)
	}
	// 2. Encrypt: VACUUM INTO an encrypted URI.
	if _, err := p.W.ExecContext(ctx, `VACUUM INTO ?`, fileURI(encPath)+"?vfs=adiantum&hexkey="+hex.EncodeToString(key)); err != nil {
		t.Fatalf("VACUUM INTO encrypted: %v", err)
	}
	p.Close()

	e, err := Open(ctx, enc, encPath, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var check string
	var layoutLen int
	e.R.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check)
	e.R.QueryRowContext(ctx, `SELECT length(document) FROM layouts`).Scan(&layoutLen)
	if check != "ok" || layoutLen < 1<<20 {
		t.Fatalf("encrypted copy: integrity %q, layout %d bytes", check, layoutLen)
	}
	if err := LookupSession(ctx, e, fx.TokenHashes[3]); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(encPath)
	if bytes.HasPrefix(b, []byte("SQLite format 3")) {
		t.Fatal("VACUUM INTO wrote plaintext")
	}

	// 3. Decrypt for support (open with the stock sqlite3 shell). The target
	//    inherits the connection's VFS unless the URI names the default one.
	plainCopy := filepath.Join(dir, "plain.db")
	if _, err := e.W.ExecContext(ctx, `VACUUM INTO ?`, fileURI(plainCopy)+"?vfs=os"); err != nil {
		t.Fatalf("VACUUM INTO plaintext: %v", err)
	}
	b, _ = os.ReadFile(plainCopy)
	if !bytes.HasPrefix(b, []byte("SQLite format 3")) {
		t.Fatal("decrypted copy has no SQLite header")
	}
	m, err := Open(ctx, modernc, plainCopy, nil)
	if err != nil {
		t.Fatalf("modernc opening the decrypted copy: %v", err)
	}
	m.Close()
}
