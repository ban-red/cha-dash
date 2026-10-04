package export

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/importer"
)

func sampleSecrets() importer.Secrets {
	return importer.Secrets{
		"instance/i_a/api_key":  importer.NewSecret(secretValues["sonarr"]),
		"instance/i_b/password": importer.NewSecret(secretValues["traefik"]),
	}
}

func TestBundleRoundTrip(t *testing.T) {
	b, err := sealBundle(passphrase, sampleSecrets())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.Names, []string{"instance/i_a/api_key", "instance/i_b/password"}) || b.KDF.LogN != scryptLogN || len(b.KDF.Salt) != 16 || len(b.Nonce) != 24 {
		t.Fatalf("bundle = %+v", b)
	}
	mustNotLeak(t, "the ciphertext", string(b.Ciphertext))
	mustNotLeak(t, "the bundle printed", fmt.Sprintf("%+v %#v", b, b))
	got, err := b.Open(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if got["instance/i_a/api_key"].Reveal() != secretValues["sonarr"] || got["instance/i_b/password"].Reveal() != secretValues["traefik"] || len(got) != 2 {
		t.Fatal("values changed in the round trip")
	}
	// The same values seal differently each time (fresh salt and nonce).
	again, _ := sealBundle(passphrase, sampleSecrets())
	if string(again.Ciphertext) == string(b.Ciphertext) || string(again.KDF.Salt) == string(b.KDF.Salt) {
		t.Fatal("salt or nonce reused")
	}
	// An empty bundle works too (a hub with no secrets).
	empty, err := sealBundle(passphrase, importer.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := empty.Open(passphrase); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v %v", got, err)
	}
}

func TestBundleDefaultWorkFactor(t *testing.T) {
	old := scryptLogN
	scryptLogN = DefaultLogN
	defer func() { scryptLogN = old }()
	b, err := sealBundle(passphrase, sampleSecrets())
	if err != nil || b.KDF.LogN != 15 {
		t.Fatalf("%+v %v", b, err)
	}
	if _, err := b.Open(passphrase); err != nil {
		t.Fatal(err)
	}
}

func TestBundleRefusesWrongPassphrasesAndTampering(t *testing.T) {
	seal := func() *Bundle {
		b, err := sealBundle(passphrase, sampleSecrets())
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for name, tc := range map[string]struct {
		mutate func(*Bundle)
		pass   string
		want   error
	}{
		"wrong passphrase":    {func(*Bundle) {}, "the wrong passphrase", ErrWrongPassphrase},
		"empty passphrase":    {func(*Bundle) {}, "", ErrWrongPassphrase},
		"flipped ciphertext":  {func(b *Bundle) { b.Ciphertext[3] ^= 1 }, passphrase, ErrWrongPassphrase},
		"truncated":           {func(b *Bundle) { b.Ciphertext = b.Ciphertext[:len(b.Ciphertext)-1] }, passphrase, ErrWrongPassphrase},
		"renamed secret":      {func(b *Bundle) { b.Names[0] = "instance/i_a/other" }, passphrase, ErrWrongPassphrase},
		"dropped name":        {func(b *Bundle) { b.Names = b.Names[:1] }, passphrase, ErrWrongPassphrase},
		"weaker work factor":  {func(b *Bundle) { b.KDF.LogN-- }, passphrase, ErrWrongPassphrase},
		"other salt":          {func(b *Bundle) { b.KDF.Salt[0] ^= 1 }, passphrase, ErrWrongPassphrase},
		"other nonce":         {func(b *Bundle) { b.Nonce[0] ^= 1 }, passphrase, ErrWrongPassphrase},
		"absurd work factor":  {func(b *Bundle) { b.KDF.LogN = 30 }, passphrase, ErrBadBundle},
		"unknown cipher":      {func(b *Bundle) { b.Cipher = "aes" }, passphrase, ErrBadBundle},
		"unsorted names":      {func(b *Bundle) { b.Names[0], b.Names[1] = b.Names[1], b.Names[0] }, passphrase, ErrBadBundle},
		"short salt":          {func(b *Bundle) { b.KDF.Salt = b.KDF.Salt[:4] }, passphrase, ErrBadBundle},
		"oversize passphrase": {func(*Bundle) {}, strings.Repeat("x", MaxPassphrase+1), ErrWrongPassphrase},
	} {
		b := seal()
		tc.mutate(b)
		got, err := b.Open(tc.pass)
		if !errors.Is(err, tc.want) || got != nil {
			t.Errorf("%s: %v (got %d values)", name, err, len(got))
			continue
		}
		mustNotLeak(t, name, err.Error())
	}
}

func TestCheckPassphrase(t *testing.T) {
	for p, ok := range map[string]bool{
		"":                            false,
		"eleven char":                 false,
		"twelve chars":                true,
		"ünïcödé pässphräse":          true,
		strings.Repeat("x", 1024):     true,
		strings.Repeat("x", 1025):     false,
		"bad utf8 \xff\xfe padding !": false,
	} {
		if got := CheckPassphrase(p) == nil; got != ok {
			t.Errorf("CheckPassphrase(%.20q) ok = %v, want %v", p, got, ok)
		}
	}
	if _, err := sealBundle("short", sampleSecrets()); !errors.Is(err, ErrWeakPassphrase) {
		t.Fatalf("weak passphrase sealed: %v", err)
	}
}

func TestBundleSurvivesTheFile(t *testing.T) {
	b, err := sealBundle(passphrase, sampleSecrets())
	if err != nil {
		t.Fatal(err)
	}
	data, err := Encode(&Document{Bundle: b})
	if err != nil {
		t.Fatal(err)
	}
	mustNotLeak(t, "the file", string(data))
	if !strings.Contains(string(data), "  names:\n    - instance/i_a/api_key\n    - instance/i_b/password\n  ciphertext: |\n    ") {
		t.Fatalf("file:\n%s", data)
	}
	d := parse(t, data)
	got, err := d.Bundle.Open(passphrase)
	if err != nil || got["instance/i_b/password"].Reveal() != secretValues["traefik"] {
		t.Fatalf("after the file: %v", err)
	}
	// An edited header in the file fails like a wrong passphrase.
	edited := strings.Replace(string(data), "log_n: 10", "log_n: 11", 1)
	if _, err := parse(t, []byte(edited)).Bundle.Open(passphrase); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("edited log_n: %v", err)
	}
	// A file asking for an absurd work factor is refused before any work.
	absurd := strings.Replace(string(data), "log_n: 10", "log_n: 25", 1)
	if _, err := Parse([]byte(absurd)); err == nil || !strings.Contains(err.Error(), "secrets_bundle.kdf.log_n") {
		t.Fatalf("absurd log_n: %v", err)
	}
}
