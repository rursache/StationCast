package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/rursache/StationCast/internal/files"
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
			ID: t.ID, Title: t.Title, Artist: t.Artist, Album: t.Album, HasArt: t.HasArt,
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
	s.tmpl.Render(w, "login.html", map[string]any{
		"StationName":      s.cfg.StationName,
		"RecaptchaSiteKey": s.cfg.RecaptchaSiteKey,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if s.cfg.RecaptchaSecret != "" {
		token := r.FormValue("g-recaptcha-response")
		if !verifyRecaptcha(r.Context(), s.cfg.RecaptchaSecret, token, r.RemoteAddr) {
			http.Redirect(w, r, "/admin/login?error=1", http.StatusSeeOther)
			return
		}
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
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
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
		slog.Error("set mode", "mode", m, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
		slog.Warn("enqueue", "id", id, "err", err)
		http.Error(w, "enqueue failed", http.StatusBadRequest)
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
		slog.Warn("dequeue", "idx", idx, "err", err)
		http.Error(w, "dequeue failed", http.StatusBadRequest)
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
	// Cap the request body before parsing the multipart form. The +4096 leaves
	// headroom for multipart framing overhead so a file of exactly the
	// configured size still fits
	r.Body = http.MaxBytesReader(w, r.Body, files.MaxUploadBytes+4096)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		slog.Warn("upload: parse multipart", "err", err)
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		slog.Warn("upload: form file", "err", err)
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}
	defer file.Close()
	if header.Size > files.MaxUploadBytes {
		http.Error(w, "file exceeds size limit", http.StatusRequestEntityTooLarge)
		return
	}
	if err := s.files.Save(header.Filename, file); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
