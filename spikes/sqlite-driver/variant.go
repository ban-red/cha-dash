// Package sqlitedriver is spike 4.8: the hub's SQLite workload under
// modernc.org/sqlite and github.com/ncruces/go-sqlite3, plain and encrypted.
//
// Throwaway code. It mirrors internal/store (pragmas, one writer plus four
// readers, goose migrations) closely enough that the numbers mean something
// for the hub; the findings go into the README and ADR-0015.
package sqlitedriver

import (
	"crypto/hkdf"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
)

// Variant is one way of opening the hub database: a driver, plus for ncruces
// an optional encrypting VFS.
type Variant struct {
	Name      string // modernc, ncruces, ncruces+adiantum, ncruces+xts
	Module    string // Go module, for reports
	Encrypted bool
	// open returns a pool for path. key is the 32-byte key-encryption key
	// (the hub's secret.key); plain variants ignore it.
	open func(path string, readOnly bool, key []byte) (*sql.DB, error)
}

// Open returns a database/sql pool for path with the hub's pragmas.
func (v Variant) Open(path string, readOnly bool, key []byte) (*sql.DB, error) {
	return v.open(path, readOnly, key)
}

var variants = map[string]Variant{}

func register(v Variant) { variants[v.Name] = v }

// Variants returns every variant linked into this binary, sorted by name.
func Variants() []Variant {
	vs := make([]Variant, 0, len(variants))
	for _, v := range variants {
		vs = append(vs, v)
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Name < vs[j].Name })
	return vs
}

// Lookup finds a variant by name.
func Lookup(name string) (Variant, error) {
	v, ok := variants[name]
	if !ok {
		return Variant{}, fmt.Errorf("unknown variant %q (linked: %v)", name, Variants())
	}
	return v, nil
}

func (v Variant) String() string { return v.Name }

// hubQuery is internal/store's dsn() query string, unchanged: the same
// pragmas in the same order, query_only on readers, BEGIN IMMEDIATE on the
// writer.
func hubQuery(readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	return q.Encode()
}

// fileURI turns a path into a "file:" URI. ncruces only reads _pragma and
// _txlock from "file:" names; modernc accepts both forms.
func fileURI(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath()
}

// DBKey derives the database encryption key from the hub's key-encryption
// key, so one secret.key (or CHADASH_SECRET_KEY_FILE) covers both the vault
// and the database without reusing the same bytes for two ciphers.
func DBKey(kek []byte, vfsName string, size int) ([]byte, error) {
	if len(kek) != 32 {
		return nil, fmt.Errorf("key-encryption key must be 32 bytes, got %d", len(kek))
	}
	return hkdf.Key(sha256.New, kek, nil, "chadash/db/"+vfsName+"/v1", size)
}
