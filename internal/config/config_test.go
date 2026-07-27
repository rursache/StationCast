package config

import (
	"strconv"
	"testing"

	"github.com/rursache/StationCast/internal/broadcast"
)

// setBaseEnv points the loader at throwaway directories and satisfies the
// required password so individual cases only vary the value under test
func setBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("STATIONCAST_MUSIC_DIR", t.TempDir())
	t.Setenv("STATIONCAST_DATA_DIR", t.TempDir())
	t.Setenv("STATIONCAST_ADMIN_PASSWORD", "hunter2")
}

func TestLoadBitrateBounds(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{name: "default when unset", value: "", want: 128},
		{name: "lower bound", value: "8", want: 8},
		{name: "upper bound", value: "320", want: 320},
		{name: "typical", value: "192", want: 192},
		{name: "below lower bound", value: "7", wantErr: true},
		{name: "above upper bound", value: "321", wantErr: true},
		{name: "zero", value: "0", wantErr: true},
		{name: "negative", value: "-128", wantErr: true},
		{name: "not a number", value: "high", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setBaseEnv(t)
			t.Setenv("STATIONCAST_BITRATE", tc.value)

			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() with bitrate %q = nil error, want error", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() with bitrate %q: %v", tc.value, err)
			}
			if cfg.Bitrate != tc.want {
				t.Errorf("Bitrate = %d, want %d", cfg.Bitrate, tc.want)
			}
		})
	}
}

// GainDB clamps rather than erroring, unlike bitrate. Locking that in so the
// two are not accidentally made consistent in the wrong direction
func TestLoadGainDBClamps(t *testing.T) {
	cases := []struct {
		value string
		want  int
	}{
		{"0", 0},
		{"5", 5},
		{"-5", -5},
		{"100", 20},
		{"-100", -20},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			setBaseEnv(t)
			t.Setenv("STATIONCAST_GAIN_DB", tc.value)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.GainDB != tc.want {
				t.Errorf("GainDB for %q = %d, want %d", tc.value, cfg.GainDB, tc.want)
			}
		})
	}
}

func TestLoadRequiresAdminPassword(t *testing.T) {
	t.Setenv("STATIONCAST_MUSIC_DIR", t.TempDir())
	t.Setenv("STATIONCAST_DATA_DIR", t.TempDir())
	t.Setenv("STATIONCAST_ADMIN_PASSWORD", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() without admin password = nil error, want error")
	}
}

func TestLoadMaxListeners(t *testing.T) {
	cases := []struct {
		value   string
		want    int
		wantErr bool
	}{
		{value: "", want: 256},
		{value: "0", want: 0},
		{value: "1000", want: 1000},
		{value: "-1", wantErr: true},
		{value: "lots", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			setBaseEnv(t)
			t.Setenv("STATIONCAST_MAX_LISTENERS", tc.value)

			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() with max listeners %q = nil error, want error", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.MaxListeners != tc.want {
				t.Errorf("MaxListeners = %d, want %d", cfg.MaxListeners, tc.want)
			}
		})
	}
}

func TestLoadBurstSeconds(t *testing.T) {
	cases := []struct {
		value   string
		want    int
		wantErr bool
	}{
		{value: "", want: broadcast.DefaultBurstSeconds},
		{value: "0", want: 0}, // explicitly disabled
		{value: "1", want: 1},
		{value: "10", want: 10},
		{value: strconv.Itoa(broadcast.MaxBurstSeconds), want: broadcast.MaxBurstSeconds},
		{value: strconv.Itoa(broadcast.MaxBurstSeconds + 1), wantErr: true},
		{value: "-1", wantErr: true},
		{value: "lots", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			setBaseEnv(t)
			t.Setenv("STATIONCAST_BURST_SECONDS", tc.value)

			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() with burst %q = nil error, want error", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.BurstSeconds != tc.want {
				t.Errorf("BurstSeconds = %d, want %d", cfg.BurstSeconds, tc.want)
			}
		})
	}
}

// Guards the boundary constants against a careless edit, since ffmpeg only
// reports the resulting failure at encoder start time
func TestBitrateBoundsAreSane(t *testing.T) {
	if MinBitrate >= MaxBitrate {
		t.Fatalf("MinBitrate %d >= MaxBitrate %d", MinBitrate, MaxBitrate)
	}
	if _, err := strconv.Atoi(strconv.Itoa(MinBitrate)); err != nil {
		t.Fatal(err)
	}
}
