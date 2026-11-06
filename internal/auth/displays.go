package auth

// Displays (v0.5, wave 33; docs/plan/15-v0.5.md, "Wave 33: how displays
// work"). A display is a wall tablet or a TV that shows one board with
// nobody at it, so it can't hold a session that needs a passkey. It isn't
// an API token either: a token acts as its user with that user's role, and
// a display has neither. This file stores displays and checks their
// secrets; which routes a display may call is internal/authz's table, and
// what its board shows is the server's.
//
// A display's link carries a claim code, `cdc_<id>_<secret>`, good once and
// for ClaimTTL. A device presents it from the link's fragment to
// POST /api/display/claim, which swaps it for the display's device secret,
// `cdd_<id>_<secret>`, and sets that in the chadash_display cookie on
// Path=/api/display: the browser never sends it anywhere else, and nobody
// ever sees it. Both are shaped like an API token (26 and 52 base32
// characters) and stored the same way, as a SHA-256. A new link for a
// display signs out the device that had it. A script may send the device
// secret as `Authorization: Bearer cdd_…` to the same routes.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// DisplayPrefix starts every display's device secret.
const DisplayPrefix = "cdd_"

// ClaimPrefix starts every display's claim code, the one-time part of its link.
const ClaimPrefix = "cdc_"

// ClaimTTL is how long a display's link can be claimed.
const ClaimTTL = 24 * time.Hour

// displayIDPrefix starts a display's public ID.
const displayIDPrefix = "dsp_"

// DisplayCookie is the cookie a claimed device carries, on DisplayPath only.
const DisplayCookie = "chadash_display"

// DisplayPath is the path the display cookie is scoped to: every route a
// display may call is under it.
const DisplayPath = "/api/display"

// displayCookieAge is the cookie's lifetime, the most browsers keep one.
const displayCookieAge = 400 * 24 * time.Hour

// Display errors. A presented secret that fails any way is ErrBadDisplay,
// unless it matched: only then does the client learn it was revoked.
var (
	ErrBadDisplay      = errors.New("auth: invalid display credential")
	ErrDisplayRevoked  = errors.New("auth: display revoked")
	ErrDisplayNotFound = errors.New("auth: no such display")
	ErrNoSuchBoard     = errors.New("auth: no such board")
	// ErrBadClaim: the claim code is unknown, wrong, used, expired or its
	// display revoked; the client learns nothing more.
	ErrBadClaim = errors.New("auth: invalid display link")
)

// EventDisplayRevoked: a display was revoked, or its link was claimed
// again (a new device took it over). Subject is its ID, UserID who revoked
// it. Its sockets close at once.
const EventDisplayRevoked EventKind = "display.revoked"

// Quiet is a display's quiet hours, HH:MM to HH:MM in the hub's local time;
// End may be earlier than Start (the hours run past midnight).
type Quiet struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Style is how a display shows its board: StyleWall, StyleTV or StyleInk.
type Style string

// The display styles, as displays.style allows them.
const (
	StyleWall Style = "wall"
	StyleTV   Style = "tv"
	StyleInk  Style = "ink"
)

// ParseStyle returns the style named s; "" is StyleWall.
func ParseStyle(s string) (Style, error) {
	switch Style(s) {
	case "", StyleWall:
		return StyleWall, nil
	case StyleTV, StyleInk:
		return Style(s), nil
	}
	return "", &ValidationError{msg.New("displays.style")}
}

