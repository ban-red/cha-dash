package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"time"
)

// Delivery: each message to each channel (or device) is a row in
// notify_deliveries, tried at once and then again with backoff while the
// failure is worth retrying (timeouts, 5xx, 429 with its Retry-After),
// up to maxAttempts. A 4xx is final; for Web Push, 404 and 410 mean the
// subscription expired, and the device is removed. Pending rows survive a
// restart.

const (
	maxAttempts  = 5
	sendParallel = 4
	dueBatch     = 32
)

// backoff is the wait after attempt n (1-based) fails.
var backoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

func backoffAfter(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > len(backoff) {
		return backoff[len(backoff)-1]
	}
	return backoff[n-1]
}

// addDelivery queues m for a channel (and a device, for Web Push). Over the
// destination's rate limit, it's logged as dropped instead.
func (s *Service) addDelivery(ctx context.Context, ch channelRow, sub *subscription, m Message, now time.Time) string {
	return s.insertDelivery(ctx, ch, sub, m, now, false)
}

// insertDelivery is addDelivery; held rows (tests, sent at once by the
// caller) have no next_at, so the loop never picks them up.
func (s *Service) insertDelivery(ctx context.Context, ch channelRow, sub *subscription, m Message, now time.Time, held bool) string {
	key := ch.id
	var subID any
	if sub != nil {
		key += "/" + sub.ID
		subID = sub.ID
	}
	state, nextAt, lastErr := "pending", any(now.UnixMilli()), any(nil)
	if held {
		nextAt = nil
	}
	if m.Kind != KindAction && m.Kind != KindTest && !s.allow(key, now) {
		state, nextAt, lastErr = "dropped", nil, "Too many messages in a short time, so this one wasn't sent."
	}
	ids := []string{m.ID}
	if m.Kind == KindGroup {
		ids = ids[:0]
		for _, it := range m.Items {
			ids = append(ids, it.ID)
		}
	}
	evs, _ := json.Marshal(ids)
	id := newID("nd_")
	if _, err := s.st.Writer().ExecContext(ctx, `INSERT INTO notify_deliveries (id, channel_id, channel_name, channel_kind, subscription_id, events, kind, title, payload, state, next_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, ch.id, ch.name, ch.kind, subID, string(evs), m.Kind, clip(m.Title, maxTitle), string(encodeMessage(m)), state, nextAt, lastErr, now.UnixMilli(), now.UnixMilli()); err != nil {
		s.log.Error("notify: queueing a delivery failed", "channel", ch.id, "err", err)
		return ""
	}
	return id
}

type dueRow struct {
	id, channelID, channelKind string
	subID                      sql.NullString
	payload                    string
	attempts                   int
}

// deliverDue sends every pending delivery that's due, a few at a time.
func (s *Service) deliverDue(ctx context.Context) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, channel_id, channel_kind, subscription_id, payload, attempts FROM notify_deliveries
		WHERE state = 'pending' AND next_at <= ? ORDER BY next_at LIMIT ?`, s.now().UnixMilli(), dueBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("notify: reading due deliveries failed", "err", err)
		}
		return
	}
	var due []dueRow
	for rows.Next() {
		var d dueRow
		if err := rows.Scan(&d.id, &d.channelID, &d.channelKind, &d.subID, &d.payload, &d.attempts); err != nil {
			rows.Close()
			return
		}
		due = append(due, d)
	}
	rows.Close()
	sem := make(chan struct{}, sendParallel)
	var wg sync.WaitGroup
	for _, d := range due {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			a := s.attemptDelivery(ctx, d)
			s.record(ctx, d, a, false)
		})
	}
	wg.Wait()
}

// attemptDelivery sends one delivery once.
func (s *Service) attemptDelivery(ctx context.Context, d dueRow) attempt {
	m, err := decodeMessage([]byte(d.payload))
	if err != nil {
		return attempt{err: "The stored message couldn't be read."}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout+5*time.Second)
	defer cancel()
	if d.channelKind == ChannelWebPush {
		sub, err := s.subscription(ctx, d.subID.String)
		if errors.Is(err, errNotFound) {
			return attempt{err: "The device was removed before this was sent."}
		} else if err != nil {
			return attempt{err: "The device couldn't be read.", retry: true}
		}
		return s.sendPush(ctx, sub, pushOf(m), urgencyOf(m))
	}
	rows, err := s.channelRows(ctx, "WHERE id = ?", d.channelID)
	if err != nil {
		return attempt{err: "The channel couldn't be read.", retry: true}
	}
	if len(rows) == 0 {
		return attempt{err: "The channel was deleted."}
	}
	ch := rows[0]
	switch ch.kind {
	case ChannelNtfy:
		var c ntfyConfig
		_ = json.Unmarshal(ch.config, &c)
		return s.sendNtfy(ctx, ch.id, c, m)
	case ChannelWebhook:
		var c webhookConfig
		_ = json.Unmarshal(ch.config, &c)
		return s.sendWebhook(ctx, ch.id, d.id, c, m)
	}
	return attempt{err: "Unknown channel kind."}
}

