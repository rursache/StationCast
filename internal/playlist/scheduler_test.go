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

// Sequential mode indexes into Library.Snapshot to find the track after the
// current one. Snapshot used to range a map, so the "next" track was drawn
// from a freshly randomised order on every call and sequential mode was
// indistinguishable from shuffle
func TestSequentialModeWalksLibraryInOrder(t *testing.T) {
	const n = 8
	s := newTestSchedulerWith(t, n)
	if err := s.SetMode(ModeSequential); err != nil {
		t.Fatal(err)
	}

	want := s.lib.Snapshot()
	if len(want) != n {
		t.Fatalf("library has %d tracks, want %d", len(want), n)
	}

	// Two full laps, so the wrap from the last track back to the first is
	// covered as well as the straight-line walk
	for lap := 0; lap < 2; lap++ {
		for i, expected := range want {
			got := s.Pick()
			if got == nil {
				t.Fatalf("lap %d: Pick at index %d returned nil", lap, i)
			}
			if got.Path != expected.Path {
				t.Fatalf("lap %d index %d: Pick = %q, want %q", lap, i, got.Path, expected.Path)
			}
			s.MarkPlaying(got)
		}
	}
}

// Peek must agree with Pick in sequential mode, otherwise the admin "up next"
// label shows one track and a different one plays
func TestSequentialPeekMatchesPick(t *testing.T) {
	const n = 6
	s := newTestSchedulerWith(t, n)
	if err := s.SetMode(ModeSequential); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < n+2; i++ {
		peeked := s.Peek()
		picked := s.Pick()
		if picked == nil {
			t.Fatalf("Pick at %d returned nil", i)
		}
		if peeked == nil {
			t.Fatalf("Peek at %d returned nil while Pick returned %q", i, picked.Path)
		}
		if peeked.Path != picked.Path {
			t.Errorf("step %d: Peek = %q but Pick = %q", i, peeked.Path, picked.Path)
		}
		s.MarkPlaying(picked)
	}
}

func TestLoopModeRepeatsCurrentTrack(t *testing.T) {
	s := newTestSchedulerWith(t, 4)
	if err := s.SetMode(ModeLoop); err != nil {
		t.Fatal(err)
	}

	first := s.Pick()
	if first == nil {
		t.Fatal("Pick returned nil")
	}
	s.MarkPlaying(first)

	for i := 0; i < 3; i++ {
		got := s.Pick()
		if got == nil || got.ID != first.ID {
			t.Fatalf("loop repeat %d = %v, want track %d", i, got, first.ID)
		}
		s.MarkPlaying(got)
	}
}
