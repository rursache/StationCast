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
	hub      *Hub
	dir      string
	segDir   string
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
	errPipe := stderrLogger("hls")
	cmd.Stderr = errPipe
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = errPipe.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = errPipe.Close()
		return err
	}
	// Both branches below wait on done first, so os/exec's stderr copier has
	// finished by the time this runs
	defer errPipe.Close()

	sub, err := m.hub.SubscribeInternal()
	if err != nil {
		// Nothing to feed the remuxer, so tear the process back down rather
		// than leaving it parked on an stdin that will never see data
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
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

// stderrPipe forwards a subprocess's stderr to the debug log. Close must be
// called once the process has been reaped, otherwise the reader goroutine
// blocks forever: os/exec copies into a non-*os.File Stderr from its own
// goroutine and never closes the writer, so nothing else ever delivers EOF.
// Close waits for the reader to finish, so returning from it means the
// goroutine is gone
type stderrPipe struct {
	w    *io.PipeWriter
	done chan struct{}
}

func (p *stderrPipe) Write(b []byte) (int, error) { return p.w.Write(b) }

func (p *stderrPipe) Close() error {
	err := p.w.Close()
	<-p.done
	return err
}

func stderrLogger(tag string) *stderrPipe {
	r, w := io.Pipe()
	p := &stderrPipe{w: w, done: make(chan struct{})}
	go func() {
		defer close(p.done)
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
	return p
}
