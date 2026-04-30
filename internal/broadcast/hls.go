package broadcast

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// HLSManager runs an ffmpeg subprocess that consumes MP3 from the hub and
// writes a rolling HLS playlist + segments to disk. The HTTP layer serves
// those files.
type HLSManager struct {
	hub     *Hub
	dir     string
	segDir  string
	playlist string
}

func NewHLSManager(hub *Hub, dataDir string) *HLSManager {
	dir := filepath.Join(dataDir, "hls")
	return &HLSManager{
		hub:      hub,
		dir:      dir,
		segDir:   dir,
		playlist: filepath.Join(dir, "playlist.m3u8"),
	}
}

func (m *HLSManager) PlaylistPath() string { return m.playlist }
func (m *HLSManager) Dir() string          { return m.segDir }

func (m *HLSManager) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := m.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("hls process exited", "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		return
	}
}

func (m *HLSManager) runOnce(ctx context.Context) error {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	pattern := filepath.Join(m.dir, "seg-%05d.ts")

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "warning",
		"-f", "mp3", "-i", "pipe:0",
		"-c:a", "copy",
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "6",
		"-hls_flags", "delete_segments+append_list+omit_endlist+independent_segments",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", pattern,
		m.playlist,
	)
	cmd.Stderr = stderrLogger("hls")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	sub := m.hub.Subscribe()
	defer sub.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	go func() {
		defer stdin.Close()
		for chunk := range sub.Chan() {
			if _, err := stdin.Write(chunk); err != nil {
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func stderrLogger(tag string) io.Writer {
	r, w := io.Pipe()
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				slog.Debug("ffmpeg", "tag", tag, "msg", string(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}()
	return w
}
