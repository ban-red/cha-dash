package audit

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Query limits.
const (
	DefaultLimit = 100
	MaxLimit     = 500
)

// Filter selects events. Zero fields match everything.
type Filter struct {
	Actor  string // the actor's ID or name (case-insensitive)
	Action string // exact, or a prefix ending in "." or "*": "proxmox.", "settings.*"
	Target string
	Result string
	Since  time.Time // at or after
	Before time.Time // strictly before
	// Cursor continues a listing: the Next of the previous page.
	Cursor string
	Limit  int
}

// Page is one page of events, newest first.
type Page struct {
	Events []Event `json:"events"`
	Next   string  `json:"next,omitempty"`
}

// ErrBadCursor is returned for a cursor Query didn't make.
var ErrBadCursor = errors.New("audit: invalid cursor")

// Query returns events matching f, newest first. Next is set when there may
// be more.
func (l *Log) Query(ctx context.Context, f Filter) (Page, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)

	var where []string
	var args []any
	if f.Actor != "" {
		where = append(where, `(actor_id = ? OR actor = ? COLLATE NOCASE)`)
		args = append(args, f.Actor, f.Actor)
	}
	if a := f.Action; a != "" {
		if p, ok := strings.CutSuffix(a, "*"); ok || strings.HasSuffix(a, ".") {
			if !ok {
				p = a
			}
			// substr rather than LIKE, so _ and % in an action ID match themselves.
			where = append(where, `substr(action, 1, ?) = ?`)
			args = append(args, len(p), p)
		} else {
			where = append(where, `action = ?`)
			args = append(args, a)
		}
	}
	if f.Target != "" {
		where = append(where, `target = ?`)
		args = append(args, f.Target)
	}
	if f.Result != "" {
		where = append(where, `result = ?`)
		args = append(args, f.Result)
	}
	if !f.Since.IsZero() {
		where = append(where, `at >= ?`)
		args = append(args, f.Since.UnixMilli())
	}
	if !f.Before.IsZero() {
		where = append(where, `at < ?`)
		args = append(args, f.Before.UnixMilli())
	}
	if f.Cursor != "" {
		seq, err := strconv.ParseInt(strings.TrimPrefix(f.Cursor, "s"), 10, 64)
		if err != nil || !strings.HasPrefix(f.Cursor, "s") || seq < 1 {
			return Page{}, ErrBadCursor
		}
		where = append(where, `seq < ?`)
		args = append(args, seq)
	}
	q := `SELECT event, hash FROM audit_events`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := l.st.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("audit: query: %w", err)
	}
	defer rows.Close()
	page := Page{Events: []Event{}}
	for rows.Next() {
		var body, hash string
		if err := rows.Scan(&body, &hash); err != nil {
			return Page{}, err
		}
		var ev Event
		if err := json.Unmarshal([]byte(body), &ev); err != nil {
			return Page{}, fmt.Errorf("audit: stored event: %w", err)
		}
		ev.Hash = hash
		page.Events = append(page.Events, ev)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		page.Next = "s" + strconv.FormatInt(page.Events[limit-1].Seq, 10)
	}
	return page, nil
}

// Export writes every event as NDJSON, oldest first: each line is the
// stored canonical JSON with ,"hash":"…" spliced in before its closing
// brace (see the package doc).
func (l *Log) Export(ctx context.Context, w io.Writer) error {
	return l.ExportWithHead(ctx, w, nil)
}

// ExportWithHead is Export, calling head first with the chain's head as of
// the export (the last event it will write; zero for an empty log), read
// in the same transaction as the events. The HTTP export puts it in a
// header before the body starts.
func (l *Log) ExportWithHead(ctx context.Context, w io.Writer, head func(Head)) error {
	bw := bufio.NewWriter(w)
	err := l.eachWithHead(ctx, head, func(r row, body []byte, hash string) error {
		if len(body) == 0 || body[len(body)-1] != '}' {
			return fmt.Errorf("audit: event %d isn't a JSON object", r.seq)
		}
		bw.Write(body[:len(body)-1])
		bw.WriteString(`,"hash":`)
		bw.WriteString(strconv.Quote(hash))
		_, err := bw.WriteString("}\n")
		return err
	})
	if err != nil {
		return err
	}
	return bw.Flush()
}

