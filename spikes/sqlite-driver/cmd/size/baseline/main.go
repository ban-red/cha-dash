// Command baseline is the tiny main the driver sizes are measured against:
// database/sql and the standard library, no driver.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	db, err := sql.Open("none", filepath.Join(os.TempDir(), "size.db"))
	fmt.Println(db, err, sql.Drivers())
}
