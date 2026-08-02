package audio

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rursache/StationCast/internal/playlist"
)

// pcmSource is an io.Reader that always returns PCM data at the requested rate.
// It transparently advances to the next track from the scheduler when the
// current decoder finishes, and emits silence when the library is empty.
type pcmSource struct {
	eng *Engine
	ctx context.Context

	curCmd   *exec.Cmd
	curOut   io.ReadCloser
	curErr   *stderrPipe
	curTrk   *playlist.Track
	curStop  chan struct{} // closed to retire the current track's stall watchdog
	lastRead atomic.Int64  // unix nano of the most recent successful PCM read

	// Track-change timing, reported once the next track's first PCM lands
	changeStart time.Time
	pickDur     time.Duration
	spawnDur    time.Duration
	markDur     time.Duration
	firstAt     time.Time
	awaitFirst  bool
	// set when the current Read already reported a track change in detail
	changeLogged bool
}

// pcmStallWarn is how long a single read may take before it is worth
// reporting. The pump asks for 100ms of audio at a time, so anything much
// beyond that is the encoder sitting idle and every listener draining buffer
const pcmStallWarn = 250 * time.Millisecond

// Read reports any call that takes long enough to eat into listener buffers.
// Every listener shares this one goroutine, so a stall here is a stall for
// the whole station at the same instant, whatever their connection is like
func (s *pcmSource) Read(p []byte) (int, error) {
	start := time.Now()
	// Captured before the call, not after. A read can span a track boundary,
	// and by the time it returns curTrk names the track that just started
	// rather than the one whose decoder actually caused the delay
	track := ""
	if s.curTrk != nil {
		track = s.curTrk.Path
	}
	s.changeLogged = false

	n, err := s.read(p)

	// A track change reports itself in more detail, so do not also emit the
	// generic line for the same stall
	if d := time.Since(start); d > pcmStallWarn && !s.changeLogged {
		slog.Warn("pcm source stalled, listeners drained this much buffer",
			"duration", d.Round(time.Millisecond), "track", track)
	}
	return n, err
}

func (s *pcmSource) read(p []byte) (int, error) {
	for {
		if s.ctx.Err() != nil {
			return 0, s.ctx.Err()
		}
		if s.curOut == nil {
			// Everything from here until the next track's first PCM byte is
			// dead air on the encoder input, so each phase is timed separately
			s.changeStart = time.Now()
			pickAt := s.changeStart
			t := s.eng.sched.Pick()
			s.pickDur = time.Since(pickAt)
			if t == nil {
				s.eng.sched.MarkPlaying(nil)
				s.eng.hub.SetMetadata(s.eng.cfg.StationName)
				return fillSilence(p), nil
			}
			spawnAt := time.Now()
			cmd, out, errPipe, err := s.startDecoder(t)
			s.spawnDur = time.Since(spawnAt)
			if err != nil {
				slog.Warn("decoder start failed", "track", t.Path, "err", err)
				continue
			}
			s.curCmd = cmd
			s.curOut = out
			s.curErr = errPipe
			s.curTrk = t
			s.curStop = make(chan struct{})
			s.lastRead.Store(time.Now().UnixNano())
			go watchStall(s.ctx, &s.lastRead, decoderStallTimeout, decoderStallTick, s.curStop, func() {
				slog.Warn("decoder stalled, killing", "track", t.Path, "after", decoderStallTimeout)
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			})
			s.eng.mu.Lock()
			s.eng.curCmd = cmd
			s.eng.mu.Unlock()
			markAt := time.Now()
			s.eng.sched.MarkPlaying(t)
			s.markDur = time.Since(markAt)
			s.firstAt = time.Now()
			s.awaitFirst = true
			line := t.DisplayLine(s.eng.cfg.StationName)
			s.eng.hub.SetMetadata(line)
			slog.Info("now playing", "id", t.ID, "title", line)
			// Re-query iTunes asynchronously so any stale or wrong artwork
			// gets corrected on the next play. RefreshArt itself throttles
			// per song, so rapid Skip events do not burst the API
			go s.eng.lib.RefreshArt(s.ctx, t)
			// Lazy-fill the track's duration via ffprobe if missing so the
			// admin progress label has a total to render against
			go s.eng.lib.EnsureDuration(t)
		}
		n, err := s.curOut.Read(p)
		if n > 0 {
			s.lastRead.Store(time.Now().UnixNano())
			if s.awaitFirst {
				s.awaitFirst = false
				s.logTrackChange()
			}
			return n, nil
		}
		if err != nil {
			close(s.curStop)
			_ = s.curOut.Close()
			// Reap the decoder process without blocking the PCM pump. A normal
			// EOF + Wait returns immediately, but a process that ignores the
			// closed pipe would otherwise hold up the next track. This only
			// covers reaping after the read has already ended; a decoder that
			// hangs while still alive is handled by watchStall above.
			// Fire-and-forget: exec.CommandContext is bound to s.ctx, so engine
			// shutdown still kills the process
			cmd := s.curCmd
			errPipe := s.curErr
			go func() {
				_ = cmd.Wait()
				// Only safe after Wait: os/exec's stderr copier is still
				// running until then. Without this the reader goroutine
				// leaks once per track played
				_ = errPipe.Close()
			}()
			s.eng.mu.Lock()
			s.eng.curCmd = nil
			s.eng.mu.Unlock()
			s.curOut = nil
			s.curCmd = nil
			s.curErr = nil
			s.curTrk = nil
			s.curStop = nil
			continue
		}
	}
}

