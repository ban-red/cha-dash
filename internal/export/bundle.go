package export

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"

	"dash.cha.sh/internal/importer"
)

// Bundle is the encrypted half of a full backup: every secret value the
// file's placeholders name, sealed together under a key derived from a
// passphrase. It's age's passphrase recipe, simplified to one sealed blob:
//
//	key        = scrypt(passphrase, "chadash.export/v1/scrypt" ‖ salt, N=2^log_n, r, p, 32 bytes)
//	ciphertext = XChaCha20-Poly1305(key, nonce, plaintext, aad = header)
//	plaintext  = {"<vault name>": "<value>", …} as JSON
//
// The header (cipher, KDF parameters, salt, nonce and the names) is the
// associated data, so changing any of it fails decryption the same way a
// wrong passphrase does. The names are in the clear on purpose: they're
// already in the placeholders, and they let a dry run without the
// passphrase say what the bundle would restore.
type Bundle struct {
	Cipher     string
	KDF        KDF
	Nonce      []byte
	Names      []string // sorted
	Ciphertext []byte
}

// KDF is the passphrase key derivation.
type KDF struct {
	Name string // scrypt
	LogN int
	R, P int
	Salt []byte
}

const (
	bundleCipher = "xchacha20poly1305"
	kdfScrypt    = "scrypt"
	scryptLabel  = "chadash.export/v1/scrypt"
	saltSize     = 16

	// DefaultLogN is the work factor new bundles use: N = 2^15 with r = 8
	// takes 32 MiB and about a tenth of a second on a small ARM board, inside
	// the hub's memory budget.
	DefaultLogN = 15
	// maxLogN bounds what a file may ask the hub to spend on one attempt
	// (2^18 is 256 MiB).
	maxLogN = 18

	// MinPassphrase and MaxPassphrase bound a passphrase, in characters and
	// bytes. The lower bound matches the hub's password rule.
	MinPassphrase = 12
	MaxPassphrase = 1024
)

// scryptLogN is the work factor Seal uses; tests lower it.
var scryptLogN = DefaultLogN

// kdfSlots bounds concurrent key derivations, so a burst of requests can't
// take more than a few times 32 MiB.
var kdfSlots = make(chan struct{}, 2)

var (
	// ErrWeakPassphrase means a passphrase is too short or too long for a new bundle.
	ErrWeakPassphrase = fmt.Errorf("export: a passphrase needs at least %d characters (and at most %d bytes)", MinPassphrase, MaxPassphrase)
	// ErrWrongPassphrase means the bundle didn't open: a wrong passphrase, or
	// a bundle (or its header) that was changed. AEAD can't tell them apart.
	ErrWrongPassphrase = errors.New("export: the passphrase is wrong, or the secrets bundle was changed")
	// ErrBadBundle means the bundle's parameters can't be used.
	ErrBadBundle = errors.New("export: the secrets bundle is damaged or uses parameters this hub doesn't support")
)

// CheckPassphrase reports whether p may protect a new bundle.
func CheckPassphrase(p string) error {
	if utf8.RuneCountInString(p) < MinPassphrase || len(p) > MaxPassphrase || !utf8.ValidString(p) {
		return ErrWeakPassphrase
	}
	return nil
}

// sealBundle encrypts values under passphrase.
func sealBundle(passphrase string, values importer.Secrets) (*Bundle, error) {
	if err := CheckPassphrase(passphrase); err != nil {
		return nil, err
	}
	b := &Bundle{
		Cipher: bundleCipher,
		KDF:    KDF{Name: kdfScrypt, LogN: scryptLogN, R: 8, P: 1, Salt: make([]byte, saltSize)},
		Nonce:  make([]byte, chacha20poly1305.NonceSizeX),
		Names:  make([]string, 0, len(values)),
	}
	rand.Read(b.KDF.Salt)
	rand.Read(b.Nonce)
	plain := make(map[string]string, len(values))
	for name, v := range values {
		b.Names = append(b.Names, name)
		plain[name] = v.Reveal()
	}
	slices.Sort(b.Names)
	pt, err := json.Marshal(plain)
	clear(plain)
	if err != nil {
		return nil, err
	}
	defer clear(pt)
	aead, err := b.aead(passphrase)
	if err != nil {
		return nil, err
	}
	b.Ciphertext = aead.Seal(nil, b.Nonce, pt, b.header())
	return b, nil
}

