package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/rursache/StationCast/internal/audio"
	"github.com/rursache/StationCast/internal/broadcast"
	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/httpx"
	"github.com/rursache/StationCast/internal/playlist"
	"github.com/rursache/StationCast/internal/storage"
)

// version is set at build time via -ldflags '-X main.version=v1.2.3'
// and falls back to "dev" for local builds
var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}
	cfg.Version = version
	logger.Info("starting",
		"version", version,
		"music", cfg.MusicDir,
		"data", cfg.DataDir,
		"addr", cfg.Addr,
		"bitrate", cfg.Bitrate,
		"loudnorm", cfg.LoudNorm,
		"replaygain", cfg.ReplayGain,
		"gain_db", cfg.GainDB,
		"max_listeners", cfg.MaxListeners,
		"recaptcha", cfg.RecaptchaSiteKey != "",
	)

	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		logger.Error("db open", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Background workers are tracked so shutdown can wait for them. Both
	// Run loops kill their ffmpeg children on the way out, and returning
	// from main before that happens orphans those processes
	var workers sync.WaitGroup
	spawn := func(fn func(context.Context)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			fn(ctx)
		}()
	}

	lib := playlist.NewLibrary(cfg, db)
	if err := lib.InitialScan(ctx); err != nil {
		logger.Error("library scan", "err", err)
	}
	spawn(lib.Watch)
	spawn(lib.FetchMissingArt)

	hub := broadcast.NewHub(cfg.Bitrate)
	hub.SetMaxListeners(cfg.MaxListeners)
	hls := broadcast.NewHLSManager(hub, cfg.DataDir)
	spawn(hls.Run)

	sched := playlist.NewScheduler(cfg, db, lib)
	if err := sched.Restore(); err != nil {
		logger.Warn("scheduler restore", "err", err)
	}

	engine := audio.NewEngine(cfg, sched, hub, lib)
	spawn(engine.Run)

	router, authSweep := httpx.NewRouter(cfg, db, lib, sched, hub, hls, engine)
	spawn(authSweep)

	// Streaming endpoints rely on long-lived writes, so we cannot set a global
	// WriteTimeout. ReadHeaderTimeout caps slow header attacks; IdleTimeout
	// reaps zombie keep-alives. Per-route ReadTimeout is enforced inside the
	// admin router for non-streaming POST endpoints
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http", "err", err)
			stop <- syscall.SIGTERM
		}
	}()

	<-stop
	logger.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	cancel()

	// Wait before closing the hub: the engine's stdout pump writes into it,
	// and the Run loops need the chance to kill and reap their ffmpeg
	// children rather than leaving them orphaned
	if !waitFor(&workers, workerShutdownTimeout) {
		logger.Warn("background workers did not stop in time", "timeout", workerShutdownTimeout)
	}
	hub.Close()
}

// workerShutdownTimeout bounds how long shutdown waits for background
// workers. Longer than the HTTP grace period because the audio engine has to
// kill and reap ffmpeg subprocesses on the way out
const workerShutdownTimeout = 10 * time.Second

// waitFor reports whether wg finished within d
func waitFor(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
