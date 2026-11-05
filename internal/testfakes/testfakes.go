// Package testfakes are fake outside services for tests: what the hub
// sends to, with a log a test reads (H3 in docs/plan/17-test-automation.md).
//
//   - Capture is any HTTP endpoint the hub posts to: ntfy, a webhook.
//   - Push is a Web Push service (RFC 8030). It checks each request's VAPID
//     signature (RFC 8292) and decrypts its payload (RFC 8291) with a
//     device's keys, so a test reads exactly what a phone would show.
//
// It imports nothing from the hub, so any package's tests can use it.
package testfakes

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Capture ──

// Request is one request a Capture received.
type Request struct {
	Method, Path string
	Header       http.Header
	Body         []byte
	At           time.Time
}

// JSON decodes the body into v.
func (r Request) JSON(v any) error { return json.Unmarshal(r.Body, v) }

// Capture is an HTTP server that records every request and answers with
// Status (200 unless set).
type Capture struct {
	*httptest.Server

	mu     sync.Mutex
	got    []Request
	status int
	wake   chan struct{}
}

// NewCapture starts a Capture, closed when the test ends.
func NewCapture(t testing.TB) *Capture {
	c := &Capture{status: http.StatusOK, wake: make(chan struct{})}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		c.mu.Lock()
		c.got = append(c.got, Request{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body, At: time.Now()})
		st := c.status
		close(c.wake)
		c.wake = make(chan struct{})
		c.mu.Unlock()
		w.WriteHeader(st)
	}))
	t.Cleanup(c.Close)
	return c
}

// SetStatus sets the status later requests are answered with.
func (c *Capture) SetStatus(code int) {
	c.mu.Lock()
	c.status = code
	c.mu.Unlock()
}

// Requests is what arrived so far, oldest first.
func (c *Capture) Requests() []Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Request(nil), c.got...)
}

// Wait waits until at least n requests have arrived, and returns them. It
// fails the test after within.
func (c *Capture) Wait(t testing.TB, n int, within time.Duration) []Request {
	t.Helper()
	deadline := time.After(within)
	for {
		c.mu.Lock()
		got, wake := append([]Request(nil), c.got...), c.wake
		c.mu.Unlock()
		if len(got) >= n {
			return got
		}
		select {
		case <-wake:
		case <-deadline:
			t.Fatalf("%d of %d requests within %s", len(got), n, within)
		}
	}
}

// ── Web Push ──

// Device is a browser's side of a push subscription: its key pair and
// auth secret.
type Device struct {
	priv *ecdh.PrivateKey
	auth []byte
}

// NewDevice makes a device's keys.
func NewDevice() Device {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	a := make([]byte, 16)
	_, _ = rand.Read(a)
	return Device{priv: k, auth: a}
}

// P256dh and Auth are the subscription's keys, as a browser's
// PushSubscription.toJSON() gives them.
func (d Device) P256dh() string {
	return base64.RawURLEncoding.EncodeToString(d.priv.PublicKey().Bytes())
}
func (d Device) Auth() string { return base64.RawURLEncoding.EncodeToString(d.auth) }

// Decrypt is the browser's half of RFC 8291 (aes128gcm, one record).
func (d Device) Decrypt(body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("testfakes: body too short for an aes128gcm header")
	}
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs < 18 || idlen != 65 || len(body) < 21+idlen+17 {
		return nil, errors.New("testfakes: bad aes128gcm header")
	}
	asPub, err := ecdh.P256().NewPublicKey(body[21 : 21+idlen])
	if err != nil {
		return nil, err
	}
	shared, err := d.priv.ECDH(asPub)
	if err != nil {
		return nil, err
	}
	info := append(append([]byte("WebPush: info\x00"), d.priv.PublicKey().Bytes()...), asPub.Bytes()...)
	prkKey, err := hkdf.Extract(sha256.New, shared, d.auth)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		return nil, err
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		return nil, errors.New("testfakes: no last-record delimiter")
	}
	return plain[:len(plain)-1], nil
}

