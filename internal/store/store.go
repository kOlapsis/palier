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
CREATE TABLE IF NOT EXISTS releases (
	repo TEXT NOT NULL,
	tag TEXT NOT NULL,
	url TEXT NOT NULL,
	published TEXT NOT NULL,
	body TEXT NOT NULL,
	PRIMARY KEY (repo, tag)
);
CREATE TABLE IF NOT EXISTS plans (
	stack TEXT PRIMARY KEY,
	input_hash TEXT NOT NULL,
	source TEXT NOT NULL,
	body TEXT NOT NULL,
	created TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS plan_cache (
	input_hash TEXT PRIMARY KEY,
	source TEXT NOT NULL,
	body TEXT NOT NULL,
	created TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
	id INTEGER PRIMARY KEY,
	stack TEXT NOT NULL,
	status TEXT NOT NULL,
	dry_run INTEGER NOT NULL,
	author TEXT NOT NULL,
	plan TEXT NOT NULL,
	checkpoint TEXT NOT NULL DEFAULT '',
	modified INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	started TEXT NOT NULL,
	finished TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS run_events (
	run_id INTEGER NOT NULL REFERENCES runs (id),
	at TEXT NOT NULL,
	message TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS decisions (
	id INTEGER PRIMARY KEY,
	stack TEXT NOT NULL,
	kind TEXT NOT NULL,
	service TEXT NOT NULL DEFAULT '',
	version TEXT NOT NULL DEFAULT '',
	until TEXT NOT NULL DEFAULT '',
	author TEXT NOT NULL,
	at TEXT NOT NULL
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
