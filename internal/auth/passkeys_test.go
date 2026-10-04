package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"

	"dash.cha.sh/internal/httpx"
)

// ── A software authenticator ─────────────────────────────────────────────────

// Authenticator data flags (WebAuthn §6.1).
const (
	flagUP = 0x01 // user present
	flagUV = 0x04 // user verified
	flagBE = 0x08 // backup eligible
	flagBS = 0x10 // backed up
	flagAT = 0x40 // attested credential data included
)

const (
	testOrigin = "https://dash.lab.example"
	testRPID   = "dash.lab.example"
)

var b64 = base64.RawURLEncoding

// softPasskey is one discoverable ES256 credential in a software authenticator.
// It answers the begin options the hub sends with the JSON a browser's
// PublicKeyCredential.toJSON() would give, and its fields let tests misbehave.
type softPasskey struct {
	id      []byte
	key     *ecdsa.PrivateKey
	aaguid  []byte
	handle  []byte // the user handle, stored at registration
	counter uint32 // signature counter reported next
	flags   byte   // extra flags reported (flagBE, flagBS)
	origin  string // the origin the "browser" puts in clientDataJSON
	rpID    string // overrides the RP ID hashed into authenticator data
}

func newSoftPasskey(t *testing.T, origin string) *softPasskey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	rand.Read(id)
	return &softPasskey{id: id, key: key, aaguid: make([]byte, 16), origin: origin}
}

func (p *softPasskey) clientData(t *testing.T, typ, challenge string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": p.origin, "crossOrigin": false})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (p *softPasskey) authData(rpID string, flags byte, attested []byte) []byte {
	if p.rpID != "" {
		rpID = p.rpID
	}
	h := sha256.Sum256([]byte(rpID))
	out := append(h[:], flags|p.flags)
	out = binary.BigEndian.AppendUint32(out, p.counter)
	return append(out, attested...)
}

// create answers register/begin with a "none" attestation.
func (p *softPasskey) create(t *testing.T, o creationOptions) []byte {
	t.Helper()
	handle, err := b64.DecodeString(o.PublicKey.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.handle = handle
	pub, err := p.key.PublicKey.Bytes() // 0x04 ‖ X ‖ Y
	if err != nil {
		t.Fatal(err)
	}
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
		Curve:         int64(webauthncose.P256),
		XCoord:        pub[1:33],
		YCoord:        pub[33:],
	})
	if err != nil {
		t.Fatal(err)
	}
	attested := append(append([]byte{}, p.aaguid...), byte(len(p.id)>>8), byte(len(p.id)))
	attested = append(append(attested, p.id...), cose...)
	attObj, err := webauthncbor.Marshal(struct {
		Fmt      string         `cbor:"fmt"`
		AttStmt  map[string]any `cbor:"attStmt"`
		AuthData []byte         `cbor:"authData"`
	}{"none", map[string]any{}, p.authData(o.PublicKey.RP.ID, flagUP|flagUV|flagAT, attested)})
	if err != nil {
		t.Fatal(err)
	}
	return p.credential(t, map[string]any{
		"clientDataJSON":    b64.EncodeToString(p.clientData(t, "webauthn.create", o.PublicKey.Challenge)),
		"attestationObject": b64.EncodeToString(attObj),
		"transports":        []string{"internal", "hybrid", "carrier-pigeon"},
	})
}

// get answers login/begin with an assertion signed by this passkey.
func (p *softPasskey) get(t *testing.T, o requestOptions) []byte {
	t.Helper()
	cdj := p.clientData(t, "webauthn.get", o.PublicKey.Challenge)
	ad := p.authData(o.PublicKey.RPID, flagUP|flagUV, nil)
	cdh := sha256.Sum256(cdj)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdh[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return p.credential(t, map[string]any{
		"clientDataJSON":    b64.EncodeToString(cdj),
		"authenticatorData": b64.EncodeToString(ad),
		"signature":         b64.EncodeToString(sig),
		"userHandle":        b64.EncodeToString(p.handle),
	})
}

func (p *softPasskey) credential(t *testing.T, response map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"id":                      b64.EncodeToString(p.id),
		"rawId":                   b64.EncodeToString(p.id),
		"type":                    "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  map[string]any{},
		"response":                response,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// creationOptions is register/begin's answer, as the SPA reads it.
type creationOptions struct {
	PublicKey struct {
		RP struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"rp"`
		User struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"user"`
		Challenge        string `json:"challenge"`
		PubKeyCredParams []struct {
			Type string `json:"type"`
			Alg  int    `json:"alg"`
		} `json:"pubKeyCredParams"`
		Timeout            int `json:"timeout"`
		ExcludeCredentials []struct {
			Type       string   `json:"type"`
			ID         string   `json:"id"`
			Transports []string `json:"transports"`
		} `json:"excludeCredentials"`
		AuthenticatorSelection struct {
			ResidentKey        string `json:"residentKey"`
			RequireResidentKey bool   `json:"requireResidentKey"`
			UserVerification   string `json:"userVerification"`
		} `json:"authenticatorSelection"`
		Attestation string `json:"attestation"`
	} `json:"publicKey"`
}

// requestOptions is login/begin's answer.
type requestOptions struct {
	PublicKey struct {
		Challenge        string            `json:"challenge"`
		Timeout          int               `json:"timeout"`
		RPID             string            `json:"rpId"`
		AllowCredentials []json.RawMessage `json:"allowCredentials"`
		UserVerification string            `json:"userVerification"`
	} `json:"publicKey"`
}

// ── Harness helpers ──────────────────────────────────────────────────────────

// newPasskeyHarness runs with CHADASH_PUBLIC_URL=https://dash.lab.example, and
// its requests carry that Origin, like a browser on that page.
func newPasskeyHarness(t *testing.T) *harness {
	h := newHarnessWith(t, []Option{WithPublicURL(testOrigin)})
	h.origin = testOrigin
	return h
}

// owner runs first-run setup and returns the owner's signed-in client.
func (h *harness) owner() *http.Client {
	h.t.Helper()
	c := h.client()
	if resp, body := h.do(c, "POST", "/api/setup", setupRequest{Token: h.token, Username: "owner", Password: "correct horse battery"}); resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("setup = %d %v", resp.StatusCode, body)
	}
	return c
}

func (h *harness) beginRegister(c *http.Client) (*http.Response, creationOptions) {
	h.t.Helper()
	resp, raw := h.doRaw(c, "POST", "/api/auth/passkeys/register/begin", nil, nil)
	var o creationOptions
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &o) != nil {
		h.t.Fatalf("register/begin = %d %s", resp.StatusCode, raw)
	}
	return resp, o
}

