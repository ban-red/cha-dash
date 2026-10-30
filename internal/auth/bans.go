package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"dash.cha.sh/internal/store"
)

// BanAfter wrong passwords in a row from one client address ban it. The ban
// stays until someone clears it: an admin in Settings → Users, or
// `chadash bans clear` on the hub's host. A banned address can't sign in by
// any method (password or passkey); sessions it already has keep working,
// so an admin who mistypes from another tab can still clear it.
//
// The count is per exact address and lives in memory: a restart forgets
// strikes, never bans. A sign-in that works clears only the strikes the
// address earned on that username, so signing in to your own account
// doesn't reset the count of guesses at someone else's.
const BanAfter = 5

// ErrBanNotFound is Unban's answer for an address that isn't banned.
var ErrBanNotFound = errors.New("auth: that address isn't banned")

// Ban is one banned address.
type Ban struct {
	IP       string    `json:"ip"`
	BannedAt time.Time `json:"banned_at"`
	Failures int       `json:"failures"`
}

// maxStrikes bounds the strike map if many addresses fail once each.
const maxStrikes = 4096

// Banned reports whether ip may not sign in.
func (s *Service) Banned(ctx context.Context, ip netip.Addr) (bool, error) {
	var n int
	err := s.st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM ip_bans WHERE ip = ?`, banKey(ip)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("auth: bans: %w", err)
	}
	return n > 0, nil
}

// strike counts a wrong password for username from ip and reports whether
// that banned it. The ban counts every username the address got wrong.
func (s *Service) strike(ctx context.Context, ip netip.Addr, username string) (bool, error) {
	key := banKey(ip)
	s.banMu.Lock()
	if s.strikes == nil {
		s.strikes = map[string]map[string]int{}
	}
	if _, ok := s.strikes[key]; !ok && len(s.strikes) >= maxStrikes {
		// Forget the addresses with one strike first, so filling the map
		// from many addresses doesn't wipe a patient guesser's count. Only
		// when every address has more is everyone forgotten; each still
		// needs BanAfter wrong passwords of its own, and the per-client rate
		// limit still applies.
		for k, m := range s.strikes {
			if total(m) <= 1 {
				delete(s.strikes, k)
			}
		}
		if len(s.strikes) >= maxStrikes {
			clear(s.strikes)
		}
	}
	if s.strikes[key] == nil {
		s.strikes[key] = map[string]int{}
	}
	s.strikes[key][username]++
	n := total(s.strikes[key])
	if n >= BanAfter {
		delete(s.strikes, key)
	}
	s.banMu.Unlock()
	if n < BanAfter {
		return false, nil
	}
	_, err := s.st.Writer().ExecContext(ctx,
		`INSERT INTO ip_bans (ip, banned_at, failures) VALUES (?, ?, ?) ON CONFLICT (ip) DO NOTHING`,
		key, s.now().UnixMilli(), n)
	if err != nil {
		return false, fmt.Errorf("auth: ban %s: %w", key, err)
	}
	s.log.Warn("client banned after too many wrong passwords; clear it in Settings → Users or with chadash bans clear", "client", key, "failures", n)
	return true, nil
}

func total(m map[string]int) (n int) {
	for _, c := range m {
		n += c
	}
	return n
}

// forgive clears ip's strikes on username after a sign-in to it that
// worked, and all of them when username is empty (an unban).
func (s *Service) forgive(ip netip.Addr, username string) {
	key := banKey(ip)
	s.banMu.Lock()
	if username == "" {
		delete(s.strikes, key)
	} else if m := s.strikes[key]; m != nil {
		delete(m, username)
		if len(m) == 0 {
			delete(s.strikes, key)
		}
	}
	s.banMu.Unlock()
}

// ListBans returns every banned address, newest first.
func (s *Service) ListBans(ctx context.Context) ([]Ban, error) { return ListBans(ctx, s.st) }

// Unban clears ip's ban. ip is an address as ListBans prints it.
func (s *Service) Unban(ctx context.Context, ip string) error {
	a, err := Unban(ctx, s.st, ip)
	if err != nil {
		return err
	}
	s.forgive(a, "")
	s.log.Info("client ban cleared", "client", banKey(a))
	return nil
}

// ListBans reads the ban list from st directly, for `chadash bans` on a
// hub that may not be running.
func ListBans(ctx context.Context, st *store.Store) ([]Ban, error) {
	rows, err := st.Reader().QueryContext(ctx, `SELECT ip, banned_at, failures FROM ip_bans ORDER BY banned_at DESC, ip`)
	if err != nil {
		return nil, fmt.Errorf("auth: bans: %w", err)
	}
	defer rows.Close()
	out := []Ban{}
	for rows.Next() {
		var b Ban
		var at int64
		if err := rows.Scan(&b.IP, &at, &b.Failures); err != nil {
			return nil, fmt.Errorf("auth: bans: %w", err)
		}
		b.BannedAt = time.UnixMilli(at).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// Unban clears ip's ban in st directly and returns the address it cleared.
// A running hub sees it on the next sign-in: bans are read from the
// database every time.
func Unban(ctx context.Context, st *store.Store, ip string) (netip.Addr, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return netip.Addr{}, &ValidationError{"that isn't an IP address"}
	}
	res, err := st.Writer().ExecContext(ctx, `DELETE FROM ip_bans WHERE ip = ?`, banKey(a))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("auth: unban: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return netip.Addr{}, ErrBanNotFound
	}
	return a, nil
}

// banKey is how an address is stored: unmapped and without a zone, so
// ::ffff:192.0.2.1 and 192.0.2.1 are one client.
func banKey(a netip.Addr) string {
	return a.Unmap().WithZone("").String()
}
