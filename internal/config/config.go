package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	MusicDir      string
	DataDir       string
	Addr          string
	PublicURL     string
	AdminPassword string
	Bitrate       int
	StationName   string
	StationGenre  string
	LoudNorm      bool
	ITunesArt     bool
}

func Load() (*Config, error) {
	cfg := &Config{
		MusicDir:      env("STATIONCAST_MUSIC_DIR", "./music"),
		DataDir:       env("STATIONCAST_DATA_DIR", "./data"),
		Addr:          env("STATIONCAST_ADDR", ":8000"),
		PublicURL:     env("STATIONCAST_PUBLIC_URL", ""),
		AdminPassword: os.Getenv("STATIONCAST_ADMIN_PASSWORD"),
		StationName:   env("STATIONCAST_STATION_NAME", "StationCast"),
		StationGenre: env("STATIONCAST_STATION_GENRE", "Various"),
		LoudNorm:     envBool("STATIONCAST_LOUDNORM", false),
		ITunesArt:    envBool("STATIONCAST_ITUNES_ART", true),
	}

	br, err := strconv.Atoi(env("STATIONCAST_BITRATE", "128"))
	if err != nil {
		return nil, fmt.Errorf("invalid STATIONCAST_BITRATE: %w", err)
	}
	cfg.Bitrate = br

	if cfg.AdminPassword == "" {
		return nil, errors.New("STATIONCAST_ADMIN_PASSWORD is required")
	}

	abs, err := filepath.Abs(cfg.MusicDir)
	if err != nil {
		return nil, err
	}
	cfg.MusicDir = abs

	abs, err = filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	cfg.DataDir = abs

	if err := os.MkdirAll(cfg.MusicDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "art"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "hls"), 0o755); err != nil {
		return nil, err
	}

	return cfg, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.ToLower(os.Getenv(key))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}
