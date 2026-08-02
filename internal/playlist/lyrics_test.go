package playlist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

// pointProviders redirects the provider chain at local servers for the
// duration of a test
func pointProviders(t *testing.T, get, search, ovh string) {
	t.Helper()
	og, os_, oo := lrclibGetURL, lrclibSearchURL, lyricsOvhURL
	lrclibGetURL, lrclibSearchURL, lyricsOvhURL = get, search, ovh
	t.Cleanup(func() { lrclibGetURL, lrclibSearchURL, lyricsOvhURL = og, os_, oo })
}

func lyricsTestServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

// LRCLIB stays primary: when it answers, the fallback is never consulted
func TestLRCLIBWinsOverFallback(t *testing.T) {
	var ovhCalled bool
	lrclib := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"trackName": "Yellow", "artistName": "Coldplay",
			"syncedLyrics": "[00:01.00]synced words", "plainLyrics": "plain words",
		})
	})
	ovh := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		ovhCalled = true
		_ = json.NewEncoder(w).Encode(map[string]any{"lyrics": "fallback words"})
	})
	pointProviders(t, lrclib+"/get", lrclib+"/search", ovh)

	lib := &Library{}
	res, err := lib.lookupLyrics(context.Background(), &Track{Artist: "Coldplay", Title: "Yellow"})
	if err != nil {
		t.Fatalf("lookupLyrics: %v", err)
	}
	if res.source != "lrclib" {
		t.Errorf("answered by %q, want the primary provider", res.source)
	}
	if res.SyncedLyrics == "" {
		t.Error("the synced lyrics from the primary provider were dropped")
	}
	if ovhCalled {
		t.Error("the fallback was called even though the primary answered")
	}
}

// The fallback exists because its catalogue differs, so a miss on the
// primary must reach it
func TestFallbackUsedWhenPrimaryHasNothing(t *testing.T) {
	lrclib := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			_, _ = w.Write([]byte("[]"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	ovh := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"lyrics": "fallback words"})
	})
	pointProviders(t, lrclib+"/get", lrclib+"/search", ovh)

	lib := &Library{}
	res, err := lib.lookupLyrics(context.Background(), &Track{Artist: "Nicolae Guta", Title: "Abracadabra"})
	if err != nil {
		t.Fatalf("lookupLyrics: %v", err)
	}
	if res.source != "lyrics.ovh" {
		t.Errorf("answered by %q, want the fallback", res.source)
	}
	if res.SyncedLyrics != "" {
		t.Error("the fallback claimed synced lyrics, which it cannot provide")
	}
	if res.PlainLyrics != "fallback words" {
		t.Errorf("PlainLyrics = %q", res.PlainLyrics)
	}
}

// Both providers missing is a definitive answer, not an error to retry
func TestBothProvidersMissing(t *testing.T) {
	miss := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			_, _ = w.Write([]byte("[]"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	pointProviders(t, miss+"/get", miss+"/search", miss)

	lib := &Library{}
	_, err := lib.lookupLyrics(context.Background(), &Track{Artist: "Nobody", Title: "Nothing"})
	if !errors.Is(err, errNoLyricsMatch) {
		t.Errorf("error = %v, want errNoLyricsMatch so the track is not retried forever", err)
	}
}

// lyrics.ovh answers 200 with an error field in some cases rather than 404
func TestFallbackTreatsErrorBodyAsMiss(t *testing.T) {
	miss := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			_, _ = w.Write([]byte("[]"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	ovh := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "No lyrics found"})
	})
	pointProviders(t, miss+"/get", miss+"/search", ovh)

	lib := &Library{}
	if _, err := lib.lookupLyrics(context.Background(), &Track{Artist: "A", Title: "B"}); !errors.Is(err, errNoLyricsMatch) {
		t.Errorf("error = %v, want errNoLyricsMatch", err)
	}
}

// A provider being down is transient, so the track must stay open for retry
func TestProviderOutageIsTransient(t *testing.T) {
	down := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	pointProviders(t, down, down, down)

	lib := &Library{}
	_, err := lib.lookupLyrics(context.Background(), &Track{Artist: "A", Title: "B"})
	if err == nil {
		t.Fatal("an outage returned no error")
	}
	if errors.Is(err, errNoLyricsMatch) {
		t.Error("an outage was recorded as a definitive miss, so the track would never be retried")
	}
}

