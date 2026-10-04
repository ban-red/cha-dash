package sqlitedriver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The hub has no metrics table yet (architecture: tiered rollups, batched
// writes). This spike-only table stands in for the raw tier.
const spikeSchema = `CREATE TABLE IF NOT EXISTS metric_samples (
    series_id INTEGER NOT NULL,
    at        INTEGER NOT NULL,
    value     REAL NOT NULL,
    PRIMARY KEY (series_id, at)
) STRICT, WITHOUT ROWID`

// KeepRevisions matches internal/boards.
const KeepRevisions = 100

// Fixture is what Seed created.
type Fixture struct {
	BoardID     string
	OwnerID     string
	TokenHashes [][]byte
}

// Seed adds the spike table, users, sessions and one board, the way the hub
// would after first-run setup and some use.
func Seed(ctx context.Context, st *Store, users, sessions int) (*Fixture, error) {
	if _, err := st.W.ExecContext(ctx, spikeSchema); err != nil {
		return nil, err
	}
	fx := &Fixture{BoardID: "b_home", OwnerID: "u_owner"}
	now := time.Now().UnixMilli()
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		for i := range users {
			id, role := fmt.Sprintf("u_%04d", i), "viewer"
			if i == 0 {
				id, role = fx.OwnerID, "owner"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, role, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)`, id, fmt.Sprintf("user%d", i), "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA", role, now, now); err != nil {
				return err
			}
		}
		for i := range sessions {
			tok := make([]byte, 32)
			rand.Read(tok)
			h := sha256.Sum256(tok)
			fx.TokenHashes = append(fx.TokenHashes, h[:])
			user := fx.OwnerID
			if i%users != 0 {
				user = fmt.Sprintf("u_%04d", i%users)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, token_hash, user_id, method, created_at, expires_at, last_seen_at, ip, user_agent)
				VALUES (?, ?, ?, 'password', ?, ?, ?, ?, ?)`, fmt.Sprintf("s_%06d", i), h[:], user, now, now+30*86400_000, now,
				"192.0.2.10", "Mozilla/5.0 (X11; Linux aarch64) spike"); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO boards (id, slug, title, position, created_at, updated_at)
			VALUES (?, 'home', 'Home', 0, ?, ?)`, fx.BoardID, now, now)
		return err
	})
	return fx, err
}

// LayoutDoc returns a compact JSON layout document of at least size bytes:
// a grid of tiles shaped like the layout package's documents. rev varies the
// first bytes so successive saves differ.
func LayoutDoc(size, rev int) string {
	var b strings.Builder
	b.Grow(size + 512)
	fmt.Fprintf(&b, `{"rev":%d,"format":3,"columns":12,"tiles":[`, rev)
	for i := 0; b.Len() < size; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"t_%06d","kind":"service","x":%d,"y":%d,"w":%d,"h":%d,`+
			`"title":"Service %d","href":"https://svc-%d.home.example","icon":"si:server",`+
			`"check":{"kind":"http","interval":30,"expect":[200,204]},"props":{"group":"g%d","note":"tile %d of the spike layout"}}`,
			i, i%12, i/12, 1+i%3, 1+i%2, i, i, i%17, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

// SaveLayout is internal/boards.SaveLayout without the json.Compact (the
// document is already compact, and Compact costs the same under any driver).
func SaveLayout(ctx context.Context, st *Store, boardID string, base int64, doc, userID string) (int64, error) {
	var next int64
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		var cur sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT (SELECT MAX(revision) FROM layouts WHERE board_id = b.id)
			FROM boards b WHERE b.id = ?`, boardID).Scan(&cur)
		if err != nil {
			return err
		}
		if cur.Int64 != base {
			return fmt.Errorf("conflict: base %d, current %d", base, cur.Int64)
		}
		next = base + 1
		now := time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `INSERT INTO layouts (board_id, revision, format_version, document, created_at, created_by)
			VALUES (?, ?, ?, ?, ?, ?)`, boardID, next, 3, doc, now, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE boards SET updated_at = ? WHERE id = ?`, now, boardID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM layouts WHERE board_id = ? AND revision <= ?`, boardID, next-KeepRevisions)
		return err
	})
	return next, err
}

// LoadLayout is internal/boards.Layout: the latest revision's document.
func LoadLayout(ctx context.Context, st *Store, boardID string) (int64, int, error) {
	var rev sql.NullInt64
	var doc sql.NullString
	err := st.R.QueryRowContext(ctx, `SELECT l.revision, l.document
		FROM boards b LEFT JOIN layouts l ON l.board_id = b.id
			AND l.revision = (SELECT MAX(revision) FROM layouts WHERE board_id = b.id)
		WHERE b.id = ?`, boardID).Scan(&rev, &doc)
	return rev.Int64, len(doc.String), err
}

// LookupSession is internal/auth's lookupSession query.
func LookupSession(ctx context.Context, st *Store, tokenHash []byte) error {
	var id, method, uid, username, role string
	var created, expires, seen int64
	return st.R.QueryRowContext(ctx, `
		SELECT s.id, s.method, s.created_at, s.expires_at, s.last_seen_at, u.id, u.username, u.role
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled_at IS NULL`,
		tokenHash, time.Now().UnixMilli()).
		Scan(&id, &method, &created, &expires, &seen, &uid, &username, &role)
}

// AppendAudit writes one audit row in autocommit, as the action engine will.
func AppendAudit(ctx context.Context, st *Store, i int) error {
	_, err := st.W.ExecContext(ctx, `INSERT INTO audit (at, actor, action, target, params, tier, result, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UnixMilli(),
		`{"user":"u_owner","via":"web","session":"s_000001","ip":"192.0.2.10"}`,
		"container.restart", fmt.Sprintf("docker:host-a/web-%d", i%40), `{"timeout":10}`, "operate", "ok",
		`{"duration_ms":1480,"plan":"restart web"}`)
	return err
}

// metricClock hands out sample timestamps, one millisecond apart, so
// batches never collide on the primary key.
var metricClock atomic.Int64

func init() { metricClock.Store(time.Now().UnixMilli()) }

// InsertMetrics writes one sample for each of n series in one transaction:
// one poll of the whole home.
func InsertMetrics(ctx context.Context, st *Store, n int) error {
	at := metricClock.Add(1)
	return st.WriteTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO metric_samples (series_id, at, value) VALUES (?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := range n {
			if _, err := stmt.ExecContext(ctx, i, at, float64(i)*1.5+float64(at%1000)/7); err != nil {
				return err
			}
		}
		return nil
	})
}

// MixedResult is what Mixed measured.
type MixedResult struct {
	Duration             time.Duration
	Reads, Writes        int64
	ReadP50, ReadP99     time.Duration
	ReadMax              time.Duration
	WriteP50, WriteP99   time.Duration
	Errors               int64
	FirstErr             string
	LayoutBytes, Readers int
}

// Mixed runs readers doing session lookups on the read pool while one writer
// loops over a 1 MiB layout save, ten audit appends and a metrics batch, for
// dur. WAL should keep the readers unblocked; any error (SQLITE_BUSY
// included) is counted.
func Mixed(ctx context.Context, st *Store, fx *Fixture, dur time.Duration, readers, layoutBytes int) MixedResult {
	ctx, cancel := context.WithTimeout(ctx, dur)
	defer cancel()
	res := MixedResult{Duration: dur, Readers: readers, LayoutBytes: layoutBytes}
	var errs atomic.Int64
	var firstErr atomic.Value
	fail := func(err error) {
		if ctx.Err() != nil {
			return // the clock ran out mid-query; drivers report that differently
		}
		if errs.Add(1) == 1 {
			firstErr.Store(err)
		}
	}

	rev, _, err := LoadLayout(ctx, st, fx.BoardID)
	if err != nil {
		fail(err)
	}
	doc := LayoutDoc(layoutBytes, 0)
	var wg sync.WaitGroup
	var writeLat []time.Duration
	wg.Go(func() {
		for i := 0; ctx.Err() == nil; i++ {
			t := time.Now()
			next, err := SaveLayout(ctx, st, fx.BoardID, rev, doc, fx.OwnerID)
			if err != nil {
				fail(err)
				continue
			}
			rev = next
			for j := range 10 {
				if err := AppendAudit(ctx, st, i*10+j); err != nil {
					fail(err)
				}
			}
			if err := InsertMetrics(ctx, st, 300); err != nil {
				fail(err)
			}
			writeLat = append(writeLat, time.Since(t))
		}
	})
	readLat := make([][]time.Duration, readers)
	for r := range readers {
		wg.Go(func() {
			for i := r; ctx.Err() == nil; i += readers {
				t := time.Now()
				if err := LookupSession(ctx, st, fx.TokenHashes[i%len(fx.TokenHashes)]); err != nil {
					fail(err)
					continue
				}
				readLat[r] = append(readLat[r], time.Since(t))
			}
		})
	}
	wg.Wait()

	all := slices.Concat(readLat...)
	res.Reads, res.Writes = int64(len(all)), int64(len(writeLat))
	res.ReadP50, res.ReadP99, res.ReadMax = pct(all, 50), pct(all, 99), pct(all, 100)
	res.WriteP50, res.WriteP99 = pct(writeLat, 50), pct(writeLat, 99)
	res.Errors = errs.Load()
	if e, ok := firstErr.Load().(error); ok {
		res.FirstErr = e.Error()
	}
	return res
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	slices.Sort(d)
	i := int(math.Ceil(p/100*float64(len(d)))) - 1
	return d[max(0, min(i, len(d)-1))]
}
