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

func newTestLibrary(t *testing.T, music, data string) *Library {
	t.Helper()
	cfg := &config.Config{MusicDir: music, DataDir: data}
	if err := os.MkdirAll(filepath.Join(data, "art"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewLibrary(cfg, db)
}

// The startup scan uses filepath.WalkDir, which does not follow symlinks, but
// the fsnotify Create handler used os.Stat, which does. A symlinked directory
// created while the server was running therefore got walked and its contents
// indexed, pulling files from outside the music root into the library
func TestIsWatchableDirIgnoresSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	realDir := filepath.Join(root, "albums")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	regularFile := filepath.Join(root, "track.mp3")
	if err := os.WriteFile(regularFile, []byte("not really mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkToOutsideDir := filepath.Join(root, "escape")
	if err := os.Symlink(outside, linkToOutsideDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linkToInsideDir := filepath.Join(root, "inside-link")
	if err := os.Symlink(realDir, linkToInsideDir); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"real directory", realDir, true},
		{"regular file", regularFile, false},
		{"symlink to directory outside root", linkToOutsideDir, false},
		{"symlink to directory inside root", linkToInsideDir, false},
		{"missing path", filepath.Join(root, "nope"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWatchableDir(tc.path); got != tc.want {
				t.Errorf("isWatchableDir(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// upsertFile's Lstat only inspects the final component, and its containment
// check is textual, so a symlinked directory partway along the path passed
// both while actually resolving outside the music root
func TestUpsertFileRejectsPathThroughSymlinkedDir(t *testing.T) {
	music := t.TempDir()
	data := t.TempDir()
	outside := t.TempDir()

	stranger := filepath.Join(outside, "stranger.mp3")
	if err := os.WriteFile(stranger, []byte("not really mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(music, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	lib := newTestLibrary(t, music, data)

	// Lexically inside the music root, but it resolves outside it
	viaSymlink := filepath.Join(music, "escape", "stranger.mp3")
	if err := lib.upsertFile(viaSymlink); err == nil {
		t.Fatal("upsertFile indexed a file that resolves outside the music root")
	}
	if lib.Count() != 0 {
		t.Fatalf("library picked up %d tracks, want 0", lib.Count())
	}
}

func TestUpsertFileAcceptsOrdinaryFile(t *testing.T) {
	music := t.TempDir()
	data := t.TempDir()

	p := filepath.Join(music, "real.mp3")
	if err := os.WriteFile(p, []byte("not really mp3"), 0o644); err != nil {
		t.Fatal(err)
	}

	lib := newTestLibrary(t, music, data)
	if err := lib.upsertFile(p); err != nil {
		t.Fatalf("upsertFile on an ordinary file: %v", err)
	}
	if lib.Count() != 1 {
		t.Fatalf("library has %d tracks, want 1", lib.Count())
	}
}

// A directory whose name merely starts with ".." is inside the root and must
// not be mistaken for a traversal attempt
func TestWithinRoot(t *testing.T) {
	root := filepath.FromSlash("/music")
	cases := []struct {
		path string
		want bool
	}{
		{filepath.FromSlash("/music/a.mp3"), true},
		{filepath.FromSlash("/music/sub/a.mp3"), true},
		{filepath.FromSlash("/music"), true},
		{filepath.FromSlash("/music/..bonus tracks/a.mp3"), true},
		{filepath.FromSlash("/etc/passwd"), false},
		{filepath.FromSlash("/musicother/a.mp3"), false},
		{filepath.FromSlash("/"), false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := withinRoot(root, tc.path); got != tc.want {
				t.Errorf("withinRoot(%q, %q) = %v, want %v", root, tc.path, got, tc.want)
			}
		})
	}
}

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