func (h *harness) finishRegister(c *http.Client, body []byte) (*http.Response, map[string]any) {
	h.t.Helper()
	return h.do(c, "POST", "/api/auth/passkeys/register/finish", body)
}

// register runs a whole registration and returns the new passkey's ID.
func (h *harness) register(c *http.Client, p *softPasskey) string {
	h.t.Helper()
	_, o := h.beginRegister(c)
	resp, body := h.finishRegister(c, p.create(h.t, o))
	if resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("register/finish = %d %v", resp.StatusCode, body)
	}
	return body["id"].(string)
}

func (h *harness) beginLogin(c *http.Client) (*http.Response, requestOptions) {
	h.t.Helper()
	resp, raw := h.doRaw(c, "POST", "/api/auth/passkeys/login/begin", nil, nil)
	var o requestOptions
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &o) != nil {
		h.t.Fatalf("login/begin = %d %s", resp.StatusCode, raw)
	}
	return resp, o
}

func (h *harness) finishLogin(c *http.Client, body []byte) (*http.Response, map[string]any) {
	h.t.Helper()
	return h.do(c, "POST", "/api/auth/passkeys/login/finish", body)
}

// login runs a whole usernameless sign-in from a fresh client.
func (h *harness) login(p *softPasskey) (*http.Response, map[string]any) {
	h.t.Helper()
	c := h.client()
	_, o := h.beginLogin(c)
	return h.finishLogin(c, p.get(h.t, o))
}

