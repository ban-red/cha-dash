package store

// This is the only file that names a SQLite driver. The driver choice is open
// (decision 15, spike 4.8: modernc vs ncruces); swapping it should only touch
// this file. Both drivers accept the `_pragma` and `_txlock` DSN parameters.

import (
	"net/url"

	_ "modernc.org/sqlite" // registers "sqlite" with database/sql
)

const driverName = "sqlite"

// dsn builds the connection string. Every connection gets the same pragmas;
// readers are additionally marked query_only so a stray write fails loudly
// instead of contending with the single writer.
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)") // safe with WAL; loses at most the last commits on power loss
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate") // take the write lock at BEGIN, not at first write
	}
	return path + "?" + q.Encode()
}