// CheckVAPID verifies an Authorization header, "vapid t=<jwt>, k=<key>",
// for the push service's origin aud: the token's audience, an ES256
// signature by k, and an expiry no more than 24 hours ahead (RFC 8292).
func CheckVAPID(header, aud string) bool {
	rest, ok := strings.CutPrefix(header, "vapid t=")
	if !ok {
		return false
	}
	tok, k, ok := strings.Cut(rest, ", k=")
	if !ok {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(k)
	if err != nil {
		return false
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return false
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var c struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(claims, &c) != nil || c.Aud != aud {
		return false
	}
	if c.Exp != 0 && time.Until(time.Unix(c.Exp, 0)) > 24*time.Hour+time.Minute {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

// Pushed is one push message the service accepted, decrypted.
type Pushed struct {
	Path     string
	Payload  []byte // the plaintext the device sees
	Urgency  string
	Topic    string
	TTL      string
	Encoding string
	VAPIDOK  bool
}

// JSON decodes the payload into v.
func (p Pushed) JSON(v any) error { return json.Unmarshal(p.Payload, v) }

// Push is a Web Push service over TLS. A device registered at a path
// (Register) gets its messages decrypted; any other path is 404, as an
// expired subscription is; Gone makes a path answer 410.
type Push struct {
	*httptest.Server
	t testing.TB

	mu      sync.Mutex
	devices map[string]Device
	status  map[string]int
	got     []Pushed
	wake    chan struct{}
}

// NewPush starts a push service, closed when the test ends. Its Client
// trusts its certificate.
func NewPush(t testing.TB) *Push {
	p := &Push{t: t, devices: map[string]Device{}, status: map[string]int{}, wake: make(chan struct{})}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

func (p *Push) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	p.mu.Lock()
	d, ok := p.devices[r.URL.Path]
	st := p.status[r.URL.Path]
	p.mu.Unlock()
	switch {
	case !ok:
		w.WriteHeader(http.StatusNotFound)
		return
	case st != 0:
		w.WriteHeader(st)
		return
	case r.Method != http.MethodPost:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	plain, err := d.Decrypt(body)
	if err != nil {
		p.t.Errorf("testfakes: a push to %s doesn't decrypt: %v", r.URL.Path, err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	got := Pushed{Path: r.URL.Path, Payload: plain, Urgency: r.Header.Get("Urgency"), Topic: r.Header.Get("Topic"), TTL: r.Header.Get("TTL"),
		Encoding: r.Header.Get("Content-Encoding"), VAPIDOK: CheckVAPID(r.Header.Get("Authorization"), "https://"+r.Host)}
	p.mu.Lock()
	p.got = append(p.got, got)
	close(p.wake)
	p.wake = make(chan struct{})
	p.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

// Register makes a device at path and returns it with its endpoint URL.
func (p *Push) Register(path string) (Device, string) {
	d := NewDevice()
	p.mu.Lock()
	p.devices[path] = d
	p.mu.Unlock()
	return d, p.URL + path
}

// Gone makes path answer 410 Gone, as an unsubscribed browser's does.
func (p *Push) Gone(path string) {
	p.mu.Lock()
	p.status[path] = http.StatusGone
	p.mu.Unlock()
}

// Messages is what was pushed so far, oldest first.
func (p *Push) Messages() []Pushed {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Pushed(nil), p.got...)
}

// Wait waits until at least n messages have arrived, and returns them.
func (p *Push) Wait(t testing.TB, n int, within time.Duration) []Pushed {
	t.Helper()
	deadline := time.After(within)
	for {
		p.mu.Lock()
		got, wake := append([]Pushed(nil), p.got...), p.wake
		p.mu.Unlock()
		if len(got) >= n {
			return got
		}
		select {
		case <-wake:
		case <-deadline:
			t.Fatalf("%d of %d push messages within %s", len(got), n, within)
		}
	}
}