func (h *harness) passkeys(c *http.Client) []Passkey {
	h.t.Helper()
	resp, raw := h.doRaw(c, "GET", "/api/auth/passkeys", nil, nil)
	var out []Passkey
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil || out == nil {
		h.t.Fatalf("list passkeys = %d %s", resp.StatusCode, raw)
	}
	return out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestPasskeyRegisterThenUsernamelessLogin(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	if list := h.passkeys(c); len(list) != 0 {
		t.Fatalf("passkeys before any = %v", list)
	}

	resp, o := h.beginRegister(c)
	pk := o.PublicKey
	if pk.RP.ID != testRPID || pk.RP.Name != "Cha Dash" || pk.User.Name != "owner" || pk.User.DisplayName != "owner" {
		t.Fatalf("rp/user = %+v %+v", pk.RP, pk.User)
	}
	if handle, err := b64.DecodeString(pk.User.ID); err != nil || len(handle) != userHandleLen {
		t.Fatalf("user.id = %q (%v): want %d base64url bytes", pk.User.ID, err, userHandleLen)
	}
	if chal, err := b64.DecodeString(pk.Challenge); err != nil || len(chal) < 16 {
		t.Fatalf("challenge = %q (%v)", pk.Challenge, err)
	}
	sel := pk.AuthenticatorSelection
	if sel.ResidentKey != "required" || !sel.RequireResidentKey || sel.UserVerification != "preferred" || pk.Attestation != "none" {
		t.Fatalf("authenticatorSelection = %+v, attestation %q", sel, pk.Attestation)
	}
	if pk.Timeout != int(ceremonyTTL/time.Millisecond) || len(pk.ExcludeCredentials) != 0 {
		t.Fatalf("timeout %d, exclude %v", pk.Timeout, pk.ExcludeCredentials)
	}
	if !slices.ContainsFunc(pk.PubKeyCredParams, func(p struct {
		Type string `json:"type"`
		Alg  int    `json:"alg"`
	}) bool {
		return p.Type == "public-key" && p.Alg == -7
	}) {
		t.Fatalf("pubKeyCredParams lacks ES256: %v", pk.PubKeyCredParams)
	}
	ck := findCookie(resp, ceremonyCookie)
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/api/auth/passkeys/register" || ck.MaxAge != 300 {
		t.Fatalf("ceremony cookie = %+v", ck)
	}

	p := newSoftPasskey(t, testOrigin)
	resp, body := h.finishRegister(c, p.create(t, o))
	if resp.StatusCode != http.StatusCreated || body["name"] != "Passkey 1" || !strings.HasPrefix(body["id"].(string), "pk_") || len(body) != 2 {
		t.Fatalf("register/finish = %d %v", resp.StatusCode, body)
	}
	if ck := findCookie(resp, ceremonyCookie); ck == nil || ck.MaxAge >= 0 {
		t.Fatalf("finish should clear the ceremony cookie: %+v", ck)
	}
	list := h.passkeys(c)
	if len(list) != 1 || list[0].ID != body["id"] || list[0].Name != "Passkey 1" || list[0].RPID != testRPID || list[0].LastUsedAt != nil || list[0].CreatedAt.IsZero() {
		t.Fatalf("passkeys = %+v", list)
	}
	var transports string
	if err := h.st.Reader().QueryRow(`SELECT transports FROM passkeys`).Scan(&transports); err != nil || transports != `["internal","hybrid"]` {
		t.Fatalf("stored transports = %s (%v)", transports, err)
	}

	// A fresh browser signs in without a username.
	anon := h.client()
	resp, lo := h.beginLogin(anon)
	if lo.PublicKey.RPID != testRPID || len(lo.PublicKey.AllowCredentials) != 0 || lo.PublicKey.UserVerification != "preferred" || lo.PublicKey.Timeout != 300000 {
		t.Fatalf("request options = %+v", lo.PublicKey)
	}
	if ck := findCookie(resp, ceremonyCookie); ck == nil || ck.Path != "/api/auth/passkeys/login" {
		t.Fatalf("login ceremony cookie = %+v", ck)
	}
	resp, body = h.finishLogin(anon, p.get(t, lo))
	if resp.StatusCode != http.StatusOK || body["user"].(map[string]any)["username"] != "owner" {
		t.Fatalf("login/finish = %d %v", resp.StatusCode, body)
	}
	if ck := findCookie(resp, CookieName); ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %+v", ck)
	}
	if resp, body := h.do(anon, "GET", "/api/auth/session", nil); resp.StatusCode != http.StatusOK || body["session"].(map[string]any)["method"] != MethodPasskey {
		t.Fatalf("session = %d %v", resp.StatusCode, body)
	}
	if list := h.passkeys(anon); list[0].LastUsedAt == nil || time.Since(*list[0].LastUsedAt) > time.Minute {
		t.Fatalf("last_used_at not recorded: %+v", list[0])
	}
}

// The dev setup: no CHADASH_PUBLIC_URL, the SPA on http://localhost:5173
// proxying to the hub with Host intact.
func TestPasskeysOnLocalhost(t *testing.T) {
	h := newHarness(t)
	h.origin, h.host = "http://localhost:5173", "localhost:5173"
	c := h.owner()
	_, o := h.beginRegister(c)
	if o.PublicKey.RP.ID != "localhost" {
		t.Fatalf("rp.id = %q", o.PublicKey.RP.ID)
	}
	p := newSoftPasskey(t, "http://localhost:5173")
	if resp, body := h.finishRegister(c, p.create(t, o)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d %v", resp.StatusCode, body)
	}
	if resp, body := h.login(p); resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d %v", resp.StatusCode, body)
	}

	// The same hub reached at 127.0.0.1 can't do passkeys, and says why.
	h.origin, h.host = "", ""
	resp, body := h.do(h.client(), "POST", "/api/auth/passkeys/login/begin", nil)
	if resp.StatusCode != http.StatusConflict || errCode(body) != "passkeys_unavailable" || !strings.Contains(body["error"].(map[string]any)["message"].(string), "http://localhost:") {
		t.Fatalf("begin at 127.0.0.1 = %d %v", resp.StatusCode, body)
	}
}

