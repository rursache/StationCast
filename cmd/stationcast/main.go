package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rursache/StationCast/internal/audio"
	"github.com/rursache/StationCast/internal/broadcast"
	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/httpx"
	"github.com/rursache/StationCast/internal/playlist"
	"github.com/rursache/StationCast/internal/storage"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}
	logger.Info("starting",
		"music", cfg.MusicDir,
		"data", cfg.DataDir,
		"addr", cfg.Addr,
		"bitrate", cfg.Bitrate,
		"loudnorm", cfg.LoudNorm,
		"gain_db", cfg.GainDB,
	)

	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		logger.Error("db open", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lib := playlist.NewLibrary(cfg, db)
	if err := lib.InitialScan(ctx); err != nil {
		logger.Error("library scan", "err", err)
	}
	go lib.Watch(ctx)
	go lib.FetchMissingArt(ctx)

	hub := broadcast.NewHub(cfg.Bitrate)
	hls := broadcast.NewHLSManager(hub, cfg.DataDir)
	go hls.Run(ctx)

	sched := playlist.NewScheduler(cfg, db, lib)
	if err := sched.Restore(); err != nil {
		logger.Warn("scheduler restore", "err", err)
	}

	engine := audio.NewEngine(cfg, sched, hub)
	go engine.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpx.NewRouter(cfg, db, lib, sched, hub, hls, engine),
		ReadHeaderTimeout: 10 * time.Second,
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
	hub.Close()
}