// Display is a display as the displays API lists it. It never carries the
// secret or its hash.
type Display struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	BoardID    string     `json:"-"`
	BoardName  string     `json:"-"`
	Quiet      *Quiet     `json:"quiet"`
	Style      Style      `json:"style"`
	CreatedBy  string     `json:"created_by"` // username; "" once the account is gone
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	LastSeenIP string     `json:"last_seen_ip,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	// LinkExpiresAt is when its unclaimed link stops working; nil once
	// claimed.
	LinkExpiresAt *time.Time `json:"link_expires_at,omitempty"`
}

// DisplayRef is the display behind a request, on its Identity.
type DisplayRef struct {
	ID      string
	Name    string
	BoardID string
	// hash is the device secret's, so a socket notices when a new link
	// replaced it.
	hash []byte
}

var displaySecretRE = regexp.MustCompile(`^cdd_([a-z2-7]{26})_([A-Z2-7]{52})$`)

var quietRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidateQuiet checks quiet hours: nil (none), or two HH:MM times that
// differ.
func ValidateQuiet(q *Quiet) error {
	if q == nil {
		return nil
	}
	if !quietRE.MatchString(q.Start) || !quietRE.MatchString(q.End) || q.Start == q.End {
		return &ValidationError{msg.New("displays.quiet")}
	}
	return nil
}

func validateDisplayName(name string) (string, error) {
	name, err := ValidateTokenName(name)
	if err != nil {
		return "", &ValidationError{msg.New("displays.name")}
	}
	return name, nil
}

// CreateDisplay stores a new display of boardID, made by userID, and
// returns it with its claim code, which is never stored or shown again and
// works once, for ClaimTTL. ErrNoSuchBoard when the board doesn't exist.
func (s *Service) CreateDisplay(ctx context.Context, userID, name, boardID string, style Style, quiet *Quiet) (Display, string, error) {
	name, err := validateDisplayName(name)
	if err != nil {
		return Display{}, "", err
	}
	if style, err = ParseStyle(string(style)); err != nil {
		return Display{}, "", err
	}
	if err := ValidateQuiet(quiet); err != nil {
		return Display{}, "", err
	}
	short := strings.ToLower(rand.Text())
	// The device secret nobody holds until the link is claimed.
	unclaimed := DisplayPrefix + short + "_" + rand.Text() + rand.Text()
	code := ClaimPrefix + short + "_" + rand.Text() + rand.Text()
	id := displayIDPrefix + short
	now := s.now().UnixMilli()
	var qs, qe sql.NullString
	if quiet != nil {
		qs, qe = sql.NullString{String: quiet.Start, Valid: true}, sql.NullString{String: quiet.End, Valid: true}
	}
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM boards WHERE id = ?`, boardID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoSuchBoard
			}
			return err
		}
		var by sql.NullString
		if userID != "" {
			by = sql.NullString{String: userID, Valid: true}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO displays (id, name, board_id, token_hash, claim_hash, claim_expires_at, quiet_start, quiet_end, style, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, name, boardID, tokenHash(unclaimed), tokenHash(code), now+ClaimTTL.Milliseconds(), qs, qe, string(style), by, now)
		return err
	})
	if err != nil {
		return Display{}, "", err
	}
	d, err := s.GetDisplay(ctx, id)
	if err != nil {
		return Display{}, "", err
	}
	s.log.Info("display created", "display", id, "board", boardID)
	return d, code, nil
}