// Break is the first place the chain doesn't hold.
type Break struct {
	Seq    int64  `json:"seq"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason"`
}

// Head is the last event of a chain: its seq and hash. Written down
// somewhere the hub can't reach, it lets an owner notice a chain that was
// cut short or rewritten later (the chain alone can't: see the package
// doc).
type Head struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

// String is "seq=<n> hash=<hash>", the X-Chadash-Audit-Head header's form.
func (h Head) String() string { return "seq=" + strconv.FormatInt(h.Seq, 10) + " hash=" + h.Hash }

// Report is the result of Verify.
type Report struct {
	OK         bool   `json:"ok"`
	Checked    int64  `json:"checked"`
	FirstBreak *Break `json:"first_break,omitempty"`
	// Head is the last event that checks out: the chain's head when OK.
	// Nil for an empty log.
	Head *Head `json:"head,omitempty"`
}

type row struct {
	seq                                int64
	id, actorID, actor, action, target string
	result, prevHash                   string
	at                                 int64
}

// Verify walks the whole chain, oldest first, and reports the first event
// that doesn't hold: a gap in seq, a hash that doesn't match the stored
// canonical JSON, a prev_hash that isn't the previous event's hash, or a
// filter column that disagrees with the event. It reads in one transaction,
// so appends made meanwhile aren't half-seen.
func (l *Log) Verify(ctx context.Context) (Report, error) {
	var rep Report
	prev, want := ZeroHash, int64(1)
	stop := errors.New("stop")
	err := l.each(ctx, func(r row, body []byte, hash string) error {
		fail := func(reason string) error {
			rep.FirstBreak = &Break{Seq: r.seq, ID: r.id, Reason: reason}
			return stop
		}
		if r.seq != want {
			rep.FirstBreak = &Break{Seq: want, Reason: fmt.Sprintf("event %d is missing (the next one is %d)", want, r.seq)}
			return stop
		}
		if HashOf(body) != hash {
			return fail("its hash doesn't match its contents")
		}
		var ev Event
		if err := json.Unmarshal(body, &ev); err != nil {
			return fail("its stored JSON doesn't parse")
		}
		switch {
		case r.prevHash != prev || ev.PrevHash != prev:
			return fail("its prev_hash isn't the previous event's hash")
		case ev.Seq != r.seq || ev.ID != r.id:
			return fail("its seq or id differs from its contents")
		case ev.At.UnixMilli() != r.at || ev.Actor.ID != r.actorID || ev.Actor.Name != r.actor ||
			ev.Action != r.action || ev.Target != r.target || ev.Result != r.result:
			return fail("its indexed columns differ from its contents")
		}
		if c, err := Canonical(ev); err != nil || !bytes.Equal(c, body) {
			return fail("its stored JSON isn't in canonical form")
		}
		rep.Checked++
		prev, want = hash, want+1
		rep.Head = &Head{Seq: r.seq, Hash: hash}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return Report{}, err
	}
	rep.OK = rep.FirstBreak == nil
	return rep, nil
}

// each calls fn for every event, oldest first, inside one read transaction.
func (l *Log) each(ctx context.Context, fn func(r row, body []byte, hash string) error) error {
	return l.eachWithHead(ctx, nil, fn)
}

// eachWithHead is each, first calling head (when not nil) with the last
// event as the same transaction sees it.
func (l *Log) eachWithHead(ctx context.Context, head func(Head), fn func(r row, body []byte, hash string) error) error {
	tx, err := l.st.Reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if head != nil {
		var h Head
		err := tx.QueryRowContext(ctx, `SELECT seq, hash FROM audit_events ORDER BY seq DESC LIMIT 1`).Scan(&h.Seq, &h.Hash)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("audit: %w", err)
		}
		head(h)
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq, id, at, actor_id, actor, action, target, result, event, prev_hash, hash
		FROM audit_events ORDER BY seq`)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r row
		var body []byte
		var hash string
		if err := rows.Scan(&r.seq, &r.id, &r.at, &r.actorID, &r.actor, &r.action, &r.target, &r.result, &body, &r.prevHash, &hash); err != nil {
			return err
		}
		if err := fn(r, body, hash); err != nil {
			return err
		}
	}
	return rows.Err()
}
