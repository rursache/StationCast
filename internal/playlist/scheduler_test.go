package playlist

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/storage"
)

// newTestSchedulerWith returns a scheduler backed by n on-disk fake mp3
// files so library lookups resolve. The library itself does the scan
func newTestSchedulerWith(t *testing.T, n int) *Scheduler {
	t.Helper()
	music := t.TempDir()
	data := t.TempDir()
	for i := 0; i < n; i++ {
		p := filepath.Join(music, fmt.Sprintf("track_%02d.mp3", i))
		if err := os.WriteFile(p, []byte("not really mp3"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
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
	t.Cleanup(func() { _ = db.Close() })
	lib := NewLibrary(cfg, db)
	if err := lib.InitialScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewScheduler(cfg, db, lib)
}

func TestShuffleDeckPlaysEveryTrackOncePerCycle(t *testing.T) {
	const n = 12
	s := newTestSchedulerWith(t, n)

	seen := map[int64]int{}
	for i := 0; i < n; i++ {
		tr := s.Pick()
		if tr == nil {
			t.Fatalf("Pick returned nil at i=%d", i)
		}
		seen[tr.ID]++
		s.MarkPlaying(tr)
	}
	if len(seen) != n {
		t.Fatalf("first cycle saw %d unique tracks, want %d", len(seen), n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Errorf("track %d played %d times in one cycle, want 1", id, c)
		}
	}
}

func TestShuffleDeckRebuildsAfterExhaustion(t *testing.T) {
	const n = 5
	s := newTestSchedulerWith(t, n)

	// Drain the first deck
	for i := 0; i < n; i++ {
		tr := s.Pick()
		if tr == nil {
			t.Fatalf("Pick returned nil at i=%d", i)
		}
		s.MarkPlaying(tr)
	}
	// Continue past the boundary, verifying we keep getting tracks
	for i := 0; i < n; i++ {
		tr := s.Pick()
		if tr == nil {
			t.Fatalf("Pick returned nil after deck exhaustion at i=%d", i)
		}
		s.MarkPlaying(tr)
	}
}

func TestShuffleDeckHandlesEmptyLibrary(t *testing.T) {
	s := newTestSchedulerWith(t, 0)
	if got := s.Pick(); got != nil {
		t.Fatalf("Pick on empty library = %v, want nil", got)
	}
}
