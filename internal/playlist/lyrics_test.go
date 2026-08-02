package playlist

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLyricsPath(t *testing.T) {
	got := lyricsPath(filepath.FromSlash("/data"), 42)
	want := filepath.Join(filepath.FromSlash("/data"), "lyrics", "42.lrc")
	if got != want {
		t.Errorf("lyricsPath = %q, want %q", got, want)
	}
}

func TestWriteAndReadLyrics(t *testing.T) {
	music, data := t.TempDir(), t.TempDir()
	lib := newTestLibrary(t, music, data)

	body := "[00:12.30] first line\n[00:15.00] second line\n"
	if err := writeLyrics(data, 7, body); err != nil {
		t.Fatalf("writeLyrics: %v", err)
	}

	got, err := lib.ReadLyrics(7)
	if err != nil {
		t.Fatalf("ReadLyrics: %v", err)
	}
	if got != body {
		t.Errorf("round trip = %q, want %q", got, body)
	}
}

func TestReadLyricsMissing(t *testing.T) {
	lib := newTestLibrary(t, t.TempDir(), t.TempDir())
	if _, err := lib.ReadLyrics(999); err == nil {
		t.Error("ReadLyrics for an uncached track returned no error")
	}
}

// A body larger than the cap is truncated rather than written whole, so a
// pathological response cannot fill the disk
func TestWriteLyricsCapsSize(t *testing.T) {
	data := t.TempDir()
	if err := writeLyrics(data, 1, strings.Repeat("x", maxLyricsBytes+5000)); err != nil {
		t.Fatalf("writeLyrics: %v", err)
	}
	st, err := os.Stat(lyricsPath(data, 1))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > maxLyricsBytes {
		t.Errorf("cached %d bytes, over the %d cap", st.Size(), maxLyricsBytes)
	}
}

// writeLyrics renames into place so a concurrent reader never sees a
// half-written file, which also means no .tmp is left behind
func TestWriteLyricsLeavesNoTempFile(t *testing.T) {
	data := t.TempDir()
	if err := writeLyrics(data, 3, "some lyrics"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(data, "lyrics"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %q left behind", e.Name())
		}
	}
}

