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
	// Version is the build-time version string injected via -ldflags
	// '-X main.version=...' and forwarded into Config by main.go after Load
	// returns. It is not an env var and is empty in non-production builds
	Version          string
	MusicDir         string
	DataDir          string
	Addr             string
	PublicURL        string
	AdminPassword    string
	Bitrate          int
	StationName      string
	StationGenre     string
	LoudNorm         bool
	ReplayGain       bool
	ITunesArt        bool
	GainDB           int
	MaxListeners     int
	RecaptchaSiteKey string
	RecaptchaSecret  string
}

func Load() (*Config, error) {
	cfg := &Config{
		MusicDir:         env("STATIONCAST_MUSIC_DIR", "./music"),
		DataDir:          env("STATIONCAST_DATA_DIR", "./data"),
		Addr:             env("STATIONCAST_ADDR", ":8000"),
		PublicURL:        env("STATIONCAST_PUBLIC_URL", ""),
		AdminPassword:    os.Getenv("STATIONCAST_ADMIN_PASSWORD"),
		StationName:      env("STATIONCAST_STATION_NAME", "StationCast"),
		StationGenre:     env("STATIONCAST_STATION_GENRE", "Various"),
		LoudNorm:         envBool("STATIONCAST_LOUDNORM", false),
		ReplayGain:       envBool("STATIONCAST_REPLAYGAIN", false),
		ITunesArt:        envBool("STATIONCAST_ITUNES_ART", true),
		RecaptchaSiteKey: os.Getenv("STATIONCAST_RECAPTCHA_SITE_KEY"),
		RecaptchaSecret:  os.Getenv("STATIONCAST_RECAPTCHA_SECRET_KEY"),
	}

	br, err := strconv.Atoi(env("STATIONCAST_BITRATE", "128"))
	if err != nil {
		return nil, fmt.Errorf("invalid STATIONCAST_BITRATE: %w", err)
	}
	cfg.Bitrate = br

	gain, err := strconv.Atoi(env("STATIONCAST_GAIN_DB", "0"))
	if err != nil {
		return nil, fmt.Errorf("invalid STATIONCAST_GAIN_DB: %w", err)
	}
	if gain < -20 {
		gain = -20
	} else if gain > 20 {
		gain = 20
	}
	cfg.GainDB = gain

	maxL, err := strconv.Atoi(env("STATIONCAST_MAX_LISTENERS", "256"))
	if err != nil || maxL < 0 {
		return nil, fmt.Errorf("invalid STATIONCAST_MAX_LISTENERS")
	}
	cfg.MaxListeners = maxL

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

	// Resolve MusicDir symlinks once so all downstream "is this path inside the
	// music root?" checks work against a canonical root. Docker bind mounts are
	// not symlinks and pass through unchanged
	if resolved, err := filepath.EvalSymlinks(cfg.MusicDir); err == nil {
		cfg.MusicDir = resolved
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
