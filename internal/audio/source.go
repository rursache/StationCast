package audio

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/rursache/StationCast/internal/playlist"
)

// pcmSource is an io.Reader that always returns PCM data at the requested rate.
// It transparently advances to the next track from the scheduler when the
// current decoder finishes, and emits silence when the library is empty.
type pcmSource struct {
	eng *Engine
	ctx context.Context

	curCmd *exec.Cmd
	curOut io.ReadCloser
	curTrk *playlist.Track
}

func (s *pcmSource) Read(p []byte) (int, error) {
	for {
		if s.ctx.Err() != nil {
			return 0, s.ctx.Err()
		}
		if s.curOut == nil {
			t := s.eng.sched.Pick()
			if t == nil {
				s.eng.sched.MarkPlaying(nil)
				s.eng.hub.SetMetadata(s.eng.cfg.StationName)
				return fillSilence(p), nil
			}
			cmd, out, err := s.startDecoder(t)
			if err != nil {
				slog.Warn("decoder start failed", "track", t.Path, "err", err)
				continue
			}
			s.curCmd = cmd
			s.curOut = out
			s.curTrk = t
			s.eng.mu.Lock()
			s.eng.curCmd = cmd
			s.eng.mu.Unlock()
			s.eng.sched.MarkPlaying(t)
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
			return n, nil
		}
		if err != nil {
			_ = s.curOut.Close()
			_ = s.curCmd.Wait()
			s.eng.mu.Lock()
			s.eng.curCmd = nil
			s.eng.mu.Unlock()
			s.curOut = nil
			s.curCmd = nil
			s.curTrk = nil
			continue
		}
	}
}

func (s *pcmSource) startDecoder(t *playlist.Track) (*exec.Cmd, io.ReadCloser, error) {
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
	cmd.Stderr = stderrLogger("decoder")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, out, nil
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
