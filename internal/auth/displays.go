package auth

// Displays (v0.5, wave 33; docs/plan/15-v0.5.md, "Wave 33: how displays
// work"). A display is a wall tablet or a TV that shows one board with
// nobody at it, so it can't hold a session that needs a passkey. It isn't
// an API token either: a token acts as its user with that user's role, and
// a display has neither. This file stores displays and checks their
// secrets; which routes a display may call is internal/authz's table, and
// what its board shows is the server's.
//
// A display's secret is `cdd_<id>_<secret>`, shaped like an API token's
// (26 and 52 base32 characters) and stored the same way, as a SHA-256. A
// device presents it once, from the link's fragment, to
// POST /api/display/claim, which sets the chadash_display cookie on
// Path=/api/display: the browser never sends it anywhere else. A script may
// send it as `Authorization: Bearer cdd_…` to the same routes.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// DisplayPrefix starts every display's secret.
const DisplayPrefix = "cdd_"

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
)

// EventDisplayRevoked: a display was revoked. Subject is its ID, UserID
// who revoked it. Its sockets close at once.
const EventDisplayRevoked EventKind = "display.revoked"

// Quiet is a display's quiet hours, HH:MM to HH:MM in the hub's local time;
// End may be earlier than Start (the hours run past midnight).
type Quiet struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Display is a display as the displays API lists it. It never carries the
// secret or its hash.
type Display struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	BoardID    string     `json:"-"`
	BoardName  string     `json:"-"`
	Quiet      *Quiet     `json:"quiet"`
	CreatedBy  string     `json:"created_by"` // username; "" once the account is gone
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	LastSeenIP string     `json:"last_seen_ip,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// DisplayRef is the display behind a request, on its Identity.
type DisplayRef struct {
	ID      string
	Name    string
	BoardID string
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
// returns it with its secret, which is never stored or shown again.
// ErrNoSuchBoard when the board doesn't exist.
func (s *Service) CreateDisplay(ctx context.Context, userID, name, boardID string, quiet *Quiet) (Display, string, error) {
	name, err := validateDisplayName(name)
	if err != nil {
		return Display{}, "", err
	}
	if err := ValidateQuiet(quiet); err != nil {
		return Display{}, "", err
	}
	short := strings.ToLower(rand.Text())
	secret := DisplayPrefix + short + "_" + rand.Text() + rand.Text()
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
		_, err := tx.ExecContext(ctx, `INSERT INTO displays (id, name, board_id, token_hash, quiet_start, quiet_end, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, name, boardID, tokenHash(secret), qs, qe, by, now)
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
	return d, secret, nil
}

const displayQuery = `SELECT d.id, d.name, d.board_id, b.title, d.quiet_start, d.quiet_end, COALESCE(u.username, ''), d.created_at, d.last_seen_at, d.last_seen_ip, d.revoked_at
	FROM displays d JOIN boards b ON b.id = d.board_id LEFT JOIN users u ON u.id = d.created_by`

