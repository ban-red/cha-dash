package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"dash.cha.sh/internal/importer"
)

// FuzzBundle: an imported file is anyone's bytes, and its secrets bundle
// is opened with the passphrase someone types. Parse never panics; a
// bundle it accepts opens or fails with ErrWrongPassphrase or
// ErrBadBundle, never anything else; and one that opens with the real
// passphrase is the bundle that was sealed, field for field: the file
// format adds no way around the AEAD.
//
//	go test -run '^$' -fuzz '^FuzzBundle$' -fuzztime 60s ./internal/export/
func FuzzBundle(f *testing.F) {
	// The same bundles in every fuzzing process: a fixed salt and nonce.
	sealed := sealFixed(f, sampleSecrets(), 1)
	empty := sealFixed(f, importer.Secrets{}, 2)
	data, err := Encode(&Document{Bundle: sealed})
	if err != nil {
		f.Fatal(err)
	}
	emptyData, _ := Encode(&Document{Bundle: empty})
	f.Add(data)
	f.Add(emptyData)
	f.Add(bytes.Replace(data, []byte("log_n: 10"), []byte("log_n: 11"), 1))
	f.Add(bytes.Replace(data, []byte("instance/i_a/api_key"), []byte("instance/i_a/api_kez"), 1))
	f.Add([]byte("api_version: chadash.dev/v1\nsecrets_bundle:\n  cipher: xchacha20poly1305\n"))

	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := Parse(b)
		if err != nil || d.Bundle == nil {
			return
		}
		bd := d.Bundle
		if bd.KDF.LogN > scryptLogN || bd.KDF.P > 1 {
			// Allowed, but too slow to try for every input; check() still runs.
			_ = bd.check()
			return
		}
		got, err := bd.Open(passphrase)
		if err != nil {
			if !errors.Is(err, ErrWrongPassphrase) && !errors.Is(err, ErrBadBundle) {
				t.Fatalf("Open: an unexpected error: %v", err)
			}
			return
		}
		same := bd.Cipher == sealed.Cipher && bd.KDF.Name == sealed.KDF.Name && bd.KDF.LogN == sealed.KDF.LogN &&
			bd.KDF.R == sealed.KDF.R && bd.KDF.P == sealed.KDF.P && bytes.Equal(bd.KDF.Salt, sealed.KDF.Salt) &&
			bytes.Equal(bd.Nonce, sealed.Nonce) && slices.Equal(bd.Names, sealed.Names) && bytes.Equal(bd.Ciphertext, sealed.Ciphertext)
		sameEmpty := bytes.Equal(bd.Ciphertext, empty.Ciphertext) && bytes.Equal(bd.Nonce, empty.Nonce) && len(bd.Names) == 0
		if !same && !sameEmpty {
			t.Fatalf("a changed bundle opened: %+v", bd)
		}
		if len(got) != len(bd.Names) {
			t.Fatalf("opened %d values for %d names", len(got), len(bd.Names))
		}
	})
}

// sealFixed is sealBundle with a salt and nonce made from seed, so every
// process that runs the fuzz target seals the same bytes.
func sealFixed(f *testing.F, values importer.Secrets, seed byte) *Bundle {
	f.Helper()
	b, err := sealBundle(passphrase, values)
	if err != nil {
		f.Fatal(err)
	}
	for i := range b.KDF.Salt {
		b.KDF.Salt[i] = seed + byte(i)
	}
	for i := range b.Nonce {
		b.Nonce[i] = seed * byte(i+1)
	}
	plain := map[string]string{}
	for name, v := range values {
		plain[name] = v.Reveal()
	}
	pt, _ := json.Marshal(plain)
	aead, err := b.aead(passphrase)
	if err != nil {
		f.Fatal(err)
	}
	b.Ciphertext = aead.Seal(nil, b.Nonce, pt, b.header())
	return b
}