// Open decrypts the bundle. A wrong passphrase, or any change to the
// bundle, gives ErrWrongPassphrase; the error never holds a value.
func (b *Bundle) Open(passphrase string) (importer.Secrets, error) {
	if err := b.check(); err != nil {
		return nil, err
	}
	if passphrase == "" || len(passphrase) > MaxPassphrase {
		return nil, ErrWrongPassphrase
	}
	aead, err := b.aead(passphrase)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, b.Nonce, b.Ciphertext, b.header())
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	defer clear(pt)
	var plain map[string]string
	if err := json.Unmarshal(pt, &plain); err != nil {
		return nil, ErrBadBundle
	}
	out := make(importer.Secrets, len(plain))
	for name, v := range plain {
		out[name] = importer.NewSecret(v)
	}
	clear(plain)
	got := make([]string, 0, len(out))
	for name := range out {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, b.Names) {
		return nil, ErrBadBundle // authenticated, so only a broken exporter gets here
	}
	return out, nil
}

// check validates a decoded bundle before any work is spent on it.
func (b *Bundle) check() error {
	if err := b.checkParams(); err != nil {
		return err
	}
	if len(b.Ciphertext) < chacha20poly1305.Overhead ||
		!slices.IsSorted(b.Names) || len(slices.Compact(slices.Clone(b.Names))) != len(b.Names) {
		return ErrBadBundle
	}
	return nil
}

// checkParams bounds the cipher and KDF parameters, so a crafted file can't
// make the hub spend more than 2^maxLogN·r·128 bytes on one attempt.
func (b *Bundle) checkParams() error {
	switch {
	case b.Cipher != bundleCipher, b.KDF.Name != kdfScrypt,
		b.KDF.LogN < 1 || b.KDF.LogN > maxLogN, b.KDF.R != 8, b.KDF.P < 1 || b.KDF.P > 4,
		len(b.KDF.Salt) < saltSize, len(b.KDF.Salt) > 64,
		len(b.Nonce) != chacha20poly1305.NonceSizeX:
		return ErrBadBundle
	}
	return nil
}

func (b *Bundle) aead(passphrase string) (cipher.AEAD, error) {
	if err := b.checkParams(); err != nil {
		return nil, err
	}
	kdfSlots <- struct{}{}
	salt := append([]byte(scryptLabel), b.KDF.Salt...)
	key, err := scrypt.Key([]byte(passphrase), salt, 1<<b.KDF.LogN, b.KDF.R, b.KDF.P, chacha20poly1305.KeySize)
	<-kdfSlots
	if err != nil {
		return nil, ErrBadBundle
	}
	defer clear(key)
	return chacha20poly1305.NewX(key)
}

// header is the associated data: every field but the ciphertext, in a fixed
// text form. Vault names can't contain a newline, so it's unambiguous.
func (b *Bundle) header() []byte {
	enc := base64.RawStdEncoding
	var sb strings.Builder
	sb.WriteString(APIVersion + " secrets bundle\n")
	sb.WriteString("cipher " + b.Cipher + "\n")
	sb.WriteString("kdf " + b.KDF.Name + " " + strconv.Itoa(b.KDF.LogN) + " " + strconv.Itoa(b.KDF.R) + " " + strconv.Itoa(b.KDF.P) + " " + enc.EncodeToString(b.KDF.Salt) + "\n")
	sb.WriteString("nonce " + enc.EncodeToString(b.Nonce) + "\n")
	sb.WriteString("names " + strconv.Itoa(len(b.Names)) + "\n")
	for _, n := range b.Names {
		sb.WriteString(n + "\n")
	}
	return []byte(sb.String())
}
