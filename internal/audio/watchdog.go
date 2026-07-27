package audio

import (
	"context"
	"sync/atomic"
	"time"
)

const (
	// PCM is consumed at a fixed byte rate, so a healthy decoder always has
	// bytes ready. Silence this long means the process is wedged rather than
	// slow: nothing legitimately pauses for seconds mid-track
	decoderStallTimeout = 15 * time.Second
	decoderStallTick    = time.Second
)

// watchStall kills a decoder that stops producing PCM without exiting.
// pcmSource.Read blocks in curOut.Read until the process writes or dies, so a
// process that stays alive and silent (a corrupt file that puts ffmpeg in a
// seek loop, a network mount that hangs) freezes the pump and every listener
// with it. Killing it makes Read return an error, which advances to the next
// track through the normal path.
//
// last holds the unix nano timestamp of the most recent successful read.
// Returns when done is closed or ctx is cancelled
func watchStall(ctx context.Context, last *atomic.Int64, timeout, tick time.Duration, done <-chan struct{}, kill func()) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case now := <-t.C:
			if now.UnixNano()-last.Load() > int64(timeout) {
				kill()
				return
			}
		}
	}
}
