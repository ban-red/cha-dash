package vault

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/crypto/chacha20poly1305"
)

const keySize = chacha20poly1305.KeySize

// generateKeyFile writes a fresh key-encryption key to path with mode 0600.
// O_EXCL means an existing key is never overwritten, even in a race.
func generateKeyFile(path string) ([]byte, error) {
	key := make([]byte, keySize)
	rand.Read(key)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, werr := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n")
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(path)
		return nil, err
	}
	return key, nil
}

// readKeyFile loads a key-encryption key. It accepts what operators are likely
// to produce for CHADASH_SECRET_KEY_FILE: 32 raw bytes (head -c 32 /dev/urandom),
// base64 (openssl rand -base64 32) or hex (openssl rand -hex 32).
// It reports whether the file is readable by group or others.
func readKeyFile(path string) (key []byte, loosePerms bool, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("vault: key file %s is not a regular file", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	key, err = parseKey(raw)
	if err != nil {
		return nil, false, fmt.Errorf("vault: key file %s: %w", path, err)
	}
	return key, fi.Mode().Perm()&0o077 != 0, nil
}

func parseKey(raw []byte) ([]byte, error) {
	// 32 random bytes are all printable with odds of about 1e-14, so a 32-byte
	// file of plain text is a passphrase or a half-length hex key, not a raw key.
	if len(raw) == keySize && !printable(raw) {
		return bytes.Clone(raw), nil
	}
	text := string(bytes.TrimSpace(raw))
	if len(text) == hex.EncodedLen(keySize) {
		if k, err := hex.DecodeString(text); err == nil {
			return k, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(text); err == nil && len(k) == keySize {
			return k, nil
		}
	}
	return nil, fmt.Errorf("want a %d-byte key as raw bytes, base64 or hex", keySize)
}

func printable(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