// logTrackChange reports how long the encoder went without PCM while
// switching tracks, broken down by phase so the cost is attributable:
// pick covers the scheduler and its queue writes, spawn covers forking
// ffmpeg, mark_playing covers the history and settings writes, and first_pcm
// covers ffmpeg opening the file, probing it and decoding its first samples.
// The total is buffer every listener loses at the same moment
func (s *pcmSource) logTrackChange() {
	s.changeLogged = true
	total := time.Since(s.changeStart)
	firstPCM := time.Since(s.firstAt)
	track := ""
	if s.curTrk != nil {
		track = s.curTrk.Path
	}
	attrs := []any{
		"total", total.Round(time.Millisecond),
		"pick", s.pickDur.Round(time.Millisecond),
		"spawn", s.spawnDur.Round(time.Millisecond),
		"mark_playing", s.markDur.Round(time.Millisecond),
		"first_pcm", firstPCM.Round(time.Millisecond),
		"track", track,
	}
	if total > pcmStallWarn {
		slog.Warn("track change left the encoder without audio", attrs...)
		return
	}
	slog.Info("track change gap", attrs...)
}

func (s *pcmSource) startDecoder(t *playlist.Track) (*exec.Cmd, io.ReadCloser, *stderrPipe, error) {
	// Force ffmpeg to treat the path as a file via the explicit file: protocol
	// prefix. This neutralises any case where a filename could otherwise be
	// misparsed as an option flag
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-i", "file:" + t.Path,
	}
	if filter := buildAudioFilter(s.eng.cfg.ReplayGain, s.eng.cfg.LoudNorm, s.eng.cfg.GainDB); filter != "" {
		args = append(args, "-af", filter)
	}
	args = append(args,
		"-vn",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-ar", fmt.Sprint(sampleRate),
		"-ac", fmt.Sprint(channels),
		"pipe:1",
	)
	cmd := exec.CommandContext(s.ctx, "ffmpeg", args...)
	errPipe := stderrLogger("decoder")
	cmd.Stderr = errPipe
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = errPipe.Close()
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = errPipe.Close()
		return nil, nil, nil, err
	}
	return cmd, out, errPipe, nil
}

func fillSilence(p []byte) int {
	for i := range p {
		p[i] = 0
	}
	return len(p)
}

// buildAudioFilter assembles the ffmpeg -af filter chain.
// Order matters: ReplayGain first (per-track static offset from ID3 tags
// brings every track to a consistent reference), then loudnorm (catches the
// rest with dynamic limiting and handles tracks without RG tags), then the
// user gain boost on top
func buildAudioFilter(replaygain, loudnorm bool, gainDB int) string {
	parts := []string{}
	if replaygain {
		parts = append(parts, "volume=replaygain=track")
	}
	if loudnorm {
		parts = append(parts, "loudnorm=I=-16:LRA=11:TP=-1.5")
	}
	if gainDB != 0 {
		sign := "+"
		if gainDB < 0 {
			sign = ""
		}
		parts = append(parts, fmt.Sprintf("volume=%s%ddB", sign, gainDB))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ",")
}
