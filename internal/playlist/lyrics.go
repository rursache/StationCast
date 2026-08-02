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
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// LRCLIB is the only free lyrics source that returns time-synced lyrics
	// without an API key, so every operator does not need their own account.
	// It matches on artist, title, album and duration

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

	// /api/search takes no duration parameter, so candidates are filtered
	// here. Generous because encoder padding, fade-outs and remasters all
	// shift the length a little, and the alternative to a near miss is
	// showing nothing at all
	lyricsDurationTolerance = 15
)

// Provider endpoints. Variables rather than constants so tests can point the
// chain at a local server instead of the real services
var (
	lrclibGetURL    = "https://lrclib.net/api/get"
	lrclibSearchURL = "https://lrclib.net/api/search"

	// lyrics.ovh is consulted only after LRCLIB has nothing. Plain text only,
	// no timings, and no way to disambiguate by duration, so it can never
	// displace a synced result. Its coverage differs enough to be worth the
	// second call: it carries tracks LRCLIB does not
	lyricsOvhURL = "https://api.lyrics.ovh/v1"
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

// artistSep matches the ways a collaboration gets credited. Tags in the wild
// use all of them, while LRCLIB usually files the track under the first
// artist alone
var artistSep = regexp.MustCompile(`(?i)\s+(?:x|si|s\x{0219}i|s\x{015F}i|\x{0219}i|\x{015F}i|feat\.?|ft\.?|featuring|&|vs\.?|with)\s+|\s*[/;,]\s*`)

// titleNoise is the decoration a YouTube rip carries into its tags
var titleNoise = regexp.MustCompile(`(?i)\s*[\(\[](?:[^)\]]*\b(?:official|oficial|video|videoclip|audio|lyrics?|versuri|visualizer|hd|hq|4k|mv|remaster(?:ed)?|explicit)\b[^)\]]*)[\)\]]`)

// cleanTitle removes the artist prefix and the release decoration that ripped
// tags carry, so "Jador - Mama | Official Video" becomes "Mama". LRCLIB folds
// case, punctuation and diacritics itself, so none of that is done here
func cleanTitle(title, artist string) string {
	t := strings.TrimSpace(title)
	// "Artist - Title" and "Title - Artist", the two shapes a rip produces
	for _, a := range artistCandidates(artist) {
		if a == "" {
			continue
		}
		if lower := strings.ToLower(t); strings.HasPrefix(lower, strings.ToLower(a)+" - ") {
			t = strings.TrimSpace(t[len(a)+3:])
		}
		if lower := strings.ToLower(t); strings.HasSuffix(lower, " - "+strings.ToLower(a)) {
			t = strings.TrimSpace(t[:len(t)-len(a)-3])
		}
	}
	t = titleNoise.ReplaceAllString(t, "")
	// a trailing "| anything" is nearly always channel decoration
	if i := strings.LastIndex(t, "|"); i > 0 {
		t = strings.TrimSpace(t[:i])
	}
	return strings.TrimSpace(t)
}

// artistCandidates returns the whole credit first, then each individual
// artist. A track credited "Kalif x Luis Gabriel" is filed under "Kalif"
func artistCandidates(artist string) []string {
	full := strings.TrimSpace(artist)
	out := []string{}
	if full != "" {
		out = append(out, full)
	}
	for _, part := range artistSep.Split(full, -1) {
		if p := strings.TrimSpace(part); p != "" && !strings.EqualFold(p, full) {
			out = append(out, p)
		}
	}
	return out
}

type lrclibResponse struct {
	// source names the provider that answered, for logging only
	source       string
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

	res, err := l.lookupLyrics(ctx, t)
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
		"source", res.source, "synced", strings.TrimSpace(res.SyncedLyrics) != "")
}

// lookupLyrics walks the providers in order of quality. LRCLIB first, since
// it is the only one that returns timings: the exact endpoint for each way
// the artist might be credited, then its search endpoint. /api/get matches
// the duration within two seconds server-side and the whole title exactly, so
// a collaboration credit or a ripped title misses it even when the track is
// there. lyrics.ovh comes last and only ever supplies plain text
func (l *Library) lookupLyrics(ctx context.Context, t *Track) (*lrclibResponse, error) {
	title := cleanTitle(t.Title, t.Artist)
	if title == "" {
		title = t.Title
	}
	credits := artistCandidates(t.Artist)

	// A provider being unreachable must not stop the others being asked, but
	// it does mean the track cannot be written off. Remembered here so the
	// final answer is retryable unless every provider definitively said no
	var transient error
	note := func(err error) {
		if err != nil && !errors.Is(err, errNoLyricsMatch) {
			transient = err
		}
	}

	// LRCLIB exact, once per way the artist might be credited
	for _, artist := range credits {
		res, err := l.queryLRCLIB(ctx, artist, title, t.Album, t.DurationMS)
		if err == nil {
			return res, nil
		}
		note(err)
	}

	// LRCLIB search, which ranks on full text and so matches records whose
	// title field holds a whole "ARTIST - Title" string
	res, err := l.searchLRCLIB(ctx, t, title)
	if err == nil && res != nil {
		return res, nil
	}
	note(err)

	// lyrics.ovh next, plain text only, but it indexes a different catalogue
	for _, artist := range credits {
		res, err := l.queryLyricsOvh(ctx, artist, title)
		if err == nil {
			return res, nil
		}
		note(err)
	}

	// versuri.ro last, since it is scraped rather than an API. It carries
	// Romanian music the other two miss outright
	for _, artist := range credits {
		res, err := l.queryVersuri(ctx, artist, title)
		if err == nil {
			return res, nil
		}
		note(err)
	}

	if transient != nil {
		return nil, transient
	}
	return nil, errNoLyricsMatch
}

