package store

// This is the only file that names a SQLite driver (ADR-0015). The hub uses
// github.com/ncruces/go-sqlite3, pinned in go.mod; going back to modernc is a
// swap of this file while encryption at rest stays off.
//
// Things the rest of the store relies on:
//   - SQLite is built with DQS=0, so a double-quoted string literal is an
//     error ("no such column"). Quote strings with single quotes.
//   - Don't import ncruces's deprecated embed package (it warns at init), and
//     link extensions such as FTS5 from its ext/ packages only when needed.
//
// Encryption at rest (ADR-0015: Adiantum, opt-in) is not implemented. When it
// is, openDB gains a key: it opens the pool with driver.Open and a connection
// hook that runs PRAGMA hexkey before the other pragmas, which keeps the key
// out of the DSN. The _pragma parameters below run before that hook, so with
// a key they move into the hook too. store.Open passes the key through, and
// cmd/chadash loads the KEK before opening the store.

import (
	"database/sql"
	"net/url"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver" // registers "sqlite3" with database/sql
)

const driverName = "sqlite3"

// openDB opens a pool on the database at path. Every connection gets the same
// pragmas; readers are additionally marked query_only so a stray write fails
// loudly instead of contending with the single writer.
func openDB(path string, readOnly bool) (*sql.DB, error) {
	return sql.Open(driverName, dsn(path, readOnly))
}

// dsn builds the connection string: an escaped "file:" URI, since ncruces
// reads _pragma and _txlock only from those. Without the prefix the whole
// string, query and all, would become the file name.
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	// ncruces runs pragmas in the order given, so busy_timeout goes first:
	// the others may have to wait for a lock.
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)") // safe with WAL; loses at most the last commits on power loss
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate") // take the write lock at BEGIN, not at first write
	}
	// ncruces's VFS creates -wal and -shm 0666 minus the umask, where
	// SQLite's own VFS copies the database's mode. modeof gives -wal, which
	// holds row data, the database's 0600. -shm (the WAL index, no row data)
	// follows the umask, which cmd/chadash sets to 077.
	q.Set("modeof", path)
	// SQLite decodes %XX in URIs but not "+", which Encode uses for spaces;
	// modeof is read by SQLite itself. Encode writes a real "+" as %2B, so
	// every "+" left is a space.
	query := strings.ReplaceAll(q.Encode(), "+", "%20")
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + query
}

// OpenForeign opens another program's SQLite database read-only and
// immutable (an importer reading Homarr's backup): no WAL, no locks, no
// writes, no pragmas of ours.
func OpenForeign(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Set("immutable", "1")
	q.Add("_pragma", "query_only(1)")
	return sql.Open(driverName, "file:"+(&url.URL{Path: path}).EscapedPath()+"?"+q.Encode())
}
