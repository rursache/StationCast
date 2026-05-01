package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

func Open(dataDir string) (*DB, error) {
	path := filepath.Join(dataDir, "stationcast.db")
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(1)
	if err := sqldb.Ping(); err != nil {
		return nil, err
	}
	// Tighten permissions on the DB file (and the WAL/SHM siblings) so other
	// local users on a shared host cannot read track paths, history, or
	// settings. SQLite created them with the process umask
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Chmod(path+suffix, 0o600)
	}
	db := &DB{sqldb}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{1, `CREATE TABLE IF NOT EXISTS tracks (
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
	)`},
	{2, `CREATE INDEX IF NOT EXISTS idx_tracks_path ON tracks(path)`},
	{3, `ALTER TABLE tracks ADD COLUMN art_tried INTEGER NOT NULL DEFAULT 0`},
	{4, `CREATE TABLE IF NOT EXISTS history (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		track_id  INTEGER NOT NULL,
		played_at INTEGER NOT NULL
	)`},
	{5, `CREATE INDEX IF NOT EXISTS idx_history_played_at ON history(played_at DESC)`},
	{6, `CREATE TABLE IF NOT EXISTS queue (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		track_id INTEGER NOT NULL,
		position INTEGER NOT NULL
	)`},
	{7, `CREATE TABLE IF NOT EXISTS settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`},
}

func (db *DB) migrate() error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}

	// Pre-versioned databases already carry the legacy schema but no rows in
	// schema_migrations. Detect that case (tracks table present, migrations
	// table empty) and seed the version log so the non-idempotent statements
	// (ALTER TABLE ADD COLUMN) do not re-run on upgrade
	var legacyTracks int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='tracks'`).Scan(&legacyTracks)
	var applied int
	_ = db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied)
	if legacyTracks > 0 && applied == 0 {
		now := time.Now().Unix()
		for _, m := range migrations {
			if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, m.version, now); err != nil {
				return fmt.Errorf("seed schema_migrations: %w", err)
			}
		}
	}

	for _, m := range migrations {
		var done int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			continue
		}
		if _, err := db.Exec(m.sql); err != nil {
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, m.version, time.Now().Unix()); err != nil {
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
	}
	return nil
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