// Artist and title go in the URL path for lyrics.ovh, so anything with a
// slash or a space has to survive escaping
func TestFallbackEscapesPathSegments(t *testing.T) {
	var gotPath string
	ovh := lyricsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.RequestURI
		_ = json.NewEncoder(w).Encode(map[string]any{"lyrics": "words"})
	})
	pointProviders(t, "", "", ovh)

	lib := &Library{}
	res, err := lib.queryLyricsOvh(context.Background(), "AC/DC", "Highway to Hell")
	if err != nil {
		t.Fatalf("queryLyricsOvh: %v", err)
	}
	if res == nil || res.PlainLyrics == "" {
		t.Fatal("no lyrics returned")
	}
	if !strings.Contains(gotPath, "AC%2FDC") {
		t.Errorf("path %q did not escape the slash in the artist", gotPath)
	}
}

func TestFallbackSkipsEmptyFields(t *testing.T) {
	lib := &Library{}
	for _, c := range []struct{ artist, title string }{{"", "x"}, {"x", ""}, {"", ""}} {
		if _, err := lib.queryLyricsOvh(context.Background(), c.artist, c.title); !errors.Is(err, errNoLyricsMatch) {
			t.Errorf("queryLyricsOvh(%q, %q) error = %v, want errNoLyricsMatch", c.artist, c.title, err)
		}
	}
}

// --- FetchLyrics end to end, against local providers ------------------

// newFetchEnv gives a library with one indexed track and the provider chain
// pointed at handlers the test controls
func newFetchEnv(t *testing.T, lrclibH, ovhH http.HandlerFunc) (*Library, *Track, string) {
	t.Helper()
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
	tr.Artist, tr.Title, tr.DurationMS = "Coldplay", "Yellow", 267_000

	if lrclibH == nil {
		lrclibH = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }
	}
	if ovhH == nil {
		ovhH = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }
	}
	lr := lyricsTestServer(t, lrclibH)
	ov := lyricsTestServer(t, ovhH)
	pointProviders(t, lr+"/get", lr+"/search", ov)
	return lib, tr, data
}

func lyricsState(t *testing.T, lib *Library, id int64) (hasLyrics, tried int) {
	t.Helper()
	if err := lib.db.QueryRow(`SELECT has_lyrics, lyrics_tried FROM tracks WHERE id=?`, id).Scan(&hasLyrics, &tried); err != nil {
		t.Fatal(err)
	}
	return
}

func TestFetchLyricsCachesSyncedResult(t *testing.T) {
	lib, tr, data := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"trackName": "Yellow", "artistName": "Coldplay", "duration": 267.0,
			"syncedLyrics": "[00:33.80]Look at the stars", "plainLyrics": "Look at the stars",
		})
	}, nil)

	lib.FetchLyrics(context.Background(), tr)

	body, err := os.ReadFile(lyricsPath(data, tr.ID))
	if err != nil {
		t.Fatalf("nothing cached: %v", err)
	}
	// the synced version must win when both are offered, since that is the
	// only one the frontend can highlight against
	if !strings.Contains(string(body), "[00:33.80]") {
		t.Errorf("cached the plain version over the synced one: %q", body)
	}
	has, tried := lyricsState(t, lib, tr.ID)
	if has != 1 {
		t.Error("has_lyrics not set, so the button would never appear")
	}
	if tried == 0 {
		t.Error("lyrics_tried not set, so the track would be looked up again every play")
	}
	if got, _ := lib.Get(tr.ID); !got.HasLyrics {
		t.Error("in-memory HasLyrics not set, so now-playing would not advertise it")
	}
}

func TestFetchLyricsCachesPlainWhenNoSynced(t *testing.T) {
	lib, tr, data := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"trackName": "Yellow", "duration": 267.0, "plainLyrics": "just words",
		})
	}, nil)

	lib.FetchLyrics(context.Background(), tr)

	body, err := os.ReadFile(lyricsPath(data, tr.ID))
	if err != nil {
		t.Fatalf("nothing cached: %v", err)
	}
	if string(body) != "just words" {
		t.Errorf("cached %q", body)
	}
}

// An instrumental has no lyrics by definition. Recording the attempt matters
// so it is not asked for again on every play
func TestFetchLyricsHandlesInstrumental(t *testing.T) {
	lib, tr, data := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"trackName": "Xtal", "duration": 267.0, "instrumental": true,
		})
	}, nil)

	lib.FetchLyrics(context.Background(), tr)

	if _, err := os.Stat(lyricsPath(data, tr.ID)); !os.IsNotExist(err) {
		t.Error("an instrumental was cached")
	}
	has, tried := lyricsState(t, lib, tr.ID)
	if has != 0 {
		t.Error("has_lyrics set for an instrumental")
	}
	if tried == 0 {
		t.Error("an instrumental was not recorded as tried, so it is asked for on every play")
	}
}

