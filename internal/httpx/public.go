package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type nowPlaying struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	HasArt      bool   `json:"has_art"`
	ArtURL      string `json:"art_url,omitempty"`
	NextTitle   string `json:"next_title,omitempty"`
	NextArtist  string `json:"next_artist,omitempty"`
	Listeners   int    `json:"listeners"`
	StationName string `json:"station_name"`
}

func (s *Server) currentNowPlaying() nowPlaying {
	np := nowPlaying{
		StationName: s.cfg.StationName,
		Listeners:   s.hub.Listeners(),
	}
	if t := s.sched.Current(); t != nil {
		np.Title = t.Title
		np.Artist = t.Artist
		np.Album = t.Album
		np.HasArt = t.HasArt
		if t.HasArt {
			np.ArtURL = "/art/" + strconv.FormatInt(t.ID, 10)
		}
	}
	if next := s.sched.Peek(); next != nil {
		np.NextTitle = next.Title
		np.NextArtist = next.Artist
	}
	return np
}

func (s *Server) handleNowPlayingJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(s.currentNowPlaying())
}

func (s *Server) handleNowPlayingSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	if flusher == nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	send := func(np nowPlaying) error {
		buf, err := json.Marshal(np)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", buf); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	last := s.currentNowPlaying()
	if err := send(last); err != nil {
		return
	}

	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			cur := s.currentNowPlaying()
			if cur != last {
				if err := send(cur); err != nil {
					return
				}
				last = cur
			} else {
				if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

func (s *Server) handleArt(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t, ok := s.lib.Get(id)
	if !ok || !t.HasArt {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.cfg.DataDir, "art", strconv.FormatInt(id, 10)+".jpg")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, path)
}

func (s *Server) handlePublicHome(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"StationName": s.cfg.StationName,
		"DirectURL":   s.streamURL(r, "/stream"),
		"PLSURL":      s.streamURL(r, "/stream.pls"),
		"M3UURL":      s.streamURL(r, "/stream.m3u"),
		"HLSURL":      s.streamURL(r, "/hls.m3u8"),
	}
	s.tmpl.Render(w, "public.html", data)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	if strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	data, ok := staticFiles[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch filepath.Ext(name) {
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(data)
}
