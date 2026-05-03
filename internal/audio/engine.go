package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/rursache/StationCast/internal/broadcast"
	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/playlist"
)

const (
	sampleRate    = 44100
	channels      = 2
	bytesPerSec   = sampleRate * channels * 2 // s16le stereo
	pumpChunkMS   = 100
	pumpChunkSize = bytesPerSec / 10
)

type Engine struct {
	cfg   *config.Config
	sched *playlist.Scheduler
	hub   *broadcast.Hub
	lib   *playlist.Library

	mu     sync.Mutex
	curCmd *exec.Cmd
}

func NewEngine(cfg *config.Config, sched *playlist.Scheduler, hub *broadcast.Hub, lib *playlist.Library) *Engine {
	return &Engine{cfg: cfg, sched: sched, hub: hub, lib: lib}
}

func (e *Engine) Skip() {
	e.mu.Lock()
	cmd := e.curCmd
	e.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (e *Engine) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := e.runOnce(ctx)
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		slog.Error("engine loop", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (e *Engine) runOnce(ctx context.Context) error {
	// -flush_packets 1 forces the mp3 muxer to flush every encoded packet
	// (one mp3 frame, ~26ms) instead of batching them. Without this the
	// libmp3lame muxer collects packets internally and emits bursts that
	// Chrome's <audio> jitter buffer cannot smooth over, producing audible
	// stutter. VLC and iOS players buffer enough to mask the burstiness so
	// they sound clean either way
	encCmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "warning",
		"-f", "s16le", "-ar", fmt.Sprint(sampleRate), "-ac", fmt.Sprint(channels), "-i", "pipe:0",
		"-c:a", "libmp3lame", "-b:a", fmt.Sprintf("%dk", e.cfg.Bitrate),
		"-ar", fmt.Sprint(sampleRate), "-ac", fmt.Sprint(channels),
		"-f", "mp3", "-write_xing", "0", "-id3v2_version", "0",
		"-flush_packets", "1",
		"pipe:1",
	)
	encStdin, err := encCmd.StdinPipe()
	if err != nil {
		return err
	}
	encStdout, err := encCmd.StdoutPipe()
	if err != nil {
		return err
	}
	encCmd.Stderr = stderrLogger("encoder")
	if err := encCmd.Start(); err != nil {
		return fmt.Errorf("start encoder: %w", err)
	}
	slog.Info("encoder started", "bitrate", e.cfg.Bitrate)

	encDone := make(chan error, 1)
	go func() { encDone <- encCmd.Wait() }()

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		buf := make([]byte, 4096)
		for {
			n, err := encStdout.Read(buf)
			if n > 0 {
				e.hub.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	pcmCtx, pcmCancel := context.WithCancel(ctx)
	src := &pcmSource{eng: e, ctx: pcmCtx}
	rt := &realtimeWriter{w: encStdin, bytesPerSec: bytesPerSec}

	copyDone := make(chan error, 1)
	go func() {
		_, err := copyChunks(rt, src, pumpChunkSize)
		copyDone <- err
	}()

	select {
	case <-ctx.Done():
		pcmCancel()
		_ = encStdin.Close()
		_ = encCmd.Process.Kill()
		<-encDone
		<-pumpDone
		return ctx.Err()
	case err := <-encDone:
		pcmCancel()
		<-pumpDone
		return fmt.Errorf("encoder exited: %w", err)
	case err := <-copyDone:
		pcmCancel()
		_ = encStdin.Close()
		<-encDone
		<-pumpDone
		return fmt.Errorf("pcm pump exited: %v", err)
	}
}

func copyChunks(dst io.Writer, src io.Reader, chunk int) (int64, error) {
	buf := make([]byte, chunk)
	var total int64
	for {
		n, err := io.ReadFull(src, buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return total, err
		}
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
