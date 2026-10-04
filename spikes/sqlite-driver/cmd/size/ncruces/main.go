// Command ncruces is the baseline plus github.com/ncruces/go-sqlite3.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func main() {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(os.TempDir(), "size.db"))
	if err != nil {
		panic(err)
	}
	var v string
	err = db.QueryRow("SELECT sqlite_version()").Scan(&v)
	fmt.Println(v, err)
}