// NewDisplayLink gives a display a new claim code, for ClaimTTL, in place
// of any it had. The device holding it keeps working until the new link is
// claimed. A revoked display is ErrDisplayNotFound.
func (s *Service) NewDisplayLink(ctx context.Context, id string) (Display, string, error) {
	short, ok := strings.CutPrefix(id, displayIDPrefix)
	if !ok {
		return Display{}, "", ErrDisplayNotFound
	}
	code := ClaimPrefix + short + "_" + rand.Text() + rand.Text()
	res, err := s.st.Writer().ExecContext(ctx, `UPDATE displays SET claim_hash = ?, claim_expires_at = ? WHERE id = ? AND revoked_at IS NULL`,
		tokenHash(code), s.now().Add(ClaimTTL).UnixMilli(), id)
	if err != nil {
		return Display{}, "", fmt.Errorf("auth: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Display{}, "", ErrDisplayNotFound
	}
	d, err := s.GetDisplay(ctx, id)
	return d, code, err
}

const displayQuery = `SELECT d.id, d.name, COALESCE(d.board_id, ''), COALESCE(b.title, ''), d.quiet_start, d.quiet_end, d.style, COALESCE(u.username, ''), d.created_at, d.last_seen_at, d.last_seen_ip, d.revoked_at, d.claim_expires_at
	FROM displays d LEFT JOIN boards b ON b.id = d.board_id LEFT JOIN users u ON u.id = d.created_by`

func scanDisplay(sc scanner) (Display, error) {
	var d Display
	var qs, qe, ip sql.NullString
	var style string
	var created int64
	var seen, revoked, claim sql.NullInt64
	if err := sc.Scan(&d.ID, &d.Name, &d.BoardID, &d.BoardName, &qs, &qe, &style, &d.CreatedBy, &created, &seen, &ip, &revoked, &claim); err != nil {
		return Display{}, err
	}
	if qs.Valid && qe.Valid {
		d.Quiet = &Quiet{Start: qs.String, End: qe.String}
	}
	d.Style = Style(style)
	d.CreatedAt = time.UnixMilli(created).UTC()
	d.LastSeenAt, d.RevokedAt, d.LinkExpiresAt = msTime(seen), msTime(revoked), msTime(claim)
	d.LastSeenIP = ip.String
	return d, nil
}

// ListDisplays returns the displays not revoked, newest first.
func (s *Service) ListDisplays(ctx context.Context) ([]Display, error) {
	rows, err := s.st.Reader().QueryContext(ctx, displayQuery+` WHERE d.revoked_at IS NULL ORDER BY d.created_at DESC, d.id`)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	defer rows.Close()
	out := []Display{}
	for rows.Next() {
		d, err := scanDisplay(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDisplay returns one display, revoked or not, or ErrDisplayNotFound.
func (s *Service) GetDisplay(ctx context.Context, id string) (Display, error) {
	d, err := scanDisplay(s.st.Reader().QueryRowContext(ctx, displayQuery+` WHERE d.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Display{}, ErrDisplayNotFound
	}
	return d, err
}

// UpdateDisplay renames a display (name non-nil), changes its style (style
// non-nil) and sets or clears its quiet hours (setQuiet; quiet nil clears
// them). A revoked display is ErrDisplayNotFound.
func (s *Service) UpdateDisplay(ctx context.Context, id string, name *string, style *Style, setQuiet bool, quiet *Quiet) (Display, error) {
	if style != nil {
		st, err := ParseStyle(string(*style))
		if err != nil {
			return Display{}, err
		}
		style = &st
	}
	if name != nil {
		n, err := validateDisplayName(*name)
		if err != nil {
			return Display{}, err
		}
		name = &n
	}
	if setQuiet {
		if err := ValidateQuiet(quiet); err != nil {
			return Display{}, err
		}
	}
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM displays WHERE id = ? AND revoked_at IS NULL`, id).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrDisplayNotFound
			}
			return err
		}
		if name != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE displays SET name = ? WHERE id = ?`, *name, id); err != nil {
				return err
			}
		}
		if style != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE displays SET style = ? WHERE id = ?`, string(*style), id); err != nil {
				return err
			}
		}
		if setQuiet {
			var qs, qe sql.NullString
			if quiet != nil {
				qs, qe = sql.NullString{String: quiet.Start, Valid: true}, sql.NullString{String: quiet.End, Valid: true}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE displays SET quiet_start = ?, quiet_end = ? WHERE id = ?`, qs, qe, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Display{}, err
	}
	return s.GetDisplay(ctx, id)
}

// RevokeDisplay revokes a display at once; by is the user who did it.
// Revoking one already revoked changes nothing and reports false. Its
// sockets close (EventDisplayRevoked).
func (s *Service) RevokeDisplay(ctx context.Context, id, by string) (Display, bool, error) {
	var byv sql.NullString
	if by != "" {
		byv = sql.NullString{String: by, Valid: true}
	}
	res, err := s.st.Writer().ExecContext(ctx, `UPDATE displays SET revoked_at = ?, revoked_by = ?, claim_hash = NULL, claim_expires_at = NULL WHERE id = ? AND revoked_at IS NULL`,
		s.now().UnixMilli(), byv, id)
	if err != nil {
		return Display{}, false, fmt.Errorf("auth: %w", err)
	}
	n, _ := res.RowsAffected()
	d, err := s.GetDisplay(ctx, id)
	if err != nil {
		return Display{}, false, err
	}
	if n == 1 {
		s.log.Info("display revoked", "display", id)
		s.emit(Event{Kind: EventDisplayRevoked, UserID: by, Subject: id})
	}
	return d, n == 1, nil
}

// dummyDisplayHash is compared against when a display's ID is unknown.
var dummyDisplayHash = tokenHash(DisplayPrefix + strings.Repeat("a", 26) + "_" + strings.Repeat("A", 52))

var claimCodeRE = regexp.MustCompile(`^cdc_([a-z2-7]{26})_([A-Z2-7]{52})$`)

// AuthenticateDisplay checks a presented device secret and returns the
// identity it reads as: no user, no session, only the display. Errors:
// ErrBadDisplay (malformed, unknown or a wrong secret), and, only when the
// secret matched, ErrDisplayRevoked. ip is recorded as where it was last
// seen, at most once a minute.
func (s *Service) AuthenticateDisplay(ctx context.Context, presented, ip string) (Identity, error) {
	m := displaySecretRE.FindStringSubmatch(presented)
	if m == nil {
		return Identity{}, ErrBadDisplay
	}
	id := displayIDPrefix + m[1]
	var (
		stored        []byte
		name          string
		board         sql.NullString
		seen, revoked sql.NullInt64
	)
	err := s.st.Reader().QueryRowContext(ctx, `SELECT token_hash, name, board_id, last_seen_at, revoked_at FROM displays WHERE id = ?`, id).
		Scan(&stored, &name, &board, &seen, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		subtle.ConstantTimeCompare(tokenHash(presented), dummyDisplayHash)
		return Identity{}, ErrBadDisplay
	}
	if err != nil {
		return Identity{}, fmt.Errorf("auth: %w", err)
	}
	hash := tokenHash(presented)
	if subtle.ConstantTimeCompare(hash, stored) != 1 {
		return Identity{}, ErrBadDisplay
	}
	if revoked.Valid || !board.Valid {
		return Identity{}, ErrDisplayRevoked
	}
	now := s.now()
	if !seen.Valid || now.Sub(time.UnixMilli(seen.Int64)) >= tokenLastUsedEvery {
		if _, err := s.st.Writer().ExecContext(ctx, `UPDATE displays SET last_seen_at = ?, last_seen_ip = ? WHERE id = ? AND (last_seen_at IS NULL OR last_seen_at <= ?)`,
			now.UnixMilli(), ip, id, now.Add(-tokenLastUsedEvery).UnixMilli()); err != nil {
			s.log.Warn("could not record a display's use", "display", id, "err", err)
		}
	}
	return Identity{Display: &DisplayRef{ID: id, Name: name, BoardID: board.String, hash: hash}, IP: ip}, nil
}

// ClaimDisplay swaps a display's claim code for a new device secret, once:
// the code is cleared, and the device that held the display before is
// signed out (EventDisplayRevoked closes its sockets). ErrBadClaim for a
// code that is malformed, unknown, wrong, used or expired, or a display
// revoked.
func (s *Service) ClaimDisplay(ctx context.Context, code, ip string) (Identity, string, error) {
	m := claimCodeRE.FindStringSubmatch(code)
	if m == nil {
		return Identity{}, "", ErrBadClaim
	}
	id := displayIDPrefix + m[1]
	hash := tokenHash(code)
	secret := DisplayPrefix + m[1] + "_" + rand.Text() + rand.Text()
	var name, board string
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var stored []byte
		var expires sql.NullInt64
		var b sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT claim_hash, claim_expires_at, name, board_id FROM displays WHERE id = ? AND revoked_at IS NULL`, id).Scan(&stored, &expires, &name, &b)
		if errors.Is(err, sql.ErrNoRows) {
			subtle.ConstantTimeCompare(hash, dummyDisplayHash)
			return ErrBadClaim
		}
		if err != nil {
			return err
		}
		if stored == nil || subtle.ConstantTimeCompare(hash, stored) != 1 || !expires.Valid || expires.Int64 <= s.now().UnixMilli() || !b.Valid {
			return ErrBadClaim
		}
		board = b.String
		_, err = tx.ExecContext(ctx, `UPDATE displays SET token_hash = ?, claim_hash = NULL, claim_expires_at = NULL, last_seen_at = ?, last_seen_ip = ? WHERE id = ?`,
			tokenHash(secret), s.now().UnixMilli(), ip, id)
		return err
	})
	if err != nil {
		return Identity{}, "", err
	}
	s.log.Info("display link claimed", "display", id)
	s.emit(Event{Kind: EventDisplayRevoked, Subject: id})
	return Identity{Display: &DisplayRef{ID: id, Name: name, BoardID: board, hash: tokenHash(secret)}, IP: ip}, secret, nil
}

