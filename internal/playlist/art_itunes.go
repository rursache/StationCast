package playlist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	itunesEndpoint = "https://itunes.apple.com/search"
	itunesUA       = "StationCast/1.0 (https://github.com/rursache/StationCast)"
	itunesArtSize  = "600x600"
	itunesPause    = 600 * time.Millisecond
	maxArtBytes    = 10 << 20

	// itunesLookupVersion is bumped whenever the iTunes search params change
	// in a way that could yield different results (eg term composition or
	// entity). Tracks with art_tried < itunesLookupVersion are eligible for a
	// fresh lookup so the library benefits from the improved search. v2
	// switched from album-by-(artist+album) to song-by-(artist+title), which
	// returns correct artwork for modern singles
	itunesLookupVersion = 2
)

type itunesResp struct {
	ResultCount int `json:"resultCount"`
	Results     []struct {
		ArtworkURL100 string `json:"artworkUrl100"`
	} `json:"results"`
}

type songKey struct {
	artist string
	title  string
}

type artCandidate struct {
	id     int64
	artist string
	title  string
}

func normKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// FetchArtworkURL queries the iTunes Search API for the artwork URL of the
// song matching artist + title. Song-level entity lookup returns the correct
// artwork even for singles that aren't tied to an album, which is the common
// case for modern releases. Returns "" with no error when no result is found
func FetchArtworkURL(ctx context.Context, client *http.Client, artist, title string) (string, error) {
	q := url.Values{}
	q.Set("term", artist+" "+title)
	q.Set("media", "music")
	q.Set("entity", "song")
	q.Set("limit", "1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, itunesEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", itunesUA)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("itunes status %d", resp.StatusCode)
	}
	var r itunesResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.ResultCount == 0 || r.Results[0].ArtworkURL100 == "" {
		return "", nil
	}
	hi := strings.ReplaceAll(r.Results[0].ArtworkURL100, "100x100", itunesArtSize)
	return hi, nil
}

func downloadTo(ctx context.Context, client *http.Client, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", itunesUA)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	// Cap at maxArtBytes+1 so we can detect oversize bodies without buffering them
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxArtBytes+1))
	if err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if n > maxArtBytes {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("art exceeds %d byte limit", maxArtBytes)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// safeRedirect blocks redirects to non-HTTPS schemes or to private,
// loopback, or link-local hosts. Used as the http.Client CheckRedirect for
// the iTunes art fetcher to neuter SSRF-via-redirect
func safeRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("redirect to non-https scheme: %s", req.URL.Scheme)
	}
	host := req.URL.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("redirect to disallowed address: %s", ip)
		}
	}
	return nil
}