// The cache must not outlive the track, or it grows without bound as a
// library churns
func TestRemoveTrackDeletesCachedLyrics(t *testing.T) {
	music, data := t.TempDir(), t.TempDir()
	p := filepath.Join(music, "song.mp3")
	if err := os.WriteFile(p, []byte("not really mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := newTestLibrary(t, music, data)
	if err := lib.upsertFile(p); err != nil {
		t.Fatal(err)
	}
	tr, _ := lib.GetByPath(p)

	if err := writeLyrics(data, tr.ID, "[00:01.00] a line"); err != nil {
		t.Fatal(err)
	}
	cached := lyricsPath(data, tr.ID)
	if _, err := os.Stat(cached); err != nil {
		t.Fatalf("setup failed, no cached lyrics: %v", err)
	}

	lib.removeTrack(tr)

	if _, err := os.Stat(cached); !os.IsNotExist(err) {
		t.Errorf("cached lyrics survived removeTrack (stat err %v)", err)
	}
}

// Replacing the bytes behind a path may mean a different song, so the lyrics
// cached for the old one no longer describe it
func TestContentChangeInvalidatesLyrics(t *testing.T) {
	music, data := t.TempDir(), t.TempDir()
	p := filepath.Join(music, "song.mp3")
	if err := os.WriteFile(p, []byte("first recording"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := newTestLibrary(t, music, data)
	if err := lib.upsertFile(p); err != nil {
		t.Fatal(err)
	}
	tr, _ := lib.GetByPath(p)

	if err := writeLyrics(data, tr.ID, "[00:01.00] old song words"); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.db.Exec(`UPDATE tracks SET has_lyrics=1, lyrics_tried=? WHERE id=?`, lyricsLookupVersion, tr.ID); err != nil {
		t.Fatal(err)
	}
	lib.mu.Lock()
	lib.byID[tr.ID].HasLyrics = true
	lib.mu.Unlock()

	if err := os.WriteFile(p, []byte("a completely different recording"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := lib.upsertFile(p); err != nil {
		t.Fatalf("upsertFile after replacement: %v", err)
	}

	if _, err := os.Stat(lyricsPath(data, tr.ID)); !os.IsNotExist(err) {
		t.Errorf("stale lyrics still cached (stat err %v)", err)
	}
	var hasLyrics, tried int
	if err := lib.db.QueryRow(`SELECT has_lyrics, lyrics_tried FROM tracks WHERE id=?`, tr.ID).Scan(&hasLyrics, &tried); err != nil {
		t.Fatal(err)
	}
	if hasLyrics != 0 {
		t.Error("has_lyrics still set after the file was replaced")
	}
	if tried != 0 {
		t.Error("lyrics_tried still set, so the new song would never be looked up")
	}
	if got, _ := lib.Get(tr.ID); got.HasLyrics {
		t.Error("in-memory HasLyrics still set after the file was replaced")
	}
}

func TestLyricsTriedRoundTrip(t *testing.T) {
	music, data := t.TempDir(), t.TempDir()
	p := filepath.Join(music, "song.mp3")
	if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := newTestLibrary(t, music, data)
	if err := lib.upsertFile(p); err != nil {
		t.Fatal(err)
	}
	tr, _ := lib.GetByPath(p)

	if lib.lyricsAlreadyTried(tr.ID) {
		t.Error("a fresh track reports as already tried")
	}
	lib.markLyricsTried(tr.ID)
	if !lib.lyricsAlreadyTried(tr.ID) {
		t.Error("marking the track tried did not stick, so it would be looked up on every play")
	}
}

// With the integration off, nothing should reach the network and nothing
// should be recorded
func TestFetchLyricsIsNoOpWhenDisabled(t *testing.T) {
	music, data := t.TempDir(), t.TempDir()
	lib := newTestLibrary(t, music, data)
	lib.cfg.Lyrics = false

	lib.FetchLyrics(context.Background(), &Track{ID: 1, Artist: "Coldplay", Title: "Yellow"})

	if _, err := os.Stat(lyricsPath(data, 1)); !os.IsNotExist(err) {
		t.Error("lyrics were cached while the integration is disabled")
	}
}

func TestFetchLyricsSkipsTracksWithNothingToMatchOn(t *testing.T) {
	music, data := t.TempDir(), t.TempDir()
	lib := newTestLibrary(t, music, data)
	lib.cfg.Lyrics = true

	// No artist or title means no way to identify the song, so there is
	// nothing worth asking LRCLIB
	for _, tr := range []*Track{
		nil,
		{ID: 1, Artist: "", Title: "Yellow"},
		{ID: 2, Artist: "Coldplay", Title: ""},
	} {
		lib.FetchLyrics(context.Background(), tr)
	}

	entries, err := os.ReadDir(filepath.Join(data, "lyrics"))
	if err == nil && len(entries) > 0 {
		t.Errorf("cached %d files for tracks with no artist or title", len(entries))
	}
}

// A transient failure must leave the track open for another attempt. Marking
// it tried on any error meant a NAS that booted before its network was up
// would never fetch lyrics for the tracks it played in that window
func TestTransientFailureDoesNotMarkTrackAsTried(t *testing.T) {
	music, data := t.TempDir(), t.TempDir()
	p := filepath.Join(music, "song.mp3")
	if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := newTestLibrary(t, music, data)
	lib.cfg.Lyrics = true
	if err := lib.upsertFile(p); err != nil {
		t.Fatal(err)
	}
	tr, _ := lib.GetByPath(p)
	tr.Artist, tr.Title = "Coldplay", "Yellow"

	// A cancelled context stands in for the network being unreachable
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lib.FetchLyrics(ctx, tr)

	if lib.lyricsAlreadyTried(tr.ID) {
		t.Error("a transient failure marked the track as tried, so it would never be retried")
	}
}

// Only one lookup per track may be in flight. Loop mode on a short track can
// start the next play before a slow request finishes
func TestFetchLyricsSkipsWhenAlreadyInFlight(t *testing.T) {
	music, data := t.TempDir(), t.TempDir()
	p := filepath.Join(music, "song.mp3")
	if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := newTestLibrary(t, music, data)
	lib.cfg.Lyrics = true
	if err := lib.upsertFile(p); err != nil {
		t.Fatal(err)
	}
	tr, _ := lib.GetByPath(p)
	tr.Artist, tr.Title = "Coldplay", "Yellow"

	// Stand in for a request already running for this track
	lib.lyricsInflight.Store(tr.ID, struct{}{})
	defer lib.lyricsInflight.Delete(tr.ID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lib.FetchLyrics(ctx, tr) // must return immediately without a second lookup

	if _, busy := lib.lyricsInflight.Load(tr.ID); !busy {
		t.Error("the in-flight marker was cleared by a call that should have been skipped")
	}
}