// DisplayStillValid reports whether the display an identity read as is
// still there, not revoked, and still on the device secret it read with:
// a display's socket asks every minute.
func (s *Service) DisplayStillValid(ctx context.Context, id Identity) (bool, error) {
	if id.Display == nil {
		return false, nil
	}
	var stored []byte
	err := s.st.Reader().QueryRowContext(ctx, `SELECT token_hash FROM displays WHERE id = ? AND revoked_at IS NULL AND board_id IS NOT NULL`, id.Display.ID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(stored, id.Display.hash) == 1, nil
}

// DisplayCredential returns the device secret r carries: a
// `Bearer cdd_…` header, else the display cookie.
func DisplayCredential(r *http.Request) (string, bool) {
	scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if ok && strings.EqualFold(scheme, "Bearer") {
		if value = strings.TrimSpace(value); strings.HasPrefix(value, DisplayPrefix) {
			return value, true
		}
	}
	if c, err := r.Cookie(DisplayCookie); err == nil && strings.HasPrefix(c.Value, DisplayPrefix) {
		return c.Value, true
	}
	return "", false
}

// RequireDisplay authenticates r's display credential and attaches its
// Identity, or answers 401 invalid_display. A secret that matches always
// gets through; only failures draw on the per-client budget (shared with
// bad API tokens), and once it's spent they get 429. So someone guessing
// behind the same address never locks a real display out. A session
// cookie or an API token counts for nothing here.
func (s *Service) RequireDisplay(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := DisplayCredential(r)
		if !ok {
			httpx.Say(w, http.StatusUnauthorized, "invalid_display", msg.New("displays.signed_out"))
			return
		}
		ip := s.proxies.ClientIP(r)
		id, err := s.AuthenticateDisplay(r.Context(), presented, ip.String())
		if err == nil {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
			return
		}
		if !errors.Is(err, ErrBadDisplay) && !errors.Is(err, ErrDisplayRevoked) {
			s.log.Error("display lookup failed", "err", err)
			httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
			return
		}
		if s.spendFailure(w, ip) {
			return
		}
		s.clearDisplayCookie(w, r)
		if errors.Is(err, ErrDisplayRevoked) {
			httpx.Say(w, http.StatusUnauthorized, "invalid_display", msg.New("displays.revoked"))
			return
		}
		httpx.Say(w, http.StatusUnauthorized, "invalid_display", msg.New("displays.signed_out"))
	})
}

