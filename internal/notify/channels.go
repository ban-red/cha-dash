package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"dash.cha.sh/internal/vault"
)

// Channel kinds.
const (
	ChannelWebPush = "webpush"
	ChannelNtfy    = "ntfy"
	ChannelWebhook = "webhook"
)

// WebPushChannel is the built-in Web Push channel's ID. It always exists;
// it can be turned off, never deleted.
const WebPushChannel = "nc_webpush"

// Channel is one channel as the API shows it. Secrets are never in it,
// only whether each is set.
type Channel struct {
	ID        string                 `json:"id"`
	Kind      string                 `json:"kind"`
	Name      string                 `json:"name"`
	Enabled   bool                   `json:"enabled"`
	Builtin   bool                   `json:"builtin"`
	Config    json.RawMessage        `json:"config"`
	Secrets   map[string]SecretState `json:"secrets"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
	// Last is the newest delivery to it, for the list's status.
	Last *DeliverySummary `json:"last"`
	// Devices counts Web Push subscriptions (the built-in channel only).
	Devices *int `json:"devices,omitempty"`
}

// SecretState says whether a secret is stored.
type SecretState struct {
	Set bool `json:"set"`
}

// DeliverySummary is the newest delivery to a channel.
type DeliverySummary struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
	Error *string   `json:"error"`
}

// ntfyConfig is an ntfy channel's config. Its secret is "token", an ntfy
// access token (optional: a public or LAN server may not need one).
type ntfyConfig struct {
	Server string `json:"server"`
	Topic  string `json:"topic"`
}

// webhookConfig is a webhook channel's config. Its secret is "secret", the
// HMAC-SHA256 key the hub signs each request with.
type webhookConfig struct {
	URL string `json:"url"`
}

// secretFields are each kind's secrets, and whether they're required.
var secretFields = map[string]map[string]bool{
	ChannelWebPush: {},
	ChannelNtfy:    {"token": false},
	ChannelWebhook: {"secret": true},
}

var (
	topicRE      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	minSecretLen = 16
)

// secretName is where a channel's secret lives in the vault.
func secretName(id, field string) string { return "notify/channel/" + id + "/" + field }

// ChannelInput is POST /api/notify/channels, and PUT's fields (all optional
// there). In secrets, a string sets a field, null or "" clears it, and a
// field left out keeps its value.
type ChannelInput struct {
	Kind    string             `json:"kind"`
	Name    *string            `json:"name"`
	Enabled *bool              `json:"enabled"`
	Config  json.RawMessage    `json:"config"`
	Secrets map[string]*string `json:"secrets"`
}

// FieldError is one invalid field.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// invalidError is a 422 with fields.
type invalidError struct{ fields []FieldError }

func (e *invalidError) Error() string {
	if len(e.fields) > 0 {
		return e.fields[0].Message
	}
	return "That isn't valid."
}

// validateConfig checks and normalises a kind's config.
func validateConfig(kind string, raw json.RawMessage) (json.RawMessage, []FieldError) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	switch kind {
	case ChannelWebPush:
		return json.RawMessage(`{}`), nil
	case ChannelNtfy:
		var c ntfyConfig
		if err := strictJSON(raw, &c); err != nil {
			return nil, []FieldError{{"/config", "An ntfy channel's config is {server, topic}."}}
		}
		var fe []FieldError
		c.Server = strings.TrimRight(strings.TrimSpace(c.Server), "/")
		if msg := checkURL(c.Server, false); msg != "" {
			fe = append(fe, FieldError{"/config/server", "The ntfy server " + msg})
		}
		c.Topic = strings.TrimSpace(c.Topic)
		if !topicRE.MatchString(c.Topic) {
			fe = append(fe, FieldError{"/config/topic", "The topic is 1 to 64 letters, digits, - and _."})
		}
		if fe != nil {
			return nil, fe
		}
		b, _ := json.Marshal(c)
		return b, nil
	case ChannelWebhook:
		var c webhookConfig
		if err := strictJSON(raw, &c); err != nil {
			return nil, []FieldError{{"/config", "A webhook's config is {url}."}}
		}
		c.URL = strings.TrimSpace(c.URL)
		if msg := checkURL(c.URL, true); msg != "" {
			return nil, []FieldError{{"/config/url", "The webhook URL " + msg}}
		}
		b, _ := json.Marshal(c)
		return b, nil
	}
	return nil, []FieldError{{"/kind", "A channel is ntfy or webhook."}}
}

func strictJSON(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// checkURL returns "" for a usable http(s) URL, else the rest of a sentence.
func checkURL(raw string, query bool) string {
	if raw == "" {
		return "is required."
	}
	if len(raw) > 2048 {
		return "is too long."
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "must be an http:// or https:// URL."
	}
	if u.User != nil {
		return "can't carry a username or password; use the token or secret field."
	}
	if u.Fragment != "" || (!query && u.RawQuery != "") {
		return "can't have a query or fragment."
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !allowed(policyLAN, ip) {
		return "points at an address Cha Dash never sends to (link-local, multicast or unspecified)."
	}
	return ""
}

// validName checks a channel's name.
func validName(n string) (string, string) {
	n = strings.TrimSpace(n)
	if n == "" || len(n) > 64 {
		return "", "A name is 1 to 64 characters."
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", "A name can't contain control characters."
		}
	}
	return n, ""
}

// errNotFound is an unknown channel or subscription.
var errNotFound = errors.New("notify: not found")

// errNameTaken is a duplicate channel name.
var errNameTaken = sentence("Another channel already has that name.")

// errBuiltin refuses deleting the Web Push channel.
var errBuiltin = sentence("Web Push is built in. Turn it off instead.")

// ensureWebPush creates the built-in channel.
func (s *Service) ensureWebPush(ctx context.Context) error {
	now := s.now().UnixMilli()
	_, err := s.st.Writer().ExecContext(ctx, `INSERT INTO notify_channels (id, kind, name, enabled, config, created_at, updated_at)
		VALUES (?, 'webpush', 'Web Push', 1, '{}', ?, ?) ON CONFLICT (id) DO NOTHING`, WebPushChannel, now, now)
	return err
}

type channelRow struct {
	id, kind, name string
	enabled        bool
	config         json.RawMessage
	created        int64
	updated        int64
}

func (s *Service) channelRows(ctx context.Context, where string, args ...any) ([]channelRow, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, kind, name, enabled, config, created_at, updated_at FROM notify_channels `+where+` ORDER BY kind <> 'webpush', name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []channelRow
	for rows.Next() {
		var r channelRow
		var cfg string
		if err := rows.Scan(&r.id, &r.kind, &r.name, &r.enabled, &cfg, &r.created, &r.updated); err != nil {
			return nil, err
		}
		r.config = json.RawMessage(cfg)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) channelIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.channelRows(ctx, "")
	if err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, r := range rows {
		m[r.id] = true
	}
	return m, nil
}

// view is the API's form of a row.
func (s *Service) view(ctx context.Context, r channelRow) Channel {
	c := Channel{ID: r.id, Kind: r.kind, Name: r.name, Enabled: r.enabled, Builtin: r.id == WebPushChannel, Config: r.config,
		Secrets: map[string]SecretState{}, CreatedAt: time.UnixMilli(r.created).UTC(), UpdatedAt: time.UnixMilli(r.updated).UTC()}
	for f := range secretFields[r.kind] {
		c.Secrets[f] = SecretState{Set: s.secretSet(ctx, r.id, f)}
	}
	var state string
	var at int64
	var lastErr sql.NullString
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT state, updated_at, last_error FROM notify_deliveries WHERE channel_id = ? ORDER BY updated_at DESC LIMIT 1`, r.id).
		Scan(&state, &at, &lastErr); err == nil {
		c.Last = &DeliverySummary{State: state, At: time.UnixMilli(at).UTC()}
		if lastErr.Valid {
			c.Last.Error = &lastErr.String
		}
	}
	if r.kind == ChannelWebPush {
		var n int
		_ = s.st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM notify_push_subscriptions`).Scan(&n)
		c.Devices = &n
	}
	return c
}

func (s *Service) secretSet(ctx context.Context, id, field string) bool {
	if s.vault == nil {
		return false
	}
	_, err := s.vault.Get(ctx, secretName(id, field))
	return err == nil
}

// secret reads a channel's secret; "" when unset.
func (s *Service) secret(ctx context.Context, id, field string) (string, error) {
	if s.vault == nil {
		return "", nil
	}
	b, err := s.vault.Get(ctx, secretName(id, field))
	if errors.Is(err, vault.ErrNotFound) {
		return "", nil
	}
	return string(b), err
}

// Channels lists every channel, Web Push first.
func (s *Service) Channels(ctx context.Context) ([]Channel, error) {
	rows, err := s.channelRows(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]Channel, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.view(ctx, r))
	}
	return out, nil
}

// Channel returns one.
func (s *Service) Channel(ctx context.Context, id string) (Channel, error) {
	rows, err := s.channelRows(ctx, "WHERE id = ?", id)
	if err != nil {
		return Channel{}, err
	}
	if len(rows) == 0 {
		return Channel{}, errNotFound
	}
	return s.view(ctx, rows[0]), nil
}

// checkSecrets validates secrets for kind against what's stored for id.
func (s *Service) checkSecrets(ctx context.Context, kind, id string, in map[string]*string) []FieldError {
	var fe []FieldError
	fields := secretFields[kind]
	for f, v := range in {
		if _, ok := fields[f]; !ok {
			fe = append(fe, FieldError{"/secrets/" + f, "A " + kind + " channel has no secret " + clip(f, 32) + "."})
			continue
		}
		if v != nil && *v != "" {
			if len(*v) > 4096 {
				fe = append(fe, FieldError{"/secrets/" + f, "That's too long."})
			} else if f == "secret" && len(*v) < minSecretLen {
				fe = append(fe, FieldError{"/secrets/" + f, "The signing secret needs at least 16 characters."})
			} else if strings.ContainsAny(*v, "\r\n") {
				fe = append(fe, FieldError{"/secrets/" + f, "It can't contain line breaks."})
			}
		}
	}
	for f, required := range fields {
		if !required {
			continue
		}
		v, given := in[f]
		set := given && v != nil && *v != ""
		if !set && (given || id == "" || !s.secretSet(ctx, id, f)) {
			fe = append(fe, FieldError{"/secrets/" + f, "A webhook needs a signing secret (at least 16 characters)."})
		}
	}
	return fe
}

// CreateChannel adds an ntfy or webhook channel.
func (s *Service) CreateChannel(ctx context.Context, in ChannelInput) (Channel, error) {
	if in.Kind != ChannelNtfy && in.Kind != ChannelWebhook {
		return Channel{}, &invalidError{[]FieldError{{"/kind", "A new channel is ntfy or webhook; Web Push is built in."}}}
	}
	var fe []FieldError
	name := ""
	if in.Name != nil {
		var msg string
		if name, msg = validName(*in.Name); msg != "" {
			fe = append(fe, FieldError{"/name", msg})
		}
	} else {
		fe = append(fe, FieldError{"/name", "A name is 1 to 64 characters."})
	}
	cfg, cfe := validateConfig(in.Kind, in.Config)
	fe = append(fe, cfe...)
	fe = append(fe, s.checkSecrets(ctx, in.Kind, "", in.Secrets)...)
	if fe != nil {
		return Channel{}, &invalidError{fe}
	}
	if s.vault == nil && len(in.Secrets) > 0 {
		return Channel{}, errNoVault
	}
	id := "nc_" + strings.ToLower(rand.Text())
	enabled := in.Enabled == nil || *in.Enabled
	now := s.now().UnixMilli()
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if taken(ctx, tx, name, "") {
			return errNameTaken
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notify_channels (id, kind, name, enabled, config, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, in.Kind, name, enabled, string(cfg), now, now); err != nil {
			return err
		}
		for f, v := range in.Secrets {
			if v != nil && *v != "" {
				if err := s.vault.PutTx(ctx, tx, secretName(id, f), []byte(*v)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return Channel{}, err
	}
	return s.Channel(ctx, id)
}

func taken(ctx context.Context, tx *sql.Tx, name, except string) bool {
	var n int
	_ = tx.QueryRowContext(ctx, `SELECT count(*) FROM notify_channels WHERE name = ? COLLATE NOCASE AND id <> ?`, name, except).Scan(&n)
	return n > 0
}

// UpdateChannel changes one. Kind can't change.
func (s *Service) UpdateChannel(ctx context.Context, id string, in ChannelInput) (Channel, []string, error) {
	cur, err := s.channelRows(ctx, "WHERE id = ?", id)
	if err != nil {
		return Channel{}, nil, err
	}
	if len(cur) == 0 {
		return Channel{}, nil, errNotFound
	}
	row := cur[0]
	var fe []FieldError
	if in.Kind != "" && in.Kind != row.kind {
		fe = append(fe, FieldError{"/kind", "A channel's kind can't change; add a new one instead."})
	}
	name := row.name
	if in.Name != nil {
		var msg string
		if name, msg = validName(*in.Name); msg != "" {
			fe = append(fe, FieldError{"/name", msg})
		}
	}
	cfg := row.config
	if in.Config != nil {
		var cfe []FieldError
		cfg, cfe = validateConfig(row.kind, in.Config)
		fe = append(fe, cfe...)
	}
	fe = append(fe, s.checkSecrets(ctx, row.kind, id, in.Secrets)...)
	if fe != nil {
		return Channel{}, nil, &invalidError{fe}
	}
	if s.vault == nil && len(in.Secrets) > 0 {
		return Channel{}, nil, errNoVault
	}
	enabled := row.enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	var changed, clear []string
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if taken(ctx, tx, name, id) {
			return errNameTaken
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notify_channels SET name = ?, enabled = ?, config = ?, updated_at = ? WHERE id = ?`,
			name, enabled, string(cfg), s.now().UnixMilli(), id); err != nil {
			return err
		}
		for f, v := range in.Secrets {
			changed = append(changed, f)
			if v != nil && *v != "" {
				if err := s.vault.PutTx(ctx, tx, secretName(id, f), []byte(*v)); err != nil {
					return err
				}
			} else {
				clear = append(clear, f)
			}
		}
		return nil
	})
	if err != nil {
		return Channel{}, nil, err
	}
	s.deleteSecrets(ctx, id, clear)
	c, err := s.Channel(ctx, id)
	return c, changed, err
}

