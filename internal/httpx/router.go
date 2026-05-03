package httpx

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rursache/StationCast/internal/audio"
	"github.com/rursache/StationCast/internal/broadcast"
	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/files"
	"github.com/rursache/StationCast/internal/playlist"
	"github.com/rursache/StationCast/internal/storage"
)

type Server struct {
	cfg    *config.Config
	db     *storage.DB
	lib    *playlist.Library
	sched  *playlist.Scheduler
	hub    *broadcast.Hub
	hls    *broadcast.HLSManager
	engine *audio.Engine
	files  *files.Manager
	auth   *AuthStore
	tmpl   *Templates
}

// NewRouter wires the HTTP routes and returns the handler plus a long-running
// sweep function. The caller starts the sweep in a goroutine so expired
// session tokens are evicted from memory periodically
func NewRouter(cfg *config.Config, db *storage.DB, lib *playlist.Library, sched *playlist.Scheduler, hub *broadcast.Hub, hls *broadcast.HLSManager, eng *audio.Engine) (http.Handler, func(context.Context)) {
	s := &Server{
		cfg:    cfg,
		db:     db,
		lib:    lib,
		sched:  sched,
		hub:    hub,
		hls:    hls,
		engine: eng,
		files:  files.NewManager(cfg, lib),
		auth:   NewAuthStore(cfg.AdminPassword),
		tmpl:   MustLoadTemplates(),
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)

	r.Get("/", s.handlePublicHome)
	r.Get("/now-playing", s.handleNowPlayingJSON)
	r.Get("/now-playing/sse", s.handleNowPlayingSSE)
	r.Get("/history", s.handleHistoryJSON)
	r.Get("/art/{id}", s.handleArt)
	r.Get("/static/*", s.handleStatic)

	r.Get("/stream", s.handleStream)
	r.Get("/stream.mp3", s.handleStream)
	r.Get("/live", s.handleStream)
	r.Get("/stream.pls", s.handlePLS)
	r.Get("/stream.m3u", s.handleM3U)
	r.Get("/hls.m3u8", s.handleHLSPlaylist)
	r.Get("/hls/{seg}", s.handleHLSSegment)

	r.Route("/admin", func(r chi.Router) {
		r.Get("/login", s.handleLoginPage)
		r.With(readTimeout(30 * time.Second)).Post("/login", s.handleLogin)
		r.With(readTimeout(15 * time.Second)).Post("/logout", s.handleLogout)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/", s.handleAdminHome)
			r.Get("/library.json", s.handleLibraryJSON)
			r.Get("/state.json", s.handleAdminStateJSON)
			r.With(readTimeout(15 * time.Second)).Post("/skip", s.handleSkip)
			r.With(readTimeout(15 * time.Second)).Post("/mode", s.handleSetMode)
			r.With(readTimeout(15 * time.Second)).Post("/queue", s.handleEnqueue)
			r.With(readTimeout(15 * time.Second)).Post("/queue/remove", s.handleDequeue)
			r.With(readTimeout(15 * time.Second)).Post("/files/rename", s.handleRename)
			r.With(readTimeout(15 * time.Second)).Post("/files/delete", s.handleDelete)
			r.With(readTimeout(10 * time.Minute)).Post("/files/upload", s.handleUpload)
		})
	})

	return r, s.auth.RunSweeper
}

// readTimeout enforces a per-request body read deadline on non-streaming
// admin endpoints, mitigating slow-loris style POST attacks. Streaming
// endpoints intentionally bypass this
func readTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(d))
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders applies a small set of conservative defaults to every
// response. CSP is omitted for now because admin templates load Tailwind from
// a public CDN
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