// FetchMissingArt walks the library and queries iTunes for artwork on every
// track that has artist + title metadata and hasn't been queried at the
// current itunesLookupVersion. When iTunes returns artwork it is downloaded
// into data/art/<id>.jpg, overwriting any embedded art that was extracted
// during scan; this gives iTunes priority when STATIONCAST_ITUNES_ART is
// enabled. When iTunes returns nothing the embedded art (if any) is left in
// place, otherwise the track has no art and the UI shows the placeholder.
// Tracks sharing artist+title (eg duplicate files) are deduped into one
// lookup. Bumping itunesLookupVersion automatically reopens previously-tried
// tracks so libraries benefit from improved search params on upgrade
func (l *Library) FetchMissingArt(ctx context.Context) {
	if !l.cfg.ITunesArt {
		return
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: safeRedirect,
	}

	rows, err := l.db.Query(`SELECT id, artist, title FROM tracks
		WHERE art_tried < ? AND artist <> '' AND title <> ''`, itunesLookupVersion)
	if err != nil {
		slog.Warn("itunes: query", "err", err)
		return
	}
	var pending []artCandidate
	for rows.Next() {
		var r artCandidate
		if err := rows.Scan(&r.id, &r.artist, &r.title); err == nil {
			pending = append(pending, r)
		}
	}
	rows.Close()
	if len(pending) == 0 {
		return
	}
	slog.Info("itunes: fetching artwork", "candidates", len(pending))

	bySong := map[songKey][]artCandidate{}
	var order []songKey
	for _, r := range pending {
		k := songKey{normKey(r.artist), normKey(r.title)}
		if _, ok := bySong[k]; !ok {
			order = append(order, k)
		}
		bySong[k] = append(bySong[k], r)
	}

	for _, k := range order {
		if ctx.Err() != nil {
			return
		}
		group := bySong[k]
		first := group[0]
		artURL, err := FetchArtworkURL(ctx, client, first.artist, first.title)
		if err != nil {
			slog.Debug("itunes: lookup failed", "artist", first.artist, "title", first.title, "err", err)
			l.markArtTried(group)
			time.Sleep(itunesPause)
			continue
		}
		if artURL == "" {
			l.markArtTried(group)
			time.Sleep(itunesPause)
			continue
		}
		dst := filepath.Join(l.cfg.DataDir, "art", fmt.Sprintf("%d.jpg", first.id))
		if err := downloadTo(ctx, client, artURL, dst); err != nil {
			slog.Debug("itunes: download failed", "url", artURL, "err", err)
			l.markArtTried(group)
			time.Sleep(itunesPause)
			continue
		}
		for _, r := range group[1:] {
			if err := copyFile(dst, filepath.Join(l.cfg.DataDir, "art", fmt.Sprintf("%d.jpg", r.id))); err != nil {
				slog.Debug("itunes: copy art", "id", r.id, "err", err)
			}
		}
		for _, r := range group {
			_, _ = l.db.Exec(`UPDATE tracks SET has_art = 1, art_tried = ? WHERE id = ?`, itunesLookupVersion, r.id)
			l.mu.Lock()
			if t, ok := l.byID[r.id]; ok {
				t.HasArt = true
			}
			l.mu.Unlock()
		}
		slog.Info("itunes: art fetched", "artist", first.artist, "title", first.title, "tracks", len(group))
		time.Sleep(itunesPause)
	}
}

// refreshThrottle bounds how often a single (artist, title) is re-queried.
// 30s is generous enough to absorb burst events (rapid Skip presses) while
// short enough that a song playing twice in normal rotation gets a fresh
// lookup each time
const refreshThrottle = 30 * time.Second

// RefreshArt re-queries iTunes for a single track and overwrites the on-disk
// artwork if a result is returned. Intended to be fired asynchronously when a
// track starts playing, so libraries with bad-on-first-fetch artwork
// self-heal as songs cycle through the schedule. A no-op when the iTunes
// integration is disabled, when artist or title is empty, or when the same
// song was refreshed within refreshThrottle
func (l *Library) RefreshArt(ctx context.Context, t *Track) {
	if !l.cfg.ITunesArt || t == nil || t.Artist == "" || t.Title == "" {
		return
	}
	k := songKey{normKey(t.Artist), normKey(t.Title)}
	now := time.Now()
	if prev, ok := l.lastRefresh.Load(k); ok {
		if now.Sub(prev.(time.Time)) < refreshThrottle {
			return
		}
	}
	l.lastRefresh.Store(k, now)

	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: safeRedirect}
	artURL, err := FetchArtworkURL(ctx, client, t.Artist, t.Title)
	if err != nil {
		slog.Debug("itunes: refresh lookup failed", "id", t.ID, "title", t.Title, "err", err)
		return
	}
	if artURL == "" {
		return
	}
	dst := filepath.Join(l.cfg.DataDir, "art", fmt.Sprintf("%d.jpg", t.ID))
	if err := downloadTo(ctx, client, artURL, dst); err != nil {
		slog.Debug("itunes: refresh download failed", "id", t.ID, "err", err)
		return
	}
	_, _ = l.db.Exec(`UPDATE tracks SET has_art = 1, art_tried = ? WHERE id = ?`, itunesLookupVersion, t.ID)
	l.mu.Lock()
	if existing, ok := l.byID[t.ID]; ok {
		existing.HasArt = true
	}
	l.mu.Unlock()
	slog.Info("itunes: art refreshed", "id", t.ID, "artist", t.Artist, "title", t.Title)
}

func (l *Library) markArtTried(rows []artCandidate) {
	for _, r := range rows {
		_, _ = l.db.Exec(`UPDATE tracks SET art_tried = ? WHERE id = ?`, itunesLookupVersion, r.id)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