func TestPasskeyWrongOriginOrRPIDRejected(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()

	for name, p := range map[string]*softPasskey{
		"other origin":   newSoftPasskey(t, "https://evil.example"),
		"plain http":     newSoftPasskey(t, "http://dash.lab.example"),
		"other RP ID":    func() *softPasskey { p := newSoftPasskey(t, testOrigin); p.rpID = "evil.example"; return p }(),
		"parent domain":  func() *softPasskey { p := newSoftPasskey(t, testOrigin); p.rpID = "lab.example"; return p }(),
		"other port too": newSoftPasskey(t, "https://dash.lab.example:8443"),
	} {
		_, o := h.beginRegister(c)
		if resp, body := h.finishRegister(c, p.create(t, o)); resp.StatusCode != http.StatusBadRequest || errCode(body) != "passkey_rejected" {
			t.Errorf("register with %s = %d %v", name, resp.StatusCode, body)
		}
	}
	if list := h.passkeys(c); len(list) != 0 {
		t.Fatalf("rejected registrations were saved: %v", list)
	}

	p := newSoftPasskey(t, testOrigin)
	h.register(c, p)
	p.origin = "https://evil.example"
	if resp, body := h.login(p); resp.StatusCode != http.StatusUnauthorized || errCode(body) != "passkey_rejected" {
		t.Fatalf("login from another origin = %d %v", resp.StatusCode, body)
	}
	p.origin, p.rpID = testOrigin, "evil.example"
	if resp, body := h.login(p); resp.StatusCode != http.StatusUnauthorized || errCode(body) != "passkey_rejected" {
		t.Fatalf("login with another RP ID = %d %v", resp.StatusCode, body)
	}
	p.rpID = ""
	if resp, body := h.login(p); resp.StatusCode != http.StatusOK {
		t.Fatalf("honest login after the failures = %d %v", resp.StatusCode, body)
	}
	if !strings.Contains(h.logs.String(), "passkey sign-in failed") {
		t.Fatal("failed sign-ins should be logged")
	}
}

func TestPasskeyChallengeIsSingleUse(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	p := newSoftPasskey(t, testOrigin)
	bare := &http.Client{} // no jar: sends exactly the cookies a test gives it

	// Registration: the same response twice, then against a newer challenge.
	resp, o := h.beginRegister(c)
	ceremonyID := findCookie(resp, ceremonyCookie).Value
	session := ""
	for _, ck := range c.Jar.Cookies(resp.Request.URL) {
		if ck.Name == CookieName {
			session = ck.Value
		}
	}
	att := p.create(t, o)
	if resp, body := h.finishRegister(c, att); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d %v", resp.StatusCode, body)
	}
	replay := map[string]string{"Cookie": CookieName + "=" + session + "; " + ceremonyCookie + "=" + ceremonyID}
	if resp, raw := h.doRaw(bare, "POST", "/api/auth/passkeys/register/finish", att, replay); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "ceremony_expired") {
		t.Fatalf("replayed registration = %d %s", resp.StatusCode, raw)
	}
	h.beginRegister(c)
	if resp, body := h.finishRegister(c, att); resp.StatusCode != http.StatusBadRequest || errCode(body) != "passkey_rejected" {
		t.Fatalf("registration under a new challenge = %d %v", resp.StatusCode, body)
	}

	// Sign-in: the same.
	anon := h.client()
	resp, lo := h.beginLogin(anon)
	ceremonyID = findCookie(resp, ceremonyCookie).Value
	assertion := p.get(t, lo)
	if resp, body := h.finishLogin(anon, assertion); resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d %v", resp.StatusCode, body)
	}
	replay = map[string]string{"Cookie": ceremonyCookie + "=" + ceremonyID}
	if resp, raw := h.doRaw(bare, "POST", "/api/auth/passkeys/login/finish", assertion, replay); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "ceremony_expired") || findCookie(resp, CookieName) != nil {
		t.Fatalf("replayed assertion = %d %s", resp.StatusCode, raw)
	}
	other := h.client()
	h.beginLogin(other)
	if resp, body := h.finishLogin(other, assertion); resp.StatusCode != http.StatusUnauthorized || errCode(body) != "passkey_rejected" {
		t.Fatalf("assertion under a new challenge = %d %v", resp.StatusCode, body)
	}

	// A registration ceremony can't finish a sign-in, nor the reverse.
	resp, _ = h.beginRegister(c)
	cross := map[string]string{"Cookie": ceremonyCookie + "=" + findCookie(resp, ceremonyCookie).Value}
	if resp, raw := h.doRaw(bare, "POST", "/api/auth/passkeys/login/finish", assertion, cross); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "ceremony_expired") {
		t.Fatalf("login finished with a registration ceremony = %d %s", resp.StatusCode, raw)
	}
}

func TestPasskeyChallengeExpires(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	p := newSoftPasskey(t, testOrigin)

	_, o := h.beginRegister(c)
	h.clock.advance(ceremonyTTL + time.Second)
	if resp, body := h.finishRegister(c, p.create(t, o)); resp.StatusCode != http.StatusBadRequest || errCode(body) != "ceremony_expired" {
		t.Fatalf("late registration = %d %v", resp.StatusCode, body)
	}
	h.register(c, p)

	anon := h.client()
	_, lo := h.beginLogin(anon)
	h.clock.advance(ceremonyTTL - time.Second) // just in time
	if resp, body := h.finishLogin(anon, p.get(t, lo)); resp.StatusCode != http.StatusOK {
		t.Fatalf("login inside the window = %d %v", resp.StatusCode, body)
	}
	_, lo = h.beginLogin(anon)
	h.clock.advance(ceremonyTTL)
	if resp, body := h.finishLogin(anon, p.get(t, lo)); resp.StatusCode != http.StatusBadRequest || errCode(body) != "ceremony_expired" {
		t.Fatalf("late login = %d %v", resp.StatusCode, body)
	}
}

