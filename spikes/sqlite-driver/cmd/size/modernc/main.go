// Command modernc is the baseline plus modernc.org/sqlite.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", filepath.Join(os.TempDir(), "size.db"))
	if err != nil {
		panic(err)
	}
	var v string
	err = db.QueryRow("SELECT sqlite_version()").Scan(&v)
	fmt.Println(v, err)
}
