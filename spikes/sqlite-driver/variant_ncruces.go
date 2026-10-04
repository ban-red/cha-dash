//go:build !no_ncruces

package sqlitedriver

import (
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver" // registers "sqlite3"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"
	_ "github.com/ncruces/go-sqlite3/vfs/xts"
)

func init() {
	register(Variant{
		Name:   "ncruces",
		Module: "github.com/ncruces/go-sqlite3",
		// The hub's DSN with one change: a "file:" prefix, without which
		// ncruces treats the whole string, query and all, as the file name.
		open: func(path string, readOnly bool, _ []byte) (*sql.DB, error) {
			return sql.Open("sqlite3", fileURI(path)+"?"+hubQuery(readOnly))
		},
	})
	register(encrypted("adiantum", 32))
	register(encrypted("xts", 64)) // AES-256-XTS
}

// encrypted opens through an encrypting VFS. The key never appears in the
// DSN: it is set by PRAGMA from the connection hook, before anything reads
// the file, and the remaining pragmas follow it. (_pragma DSN parameters
// run before the hook, which is too early: SQLite would read page 1 without
// a key.)
func encrypted(vfsName string, keySize int) Variant {
	return Variant{
		Name:      "ncruces+" + vfsName,
		Module:    "github.com/ncruces/go-sqlite3/vfs/" + vfsName,
		Encrypted: true,
		open: func(path string, readOnly bool, kek []byte) (*sql.DB, error) {
			key, err := DBKey(kek, vfsName, keySize)
			if err != nil {
				return nil, err
			}
			hexKey := hex.EncodeToString(key)
			clear(key)
			txlock := "&_txlock=immediate"
			if readOnly {
				txlock = ""
			}
			return driver.Open(fileURI(path)+"?vfs="+vfsName+txlock, func(c *sqlite3.Conn) error {
				return initEncrypted(c, hexKey, readOnly)
			})
		},
	}
}

func initEncrypted(c *sqlite3.Conn, hexKey string, readOnly bool) error {
	pragmas := []string{
		"PRAGMA hexkey='" + hexKey + "'", // must come first
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=1",
		"PRAGMA synchronous=NORMAL",
	}
	if readOnly {
		pragmas = append(pragmas, "PRAGMA query_only=1")
	}
	for _, p := range pragmas {
		if err := c.Exec(p); err != nil {
			if p == pragmas[0] {
				p = "PRAGMA hexkey" // keep the key out of errors
			}
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}
