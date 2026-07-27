package playlist

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

// queueFromDB reads the persisted queue in position order, which is exactly
// what Restore() replays into s.manual on the next start
func queueFromDB(t *testing.T, s *Scheduler) []int64 {
	t.Helper()
	rows, err := s.db.Query(`SELECT track_id FROM queue ORDER BY position ASC`)
	if err != nil {
		t.Fatalf("query queue: %v", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func memoryQueue(s *Scheduler) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.manual...)
}

func assertQueuesAgree(t *testing.T, s *Scheduler) {
	t.Helper()
	mem := memoryQueue(s)
	disk := queueFromDB(t, s)
	if len(mem) != len(disk) {
		t.Fatalf("queue length mismatch: memory has %d, db has %d (%v vs %v)", len(mem), len(disk), mem, disk)
	}
	for i := range mem {
		if mem[i] != disk[i] {
			t.Fatalf("queue diverged at position %d: memory %d, db %d (%v vs %v)", i, mem[i], disk[i], mem, disk)
		}
	}
}

func TestEnqueuePersistsImmediately(t *testing.T) {
	s := newTestSchedulerWith(t, 5)
	tracks := s.lib.Snapshot()

	for _, tr := range tracks {
		if err := s.Enqueue(tr.ID); err != nil {
			t.Fatalf("Enqueue(%d): %v", tr.ID, err)
		}
	}
	assertQueuesAgree(t, s)

	if err := s.Dequeue(2); err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	assertQueuesAgree(t, s)
}

// Every mutation path must rewrite the queue table from s.manual while
// holding s.mu, so the table always reflects the whole authoritative queue.
// Enqueue used to append a single INSERT computed from a position it had
// read earlier, which is what let a concurrent rewrite and a pending insert
// disagree. Seeding a row that is not in s.manual pins that down without
// depending on timing: a full rewrite erases it, a bare INSERT leaves it
func TestEnqueueRewritesWholeQueueTable(t *testing.T) {
	s := newTestSchedulerWith(t, 4)
	tracks := s.lib.Snapshot()

	if err := s.Enqueue(tracks[0].ID); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// A row the in-memory queue knows nothing about, exactly the shape a
	// racing insert leaves behind
	if _, err := s.db.Exec(`INSERT INTO queue(track_id, position) VALUES(?, ?)`, tracks[3].ID, 99); err != nil {
		t.Fatalf("seed stray row: %v", err)
	}

	if err := s.Enqueue(tracks[1].ID); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	assertQueuesAgree(t, s)

	// Positions must stay contiguous from 1, since Restore orders by them
	rows, err := s.db.Query(`SELECT position FROM queue ORDER BY position ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var positions []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		positions = append(positions, p)
	}
	for i, p := range positions {
		if p != i+1 {
			t.Fatalf("positions not contiguous from 1: %v", positions)
		}
	}
}

// Dequeue and Pick must have the same property
func TestDequeueAndPickRewriteWholeQueueTable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drain func(s *Scheduler)
	}{
		{"Dequeue", func(s *Scheduler) { _ = s.Dequeue(0) }},
		{"Pick", func(s *Scheduler) { _ = s.Pick() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSchedulerWith(t, 4)
			tracks := s.lib.Snapshot()
			for _, tr := range tracks[:3] {
				if err := s.Enqueue(tr.ID); err != nil {
					t.Fatalf("Enqueue: %v", err)
				}
			}
			if _, err := s.db.Exec(`INSERT INTO queue(track_id, position) VALUES(?, ?)`, tracks[3].ID, 99); err != nil {
				t.Fatalf("seed stray row: %v", err)
			}

			tc.drain(s)
			assertQueuesAgree(t, s)
		})
	}
}

// checkQueueInvariant samples the in-memory queue and the persisted queue
// while holding s.mu. Every mutation path holds s.mu across both the slice
// update and the table rewrite, so under that lock the two are never allowed
// to disagree. Comparing only after the load settles is not enough: a stray
// row is healed by the next full rewrite, so the damage is usually gone by
// the time the goroutines finish
func checkQueueInvariant(s *Scheduler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mem := append([]int64(nil), s.manual...)

	rows, err := s.db.Query(`SELECT track_id FROM queue ORDER BY position ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var disk []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		disk = append(disk, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(mem) != len(disk) {
		return fmt.Errorf("queue length mismatch, memory has %d, db has %d (%v vs %v)", len(mem), len(disk), mem, disk)
	}
	for i := range mem {
		if mem[i] != disk[i] {
			return fmt.Errorf("queue diverged at position %d, memory %d, db %d (%v vs %v)", i, mem[i], disk[i], mem, disk)
		}
	}
	return nil
}

// Enqueue used to append to s.manual under the lock and then INSERT after
// releasing it, while Dequeue and Pick rewrote the whole table from a
// snapshot. Interleaving the two either duplicated a row or wiped one, and
// the damage only became visible after a restart replayed the queue table
func TestQueuePersistenceUnderConcurrentMutation(t *testing.T) {
	const (
		n      = 24
		rounds = 20
	)
	s := newTestSchedulerWith(t, n)
	tracks := s.lib.Snapshot()

	stop := make(chan struct{})
	failures := make(chan error, 1)

	// Sample the invariant continuously rather than once at the end
	var checker sync.WaitGroup
	checker.Add(1)
	go func() {
		defer checker.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := checkQueueInvariant(s); err != nil {
				select {
				case failures <- err:
				default:
				}
				return
			}
		}
	}()

	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		for _, tr := range tracks {
			wg.Add(1)
			go func(id int64) {
				defer wg.Done()
				_ = s.Enqueue(id)
			}(tr.ID)
		}
		// Drain concurrently with the enqueues so the two paths genuinely
		// race. Pick drains the queue the same way Dequeue does, so both
		// consumers are represented
		for i := 0; i < n/2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = s.Dequeue(0)
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				if picked := s.Pick(); picked != nil {
					s.MarkPlaying(picked)
				}
			}()
		}
		wg.Wait()
	}

	close(stop)
	checker.Wait()

	select {
	case err := <-failures:
		t.Fatalf("queue invariant violated: %v", err)
	default:
	}

	// Whatever survived the load must still round-trip
	assertQueuesAgree(t, s)
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
