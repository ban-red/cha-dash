// Package store here is not the hub's store: it is the replacement for
// internal/store/driver_modernc.go that hubswap.sh overlays onto the hub, so
// the hub's own tests run under ncruces without editing internal/. It is the
// whole plain-ncruces swap; ADR-0015 describes it.
package store

// This is the only file that names a SQLite driver (ADR-0015).

import (
	"net/url"

	_ "github.com/ncruces/go-sqlite3/driver" // registers "sqlite3" with database/sql
)

const driverName = "sqlite3"

// dsn builds the connection string. Every connection gets the same pragmas;
// readers are additionally marked query_only so a stray write fails loudly
// instead of contending with the single writer.
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	// ncruces runs _pragma in the order given (modernc sorts busy_timeout
	// first), so busy_timeout goes first: the others may wait on a lock.
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)") // safe with WAL; loses at most the last commits on power loss
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate") // take the write lock at BEGIN, not at first write
	}
	// ncruces's Go VFS creates -wal and -shm 0666 minus the umask, where
	// SQLite's own VFS copies the database's mode. modeof fixes -wal, which
	// holds row data; -shm (the WAL index, no row data) still follows the
	// umask.
	p := (&url.URL{Path: path}).EscapedPath()
	q.Set("modeof", path)
	// ncruces reads _pragma and _txlock only from "file:" URIs.
	return "file:" + p + "?" + q.Encode()
}