func TestPasskeyDeleteThenLoginFails(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	a, b := newSoftPasskey(t, testOrigin), newSoftPasskey(t, testOrigin)
	idA, idB := h.register(c, a), h.register(c, b)

	if resp, _ := h.do(c, "DELETE", "/api/auth/passkeys/"+idA, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	resp, body := h.login(a)
	if resp.StatusCode != http.StatusUnauthorized || errCode(body) != "unknown_passkey" || findCookie(resp, CookieName) != nil {
		t.Fatalf("login with a deleted passkey = %d %v", resp.StatusCode, body)
	}
	// What the SPA hands to PublicKeyCredential.signalUnknownCredential.
	if sig, _ := body["signal"].(map[string]any); sig["rpId"] != testRPID || sig["credentialId"] != b64.EncodeToString(a.id) {
		t.Fatalf("signal = %v", body["signal"])
	}
	if resp, body := h.login(b); resp.StatusCode != http.StatusOK {
		t.Fatalf("login with the other passkey = %d %v", resp.StatusCode, body)
	}
	for _, id := range []string{idA, "pk_nope"} {
		if resp, body := h.do(c, "DELETE", "/api/auth/passkeys/"+id, nil); resp.StatusCode != http.StatusNotFound || errCode(body) != "not_found" {
			t.Fatalf("delete %s = %d %v", id, resp.StatusCode, body)
		}
	}

	// Without a password, the last passkey is the only way in and stays.
	if _, err := h.st.Writer().Exec(`UPDATE users SET password_hash = NULL`); err != nil {
		t.Fatal(err)
	}
	if resp, body := h.do(c, "DELETE", "/api/auth/passkeys/"+idB, nil); resp.StatusCode != http.StatusConflict || errCode(body) != "last_sign_in_method" {
		t.Fatalf("delete the last sign-in method = %d %v", resp.StatusCode, body)
	}
	idC := h.register(c, newSoftPasskey(t, testOrigin))
	if resp, _ := h.do(c, "DELETE", "/api/auth/passkeys/"+idB, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete with another passkey left = %d", resp.StatusCode)
	}
	if list := h.passkeys(c); len(list) != 1 || list[0].ID != idC {
		t.Fatalf("passkeys = %+v", list)
	}
}

func TestPasskeyLoginRateLimited(t *testing.T) {
	h := newPasskeyHarness(t)
	anon := h.client()
	for i := range 10 {
		if resp, body := h.finishLogin(anon, []byte(`{}`)); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("attempt %d = %d %v", i+1, resp.StatusCode, body)
		}
	}
	resp, _ := h.finishLogin(anon, []byte(`{}`))
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("11th attempt = %d (Retry-After %q)", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	// One budget with password sign-in.
	if resp, _ := h.do(anon, "POST", "/api/auth/login", loginRequest{Username: "owner", Password: "guess guess guess"}); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("password login after passkey attempts = %d", resp.StatusCode)
	}

	// Beginning has its own budget, so passkey autofill on every page load
	// doesn't spend sign-in attempts, but it's bounded too.
	h2 := newPasskeyHarness(t)
	for range 10 {
		h2.beginLogin(anon)
	}
	if resp, _ := h2.do(anon, "POST", "/api/auth/passkeys/login/begin", nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("11th begin = %d", resp.StatusCode)
	}
	if resp, _ := h2.do(anon, "POST", "/api/auth/login", loginRequest{Username: "owner", Password: "guess guess guess"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("password login after begins = %d", resp.StatusCode)
	}
}

func TestPasskeySignCountRegression(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	count := func() (n int64, clone bool) {
		t.Helper()
		if err := h.st.Reader().QueryRow(`SELECT sign_count, clone_warning FROM passkeys`).Scan(&n, &clone); err != nil {
			t.Fatal(err)
		}
		return n, clone
	}
	warnings := func() int { return strings.Count(h.logs.String(), "counter went backwards") }

	// A security key with a real counter.
	key := newSoftPasskey(t, testOrigin)
	key.counter = 1
	h.register(c, key)
	key.counter = 5
	if resp, _ := h.login(key); resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	if n, clone := count(); n != 5 || clone || warnings() != 0 {
		t.Fatalf("after a rising count: %d, clone %v, %d warnings", n, clone, warnings())
	}

	// Backwards: allowed, but logged, flagged, and the stored count holds.
	key.counter = 3
	if resp, body := h.login(key); resp.StatusCode != http.StatusOK {
		t.Fatalf("login with a lower count = %d %v", resp.StatusCode, body)
	}
	if n, clone := count(); n != 5 || !clone || warnings() != 1 {
		t.Fatalf("after a regression: %d, clone %v, %d warnings", n, clone, warnings())
	}
	if list := h.passkeys(c); !list[0].CloneWarning {
		t.Fatalf("list should show the clone warning: %+v", list[0])
	}
	key.counter = 5 // equal is a regression too
	h.login(key)
	key.counter = 6
	h.login(key)
	if n, clone := count(); n != 6 || !clone || warnings() != 2 {
		t.Fatalf("later: %d, clone %v, %d warnings", n, clone, warnings())
	}

	// Synced passkeys report 0 every time; that's not a regression.
	if _, err := h.st.Writer().Exec(`DELETE FROM passkeys`); err != nil {
		t.Fatal(err)
	}
	synced := newSoftPasskey(t, testOrigin)
	h.register(c, synced)
	for range 2 {
		if resp, _ := h.login(synced); resp.StatusCode != http.StatusOK {
			t.Fatalf("synced login = %d", resp.StatusCode)
		}
	}
	if n, clone := count(); n != 0 || clone || warnings() != 2 {
		t.Fatalf("synced: %d, clone %v, %d warnings", n, clone, warnings())
	}
}

func TestPasskeyBackupFlags(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()

	p := newSoftPasskey(t, testOrigin)
	p.flags = flagBE // can sync, hasn't yet
	h.register(c, p)
	if list := h.passkeys(c); list[0].BackedUp {
		t.Fatalf("backed_up before any backup: %+v", list[0])
	}
	p.flags = flagBE | flagBS
	if resp, _ := h.login(p); resp.StatusCode != http.StatusOK {
		t.Fatalf("login once backed up = %d", resp.StatusCode)
	}
	if list := h.passkeys(c); !list[0].BackedUp {
		t.Fatalf("backed_up not recorded: %+v", list[0])
	}
	// Backup eligibility must never change.
	p.flags = 0
	if resp, body := h.login(p); resp.StatusCode != http.StatusUnauthorized || errCode(body) != "passkey_rejected" {
		t.Fatalf("login with BE dropped = %d %v", resp.StatusCode, body)
	}
	q := newSoftPasskey(t, testOrigin)
	h.register(c, q)
	q.flags = flagBE
	if resp, _ := h.login(q); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login with BE raised = %d", resp.StatusCode)
	}
	// Backed up but not eligible is impossible.
	bad := newSoftPasskey(t, testOrigin)
	bad.flags = flagBS
	_, o := h.beginRegister(c)
	if resp, body := h.finishRegister(c, bad.create(t, o)); resp.StatusCode != http.StatusBadRequest || errCode(body) != "passkey_rejected" {
		t.Fatalf("register with BS but not BE = %d %v", resp.StatusCode, body)
	}
}