func scanDisplay(sc scanner) (Display, error) {
	var d Display
	var qs, qe, ip sql.NullString
	var created int64
	var seen, revoked sql.NullInt64
	if err := sc.Scan(&d.ID, &d.Name, &d.BoardID, &d.BoardName, &qs, &qe, &d.CreatedBy, &created, &seen, &ip, &revoked); err != nil {
		return Display{}, err
	}
	if qs.Valid && qe.Valid {
		d.Quiet = &Quiet{Start: qs.String, End: qe.String}
	}
	d.CreatedAt = time.UnixMilli(created).UTC()
	d.LastSeenAt, d.RevokedAt = msTime(seen), msTime(revoked)
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

// UpdateDisplay renames a display (name non-nil) and sets or clears its
// quiet hours (setQuiet; quiet nil clears them). A revoked display is
// ErrDisplayNotFound.
func (s *Service) UpdateDisplay(ctx context.Context, id string, name *string, setQuiet bool, quiet *Quiet) (Display, error) {
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
	res, err := s.st.Writer().ExecContext(ctx, `UPDATE displays SET revoked_at = ?, revoked_by = ? WHERE id = ? AND revoked_at IS NULL`,
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

// AuthenticateDisplay checks a presented display secret and returns the
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
		name, board   string
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
	if subtle.ConstantTimeCompare(tokenHash(presented), stored) != 1 {
		return Identity{}, ErrBadDisplay
	}
	if revoked.Valid {
		return Identity{}, ErrDisplayRevoked
	}
	now := s.now()
	if !seen.Valid || now.Sub(time.UnixMilli(seen.Int64)) >= tokenLastUsedEvery {
		if _, err := s.st.Writer().ExecContext(ctx, `UPDATE displays SET last_seen_at = ?, last_seen_ip = ? WHERE id = ? AND (last_seen_at IS NULL OR last_seen_at <= ?)`,
			now.UnixMilli(), ip, id, now.Add(-tokenLastUsedEvery).UnixMilli()); err != nil {
			s.log.Warn("could not record a display's use", "display", id, "err", err)
		}
	}
	return Identity{Display: &DisplayRef{ID: id, Name: name, BoardID: board}, IP: ip}, nil
}

// DisplayStillValid reports whether the display an identity read as is
// still there and not revoked: a display's socket asks every minute.
func (s *Service) DisplayStillValid(ctx context.Context, id Identity) (bool, error) {
	if id.Display == nil {
		return false, nil
	}
	var one int
	err := s.st.Reader().QueryRowContext(ctx, `SELECT 1 FROM displays WHERE id = ? AND revoked_at IS NULL`, id.Display.ID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// DisplayCredential returns the display secret r carries: a
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
// Identity, or answers 401 invalid_display (429 once a client has failed
// too often, on the same budget as bad API tokens). A session cookie or an
// API token counts for nothing here.
func (s *Service) RequireDisplay(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := DisplayCredential(r)
		if !ok {
			httpx.Say(w, http.StatusUnauthorized, "invalid_display", msg.New("displays.signed_out"))
			return
		}
		id, ok := s.checkDisplay(w, r, presented)
		if !ok {
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// checkDisplay authenticates presented for r, answering the refusal itself.
func (s *Service) checkDisplay(w http.ResponseWriter, r *http.Request, presented string) (Identity, bool) {
	ip := s.proxies.ClientIP(r)
	key := "token|" + httpx.ClientKey(ip)
	if wait := s.tokenFailures.Wait(key); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		httpx.Say(w, http.StatusTooManyRequests, "rate_limited", msg.New("auth.token_rate_limited"))
		return Identity{}, false
	}
	id, err := s.AuthenticateDisplay(r.Context(), presented, ip.String())
	switch {
	case err == nil:
		return id, true
	case errors.Is(err, ErrDisplayRevoked):
		s.tokenFailures.Allow(key)
		s.clearDisplayCookie(w, r)
		httpx.Say(w, http.StatusUnauthorized, "invalid_display", msg.New("displays.revoked"))
	case errors.Is(err, ErrBadDisplay):
		s.tokenFailures.Allow(key)
		s.clearDisplayCookie(w, r)
		httpx.Say(w, http.StatusUnauthorized, "invalid_display", msg.New("displays.signed_out"))
	default:
		s.log.Error("display lookup failed", "err", err)
		httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
	}
	return Identity{}, false
}

// HandleDisplayClaim serves POST /api/display/claim {"token"}: a device
// presents the secret from its link once, and gets the display cookie
// (204). The secret travels in the body, never in a URL.
func (s *Service) HandleDisplayClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if _, ok := s.checkDisplay(w, r, req.Token); !ok {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     DisplayCookie,
		Value:    req.Token,
		Path:     DisplayPath,
		MaxAge:   int(displayCookieAge / time.Second),
		Expires:  s.now().Add(displayCookieAge),
		HttpOnly: true,
		Secure:   s.proxies.IsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
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
