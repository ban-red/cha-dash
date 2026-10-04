// Package vault stores integration credentials with envelope encryption.
//
// A key-encryption key (KEK) lives outside the database: <data>/secret.key,
// generated on first boot, or CHADASH_SECRET_KEY_FILE for hardening. Each
// secret gets a fresh random data key; the value is sealed with the data key
// and the data key is sealed with the KEK, both with XChaCha20-Poly1305 (whose
// 192-bit nonces are safe to pick at random). Both seals carry associated data
// binding the secret's ID and name, so rows or columns can't be swapped or
// renamed in the database without decryption failing.
//
// Secret values never appear in logs or errors; only names do.
package vault

import (
	"context"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"

	"dash.cha.sh/internal/store"
)

// alg names the envelope format stored with each row, so it can evolve.
const alg = "xchacha20poly1305/v1"

// MaxValueSize bounds a single secret. Credentials are small; this stops the
// vault from being used as a blob store.
const MaxValueSize = 64 << 10

var (
	ErrNotFound    = errors.New("vault: secret not found")
	ErrInvalidName = errors.New("vault: invalid secret name")
	ErrTooLarge    = errors.New("vault: secret value too large")
	// ErrWrongKey means the secret was sealed under a different KEK.
	ErrWrongKey = errors.New("vault: secret was sealed with a different key")
	// ErrCorrupt means authentication failed: the row was modified or swapped.
	ErrCorrupt = errors.New("vault: secret failed authentication")
)

// Names are what ${secret:name} references use: letters, digits and . _ / -.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// Vault is an open secret store.
type Vault struct {
	st    *store.Store
	kek   cipher.AEAD
	kekID string
}

// KeyConfig says where the KEK lives.
type KeyConfig struct {
	Path string
	// Generate allows creating the key when it doesn't exist. True only for the
	// default path: an explicitly configured key file that is missing is a
	// mistake (an unmounted secret), not a first boot.
	Generate bool
}

// Open loads or creates the KEK and checks it against the stored secrets.
func Open(ctx context.Context, st *store.Store, kc KeyConfig, log *slog.Logger) (*Vault, error) {
	present, err := exists(kc.Path)
	if err != nil {
		return nil, fmt.Errorf("vault: key file: %w", err)
	}

	var key []byte
	switch {
	case present:
		var loose bool
		key, loose, err = readKeyFile(kc.Path)
		if err != nil {
			return nil, err
		}
		if loose {
			log.Warn("vault key file is readable by group or others; chmod 600 it", "path", kc.Path)
		}
	case !kc.Generate:
		return nil, fmt.Errorf("vault: key file %s does not exist", kc.Path)
	default:
		// Never mint a new key over existing secrets: they'd become unreadable.
		n, err := count(ctx, st)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, fmt.Errorf("vault: key file %s is missing but the vault holds %d secrets sealed with it; restore the key file", kc.Path, n)
		}
		if key, err = generateKeyFile(kc.Path); err != nil {
			return nil, fmt.Errorf("vault: generate key: %w", err)
		}
		log.Info("generated vault key", "path", kc.Path)
	}

	v, err := New(st, key)
	clear(key)
	if err != nil {
		return nil, err
	}
	if err := v.checkKey(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

// New builds a vault from a raw 32-byte KEK.
func New(st *store.Store, key []byte) (*Vault, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("vault: key must be %d bytes", keySize)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	// The KEK ID is a keyed fingerprint: it identifies the key without revealing it.
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("chadash/vault/kek-id"))
	return &Vault{st: st, kek: aead, kekID: hex.EncodeToString(mac.Sum(nil)[:8])}, nil
}

// KeyID returns the fingerprint of the KEK in use.
func (v *Vault) KeyID() string { return v.kekID }