// A 200 carrying empty fields is a miss, not a cache of nothing
func TestFetchLyricsHandlesEmptyBody(t *testing.T) {
	lib, tr, data := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"trackName": "Yellow", "duration": 267.0})
	}, nil)

	lib.FetchLyrics(context.Background(), tr)

	if _, err := os.Stat(lyricsPath(data, tr.ID)); !os.IsNotExist(err) {
		t.Error("an empty result was cached")
	}
	if has, _ := lyricsState(t, lib, tr.ID); has != 0 {
		t.Error("has_lyrics set despite there being no lyrics")
	}
}

// A track already looked up must not touch the network again
func TestFetchLyricsSkipsAlreadyTried(t *testing.T) {
	var calls int
	lib, tr, _ := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}, nil)

	lib.markLyricsTried(tr.ID)
	lib.FetchLyrics(context.Background(), tr)

	if calls != 0 {
		t.Errorf("made %d request(s) for a track already looked up", calls)
	}
}

// If the cache cannot be written, has_lyrics must stay clear. Otherwise the
// button appears and the endpoint 404s
func TestFetchLyricsDoesNotClaimLyricsItCouldNotCache(t *testing.T) {
	lib, tr, data := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"trackName": "Yellow", "duration": 267.0, "plainLyrics": "words",
		})
	}, nil)

	// make the cache directory unwritable
	dir := filepath.Join(data, "lyrics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make the directory read only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("running as root, permissions are not enforced")
	}

	lib.FetchLyrics(context.Background(), tr)

	if has, _ := lyricsState(t, lib, tr.ID); has != 0 {
		t.Error("has_lyrics was set even though the cache write failed")
	}
	if got, _ := lib.Get(tr.ID); got.HasLyrics {
		t.Error("in-memory HasLyrics set even though the cache write failed")
	}
}

// A response far larger than any real lyric must not fill the disk
func TestFetchLyricsTruncatesOversizeResponse(t *testing.T) {
	huge := strings.Repeat("a", maxLyricsBytes*2)
	lib, tr, data := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"trackName": "Yellow", "duration": 267.0, "plainLyrics": huge,
		})
	}, nil)

	lib.FetchLyrics(context.Background(), tr)

	if st, err := os.Stat(lyricsPath(data, tr.ID)); err == nil && st.Size() > maxLyricsBytes {
		t.Errorf("cached %d bytes, over the %d cap", st.Size(), maxLyricsBytes)
	}
}

// The fallback provider must be reached through the full FetchLyrics path,
// not just the lookup helper
func TestFetchLyricsReachesFallbackProvider(t *testing.T) {
	lib, tr, data := newFetchEnv(t,
		func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "search") {
				_, _ = w.Write([]byte("[]"))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"lyrics": "fallback words"})
		})

	lib.FetchLyrics(context.Background(), tr)

	body, err := os.ReadFile(lyricsPath(data, tr.ID))
	if err != nil {
		t.Fatalf("the fallback result was not cached: %v", err)
	}
	if string(body) != "fallback words" {
		t.Errorf("cached %q", body)
	}
}

// An outage must leave the track open, so it is picked up once the network
// comes back rather than being written off
func TestFetchLyricsLeavesTrackOpenAfterOutage(t *testing.T) {
	lib, tr, _ := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	lib.FetchLyrics(context.Background(), tr)

	if _, tried := lyricsState(t, lib, tr.ID); tried != 0 {
		t.Error("an outage marked the track as tried, so it would never be retried")
	}
}

// Both providers definitively answering no is worth recording, so the track
// is not asked for on every play forever
func TestFetchLyricsRecordsDefinitiveMiss(t *testing.T) {
	lib, tr, _ := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			_, _ = w.Write([]byte("[]"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}, nil)

	lib.FetchLyrics(context.Background(), tr)

	if _, tried := lyricsState(t, lib, tr.ID); tried == 0 {
		t.Error("a definitive miss was not recorded")
	}
}

// A malformed body is not a reason to write the track off permanently
func TestFetchLyricsHandlesMalformedResponse(t *testing.T) {
	lib, tr, data := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("also not json"))
	})

	lib.FetchLyrics(context.Background(), tr)

	if _, err := os.Stat(lyricsPath(data, tr.ID)); !os.IsNotExist(err) {
		t.Error("garbage was cached")
	}
	if _, tried := lyricsState(t, lib, tr.ID); tried != 0 {
		t.Error("a malformed response was treated as a definitive miss")
	}
}

// A track that has left the library between the lookup starting and
// finishing must not panic the fetch
func TestFetchLyricsForTrackNoLongerInLibrary(t *testing.T) {
	lib, tr, _ := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"trackName": "Yellow", "duration": 267.0, "plainLyrics": "words",
		})
	}, nil)

	ghost := &Track{ID: 999999, Artist: tr.Artist, Title: tr.Title}
	lib.FetchLyrics(context.Background(), ghost) // must not panic
}

