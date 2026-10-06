package notify

import (
	"bytes"
	"context"
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
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/vault"
)

// Web Push: RFC 8030 (delivery), RFC 8291 (message encryption, aes128gcm
// from RFC 8188) and RFC 8292 (VAPID), with the standard library only.

const (
	// recordSize is the aes128gcm record size the header announces. One
	// record carries the whole message.
	recordSize = 4096
	// maxPushPlaintext keeps a message inside the 4096 bytes push services
	// accept: 86 bytes of header, a delimiter and a 16-byte tag around it.
	maxPushPlaintext = 3800
	// pushTTL is how long the push service keeps a message for a device
	// that's offline.
	pushTTL = 24 * time.Hour
	// vapidLifetime is the JWT's lifetime; RFC 8292 caps it at 24 hours.
	vapidLifetime = 12 * time.Hour
	// vapidVaultName is where the hub's VAPID private key lives.
	vapidVaultName = "notify/vapid-key"
	// defaultVAPIDSubject is the contact in the VAPID JWT when the hub has
	// no https public URL. Push services want a mailto: or https: URI.
	defaultVAPIDSubject = "https://dash.cha.sh"
	// maxEndpoint bounds a subscription's endpoint URL.
	maxEndpoint = 1024
)

// encrypt seals plaintext for one subscription (RFC 8291 §3.4): an ECDH
// secret between asPriv and the browser's key, mixed with its auth secret,
// gives the content key and nonce for one aes128gcm record (RFC 8188 §2).
// asPriv and salt must be fresh for every message; tests pass RFC 8291's.
func encrypt(plaintext, uaPublic, authSecret []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(authSecret) != 16 {
		return nil, errors.New("notify: the subscription's auth secret isn't 16 bytes")
	}
	if len(salt) != 16 {
		return nil, errors.New("notify: the salt isn't 16 bytes")
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, errors.New("notify: the subscription's key isn't a P-256 point")
	}
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPublic := asPriv.PublicKey().Bytes()

	// IKM = HKDF(auth_secret, ecdh_secret, "WebPush: info" || 0x00 || ua_public || as_public, 32)
	info := make([]byte, 0, 14+len(uaPublic)+len(asPublic))
	info = append(info, "WebPush: info\x00"...)
	info = append(info, uaPublic...)
	info = append(info, asPublic...)
	prkKey, err := hkdf.Extract(sha256.New, shared, authSecret)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	if err != nil {
		return nil, err
	}
	// CEK and nonce from the salt (RFC 8188 §2.2, §2.3).
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
	// The only record is the last one: plaintext, then the 0x02 delimiter,
	// no padding.
	record := make([]byte, 0, len(plaintext)+1)
	record = append(record, plaintext...)
	record = append(record, 0x02)

	// Header: salt (16) || rs (uint32) || idlen (1) || keyid (the AS public key).
	out := make([]byte, 0, 16+4+1+len(asPublic)+len(record)+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	return gcm.Seal(out, nonce, record, nil), nil
}

// vapidJWT is the RFC 8292 token for one push service: ES256 over the
// push service's origin, an expiry and a contact.
func vapidJWT(key *ecdsa.PrivateKey, aud, sub string, exp time.Time) (string, error) {
	header := b64(`{"typ":"JWT","alg":"ES256"}`)
	claims, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}{aud, exp.Unix(), sub})
	if err != nil {
		return "", err
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// decodeB64 reads base64url with or without padding (browsers send it
// without; some libraries add it), or standard base64.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// vapidKey is the hub's VAPID key pair.
type vapidKey struct {
	priv *ecdsa.PrivateKey
	pub  []byte // uncompressed P-256 point, what browsers take as applicationServerKey
}

// vapid returns the hub's VAPID key, making one on first use. The private
// key is kept in the vault and nowhere else.
func (s *Service) vapid(ctx context.Context) (*vapidKey, error) {
	s.vapidMu.Lock()
	defer s.vapidMu.Unlock()
	if s.vapidKey != nil {
		return s.vapidKey, nil
	}
	if s.vault == nil {
		return nil, errNoVault
	}
	raw, err := s.vault.Get(ctx, vapidVaultName)
	switch {
	case errors.Is(err, vault.ErrNotFound):
		k, gerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if gerr != nil {
			return nil, gerr
		}
		if raw, err = k.Bytes(); err != nil {
			return nil, err
		}
		if err := s.vault.Put(ctx, vapidVaultName, raw); err != nil {
			return nil, fmt.Errorf("notify: storing the VAPID key: %w", err)
		}
		s.log.Info("notify: generated the hub's Web Push (VAPID) key")
	case err != nil:
		return nil, fmt.Errorf("notify: reading the VAPID key: %w", err)
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("notify: the stored VAPID key is unreadable: %w", err)
	}
	pub, err := priv.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	s.vapidKey = &vapidKey{priv: priv, pub: pub}
	return s.vapidKey, nil
}

var errNoVault = errors.New("notify: no vault, so Web Push has no key")

// checkEndpoint validates a push subscription's endpoint: an https URL at a
// push service, by name, without credentials, of reasonable length. The
// dialer refuses non-public addresses as well (policyPublic), so a name that
// resolves into the LAN gets nowhere either.
func (s *Service) checkEndpoint(raw string) (*url.URL, error) {
	if len(raw) > maxEndpoint {
		return nil, sentence("The push endpoint is too long.")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, sentence("A push endpoint must be an https URL at a push service.")
	}
	host := u.Hostname()
	if s.allowLoopbackPush {
		return u, nil
	}
	if _, err := netip.ParseAddr(host); err == nil || host == "localhost" || strings.HasSuffix(host, ".localhost") || !strings.Contains(host, ".") {
		return nil, sentence("A push endpoint must name a push service, not an address on this network.")
	}
	return u, nil
}

// pushMessage is what the service worker receives (web/sw/service-worker.js).
type pushMessage struct {
	V        int    `json:"v"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Path     string `json:"path"` // where a click opens, in the app
	Tag      string `json:"tag,omitempty"`
	At       string `json:"at"`
}

// subscription is one stored device.
type subscription struct {
	ID       string
	UserID   string
	Role     string
	Endpoint string
	P256dh   []byte
	Auth     []byte
	Label    string
}

// sendPush delivers one message to one device.
func (s *Service) sendPush(ctx context.Context, sub subscription, msg pushMessage, urgency string) attempt {
	u, err := s.checkEndpoint(sub.Endpoint)
	if err != nil {
		return attempt{err: err.Error(), gone: true}
	}
	key, err := s.vapid(ctx)
	if err != nil {
		s.log.Error("notify: web push key", "err", err)
		return attempt{err: "The hub's Web Push key isn't available.", retry: true}
	}
	plain, _ := json.Marshal(msg)
	if len(plain) > maxPushPlaintext {
		msg.Body = clip(msg.Body, max(0, len(msg.Body)-(len(plain)-maxPushPlaintext)-8))
		plain, _ = json.Marshal(msg)
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return attempt{err: "Encrypting the message failed.", retry: true}
	}
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	body, err := encrypt(plain, sub.P256dh, sub.Auth, asPriv, salt)
	if err != nil {
		return attempt{err: "This device's subscription keys are unusable; it was removed.", gone: true}
	}
	jwt, err := vapidJWT(key.priv, u.Scheme+"://"+u.Host, s.vapidSubject(), s.now().Add(vapidLifetime))
	if err != nil {
		return attempt{err: "Signing the request failed.", retry: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return attempt{err: "The push endpoint is unusable; it was removed.", gone: true}
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.Itoa(int(pushTTL/time.Second)))
	req.Header.Set("Urgency", urgency)
	if msg.Tag != "" {
		req.Header.Set("Topic", topicOf(msg.Tag))
	}
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+base64.RawURLEncoding.EncodeToString(key.pub))
	req.Header.Set("User-Agent", s.userAgent)
	a := s.do(s.pushClient, req)
	if a.status == http.StatusNotFound || a.status == http.StatusGone {
		a.gone, a.retry = true, false
		a.err = "The browser's push service says this device's subscription has expired; it was removed."
	}
	return a
}

// topicOf turns a tag into a Topic header (RFC 8030 §5.4: at most 32
// base64url characters), so a newer message about the same incident
// replaces an undelivered older one.
func topicOf(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}

func (s *Service) vapidSubject() string {
	if strings.HasPrefix(s.publicURL, "https://") {
		return s.publicURL
	}
	return defaultVAPIDSubject
}
