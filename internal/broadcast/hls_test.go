package broadcast

import (
	"path/filepath"
	"testing"
)

func TestNewHLSManagerPaths(t *testing.T) {
	dir := t.TempDir()
	m := NewHLSManager(NewHub(128), dir)

	wantDir := filepath.Join(dir, "hls")
	if got := m.Dir(); got != wantDir {
		t.Errorf("Dir = %q, want %q", got, wantDir)
	}
	if got := m.PlaylistPath(); got != filepath.Join(wantDir, "playlist.m3u8") {
		t.Errorf("PlaylistPath = %q", got)
	}
}

// A player may start at the oldest entry in the playlist, which puts it
// hls_time*hls_list_size behind live. A segment leaves the playlist exactly
// that long after it was written, so with the ffmpeg default threshold of 1
// such a player requests a segment at the moment it is being deleted and gets
// a 404. It stalls once, resyncs nearer the live edge and never trips again,
// which is easy to mistake for a server fault
func TestHLSRetentionOutlastsTheWholePlaylistWindow(t *testing.T) {
	windowSeconds := hlsSegmentSeconds * hlsListSize
	graceSeconds := hlsSegmentSeconds * hlsDeleteThreshold

	if graceSeconds <= 0 {
		t.Fatal("no grace period, a player at the oldest entry races the delete")
	}
	// The worst-placed player is a full window behind live. Retention has to
	// cover that plus room for a slow fetch
	totalRetention := windowSeconds + graceSeconds
	if totalRetention <= windowSeconds {
		t.Fatalf("retention %ds does not exceed the %ds playlist window", totalRetention, windowSeconds)
	}
	if graceSeconds < hlsSegmentSeconds*2 {
		t.Errorf("grace period is only %ds, less than two segments of slack", graceSeconds)
	}
}

func TestHLSWindowIsLongEnoughToStartPlayback(t *testing.T) {
	// Players commonly begin three segments from the live edge, so the
	// playlist has to hold more than that
	if hlsListSize < 4 {
		t.Errorf("hls_list_size %d leaves a player nothing to buffer", hlsListSize)
	}
	if hlsSegmentSeconds <= 0 || hlsSegmentSeconds > 10 {
		t.Errorf("hls_time %d is outside the range players expect", hlsSegmentSeconds)
	}
}
