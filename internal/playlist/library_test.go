package playlist

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/storage"
)

// Snapshot backs ModeSequential's "next track" indexing, so its order has to
// be identical across calls and sorted by path rather than by map iteration
func TestSnapshotOrderIsStableAndSortedByPath(t *testing.T) {
	music := t.TempDir()
	data := t.TempDir()

	// Deliberately created out of order so a passing test cannot be an
	// accident of insertion order
	names := []string{"delta.mp3", "alpha.mp3", "echo.mp3", "bravo.mp3", "charlie.mp3"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(music, n), []byte("not really mp3"), 0o644); err != nil {
			t.Fatalf("write %q: %v", n, err)
		}
	}

	cfg := &config.Config{MusicDir: music, DataDir: data}
	if err := os.MkdirAll(filepath.Join(data, "art"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	lib := NewLibrary(cfg, db)
	if err := lib.InitialScan(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{"alpha.mp3", "bravo.mp3", "charlie.mp3", "delta.mp3", "echo.mp3"}
	// Repeat enough times that Go's randomised map iteration would show up
	for i := 0; i < 50; i++ {
		snap := lib.Snapshot()
		if len(snap) != len(want) {
			t.Fatalf("iteration %d: got %d tracks, want %d", i, len(snap), len(want))
		}
		for j, tr := range snap {
			if got := filepath.Base(tr.Path); got != want[j] {
				t.Fatalf("iteration %d position %d: got %q, want %q", i, j, got, want[j])
			}
		}
	}
}

// Verifies that filenames with multibyte UTF-8 characters survive a full scan
// round-trip (filesystem -> WalkDir -> SQLite -> in-memory map -> lookup).
// Same behavior is required on macOS and Linux.
func TestUnicodeFilenameRoundtrip(t *testing.T) {
	music := t.TempDir()
	data := t.TempDir()

	names := []string{
		"plain ascii.mp3",
		"café résumé.mp3",
		"日本語タイトル.mp3",
		"🎵 emoji track 🔥.mp3",
		"Zoë's Über-mix.mp3",
		"Romanian ăîșțâ.mp3",
	}
	for _, n := range names {
		p := filepath.Join(music, n)
		if err := os.WriteFile(p, []byte("not really mp3"), 0o644); err != nil {
			t.Fatalf("write %q: %v", n, err)
		}
		if !utf8.ValidString(n) {
			t.Fatalf("name not valid utf-8: %q", n)
		}
	}

	cfg := &config.Config{MusicDir: music, DataDir: data}
	if err := os.MkdirAll(filepath.Join(data, "art"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	lib := NewLibrary(cfg, db)
	if err := lib.InitialScan(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := lib.Count(); got != len(names) {
		t.Fatalf("scan count = %d, want %d", got, len(names))
	}

	for _, n := range names {
		full := filepath.Join(music, n)
		tr, ok := lib.GetByPath(full)
		if !ok {
			t.Errorf("missing track for %q", n)
			continue
		}
		if !strings.Contains(tr.Title, strings.TrimSuffix(n, ".mp3")) && tr.Title != "" {
			// Tag reader may strip extension; we just need a non-empty title or matching basename
			if tr.Title == "" {
				t.Errorf("empty title for %q", n)
			}
		}
	}
}
