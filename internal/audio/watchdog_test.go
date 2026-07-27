package audio

import (
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

// A decoder that stops producing PCM without exiting used to block
// pcmSource.Read forever, freezing the stream for every listener at once
func TestWatchStallKillsWhenReadsStop(t *testing.T) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())

	killed := make(chan struct{})
	done := make(chan struct{})
	defer close(done)

	go watchStall(context.Background(), &last, 50*time.Millisecond, 5*time.Millisecond, done, func() {
		close(killed)
	})

	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not fire for a decoder that stopped producing")
	}
}

// A healthy decoder keeps reads flowing, and must never be killed
func TestWatchStallLeavesHealthyDecoderAlone(t *testing.T) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())

	killed := make(chan struct{})
	done := make(chan struct{})

	go watchStall(context.Background(), &last, 100*time.Millisecond, 5*time.Millisecond, done, func() {
		close(killed)
	})

	// Keep reporting progress for several timeout windows
	deadline := time.After(500 * time.Millisecond)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-killed:
			t.Fatal("watchdog killed a decoder that was still producing")
		case <-deadline:
			close(done)
			return
		case <-ticker.C:
			last.Store(time.Now().UnixNano())
		}
	}
}

// Track changes retire the watchdog, so a long-finished track cannot have its
// successor killed by a stale watcher
func TestWatchStallStopsWhenDoneClosed(t *testing.T) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())

	killed := make(chan struct{})
	done := make(chan struct{})
	returned := make(chan struct{})

	go func() {
		defer close(returned)
		watchStall(context.Background(), &last, 20*time.Millisecond, 5*time.Millisecond, done, func() {
			close(killed)
		})
	}()

	close(done)

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not return after done was closed")
	}

	// Well past the stall timeout, with last deliberately never updated
	time.Sleep(60 * time.Millisecond)
	select {
	case <-killed:
		t.Fatal("retired watchdog still killed the decoder")
	default:
	}
}

func TestWatchStallStopsOnContextCancel(t *testing.T) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer close(done)
	returned := make(chan struct{})

	go func() {
		defer close(returned)
		watchStall(ctx, &last, time.Hour, 5*time.Millisecond, done, func() {})
	}()

	cancel()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not return after context cancellation")
	}
}

// End to end against a real process that starts, goes silent, and never
// exits, which is the shape of the failure being guarded against
func TestWatchStallKillsRealWedgedProcess(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh unavailable: %v", err)
	}

	cmd := exec.Command("sh", "-c", "sleep 60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	done := make(chan struct{})
	defer close(done)

	go watchStall(context.Background(), &last, 50*time.Millisecond, 5*time.Millisecond, done, func() {
		_ = cmd.Process.Kill()
	})

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("wedged process was not killed by the watchdog")
	}
}
