package httpx

import (
	"net/http"

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

func NewRouter(cfg *config.Config, db *storage.DB, lib *playlist.Library, sched *playlist.Scheduler, hub *broadcast.Hub, hls *broadcast.HLSManager, eng *audio.Engine) http.Handler {
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

	r.Get("/", s.handlePublicHome)
	r.Get("/now-playing", s.handleNowPlayingJSON)
	r.Get("/now-playing/sse", s.handleNowPlayingSSE)
	r.Get("/art/{id}", s.handleArt)
	r.Get("/static/*", s.handleStatic)

	r.Get("/stream", s.handleStream)
	r.Get("/stream.mp3", s.handleStream)
	r.Get("/stream.pls", s.handlePLS)
	r.Get("/stream.m3u", s.handleM3U)
	r.Get("/hls.m3u8", s.handleHLSPlaylist)
	r.Get("/hls/{seg}", s.handleHLSSegment)

	r.Route("/admin", func(r chi.Router) {
		r.Get("/login", s.handleLoginPage)
		r.Post("/login", s.handleLogin)
		r.Post("/logout", s.handleLogout)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/", s.handleAdminHome)
			r.Post("/skip", s.handleSkip)
			r.Post("/mode", s.handleSetMode)
			r.Post("/queue", s.handleEnqueue)
			r.Post("/queue/remove", s.handleDequeue)
			r.Post("/files/rename", s.handleRename)
			r.Post("/files/delete", s.handleDelete)
			r.Post("/files/upload", s.handleUpload)
		})
	})

	return r
}
