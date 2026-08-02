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

// Tags ripped from YouTube carry the artist and the release decoration inside
// the title, which no lyrics database indexes that way
func TestCleanTitle(t *testing.T) {
	cases := []struct{ title, artist, want string }{
		{"Mama", "Jador", "Mama"},
		{"Jador - Mama", "Jador", "Mama"},
		{"Jador - Mama | Official Video", "Jador", "Mama"},
		{"Mama (Official Video)", "Jador", "Mama"},
		{"Mama [Official Audio]", "Jador", "Mama"},
		{"Mama (Official Lyric Video)", "Jador", "Mama"},
		{"Mama - Jador", "Jador", "Mama"},
		{"JADOR - Mama", "Jador", "Mama"}, // case insensitive prefix
		// the individual artist of a collaboration also counts as a prefix
		{"Kalif - Fata Care M-a Dat Gata", "Kalif x Luis Gabriel", "Fata Care M-a Dat Gata"},
		// a title that is only decoration must not be reduced to nothing
		{"Yellow", "Coldplay", "Yellow"},
		// parentheses that are part of the song stay
		{"Everything (I Do)", "Bryan Adams", "Everything (I Do)"},
	}
	for _, c := range cases {
		if got := cleanTitle(c.title, c.artist); got != c.want {
			t.Errorf("cleanTitle(%q, %q) = %q, want %q", c.title, c.artist, got, c.want)
		}
	}
}

// LRCLIB files a collaboration under one artist, so every credited form has
// to be tried. This is what made "Kalif x Luis Gabriel" resolvable
func TestArtistCandidates(t *testing.T) {
	cases := []struct {
		artist string
		want   []string
	}{
		{"Kalif x Luis Gabriel", []string{"Kalif x Luis Gabriel", "Kalif", "Luis Gabriel"}},
		{"Nicolae Guta si Modjo", []string{"Nicolae Guta si Modjo", "Nicolae Guta", "Modjo"}},
		{"A feat. B", []string{"A feat. B", "A", "B"}},
		{"A ft. B", []string{"A ft. B", "A", "B"}},
		{"A & B", []string{"A & B", "A", "B"}},
		{"A, B", []string{"A, B", "A", "B"}},
		{"Coldplay", []string{"Coldplay"}},
		{"", nil},
	}
	for _, c := range cases {
		got := artistCandidates(c.artist)
		if len(got) != len(c.want) {
			t.Errorf("artistCandidates(%q) = %v, want %v", c.artist, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("artistCandidates(%q)[%d] = %q, want %q", c.artist, i, got[i], c.want[i])
			}
		}
	}
	// the full credit must always be tried first, since it is the most precise
	if got := artistCandidates("A x B"); got[0] != "A x B" {
		t.Errorf("full credit is not tried first: %v", got)
	}
}

// Search results carry no duration filter server-side, so a wrong-length
// result must be rejected rather than shown. Wrong lyrics are worse than none
func TestPickLyricsCandidate(t *testing.T) {
	synced := lrclibResponse{TrackName: "right", Duration: 200, SyncedLyrics: "[00:01.00]a"}
	plain := lrclibResponse{TrackName: "plain", Duration: 200, PlainLyrics: "words"}
	farOff := lrclibResponse{TrackName: "wrong length", Duration: 900, SyncedLyrics: "[00:01.00]a"}
	instrumental := lrclibResponse{TrackName: "instrumental", Duration: 200, Instrumental: true}
	empty := lrclibResponse{TrackName: "empty", Duration: 200}

	want := int64(200_000)
	if got := pickLyricsCandidate([]lrclibResponse{plain, synced}, want); got == nil || got.TrackName != "right" {
		t.Errorf("a synced result should win over a plain one, got %v", got)
	}
	if got := pickLyricsCandidate([]lrclibResponse{plain}, want); got == nil || got.TrackName != "plain" {
		t.Errorf("a plain result should be used when nothing is synced, got %v", got)
	}
	if got := pickLyricsCandidate([]lrclibResponse{farOff}, want); got != nil {
		t.Errorf("a result %v seconds out was accepted", farOff.Duration)
	}
	if got := pickLyricsCandidate([]lrclibResponse{instrumental, empty}, want); got != nil {
		t.Errorf("an instrumental or empty result was accepted: %v", got)
	}
	if got := pickLyricsCandidate(nil, want); got != nil {
		t.Error("an empty result set produced a candidate")
	}
	// with no known duration the window cannot be applied, so length is not
	// a reason to reject
	if got := pickLyricsCandidate([]lrclibResponse{farOff}, 0); got == nil {
		t.Error("a result was rejected on length despite the duration being unknown")
	}
	// just inside and just outside the window
	edgeIn := lrclibResponse{Duration: float64(200 + lyricsDurationTolerance), SyncedLyrics: "[00:01.00]a"}
	edgeOut := lrclibResponse{Duration: float64(200 + lyricsDurationTolerance + 1), SyncedLyrics: "[00:01.00]a"}
	if pickLyricsCandidate([]lrclibResponse{edgeIn}, want) == nil {
		t.Error("a result exactly on the tolerance boundary was rejected")
	}
	if pickLyricsCandidate([]lrclibResponse{edgeOut}, want) != nil {
		t.Error("a result past the tolerance boundary was accepted")
	}
}