func TestPasskeyNamesAndExclusions(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	icloud := []byte{0xfb, 0xfc, 0x30, 0x07, 0x15, 0x4e, 0x4e, 0xcc, 0x8c, 0x0b, 0x6e, 0x02, 0x05, 0x57, 0xd7, 0xbd}

	var ids []string
	for i, want := range []string{"Passkey 1", "iCloud Keychain", "iCloud Keychain 2", "Passkey 4"} {
		p := newSoftPasskey(t, testOrigin)
		if i == 1 || i == 2 {
			p.aaguid = icloud
		}
		_, o := h.beginRegister(c)
		if len(o.PublicKey.ExcludeCredentials) != i {
			t.Fatalf("registration %d excludes %d passkeys", i+1, len(o.PublicKey.ExcludeCredentials))
		}
		if i > 0 && (o.PublicKey.ExcludeCredentials[0].Type != "public-key" || !slices.Equal(o.PublicKey.ExcludeCredentials[0].Transports, []string{"internal", "hybrid"})) {
			t.Fatalf("excludeCredentials = %+v", o.PublicKey.ExcludeCredentials)
		}
		resp, body := h.finishRegister(c, p.create(t, o))
		if resp.StatusCode != http.StatusCreated || body["name"] != want {
			t.Fatalf("registration %d = %d %v, want name %q", i+1, resp.StatusCode, body, want)
		}
		ids = append(ids, body["id"].(string))
		if i == 0 {
			// An authenticator that ignores excludeCredentials still can't register twice.
			_, o := h.beginRegister(c)
			if resp, body := h.finishRegister(c, p.create(t, o)); resp.StatusCode != http.StatusConflict || errCode(body) != "passkey_exists" {
				t.Fatalf("registering the same passkey again = %d %v", resp.StatusCode, body)
			}
		}
	}

	// Rename.
	resp, raw := h.doRaw(c, "PATCH", "/api/auth/passkeys/"+ids[0], map[string]string{"name": "  Laptop  "}, nil)
	var renamed Passkey
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &renamed) != nil || renamed.ID != ids[0] || renamed.Name != "Laptop" {
		t.Fatalf("rename = %d %s", resp.StatusCode, raw)
	}
	if list := h.passkeys(c); list[0].Name != "Laptop" || len(list) != 4 {
		t.Fatalf("passkeys after rename = %+v", list)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", maxPasskeyName+1), "two\nlines"} {
		if resp, body := h.do(c, "PATCH", "/api/auth/passkeys/"+ids[0], map[string]string{"name": bad}); resp.StatusCode != http.StatusUnprocessableEntity || errCode(body) != "invalid" {
			t.Errorf("rename to %q = %d %v", bad, resp.StatusCode, body)
		}
	}
	if resp, _ := h.do(c, "PATCH", "/api/auth/passkeys/"+ids[0], map[string]string{"name": strings.Repeat("é", maxPasskeyName)}); resp.StatusCode != http.StatusOK {
		t.Errorf("rename to %d characters = %d", maxPasskeyName, resp.StatusCode)
	}
	if resp, _ := h.do(c, "PATCH", "/api/auth/passkeys/pk_nope", map[string]string{"name": "x"}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("rename unknown = %d", resp.StatusCode)
	}
	if resp, _ := h.do(c, "PATCH", "/api/auth/passkeys/"+ids[0], map[string]string{"nickname": "x"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("rename with an unknown field = %d", resp.StatusCode)
	}
}

