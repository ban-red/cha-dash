package nodeproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// pinPrefix starts every pin: the form policy.yaml and the hub's registry use.
const pinPrefix = "ed25519:"

// Pin is a public key as the other side pins it: "ed25519:" and the key in
// unpadded base64url. Pins are public; knowing one grants nothing.
func Pin(pub ed25519.PublicKey) string {
	return pinPrefix + base64.RawURLEncoding.EncodeToString(pub)
}

// ParsePin reads a pin back into a public key.
func ParsePin(pin string) (ed25519.PublicKey, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(pin), pinPrefix)
	if !ok {
		return nil, errors.New("nodeproto: a pin must start with ed25519")
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("nodeproto: malformed pin")
	}
	return ed25519.PublicKey(b), nil
}

// ValidPin reports whether pin parses.
func ValidPin(pin string) bool {
	_, err := ParsePin(pin)
	return err == nil
}

// Fingerprint is a short form of a pin for people to compare: the first 16
// bytes of the key's SHA-256, in groups of four hex digits. The node prints
// it in its log and the hub shows it beside the node.
func Fingerprint(pin string) string {
	pub, err := ParsePin(pin)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:16])
	var b strings.Builder
	for i := 0; i < len(h); i += 4 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+4])
	}
	return b.String()
}

// GenerateKey makes a new Ed25519 private key.
func GenerateKey() (ed25519.PrivateKey, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	return key, err
}

// PinOf is the pin of a private key's public half.
func PinOf(key ed25519.PrivateKey) string { return Pin(key.Public().(ed25519.PublicKey)) }

// pemType is the PEM block a key file holds.
const pemType = "PRIVATE KEY"

// EncodeKey writes a private key as PEM-wrapped PKCS#8.
func EncodeKey(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: der}), nil
}

// DecodeKey reads a key EncodeKey wrote.
func DecodeKey(b []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != pemType {
		return nil, errors.New("nodeproto: not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("nodeproto: %w", err)
	}
	ed, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("nodeproto: the key must be Ed25519")
	}
	return ed, nil
}

// WriteKeyFile stores key at path, 0600, atomically: a crash leaves the old
// file or the new one, never half of either.
func WriteKeyFile(path string, key ed25519.PrivateKey) error {
	b, err := EncodeKey(key)
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, b, 0o600)
}

// ReadKeyFile loads a key WriteKeyFile stored. A missing file is
// os.ErrNotExist (test with errors.Is).
func ReadKeyFile(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DecodeKey(b)
}

// WriteFileAtomic writes b to a temporary file beside path, syncs it, sets
// its mode and renames it over path.
func WriteFileAtomic(path string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // the rename itself survives a crash
		d.Close()
	}
	return nil
}
