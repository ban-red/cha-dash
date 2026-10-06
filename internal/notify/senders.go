package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
)

// ── ntfy ─────────────────────────────────────────────────────────────────

// ntfyPriority maps a severity to ntfy's 1–5: down is high (4), attention
// the default (3), anything else low (2). Nothing is "urgent" (5), which
// breaks through do-not-disturb on phones: calm, never alarmist.
func ntfyPriority(sev string) int {
	switch sev {
	case "down":
		return 4
	case "attention":
		return 3
	}
	return 2
}

// sendNtfy publishes m with ntfy's JSON API: a POST to the server's root
// naming the topic, so titles and bodies need no header encoding. The
// optional token goes in Authorization, never in the URL.
func (s *Service) sendNtfy(ctx context.Context, channelID string, cfg ntfyConfig, m Message) attempt {
	token, err := s.secret(ctx, channelID, "token")
	if err != nil {
		return attempt{err: "The channel's token couldn't be read from the vault.", retry: true}
	}
	body := struct {
		Topic    string   `json:"topic"`
		Title    string   `json:"title"`
		Message  string   `json:"message"`
		Priority int      `json:"priority"`
		Tags     []string `json:"tags"`
		Click    string   `json:"click,omitempty"`
	}{cfg.Topic, m.Title, m.Body, ntfyPriority(m.Severity), []string{"chadash", m.Kind}, m.URL}
	if body.Message == "" {
		body.Message = m.Title
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Server+"/", bytes.NewReader(b))
	if err != nil {
		return attempt{err: "The ntfy server's address is unusable."}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.userAgent)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return s.do(s.lanClient, req)
}

// ── webhook ──────────────────────────────────────────────────────────────

// Webhook headers. The signature is HMAC-SHA256, keyed with the channel's
// secret, over the timestamp, a dot and the exact body bytes:
//
//	X-Chadash-Signature: sha256=hex(HMAC(secret, timestamp + "." + body))
//
// so a receiver can refuse a replayed request by its timestamp.
const (
	HeaderSignature = "X-Chadash-Signature"
	HeaderTimestamp = "X-Chadash-Timestamp"
	HeaderEvent     = "X-Chadash-Event"
	HeaderDelivery  = "X-Chadash-Delivery"
)

// Sign is the webhook signature for a body sent at timestamp (Unix
// seconds), as the X-Chadash-Signature header carries it.
func Sign(secret []byte, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// sendWebhook POSTs m as JSON (Message, version 1), signed.
func (s *Service) sendWebhook(ctx context.Context, channelID, deliveryID string, cfg webhookConfig, m Message) attempt {
	secret, err := s.secret(ctx, channelID, "secret")
	if err != nil || secret == "" {
		return attempt{err: "The webhook's signing secret couldn't be read from the vault.", retry: err != nil}
	}
	body, _ := json.Marshal(m)
	ts := s.now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return attempt{err: "The webhook's address is unusable."}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign([]byte(secret), ts, body))
	req.Header.Set(HeaderEvent, m.Kind)
	req.Header.Set(HeaderDelivery, deliveryID)
	return s.do(s.lanClient, req)
}