// record stores how an attempt went. final refuses retries (tests).
func (s *Service) record(ctx context.Context, d dueRow, a attempt, final bool) {
	ctx = context.WithoutCancel(ctx)
	now := s.now()
	attempts := d.attempts + 1
	var status any
	if a.status != 0 {
		status = a.status
	}
	state, nextAt, lastErr := "sent", any(nil), any(nil)
	if !a.ok() {
		lastErr = a.err
		if a.retry && !final && attempts < maxAttempts {
			state = "pending"
			nextAt = now.Add(max(backoffAfter(attempts), a.retryAfter)).UnixMilli()
		} else {
			state = "failed"
		}
	}
	if _, err := s.st.Writer().ExecContext(ctx, `UPDATE notify_deliveries SET state = ?, attempts = ?, next_at = ?, status_code = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		state, attempts, nextAt, status, lastErr, now.UnixMilli(), d.id); err != nil {
		s.log.Error("notify: recording a delivery failed", "delivery", d.id, "err", err)
	}
	if !d.subID.Valid {
		if state == "failed" {
			s.log.Warn("notify: a message couldn't be delivered", "channel", d.channelID, "delivery", d.id, "reason", a.err)
		}
		return
	}
	switch {
	case a.gone:
		if _, err := s.st.Writer().ExecContext(ctx, `DELETE FROM notify_push_subscriptions WHERE id = ?`, d.subID.String); err != nil {
			s.log.Error("notify: removing an expired device failed", "err", err)
		}
		s.log.Info("notify: removed an expired push subscription", "subscription", d.subID.String)
	case a.ok():
		_, _ = s.st.Writer().ExecContext(ctx, `UPDATE notify_push_subscriptions SET last_ok_at = ?, last_error = NULL WHERE id = ?`, now.UnixMilli(), d.subID.String)
	default:
		_, _ = s.st.Writer().ExecContext(ctx, `UPDATE notify_push_subscriptions SET last_error = ? WHERE id = ?`, a.err, d.subID.String)
	}
}

// pushOf is what the service worker gets.
func pushOf(m Message) pushMessage {
	return pushMessage{V: 1, Kind: m.Kind, Severity: m.Severity, Title: m.Title, Body: m.Body, Path: m.Path, Tag: m.Tag, At: m.At.UTC().Format(time.RFC3339)}
}

// urgencyOf is RFC 8030's Urgency: high wakes a sleeping phone.
func urgencyOf(m Message) string {
	switch {
	case m.Severity == "down" || m.Kind == KindAction:
		return "high"
	case m.Kind == KindResolved:
		return "low"
	}
	return "normal"
}

// prune drops old events and deliveries and gives up on stale ones.
func (s *Service) prune(ctx context.Context, now time.Time) {
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE notify_deliveries SET state = 'failed', next_at = NULL, last_error = 'Gave up after two days of trying.', updated_at = ? WHERE state = 'pending' AND created_at < ?`,
			[]any{now.UnixMilli(), now.Add(-giveUpAfter).UnixMilli()}},
		{`DELETE FROM notify_deliveries WHERE state <> 'pending' AND created_at < ?`, []any{now.Add(-retention).UnixMilli()}},
		{`DELETE FROM notify_events WHERE created_at < ?`, []any{now.Add(-retention).UnixMilli()}},
	} {
		if _, err := s.st.Writer().ExecContext(ctx, q.sql, q.args...); err != nil && ctx.Err() == nil {
			s.log.Error("notify: pruning failed", "err", err)
		}
	}
}

// ── the delivery log ─────────────────────────────────────────────────────

