package httpx

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rursache/StationCast/internal/broadcast"
)

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	wantMeta := r.Header.Get("Icy-MetaData") == "1"

	h := w.Header()
	h.Set("Content-Type", "audio/mpeg")
	h.Set("Cache-Control", "no-cache, no-store")
	h.Set("Connection", "close")
	h.Set("icy-name", s.cfg.StationName)
	h.Set("icy-genre", s.cfg.StationGenre)
	h.Set("icy-pub", "1")
	h.Set("icy-br", strconv.Itoa(s.cfg.Bitrate))
	if wantMeta {
		h.Set("icy-metaint", strconv.Itoa(broadcast.ICYMetaInt))
	}

	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	sub := s.hub.Subscribe()
	defer sub.Close()

	titleFn := func() string {
		t := s.hub.Metadata()
		if t == "" {
			t = s.cfg.StationName
		}
		return t
	}
	_ = broadcast.ICYStream(w, flush, sub, wantMeta, broadcast.ICYMetaInt, titleFn)
}

func (s *Server) handlePLS(w http.ResponseWriter, r *http.Request) {
	url := s.streamURL(r, "/stream")
	w.Header().Set("Content-Type", "audio/x-scpls")
	w.Header().Set("Content-Disposition", `inline; filename="stationcast.pls"`)
	fmt.Fprintf(w, "[playlist]\nNumberOfEntries=1\nFile1=%s\nTitle1=%s\nLength1=-1\nVersion=2\n", url, s.cfg.StationName)
}

func (s *Server) handleM3U(w http.ResponseWriter, r *http.Request) {
	url := s.streamURL(r, "/stream")
	w.Header().Set("Content-Type", "audio/x-mpegurl")
	w.Header().Set("Content-Disposition", `inline; filename="stationcast.m3u"`)
	fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1,%s\n%s\n", s.cfg.StationName, url)
}

func (s *Server) handleHLSPlaylist(w http.ResponseWriter, r *http.Request) {
	f, err := os.Open(s.hls.PlaylistPath())
	if err != nil {
		http.Error(w, "playlist unavailable", http.StatusServiceUnavailable)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "#") && strings.HasSuffix(line, ".ts") {
			line = "hls/" + line
		}
		bw.WriteString(line)
		bw.WriteByte('\n')
	}
}

func (s *Server) handleHLSSegment(w http.ResponseWriter, r *http.Request) {
	seg := chi.URLParam(r, "seg")
	if seg == "" || strings.ContainsAny(seg, "/\\") || !strings.HasSuffix(seg, ".ts") {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(s.hls.Dir(), seg)
	rel, err := filepath.Rel(s.hls.Dir(), full)
	if err != nil || strings.HasPrefix(rel, "..") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "max-age=10")
	http.ServeFile(w, r, full)
}

func (s *Server) streamURL(r *http.Request, path string) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimRight(s.cfg.PublicURL, "/") + path
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	return scheme + "://" + r.Host + path
}
