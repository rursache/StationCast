package playlist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
)

type itunesResp struct {
	ResultCount int `json:"resultCount"`
	Results     []struct {
		ArtworkURL100 string `json:"artworkUrl100"`
	} `json:"results"`
}

type albumKey struct {
	artist string
	album  string
}

type artCandidate struct {
	id     int64
	artist string
	album  string
}

func normKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// FetchArtworkURL queries the iTunes Search API for an album cover URL.
// Returns "" with no error when no result is found.
func FetchArtworkURL(ctx context.Context, client *http.Client, artist, album string) (string, error) {
	q := url.Values{}
	q.Set("term", artist+" "+album)
	q.Set("media", "music")
	q.Set("entity", "album")
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
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// FetchMissingArt walks the library, looks up album art on iTunes for tracks
// that have no embedded art and have artist + album metadata, downloads the
// image into data/art/<id>.jpg, and marks the track row so it is not retried.
// Same album art is reused across all tracks of the same artist+album.
func (l *Library) FetchMissingArt(ctx context.Context) {
	if !l.cfg.ITunesArt {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}

	rows, err := l.db.Query(`SELECT id, artist, album FROM tracks
		WHERE has_art = 0 AND art_tried = 0 AND artist <> '' AND album <> ''`)
	if err != nil {
		slog.Warn("itunes: query", "err", err)
		return
	}
	var pending []artCandidate
	for rows.Next() {
		var r artCandidate
		if err := rows.Scan(&r.id, &r.artist, &r.album); err == nil {
			pending = append(pending, r)
		}
	}
	rows.Close()
	if len(pending) == 0 {
		return
	}
	slog.Info("itunes: fetching missing album art", "candidates", len(pending))

	byAlbum := map[albumKey][]artCandidate{}
	var order []albumKey
	for _, r := range pending {
		k := albumKey{normKey(r.artist), normKey(r.album)}
		if _, ok := byAlbum[k]; !ok {
			order = append(order, k)
		}
		byAlbum[k] = append(byAlbum[k], r)
	}

	for _, k := range order {
		if ctx.Err() != nil {
			return
		}
		group := byAlbum[k]
		first := group[0]
		artURL, err := FetchArtworkURL(ctx, client, first.artist, first.album)
		if err != nil {
			slog.Debug("itunes: lookup failed", "artist", first.artist, "album", first.album, "err", err)
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
			_, _ = l.db.Exec(`UPDATE tracks SET has_art = 1, art_tried = 1 WHERE id = ?`, r.id)
			l.mu.Lock()
			if t, ok := l.byID[r.id]; ok {
				t.HasArt = true
			}
			l.mu.Unlock()
		}
		slog.Info("itunes: art fetched", "artist", first.artist, "album", first.album, "tracks", len(group))
		time.Sleep(itunesPause)
	}
}

func (l *Library) markArtTried(rows []artCandidate) {
	for _, r := range rows {
		_, _ = l.db.Exec(`UPDATE tracks SET art_tried = 1 WHERE id = ?`, r.id)
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