// checkKey fails fast when stored secrets were sealed with another key, e.g.
// when CHADASH_SECRET_KEY_FILE points at the wrong file. Key rotation, when it
// arrives, will relax this to "every key ID is known".
func (v *Vault) checkKey(ctx context.Context) error {
	var n int
	err := v.st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM vault_secrets WHERE kek_id <> ?`, v.kekID).Scan(&n)
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("vault: %d secrets were sealed with a different key than the one loaded (key id %s); check the key file", n, v.kekID)
	}
	return nil
}

// Put stores value under name, replacing any previous value.
func (v *Vault) Put(ctx context.Context, name string, value []byte) error {
	if !nameRE.MatchString(name) {
		return ErrInvalidName
	}
	if len(value) > MaxValueSize {
		return ErrTooLarge
	}
	return v.st.WriteTx(ctx, func(tx *sql.Tx) error { return v.PutTx(ctx, tx, name, value) })
}

// PutTx is Put inside the caller's write transaction (from the store's
// writer), so a secret commits or rolls back with the rows that reference
// it. The importer uses it to apply a plan atomically.
func (v *Vault) PutTx(ctx context.Context, tx *sql.Tx, name string, value []byte) error {
	if !nameRE.MatchString(name) {
		return ErrInvalidName
	}
	if len(value) > MaxValueSize {
		return ErrTooLarge
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM vault_secrets WHERE name = ?`, name).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id = "sec_" + strings.ToLower(rand.Text())
	case err != nil:
		return err
	}
	dataKey, ciphertext, err := v.seal(id, name, value)
	if err != nil {
		return err
	}
	now := store.NowMillis()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO vault_secrets (id, name, alg, kek_id, data_key, ciphertext, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET
			alg = excluded.alg, kek_id = excluded.kek_id, data_key = excluded.data_key,
			ciphertext = excluded.ciphertext, updated_at = excluded.updated_at`,
		id, name, alg, v.kekID, dataKey, ciphertext, now, now)
	return err
}

// Get returns the value stored under name.
func (v *Vault) Get(ctx context.Context, name string) ([]byte, error) {
	if !nameRE.MatchString(name) {
		return nil, ErrInvalidName
	}
	var id, rowAlg, kekID string
	var dataKey, ciphertext []byte
	err := v.st.Reader().QueryRowContext(ctx,
		`SELECT id, alg, kek_id, data_key, ciphertext FROM vault_secrets WHERE name = ?`, name).
		Scan(&id, &rowAlg, &kekID, &dataKey, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	if rowAlg != alg {
		return nil, fmt.Errorf("vault: secret %q uses unsupported format %q", name, rowAlg)
	}
	if kekID != v.kekID {
		return nil, ErrWrongKey
	}
	return v.open(id, name, dataKey, ciphertext)
}

// Delete removes a secret.
func (v *Vault) Delete(ctx context.Context, name string) error {
	if !nameRE.MatchString(name) {
		return ErrInvalidName
	}
	res, err := v.st.Writer().ExecContext(ctx, `DELETE FROM vault_secrets WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns all secret names, sorted.
func (v *Vault) List(ctx context.Context) ([]string, error) {
	rows, err := v.st.Reader().QueryContext(ctx, `SELECT name FROM vault_secrets ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// aad binds a ciphertext to one secret. Names can't contain NUL, so the
// encoding is unambiguous.
func aad(id, name string) []byte {
	return []byte(alg + "\x00" + id + "\x00" + name)
}

func (v *Vault) seal(id, name string, value []byte) (dataKey, ciphertext []byte, err error) {
	dek := make([]byte, keySize)
	rand.Read(dek)
	defer clear(dek)
	dekAEAD, err := chacha20poly1305.NewX(dek)
	if err != nil {
		return nil, nil, err
	}
	ad := aad(id, name)
	return sealWith(v.kek, dek, ad), sealWith(dekAEAD, value, ad), nil
}

func (v *Vault) open(id, name string, dataKey, ciphertext []byte) ([]byte, error) {
	ad := aad(id, name)
	dek, err := openWith(v.kek, dataKey, ad)
	if err != nil {
		return nil, ErrCorrupt
	}
	defer clear(dek)
	dekAEAD, err := chacha20poly1305.NewX(dek)
	if err != nil {
		return nil, ErrCorrupt
	}
	value, err := openWith(dekAEAD, ciphertext, ad)
	if err != nil {
		return nil, ErrCorrupt
	}
	return value, nil
}

// sealWith returns nonce || ciphertext.
func sealWith(aead cipher.AEAD, plaintext, ad []byte) []byte {
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	rand.Read(nonce)
	return aead.Seal(nonce, nonce, plaintext, ad)
}

func openWith(aead cipher.AEAD, sealed, ad []byte) ([]byte, error) {
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("short ciphertext")
	}
	n := aead.NonceSize()
	return aead.Open(nil, sealed[:n], sealed[n:], ad)
}

func count(ctx context.Context, st *store.Store) (int, error) {
	var n int
	if err := st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM vault_secrets`).Scan(&n); err != nil {
		return 0, fmt.Errorf("vault: %w", err)
	}
	return n, nil
}
