// Package store holds Palier's SQLite database.
package store

import (
	"database/sql"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS tag_dates (
	image TEXT NOT NULL,
	tag TEXT NOT NULL,
	pushed TEXT NOT NULL,
	PRIMARY KEY (image, tag)
);
`

// Open opens, and creates if needed, the database of the data directory.
func Open(dataDir string) (*sql.DB, error) {
	dsn := "file:" + filepath.Join(dataDir, "palier.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
