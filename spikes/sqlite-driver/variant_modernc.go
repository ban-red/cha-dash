//go:build !no_modernc

package sqlitedriver

import (
	"database/sql"

	_ "modernc.org/sqlite" // registers "sqlite"
)

func init() {
	register(Variant{
		Name:   "modernc",
		Module: "modernc.org/sqlite",
		// Exactly what internal/store/driver_modernc.go does today.
		open: func(path string, readOnly bool, _ []byte) (*sql.DB, error) {
			return sql.Open("sqlite", path+"?"+hubQuery(readOnly))
		},
	})
}
