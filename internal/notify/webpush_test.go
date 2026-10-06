package notify

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// RFC 8291 Appendix A: the published example, end to end.
func TestEncryptRFC8291Vector(t *testing.T) {
	plaintext := mustB64(t, "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24")
	if string(plaintext) != "When I grow up, I want to be a watermelon" {
		t.Fatalf("plaintext = %q", plaintext)
	}
	asPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(asPriv.PublicKey().Bytes()); got != "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" {
		t.Fatalf("as_public = %s", got)
	}
	uaPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	if err != nil {
		t.Fatal(err)
	}
	uaPublic := mustB64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	if !bytes.Equal(uaPriv.PublicKey().Bytes(), uaPublic) {
		t.Fatal("ua_public doesn't match ua_private")
	}
	authSecret := mustB64(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := mustB64(t, "DGv6ra1nlYgDCS1FRnbzlw")

	got, err := encrypt(plaintext, uaPublic, authSecret, asPriv, salt)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if enc := base64.RawURLEncoding.EncodeToString(got); enc != want {
		t.Fatalf("ciphertext\n got %s\nwant %s", enc, want)
	}
}

func TestEncryptRefusesBadKeys(t *testing.T) {
	as, _ := ecdh.P256().GenerateKey(rand.Reader)
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	salt := make([]byte, 16)
	if _, err := encrypt([]byte("x"), ua.PublicKey().Bytes(), make([]byte, 15), as, salt); err == nil {
		t.Error("a 15-byte auth secret was accepted")
	}
	if _, err := encrypt([]byte("x"), make([]byte, 65), make([]byte, 16), as, salt); err == nil {
		t.Error("a key that isn't a point was accepted")
	}
}

// The VAPID JWT is ES256 over aud, exp and sub, with a raw r||s signature
// that the public key verifies.
func TestVAPIDJWT(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	tok, err := vapidJWT(key, "https://push.example", "https://dash.lab.example", exp)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token = %s", tok)
	}
	var header map[string]string
	if err := json.Unmarshal(mustB64(t, parts[0]), &header); err != nil || header["alg"] != "ES256" || header["typ"] != "JWT" {
		t.Fatalf("header = %v, %v", header, err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(mustB64(t, parts[1]), &claims); err != nil || claims.Aud != "https://push.example" || claims.Exp != exp.Unix() || claims.Sub != "https://dash.lab.example" {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
	sig := mustB64(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the signature doesn't verify")
	}
}

func TestCheckEndpoint(t *testing.T) {
	s := &Service{}
	for _, ok := range []string{"https://push.example/fcm/send/abc", "https://updates.push.example/wpush/v2/x", "https://web.push.example/Q"} {
		if _, err := s.checkEndpoint(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://push.example/x", "https://192.0.2.1/x", "https://[2001:db8::1]/x", "https://localhost/x", "https://nas/x",
		"https://user:pw@push.example/x", "https://push.example/x#f", "ftp://push.example/x", "https://push.example/" + strings.Repeat("a", maxEndpoint)} {
		if _, err := s.checkEndpoint(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