// Delivery is one row of the delivery log.
type Delivery struct {
	ID          string     `json:"id"`
	ChannelID   string     `json:"channel_id"`
	ChannelName string     `json:"channel_name"`
	ChannelKind string     `json:"channel_kind"`
	Device      *string    `json:"device"` // Web Push: the device's label
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	StatusCode  *int       `json:"status_code"`
	Error       *string    `json:"error"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	NextAt      *time.Time `json:"next_at"`
}

// Delivery states.
var deliveryStates = map[string]bool{"pending": true, "sent": true, "failed": true, "dropped": true}

// Deliveries lists the log, newest first; state narrows it.
func (s *Service) Deliveries(ctx context.Context, state string, limit int) ([]Delivery, error) {
	q := `SELECT d.id, d.channel_id, d.channel_name, d.channel_kind, p.label, d.subscription_id, d.kind, d.title, d.state, d.attempts, d.status_code, d.last_error, d.created_at, d.updated_at, d.next_at
		FROM notify_deliveries d LEFT JOIN notify_push_subscriptions p ON p.id = d.subscription_id`
	args := []any{}
	if state != "" {
		q += ` WHERE d.state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY d.created_at DESC, d.rowid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.st.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var label, subID, lastErr sql.NullString
		var status, next sql.NullInt64
		var created, updated int64
		if err := rows.Scan(&d.ID, &d.ChannelID, &d.ChannelName, &d.ChannelKind, &label, &subID, &d.Kind, &d.Title, &d.State, &d.Attempts, &status, &lastErr, &created, &updated, &next); err != nil {
			return nil, err
		}
		if subID.Valid {
			l := label.String
			if !label.Valid {
				l = "a removed device"
			} else if l == "" {
				l = "a device"
			}
			d.Device = &l
		}
		if status.Valid {
			n := int(status.Int64)
			d.StatusCode = &n
		}
		if lastErr.Valid {
			d.Error = &lastErr.String
		}
		if next.Valid {
			t := time.UnixMilli(next.Int64).UTC()
			d.NextAt = &t
		}
		d.CreatedAt, d.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── tests ────────────────────────────────────────────────────────────────

// TestResult is POST /api/notify/channels/{id}/test.
type TestResult struct {
	OK         bool    `json:"ok"`
	StatusCode *int    `json:"status_code"`
	Error      *string `json:"error"`
	Devices    *int    `json:"devices,omitempty"` // Web Push: how many of your devices it went to
}

// errNoDevices: a Web Push test with nowhere to go.
var errNoDevices = sentence("This account has no devices to notify yet. Turn on “Notify me on this device” first.")

// Test sends a test message to a channel now, once, and logs it. Web Push
// goes to the caller's own devices.
func (s *Service) Test(ctx context.Context, id, userID, username string) (TestResult, error) {
	rows, err := s.channelRows(ctx, "WHERE id = ?", id)
	if err != nil {
		return TestResult{}, err
	}
	if len(rows) == 0 {
		return TestResult{}, errNotFound
	}
	ch := rows[0]
	now := s.now()
	m := Message{Version: PayloadVersion, ID: newID("ne_"), Kind: KindTest, Severity: "info", At: now.UTC(), Path: "/settings/notifications", Tag: "test",
		Title: "Test from Cha Dash",
		Body:  "Sent by " + username + " at " + hhmm(now) + ". If you can read this, " + ch.name + " works."}
	m = withURLs(m, s.publicURL)
	var targets []*subscription
	if ch.kind == ChannelWebPush {
		subs, err := s.subscriptions(ctx, userID)
		if err != nil {
			return TestResult{}, err
		}
		if len(subs) == 0 {
			return TestResult{}, errNoDevices
		}
		for i := range subs {
			targets = append(targets, &subs[i])
		}
	} else {
		targets = []*subscription{nil}
	}
	var res TestResult
	for _, sub := range targets {
		did := s.insertDelivery(ctx, ch, sub, m, now, true)
		d := dueRow{id: did, channelID: ch.id, channelKind: ch.kind, payload: string(encodeMessage(m))}
		if sub != nil {
			d.subID = sql.NullString{String: sub.ID, Valid: true}
		}
		a := s.attemptDelivery(ctx, d)
		if did != "" {
			s.record(ctx, d, a, true)
		}
		if a.ok() {
			res.OK = true
		} else if res.Error == nil {
			e := a.err
			res.Error = &e
		}
		if a.status != 0 && res.StatusCode == nil {
			n := a.status
			res.StatusCode = &n
		}
	}
	if ch.kind == ChannelWebPush {
		n := len(targets)
		res.Devices = &n
	}
	if res.OK {
		res.Error = nil
	}
	return res, nil
}

// ── small helpers ────────────────────────────────────────────────────────

func ecdhPoint(b []byte) (*ecdh.PublicKey, error) { return ecdh.P256().NewPublicKey(b) }

func hostOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func fingerprint(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:8])
}