// spendFailure draws a failure from ip's budget, answering 429 (and
// reporting true) once it's spent.
func (s *Service) spendFailure(w http.ResponseWriter, ip netip.Addr) bool {
	key := "token|" + httpx.ClientKey(ip)
	if wait := s.tokenFailures.Wait(key); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		httpx.Say(w, http.StatusTooManyRequests, "rate_limited", msg.New("auth.token_rate_limited"))
		return true
	}
	s.tokenFailures.Allow(key)
	return false
}

// HandleDisplayClaim answers POST /api/display/claim {"token"}: a device
// presents the claim code from its link, and gets the display's new device
// secret in its cookie (204). The code travels in the body, never in a
// URL. It returns the display's identity when the claim went through, for
// the caller to audit; ok false means it answered a refusal.
func (s *Service) HandleDisplayClaim(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	var req struct {
		Token string `json:"token"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return Identity{}, false
	}
	ip := s.proxies.ClientIP(r)
	key := "token|" + httpx.ClientKey(ip)
	if wait := s.tokenFailures.Wait(key); wait > 0 {
		// A claim is rare: guessing codes is what the budget is for.
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		httpx.Say(w, http.StatusTooManyRequests, "rate_limited", msg.New("auth.token_rate_limited"))
		return Identity{}, false
	}
	id, secret, err := s.ClaimDisplay(r.Context(), strings.TrimSpace(req.Token), ip.String())
	switch {
	case errors.Is(err, ErrBadClaim):
		s.tokenFailures.Allow(key)
		httpx.Say(w, http.StatusUnauthorized, "invalid_display", msg.New("displays.link_used"))
		return Identity{}, false
	case err != nil:
		s.log.Error("claiming a display failed", "err", err)
		httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
		return Identity{}, false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     DisplayCookie,
		Value:    secret,
		Path:     DisplayPath,
		MaxAge:   int(displayCookieAge / time.Second),
		Expires:  s.now().Add(displayCookieAge),
		HttpOnly: true,
		Secure:   s.proxies.IsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
	return id, true
}

func (s *Service) clearDisplayCookie(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(DisplayCookie); err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     DisplayCookie,
		Value:    "",
		Path:     DisplayPath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.proxies.IsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}
