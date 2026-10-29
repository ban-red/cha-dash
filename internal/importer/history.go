package importer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"dash.cha.sh/internal/store"
)

// HistoryEntry is one row of the imports table: an applied import that
// changed something.
type HistoryEntry struct {
	ID        string   `json:"id"`
	Source    string   `json:"source"`
	Actor     string   `json:"actor,omitempty"`
	CreatedAt int64    `json:"created_at"`
	Files     []string `json:"files"`
	Target    string   `json:"target"`
	BoardID   string   `json:"board_id,omitempty"`
	Created   Created  `json:"created"`
	Counts    Counts   `json:"counts"`
	// RevertedAt is set once Revert ran; Reverted says what it did.
	RevertedAt int64           `json:"reverted_at,omitempty"`
	Reverted   json.RawMessage `json:"reverted,omitempty"`
}

// Created is what an import made, by ID: what Revert may remove.
type Created struct {
	// Board is set when the import made the board.
	Board     string   `json:"board,omitempty"`
	Instances []string `json:"instances"`
	Secrets   []string `json:"secrets"`
	// Items are the layout items the import added to the board.
	Items []string `json:"items"`
}

// Counts are an import's numbers, for History's list.
type Counts struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Tiles     int `json:"tiles"`
	Instances int `json:"instances"`
	Secrets   int `json:"secrets"`
}

func newImportID() string { return "imp_" + strings.ToLower(rand.Text()) }

func (s *Service) writeHistory(ctx context.Context, tx *sql.Tx, res *Result, w *work, c Choices, m Meta) (string, error) {
	p := &res.Plan
	cr := Created{Instances: []string{}, Secrets: []string{}, Items: w.items}
	if cr.Items == nil {
		cr.Items = []string{}
	}
	if w.board.action == ActionCreate {
		cr.Board = p.Board.ID
	}
	n := Counts{Tiles: len(w.items), Instances: len(p.Instances), Secrets: len(p.Secrets)}
	for _, ch := range w.changes {
		switch ch.Action {
		case ActionCreate:
			n.Created++
			switch ch.Kind {
			case "instance":
				cr.Instances = append(cr.Instances, ch.ID)
			case "secret":
				cr.Secrets = append(cr.Secrets, ch.ID)
			}
		case ActionUpdate:
			n.Updated++
		}
	}
	target := c.Target
	if target == "" {
		target = TargetNew
	}
	files := m.Files
	if files == nil {
		files = []string{}
	}
	id := newImportID()
	fj, _ := json.Marshal(files)
	cj, _ := json.Marshal(cr)
	nj, _ := json.Marshal(n)
	_, err := tx.ExecContext(ctx, `INSERT INTO imports (id, source, actor, created_at, files, target, board_id, created, counts) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, p.Source, sql.NullString{String: m.UserID, Valid: m.UserID != ""}, store.NowMillis(), string(fj), target,
		sql.NullString{String: p.Board.ID, Valid: p.Board.ID != ""}, string(cj), string(nj))
	return id, err
}

// History lists applied imports, newest first (at most limit).
func (s *Service) History(ctx context.Context, limit int) ([]HistoryEntry, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, source, COALESCE(actor, ''), created_at, files, target, COALESCE(board_id, ''), created, counts, COALESCE(reverted_at, 0), COALESCE(reverted, '')
		FROM imports ORDER BY created_at DESC, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Entry returns one History row, or ErrNoImport.
func (s *Service) Entry(ctx context.Context, id string) (HistoryEntry, error) {
	return s.entry(ctx, s.st.Reader(), id)
}

// ErrNoImport is an import ID History doesn't have.
var ErrNoImport = errors.New("importer: no such import")

func (s *Service) entry(ctx context.Context, q querier, id string) (HistoryEntry, error) {
	row := q.QueryRowContext(ctx, `SELECT id, source, COALESCE(actor, ''), created_at, files, target, COALESCE(board_id, ''), created, counts, COALESCE(reverted_at, 0), COALESCE(reverted, '')
		FROM imports WHERE id = ?`, id)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNoImport
	}
	return e, err
}

func scanEntry(r interface{ Scan(...any) error }) (HistoryEntry, error) {
	var e HistoryEntry
	var files, created, counts, reverted string
	if err := r.Scan(&e.ID, &e.Source, &e.Actor, &e.CreatedAt, &files, &e.Target, &e.BoardID, &created, &counts, &e.RevertedAt, &reverted); err != nil {
		return e, err
	}
	if err := errors.Join(json.Unmarshal([]byte(files), &e.Files), json.Unmarshal([]byte(created), &e.Created), json.Unmarshal([]byte(counts), &e.Counts)); err != nil {
		return e, err
	}
	if reverted != "" {
		e.Reverted = json.RawMessage(reverted)
	}
	return e, nil
}

// resembles finds an instance the hub already has, outside the plan, that
// in looks like: the same integration reading the same URL, or one with the
// name the source asked for (when in had to be renamed).
func resembles(ctx context.Context, q querier, in *Instance, asked string, planned map[string]bool) (string, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, config FROM integration_instances WHERE integration = ? ORDER BY id`, in.Integration)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	want := targetOf(in.Config)
	var byName string
	for rows.Next() {
		var id, name, cfg string
		if err := rows.Scan(&id, &name, &cfg); err != nil {
			return "", "", err
		}
		if planned[id] {
			continue
		}
		var m map[string]any
		if want != "" && json.Unmarshal([]byte(cfg), &m) == nil && sameTarget(targetOf(m), want) {
			return id, "url", rows.Close()
		}
		if name == asked && in.Name != asked && byName == "" {
			byName = id
		}
	}
	if byName != "" {
		return byName, "name", rows.Err()
	}
	return "", "", rows.Err()
}

func targetOf(cfg map[string]any) string {
	for _, k := range []string{"url", "endpoint"} {
		if v, ok := cfg[k].(string); ok && v != "" && !strings.HasPrefix(v, "${") {
			return v
		}
	}
	return ""
}

// sameTarget compares URLs ignoring case in the scheme and host, a trailing
// slash and a default port.
func sameTarget(a, b string) bool {
	norm := func(s string) string {
		u, err := url.Parse(strings.TrimRight(strings.TrimSpace(s), "/"))
		if err != nil {
			return s
		}
		port := u.Port()
		if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
			port = ""
		}
		return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port + u.Path
	}
	return a != "" && norm(a) == norm(b)
}