// queryLyricsOvh asks lyrics.ovh, which returns plain text with no timings.
// Artist and title go in the path rather than the query string, so both are
// escaped. A hobby service with no uptime guarantee, hence the fallback
// position and the same transient-versus-definitive distinction as LRCLIB
func (l *Library) queryLyricsOvh(ctx context.Context, artist, title string) (*lrclibResponse, error) {
	if artist == "" || title == "" {
		return nil, errNoLyricsMatch
	}
	u := lyricsOvhURL + "/" + url.PathEscape(artist) + "/" + url.PathEscape(title)

	reqCtx, cancel := context.WithTimeout(ctx, lyricsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
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
		return nil, fmt.Errorf("lyrics.ovh status %d", resp.StatusCode)
	}

	var out struct {
		Lyrics string `json:"lyrics"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLyricsBytes)).Decode(&out); err != nil {
		return nil, err
	}
	// It answers 200 with an error field in some cases rather than a 404
	if out.Error != "" || strings.TrimSpace(out.Lyrics) == "" {
		return nil, errNoLyricsMatch
	}
	return &lrclibResponse{
		source:      "lyrics.ovh",
		ArtistName:  artist,
		TrackName:   title,
		PlainLyrics: out.Lyrics,
	}, nil
}

func (l *Library) queryLRCLIB(ctx context.Context, artist, title, album string, durationMS int64) (*lrclibResponse, error) {
	q := url.Values{}
	q.Set("artist_name", artist)
	q.Set("track_name", title)
	if album != "" {
		q.Set("album_name", album)
	}
	// LRCLIB matches duration within a couple of seconds, which is what stops
	// a cover or a remaster being served for the wrong recording. It rejects
	// anything outside 1-3600, so only send a plausible value
	if secs := durationMS / 1000; secs >= 1 && secs <= 3600 {
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
	out.source = "lrclib"
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

// searchLRCLIB is the fallback when no exact match exists. It sends one
// combined query string rather than separate fields, which is what engages
// LRCLIB's full text ranking and so matches records whose title field holds a
// whole "ARTIST - Title" string. Search takes no duration parameter, so
// candidates are filtered here, and a candidate outside tolerance is dropped
// rather than accepted as a near miss: the wrong lyrics are worse than none
func (l *Library) searchLRCLIB(ctx context.Context, t *Track, title string) (*lrclibResponse, error) {
	q := url.Values{}
	q.Set("q", strings.TrimSpace(t.Artist+" "+title))

	reqCtx, cancel := context.WithTimeout(ctx, lyricsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, lrclibSearchURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", lrclibUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lrclib search status %d", resp.StatusCode)
	}

	var results []lrclibResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4*maxLyricsBytes)).Decode(&results); err != nil {
		return nil, err
	}
	if best := pickLyricsCandidate(results, t.DurationMS); best != nil {
		best.source = "lrclib/search"
		return best, nil
	}
	return nil, nil
}

// pickLyricsCandidate takes the best usable result, preferring a synced
// version. Results arrive already ranked by relevance, so the first one
// within tolerance wins and only a synced match displaces an earlier plain
// one. Returns nil when nothing qualifies
func pickLyricsCandidate(results []lrclibResponse, durationMS int64) *lrclibResponse {
	want := durationMS / 1000
	var plain *lrclibResponse
	for i := range results {
		r := &results[i]
		if r.Instrumental {
			continue
		}
		hasSynced := strings.TrimSpace(r.SyncedLyrics) != ""
		if !hasSynced && strings.TrimSpace(r.PlainLyrics) == "" {
			continue
		}
		// Only enforce the window when the track duration is known
		if want > 0 {
			if d := int64(r.Duration); d > 0 {
				if diff := d - want; diff > lyricsDurationTolerance || diff < -lyricsDurationTolerance {
					continue
				}
			}
		}
		if hasSynced {
			return r
		}
		if plain == nil {
			plain = r
		}
	}
	return plain
}