func TestPasskeyRoutesNeedOwnerSession(t *testing.T) {
	h := newPasskeyHarness(t)
	h.owner()
	anon := h.client()
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/auth/passkeys/register/begin"},
		{"POST", "/api/auth/passkeys/register/finish"},
		{"GET", "/api/auth/passkeys"},
		{"PATCH", "/api/auth/passkeys/pk_x"},
		{"DELETE", "/api/auth/passkeys/pk_x"},
	} {
		if resp, _ := h.do(anon, r.method, r.path, map[string]string{}); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d", r.method, r.path, resp.StatusCode)
		}
	}
	// Signing in doesn't: it's how you get a session.
	if resp, _ := h.do(anon, "POST", "/api/auth/passkeys/login/begin", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("login/begin without a session = %d", resp.StatusCode)
	}
}

func TestPasskeyRelyingParty(t *testing.T) {
	proxy := netip.MustParsePrefix("10.0.0.0/8")
	for _, tc := range []struct {
		name, publicURL, host, origin string
		forwardedHTTPS                bool
		want                          relyingParty
		unavailable                   string // part of the message, when unavailable
	}{
		{name: "dev server", host: "localhost:5173", origin: "http://localhost:5173", want: relyingParty{"localhost", "http://localhost:5173"}},
		{name: "subdomain of localhost", host: "Dash.Localhost", want: relyingParty{"dash.localhost", "http://dash.localhost"}},
		{name: "localhost over TLS proxy", host: "localhost:8443", forwardedHTTPS: true, want: relyingParty{"localhost", "https://localhost:8443"}},
		{name: "loopback IPv4", host: "127.0.0.1:3696", unavailable: "open Cha Dash at http://localhost:3696"},
		{name: "loopback IPv6", host: "[::1]:3696", unavailable: "http://localhost:3696"},
		{name: "LAN address", host: "192.168.1.5:3696", unavailable: "CHADASH_PUBLIC_URL"},
		{name: "name without public URL", host: "dash.lab.example", unavailable: "CHADASH_PUBLIC_URL"},
		{name: "dev server behind a Host rewrite", host: "localhost:3696", origin: "http://localhost:5173", unavailable: "this page is at http://localhost:5173"},
		{name: "public URL", publicURL: "https://dash.lab.example", host: "10.0.0.9:3696", origin: "https://dash.lab.example", want: relyingParty{testRPID, testOrigin}},
		{name: "public URL with port", publicURL: "https://dash.lab.example:8443", host: "x", want: relyingParty{testRPID, "https://dash.lab.example:8443"}},
		{name: "public URL on localhost", publicURL: "http://localhost:5173", host: "x", want: relyingParty{"localhost", "http://localhost:5173"}},
		{name: "public URL over http", publicURL: "http://dash.lab.example", host: "x", unavailable: "need HTTPS"},
		{name: "public URL is an IP", publicURL: "https://192.168.1.5", host: "x", unavailable: "need a domain name"},
		{name: "page at another address", publicURL: "https://dash.lab.example", host: "192.168.1.5:3696", origin: "http://192.168.1.5:3696", unavailable: "passkeys work at https://dash.lab.example, but this page is at http://192.168.1.5:3696"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{proxies: httpx.NewProxies([]netip.Prefix{proxy}), publicURL: tc.publicURL}
			if tc.publicURL != "" {
				var err error
				if s.publicRP, s.publicWhy, err = publicRelyingParty(tc.publicURL); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest("POST", "/api/auth/passkeys/login/begin", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.forwardedHTTPS {
				r.RemoteAddr = "10.0.0.2:1234"
				r.Header.Set("X-Forwarded-Proto", "https")
			}
			rp, err := s.relyingParty(r)
			var ue *unavailableError
			switch {
			case tc.unavailable != "":
				if !errors.As(err, &ue) || !strings.Contains(ue.msg, tc.unavailable) {
					t.Fatalf("err = %v, want unavailable mentioning %q", err, tc.unavailable)
				}
			case err != nil || rp != tc.want:
				t.Fatalf("rp = %+v, %v; want %+v", rp, err, tc.want)
			}
		})
	}
	if _, _, err := publicRelyingParty("dash.example.com"); err == nil {
		t.Error("a public URL without a scheme should be an error")
	}
}

func TestPublicURLAtStartup(t *testing.T) {
	h := newHarnessWith(t, []Option{WithPublicURL("http://dash.lab.example:3696")})
	if !strings.Contains(h.logs.String(), "passkeys are unavailable: passkeys need HTTPS") {
		t.Fatalf("no startup warning: %s", h.logs.String())
	}
	h.owner()
	resp, body := h.do(h.client(), "POST", "/api/auth/passkeys/login/begin", nil)
	if resp.StatusCode != http.StatusConflict || errCode(body) != "passkeys_unavailable" {
		t.Fatalf("begin = %d %v", resp.StatusCode, body)
	}

	h.opts = []Option{WithPublicURL("not a url")}
	if _, err := New(t.Context(), h.st, h.svc.log, httpx.Proxies{}, h.opts...); err == nil {
		t.Fatal("New accepted a malformed public URL")
	}
}

func TestCeremonyStore(t *testing.T) {
	var cs ceremonies
	now := time.Unix(1_000_000, 0)
	first := cs.put(now, ceremony{kind: ceremonyLogin, expires: now.Add(time.Minute)})
	for i := range maxCeremonies {
		cs.put(now, ceremony{kind: ceremonyLogin, expires: now.Add(2*time.Minute + time.Duration(i))})
	}
	if len(cs.m) != maxCeremonies {
		t.Fatalf("%d ceremonies kept, want at most %d", len(cs.m), maxCeremonies)
	}
	if _, ok := cs.m[first]; ok {
		t.Fatal("the ceremony closest to expiring should make room")
	}

	reg := cs.put(now, ceremony{kind: ceremonyRegister, expires: now.Add(time.Minute)})
	if _, ok := cs.take(now, reg, ceremonyLogin); ok {
		t.Fatal("took a registration as a sign-in")
	}
	if _, ok := cs.take(now, reg, ceremonyRegister); !ok {
		t.Fatal("the wrong-kind take shouldn't consume it")
	}
	if _, ok := cs.take(now, reg, ceremonyRegister); ok {
		t.Fatal("took a ceremony twice")
	}
	late := cs.put(now, ceremony{kind: ceremonyLogin, expires: now.Add(time.Minute)})
	if _, ok := cs.take(now.Add(time.Minute), late, ceremonyLogin); ok {
		t.Fatal("took an expired ceremony")
	}
	if _, ok := cs.m[late]; ok {
		t.Fatal("an expired ceremony should be dropped when found")
	}
	// Expired ones are swept on the next put.
	cs.put(now.Add(time.Hour), ceremony{kind: ceremonyLogin, expires: now.Add(2 * time.Hour)})
	if len(cs.m) != 1 {
		t.Fatalf("%d ceremonies after the sweep, want 1", len(cs.m))
	}
}

func TestDefaultPasskeyName(t *testing.T) {
	gpm := []byte{0xea, 0x9b, 0x8d, 0x66, 0x4d, 0x01, 0x1d, 0x21, 0x3c, 0xe4, 0xb6, 0xb4, 0x8c, 0xb5, 0x75, 0xd4}
	for _, tc := range []struct {
		aaguid []byte
		taken  []string
		want   string
	}{
		{nil, nil, "Passkey 1"},
		{make([]byte, 16), []string{"Laptop"}, "Passkey 2"},
		{nil, []string{"Laptop", "passkey 3"}, "Passkey 4"},
		{gpm, nil, "Google Password Manager"},
		{gpm, []string{"Google Password Manager", "Google Password Manager 2"}, "Google Password Manager 3"},
	} {
		if got := defaultPasskeyName(tc.aaguid, tc.taken); got != tc.want {
			t.Errorf("defaultPasskeyName(%x, %v) = %q, want %q", tc.aaguid, tc.taken, got, tc.want)
		}
	}
}

// The stored credential round-trips into what the library verifies against.
func TestScanCredentialRoundTrip(t *testing.T) {
	h := newPasskeyHarness(t)
	c := h.owner()
	p := newSoftPasskey(t, testOrigin)
	p.flags = flagBE
	h.register(c, p)
	var userID string
	if err := h.st.Reader().QueryRow(`SELECT id FROM users`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	creds, err := h.svc.userCredentials(t.Context(), userID, testRPID)
	if err != nil || len(creds) != 1 {
		t.Fatalf("credentials = %v, %v", creds, err)
	}
	got := creds[0]
	want := webauthn.Credential{ID: p.id, AttestationFormat: "none", AttestationType: "none"}
	if !slices.Equal(got.ID, want.ID) || got.AttestationFormat != want.AttestationFormat || got.AttestationType != want.AttestationType ||
		!got.Flags.BackupEligible || got.Flags.BackupState || !got.Flags.UserVerified || len(got.Transport) != 2 || len(got.Authenticator.AAGUID) != 16 ||
		got.Authenticator.Attachment != "platform" {
		t.Fatalf("credential = %+v", got)
	}
	if other, err := h.svc.userCredentials(t.Context(), userID, "localhost"); err != nil || len(other) != 0 {
		t.Fatalf("credentials under another RP ID = %v, %v", other, err)
	}
}
