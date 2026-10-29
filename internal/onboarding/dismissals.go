package onboarding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/store"
)

// Discovery leads the owner said no to, hub-wide (discovery_dismissals,
// migration 00016). A lead's key is the web's: its integration and
// normalized URL, or its integration and resource ID. Label discoveries,
// which are tiles for one board, keep their per-board dismissals in
// internal/boards.
//
//	GET  /api/discover/dismissed           {dismissed: [key]}
//	POST /api/discover/dismiss  {keys}  →  the same; idempotent; the newest 1000 are kept
//	POST /api/discover/restore  {keys}  →  the same
const (
	maxDismissKeys = 200
	keepDismissed  = 1000
	maxKeyLen      = 512
)

// Dismissed lists the dismissed lead keys, newest first.
func (s *Service) Dismissed(ctx context.Context) ([]string, error) {
	rows, err := s.o.Store.Reader().QueryContext(ctx, `SELECT key FROM discovery_dismissals ORDER BY dismissed_at DESC, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func cleanKeys(keys []string) ([]string, error) {
	if len(keys) == 0 || len(keys) > maxDismissKeys {
		return nil, fmt.Errorf("%w: send 1 to %d keys", ErrInvalid, maxDismissKeys)
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || len(k) > maxKeyLen {
			return nil, fmt.Errorf("%w: a key is empty or too long", ErrInvalid)
		}
		out = append(out, k)
	}
	return out, nil
}

// Dismiss remembers keys as dismissed.
func (s *Service) Dismiss(ctx context.Context, keys []string, by string) error {
	keys, err := cleanKeys(keys)
	if err != nil {
		return err
	}
	now := store.NowMillis()
	return s.o.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := tx.ExecContext(ctx, `INSERT INTO discovery_dismissals (key, dismissed_at, dismissed_by) VALUES (?, ?, ?)
				ON CONFLICT (key) DO NOTHING`, k, now, sql.NullString{String: by, Valid: by != ""}); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM discovery_dismissals WHERE key NOT IN
			(SELECT key FROM discovery_dismissals ORDER BY dismissed_at DESC, key LIMIT ?)`, keepDismissed)
		return err
	})
}

// Restore forgets dismissals: the leads are offered again.
func (s *Service) Restore(ctx context.Context, keys []string) error {
	keys, err := cleanKeys(keys)
	if err != nil {
		return err
	}
	return s.o.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := tx.ExecContext(ctx, `DELETE FROM discovery_dismissals WHERE key = ?`, k); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) handleDismissed(w http.ResponseWriter, r *http.Request) {
	keys, err := s.Dismissed(r.Context())
	if err != nil {
		s.o.Log.Error("onboarding: reading dismissals failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string][]string{"dismissed": keys})
}

func (s *Service) handleDismissal(dismiss bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Keys []string `json:"keys"`
		}
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}
		var by string
		if id, ok := auth.FromContext(r.Context()); ok {
			by = id.User.ID
		}
		var err error
		action := "discover.restore"
		if dismiss {
			action = "discover.dismiss"
			err = s.Dismiss(r.Context(), req.Keys, by)
		} else {
			err = s.Restore(r.Context(), req.Keys)
		}
		if errors.Is(err, ErrInvalid) {
			httpx.Error(w, http.StatusUnprocessableEntity, "invalid", err.Error())
			return
		}
		if err != nil {
			s.o.Log.Error("onboarding: "+action+" failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		s.Audit.Change(r, action, "settings:discovery", map[string]any{"keys": len(req.Keys)}, authz.T0, nil)
		s.handleDismissed(w, r)
	}
}
