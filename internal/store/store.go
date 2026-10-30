// Package store holds Palier's SQLite database.
package store

import (
	"database/sql"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens, and creates if needed, the database of the data directory.
func Open(dataDir string) (*sql.DB, error) {
	dsn := "file:" + filepath.Join(dataDir, "palier.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
