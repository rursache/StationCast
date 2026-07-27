package storage

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func indexExists(t *testing.T, db *DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	return n > 0
}

func appliedVersions(t *testing.T, db *DB) []int {
	t.Helper()
	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func TestOpenAppliesEveryMigration(t *testing.T) {
	db := openTestDB(t)

	got := appliedVersions(t, db)
	if len(got) != len(migrations) {
		t.Fatalf("applied %d migrations, want %d (%v)", len(got), len(migrations), got)
	}
	for i, m := range migrations {
		if got[i] != m.version {
			t.Errorf("applied version at %d = %d, want %d", i, got[i], m.version)
		}
	}

	for _, table := range []string{"tracks", "history", "queue", "settings", "schema_migrations"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %q missing after migration", table)
		}
	}
}

// tracks.path is UNIQUE, and SQLite backs that with an implicit index, so the
// explicit one only added write cost
func TestMigrationDropsRedundantPathIndex(t *testing.T) {
	db := openTestDB(t)

	if indexExists(t, db, "idx_tracks_path") {
		t.Error("idx_tracks_path is still present, it duplicates the UNIQUE constraint index")
	}
	// The one that does earn its keep must survive
	if !indexExists(t, db, "idx_history_played_at") {
		t.Error("idx_history_played_at was dropped, it backs the history ordering")
	}
}

// Dropping the index must not weaken the constraint the scanner relies on to
// keep one row per path
func TestPathUniquenessStillEnforced(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.Exec(`INSERT INTO tracks(path, size, mtime, has_art, added_at) VALUES('/music/a.mp3', 1, 1, 0, 1)`); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tracks(path, size, mtime, has_art, added_at) VALUES('/music/a.mp3', 2, 2, 0, 2)`); err == nil {
		t.Fatal("duplicate path was accepted, the UNIQUE constraint is gone")
	}
}

func TestMigrateIsIdempotentAcrossReopens(t *testing.T) {
	dir := t.TempDir()

	for i := 0; i < 3; i++ {
		db, err := Open(dir)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		got := appliedVersions(t, db)
		if len(got) != len(migrations) {
			t.Fatalf("open %d: applied %d migrations, want %d", i, len(got), len(migrations))
		}
		_ = db.Close()
	}
}

// A database from before schema_migrations existed carries the old schema but
// no version rows. The seeding must cover exactly the pre-versioning
// migrations, so the non-idempotent ALTER TABLE statements do not re-run while
// anything added later still gets applied
func TestLegacyDatabaseSeedsOnlyPreVersioningMigrations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stationcast.db")

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// The legacy shape: tracks with art_tried already added, plus the
	// redundant index, and no schema_migrations table at all
	legacy := []string{
		`CREATE TABLE tracks (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			path        TEXT NOT NULL UNIQUE,
			size        INTEGER NOT NULL,
			mtime       INTEGER NOT NULL,
			title       TEXT,
			artist      TEXT,
			album       TEXT,
			duration_ms INTEGER,
			has_art     INTEGER NOT NULL DEFAULT 0,
			added_at    INTEGER NOT NULL,
			art_tried   INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX idx_tracks_path ON tracks(path)`,
		`INSERT INTO tracks(path, size, mtime, has_art, added_at) VALUES('/music/legacy.mp3', 1, 1, 0, 1)`,
	}
	for _, stmt := range legacy {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed legacy schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open on a legacy database: %v", err)
	}
	defer db.Close()

	// Existing rows survive
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tracks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("legacy row count = %d, want 1", n)
	}

	// Every version is recorded, including the ones seeded rather than run
	got := appliedVersions(t, db)
	if len(got) != len(migrations) {
		t.Fatalf("applied versions %v, want %d entries", got, len(migrations))
	}

	// The post-legacy migration actually ran instead of being seeded away
	if indexExists(t, db, "idx_tracks_path") {
		t.Error("migration 8 was seeded as applied on a legacy database instead of running")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	db := openTestDB(t)

	got, err := db.GetSetting("mode")
	if err != nil {
		t.Fatalf("GetSetting on a missing key: %v", err)
	}
	if got != "" {
		t.Errorf("missing key returned %q, want empty", got)
	}

	if err := db.SetSetting("mode", "shuffle"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got, err := db.GetSetting("mode"); err != nil || got != "shuffle" {
		t.Errorf("GetSetting = %q (err %v), want %q", got, err, "shuffle")
	}

	// Upsert, not a duplicate row
	if err := db.SetSetting("mode", "sequential"); err != nil {
		t.Fatalf("SetSetting overwrite: %v", err)
	}
	if got, err := db.GetSetting("mode"); err != nil || got != "sequential" {
		t.Errorf("GetSetting after overwrite = %q (err %v), want %q", got, err, "sequential")
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'mode'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("settings holds %d rows for one key, want 1", rows)
	}
}

func TestOpenSetsWALAndSingleConnection(t *testing.T) {
	db := openTestDB(t)

	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
}
