package httpx

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/rursache/StationCast/internal/playlist"
)

func (s *Server) sortedLibrary() []adminViewTrack {
	tracks := s.lib.Snapshot()
	sort.Slice(tracks, func(i, j int) bool {
		ti := strings.ToLower(tracks[i].Artist + tracks[i].Title)
		tj := strings.ToLower(tracks[j].Artist + tracks[j].Title)
		return ti < tj
	})
	view := make([]adminViewTrack, 0, len(tracks))
	for _, t := range tracks {
		view = append(view, adminViewTrack{
			ID: t.ID, Path: t.Path, Title: t.Title, Artist: t.Artist, Album: t.Album, HasArt: t.HasArt,
		})
	}
	return view
}

func (s *Server) handleLibraryJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(s.sortedLibrary())
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.tmpl.Render(w, "login.html", map[string]any{"StationName": s.cfg.StationName})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.auth.Verify(r.FormValue("password")) {
		http.Redirect(w, r, "/admin/login?error=1", http.StatusSeeOther)
		return
	}
	tok := s.auth.Issue()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.Revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

type adminViewTrack struct {
	ID     int64  `json:"id"`
	Path   string `json:"path"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
	HasArt bool   `json:"has_art"`
}

func (s *Server) handleAdminHome(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"StationName": s.cfg.StationName,
		"Current":     s.sched.Current(),
		"Next":        s.sched.Peek(),
		"Mode":        string(s.sched.Mode()),
		"Tracks":      s.sortedLibrary(),
		"Queue":       s.sched.Queue(),
		"History":     s.sched.History(),
		"Listeners":   s.hub.Listeners(),
	}
	s.tmpl.Render(w, "admin.html", data)
}

func (s *Server) handleSkip(w http.ResponseWriter, r *http.Request) {
	s.engine.Skip()
	if isHTMX(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	m, err := playlist.ParseMode(r.FormValue("mode"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.sched.SetMode(m); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.sched.Enqueue(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleDequeue(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	idx, err := strconv.Atoi(r.FormValue("idx"))
	if err != nil {
		http.Error(w, "bad idx", http.StatusBadRequest)
		return
	}
	if err := s.sched.Dequeue(idx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.files.Rename(id, r.FormValue("name")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.files.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	if err := s.files.Save(header.Filename, file); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
