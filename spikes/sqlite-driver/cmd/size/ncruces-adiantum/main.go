// Command ncruces-adiantum is the baseline plus ncruces with the Adiantum
// VFS and an HKDF-derived key: what an encrypted hub would link.
package main

import (
	"crypto/hkdf"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"
)

func main() {
	kek := make([]byte, 32) // a stand-in; the hub reads secret.key
	key, err := hkdf.Key(sha256.New, kek, nil, "chadash/db/adiantum/v1", 32)
	if err != nil {
		panic(err)
	}
	var db *sql.DB
	db, err = driver.Open("file:"+filepath.Join(os.TempDir(), "size-enc.db")+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + hex.EncodeToString(key) + "'")
	})
	if err != nil {
		panic(err)
	}
	var v string
	err = db.QueryRow("SELECT sqlite_version()").Scan(&v)
	fmt.Println(v, err)
}