// The search fallback is the path that finds records filed under a whole
// "ARTIST - Title" string, so its success case needs covering directly
func TestSearchFallbackSucceedsWhenExactMisses(t *testing.T) {
	var sawExact, sawSearch bool
	lib, tr, data := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			sawSearch = true
			// the shape LRCLIB actually stores for a ripped upload
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"trackName": "COLDPLAY - Yellow (Official Video)", "artistName": "Coldplay",
				"duration": 267.0, "syncedLyrics": "[00:33.80]Look at the stars",
			}})
			return
		}
		sawExact = true
		w.WriteHeader(http.StatusNotFound)
	}, nil)

	lib.FetchLyrics(context.Background(), tr)

	if !sawExact || !sawSearch {
		t.Fatalf("expected the exact endpoint then search, got exact=%v search=%v", sawExact, sawSearch)
	}
	body, err := os.ReadFile(lyricsPath(data, tr.ID))
	if err != nil {
		t.Fatalf("the search result was not cached: %v", err)
	}
	if !strings.Contains(string(body), "[00:33.80]") {
		t.Errorf("cached %q, want the synced body from search", body)
	}
	if has, _ := lyricsState(t, lib, tr.ID); has != 1 {
		t.Error("has_lyrics not set from a search result")
	}
}

// A candidate whose length is nowhere near the track must be rejected even
// though search returned it, because wrong lyrics are worse than none
func TestSearchFallbackRejectsWrongLengthCandidate(t *testing.T) {
	lib, tr, data := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "search") {
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"trackName": "Yellow", "artistName": "Coldplay",
				"duration": 900.0, "syncedLyrics": "[00:01.00]wrong song",
			}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}, nil)

	lib.FetchLyrics(context.Background(), tr) // track is 267s, candidate is 900s

	if _, err := os.Stat(lyricsPath(data, tr.ID)); !os.IsNotExist(err) {
		t.Error("a candidate 10 minutes from the track length was accepted")
	}
}

// A title made entirely of release decoration cleans to nothing, and asking
// for an empty title would match anything. The raw title is used instead
func TestTitleThatCleansToNothingFallsBackToRaw(t *testing.T) {
	var askedTitles []string
	lib, tr, _ := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		// only the exact endpoint carries track_name, search sends q
		if !strings.Contains(r.URL.Path, "search") {
			askedTitles = append(askedTitles, r.URL.Query().Get("track_name"))
		}
		w.WriteHeader(http.StatusNotFound)
	}, nil)
	tr.Title = "(Official Video)"

	lib.FetchLyrics(context.Background(), tr)

	if len(askedTitles) == 0 {
		t.Fatal("no lookup was attempted")
	}
	for _, got := range askedTitles {
		if got == "" {
			t.Error("asked for an empty title, which would match an arbitrary track")
		}
	}
}

// The album narrows an exact match, so it must be sent when known
func TestAlbumIsSentWhenKnown(t *testing.T) {
	var gotAlbum string
	lib, tr, _ := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if gotAlbum == "" {
			gotAlbum = r.URL.Query().Get("album_name")
		}
		w.WriteHeader(http.StatusNotFound)
	}, nil)
	tr.Album = "Parachutes"

	lib.FetchLyrics(context.Background(), tr)

	if gotAlbum != "Parachutes" {
		t.Errorf("album_name = %q, want the track's album", gotAlbum)
	}
}

// The duration window on the exact endpoint is enforced server side, so an
// implausible value must not be sent at all
func TestImplausibleDurationIsNotSent(t *testing.T) {
	for _, durMS := range []int64{0, -5000, 4000 * 1000} {
		var sent string
		lib, tr, _ := newFetchEnv(t, func(w http.ResponseWriter, r *http.Request) {
			if sent == "" {
				sent = r.URL.Query().Get("duration")
			}
			w.WriteHeader(http.StatusNotFound)
		}, nil)
		tr.DurationMS = durMS

		lib.FetchLyrics(context.Background(), tr)

		if sent != "" {
			t.Errorf("duration %dms was sent as %q, outside the range the endpoint accepts", durMS, sent)
		}
	}
}

func TestWriteLyricsFailsWhenDirectoryCannotBeCreated(t *testing.T) {
	data := t.TempDir()
	// a file where the lyrics directory needs to be
	if err := os.WriteFile(filepath.Join(data, "lyrics"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeLyrics(data, 1, "words"); err == nil {
		t.Error("writeLyrics reported success despite being unable to create its directory")
	}
}