// DeleteChannel removes a channel and its secrets. Its deliveries stay in
// the log; pending ones are dropped.
func (s *Service) DeleteChannel(ctx context.Context, id string) (Channel, error) {
	if id == WebPushChannel {
		return Channel{}, errBuiltin
	}
	c, err := s.Channel(ctx, id)
	if err != nil {
		return Channel{}, err
	}
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM notify_channels WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET state = 'dropped', next_at = NULL, last_error = 'The channel was deleted.', updated_at = ?
			WHERE channel_id = ? AND state = 'pending'`, s.now().UnixMilli(), id)
		return err
	})
	if err != nil {
		return Channel{}, err
	}
	fields := make([]string, 0, len(secretFields[c.Kind]))
	for f := range secretFields[c.Kind] {
		fields = append(fields, f)
	}
	s.deleteSecrets(ctx, id, fields)
	s.dropChannel(ctx, id)
	return c, nil
}

// deleteSecrets removes a channel's secrets from the vault.
func (s *Service) deleteSecrets(ctx context.Context, id string, fields []string) {
	if s.vault == nil {
		return
	}
	for _, f := range fields {
		if err := s.vault.Delete(ctx, secretName(id, f)); err != nil && !errors.Is(err, vault.ErrNotFound) {
			s.log.Warn("notify: deleting a channel secret failed", "channel", id, "field", f, "err", err)
		}
	}
}
