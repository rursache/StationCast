package playlist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// LRCLIB is the only free lyrics source that returns time-synced lyrics
	// without an API key, so every operator does not need their own account.
	// It matches on artist, title, album and duration
	lrclibGetURL = "https://lrclib.net/api/get"

	// Identifies us to LRCLIB, which asks consumers to say who they are.
	// It must not contain "Jellyfin-Server", a string their edge silently
	// drops connections for
	lrclibUserAgent = "StationCast (https://github.com/rursache/StationCast)"

	// Bumping this reopens tracks whose previous lookup found nothing, the
	// same way itunesLookupVersion does for artwork
	lyricsLookupVersion = 1

	// Lyrics are text. Anything past this is not lyrics
	maxLyricsBytes = 256 << 10

	lyricsTimeout = 10 * time.Second
)

// errNoLyricsMatch is a definitive answer: LRCLIB has nothing for this track.
// Distinguished from a transient failure so a NAS that boots before its
// network is up does not permanently write those tracks off
var errNoLyricsMatch = errors.New("no lyrics match")

// lyricsPath is where a track's cached lyrics live on disk. One file per
// track, holding LRC when a synced version exists and plain text otherwise,
// which the frontend tells apart by looking for timestamps
func lyricsPath(dataDir string, id int64) string {
	return filepath.Join(dataDir, "lyrics", fmt.Sprintf("%d.lrc", id))
}

type lrclibResponse struct {
	ID           int64   `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

// FetchLyrics looks the track up on LRCLIB and caches whatever it returns.
// A no-op when the integration is off, when the track has no artist or title
// to match on, or when this track has already been looked up. Intended to be
// called when a track starts playing, so a library fills in as it is heard
// rather than in one burst at startup
func (l *Library) FetchLyrics(ctx context.Context, t *Track) {
	if !l.cfg.Lyrics || t == nil || t.Artist == "" || t.Title == "" {
		return
	}
	if l.lyricsAlreadyTried(t.ID) {
		return
	}

	// One lookup per track at a time. Loop mode and short tracks can start
	// the next play before a slow request finishes, which would otherwise
	// duplicate the call and race two writers onto the same temp file
	if _, busy := l.lyricsInflight.LoadOrStore(t.ID, struct{}{}); busy {
		return
	}
	defer l.lyricsInflight.Delete(t.ID)

	res, err := l.queryLRCLIB(ctx, t)
	if err != nil {
		// Only a definitive miss is recorded. A timeout, a DNS failure or a
		// 5xx leaves the track open so the next play tries again
		if errors.Is(err, errNoLyricsMatch) {
			l.markLyricsTried(t.ID)
		}
		slog.Debug("lyrics: lookup failed", "id", t.ID, "artist", t.Artist, "title", t.Title, "err", err)
		return
	}
	// The answer was definitive either way from here on
	l.markLyricsTried(t.ID)

	body := res.SyncedLyrics
	if strings.TrimSpace(body) == "" {
		body = res.PlainLyrics
	}
	if res.Instrumental || strings.TrimSpace(body) == "" {
		slog.Debug("lyrics: no usable lyrics", "id", t.ID, "instrumental", res.Instrumental)
		return
	}

	if err := writeLyrics(l.cfg.DataDir, t.ID, body); err != nil {
		slog.Warn("lyrics: cache write failed", "id", t.ID, "err", err)
		return
	}
	if _, err := l.db.Exec(`UPDATE tracks SET has_lyrics = 1 WHERE id = ?`, t.ID); err != nil {
		slog.Warn("lyrics: db update failed", "id", t.ID, "err", err)
		return
	}
	l.mu.Lock()
	if existing, ok := l.byID[t.ID]; ok {
		existing.HasLyrics = true
	}
	l.mu.Unlock()

	slog.Info("lyrics: cached", "id", t.ID, "artist", t.Artist, "title", t.Title,
		"synced", strings.TrimSpace(res.SyncedLyrics) != "")
}

func (l *Library) queryLRCLIB(ctx context.Context, t *Track) (*lrclibResponse, error) {
	q := url.Values{}
	q.Set("artist_name", t.Artist)
	q.Set("track_name", t.Title)
	if t.Album != "" {
		q.Set("album_name", t.Album)
	}
	// LRCLIB matches duration within a couple of seconds, which is what stops
	// a cover or a remaster being served for the wrong recording. It rejects
	// anything outside 1-3600, so only send a plausible value
	if secs := t.DurationMS / 1000; secs >= 1 && secs <= 3600 {
		q.Set("duration", strconv.FormatInt(secs, 10))
	}

	reqCtx, cancel := context.WithTimeout(ctx, lyricsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, lrclibGetURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", lrclibUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errNoLyricsMatch
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lrclib status %d", resp.StatusCode)
	}

	var out lrclibResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLyricsBytes)).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func writeLyrics(dataDir string, id int64, body string) error {
	if len(body) > maxLyricsBytes {
		body = body[:maxLyricsBytes]
	}
	dst := lyricsPath(dataDir, id)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Write and rename so a reader never sees a half-written file
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ReadLyrics returns the cached lyrics for a track, or an error when none
// have been cached
func (l *Library) ReadLyrics(id int64) (string, error) {
	b, err := os.ReadFile(lyricsPath(l.cfg.DataDir, id))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (l *Library) lyricsAlreadyTried(id int64) bool {
	var tried int
	if err := l.db.QueryRow(`SELECT lyrics_tried FROM tracks WHERE id = ?`, id).Scan(&tried); err != nil {
		return false
	}
	return tried >= lyricsLookupVersion
}

func (l *Library) markLyricsTried(id int64) {
	if _, err := l.db.Exec(`UPDATE tracks SET lyrics_tried = ? WHERE id = ?`, lyricsLookupVersion, id); err != nil {
		slog.Debug("lyrics: mark tried failed", "id", id, "err", err)
	}
}

// removeLyrics deletes a track's cached lyrics. Called when the track leaves
// the library and when its file is replaced, so the cache cannot grow without
// bound as a library churns
func removeLyrics(dataDir string, id int64) {
	_ = os.Remove(lyricsPath(dataDir, id))
}
