package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

func Open(dataDir string) (*DB, error) {
	dsn := "file:" + filepath.Join(dataDir, "stationcast.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(1)
	if err := sqldb.Ping(); err != nil {
		return nil, err
	}
	db := &DB{sqldb}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS tracks (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		path        TEXT NOT NULL UNIQUE,
		size        INTEGER NOT NULL,
		mtime       INTEGER NOT NULL,
		title       TEXT,
		artist      TEXT,
		album       TEXT,
		duration_ms INTEGER,
		has_art     INTEGER NOT NULL DEFAULT 0,
		added_at    INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_tracks_path ON tracks(path)`,
	`ALTER TABLE tracks ADD COLUMN art_tried INTEGER NOT NULL DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS history (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		track_id  INTEGER NOT NULL,
		played_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_history_played_at ON history(played_at DESC)`,
	`CREATE TABLE IF NOT EXISTS queue (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		track_id INTEGER NOT NULL,
		position INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
}

func (db *DB) migrate() error {
	for i, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil {
			// Tolerate "duplicate column" on ALTER TABLE re-runs; SQLite has no IF NOT EXISTS for columns
			if isDuplicateColumn(err) {
				continue
			}
			return fmt.Errorf("migration %d: %w", i, err)
		}
	}
	return nil
}

func isDuplicateColumn(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return contains(s, "duplicate column") || contains(s, "already exists")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func (db *DB) GetSetting(key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (db *DB) SetSetting(key, value string) error {
	_, err := db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
