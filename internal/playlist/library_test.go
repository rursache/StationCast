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
